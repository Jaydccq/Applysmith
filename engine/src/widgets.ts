// Clean-room: docs/provenance.md#CR-ENGINE
// Which driver a control needs, read from the fingerprint of its markup: a
// widget library's classes and attributes, never the site's host.

import { isCalendarTrigger } from "./calendar";
import { isDijitSelect, isSdfSelect, isTypeaheadJs } from "./libraries";
import { flatText } from "./dom";
import { byIdDeep, classTokens, reactSelectControl } from "./popup";
import { isSelect2 } from "./select2";
import { toggleGroupOf } from "./toggles";
import { isIcimsDropdown } from "./icims";
import { isLeverLocation } from "./lever";
import { isPctyField, isPctySelect, isRwDropdown } from "./reactwidgets";
import { isSrComponent } from "./sr";
import { isOracleAddress, isOracleSelect } from "./oracle";
import { isSkillsPrompt, isWorkdayPrompt } from "./workday";

export type Driver = "listbox" | "toggle" | "calendar" | "dijit" | "sdf" | "select2" | "typeahead-js" | "workday-prompt" | "date-segments" | "oracle-cx" | "oracle-address" | "icims" | "lever-location" | "sr-component" | "react-widgets" | "pcty-text" | "pcty-select";

const SKIP_TYPES = new Set(["tel", "checkbox", "radio", "file", "hidden", "submit", "button", "reset", "image", "range", "color"]);

/** LinkedIn's location box declares a list of suggestions but no combobox role. */
function isTypeaheadInput(el: Element): boolean {
  return el.getAttribute("data-testid") === "typeahead-input" && /^(list|both)$/i.test(el.getAttribute("aria-autocomplete") ?? "");
}

/** The popup-owning trigger of a listbox: a combobox or an element that declares a listbox popup. */
function isListboxTrigger(el: Element): boolean {
  if (el.matches("[role=listbox], [role=menu], [role=option]")) return false;
  if (classTokens(el).includes("iti__selected-flag")) return false;
  const role = el.getAttribute("role");
  const popup = el.getAttribute("aria-haspopup");
  if (el.localName === "input") {
    const type = (el.getAttribute("type") ?? "text").toLowerCase();
    if (SKIP_TYPES.has(type)) return false;
    return role === "combobox" || popup === "listbox" || isTypeaheadInput(el);
  }
  if (el.localName === "textarea" || el.localName === "select") return false;
  // A Fabric select is a button that names its menu by id and declares a bare popup (aria-haspopup="true").
  return role === "combobox" || popup === "listbox" || (el.localName === "button" && el.hasAttribute("data-menu-id") && popup !== null && popup !== "false");
}

/** The wrapper (ARIA 1.1) that owns an input without a role of its own, if any. */
export function wrapperOwning(input: Element): Element | null {
  if (input.localName !== "input" || input.getAttribute("role")) return null;
  return input.parentElement?.closest("[role=combobox]") ?? null;
}

const WIDGET_TAGS = new Set(["input", "select", "button", "sdf-select-simple"]);

export function driverOf(el: Element): Driver | null {
  if (isSrComponent(el)) return "sr-component";
  if (isRwDropdown(el)) return "react-widgets";
  if (isPctySelect(el)) return "pcty-select";
  if (isPctyField(el) && (el as HTMLInputElement).type !== "checkbox" && (el as HTMLInputElement).type !== "radio") return "pcty-text";
  // Plain structure with no widget attributes is the common case on a big page.
  if (!WIDGET_TAGS.has(el.localName) && !el.hasAttribute("role") && !el.hasAttribute("aria-haspopup") && !el.hasAttribute("widgetid")) return null;
  if (isWorkdayPrompt(el)) return "workday-prompt";
  if (isIcimsDropdown(el)) return "icims";
  if (isLeverLocation(el)) return "lever-location";
  if (isOracleSelect(el)) return "oracle-cx";
  if (isOracleAddress(el)) return "oracle-address";
  if (isSelect2(el)) return "select2";
  if (isTypeaheadJs(el)) return "typeahead-js";
  if (isSdfSelect(el)) return "sdf";
  if (isDijitSelect(el)) return "dijit";
  if (isCalendarTrigger(el)) return "calendar";
  if (toggleGroupOf(el)) return "toggle";
  if (isListboxTrigger(el)) return "listbox";
  return null;
}

/**
 * Read-only, so the engine leaves it alone. A read-only combobox that names its
 * list (Workable's dropdowns) is the exception: a person opens the list and
 * picks, nothing is typed, so the listbox driver reads and writes it. A popup
 * that is not a list (a dialog, a grid) keeps the box read-only, and so does
 * any other driver, whose writer may type.
 */
export function readOnlyBlocks(el: Element, driver: Driver | null): boolean {
  // readonly means nothing to a radio or a checkbox (Eightfold marks its live radios readonly).
  if (!el.hasAttribute("readonly") || (el.localName === "input" && /^(radio|checkbox)$/i.test(el.getAttribute("type") ?? ""))) return false;
  const named = /\S/.test(`${el.getAttribute("aria-controls") ?? ""} ${el.getAttribute("aria-owns") ?? ""}`);
  return !(driver === "listbox" && named && (el.getAttribute("aria-haspopup") ?? "listbox") === "listbox");
}

/** A multi-value control: chips accumulate instead of one value replacing another. */
export function isMultiValue(el: Element): boolean {
  if (isWorkdayPrompt(el)) return isSkillsPrompt(el);
  if (el.getAttribute("aria-multiselectable") === "true" || el.closest("[aria-multiselectable=true]")) return true;
  const control = reactSelectControl(el);
  if (control) {
    const scope = control.parentElement ?? control;
    if (scope.querySelector('[class*="--is-multi"], [class*="__multi-value"]')) return true;
  }
  return el.closest(".select2-container") !== null && !!el.closest(".select2-container--multiple, .select2-selection--multiple");
}

const HINT_CLASS = /(^|[\s_-])(description|desc|help|helper|helptext|hint|subtext)([\s_-]|$)/i;

/** The hint text of a control: what aria-describedby names and any hint block in its own field. */
function hintText(el: Element): string {
  const parts: string[] = [];
  for (const id of (el.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean)) {
    const named = byIdDeep(el, id);
    if (named) parts.push(flatText(named));
  }
  let scope: Element | null = el.parentElement;
  for (let depth = 0; scope && depth < 4; depth++, scope = scope.parentElement) {
    if (scope.querySelectorAll("input:not([type=hidden]), select, textarea").length > 1 && !scope.querySelector("[class*=__control]")) break;
    scope.querySelectorAll("*").forEach((n) => {
      if (HINT_CLASS.test(n.getAttribute("class") ?? "")) parts.push(flatText(n));
    });
  }
  return parts.join(" ");
}

/** "Select up to N" on a multi-value control, from the question or the hint beside it. */
export function selectionLimit(label: string, el?: Element): number | undefined {
  const m = /\b(?:up to|at most|maximum of|max(?:imum)?)\s*(\d+)\b/i.exec(`${label} ${el ? hintText(el) : ""}`);
  return m ? parseInt(m[1] as string, 10) : undefined;
}
