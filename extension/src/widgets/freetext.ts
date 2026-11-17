// Which controls are free-text questions, and what they ask.
import { hasLayout } from "../../../engine/src/dom";
import { resolveLabel } from "../../../engine/src/labels";
import { isRendered } from "../../../engine/src/visibility";
import { isEditableRegion } from "./write";

/** Roughly three lines of text. */
const MIN_HEIGHT_PX = 54;
const MIN_ROWS = 3;
/** A one-line box holds a long answer only past this limit: 255 and 256 are column defaults that some systems (Greenhouse) put on every box, names and email included. */
const LONG_INPUT = 256;

const SEARCH = /(^|[\s_-])(search|query|q|find)($|[\s_-])/i;
const CHAT = /(^|[\s_-])(chat|messenger|livechat|intercom|drift|crisp|reply|message-box)($|[\s_-])/i;

function words(el: Element): string {
  return [el.getAttribute("name"), el.getAttribute("id"), el.getAttribute("placeholder"), el.getAttribute("aria-label"), el.getAttribute("class")].filter(Boolean).join(" ");
}

function inChatOrSearch(el: Element): boolean {
  if (el.closest("[role=search], [role=log], [data-applysmith]")) return true;
  if (SEARCH.test(words(el)) || CHAT.test(words(el))) return true;
  for (let n: Element | null = el.parentElement, i = 0; n && i < 6; n = n.parentElement, i++) {
    if (CHAT.test(`${n.id} ${n.className?.toString() ?? ""}`)) return true;
  }
  return false;
}

function tallEnough(el: HTMLElement): boolean {
  if (hasLayout(el.ownerDocument)) return el.getBoundingClientRect().height >= MIN_HEIGHT_PX;
  return el instanceof HTMLTextAreaElement ? el.rows >= MIN_ROWS && el.hasAttribute("rows") : true;
}

/**
 * The detector behind the Answer cards and the Edit with AI button. A box smaller than a few lines gets
 * no Edit with AI button, but on the Answer tab (`anyTextarea`) every textarea is a question, as for
 * the engine's drafts.
 */
export function isFreeTextControl(el: Element, anyTextarea = false): el is HTMLElement {
  if (!(el instanceof HTMLElement) || !isRendered(el)) return false;
  if (el instanceof HTMLTextAreaElement) {
    if (el.disabled || el.readOnly) return false;
  } else if (el instanceof HTMLInputElement) {
    const type = (el.getAttribute("type") ?? "text").toLowerCase();
    if (type !== "text" || el.disabled || el.readOnly || el.maxLength <= LONG_INPUT) return false;
  } else if (!isEditableRegion(el) || el.closest("input, textarea")) {
    return false;
  }
  return !inChatOrSearch(el) && (el instanceof HTMLInputElement || (anyTextarea && el instanceof HTMLTextAreaElement) || tallEnough(el));
}

export function freeTextControls(doc: Document): HTMLElement[] {
  return [...doc.querySelectorAll<HTMLElement>("textarea, input[type=text], input:not([type]), [contenteditable], [role=textbox]")].filter((el) => isFreeTextControl(el));
}

export function questionOf(el: HTMLElement): string {
  return resolveLabel(el).text;
}

export function lengthLimit(el: HTMLElement): number | undefined {
  const max = el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement ? el.maxLength : -1;
  return max > 0 ? max : undefined;
}
