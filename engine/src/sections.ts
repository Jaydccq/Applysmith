// Clean-room: docs/provenance.md#CR-ENGINE
// Section preparation: repeated rows are added until there is one per profile
// entry, rows the page's own parser invented are removed, and a sweep removes
// the ones the page makes again. Every press is guarded: an add control by the
// ordinary guard, a remover by the row-removal guard, which allows a delete
// control only inside the row it owns.

import { clickLabel, isUnsafeLabel, isUnsafeRemoval } from "./click-guard";
import { collapse, flatText, normalize } from "./dom";
import { waitFor, type Timing, type WriteCtx } from "./env";
import { pointerSequence } from "./gestures";
import { bindRows, entriesOf, entryName, namesIn, OTHER_HISTORY, type Binding, type Entity, type Entry, type Row } from "./rows";
import { isParserHost, pageRows as rowsOf } from "./row-plan";
import type { ApplicantSnapshot } from "./protocol";
import type { UserEdits } from "./restore";
import type { Run, RunLog } from "./run";
import { readInfo, scanControls } from "./scan";
import { isRendered } from "./visibility";

/** Hosts whose parser pairs one entry's degree with another's school: such a record is junk, not a row to rewrite. */
const STRICT_DETAIL = new Set(["dayforce", "ukg"]);

/** The most Add presses for one section, and the most removals. */
export const MAX_ADD_PASSES = 30;
export const MAX_REMOVALS = 20;
/** A wait whose probe scans the whole page polls this many times slower than a wait on one control. */
const SCAN_POLLS = 5;

const ADD = /^(\+\s*)?add( another| more)?\b/i;
const ENTITY_WORDS: Record<Exclude<Entity, "websites">, RegExp> = {
  employment: /\b(work|employ|experience|job|position)/i,
  education: /education|school|degree|academic/i,
};
const DELETE = /\b(delete|remove|trash)\b/i;
const CONFIRM = /^(ok|yes|delete|confirm|remove)$/i;
const DELETE_TITLE = /\b(delete|remove)\b.*\b(history|record|item|entry|experience|education)\b/i;

const labelOf = (el: Element): string => collapse(`${el.getAttribute("aria-label") ?? ""} ${flatText(el)}`.trim());

/** What the page showed at the start of the run, by row: a row the parse made or changed is not in it. */
export type RowSnapshot = Map<Element, string>;

export function snapshotRows(run: Run): RowSnapshot {
  const rows = pageRows(run.doc);
  const snap: RowSnapshot = new Map(rows.map((r) => [r.root, `${r.identity}|${r.detail}`]));
  for (const c of committedCards(run.doc, rows)) snap.set(c.card, `card|${normalize(flatText(c.card))}`);
  return snap;
}

/** A committed record: read-only text with its own Delete Record control. */
interface Card {
  card: Element;
  remove: HTMLElement;
}

const CARD_DELETE = /^delete record$/i;
const CARD = "li, article, tr, [role=listitem]";

function committedCards(doc: Document, rows: Row[]): Card[] {
  return Array.from(doc.querySelectorAll<HTMLElement>("button, [role=button]"))
    .filter((b) => isRendered(b) && CARD_DELETE.test(labelOf(b)) && !rows.some((r) => r.root.contains(b)))
    .map((remove) => ({ remove, card: remove.closest(CARD) ?? remove.parentElement ?? remove }));
}

/** Which section a card sits in: the nearest heading above it that names education or work. */
function cardEntity(card: Element): Exclude<Entity, "websites"> | null {
  for (let node: Element | null = card, depth = 0; node && depth < 6; node = node.parentElement, depth++) {
    const heads: Element[] = [];
    for (let sib = node.previousElementSibling, i = 0; sib && i < 4; sib = sib.previousElementSibling, i++) heads.push(sib);
    heads.push(...Array.from(node.querySelectorAll(":scope > h1, :scope > h2, :scope > h3, :scope > h4, :scope > legend")));
    for (const h of heads) {
      const text = collapse(flatText(h)).slice(0, 80);
      if (OTHER_HISTORY.test(text)) continue;
      if (ENTITY_WORDS.education.test(text) && !ENTITY_WORDS.employment.test(text)) return "education";
      if (ENTITY_WORDS.employment.test(text)) return "employment";
    }
  }
  return null;
}

/**
 * Cards the parse made that name no profile entry are deleted: Delete Record
 * opens a confirmation that must be answered or the record stays behind a
 * blocking modal. Cards that stood on the page before the run, and sections
 * the profile has no entries for, are left alone; a delete that does not
 * lower the count ends the loop.
 */
async function removeJunkCards(run: Run, entity: Exclude<Entity, "websites">, entries: Entry[], start: RowSnapshot): Promise<void> {
  const ctx = run.ctx();
  const junk = (): Card[] =>
    committedCards(run.doc, pageRows(run.doc)).filter((c) => {
      if (cardEntity(c.card) !== entity) return false;
      const text = flatText(c.card);
      if (entries.some((e) => namesIn(text, entryName(entity, e)))) return false;
      return start.get(c.card) !== `card|${normalize(text)}`;
    });
  for (let i = 0; i < MAX_REMOVALS; i++) {
    await run.checkpoint();
    const found = junk();
    const target = found[0];
    if (!target) return;
    const before = found.length;
    if (isUnsafeRemoval(clickLabel(target.remove), target.card, target.remove) || pointerSequence(target.remove, { removeWithin: target.card }) === "unsafe-click") return;
    await ctx.sleep(ctx.timing.sectionConfirmMs);
    const confirm = openConfirm(ctx.doc);
    if (confirm) pointerSequence(confirm, { removeWithin: confirm.closest("[role=dialog], [role=alertdialog], dialog") ?? confirm });
    if (!(await waitFor(ctx, () => junk().length < before, ctx.timing.sectionRemoveWaitMs, ctx.timing.pollMs * SCAN_POLLS))) {
      run.log.add("sections", "card-stuck", { entity });
      return;
    }
  }
}

const pageRows = (doc: Document): Row[] => rowsOf(scanControls(doc), doc);

/** Controls inside the root that belong to the row; a remover is safe only when nothing else lives there. */
function holdsOnly(root: Element, row: Row, doc: Document): boolean {
  const own = new Set(row.controls.flatMap((c) => [c.info.el, ...c.info.members]));
  return scanControls(doc).every((i) => !root.contains(i.el) || own.has(i.el) || i.members.some((m) => own.has(m)));
}

const BARE_ADD = /^(\+\s*)?add( another| more)?$/i;

/** An Add control's name: its own label, and for a bare "Add" (Workday's) the heading of the group it sits in. */
function addName(b: Element): string {
  const own = labelOf(b);
  if (!BARE_ADD.test(own)) return own;
  const ids = (b.closest("[role=group][aria-labelledby]")?.getAttribute("aria-labelledby") ?? "").split(/\s+/).filter(Boolean);
  return collapse(`${own} ${ids.map((id) => b.ownerDocument.getElementById(id)?.textContent ?? "").join(" ")}`);
}

function addControls(doc: Document): HTMLElement[] {
  return Array.from(doc.querySelectorAll<HTMLElement>("button, a, [role=button]")).filter((b) => isRendered(b) && ADD.test(labelOf(b)) && !isUnsafeLabel(clickLabel(b)));
}

const ENTRY_ADD = /\b(experience|employment|education)\b/i;

/** True when the page offers to add a work-experience or education entry by name. */
export function offersEntryAdd(doc: Document): boolean {
  return addControls(doc).some((b) => ENTRY_ADD.test(addName(b)));
}

function addButton(doc: Document, entity: Exclude<Entity, "websites">, rows: Row[]): HTMLElement | null {
  const all = addControls(doc);
  const named = all.filter((b) => ENTITY_WORDS[entity].test(addName(b)) && !OTHER_HISTORY.test(addName(b)));
  if (named.length) return named[named.length - 1] as HTMLElement;
  // A bare "Add" belongs to the section when it sits beside the rows: the nearest container up from the
  // row that holds one, at most two levels above it (a lone row's root is its whole block, its Add
  // included), so the next block's Add (Greenhouse's education after employment) is never taken.
  let node: Element | null = rows.find((r) => r.entity === entity)?.root ?? null;
  for (let up = 0; node && up <= 2; up++, node = node.parentElement) {
    const within = node;
    const near = all.find((b) => within.contains(b));
    if (near) return near;
  }
  return null;
}

/** The committed cards that stand for rows of a section: its own, and those no heading places in the other section. */
function cardCount(doc: Document, rows: Row[], entity: Exclude<Entity, "websites">): number {
  return committedCards(doc, rows).filter((c) => [entity, null].includes(cardEntity(c.card))).length;
}

/** Add controls whose press mounted no row the engine reads: a later run that pressed one again would only leave another blank row. */
const SPENT_ADDS = new WeakSet<Element>();

async function addUntil(run: Run, entity: Exclude<Entity, "websites">, want: number): Promise<void> {
  const ctx = run.ctx();
  for (let pass = 0; pass < MAX_ADD_PASSES; pass++) {
    await run.checkpoint();
    const rows = pageRows(run.doc);
    const have = rows.filter((r) => r.entity === entity).length + cardCount(run.doc, rows, entity);
    if (have >= want) return;
    const btn = addButton(run.doc, entity, rows);
    if (!btn || SPENT_ADDS.has(btn)) return;
    if (pointerSequence(btn) === "unsafe-click") return;
    // The page shows a new row this engine can read, or it does not: an Add that makes no recognised row (nothing, or controls the engine cannot place in a row) is the last one.
    const grew = await waitFor(ctx, () => {
      const now = pageRows(run.doc);
      return now.filter((r) => r.entity === entity).length + cardCount(run.doc, now, entity) > have;
    }, ctx.timing.sectionAddWaitMs, ctx.timing.pollMs * SCAN_POLLS);
    run.log.add("sections", "add", { entity, grew: grew === true });
    if (!grew) {
      SPENT_ADDS.add(btn);
      return;
    }
  }
}

/** The control that removes one row: inside the row it owns, or the section's single remover when the row is the last. */
export function removerFor(row: Row, rows: Row[], doc: Document): { button: HTMLElement; within: Element } | null {
  // A lone row's root is widened and can take in committed cards: a Delete Record in a list item that holds none of the row's controls is a card's.
  const ofCard = (b: HTMLElement): boolean => {
    const card = CARD_DELETE.test(labelOf(b)) ? b.closest(CARD) : null;
    return card !== null && !row.controls.some((c) => card.contains(c.info.el));
  };
  const inside = Array.from(row.root.querySelectorAll<HTMLElement>("button, a, [role=button]")).filter((b) => isRendered(b) && DELETE.test(labelOf(b)) && !ofCard(b));
  if (inside.length && holdsOnly(row.root, row, doc)) return { button: inside[0] as HTMLElement, within: row.root };
  const mine = rows.filter((r) => r.entity === row.entity && r.group === row.group);
  const last = mine[mine.length - 1];
  if (last?.root === row.root && mine.length > 1) {
    // One "Delete This ... History" button removes the last row of its section and hides when one row is left.
    const word = row.entity === "education" ? "education" : "work";
    const shared = Array.from(doc.querySelectorAll<HTMLElement>("button, a, [role=button]")).find((b) => isRendered(b) && new RegExp(`^delete this ${word} history$`, "i").test(labelOf(b)));
    if (shared) return { button: shared, within: shared.parentElement ?? shared };
  }
  return null;
}

export function openConfirm(doc: Document): HTMLElement | null {
  for (const d of Array.from(doc.querySelectorAll<HTMLElement>("[role=dialog], [role=alertdialog], dialog[open]"))) {
    if (!isRendered(d)) continue;
    const title = collapse(d.getAttribute("aria-label") ?? flatText(d.querySelector("h1, h2, h3, h4, [role=heading], legend") ?? d));
    if (!DELETE_TITLE.test(title)) continue;
    const ok = Array.from(d.querySelectorAll<HTMLElement>("button")).find((b) => CONFIRM.test(labelOf(b)) && !/application/i.test(labelOf(b)));
    if (ok) return ok;
  }
  return null;
}

/** Presses the row's remover, answers a confirmation that names a deletion, and waits for the row to go. */
export async function removeRow(ctx: WriteCtx, row: Row, rows: Row[]): Promise<boolean> {
  const rem = removerFor(row, rows, ctx.doc);
  if (!rem) return false;
  const label = clickLabel(rem.button);
  if (isUnsafeRemoval(label, rem.within, rem.button) || pointerSequence(rem.button, { removeWithin: rem.within }) === "unsafe-click") return false;
  await ctx.sleep(ctx.timing.sectionConfirmMs);
  const confirm = openConfirm(ctx.doc);
  if (confirm) {
    const dialog = confirm.closest("[role=dialog], [role=alertdialog], dialog") ?? confirm;
    pointerSequence(confirm, { removeWithin: dialog });
  }
  const gone = await waitFor(ctx, () => !row.root.isConnected || !row.controls.some((c) => c.info.el.isConnected && isRendered(c.info.el)), ctx.timing.sectionRemoveWaitMs);
  return gone === true;
}

/** Whether the run wrote, or tried to write, the control with this selector. */
export type Wrote = (selector: string) => boolean;

export const wroteIn = (rows: ReadonlyMap<string, { outcome: string }>): Wrote => (selector) => ["filled", "failed"].includes(rows.get(selector)?.outcome ?? "");

/**
 * Whether a row was made or changed by the page's parse during this run: no
 * person touched it, and the run did not write its employer or school (what
 * the page shows for a name the run wrote is the run's row, not a parse).
 */
function parserMade(edits: UserEdits, row: Row, start: RowSnapshot, wrote: Wrote): boolean {
  if (row.controls.some((c) => edits.edited(c.info))) return false;
  const lead = row.entity === "employment" ? "company" : "school";
  if (row.controls.some((c) => c.field === lead && wrote(c.info.selector))) return false;
  const was = start.get(row.root);
  return was === undefined || was !== `${row.identity}|${row.detail}`;
}

/**
 * Rows to remove for one section: those with no entry, and the duplicates of
 * an entry already claimed, that the parse made. A blank template row stays;
 * a row the user typed stays.
 */
export function junkOf(edits: UserEdits, bindings: Binding[], start: RowSnapshot, strictDetail = false, wrote: Wrote = () => false): Row[] {
  return bindings
    .filter((b) => ((!b.entry && b.why !== "beyond") || (strictDetail && b.entry && !b.detailOk)) && !b.blank && parserMade(edits, b.row, start, wrote))
    .map((b) => b.row);
}

async function removeAll(run: Run, rows: Row[], entity: Entity): Promise<number> {
  const ctx = run.ctx();
  let removed = 0;
  // From the last row up: a remover that works on the last row only keeps working.
  const order = rows.slice().sort((a, b) => b.ordinal - a.ordinal);
  for (const row of order.slice(0, MAX_REMOVALS)) {
    await run.checkpoint();
    const all = pageRows(run.doc);
    if (!(await removeRow(ctx, row, all))) {
      run.log.add("sections", "remove-stuck", { entity });
      break;
    }
    removed++;
  }
  return removed;
}

/** Add and clean up the history sections of a page: nothing happens for a section the profile has no entries for. */
export async function prepareSections(run: Run, start: RowSnapshot): Promise<void> {
  const applicant = run.applicant;
  if (!applicant) return;
  const parser = isParserHost(run.site);
  for (const entity of ["employment", "education"] as const) {
    const entries = entriesOf(applicant, entity);
    if (!entries.length) continue;
    if (parser) {
      const rows = pageRows(run.doc);
      const bindings = bindRows(rows, entries, entity);
      const wrote = wroteIn(run.rows);
      const junk = junkOf(run.edits, bindings, start, STRICT_DETAIL.has(run.site ?? ""), wrote);
      if (junk.length) run.log.add("sections", "junk", { entity, rows: junk.length });
      await removeAll(run, junk, entity);
      // Rows past the last entry that the parse made go too, from the end.
      const after = bindRows(pageRows(run.doc), entries, entity);
      const surplus = after.filter((b) => !b.entry && b.why === "beyond" && !b.blank && parserMade(run.edits, b.row, start, wrote)).map((b) => b.row);
      await removeAll(run, surplus, entity);
      await removeJunkCards(run, entity, entries, start);
    }
    // Workable's editors are drafts the page opened for the applicant; more of them are not opened.
    if (run.site === "workable") continue;
    await addUntil(run, entity, entries.length);
  }
}

// --- the sweep ------------------------------------------------------------------

/** The most one row identity is removed by the sweep. */
export const MAX_SWEEP_REMOVALS = 3;

/** Same title, company and start month: a copy the parse made after the fill. */
function duplicateKeys(rows: Row[]): Map<Row, string> {
  const seen = new Map<string, Row>();
  const dups = new Map<Row, string>();
  for (const r of rows.filter((x) => x.entity === "employment")) {
    const get = (f: string) => collapse(r.controls.filter((c) => c.field === f).map((c) => readInfo(c.info)).find((v) => v !== "") ?? "");
    const title = get("title");
    const company = get("company");
    const start = [get("start"), get("startMonth"), get("startYear")].join("-");
    // A row missing a title or a company may be a fresh one nobody has reached yet.
    if (!title || !company) continue;
    const key = normalize(`${title}|${company}|${start}`);
    if (seen.has(key)) dups.set(r, key);
    else seen.set(key, r);
  }
  return dups;
}

/** The most duplicate rows removed once the fill is done. */
const MAX_DUPLICATE_REMOVALS = 10;

/**
 * Work rows that repeat an earlier row once the fill committed every row's
 * identity, however the copy appeared (a draft the page kept counts too): each
 * goes before the summary, and its controls leave the summary, since a copy's
 * required fields have no entry to answer them and would stop the check.
 */
export async function removeDuplicateRows(run: Run): Promise<void> {
  if (!isParserHost(run.site)) return;
  const ctx = run.ctx();
  for (let i = 0; i < MAX_DUPLICATE_REMOVALS; i++) {
    await run.checkpoint();
    const rows = pageRows(run.doc);
    const target = duplicateKeys(rows).keys().next().value;
    if (!target) return;
    const selectors = target.controls.map((c) => c.info.selector);
    if (!(await removeRow(ctx, target, rows))) {
      run.log.add("sections", "duplicate-stuck", {});
      return;
    }
    for (const s of selectors) run.rows.delete(s);
    run.log.add("sections", "duplicate-removed", {});
  }
}

export interface SweepEnv {
  applicant: ApplicantSnapshot;
  site: string | null;
  edits: UserEdits;
  log: RunLog;
  timing: Timing;
  start: RowSnapshot;
  /** The controls the run wrote: a row whose employer or school it wrote is never swept as a parse's. */
  wrote: Wrote;
}

/**
 * Removes again what the page makes again. Every `sweepPollMs` it classifies
 * the rows as before; it ends after `sweepIdleMs` with nothing to remove, or at
 * the hard cap, and gives up on a row identity after three removals.
 */
export class RowSweeper {
  private readonly began = Date.now();
  private lastRemoval = Date.now();
  private lastLook = 0;
  private readonly tries = new Map<string, number>();
  removed = 0;

  constructor(private readonly env: SweepEnv) {}

  get done(): boolean {
    const now = Date.now();
    return !isParserHost(this.env.site) || now - this.began >= this.env.timing.sweepCapMs || now - this.lastRemoval >= this.env.timing.sweepIdleMs;
  }

  async step(ctx: WriteCtx): Promise<void> {
    if (this.done || Date.now() - this.lastLook < this.env.timing.sweepPollMs) return;
    this.lastLook = Date.now();
    let removedNow = 0;
    for (const entity of ["employment", "education"] as const) {
      const entries = entriesOf(this.env.applicant, entity);
      if (!entries.length) continue;
      const rows = pageRows(ctx.doc);
      const dup = duplicateKeys(rows);
      const target = [...junkOf(this.env.edits, bindRows(rows, entries, entity), this.env.start, STRICT_DETAIL.has(this.env.site ?? ""), this.env.wrote), ...Array.from(dup.keys())][0];
      if (!target) continue;
      const id = normalize(`${entity}|${target.identity}|${target.detail}|${dup.get(target) ?? ""}`);
      if ((this.tries.get(id) ?? 0) >= MAX_SWEEP_REMOVALS) {
        this.env.log.add("sections", "abandoned", { entity });
        continue;
      }
      this.tries.set(id, (this.tries.get(id) ?? 0) + 1);
      if (await removeRow(ctx, target, rows)) removedNow++;
    }
    if (removedNow) {
      this.lastRemoval = Date.now();
      this.removed += removedNow;
      this.env.log.add("sections", "swept", { rows: removedNow });
    }
  }
}
