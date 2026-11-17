// "Fill code from Gmail": a button beside each one-time-code input. Mail is
// read only after the person's click, or inside the 210 s window that a
// sign-in run the person started has opened. The application decides what a
// code is; the code is written into the field and kept nowhere else.
import { isRendered } from "../../../engine/src/visibility";
import { h } from "../shared/dom";
import { createLayer, type Layer } from "./layer";
import type { PageChanges } from "./main";
import { writeControl } from "./write";

export const POLL_MS = 1500;
const POLL_LIMIT_MS = 180_000;
export const WINDOW_MS = 210_000;
export const LOOKBACK_MS = 10 * 60_000;

const CSS = `.btn { position: fixed; z-index: 2147483644; } .pop { position: fixed; z-index: 2147483644; width: 260px; padding: 8px 10px; background: var(--bg); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 4px 16px rgba(0,0,0,.25); } .row { display: flex; gap: 6px; margin-top: 6px; justify-content: flex-end; }`;
const WORDING = /otp|one[\s_-]?time|verification[\s_-]?code|security[\s_-]?code|passcode|2fa|auth(entication)?[\s_-]?code|sms[\s_-]?code/i;

export interface CodeMail {
  code: string;
  receivedAt: string;
  from: string;
}

export interface CodeTarget {
  /** One input, or the boxes of a split code in order. */
  inputs: HTMLInputElement[];
}

let windowUntil = 0;
export function openAuthorizationWindow(now = Date.now()): void {
  windowUntil = now + WINDOW_MS;
}
export const windowOpen = (now = Date.now()): boolean => now < windowUntil;

const usable = (i: HTMLInputElement) => !i.disabled && !i.readOnly && isRendered(i) && !i.closest("[data-applysmith]");

/** One-time-code inputs: a single field, or a row of single-digit boxes. */
export function findTargets(doc: Document): CodeTarget[] {
  const inputs = [...doc.querySelectorAll<HTMLInputElement>("input")].filter((i) => ["text", "tel", "number", "password", ""].includes((i.getAttribute("type") ?? "").toLowerCase()) && usable(i));
  const out: CodeTarget[] = [];
  const used = new Set<HTMLInputElement>();
  const byParent = new Map<Element, HTMLInputElement[]>();
  for (const i of inputs) if (i.maxLength === 1 && i.parentElement) byParent.set(i.parentElement, [...(byParent.get(i.parentElement) ?? []), i]);
  for (const boxes of byParent.values()) {
    if (boxes.length >= 4 && boxes.length <= 8) {
      out.push({ inputs: boxes });
      boxes.forEach((b) => used.add(b));
    }
  }
  for (const i of inputs) {
    if (used.has(i) || (i.getAttribute("type") ?? "") === "password" && i.autocomplete !== "one-time-code") continue;
    const own = `${i.name} ${i.id} ${i.placeholder} ${i.getAttribute("aria-label") ?? ""}`;
    if (i.autocomplete === "one-time-code" || WORDING.test(own)) out.push({ inputs: [i] });
  }
  return out;
}

export interface OtpDeps {
  doc: Document;
  win: Window;
  /** The widgets' shared page observer. */
  changes: PageChanges;
  /** Asks for the latest code that arrived after `since`; null while there is none. */
  latest(since: string): Promise<CodeMail | null>;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
}

export interface Otp {
  refresh(): void;
  destroy(): void;
}

const domainOf = (from: string): string => (from.includes("@") ? from.slice(from.lastIndexOf("@") + 1) : from).replace(/[>\s]/g, "");

export function startOtp(deps: OtpDeps): Otp {
  const { doc, win } = deps;
  const now = deps.now ?? Date.now;
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  let layer: Layer | undefined;
  // gone: the widget's field left the page or the widget was destroyed; a request in flight stops at its next look.
  const widgets = new Map<HTMLInputElement, { btn: HTMLButtonElement; target: CodeTarget; busy: boolean; autoRan: boolean; gone: boolean }>();
  let popover: HTMLElement | undefined;

  function place(): void {
    for (const [first, w] of widgets) {
      const r = first.getBoundingClientRect();
      w.btn.style.left = `${Math.max(0, Math.round(r.left))}px`;
      w.btn.style.top = `${Math.round(r.bottom + 4)}px`;
    }
    if (popover) {
      const f = [...widgets.keys()][0]?.getBoundingClientRect();
      if (f) Object.assign(popover.style, { left: `${Math.max(0, Math.round(f.left))}px`, top: `${Math.round(f.bottom + 40)}px` });
    }
  }

  function closePopover(): void {
    popover?.remove();
    popover = undefined;
  }

  async function writeCode(t: CodeTarget, code: string): Promise<string[] | null> {
    const earlier = t.inputs.map((i) => i.value);
    const digits = t.inputs.length > 1 ? [...code] : [code];
    if (t.inputs.length > 1 && digits.length !== t.inputs.length) return null;
    for (let k = 0; k < t.inputs.length; k++) if (!(await writeControl(t.inputs[k]!, digits[k]!)).ok) return null;
    return earlier;
  }

  async function fetchInto(first: HTMLInputElement): Promise<void> {
    const w = widgets.get(first);
    if (!w || w.busy) return;
    w.busy = true;
    w.btn.disabled = true;
    closePopover();
    let code: string | undefined;
    let cancelled = false;
    const say = (text: string, tone: string, extra: Node[] = []) => {
      closePopover();
      popover = h("div", { class: "pop", role: "status" }, h("p", { class: tone }, text), h("div", { class: "row" }, ...extra));
      layer?.box.append(popover);
      place();
    };
    const close = () => {
      code = undefined;
      closePopover();
    };
    try {
      say("Waiting for the code mail.", "muted", [h("button", { type: "button", onclick: () => ((cancelled = true), close()) }, "Cancel")]);
      const since = new Date(now() - LOOKBACK_MS).toISOString();
      const started = now();
      let mail: CodeMail | null = null;
      while (!cancelled && !w.gone && now() - started < POLL_LIMIT_MS) {
        mail = await deps.latest(since);
        if (mail || w.gone) break;
        await sleep(POLL_MS);
      }
      if (cancelled || w.gone) return;
      if (!mail) return say("No unambiguous code arrived in the 10 minutes before your click or in the 3 minutes after.", "bad", [h("button", { type: "button", onclick: close }, "Close")]);
      code = mail.code;
      const earlier = await writeCode(w.target, code);
      if (!earlier) return say("The page did not keep the code.", "bad", [h("button", { type: "button", onclick: close }, "Close")]);
      const when = new Date(mail.receivedAt).toLocaleTimeString(undefined, { timeStyle: "short" });
      const undo = h("button", { type: "button" }, "Undo");
      undo.addEventListener("click", async () => {
        for (let k = 0; k < w.target.inputs.length; k++) await writeControl(w.target.inputs[k]!, earlier[k]!);
        close();
      });
      say(`Code written from ${domainOf(mail.from)}, received ${when}.`, "ok", [undo, h("button", { type: "button", onclick: close }, "Close")]);
    } catch (e) {
      say((e as { message?: string }).message || "The code could not be read.", "bad", [h("button", { type: "button", onclick: close }, "Close")]);
    } finally {
      code = undefined;
      w.busy = false;
      w.btn.disabled = false;
    }
  }

  function forget(): void {
    for (const w of widgets.values()) w.gone = true;
    widgets.clear();
  }

  function refresh(): void {
    const targets = findTargets(doc);
    if (targets.length === 0) {
      layer?.destroy();
      layer = popover = undefined;
      forget();
      return;
    }
    if (layer && !layer.alive()) {
      layer = popover = undefined;
      forget();
    }
    layer ??= createLayer(doc, CSS);
    for (const first of [...widgets.keys()]) if (!targets.some((t) => t.inputs[0] === first)) {
      widgets.get(first)!.btn.remove();
      widgets.get(first)!.gone = true;
      widgets.delete(first);
    }
    for (const t of targets) {
      const first = t.inputs[0]!;
      if (widgets.has(first)) continue;
      const btn = h("button", { class: "btn", type: "button" }, "Fill code from Gmail");
      btn.addEventListener("click", () => void fetchInto(first));
      layer.box.append(btn);
      widgets.set(first, { btn, target: t, busy: false, autoRan: false, gone: false });
    }
    // Inside the window a sign-in run opened, the run's code field is read without a second click.
    if (windowOpen(now())) {
      for (const [first, w] of widgets) {
        if (!w.autoRan) {
          w.autoRan = true;
          void fetchInto(first);
        }
      }
    }
    place();
  }

  const stopWatching = deps.changes.on(refresh, 500);
  win.addEventListener("resize", place);
  win.addEventListener("scroll", place, true);
  refresh();
  return {
    refresh,
    destroy() {
      stopWatching();
      win.removeEventListener("resize", place);
      win.removeEventListener("scroll", place, true);
      layer?.destroy();
      forget();
    },
  };
}
