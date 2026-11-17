// Clean-room: docs/provenance.md#CR-ENGINE
// Reading the options of a closed custom listbox without choosing any: open it,
// read what it offers, close it, and leave the control as it was.

import { REOPEN } from "./answers";
import { waitFor } from "./env";
import type { WriteCtx } from "./env";
import type { Run } from "./run";
import { backControl, categoryLabel, cleanOptionLabel, isBackRow, isCategoryRow, isChoiceRow, isEscapeRow, optionsOf, PATH_SEP, popupFor, populatedPopups } from "./popup";
import { isPlaceholderText } from "./placeholder";
import { closeWidget, openPopup, traitsOf, type Trace } from "./listbox";
import { isDijitSelect, isSdfSelect } from "./libraries";
import { isOracleSelect, readOracleOptions } from "./oracle";
import { readSelect2Options } from "./select2";
import { readSrChoices, srChoiceBox } from "./sr";
import type { ControlInfo, Option } from "./scan";
import { flatText, walkElements } from "./dom";
import { hover, pointerSequence, press } from "./gestures";
import { isRendered } from "./visibility";

/** At most this many closed listboxes are opened in one scan; the rest wait for the next pass. */
const MAX_OPENS_PER_SCAN = 25;
/** The closing sweep over required listboxes no lane claimed: the same cap, and this long in all. */
const UNCLAIMED_READ_BUDGET_MS = 20_000;
/** A SuccessFactors paginated picklist renders its rows a page of 100 at a time (the component's own page size). */
const PICKLIST_PAGE_ROWS = 100;
/** A button's words. "Review" leads a button ("Review and submit"); inside a request ("Please review and acknowledge the following:", Alnylam) it is a question's. */
const SUBMIT_SHAPED = /\b(submit|apply|continue|next|sign (in|up)|log ?in)\b|^\W*review\b/i;
/** "(mark all that apply)" asks for a list's choices; its "apply" is no button's word. */
const ALL_THAT_APPLY = /\b(?:select|mark|check|tick|choose|pick)\s+(?:all|any)\s+that\s+apply\b/gi;

export interface PrescanState {
  /** Triggers that would not open; they are not tried again. */
  failed: Set<string>;
  /** Triggers whose skip has been logged, so a later pass does not log it again. */
  skipped: Set<string>;
}

export const newPrescan = (): PrescanState => ({ failed: new Set(), skipped: new Set() });

/** Why a trigger is not opened, or null when it may be. */
function skipReason(info: ControlInfo): string | null {
  if (!isRendered(info.el)) return "not-visible";
  // A button-like label, not a question that mentions applying ("Message and data rates may apply.").
  if (SUBMIT_SHAPED.test(info.label.text.replace(ALL_THAT_APPLY, " ")) && info.label.text.trim().split(/\s+/).length <= 6) return "submit-shaped";
  if (info.el.closest("nav, footer, header, [role=search], [role=navigation]")) return "page-chrome";
  if (info.answered && !REOPEN.test(info.label.text)) return "already-committed";
  return null;
}

async function readSdf(info: ControlInfo, run: Run): Promise<Option[] | null> {
  const ctx = run.ctx();
  const host = info.el;
  const trigger = host.shadowRoot?.querySelector<HTMLElement>(".trigger-button, [part=trigger], button") ?? host;
  if (pointerSequence(trigger) === "unsafe-click") return null;
  const items = await waitFor(ctx, () => {
    const out: string[] = [];
    const scan = (root: ParentNode) =>
      walkElements(root, (el) => {
        if (el.localName === "sdf-select-item" && isRendered(el)) out.push(cleanOptionLabel(flatText(el)));
      });
    scan(host.shadowRoot ?? host);
    scan(host.ownerDocument.body);
    return out.length ? Array.from(new Set(out)) : null;
  }, ctx.timing.prescanReadMs);
  press(trigger, "Escape");
  return items ? items.map((label) => ({ label, value: "" })) : null;
}

async function readDijit(info: ControlInfo, run: Run): Promise<Option[] | null> {
  const ctx = run.ctx();
  const id = info.el.getAttribute("widgetid") as string;
  hover(info.el);
  pointerSequence(info.el);
  const popup = await waitFor(ctx, () => {
    const p = ctx.doc.getElementById(`${id}_dropdown`);
    return p && optionsOf(p).length ? p : null;
  }, ctx.timing.prescanReadMs);
  const labels = popup ? optionsOf(popup).map((o) => o.label) : null;
  press(info.el, "Escape");
  return labels ? labels.map((label) => ({ label, value: "" })) : null;
}

/** A referral-source picker, the one kind of menu whose categories are opened to read the choices inside. */
const HEAR_ABOUT = /how did you hear|hear about|referral source|job source|source of/i;
/** How long a sub-menu, or the way back from one, is given to show its labels. */
const DRILL_WAIT_MS = 400;

const labelsNow = (trigger: HTMLElement, doc: Document): string[] | null => {
  const l = popupFor(trigger, doc);
  return l.state === "found" ? optionsOf(l.popup).map((o) => o.label) : null;
};
const same = (a: string[] | null, b: string[]) => !!a && a.join("|") === b.join("|");

/**
 * A menu of categories ("Job Sites", "Social Media") read whole: each row that
 * says it opens a sub-menu is opened, one at a time, its choices are listed as
 * "Category > Choice" in place of the category, and the menu's Back control
 * returns to the top; when it does not return, the walk stops there. A row
 * that holds a choice is never clicked, so nothing is committed.
 */
async function readCategories(trigger: HTMLElement, base: Option[], ctx: WriteCtx): Promise<Option[]> {
  const doc = ctx.doc;
  const top = labelsNow(trigger, doc);
  const l = popupFor(trigger, doc);
  if (!top || l.state !== "found") return base;
  const categories = optionsOf(l.popup).filter((r) => isCategoryRow(r.el) && !isChoiceRow(r.el)).map((r) => r.label);
  ctx.log("categories", { found: categories.length, rows: optionsOf(l.popup).length });
  if (!categories.length) return base;
  const inside = new Map<string, Option[]>();
  for (const label of categories) {
    await ctx.checkpoint();
    const p = popupFor(trigger, doc);
    const row = p.state === "found" ? optionsOf(p.popup).find((r) => r.label === label) : undefined;
    if (!row || isChoiceRow(row.el) || pointerSequence(row.el) === "unsafe-click") continue;
    const inner = await waitFor(ctx, () => {
      const now = labelsNow(trigger, doc);
      return now && now.length && !same(now, top) ? popupFor(trigger, doc) : null;
    }, DRILL_WAIT_MS);
    ctx.log("category", { label, opened: !!inner && inner.state === "found" });
    if (!inner || inner.state !== "found") continue;
    const name = categoryLabel(label);
    inside.set(label, realOptions(inner.popup).filter((o) => !isBackRow(o.label, name)).map((o) => ({ label: `${name}${PATH_SEP}${o.label}`, value: "" })));
    const back = backControl(inner.popup);
    ctx.log("category-inside", { label, choices: inside.get(label)?.length ?? 0, back: !!back });
    if (!back || pointerSequence(back) === "unsafe-click") break;
    if (!(await waitFor(ctx, () => same(labelsNow(trigger, doc), top) || null, DRILL_WAIT_MS))) {
      ctx.log("category-back-not-returned", { label });
      break;
    }
  }
  const out: Option[] = [];
  for (const o of base) out.push(...(inside.get(o.label)?.length ? (inside.get(o.label) as Option[]) : [o]));
  return out.filter((o, i) => out.findIndex((x) => x.label === o.label) === i);
}

/** Real options of the popup: placeholders and manual-entry rows are not choices. */
function realOptions(popup: Element): Option[] {
  return optionsOf(popup)
    .filter((o) => !isPlaceholderText(o.label) && !isEscapeRow(o.label))
    .map((o) => ({ label: o.label, value: o.value }));
}

/**
 * The chevron beside a search box that shows its whole list (Ashby's value
 * select, Thumbtack: "Start typing..." over the states it hires in): an
 * icon-only button in the box's own container. A button that would submit a
 * form is never it.
 */
function listToggle(box: HTMLInputElement | null): HTMLButtonElement | null {
  const b = box?.parentElement?.querySelector<HTMLButtonElement>(":scope > button") ?? null;
  if (!b || flatText(b).trim() !== "" || (b.type === "submit" && b.form !== null)) return null;
  return b;
}

/**
 * The whole list a search widget's chevron shows, read and closed again; [] when it shows none. Only rows that say
 * they are options count: Ashby's place search shows a bare "No results" under the same chevron.
 */
async function readByToggle(info: ControlInfo, input: HTMLInputElement, toggle: HTMLButtonElement, run: Run): Promise<Option[]> {
  const ctx = run.ctx();
  if (pointerSequence(toggle) === "unsafe-click") return [];
  const read = await waitFor(ctx, () => {
    const l = popupFor(info.el, ctx.doc);
    const o = l.state === "found" && l.popup.querySelector("[role=option]") ? realOptions(l.popup) : [];
    return o.length ? o : null;
  }, ctx.timing.prescanReadMs);
  ctx.log("prescan-read", { selector: info.selector, options: read?.length ?? 0, popup: !!read, toggle: true, labels: (read ?? []).slice(0, 8).map((o) => o.label) });
  press(input, "Escape");
  await waitFor(ctx, () => populatedPopups(ctx.doc).length === 0, ctx.timing.prescanDrainMs);
  return read ?? [];
}

async function readListbox(info: ControlInfo, run: Run): Promise<Option[] | null> {
  const ctx = run.ctx();
  const trigger = info.el;
  const t = traitsOf(trigger);
  // A search widget has no list to read until something is typed: it is asked with no options, unless its chevron shows the whole list.
  if (t.typeahead) {
    const toggle = listToggle(t.input);
    return toggle && t.input ? readByToggle(info, t.input, toggle, run) : [];
  }
  const trace: Trace = { stage: "prescan", labels: [] };
  // A paginated picklist fetches its first page from the server when it opens.
  const wait = t.paginated ? ctx.timing.picklistOpenMs : ctx.timing.prescanReadMs;
  const popup = await openPopup(trigger, t, "", ctx, trace, { open: wait, retry: wait });
  if (!popup || popup === "unsafe-click") {
    await closeWidget(trigger, t, null, ctx);
    // A paginated picklist that shows no page is still filtered by typing: it is asked with no options.
    if (popup === null && t.paginated) return [];
    // A react-select that offers no option until something is typed (no menu, or an empty one) is an asynchronous search such as a city lookup: it is asked with no options.
    if (popup === null && t.reactSelect && t.input !== null && (trace.stage === "not-opened" || trace.stage === "no-options")) return [];
    // So is a popup that opens empty on its own search box (Nebius's city lookup).
    if (popup === null && trace.stage === "search-box") return [];
    return null;
  }
  // A popup that holds only its placeholder is given time to fill.
  const read = await waitFor(ctx, () => {
    const live = optionsOf(popup).length ? popup : null;
    const o = live ? realOptions(live) : [];
    return o.length ? o : null;
  }, wait);
  // A paginated picklist whose first page is not full shows its whole list; a full page may have more behind it, so it is asked with no options and filtered by typing.
  const rows = t.paginated ? optionsOf(popup).length : 0;
  const partial = t.paginated && (read === null || rows >= PICKLIST_PAGE_ROWS);
  const options = partial ? [] : read && HEAR_ABOUT.test(info.label.text) ? await readCategories(trigger, read, ctx) : read;
  ctx.log("prescan-read", { selector: info.selector, options: options?.length ?? 0, popup: !!read, labels: (options ?? []).slice(0, 8).map((o) => o.label), ...(t.paginated ? { pageRows: rows } : {}) });
  // A walk through categories may have left a different popup open.
  const now = popupFor(trigger, ctx.doc);
  await closeWidget(trigger, t, now.state === "found" ? now.popup : popup, ctx);
  await waitFor(ctx, () => populatedPopups(ctx.doc).length === 0, ctx.timing.prescanDrainMs);
  return options;
}

/** Opens a closed listbox of any family, reads its options and closes it again; null when it would not open. Nothing is selected. */
function readOptions(info: ControlInfo, run: Run): Promise<Option[] | null> {
  const sr = info.driver === "sr-component" ? srChoiceBox(info.el) : null;
  if (sr) return readSrChoices(sr, run.ctx());
  if (isSdfSelect(info.el)) return readSdf(info, run);
  if (isDijitSelect(info.el)) return readDijit(info, run);
  if (info.driver === "select2") return readSelect2Options(info.el as HTMLSelectElement, run.ctx());
  if (isOracleSelect(info.el)) return readOracleOptions(info.el as HTMLInputElement, run.ctx()).then((labels) => labels?.map((label) => ({ label, value: "" })) ?? null);
  return readListbox(info, run);
}

/**
 * The options of required custom listboxes that no lane claimed (a follow-up
 * the page showed only after the question pass, for instance), so the check
 * that runs after the fill can still answer them: each is opened, read and
 * closed, and nothing is selected. At most 25 are opened within 20 s; the
 * rest keep no options. Returns the options read, by selector.
 */
export async function readUnclaimedListboxes(run: Run, infos: ControlInfo[]): Promise<Map<string, Option[]>> {
  const out = new Map<string, Option[]>();
  const deadline = run.active() + UNCLAIMED_READ_BUDGET_MS;
  let opened = 0;
  for (const info of infos) {
    if (info.kind !== "custom-listbox" || !info.markedRequired) continue;
    const reason = skipReason(info);
    if (reason) {
      if (reason !== "already-committed") run.log.add("summary", "unclaimed-listbox-skip", { selector: info.selector, reason });
      continue;
    }
    // A list the scan already holds needs no open.
    if (info.options.length && !info.needsOptions) {
      out.set(info.selector, info.options);
      continue;
    }
    if (opened >= MAX_OPENS_PER_SCAN || run.active() > deadline) {
      run.log.add("summary", "unclaimed-listbox-deferred", { selector: info.selector });
      continue;
    }
    await run.checkpoint();
    opened++;
    const options = await readOptions(info, run);
    if (options?.length) {
      out.set(info.selector, options);
      run.listOptions.set(info.selector, options);
    }
  }
  return out;
}

/**
 * Fills in the options of closed custom listboxes so they can be asked. At
 * most 25 are opened per scan: the rest keep `needsOptions` and are read in
 * the next pass, never sent without options. Returns how many were deferred.
 */
export async function prescanListboxes(run: Run, infos: ControlInfo[], state: PrescanState, deadline: number): Promise<number> {
  let opened = 0;
  let deferred = 0;
  for (const info of infos) {
    if (!info.needsOptions) continue;
    // Options already read are kept for the rest of the run.
    const cached = run.listOptions.get(info.selector);
    if (cached) {
      info.options = cached;
      info.needsOptions = false;
      continue;
    }
    if (info.owner !== "matcher" || state.failed.has(info.selector)) continue;
    const skip = skipReason(info);
    if (skip) {
      if (!state.skipped.has(info.selector)) run.log.add("prescan", "skip", { selector: info.selector, reason: skip });
      state.skipped.add(info.selector);
      continue;
    }
    if (opened >= MAX_OPENS_PER_SCAN || run.active() > deadline) {
      deferred++;
      continue;
    }
    await run.checkpoint();
    opened++;
    const options = await readOptions(info, run);
    if (options === null) {
      state.failed.add(info.selector);
      continue;
    }
    run.listOptions.set(info.selector, options);
    info.options = options;
    info.needsOptions = false;
  }
  return deferred;
}
