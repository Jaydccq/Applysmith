// Clean-room: docs/provenance.md#CR-ENGINE
// iCIMS dropdowns: a hidden native select that holds only a placeholder, next
// to a custom control and a list of selectable rows. The driver opens the
// custom control, chooses from its rows and checks its display and the
// hidden select. A select stamped as not enabled is a plain native select.

import { collapse, fire, fireKey, fireInput, flatText, nativeValueSetter, normalize } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { hover, pointerSequence } from "./gestures";
import { isPlaceholderText } from "./placeholder";
import { isRendered } from "./visibility";
import type { WriteOutcome } from "./writers";

const ROWS = "li.result-selectable";

/** The wrapper that holds the select, its custom control and its list. */
function holder(select: Element): Element | null {
  const parent = select.parentElement;
  return parent && parent.querySelector("a.dropdown-select") && parent.querySelector("ul.dropdown-results") ? parent : null;
}

export function isIcimsDropdown(el: Element): boolean {
  return el.localName === "select" && el.getAttribute("icimsdropdown-enabled") !== "0" && holder(el) !== null;
}

export const isSearchWidget = (select: Element): boolean => select.getAttribute("icimsdropdown-search") === "1";
const isAjaxWidget = (select: Element): boolean => select.hasAttribute("icimsdropdown-ajax");

export interface IcimsRow {
  el: HTMLElement;
  label: string;
  /** The row's id or data-value: a bank value may be the option's own id. */
  value: string;
}

export function icimsRows(select: Element): IcimsRow[] {
  const root = holder(select);
  if (!root) return [];
  return Array.from(root.querySelectorAll<HTMLElement>(ROWS))
    .map((el) => ({ el, label: collapse(flatText(el)), value: el.getAttribute("data-value") ?? el.getAttribute("id") ?? "" }))
    .filter((r) => r.label !== "" && !isPlaceholderText(r.label));
}

/** What the custom control shows; the placeholder reads empty. */
export function icimsDisplay(select: Element): string {
  const shown = collapse(holder(select)?.querySelector(".dropdown-text")?.textContent ?? "");
  return isPlaceholderText(shown) ? "" : shown;
}

const tokens = (s: string): string[] => normalize(s).split(" ").filter(Boolean);
const withoutQualifier = (s: string): string => s.replace(/\s*\([^)]*\)\s*$/, "");

/**
 * The row an answer names: the exact label; the label, or else the answer,
 * without a trailing parenthetical ("North Carolina (NC)"); a unique boundary prefix or suffix
 * ("No" never matches inside "Latino"); a unique containment; or the row's
 * own value id. The placeholder is never a choice.
 */
export function pickIcimsRow(rows: IcimsRow[], answer: string): IcimsRow | null {
  const a = normalize(answer);
  if (!a) return null;
  const at = tokens(answer);
  const tiers: ((r: IcimsRow) => boolean)[] = [
    (r) => normalize(r.label) === a,
    (r) => normalize(withoutQualifier(r.label)) === a,
    (r) => normalize(r.label) === normalize(withoutQualifier(answer)),
    (r) => {
      const t = tokens(r.label);
      return t.length > at.length && (at.every((x, i) => t[i] === x) || at.every((x, i) => t[t.length - at.length + i] === x));
    },
    (r) => ` ${normalize(r.label)} `.includes(` ${a} `),
    (r) => r.value !== "" && (r.value === answer.trim() || normalize(r.value) === a),
  ];
  for (const hit of tiers) {
    const found = rows.filter(hit);
    if (found.length) return found.length === 1 ? (found[0] as IcimsRow) : null;
  }
  return null;
}

function press(el: HTMLElement): "ok" | "unsafe-click" {
  // The control opens on mouse down; a bare click leaves the list closed.
  hover(el);
  return pointerSequence(el);
}

/** Search widgets list nothing until the bare label is typed and a key goes up (the filter fires on keyup). */
function searchBox(select: Element): HTMLInputElement | null {
  const root = holder(select);
  return root?.querySelector<HTMLInputElement>("input[type=text], input:not([type])") ?? null;
}

export async function writeIcims(select: HTMLSelectElement, answer: string, ctx: WriteCtx): Promise<WriteOutcome> {
  const root = holder(select) as Element;
  const trigger = root.querySelector<HTMLElement>("a.dropdown-select") as HTMLElement;
  if (press(trigger) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const search = isSearchWidget(select);
  if (search) {
    const box = searchBox(select);
    if (!box) return { ok: false, reason: "listbox-not-opened" };
    // The search matches the literal text: "North Carolina (NC)" finds no row "North Carolina".
    const bare = withoutQualifier(answer).trim() || answer;
    nativeValueSetter(box)?.(bare);
    fireInput(box, bare, "insertText");
    fireKey(box, "keyup", bare.slice(-1) || "a");
  }
  const waitMs = search ? ctx.timing.icimsSearchWaitMs : isAjaxWidget(select) ? ctx.timing.icimsAjaxWaitMs : ctx.timing.openWaitMs;
  const pick = await waitFor(ctx, () => pickIcimsRow(icimsRows(select), answer), waitMs, ctx.timing.icimsPollMs);
  if (!pick) {
    // Close without choosing: the list is toggled shut.
    if (isRendered(root.querySelector("ul.dropdown-results") as Element)) press(trigger);
    return { ok: false, reason: "option-not-found", abstain: true };
  }
  if (press(pick.el) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const landed = await waitFor(ctx, () => normalize(icimsDisplay(select)) === normalize(pick.label) || (pick.value !== "" && select.value === pick.value), ctx.timing.commitWaitMs);
  if (!landed) return { ok: false, reason: "write-not-committed" };
  fire(select, "change");
  return { ok: true, optionLabel: pick.label };
}
