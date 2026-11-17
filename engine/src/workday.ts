// Clean-room: docs/provenance.md#CR-ENGINE
// Workday search prompts (school, field of study, skills, referral source,
// phone country code) and the questionnaire date. The DOM contract is the
// public data-automation-id vocabulary; the rules are the spec's: every read
// is scoped to the widget's own id, results are waited for, a pick abstains on
// any ambiguity, and a failed search leaves no text behind.

import { cleanOptionLabel } from "./popup";
import { collapse, flatText, normalize, walkElements } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { clearText, pointerSequence, replaceText, safePress } from "./gestures";
import { fireKey } from "./dom";
import { isRendered } from "./visibility";
import type { WriteOutcome } from "./writers";

const AUTO = "data-automation-id";
const NO_ITEMS = /^no items\.?$/i;
/** The most skills committed in one write; each is a server search. */
export const MAX_SKILLS = 8;
/** The virtualized list is scrolled this many steps at most. */
const MAX_SCROLL_STEPS = 60;
/** A list that ignores the typed query is trusted only for the exact label and its qualifier-stripped base. */
const SCROLL_ONLY_TIERS = 2;

const attr = (el: Element, name: string): string => el.getAttribute(name) ?? "";
const auto = (el: Element): string => attr(el, AUTO);

function quoteAttr(v: string): string {
  return v.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

/** A search input of Workday's prompt widget: it has no type, only these markers. */
export function isWorkdayPrompt(el: Element): boolean {
  if (el.localName !== "input") return false;
  return attr(el, "data-uxi-widget-type") === "selectinput" || el.hasAttribute("data-uxi-multiselect-id") || auto(el.parentElement ?? el) === "multiSelectContainer";
}

/** The widget id shared by the input, its container and its prompt. */
export function promptWidgetId(input: Element): string {
  return attr(input, "data-uxi-multiselect-id") || attr(containerOf(input), "data-uxi-multiselect-id") || attr(containerOf(input), "id");
}

function containerOf(input: Element): Element {
  return input.closest(`[${AUTO}="multiSelectContainer"]`) ?? input.parentElement ?? input;
}

/** Skills accumulate chips; every other prompt holds one answer. */
export function isSkillsPrompt(input: Element): boolean {
  const field = input.closest(`[${AUTO}^="formField"]`);
  return /skill/i.test(`${attr(input, "id")} ${attr(input, "aria-label")} ${field ? auto(field) : ""}`);
}

/** School and field of study hold one pill; a different pill there is a parser guess. */
const SINGLE_VALUED = /school|university|field of study|area of study|major/i;
const SUBJECT = /field of study|area of study|major|discipline/i;
const SCHOOL = /school|university|college|institution/i;
/** The one row a school catalog offers for a school it does not hold: "Other", or "Other ... not listed". */
const OTHER_SCHOOL = /^other(?:\b.*\bnot listed\b.*)?$/;

/** The last word of a name of two or more words ("Applied Mathematics" -> "Mathematics"), or "". */
function headNoun(name: string): string {
  const words = name.split(/[\s,/&]+/).filter(Boolean);
  const last = (words[words.length - 1] ?? "").replace(/[^\p{L}]/gu, "");
  return words.length >= 2 && last.length >= 4 ? last : "";
}

export function pillElements(input: Element): HTMLElement[] {
  return Array.from(containerOf(input).querySelectorAll<HTMLElement>(`[${AUTO}="selectedItem"]`));
}

export function pillTexts(input: Element): string[] {
  return pillElements(input).map((p) => cleanOptionLabel(flatText(p))).filter(Boolean);
}

/** The prompts that belong to this widget and no other. */
function promptRoots(doc: Document, widget: string): Element[] {
  if (!widget) return [];
  return Array.from(doc.querySelectorAll(`[data-associated-widget="${quoteAttr(widget)}"]`));
}

export interface PromptRow {
  el: HTMLElement;
  label: string;
  /** The element a pointer sequence commits (or opens) through. */
  leaf: HTMLElement;
  /** The row advertises a sub-menu. */
  branch: boolean;
}

const BRANCH_MARK = `[${AUTO}*="chevron" i], [class*="chevron" i]`;

function rowOf(el: HTMLElement): PromptRow | null {
  const label = cleanOptionLabel(attr(el, "data-automation-label") || flatText(el));
  if (!label || NO_ITEMS.test(label)) return null;
  // Workday wraps the labelled promptOption in its option row, which carries
  // the sub-menu chevron and the node a press goes through.
  const holder = el.closest<HTMLElement>("[role=option]") ?? el;
  const haspopup = attr(holder, "aria-haspopup");
  const branch = (haspopup !== "" && haspopup !== "false") || holder.hasAttribute("aria-expanded") || holder.querySelector(BRANCH_MARK) !== null;
  return { el, label, leaf: holder.querySelector<HTMLElement>(`[${AUTO}="promptLeafNode"]`) ?? el, branch };
}

/** Option rows of this widget's own prompts, in order. */
export function ownRows(doc: Document, widget: string): PromptRow[] {
  const out: PromptRow[] = [];
  for (const root of promptRoots(doc, widget)) {
    const found: HTMLElement[] = [];
    walkElements(root, (e) => {
      // One row per item: an option row that holds a labelled promptOption is
      // read through that label.
      if (auto(e) === "promptOption" || (e.getAttribute("role") === "option" && !e.closest(`[${AUTO}="promptOption"]`) && !e.querySelector(`[${AUTO}="promptOption"]`))) found.push(e as HTMLElement);
    });
    for (const e of found) {
      const r = rowOf(e);
      if (r) out.push(r);
    }
  }
  return out;
}

const sameLabels = (a: string[], b: string[]): boolean => a.length === b.length && a.every((x, i) => x === b[i]);

/** True when the prompt is the responsive full-screen dialog. */
function responsive(doc: Document, widget: string): Element | null {
  for (const root of promptRoots(doc, widget)) {
    const dlg = root.closest('[data-automation-mode="reactresponsive"]') ?? (attr(root, "data-automation-mode") === "reactresponsive" ? root : null);
    if (dlg) return dlg;
  }
  return null;
}

// --- the pick ---------------------------------------------------------------

const tokens = (s: string): string[] => normalize(s).split(" ").filter(Boolean);

function contiguous(hay: string[], needle: string[]): boolean {
  if (!needle.length || needle.length > hay.length) return false;
  for (let i = 0; i + needle.length <= hay.length; i++) if (needle.every((t, j) => hay[i + j] === t)) return true;
  return false;
}

const unique = <T>(xs: T[]): T | null => (xs.length === 1 ? (xs[0] as T) : null);

/** The signs a comparison without punctuation drops, which name another thing: "C++", "C#" and "C" are three skills. */
const marks = (s: string): string => (s.match(/[+#]/g) ?? []).join("");

/** The whole label, case and punctuation aside: "Java" is not "JavaScript", and "C#" is not "C++". */
const sameLabel = (a: string, b: string): boolean => normalize(a) === normalize(b) && marks(a) === marks(b);

/** The same words, each as often, in any order. */
const sameWords = (x: string[], y: string[]): boolean => x.length === y.length && [...x].sort().join(" ") === [...y].sort().join(" ");

/**
 * The one catalog label an answer names, or null. Tiers, in order: the exact
 * label; the label without one trailing qualifier ("Python (Programming
 * Language)"); the same words in another order (NXP files "Computer
 * Engineering" as "Engineering-Computer"); a unique whole-token prefix; a
 * unique containment; a unique " or " category whose alternative the answer
 * contains as whole words. A tier with two candidates ends the search: a wrong
 * school or skill is a false statement, a blank is not. A label whose plus
 * and hash signs are not the answer's is never the answer. `tiers` limits the
 * search to the first n tiers.
 */
export function pickCatalogLabel(labels: string[], answer: string, tiers = 6): string | null {
  const a = tokens(answer);
  if (!a.length) return null;
  const uniq = Array.from(new Set(labels)).filter((l) => marks(l) === marks(answer));
  const hits: ((l: string) => boolean)[] = [
    (l) => normalize(l) === normalize(answer),
    (l) => normalize(l.replace(/\s*\([^)]*\)\s*$/, "")) === normalize(answer),
    (l) => sameWords(tokens(l), a),
    (l) => {
      const t = tokens(l);
      return t.length > a.length && a.every((x, i) => t[i] === x);
    },
    (l) => contiguous(tokens(l), a),
    (l) => /\bor\b/i.test(l) && l.split(/\s+or\s+|[,/]/i).some((alt) => contiguous(a, tokens(alt))),
  ];
  for (const hit of hits.slice(0, tiers)) {
    const matched = uniq.filter(hit);
    if (matched.length) return unique(matched);
  }
  return null;
}

/** The faculty words a degree names: its own words, and the faculty of a B.S., B.A. or B.Eng. abbreviation. */
function facultiesOf(degree: string): string[] {
  const words = tokens(degree);
  if (/\b[bm]\.?\s?sc?\b/i.test(degree)) words.push("science");
  if (/\b[bm]\.?\s?a\b/i.test(degree)) words.push("arts");
  if (/\b[bm]\.?\s?eng\b/i.test(degree)) words.push("engineering");
  return words;
}

/**
 * The one "<Faculty>-<subject>" row whose subject is the answer and whose
 * faculty the row's degree names: NXP files "Mathematics" as both
 * "Arts-Mathematics" and "Science-Mathematics", and a Bachelor of Science
 * takes the second. No degree, or no single such row, is no pick.
 */
export function pickByFaculty(labels: string[], subject: string, degree: string | undefined): string | null {
  if (!degree) return null;
  const faculties = facultiesOf(degree);
  return unique(
    Array.from(new Set(labels)).filter((l) => {
      const m = /^([A-Za-z]+)\s*-\s*(.+)$/.exec(l.trim());
      return m !== null && normalize(m[2] as string) === normalize(subject) && faculties.includes(normalize(m[1] as string));
    }),
  );
}

/**
 * A phone country code row: it carries the dial code as "(+1)" or as the
 * trailing token, never as the prefix of a longer code (+1264), and names the
 * country. The shortest label wins a tie.
 */
export function pickPhoneRow(labels: string[], country: string, dial: string): string | null {
  const name = normalize(country);
  const code = dial.replace(/[^\d]/g, "");
  const carries = (l: string) => new RegExp(`\\(\\+${code}\\)|\\+${code}\\s*$`).test(l.trim());
  const hits = labels.filter((l) => carries(l) && normalize(l.replace(/\(?\+\d+\)?/g, " ")).includes(name));
  if (!hits.length) return null;
  return hits.sort((x, y) => x.length - y.length)[0] ?? null;
}

// --- driving ------------------------------------------------------------------

export interface PromptRequest {
  answers: string[];
  label: string;
  /** Set for a phone country code: rows are chosen by dial code and country name. */
  phoneCountry?: { country: string; dial: string };
  /** A field of study's row degree ("Bachelor of Science"), which names the faculty of a "<Faculty>-<subject>" row. */
  degree?: string;
}

async function pressEnter(input: HTMLInputElement): Promise<"ok" | "unsafe-click"> {
  if (safePress(input, "Enter") === "unsafe-click") return "unsafe-click";
  fireKey(input, "keypress", "Enter");
  return "ok";
}

function closeResponsive(doc: Document, widget: string): void {
  const dlg = responsive(doc, widget);
  if (!dlg) return;
  // The header x is an icon element holding the wd-icon-x glyph, not a button.
  const x = Array.from(dlg.querySelectorAll<HTMLElement>(`[${AUTO}="icon"]`)).find((i) => i.querySelector(".wd-icon-x"));
  if (x) pointerSequence(x);
}

/** Leave nothing behind: no typed query, no open dialog. */
async function bail(input: HTMLInputElement, widget: string, ctx: WriteCtx): Promise<void> {
  if (input.value !== "") clearText(input);
  closeResponsive(ctx.doc, widget);
  await ctx.sleep(ctx.timing.pollMs);
}

async function pressDone(doc: Document, widget: string, ctx: WriteCtx): Promise<void> {
  const dlg = responsive(doc, widget);
  if (!dlg) return;
  await ctx.sleep(ctx.timing.doneMs);
  const buttons = Array.from(dlg.querySelectorAll<HTMLElement>("button")).filter((b) => isRendered(b));
  const done = buttons.find((b) => /^(done|ok)$/i.test(collapse(flatText(b)))) ?? (buttons.length === 1 ? buttons[0] : undefined);
  // The press goes through the click guard: a lone Next, Continue or Save in the dialog is left alone.
  if (done) pointerSequence(done);
}

async function removePill(pill: HTMLElement, input: Element, ctx: WriteCtx): Promise<void> {
  const del = pill.querySelector<HTMLElement>(`[${AUTO}="DELETE_charm"]`) ?? pill;
  // The pill's own delete control says "press delete to clear value"; it is allowed only inside its own pill.
  if (pointerSequence(del, { removeWithin: pill }) === "unsafe-click") return;
  await waitFor(ctx, () => !pill.isConnected || !pillElements(input).includes(pill), ctx.timing.commitWaitMs);
}

/** Every label of the widget's own list, scrolling the virtualized list from the top. */
async function labelsByScrolling(doc: Document, widget: string, ctx: WriteCtx): Promise<string[]> {
  const seen: string[] = [];
  const add = () => ownRows(doc, widget).forEach((r) => !seen.includes(r.label) && seen.push(r.label));
  add();
  for (const root of promptRoots(doc, widget)) {
    const list = root.querySelector<HTMLElement>("[role=listbox]") ?? (root as HTMLElement);
    list.scrollTop = 0;
    const step = Math.max(list.clientHeight, 20);
    for (let i = 0; i < MAX_SCROLL_STEPS; i++) {
      const before = list.scrollTop;
      list.scrollTop = before + step;
      list.dispatchEvent(new Event("scroll", { bubbles: true }));
      await ctx.sleep(ctx.timing.scrollStepMs);
      add();
      // A list that did not move is at its end.
      if (list.scrollTop === before) break;
    }
    list.scrollTop = 0;
    list.dispatchEvent(new Event("scroll", { bubbles: true }));
  }
  return seen;
}

/** The live row for a label, scrolling from the top when the list recycles its nodes. */
async function liveRow(doc: Document, widget: string, label: string, ctx: WriteCtx): Promise<PromptRow | null> {
  const find = () => ownRows(doc, widget).find((r) => r.label === label && r.el.isConnected) ?? null;
  const direct = find();
  if (direct) return direct;
  for (const root of promptRoots(doc, widget)) {
    const list = root.querySelector<HTMLElement>("[role=listbox]") ?? (root as HTMLElement);
    list.scrollTop = 0;
    const step = Math.max(list.clientHeight, 20);
    for (let i = 0; i < MAX_SCROLL_STEPS; i++) {
      list.scrollTop += step;
      list.dispatchEvent(new Event("scroll", { bubbles: true }));
      await ctx.sleep(ctx.timing.scrollStepMs);
      const hit = find();
      if (hit) return hit;
    }
  }
  return null;
}

/** On a pick miss, the catalog labels that share a word with the answer, most shared words first; all of them when none do. */
function relatedLabels(labels: string[], answer: string): string[] {
  const words = answer.toLowerCase().split(/\W+/).filter((w) => w.length > 2);
  const shared = (l: string) => words.filter((w) => l.toLowerCase().includes(w)).length;
  const related = labels.filter((l) => shared(l) > 0).sort((a, b) => shared(b) - shared(a));
  return (related.length ? related : labels).slice(0, 15);
}

const fail = (reason: "option-not-found" | "write-not-committed" | "unsafe-click", stage: string, abstain = false, labels?: string[]): WriteOutcome => ({
  ok: false,
  reason,
  abstain: abstain || undefined,
  detail: labels ? `stage=${stage} labels=${labels.join("|")}` : `stage=${stage}`,
});

async function commitOne(input: HTMLInputElement, widget: string, answer: string, req: PromptRequest, ctx: WriteCtx, search = answer): Promise<WriteOutcome> {
  const doc = ctx.doc;
  const query = req.phoneCountry ? req.phoneCountry.country : search;
  // A narrower search also takes the row that is exactly what it searched.
  const choose = (labels: string[]) =>
    req.phoneCountry
      ? pickPhoneRow(labels, req.phoneCountry.country, req.phoneCountry.dial)
      : (pickCatalogLabel(labels, answer) ?? pickByFaculty(labels, answer, req.degree) ?? (search !== answer ? (pickCatalogLabel(labels, search, 2) ?? pickByFaculty(labels, search, req.degree)) : null));
  // A pill the pick tiers accept already holds the answer ("Carver University" holds "Carver") and is not
  // selected again: a click toggles it off. A skill is held only by a chip with its whole label: beside
  // "React Native" the applicant's "React" is still to be added.
  const holding = isSkillsPrompt(input) ? (pillTexts(input).find((p) => sameLabel(p, answer)) ?? null) : choose(pillTexts(input));
  if (holding) return { ok: true, optionLabel: holding };

  const before = ownRows(doc, widget).map((r) => r.label);
  const held = pillElements(input);
  const pillsBefore = pillTexts(input).length;
  replaceText(input, query);
  if ((await pressEnter(input)) === "unsafe-click") {
    await bail(input, widget, ctx);
    return fail("unsafe-click", "enter");
  }
  // Results arrive after the server answers; the previous query's rows are still mounted until then.
  // A tenant that commits the single match on Enter shows a new pill instead.
  const after = await waitFor(ctx, () => {
    if (pillTexts(input).length > pillsBefore) return "pill" as const;
    const now = ownRows(doc, widget).map((r) => r.label);
    return now.length && !sameLabels(now, before) ? now : null;
  }, ctx.timing.promptWaitMs, ctx.timing.promptPollMs);
  // School and field of study hold one pill: a different one (a parser guess, an earlier row) is removed
  // only once the row to commit is chosen, so a failed search never takes a correct pill away.
  const keepOnly = async (label: string): Promise<void> => {
    if (!SINGLE_VALUED.test(req.label) || isSkillsPrompt(input)) return;
    for (const pill of pillElements(input)) if (normalize(cleanOptionLabel(flatText(pill))) !== normalize(label)) await removePill(pill, input, ctx);
  };
  if (after === "pill") {
    await ctx.sleep(ctx.timing.pillRecheckMs);
    const fresh = pillElements(input).filter((p) => !held.includes(p)).pop();
    const shown = fresh ? cleanOptionLabel(flatText(fresh)) : "";
    if (!fresh || !shown) {
      await bail(input, widget, ctx);
      return fail("write-not-committed", "pill-vanished");
    }
    // The tenant chose the row itself: it is the answer only when the pick would have chosen it too.
    if (!choose([shown])) {
      await removePill(fresh, input, ctx);
      await bail(input, widget, ctx);
      return fail("option-not-found", "enter-committed-another", true, [shown]);
    }
    await keepOnly(shown);
    if (input.value !== "") clearText(input);
    return { ok: true, optionLabel: shown };
  }
  let label: string | null;
  let seen: string[] = [];
  if (after) {
    label = choose(after);
    seen = after;
    if (!label) {
      seen = await labelsByScrolling(doc, widget, ctx);
      label = choose(seen);
    }
  } else {
    // Rows that never changed are either the previous query's or a list that only scrolls. Only a name that is
    // spelled exactly is taken from them, so a stale list can never supply a near miss.
    label = req.phoneCountry ? null : pickCatalogLabel(await labelsByScrolling(doc, widget, ctx), answer, SCROLL_ONLY_TIERS);
    if (!label) {
      await bail(input, widget, ctx);
      return fail("option-not-found", "search-returned-no-options", true);
    }
  }
  if (!label) {
    await bail(input, widget, ctx);
    return fail("option-not-found", "no-unambiguous-option", true, relatedLabels(seen, answer));
  }

  await keepOnly(label);

  // Some tenants commit the single match on Enter: the pill is already there.
  const committed = () => pillTexts(input).some((p) => sameLabel(p, label as string));
  if (!committed()) {
    const row = await liveRow(doc, widget, label, ctx);
    if (!row) {
      await bail(input, widget, ctx);
      return fail("write-not-committed", "option-vanished");
    }
    if (pointerSequence(row.leaf) === "unsafe-click") {
      await bail(input, widget, ctx);
      return fail("unsafe-click", "leaf");
    }
  }
  await pressDone(doc, widget, ctx);
  const landed = await waitFor(ctx, committed, ctx.timing.commitWaitMs);
  if (landed) await ctx.sleep(ctx.timing.pillRecheckMs);
  if (!landed || !committed()) {
    await bail(input, widget, ctx);
    return fail("write-not-committed", "no-pill");
  }
  if (input.value !== "") clearText(input);
  return { ok: true, optionLabel: label };
}

/**
 * Fills a search prompt: types the answer, presses Enter to run the search,
 * reads only this widget's options, picks one unambiguously and verifies the
 * selected pill. Skills are committed one chip at a time, at most eight; the
 * write succeeds when at least one chip landed.
 */
export async function writeWorkdayPrompt(input: HTMLInputElement, req: PromptRequest, ctx: WriteCtx): Promise<WriteOutcome> {
  const widget = promptWidgetId(input);
  const multi = isSkillsPrompt(input);
  const answers = (multi ? req.answers : req.answers.slice(0, 1)).slice(0, MAX_SKILLS);
  const done: string[] = [];
  let first: WriteOutcome | null = null;
  for (const a of answers) {
    await ctx.checkpoint();
    let out = await commitOne(input, widget, a, req, ctx);
    // A subject catalog files "Applied Mathematics" under "Mathematics": when
    // the whole name finds nothing, its head noun is searched, and the rows it
    // returns are still judged against the whole name.
    const head = SUBJECT.test(req.label) ? headNoun(a) : "";
    if (!out.ok && out.abstain && head) {
      const narrow = await commitOne(input, widget, a, req, ctx, head);
      out = narrow.ok ? narrow : { ...out, detail: `${out.detail ?? ""}; head ${head}: ${narrow.detail ?? ""}` };
    }
    if (out.ok) done.push(out.optionLabel ?? a);
    else first ??= out;
  }
  if (done.length) return { ok: true, optionLabel: done.join(", ") };
  // A school the catalog does not hold at all is the catalog's own "Other" row (Ensemble has no
  // University of Westbridge); a pill that is not that row is taken back off.
  if (!multi && first?.abstain && first.detail?.includes("stage=search-returned-no-options") && SCHOOL.test(req.label) && !SUBJECT.test(req.label)) {
    const other = await commitOne(input, widget, "Other", req, ctx);
    if (other.ok && OTHER_SCHOOL.test(normalize(other.optionLabel ?? ""))) return other;
    if (other.ok) for (const pill of pillElements(input)) if (normalize(cleanOptionLabel(flatText(pill))) === normalize(other.optionLabel ?? "")) await removePill(pill, input, ctx);
  }
  return first ?? fail("option-not-found", "no-answer", true);
}

// --- hierarchical menus -------------------------------------------------------

const BACK = /^back$/i;

function backButton(doc: Document, widget: string): HTMLElement | null {
  for (const root of promptRoots(doc, widget)) {
    // Workday's way back is the sub-menu's header: an arrow icon beside the
    // category's title, clickable as a whole.
    const header = Array.from(root.querySelectorAll<HTMLElement>(`[${AUTO}="multiSelectHeader"]`)).find(
      (h) => isRendered(h) && (attr(h, "data-uxi-multiselectlistheader-clickable") === "true" || h.querySelector(`[${AUTO}="backButton"]`) !== null),
    );
    if (header) return header;
    const b = Array.from(root.querySelectorAll<HTMLElement>("button, [role=button]")).find((x) => isRendered(x) && (BACK.test(collapse(flatText(x))) || BACK.test(attr(x, "aria-label"))));
    if (b) return b;
  }
  return null;
}

/** A referral menu is fetched a level at a time: its drill may take this many field budgets. */
export const MENU_BUDGET_FACTOR = 4;

/**
 * A menu of categories and leaves (the referral source): opened by a pointer
 * sequence, and drilled only through rows that advertise a sub-menu, two
 * levels deep, back by the visible back button. A plain row is a leaf and is
 * clicked only when it is the answer.
 */
/**
 * The answer may name a category ("Internet Job Board / Job Search", the
 * check's choice from the menu's categories): a category that holds one choice
 * is answered by it. When no leaf is the answer, onMenu receives the top
 * level's labels, so the check can choose among what the menu holds.
 */
export async function drillWorkdayMenu(input: HTMLInputElement, answers: string[], ctx: WriteCtx, onMenu?: (labels: string[]) => void): Promise<WriteOutcome> {
  const doc = ctx.doc;
  const widget = promptWidgetId(input);
  const answer = answers[0] as string;
  if (pointerSequence(input) === "unsafe-click") return fail("unsafe-click", "open");
  const top = await waitFor(ctx, () => (ownRows(doc, widget).length ? ownRows(doc, widget) : null), ctx.timing.openWaitMs);
  if (!top) return fail("option-not-found", "menu-not-opened");
  const finish = async (row: PromptRow): Promise<WriteOutcome> => {
    // A pill the prompt held before the click proves nothing: the leaf's own label, or a pill that was not there, does.
    const held = pillTexts(input);
    if (pointerSequence(row.leaf) === "unsafe-click") return fail("unsafe-click", "leaf");
    await pressDone(doc, widget, ctx);
    const ok = await waitFor(ctx, () => pillTexts(input).some((p) => sameLabel(p, row.label) || !held.includes(p)), ctx.timing.commitWaitMs);
    return ok ? { ok: true, optionLabel: row.label } : fail("write-not-committed", "no-pill");
  };
  const leaf = (rows: PromptRow[]): PromptRow | null => {
    const label = pickCatalogLabel(rows.filter((r) => !r.branch).map((r) => r.label), answer);
    return rows.find((r) => !r.branch && r.label === label) ?? null;
  };
  // What each level held, for the row when no leaf is the answer.
  const seen: string[] = [];
  const note = (line: string) => {
    if (!seen.includes(line)) seen.push(line);
  };
  // Leave the drill's budget room to report: a category not reached is named.
  const stopAt = Date.now() + MENU_BUDGET_FACTOR * ctx.timing.fieldBudgetMs - 3 * ctx.timing.openWaitMs;
  // Workday fetches a sub-menu, and the way back, from its server: each is
  // waited for as long as a search. A level below the top shows its way back;
  // the top shows none. Labels alone do not tell them apart: the top is drawn
  // a few rows at a time and lists each item twice.
  const wait = ctx.timing.searchWaitMs;
  const atTop = (): PromptRow[] | null => {
    const rows = ownRows(doc, widget);
    return rows.length && !backButton(doc, widget) ? rows : null;
  };
  const inside = (): PromptRow[] | null => {
    const rows = ownRows(doc, widget);
    return rows.length && backButton(doc, widget) ? rows : null;
  };
  // A category the server answers with "No Items." is empty once that answer
  // has stood for a moment: the full wait is for a sub-menu still on its way.
  let emptySince = 0;
  const insideOrEmpty = (): PromptRow[] | null => {
    const rows = inside();
    if (rows) return rows;
    const empty = !!backButton(doc, widget) && promptRoots(doc, widget).some((r) => Array.from(r.querySelectorAll(`[${AUTO}="promptOption"]`)).some((e) => NO_ITEMS.test(flatText(e).trim())));
    if (!empty) emptySince = 0;
    else if (!emptySince) emptySince = Date.now();
    else if (Date.now() - emptySince >= ctx.timing.openWaitMs) return [];
    return null;
  };
  const visited = new Set<string>();
  const topLabels: string[] = [];
  for (let n = 0; n <= 12; n++) {
    await ctx.checkpoint();
    // The top is read again on every pass: the site re-renders it on every
    // level change, so only the row on screen now is pressed.
    let rows = n === 0 ? top : atTop();
    if (!rows) {
      const back = backButton(doc, widget);
      if (back) pointerSequence(back);
      rows = await waitFor(ctx, atTop, wait);
      if (!rows) {
        note("(the top level did not come back)");
        break;
      }
    }
    const direct = leaf(rows);
    if (direct) return finish(direct);
    for (const r of rows) {
      if (!r.branch) note(r.label);
      if (!topLabels.includes(r.label)) topLabels.push(r.label);
    }
    // The category the answer names is visited first.
    const named = pickCatalogLabel(rows.filter((r) => r.branch).map((r) => r.label), answer);
    const next = (rs: PromptRow[] | null) =>
      rs?.find((r) => r.branch && r.label === named && !visited.has(r.label)) ?? rs?.find((r) => r.branch && !visited.has(r.label)) ?? null;
    // The rest of the top may still be drawing: the list ends only when no
    // unvisited category comes.
    const category = next(rows) ?? (await waitFor(ctx, () => next(atTop()), ctx.timing.openWaitMs));
    if (!category) break;
    visited.add(category.label);
    if (Date.now() > stopAt) {
      note(`${category.label} > (not reached in time)`);
      continue;
    }
    if (pointerSequence(category.leaf) === "unsafe-click") continue;
    emptySince = 0;
    const inner = await waitFor(ctx, insideOrEmpty, wait);
    const hit = inner ? leaf(inner) : null;
    if (hit) return finish(hit);
    const only = inner ? Array.from(new Map(inner.filter((r) => !r.branch).map((r) => [r.label, r])).values()) : [];
    if (category.label === named && only.length === 1) return finish(only[0] as PromptRow);
    if (!inner) {
      const inRoot = promptRoots(doc, widget).flatMap((r) => Array.from(r.querySelectorAll(`[${AUTO}]`)).map((e) => attr(e, AUTO)));
      ctx.log("menu-category-closed", { label: category.label, rows: ownRows(doc, widget).length, back: !!backButton(doc, widget), ids: Array.from(new Set(inRoot)).slice(0, 20), options: promptRoots(doc, widget).flatMap((r) => Array.from(r.querySelectorAll(`[${AUTO}="promptOption"]`))).slice(0, 4).map((e) => (e as HTMLElement).outerHTML.slice(0, 300)) });
    }
    note(`${category.label} > ${inner ? Array.from(new Set(inner.map((r) => r.label))).join(", ") || "(no items)" : "(did not open)"}`);
  }
  closeResponsive(doc, widget);
  if (topLabels.length) onMenu?.(topLabels);
  return fail("option-not-found", "no-leaf", true, seen);
}

// --- questionnaire date -------------------------------------------------------

const SEGMENT = /dateSection(Month|Day|Year)-input$/i;

/** The month, day and year inputs of the wrapper a segment sits in, or null. */
export function dateSegments(el: Element): { wrapper: HTMLElement; parts: HTMLInputElement[]; questionnaire: boolean } | null {
  if (el.localName !== "input" || !SEGMENT.test(attr(el, "id"))) return null;
  const wrapper = el.closest<HTMLElement>(`[${AUTO}="dateInputWrapper"]`);
  if (!wrapper) return null;
  const parts = Array.from(wrapper.querySelectorAll<HTMLInputElement>("input")).filter((i) => SEGMENT.test(attr(i, "id")));
  if (parts.length < 2) return null;
  return { wrapper, parts, questionnaire: /Questionnaire--/.test(attr(wrapper, "id")) };
}
