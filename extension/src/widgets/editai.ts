// "Edit with AI": a small button at the corner of each free-text control on an
// application page. Pressing it opens a popover; only its Generate button asks
// for text, and the result goes into the control for that request alone, with
// one step of undo. Nothing is composed without that click.
import { selectorFor } from "../../../engine/src/selector";
import type { ComposePosting, ComposeView, Flags } from "../shared/messages";
import { h } from "../shared/dom";
import { freeTextControls, lengthLimit, questionOf } from "./freetext";
import { createLayer, type Layer } from "./layer";
import type { PageChanges } from "./main";
import { currentText, limitAtSentence, writeControl } from "./write";

const CSS = `
.wrap { position: fixed; inset: 0; pointer-events: none; z-index: 2147483646; }
.spark { position: fixed; pointer-events: auto; display: inline-flex; gap: 4px; align-items: center; padding: 2px 6px; font-size: 12px; border-radius: 12px; }
.spark .label { display: none; } .spark:hover .label, .spark:focus-visible .label { display: inline; }
.pop { position: fixed; pointer-events: auto; width: 280px; padding: 10px; background: var(--bg); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 24px rgba(0,0,0,.3); }
.pop .row { display: flex; gap: 6px; margin-top: 8px; justify-content: flex-end; }
`;
const REPOSITION_MS = 500;
const MAX_INSTRUCTION = 500;

export interface EditAIDeps {
  doc: Document;
  win: Window;
  /** The widgets' shared page observer. */
  changes: PageChanges;
  flags(): Promise<Flags>;
  /** Whether the current address is one that gets the buttons; absent, every page does. */
  applicationPage?(): boolean;
  posting(): Promise<ComposePosting>;
  compose(req: { key: string; question: string; maxLength?: number; posting: ComposePosting; current?: string; instruction?: string }): Promise<ComposeView>;
  /** Cancels the request of a key; closing the popover of a running request sends it. */
  cancel(key: string): Promise<void>;
}

export interface EditAI {
  /** Reads the switches and the page again, then draws or removes the buttons. */
  refresh(): Promise<void>;
  destroy(): void;
}

interface State {
  busy: boolean;
  /** Counts the control's requests: one that is not the latest when it answers was cancelled and writes nothing. */
  seq: number;
  previous?: string;
  message?: string;
  tone?: "ok" | "bad";
}

export function startEditAI(deps: EditAIDeps): EditAI {
  const { doc, win } = deps;
  let layer: Layer | undefined;
  let wrap: HTMLElement | undefined;
  let controls: HTMLElement[] = [];
  let open: HTMLElement | undefined;
  const states = new WeakMap<HTMLElement, State>();
  const buttons = new Map<HTMLElement, HTMLButtonElement>();
  let pop: HTMLElement | undefined;

  const stateOf = (el: HTMLElement): State => states.get(el) ?? (states.set(el, { busy: false, seq: 0 }), states.get(el)!);
  const keyOf = (el: HTMLElement) => `edit:${selectorFor(el)}`;

  function place(): void {
    for (const [el, btn] of buttons) {
      const r = el.getBoundingClientRect();
      btn.style.left = `${Math.max(0, Math.round(r.right - btn.offsetWidth - 6))}px`;
      btn.style.top = `${Math.max(0, Math.round(r.top + 4))}px`;
    }
    if (open && pop) {
      const r = open.getBoundingClientRect();
      pop.style.left = `${Math.max(8, Math.min(win.innerWidth - 296, Math.round(r.right - 280)))}px`;
      pop.style.top = `${Math.min(win.innerHeight - 40, Math.round(r.top + 32))}px`;
    }
  }
  /** Closing the popover of a running request cancels it. */
  function closePopover(): void {
    const st = open && stateOf(open);
    if (open && st?.busy) {
      st.seq++;
      st.busy = false;
      void deps.cancel(keyOf(open)).catch(() => {});
    }
    pop?.remove();
    pop = undefined;
    open = undefined;
  }

  function popover(el: HTMLElement): void {
    // Its own button pressed again leaves the open popover, and a request it runs, as they are.
    if (open === el && pop) return;
    closePopover();
    open = el;
    const st = stateOf(el);
    const instruction = h("input", { type: "text", maxlength: MAX_INSTRUCTION, placeholder: "What should change? Leave empty to draft from your profile.", "aria-label": "Instruction" });
    const status = h("p", { role: "status", class: st.tone === "ok" ? "ok" : "bad" }, st.message ?? "");
    const generate = h("button", { type: "button", disabled: st.busy }, st.busy ? "Generating" : "Generate");
    const undo = h("button", { type: "button", hidden: st.previous === undefined }, "Undo");
    const note = (message: string, tone: "ok" | "bad") => {
      st.message = message;
      st.tone = tone;
      status.textContent = message;
      status.className = tone;
    };
    generate.addEventListener("click", async () => {
      if (st.busy) return;
      const current = currentText(el);
      const text = instruction.value.trim();
      if (text && !current.trim()) return note("Write a few words in the field first, or leave the instruction empty to draft from your profile.", "bad");
      st.busy = true;
      const mine = ++st.seq;
      generate.disabled = true;
      note("", "ok");
      try {
        const out = await deps.compose({
          key: keyOf(el),
          question: questionOf(el) || "Free-text answer",
          maxLength: lengthLimit(el),
          posting: await deps.posting(),
          ...(current.trim() ? { current } : {}),
          ...(text ? { instruction: text } : {}),
        });
        if (st.seq !== mine) return;
        if (out.outcome !== "filled-model" || !out.answer) {
          note("No grounded answer: nothing in your profile or answer bank supports one. The field is unchanged.", "bad");
        } else {
          const limit = lengthLimit(el);
          st.previous = current;
          undo.hidden = false;
          const res = await writeControl(el, limit ? limitAtSentence(out.answer, limit) : out.answer);
          if (res.ok) {
            note("Written to the field. Undo restores the earlier text.", "ok");
          } else {
            st.previous = undefined;
            undo.hidden = true;
            note("The page did not keep the text. The field is unchanged.", "bad");
          }
        }
      } catch (e) {
        if (st.seq === mine) note((e as { message?: string }).message || "The request failed.", "bad");
      } finally {
        if (st.seq === mine) {
          st.busy = false;
          generate.disabled = false;
        }
      }
    });
    undo.addEventListener("click", async () => {
      if (st.previous === undefined) return;
      const res = await writeControl(el, st.previous);
      if (res.ok) {
        st.previous = undefined;
        undo.hidden = true;
        note("Restored the earlier text.", "ok");
      } else note("The page did not take the earlier text back.", "bad");
    });
    const cancel = h("button", { type: "button", onclick: closePopover }, "Close");
    pop = h("div", { class: "pop", role: "dialog", "aria-label": "Edit with AI" }, instruction, status, h("div", { class: "row" }, undo, cancel, generate));
    pop.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key === "Escape") closePopover();
    });
    wrap?.append(pop);
    place();
    instruction.focus();
  }

  function sync(want: HTMLElement[]): void {
    for (const [el, btn] of [...buttons]) {
      if (!want.includes(el)) {
        btn.remove();
        buttons.delete(el);
        if (open === el) closePopover();
      }
    }
    for (const el of want) {
      if (buttons.has(el)) continue;
      const btn = h("button", { class: "spark", type: "button", "aria-label": "Edit with AI" }, "✦", h("span", { class: "label" }, "Edit with AI"));
      btn.addEventListener("click", () => popover(el));
      buttons.set(el, btn);
      wrap!.append(btn);
    }
    place();
  }

  async function refresh(): Promise<void> {
    let flags: Flags;
    try {
      flags = await deps.flags();
    } catch {
      flags = { off: true, editWithAI: false };
    }
    if (flags.off || !flags.editWithAI || (deps.applicationPage && !deps.applicationPage())) {
      closePopover();
      layer?.destroy();
      layer = wrap = undefined;
      buttons.clear();
      return;
    }
    if (layer && !layer.alive()) {
      layer = wrap = pop = open = undefined;
      buttons.clear();
    }
    if (!layer) {
      layer = createLayer(doc, CSS);
      wrap = h("div", { class: "wrap" });
      layer.box.append(wrap);
    }
    controls = freeTextControls(doc);
    sync(controls);
  }

  const stopWatching = deps.changes.on(() => void refresh(), REPOSITION_MS);
  win.addEventListener("scroll", place, true);
  win.addEventListener("resize", place);
  void refresh();

  return {
    refresh,
    destroy() {
      stopWatching();
      win.removeEventListener("scroll", place, true);
      win.removeEventListener("resize", place);
      layer?.destroy();
      layer = undefined;
    },
  };
}
