// Clean-room: docs/provenance.md#CR-ENGINE
// Every programmatic click the engine makes passes through here. The engine
// never presses Submit, Withdraw, Discard, Delete, Next, Continue, Review,
// sign-in or sign-up, any Save, Apply or a terminal apply control. The
// exceptions are the Save of an open repeatable-row editor (isEditorSave) and
// the trigger of a list, whose label is its question (isUnsafeListTriggerLabel).

import { collapse, flatText } from "./dom";
import { foldLabel, hasMixedScriptWord, isUnsafeLabel, isUnsafeListTriggerLabel, isUnsafeOptionLabel, isUnsafeToggleLabel, removalLabelRefused } from "./guard-text";
import { labelElements } from "./labels";

/** The text of the elements an aria-labelledby names, and of the aria-labels nested inside the control. */
function referencedNames(el: Element): string[] {
  const doc = el.ownerDocument;
  const named = (el.getAttribute("aria-labelledby") ?? "").split(/\s+/).filter(Boolean).map((id) => (doc.getElementById(id) ? flatText(doc.getElementById(id) as Element) : ""));
  const nested = Array.from(el.querySelectorAll("[aria-label]")).map((n) => n.getAttribute("aria-label") ?? "");
  return [...named, ...nested];
}

/** The parts a control is named by: accessible label, referenced and nested names, text, label elements, title and value. */
function labelParts(el: Element): string[] {
  return [
    el.getAttribute("aria-label"),
    ...referencedNames(el),
    flatText(el),
    ...(el.localName === "input" ? labelElements(el).map((l) => flatText(l)) : []),
    el.getAttribute("title"),
    (el as HTMLInputElement).value ?? el.getAttribute("value"),
  ].filter((p): p is string => typeof p === "string" && p.length > 0);
}

/** Accessible label, text, title and value of a control, joined. */
export function clickLabel(el: Element): string {
  return collapse(labelParts(el).join(" "));
}

/**
 * The guard for a row-removal press. A delete control is allowed only inside
 * the repeatable row (or chip) that owns it, and never when its label also
 * names the application or carries any other refused word.
 */
export function isUnsafeRemoval(label: string, within: Element | null, target: Element): boolean {
  return !within || !within.contains(target) || removalLabelRefused(label);
}

/**
 * The one Save the engine may press: a control every part of whose name is
 * exactly "Save", and which sits inside the open repeatable-row editor itself
 * (never the editor's own root, a section or the page).
 */
export function isEditorSave(el: Element, editor: Element): boolean {
  if (el === editor || !editor.contains(el)) return false;
  const parts = labelParts(el);
  return parts.length > 0 && parts.every((p) => !hasMixedScriptWord(p) && /^save(?: (?:experience|education) entry)?$/.test(foldLabel(p)));
}

export { isUnsafeLabel, isUnsafeListTriggerLabel, isUnsafeOptionLabel };

/** Click the element unless its label names a destructive or terminal action. */
export function safeClick(el: HTMLElement): "ok" | "unsafe-click" {
  if (isUnsafeLabel(clickLabel(el))) return "unsafe-click";
  el.click();
  return "ok";
}

/**
 * A native radio named "Continue" and nothing else (GC AI's one-option notice,
 * "By continuing, you acknowledge the following: ..."): checking it answers the
 * question and advances nothing. A radio's default value "on" is no name.
 */
function isContinueChoice(input: HTMLInputElement): boolean {
  if (input.type !== "radio") return false;
  const parts = labelParts(input).filter((p) => input.hasAttribute("value") || p !== "on");
  return parts.length > 0 && parts.every((p) => !hasMixedScriptWord(p) && foldLabel(p) === "continue");
}

/**
 * A native checkbox named only by the button its statement quotes (Aledade on
 * Lever: "By clicking "Submit Application" I agree to the ... Privacy Policy"
 * over one box "Submit Application"): checking it agrees to the statement and
 * presses nothing. The statement must sit in a container that holds this box
 * and no other control.
 */
function isQuotedButtonAgreement(input: HTMLInputElement): boolean {
  if (input.type !== "checkbox") return false;
  const parts = labelParts(input).filter((p) => input.hasAttribute("value") || p !== "on");
  const names = new Set(parts.map(foldLabel));
  if (parts.length === 0 || names.size !== 1 || parts.some(hasMixedScriptWord)) return false;
  const name = (names.values().next().value as string).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const quoted = new RegExp(`\\bby (?:clicking|selecting|pressing) [^a-z0-9]*${name}[^a-z0-9]* i (?:agree|certify|acknowledge|consent|confirm|understand)\\b`);
  for (let a = input.parentElement, hops = 0; a && hops < 8; a = a.parentElement, hops++) {
    if (Array.from(a.querySelectorAll("input:not([type=hidden]), select, textarea, button")).some((c) => c !== input)) return false;
    if (quoted.test(foldLabel(flatText(a)))) return true;
  }
  return false;
}

/**
 * Click a native checkbox or radio, or the label that activates it, unless the
 * box's names hold a refused word read whole (isUnsafeToggleLabel) and it is
 * neither a radio named only "Continue" nor a box named only by the button its
 * statement quotes.
 */
export function safeToggleClick(input: HTMLInputElement, target: HTMLElement): "ok" | "unsafe-click" {
  if (isUnsafeToggleLabel(clickLabel(input)) && !isContinueChoice(input) && !isQuotedButtonAgreement(input)) return "unsafe-click";
  target.click();
  return "ok";
}

/** A role=checkbox or role=switch widget whose activation only flips it: not a button or input that submits or resets its form. */
function flipsOnly(el: HTMLElement): boolean {
  const role = el.getAttribute("role");
  if (role !== "checkbox" && role !== "switch") return false;
  const formControl = el.localName === "button" || el.localName === "input";
  return !(formControl && /^(submit|reset|image)$/.test((el as HTMLInputElement).type) && !!(el as HTMLInputElement).form);
}

/** Click an ARIA choice: a widget that only flips by the toggle rule, every other one by the click rule. */
export function safeChoiceClick(el: HTMLElement): "ok" | "unsafe-click" {
  if (!flipsOnly(el)) return safeClick(el);
  if (isUnsafeToggleLabel(clickLabel(el))) return "unsafe-click";
  el.click();
  return "ok";
}
