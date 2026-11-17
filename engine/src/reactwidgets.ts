// Clean-room: docs/provenance.md#CR-ENGINE
// react-widgets (Paylocity): a dropdown list whose options react to the
// library's own click handler only, because a synthetic click never reaches a
// delegated React handler. The handler is called from the page's world by the
// helper. Plain text fields of the same pages take their value through
// onChange and onBlur with an event that carries persist().

import { collapse, fireInput, flatText, nativeValueSetter, normalize } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { focusEl, pointerSequence, press } from "./gestures";
import type { OptionRow } from "./popup";
import { optionsOf } from "./popup";
import { isPlaceholderText } from "./placeholder";
import { selectorFor } from "./selector";
import type { WriteOutcome } from "./writers";

export const isRwDropdown = (el: Element): boolean => el.classList.contains("rw-dropdownlist");

/** What the widget shows as its value. */
export function rwDisplay(root: Element): string {
  const shown = collapse(flatText(root.querySelector(".rw-dropdownlist-value, .rw-input") ?? root, (e) => e.matches(".rw-popup-container, .rw-list")));
  return isPlaceholderText(shown) ? "" : shown;
}

/** The popup of this widget: its own container, inside the wrapper the widget sits in. */
function listOf(root: Element): Element | null {
  const owned = root.getAttribute("aria-owns") ?? root.getAttribute("aria-controls");
  const byId = owned ? root.ownerDocument.getElementById(owned) : null;
  const scope = root.parentElement;
  return byId ?? scope?.querySelector(".rw-popup-container .rw-list, .rw-list") ?? null;
}

const MARK = /[\s*†‡]+$/u;
const bare = (s: string): string => normalize(s.replace(MARK, ""));

/** Exact, then ignoring a trailing required marker, then starts-with either way ending on a word (a unique row): "No" opens neither "None" nor "North Carolina". */
export function pickRwRow(rows: OptionRow[], answer: string): OptionRow | null {
  const a = normalize(answer);
  const tiers: ((r: OptionRow) => boolean)[] = [
    (r) => normalize(r.label) === a,
    (r) => bare(r.label) === bare(answer),
    (r) => {
      const l = bare(r.label);
      const b = bare(answer);
      return l !== "" && b !== "" && (`${l} `.startsWith(`${b} `) || `${b} `.startsWith(`${l} `));
    },
  ];
  for (const hit of tiers) {
    const found = rows.filter((r) => !r.disabled && hit(r));
    if (found.length) return found.length === 1 ? (found[0] as OptionRow) : null;
  }
  return null;
}

export async function writeRwDropdown(root: HTMLElement, answer: string, ctx: WriteCtx): Promise<WriteOutcome> {
  if (!ctx.main) return { ok: false, reason: "binding-unavailable" };
  const main = ctx.main;
  let rows = listOf(root) ? optionsOf(listOf(root) as Element) : [];
  const opened = !rows.length;
  if (opened) {
    // A fresh row has no list until it is opened: the root's own handler opens it, then it is given a moment.
    const clicked = await main.request<{ called: boolean }>("react.call", { selector: selectorFor(root), prop: "onClick", event: {} }).catch(() => ({ called: false }));
    if (!clicked.called) pointerSequence(root);
    await ctx.sleep(ctx.timing.rwOpenMs);
    rows = await waitFor(ctx, () => {
      const l = listOf(root);
      const r = l ? optionsOf(l) : [];
      return r.length ? r : null;
    }, ctx.timing.openWaitMs) ?? [];
  }
  const pick = pickRwRow(rows, answer);
  if (!pick) {
    // The list this write opened is closed again as a person closes it: Escape, through the component's own key handler.
    if (opened) {
      const escape = { key: "Escape", code: "Escape", keyCode: 27, which: 27 };
      const closed = await main.request<{ called: boolean }>("react.call", { selector: selectorFor(root), prop: "onKeyDown", event: escape }).catch(() => ({ called: false }));
      if (!closed.called) press(root, "Escape");
    }
    return { ok: false, reason: "option-not-found", abstain: true };
  }
  const out = await main.request<{ called: boolean; reason?: string }>("react.call", { selector: selectorFor(pick.el), prop: "onClick", event: {} }).catch(() => ({ called: false, reason: undefined }));
  if (out.reason === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  if (!out.called) return { ok: false, reason: "write-not-committed", detail: "stage=handler" };
  const shown = await waitFor(ctx, () => normalize(rwDisplay(root)) === normalize(pick.label) || bare(rwDisplay(root)) === bare(pick.label), ctx.timing.commitWaitMs);
  return shown ? { ok: true, optionLabel: pick.label } : { ok: false, reason: "write-not-committed" };
}

// --- text through the component's handlers --------------------------------------------

/** Paylocity's history fields, by the structure of their ids. */
export const isPctyField = (el: Element): boolean => el.localName === "input" && /^(?:workHistory|educationHistory)\.(?:\d+\.|[A-Za-z]+\.\d+$)/.test(el.getAttribute("id") ?? "");

/**
 * Sets the value natively, then calls the component's onChange and onBlur
 * with an event that has persist() (some handlers throw without it), and
 * verifies only after the settle: an immediate read-back is a false positive.
 */
export async function writePctyText(el: HTMLInputElement, value: string, ctx: WriteCtx): Promise<WriteOutcome> {
  if (!ctx.main) return { ok: false, reason: "binding-unavailable" };
  const sel = selectorFor(el);
  focusEl(el);
  nativeValueSetter(el)?.(value);
  fireInput(el, value, "insertReplacementText");
  for (const prop of ["onChange", "onBlur"]) {
    await ctx.main.request("react.call", { selector: sel, prop, event: {} }).catch(() => undefined);
  }
  await ctx.sleep(ctx.timing.pctyVerifyMs);
  return el.value === value ? { ok: true, optionLabel: value } : { ok: false, reason: "write-not-committed" };
}

// --- pcty-input-select ---------------------------------------------------------------------

/** The input inside Paylocity's own select: it keeps no value of its own, only a display element that exists while the widget is unfocused. */
export const isPctySelect = (el: Element): boolean => el.localName === "input" && el.closest(".pcty-input-select") !== null;

export function pctyDisplay(el: Element): string {
  const root = el.closest(".pcty-input-select");
  const shown = collapse(flatText(root?.querySelector(".input-select-input-single-value") ?? el, () => false));
  return root?.querySelector(".input-select-input-single-value") && !isPlaceholderText(shown) ? shown : "";
}

/**
 * Focus, ArrowDown through the component's own handler (the menu takes about
 * 400 ms), the leaf option's mouse-down handler (its options carry no click
 * handler), then the blur handler and a real blur. The commit is read from the
 * display element, which renders only while the widget is unfocused.
 */
export async function writePctySelect(input: HTMLInputElement, answer: string, ctx: WriteCtx): Promise<WriteOutcome> {
  if (!ctx.main) return { ok: false, reason: "binding-unavailable" };
  const main = ctx.main;
  const root = input.closest(".pcty-input-select") as HTMLElement;
  const call = (selector: string, prop: string, event: Record<string, unknown> = {}) =>
    main.request<{ called: boolean; reason?: string }>("react.call", { selector, prop, event }).catch(() => ({ called: false, reason: undefined }));
  focusEl(input);
  const sel = selectorFor(input);
  if (!(await call(sel, "onKeyDown", { key: "ArrowDown", code: "ArrowDown", keyCode: 40, which: 40 })).called) return { ok: false, reason: "write-not-committed", detail: "stage=open" };
  const rows = await waitFor(ctx, () => {
    const menu = root.querySelector(".input-select-menu") ?? root.parentElement?.querySelector(".input-select-menu");
    const r = menu ? optionsOf(menu) : [];
    return r.length ? r : null;
  }, ctx.timing.pctyMenuMs);
  const pick = rows ? pickRwRow(rows, answer) : null;
  if (!pick) {
    await call(sel, "onBlur");
    input.blur();
    return { ok: false, reason: "option-not-found", abstain: true };
  }
  const leaf = await call(selectorFor(pick.el), "onMouseDown");
  if (leaf.reason === "unsafe-click") {
    await call(sel, "onBlur");
    input.blur();
    return { ok: false, reason: "unsafe-click" };
  }
  await call(sel, "onBlur");
  input.blur();
  if (!leaf.called) return { ok: false, reason: "write-not-committed", detail: "stage=leaf" };
  const shown = await waitFor(ctx, () => normalize(pctyDisplay(input)) === normalize(pick.label) || bare(pctyDisplay(input)) === bare(pick.label), ctx.timing.commitWaitMs);
  return shown ? { ok: true, optionLabel: pick.label } : { ok: false, reason: "write-not-committed" };
}
