// Clean-room: docs/provenance.md#CR-ENGINE
// Select2 version 4 selects, single and multiple, including the search-backed
// catalogs: the menu is mounted at the body level and belongs to one
// container, the native select behind it holds the committed value.

import { collapse, fire, flatText, normalize } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { pointerSequence, press, replaceText } from "./gestures";
import { byIdDeep, classTokens, optionsOf, type OptionRow } from "./popup";
import { degreeLevel, levelWord, seedKind } from "./choosers";
import type { Option } from "./scan";
import { scoreOption, type WriteOutcome } from "./writers";

const STATUS_ROW = /^(searching|loading more|no results|please enter|please delete|the results could not|error loading|loading)/i;

export function isSelect2(el: Element): boolean {
  return el.localName === "select" && (classTokens(el).includes("select2-hidden-accessible") || el.hasAttribute("data-select2-id")) && container(el as HTMLSelectElement) !== null;
}

/** The Select2 select whose widget holds an element: the select is the control, its chips and boxes are not. */
export function select2Owning(el: Element): HTMLSelectElement | null {
  const prev = el.closest(".select2-container")?.previousElementSibling;
  return prev && isSelect2(prev) ? (prev as HTMLSelectElement) : null;
}

export function insideSelect2Widget(el: Element): boolean {
  return select2Owning(el) !== null;
}

export function container(sel: HTMLSelectElement): HTMLElement | null {
  const next = sel.nextElementSibling;
  return next && classTokens(next).includes("select2-container") ? (next as HTMLElement) : null;
}

/** The results list of one container: named by its selection, else the single open menu. */
function resultsFor(sel: HTMLSelectElement, selection: Element): Element | null {
  const id = selection.getAttribute("aria-owns") ?? selection.getAttribute("aria-controls");
  if (id) return byIdDeep(selection, id);
  const open = Array.from(sel.ownerDocument.querySelectorAll(".select2-container--open .select2-results"));
  return open.length === 1 ? (open[0] as Element) : null;
}

function realRows(results: Element): OptionRow[] {
  return optionsOf(results).filter((r) => !STATUS_ROW.test(r.label) && !classTokens(r.el).includes("select2-results__message") && !classTokens(r.el).includes("loading-results"));
}

function statusTexts(results: Element): string[] {
  return Array.from(results.querySelectorAll("[class*=select2-results__]"))
    .filter((e) => !e.querySelector("[role=option]"))
    .map((e) => collapse(flatText(e)))
    .filter((t) => STATUS_ROW.test(t));
}

/** A search in flight; an infinite-scroll catalog's "Loading more results" row stays up under a page that has answered. */
function searching(results: Element): boolean {
  return statusTexts(results).some((t) => /^(searching|loading(?! more))/i.test(t));
}

/** What the results list shows, status rows included: a search has answered once this changes. */
function shown(results: Element): string {
  return collapse(flatText(results));
}

type CatalogKind = "degree" | "study" | "plain";

/** A degree or field-of-study catalog, by the words a listbox with the same label is classed by. */
function catalogKind(label: string): CatalogKind {
  const kind = seedKind(label);
  return kind === "degree" || kind === "study" ? kind : "plain";
}

function only(rows: OptionRow[]): OptionRow | null {
  return rows.length === 1 ? (rows[0] as OptionRow) : null;
}

/**
 * Exact normalized text, else the single prefix match. A degree or an area of
 * study the catalog spells differently is then matched the way a native
 * select's options are: the one row of the answer's degree level ("Bachelor of
 * Science (B.S)" is "Bachelor's Degree"), or the one row that is a whole-word
 * part of the answer or holds it ("Applied Mathematics" is "Mathematics").
 * Anything else, and any tie, commits nothing: never the first row.
 */
export function select2Pick(rows: OptionRow[], answer: string, label = ""): OptionRow | null {
  const a = normalize(answer);
  const exact = rows.filter((r) => normalize(r.label) === a);
  if (exact.length) return exact[0] as OptionRow;
  const prefix = only(rows.filter((r) => normalize(r.label).startsWith(`${a} `)));
  if (prefix) return prefix;
  const kind = catalogKind(label);
  if (kind === "degree" && degreeLevel(answer)) return only(rows.filter((r) => degreeLevel(r.label) === degreeLevel(answer)));
  if (kind === "study") return only(rows.filter((r) => scoreOption(r.label, answer) >= 2));
  return null;
}

/** Search terms: the answer, then a degree's level word or an area of study's two longest words. */
export function select2Terms(label: string, answer: string): string[] {
  const kind = catalogKind(label);
  const terms = [answer];
  if (kind === "degree") {
    const level = levelWord(answer);
    if (level) terms.push(level);
  } else if (kind === "study") {
    const words = answer.split(/[^A-Za-z]+/).filter((w) => w.length >= 4).sort((a, b) => b.length - a.length).slice(0, 2);
    terms.push(...words);
  }
  return Array.from(new Set(terms));
}

/** The label of the option the native select holds as chosen that reads as the answer, or "": a multiple select holds several, and its empty first option is a placeholder. */
function selectedAs(sel: HTMLSelectElement, answer: string): string {
  const held = Array.from(sel.selectedOptions ?? [])
    .filter((o) => !(o.value === "" && o.index === 0))
    .map((o) => collapse(o.label || o.textContent || ""));
  return held.find((text) => normalize(text) === normalize(answer)) ?? "";
}

/**
 * The rows a search-backed Select2 shows when opened with nothing typed (its
 * first page), read and closed without choosing. A widget that will not open,
 * or that offers nothing until something is typed, is asked with no options.
 */
export async function readSelect2Options(sel: HTMLSelectElement, ctx: WriteCtx): Promise<Option[]> {
  const box = container(sel);
  const selection = box?.querySelector<HTMLElement>(".select2-selection");
  if (!box || !selection || pointerSequence(selection) === "unsafe-click") return [];
  const results = await waitFor(ctx, () => resultsFor(sel, selection), ctx.timing.openWaitMs);
  const rows = results ? await waitFor(ctx, () => (searching(results) || !realRows(results).length ? null : realRows(results)), ctx.timing.prescanReadMs) : null;
  if (classTokens(box).includes("select2-container--open")) press(box.querySelector<HTMLElement>(".select2-search__field") ?? selection, "Escape");
  return (rows ?? []).map((r) => ({ label: r.label, value: "" }));
}

/** One answer on a single select; each answer in turn on a multiple one, which is filled when any of them was taken. */
export async function writeSelect2(sel: HTMLSelectElement, answers: string[], label: string, ctx: WriteCtx): Promise<WriteOutcome> {
  if (!sel.multiple) return pickAnswer(sel, answers[0] as string, label, ctx);
  const chosen: string[] = [];
  let firstFailure: WriteOutcome | null = null;
  for (const answer of answers) {
    await ctx.checkpoint();
    const r = await pickAnswer(sel, answer, label, ctx);
    if (r.ok) chosen.push(r.optionLabel ?? answer);
    else firstFailure ??= r;
  }
  if (chosen.length) return { ok: true, optionLabel: chosen.join(", ") };
  return firstFailure ?? { ok: false, reason: "option-not-found" };
}

async function pickAnswer(sel: HTMLSelectElement, answer: string, label: string, ctx: WriteCtx): Promise<WriteOutcome> {
  const box = container(sel);
  const selection = box?.querySelector<HTMLElement>(".select2-selection");
  if (!box || !selection) return { ok: false, reason: "driver-unavailable" };
  // A choice the select already holds is not clicked again: a multiple select takes the click as its removal.
  const held = selectedAs(sel, answer);
  if (held) return { ok: true, optionLabel: held };
  if (pointerSequence(selection) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const results = await waitFor(ctx, () => resultsFor(sel, selection), ctx.timing.openWaitMs);
  if (!results) return { ok: false, reason: "listbox-not-opened" };
  const dropdown = results.closest(".select2-dropdown") ?? results.parentElement ?? results;
  // A multi-select searches in the box inline beside its chips, not in the menu.
  const search = dropdown.querySelector<HTMLInputElement>(".select2-search__field") ?? (sel.multiple ? box.querySelector<HTMLInputElement>(".select2-search__field") : null);
  // The open menu's search box holds the focus and takes the key; a menu without one is closed from the selection.
  const closeMenu = async () => {
    if (classTokens(box).includes("select2-container--open")) {
      press(search ?? selection, "Escape");
      await ctx.sleep(ctx.timing.pollMs);
    }
  };
  const pick = () => select2Pick(realRows(results), answer, label);
  let hit: OptionRow | null = null;
  // A short catalog (a degree list) is all in the rows the menu opens with.
  if (catalogKind(label) !== "plain") {
    await waitFor(ctx, () => !searching(results), ctx.timing.searchWaitMs);
    hit = pick();
  }
  for (const term of select2Terms(label, answer)) {
    if (hit || !search) break;
    // An async catalog keeps the old rows up through its debounce: the search
    // has answered once the list changed, or a Searching row came and went.
    const before = shown(results);
    let sawSearching = false;
    replaceText(search, term);
    await waitFor(
      ctx,
      () => {
        if (searching(results)) {
          sawSearching = true;
          return false;
        }
        return pick() !== null || sawSearching || shown(results) !== before;
      },
      ctx.timing.searchWaitMs,
    );
    hit = pick();
  }
  if (!search && !hit) hit = pick();
  const labels = realRows(results).slice(0, 20).map((r) => r.label);
  if (!hit) {
    await closeMenu();
    return { ok: false, reason: "option-not-found", detail: `stage=search labels=${labels.join("|")}` };
  }
  if (pointerSequence(hit.el) === "unsafe-click") {
    await closeMenu();
    return { ok: false, reason: "unsafe-click" };
  }
  const chosen = hit.label;
  const committed = await waitFor(ctx, () => selectedAs(sel, chosen) !== "", ctx.timing.commitWaitMs);
  if (search) fire(search, "blur");
  await closeMenu();
  return committed ? { ok: true, optionLabel: chosen } : { ok: false, reason: "write-not-committed" };
}
