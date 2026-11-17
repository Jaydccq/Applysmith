// Clean-room: docs/provenance.md#CR-ENGINE
// One reader for fill, inspect and answer: what a control currently shows.

import { committedDisplay, isWidgetInput, triggerDisplay } from "./committed";
import { collapse, flatText } from "./dom";
import { icimsDisplay, isIcimsDropdown } from "./icims";
import { isLeverLocation, leverFilled } from "./lever";
import { tinymceEditor, tinymceText } from "./libraries";
import { optionLabel } from "./labels";
import { isPlaceholderText } from "./placeholder";

const DIAL_ONLY = /^\s*\+\d{1,3}\s*$/;


export function optionText(opt: HTMLOptionElement): string {
  // An AngularJS option may carry its text only in the label attribute.
  return collapse(opt.getAttribute("label") || opt.label || opt.textContent || "");
}

/** A valueless first option, a disabled first option, empty text or a placeholder phrase. */
export function isPlaceholderOption(opt: HTMLOptionElement, index: number): boolean {
  if (opt.disabled && index === 0) return true;
  if (index === 0 && opt.value.trim() === "") return true;
  if (opt.value !== "" && opt.value.trim() === "") return true;
  return isPlaceholderText(optionText(opt));
}

export function readSelect(sel: HTMLSelectElement): string {
  const opts = Array.from(sel.options);
  const chosen = opts.filter((o) => o.selected);
  const labels = chosen
    .filter((o) => !isPlaceholderOption(o, opts.indexOf(o)))
    .map(optionText)
    .filter(Boolean);
  return labels.join(", ");
}

export function readGroup(members: HTMLInputElement[]): string {
  const ticked = members.filter((m) => m.checked);
  if (members.length === 1) return ticked.length ? "checked" : "";
  return ticked.map((m) => optionLabel(m)).join(", ");
}

/** The value a control shows. Password inputs are never read. */
export function readElement(el: Element): string {
  const tag = el.localName;
  if (tag === "input") {
    const input = el as HTMLInputElement;
    const type = (input.getAttribute("type") ?? "text").toLowerCase();
    if (type === "password") return "";
    if (type === "file") return input.files?.[0]?.name ?? "";
    if (type === "radio" || type === "checkbox") return input.checked ? optionLabel(input) || "checked" : "";
    if (isWidgetInput(input)) return triggerDisplay(input);
    // The visible box is only a search: the place is chosen once the hidden field holds it.
    if (isLeverLocation(input)) return leverFilled(input) ? input.value : "";
    // A phone box seeded with only a dial code holds no number yet.
    if (type === "tel" && DIAL_ONLY.test(input.value)) return "";
    return input.value.trim() === "" ? "" : input.value;
  }
  if (tag === "textarea") return tinymceEditor(el) ? tinymceText(el as HTMLTextAreaElement) : (el as HTMLTextAreaElement).value;
  if (tag === "select") return isIcimsDropdown(el) ? icimsDisplay(el) || readSelect(el as HTMLSelectElement) : readSelect(el as HTMLSelectElement);
  if (committedDisplay(el) !== null || el.getAttribute("role") === "combobox" || el.getAttribute("aria-haspopup") !== null) return triggerDisplay(el);
  return collapse(flatText(el));
}
