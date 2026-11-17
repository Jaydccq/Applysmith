// Clean-room: docs/provenance.md#CR-BROWSER-VIEW
// "Point at a field": the person arms the picker, clicks the control that
// autofill missed or got wrong, adds a note and sends a report. While armed the
// picker takes the person's clicks, so the page never sees them; it fills,
// clicks and submits nothing, and a report never carries a field's value.
import { reduceUrl } from "../../../engine/src/frames";
import { resolveLabel } from "../../../engine/src/labels";
import { SHADOW_SEP, selectorFor } from "../../../engine/src/selector";
import { classifySensitivity } from "../../../engine/src/sensitive";
import { h } from "../shared/dom";
import type { FieldReport } from "../shared/messages";
import { createLayer, type Layer } from "./layer";

const ARMED_MS = 60_000;
export const MAX_NOTE = 500;
export const MAX_OPTIONS = 25;

const CONTROL = "input:not([type=hidden]):not([type=submit]):not([type=button]), select, textarea, [contenteditable=''], [contenteditable=true], [role=combobox], [role=listbox], [role=textbox]";
const CSS = `
.bar { position: fixed; left: 50%; top: 12px; transform: translateX(-50%); z-index: 2147483647; padding: 8px 12px; background: var(--bg); border: 1px solid var(--accent); border-radius: 8px; box-shadow: 0 6px 24px rgba(0,0,0,.3); max-width: min(520px, calc(100vw - 24px)); }
.bar .row { display: flex; gap: 6px; margin-top: 6px; justify-content: flex-end; }
.hl { position: fixed; z-index: 2147483646; pointer-events: none; border: 2px solid var(--accent); border-radius: 4px; background: rgba(31,95,191,.12); }
`;

/** The control a click on a control, its label or a wrapper stands for. */
function controlFor(target: Element): HTMLElement | null {
  const label = target.closest("label");
  const owned = label && (label as HTMLLabelElement).control;
  if (owned) return owned as HTMLElement;
  const own = target.closest<HTMLElement>(CONTROL);
  if (own) return own;
  let node: Element | null = target;
  for (let i = 0; node && i < 4; i++, node = node.parentElement) {
    const inside = node.querySelectorAll<HTMLElement>(CONTROL);
    if (inside.length === 1) return inside[0]!;
    if (inside.length > 1) return null;
  }
  return null;
}

function kindOf(el: HTMLElement): string {
  if (el instanceof HTMLSelectElement) return "select";
  if (el instanceof HTMLInputElement && el.type === "radio") return "radio-group";
  if (el instanceof HTMLInputElement && el.type === "checkbox") return "checkbox";
  const role = el.getAttribute("role");
  return role === "combobox" || role === "listbox" ? "custom-listbox" : "text";
}

function optionsOf(el: HTMLElement): string[] {
  if (el instanceof HTMLSelectElement) return [...el.options].map((o) => (o.textContent ?? "").trim()).filter(Boolean).slice(0, MAX_OPTIONS);
  if (el instanceof HTMLInputElement && el.type === "radio" && el.name) {
    const group = [...el.ownerDocument.querySelectorAll<HTMLInputElement>(`input[type=radio]`)].filter((r) => r.name === el.name);
    return group.map((r) => resolveLabel(r).text).filter(Boolean).slice(0, MAX_OPTIONS);
  }
  return [];
}

/** Attributes the server's snippet check allows, and never a value. */
function snippetOf(el: HTMLElement): string {
  const keep = ["id", "name", "type", "role", "placeholder", "autocomplete", "data-testid", "data-automation-id"];
  const attrs = keep.filter((a) => el.hasAttribute(a)).map((a) => ` ${a}="${(el.getAttribute(a) ?? "").replaceAll('"', "&quot;").slice(0, 80)}"`);
  return `<${el.localName}${attrs.join("")}>`;
}

/** The words around a control: its wrapper's text with every control, editable region and option taken out, so never a value. */
function nearbyTextOf(el: HTMLElement): string {
  const wrapper = el.parentElement?.closest("fieldset, [role=group], label, div");
  if (!wrapper) return "";
  const copy = wrapper.cloneNode(true) as Element;
  for (const n of copy.querySelectorAll(`${CONTROL}, option, [contenteditable]`)) n.remove();
  return (copy.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 200);
}

/** The report for a picked control: what it asks and where it is, with no value. */
export function reportFor(el: HTMLElement, note: string, win: Window): FieldReport {
  const label = resolveLabel(el).text || el.getAttribute("aria-label") || el.getAttribute("placeholder") || "";
  const path = selectorFor(el);
  const sensitive = classifySensitivity(el, label) === "withhold";
  const where = reduceUrl(win.location.href);
  return {
    question: label || "(unlabeled field)",
    controlKind: kindOf(el),
    options: sensitive ? [] : optionsOf(el),
    note: note.slice(0, MAX_NOTE),
    tag: el.localName,
    role: el.getAttribute("role") ?? "",
    accessibleName: label,
    path,
    nearbyText: sensitive ? "" : nearbyTextOf(el),
    url: where,
    frameUrl: where,
    shadowDepth: path.split(SHADOW_SEP).length - 1,
    snippet: snippetOf(el),
    inputType: el instanceof HTMLInputElement ? el.type : undefined,
  };
}

export interface PickerDeps {
  doc: Document;
  win: Window;
  /** Delivers the report; rejects with a message the person can read. */
  send(report: FieldReport): Promise<void>;
}

export interface Picker {
  /** Arming again replaces the earlier session. */
  arm(): void;
  cancel(): void;
  isArmed(): boolean;
}

const CONSUMED = ["pointerdown", "pointerup", "mousedown", "mouseup", "click", "auxclick", "dblclick"];

export function createPicker(deps: PickerDeps): Picker {
  const { doc, win } = deps;
  let session: { stop(): void } | undefined;

  // Our own surfaces keep working while armed; a closed root retargets events to its host, an open one is looked through.
  const ours = (t: EventTarget | null): boolean => {
    if (!(t instanceof Element)) return false;
    const root = t.getRootNode();
    return t.closest("[data-applysmith]") !== null || (root instanceof ShadowRoot && root.host.hasAttribute("data-applysmith"));
  };

  function arm(): void {
    session?.stop();
    const layer: Layer = createLayer(doc, CSS);
    const hl = h("div", { class: "hl", hidden: true });
    const message = h("p", { role: "status" }, "Click the field that autofill missed or got wrong. Nothing on the page is changed.");
    const bar = h("div", { class: "bar", role: "dialog", "aria-label": "Point at a field" }, message);
    const cancelBtn = h("button", { type: "button" }, "Cancel");
    const actions = h("div", { class: "row" }, cancelBtn);
    bar.append(actions);
    layer.box.append(hl, bar);
    let picked: HTMLElement | undefined;
    let ended = false;

    const stop = () => {
      if (ended) return;
      ended = true;
      for (const t of CONSUMED) win.removeEventListener(t, consume, true);
      win.removeEventListener("mousemove", move, true);
      win.removeEventListener("keydown", key, true);
      win.removeEventListener("pagehide", stop);
      win.removeEventListener("popstate", stop);
      win.removeEventListener("hashchange", stop);
      clearTimeout(timer);
      layer.destroy();
      if (session === current) session = undefined;
    };
    const current = { stop };
    session = current;
    const say = (text: string, tone = "") => {
      message.textContent = text;
      message.className = tone;
    };

    function consume(e: Event): void {
      if (ours(e.target)) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      if (e.type !== "click" || picked || !(e.target instanceof Element)) return;
      const el = controlFor(e.target);
      if (!el) return say("No field there. Click on a field or its label.", "bad");
      const label = resolveLabel(el).text;
      if (el instanceof HTMLInputElement && el.type === "password") return say("That is a password field. It cannot be reported.", "bad");
      if (classifySensitivity(el, label) === "never") return say("That field holds a secret or a code. It cannot be reported.", "bad");
      picked = el;
      hl.hidden = true;
      // The click is used up; from here the page works normally while the report is written.
      for (const t of CONSUMED) win.removeEventListener(t, consume, true);
      win.removeEventListener("mousemove", move, true);
      form(el);
    }
    function move(e: Event): void {
      if (picked || ours(e.target) || !(e.target instanceof Element)) return;
      const el = controlFor(e.target);
      if (!el) return void (hl.hidden = true);
      const r = el.getBoundingClientRect();
      Object.assign(hl.style, { left: `${r.left}px`, top: `${r.top}px`, width: `${r.width}px`, height: `${r.height}px` });
      hl.hidden = false;
    }
    function key(e: Event): void {
      if ((e as KeyboardEvent).key === "Escape") {
        e.stopImmediatePropagation();
        stop();
      }
    }
    function form(el: HTMLElement): void {
      const note = h("textarea", { rows: 3, maxlength: MAX_NOTE, "aria-label": "What went wrong? (optional)", placeholder: "What went wrong? (optional)" });
      const send = h("button", { type: "button" }, "Send report");
      say(`Report this field: ${resolveLabel(el).text || "(unlabeled field)"}`);
      actions.replaceChildren(cancelBtn, send);
      bar.insertBefore(note, actions);
      send.addEventListener("click", async () => {
        send.disabled = true;
        try {
          await deps.send(reportFor(el, note.value.trim(), win));
          say("Sent. Thank you.", "ok");
          setTimeout(stop, 1200);
        } catch (e) {
          send.disabled = false;
          say((e as { message?: string }).message || "The report could not be sent.", "bad");
        }
      });
    }

    for (const t of CONSUMED) win.addEventListener(t, consume, true);
    win.addEventListener("mousemove", move, true);
    win.addEventListener("keydown", key, true);
    win.addEventListener("pagehide", stop);
    win.addEventListener("popstate", stop);
    win.addEventListener("hashchange", stop);
    cancelBtn.addEventListener("click", stop);
    const timer = setTimeout(() => {
      if (picked) return;
      say("No field was clicked, so the picker was cancelled.", "bad");
      setTimeout(stop, 1500);
    }, ARMED_MS);
  }

  return { arm, cancel: () => session?.stop(), isArmed: () => session !== undefined };
}
