// Clean-room: docs/provenance.md#CR-ENGINE
// Phone values follow the field's own wording.

import { collapse, composedAncestors, flatText } from "./dom";
import { isControlEl } from "./labels";

const PHONE_LIKE = /^\+?[\d\s().-]{7,}$/;

export function looksLikePhone(value: string): boolean {
  return PHONE_LIKE.test(value.trim()) && value.replace(/\D/g, "").length >= 7;
}

/** A clean North American number as ten digits, or null for anything else. */
export function nanpDigits(value: string): string | null {
  const t = value.trim();
  const digits = t.replace(/\D/g, "");
  if (t.startsWith("+")) return digits.length === 11 && digits.startsWith("1") ? digits.slice(1) : null;
  if (digits.length === 11 && digits.startsWith("1")) return digits.slice(1);
  return digits.length === 10 ? digits : null;
}

/** Label, placeholder, title, described-by text, and the descriptive text of the nearest single-control container. */
export function phoneHint(el: Element, label: string): string {
  const parts = [label, el.getAttribute("placeholder"), el.getAttribute("title")];
  const root = el.getRootNode() as Document | ShadowRoot;
  for (const id of (el.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean)) {
    const t = root.getElementById?.(id);
    if (t) parts.push(flatText(t));
  }
  let node: Element = el;
  let depth = 0;
  for (const a of composedAncestors(el)) {
    if (++depth > 5) break;
    const controls = Array.from(a.querySelectorAll("input, select, textarea")).filter(
      (c) => (c as HTMLInputElement).type !== "hidden",
    );
    if (controls.length !== 1) break;
    for (const kid of Array.from(a.children)) {
      if (kid === node || kid.contains(node) || isControlEl(kid)) continue;
      const t = flatText(kid, isControlEl);
      if (t) parts.push(t.slice(0, 300));
    }
    node = a;
  }
  return collapse(parts.filter(Boolean).join(" ")).slice(0, 900);
}

const EXAMPLES: { re: RegExp; shape: (d: string) => string }[] = [
  { re: /\(\s*\d{3}\s*\)\s*\d{3}[-\s]\d{4}|\(\s*[x#]{3}\s*\)/i, shape: (d) => `(${d.slice(0, 3)}) ${d.slice(3, 6)}-${d.slice(6)}` },
  { re: /\b\d{3}-\d{3}-\d{4}\b|\b[x#]{3}-[x#]{3}-[x#]{4}\b/i, shape: (d) => `${d.slice(0, 3)}-${d.slice(3, 6)}-${d.slice(6)}` },
  { re: /\b\d{3}\.\d{3}\.\d{4}\b/, shape: (d) => `${d.slice(0, 3)}.${d.slice(3, 6)}.${d.slice(6)}` },
  { re: /\b\d{3} \d{3} \d{4}\b/, shape: (d) => `${d.slice(0, 3)} ${d.slice(3, 6)} ${d.slice(6)}` },
  { re: /(?<![\d-])\d{10}(?![\d-])/, shape: (d) => d },
];

/**
 * Reshape a clean 10-digit North American number to what the hint asks for.
 * E.164 cues win, then worked examples, then prose. Anything else, or no hint,
 * returns the value unchanged.
 */
export function shapePhone(value: string, hint: string): string {
  const d = nanpDigits(value);
  if (!d) return value;
  if (/\be\.?164\b|\+\s*1\b|\+\s*\d{1,3}\s*[#x]|international format|include (the )?country code/i.test(hint)) return `+1${d}`;
  for (const ex of EXAMPLES) if (ex.re.test(hint)) return ex.shape(d);
  if (/\b(no|without) (dashes|spaces|dots|punctuation|special characters|hyphens)\b|digits only|numbers only|only (digits|numbers)/i.test(hint)) return d;
  if (/\bwith dashes\b|\bdashes\b/i.test(hint)) return `${d.slice(0, 3)}-${d.slice(3, 6)}-${d.slice(6)}`;
  return value;
}

/** A tel input that reads typed digits as a calling code wants the full E.164 number. */
export function detectsCountry(el: Element): boolean {
  return el.getAttribute("role") === "combobox" && el.getAttribute("aria-haspopup") === "listbox";
}

/** intl-tel-input in separate-dial-code mode: national digits only for a +1 selector. */
export function separateDialCode(el: Element): string | null {
  const wrap = el.closest(".iti--separate-dial-code, .iti");
  const code = wrap?.querySelector(".iti__selected-dial-code")?.textContent?.trim();
  return code ? code : null;
}
