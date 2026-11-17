// Clean-room: docs/provenance.md#CR-ENGINE
// Choices made of buttons and ARIA widgets instead of inputs: Yes/No pill
// pairs, role=radio groups, switches. Their state lives in an attribute, a
// class token or a hidden backing checkbox, and a second click on a selected
// choice can clear it.

import { safeChoiceClick } from "./click-guard";
import { collapse, flatText, normalize } from "./dom";
import type { WriteCtx } from "./env";
import { classTokens, byIdDeep } from "./popup";
import { loneTarget, scoreOption, type WriteOutcome } from "./writers";

const STATE_CLASS = /^_?(active|selected|chosen|checked|on|pressed)(_[a-z0-9]{4,})?$/i;
const HASH = /_[a-z0-9]{4,}$/i;
const CHOICE_SEL = "[role=radio], [role=switch], [role=checkbox], button[aria-pressed], button[aria-checked]";
const NATIVE = new Set(["input", "select", "textarea"]);

/**
 * Text of one choice: its aria-label, its aria-labelledby target, or its own text. A choice named by the
 * question's label and then its own (Workable: "<question> YES") is named by the target inside it.
 */
export function toggleLabel(el: Element): string {
  const aria = collapse(el.getAttribute("aria-label") ?? "");
  if (aria) return aria;
  const named = (el.getAttribute("aria-labelledby") ?? "").split(/\s+/).flatMap((id) => (id ? [byIdDeep(el, id)] : [])).filter((n) => !!n) as Element[];
  const named0 = named.find((n) => el.contains(n)) ?? named[0];
  if (named0) return collapse(flatText(named0));
  return collapse(flatText(el)) || collapse(el.getAttribute("title") ?? "");
}

function backing(el: Element): HTMLInputElement | null {
  const inside = el.querySelector<HTMLInputElement>("input[type=checkbox]");
  if (inside) return inside;
  const next = el.nextElementSibling;
  return next && next.localName === "input" && (next as HTMLInputElement).type === "checkbox" ? (next as HTMLInputElement) : null;
}

/** Whether a choice shows as selected: ARIA state, a state class token (hash suffixes dropped), a Radix data-state, or a backing checkbox (which vouches for Yes only). */
export function toggleOn(el: Element): boolean {
  for (const a of ["aria-pressed", "aria-checked", "aria-selected"]) {
    const v = el.getAttribute(a);
    if (v === "true") return true;
    if (v === "false") return false;
  }
  const ds = el.getAttribute("data-state");
  if (ds === "checked" || ds === "on" || ds === "active") return true;
  if (classTokens(el).some((t) => STATE_CLASS.test(t.replace(HASH, "")) || STATE_CLASS.test(t))) return true;
  const box = backing(el);
  return !!box && box.checked && normalize(toggleLabel(el)) === "yes";
}

/**
 * The answer a web-component radio group publishes for itself, or null when no
 * group host does: a custom element or a radiogroup within six ancestors whose
 * value is a string (its property, else its attribute). ADP's Section 503
 * group reads "" the moment the page clears the answer while the old option
 * still reads aria-checked, so a cleared group outranks its buttons.
 */
function groupValue(el: Element): string | null {
  for (let n = el.parentElement, i = 0; n && i < 6; n = n.parentElement, i++) {
    if (!n.localName.includes("-") && n.getAttribute("role") !== "radiogroup") continue;
    const v = (n as Element & { value?: unknown }).value;
    if (typeof v === "string") return v;
    if (n.localName.includes("-") && n.hasAttribute("value")) return n.getAttribute("value") as string;
  }
  return null;
}

const cleared = (el: Element | undefined): boolean => el?.getAttribute("role") === "radio" && groupValue(el) === "";

export function readToggles(group: HTMLElement[]): string {
  if (cleared(group[0])) return "";
  const on = group.filter(toggleOn);
  if (group.length === 1) return on.length ? "checked" : "";
  return on.map(toggleLabel).join(", ");
}

/** Whether a native control or a labelled yes/no pill: used to decide that two buttons form one question. */
function pillPair(el: HTMLElement): HTMLElement[] | null {
  if (el.localName !== "button" || (el.getAttribute("type") ?? "") !== "button") return null;
  const parent = el.parentElement;
  if (!parent) return null;
  const buttons = Array.from(parent.children).filter((c): c is HTMLElement => c.localName === "button" && c.getAttribute("type") === "button");
  if (buttons.length < 2 || buttons.length > 6) return null;
  const words = buttons.map((b) => normalize(toggleLabel(b)));
  const yesNo = buttons.length === 2 && words.includes("yes") && words.includes("no");
  const proxy = Array.from(parent.children).some((c) => c.localName === "input" && c.getAttribute("tabindex") === "-1");
  return yesNo || proxy ? buttons : null;
}

/**
 * The choices that make up the question this element belongs to, or null when
 * it is not a choice the engine asks about: a radio group is the members of
 * its group, a switch or ARIA checkbox stands alone (but for a Yes and No pair
 * of them), and buttons that hold
 * their state in an attribute are a group when they have a sibling that does.
 */
export function toggleGroupOf(el: Element): HTMLElement[] | null {
  if (NATIVE.has(el.localName) || !(el.matches(CHOICE_SEL) || (el.localName === "button" && el.getAttribute("type") === "button"))) return null;
  const role = el.getAttribute("role");
  if (role === "checkbox") {
    // Yes and No drawn as the two ARIA checkboxes of one group (Rippling) are one Yes/No question.
    const scope = el.closest("[role=group]") ?? el.parentElement;
    const pair = scope ? Array.from(scope.querySelectorAll<HTMLElement>("[role=checkbox]")).filter((r) => !NATIVE.has(r.localName)) : [];
    if (pair.length === 2 && pair.map((p) => normalize(toggleLabel(p))).sort().join(",") === "no,yes") return pair;
  }
  if (role === "switch" || role === "checkbox") return [el as HTMLElement];
  if (role === "radio") {
    const scope = el.closest("[role=radiogroup]") ?? el.parentElement;
    const group = scope ? Array.from(scope.querySelectorAll<HTMLElement>("[role=radio]")).filter((r) => !NATIVE.has(r.localName)) : [];
    return group.length ? group : [el as HTMLElement];
  }
  if (el.hasAttribute("aria-pressed") || el.hasAttribute("aria-checked")) {
    const state = el.hasAttribute("aria-pressed") ? "aria-pressed" : "aria-checked";
    const sibs = Array.from(el.parentElement?.children ?? []).filter((c): c is HTMLElement => c.localName === "button" && c.hasAttribute(state));
    return sibs.length >= 2 ? sibs : null;
  }
  return pillPair(el as HTMLElement);
}

/** A hidden input beside a pill pair that backs it: the page keeps its state in the buttons, so the input is no question. */
export function isPillProxy(input: Element): boolean {
  if (input.localName !== "input" || input.getAttribute("tabindex") !== "-1") return false;
  return Array.from(input.parentElement?.children ?? []).some((c) => c !== input && pillPair(c as HTMLElement) !== null);
}

/** Hidden inputs that back a choice made of buttons: inside a button, or beside the buttons with tabindex -1. They are not questions of their own. */
export function toggleProxies(group: HTMLElement[]): HTMLInputElement[] {
  const out = new Set<HTMLInputElement>();
  for (const g of group) {
    g.querySelectorAll<HTMLInputElement>("input").forEach((i) => out.add(i));
    for (const sib of Array.from(g.parentElement?.children ?? [])) {
      if (sib.localName === "input" && sib.getAttribute("tabindex") === "-1") out.add(sib as HTMLInputElement);
    }
  }
  return Array.from(out);
}

/** Whether the target alone is on, in a group that has not cleared its answer: a cleared group's option can still read as on. */
const taken = (group: HTMLElement[], target: HTMLElement): boolean => !cleared(target) && toggleOn(target) && group.every((g) => g === target || !toggleOn(g));

async function settle(group: HTMLElement[], target: HTMLElement, ctx: WriteCtx): Promise<boolean> {
  for (let i = 0; i < 8; i++) {
    if (taken(group, target)) return true;
    await ctx.sleep(ctx.timing.toggleGapMs);
  }
  return taken(group, target);
}

/** Whether an answer turns a lone choice on (true) or leaves it off (false), by the words a native tick box takes; null when it says neither. */
const loneWant = (group: HTMLElement[], answer: string): boolean | null => (group.length === 1 ? loneTarget(answer, toggleLabel(group[0] as HTMLElement)) : null);

/** The choice an answer names, or null when it names none or more than one. */
export function targetToggle(group: HTMLElement[], answer: string): HTMLElement | null {
  if (group.length === 1) return loneWant(group, answer) === true ? (group[0] as HTMLElement) : null;
  const scored = group.map((g) => ({ g, s: scoreOption(toggleLabel(g), answer) }));
  const top = Math.max(0, ...scored.map((x) => x.s));
  const best = scored.filter((x) => x.s === top && top > 0);
  return best.length === 1 ? (best[0] as { g: HTMLElement }).g : null;
}

/** Whether the group already shows exactly the answer. */
export function togglesHold(group: HTMLElement[], answer: string): boolean {
  if (loneWant(group, answer) === false) return !toggleOn(group[0] as HTMLElement);
  const target = targetToggle(group, answer);
  return !!target && taken(group, target);
}

/**
 * Selects the choice that answers. A selected choice is never clicked again.
 * A switch or role=radio widget is clicked up to the configured count, a gap
 * apart, and reported failed when its state never moves; a switch is only
 * ever turned on.
 */
export async function writeToggles(group: HTMLElement[], answers: string[], ctx: WriteCtx): Promise<WriteOutcome> {
  const answer = answers[0] ?? "";
  if (loneWant(group, answer) === false) {
    const only = group[0] as HTMLElement;
    return toggleOn(only) ? { ok: false, reason: "format-not-accepted" } : { ok: true };
  }
  const target = targetToggle(group, answer);
  if (!target) return { ok: false, reason: group.length === 1 ? "format-not-accepted" : "option-not-found" };
  // A cleared group's stale option is pressed again, so the group takes the answer.
  if (toggleOn(target) && !cleared(target)) {
    if (group.length > 1 && group.some((g) => g !== target && toggleOn(g))) return { ok: false, reason: "write-not-committed" };
    return { ok: true, optionLabel: toggleLabel(target) };
  }
  const clicks = target.getAttribute("role") === "switch" || target.getAttribute("role") === "radio" ? ctx.timing.toggleClicks : 1;
  // A wrapper that owns no handler: the page listens on the inert input inside it.
  const presses = target.getAttribute("role") === "radio" ? (target.querySelector<HTMLElement>("input[type=radio]") ?? target) : target;
  for (let i = 0; i < clicks; i++) {
    if (safeChoiceClick(presses) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
    await ctx.sleep(ctx.timing.toggleGapMs);
    if (toggleOn(target) && !cleared(target)) break;
  }
  return (await settle(group, target, ctx)) ? { ok: true, optionLabel: toggleLabel(target) } : { ok: false, reason: "write-not-committed" };
}
