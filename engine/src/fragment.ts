// Clean-room: docs/provenance.md#CR-ENGINE
// A redacted copy of the markup around a control, for diagnostics. It carries
// structure and wording, never a typed value: contact strings become tokens,
// scripts, hidden tokens and editable contents are dropped, and the copy is
// bounded in nodes, depth and bytes.

import { classifySensitivity } from "./sensitive";

export const MAX_FRAGMENT_NODES = 60;
export const MAX_FRAGMENT_DEPTH = 8;
export const MAX_FRAGMENT_BYTES = 4000;

const KEEP_ATTRS = new Set([
  "class",
  "role",
  "type",
  "name",
  "for",
  "placeholder",
  "autocomplete",
  "required",
  "disabled",
  "readonly",
  "maxlength",
  "min",
  "max",
  "aria-label",
  "aria-labelledby",
  "aria-required",
  "aria-expanded",
  "aria-haspopup",
  "aria-controls",
  "aria-checked",
  "aria-pressed",
  "aria-selected",
  "data-automation-id",
]);
const DROP_TAGS = new Set(["script", "style", "svg", "template", "noscript"]);
const EMAIL = /[^\s@<>"']+@[^\s@<>"']+\.[^\s@<>"']+/g;
const PHONE = /\+?\d[\d\s().-]{6,}\d/g;

export interface Fragment {
  html: string;
  /** The copy stopped at a limit and does not hold the whole block. */
  incomplete: boolean;
}

export function maskContacts(text: string): string {
  return text.replace(EMAIL, "[email]").replace(PHONE, "[phone]");
}

const escText = (t: string) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const escAttr = (t: string) => escText(t).replace(/"/g, "&quot;");

/** Cuts on a character boundary so no byte sequence is split. */
export function cutBytes(text: string, max: number): string {
  const enc = new TextEncoder();
  if (enc.encode(text).length <= max) return text;
  let out = "";
  let used = 0;
  for (const ch of text) {
    const n = enc.encode(ch).length;
    if (used + n > max) break;
    out += ch;
    used += n;
  }
  return out;
}

function droppedNode(el: Element): boolean {
  if (DROP_TAGS.has(el.localName)) return true;
  if (el.hasAttribute("hidden") || el.getAttribute("type") === "hidden" || el.getAttribute("type") === "password") return true;
  const ce = el.getAttribute("contenteditable");
  return ce === "" || ce === "true" || ce === "plaintext-only";
}

/** Redacts a subtree. Input values never appear; an option, radio or checkbox keeps its value attribute. */
export function redactedFragment(root: Element): Fragment {
  let nodes = 0;
  let incomplete = false;
  const walk = (el: Element, depth: number): string => {
    if (droppedNode(el)) return "";
    if (nodes >= MAX_FRAGMENT_NODES || depth > MAX_FRAGMENT_DEPTH) {
      incomplete = true;
      return "";
    }
    nodes++;
    const attrs: string[] = [];
    for (const a of Array.from(el.attributes)) {
      if (KEEP_ATTRS.has(a.name)) attrs.push(` ${a.name}="${escAttr(maskContacts(a.value))}"`);
      else if (a.name === "value" && (el.localName === "option" || ["radio", "checkbox"].includes(el.getAttribute("type") ?? ""))) {
        attrs.push(` value="${escAttr(maskContacts(a.value))}"`);
      }
    }
    let inner = "";
    if (el.localName !== "textarea") {
      for (const child of Array.from(el.childNodes)) {
        if (child.nodeType === 3) inner += escText(maskContacts(child.nodeValue ?? ""));
        else if (child.nodeType === 1) inner += walk(child as Element, depth + 1);
      }
    }
    return `<${el.localName}${attrs.join("")}>${inner}</${el.localName}>`;
  };
  const html = walk(root, 0);
  const cut = cutBytes(html, MAX_FRAGMENT_BYTES);
  return { html: cut, incomplete: incomplete || cut.length < html.length };
}

/**
 * The block around a control, redacted; null for a detached control and for a
 * control whose value must never leave the page (password, one-time code, and
 * every other sensitive control).
 */
export function controlSnapshot(el: Element, label = ""): Fragment | null {
  if (!el.isConnected) return null;
  if (classifySensitivity(el, label) !== null) return null;
  const block = el.closest("fieldset, [role=group], [role=radiogroup]") ?? el.parentElement?.parentElement ?? el.parentElement ?? el;
  return redactedFragment(block);
}
