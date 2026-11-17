// Writing text into a page control with the engine's own writers, which commit
// the value the way the page's framework expects and read it back.
import { DEFAULT_TIMING, type Timing, type WriteCtx } from "../../../engine/src/env";
import { writeEditable, writeText } from "../../../engine/src/writers";

export type WriteResult = { ok: boolean };

function context(doc: Document, timing: Timing): WriteCtx {
  return {
    doc,
    timing,
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    checkpoint: async () => {},
    now: () => new Date(),
    log: () => {},
  };
}

/**
 * The longest prefix of the text that fits the limit and ends a sentence; a text
 * with no sentence end inside the limit is cut at the last word.
 */
export function limitAtSentence(text: string, limit: number): string {
  if (limit <= 0 || text.length <= limit) return text;
  const head = text.slice(0, limit);
  let end = -1;
  for (const m of head.matchAll(/[.!?]["')\]]*(?=\s|$)/g)) end = m.index! + m[0].length;
  if (end > 0) return head.slice(0, end).trim();
  const space = head.lastIndexOf(" ");
  return (space > 0 ? head.slice(0, space) : head).trim();
}

export function isEditableRegion(el: Element): boolean {
  const ce = el.getAttribute("contenteditable");
  return ce === "" || ce === "true" || ce === "plaintext-only" || (el.getAttribute("role") === "textbox" && el.getAttribute("aria-multiline") === "true");
}

export function currentText(el: Element): string {
  if (el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement) return el.value;
  return (el.textContent ?? "").trim();
}

/** Writes the text and reports whether the control holds it afterwards. */
export async function writeControl(el: HTMLElement, text: string, timing: Timing = DEFAULT_TIMING): Promise<WriteResult> {
  const ctx = context(el.ownerDocument, timing);
  let outcome;
  if (el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement) outcome = await writeText(el, text, false, ctx);
  else if (isEditableRegion(el)) outcome = await writeEditable(el, text, ctx);
  else return { ok: false };
  return { ok: outcome.ok };
}
