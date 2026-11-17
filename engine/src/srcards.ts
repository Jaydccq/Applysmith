// Clean-room: docs/provenance.md#CR-ENGINE
// SmartRecruiters history sections. The page's own parse fills Experience and
// Education with cards of its own; the entries the parse made are replaced by
// one card per profile entry, written through the components' own method. A
// section the profile has no entries for is never touched, and the cards that
// were on the page before the run belong to the person.

import { makeRow } from "./answers";
import { clickLabel, isUnsafeRemoval } from "./click-guard";
import { collapse, flatText, normalize } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { singleFlight } from "./flight";
import { innerButton, pointerSequence, pressEditorSave } from "./gestures";
import { entriesOf, entryName, fieldOf, namesIn, rowValue, within, type Entity, type Entry, type Field } from "./rows";
import type { Run } from "./run";
import { openConfirm } from "./sections";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import { isOfficeLocation, isSrCardComponent, isSrMonthField, writeSrComponent } from "./sr";
import { isRendered } from "./visibility";

type Kind = Exclude<Entity, "websites">;

const TITLES: Record<Kind, RegExp> = {
  employment: /^(work )?experience$|^employment( history)?$|^work history$/,
  education: /^education$/,
};
const ADD = /^(\+\s*)?add( another)?( (work )?(experience|education|position|school|job))?( entry)?$/i;
const DELETE = /^(delete|remove)( this)?( (work )?(experience|education|position|school|job|entry|item))?$/i;
// The live page names its controls "Save experience entry", "Cancel adding experience entry", "Add experience entry".
const SAVE = /^save( (experience|education) entry)?$/i;
const CANCEL = /^cancel( adding (experience|education) entry)?$/i;
const COMPONENTS = "spl-input, spl-textarea, spl-datepicker, spl-phone-field, spl-autocomplete, spl-date-field";
const HEADINGS = "h1, h2, h3, h4, h5, h6, legend, [role=heading], spl-typography-title";

/** The most delete passes of one section, and the most late-parse passes. */
const MAX_DELETES = 40;
const LATE_PASSES = 2;

interface Section {
  entity: Kind;
  root: Element;
}

/** What the run saw and made, per run: the cards that stood on the page first, and the ones this driver added. */
interface State {
  start: Set<Element>;
  own: Set<Element>;
}

const states = new WeakMap<Run, State>();
/** What a control is called: its accessible name, else its tooltip (an icon button's), else its text. */
const labelOf = (el: Element): string => collapse(el.getAttribute("aria-label") ?? el.getAttribute("tooltip") ?? flatText(el));
/** A press of a page control; an spl-button takes it on the button it draws. */
const pressOn = (b: HTMLElement, opts: { removeWithin?: Element } = {}) => pointerSequence(b, { ...opts, target: innerButton(b) });

function sectionsOf(doc: Document): Section[] {
  const found: Section[] = [];
  for (const h of Array.from(doc.querySelectorAll(HEADINGS))) {
    const text = normalize(flatText(h));
    for (const entity of ["employment", "education"] as const) {
      if (!TITLES[entity].test(text) || !isRendered(h)) continue;
      const root = h.closest("section, fieldset, [role=group], oc-experience, oc-education") ?? h.parentElement;
      if (root && !found.some((s) => s.root === root)) found.push({ entity, root });
    }
  }
  return found;
}

const buttons = (root: Element): HTMLElement[] => Array.from(root.querySelectorAll<HTMLElement>("button, [role=button], spl-button")).filter((b) => isRendered(b));

/**
 * An open entry editor: the smallest container of a component that also holds
 * its own Save or Cancel control. The search never reaches the section root,
 * so a Save that belongs to the section or the page does not make an editor.
 */
function editorsOf(section: Section): Element[] {
  const out: Element[] = [];
  for (const c of Array.from(section.root.querySelectorAll(COMPONENTS))) {
    let node: Element | null = c.parentElement;
    while (node && node !== section.root) {
      if (buttons(node).some((b) => SAVE.test(labelOf(b)) || CANCEL.test(labelOf(b)))) {
        if (!out.includes(node)) out.push(node);
        break;
      }
      node = node.parentElement;
    }
  }
  return out.filter((e) => !out.some((o) => o !== e && e.contains(o)));
}

/** A committed card: the list item or card container of a Delete control that sits in no editor. */
function cardsOf(section: Section): { card: Element; remove: HTMLElement }[] {
  const editors = editorsOf(section);
  return buttons(section.root)
    .filter((b) => DELETE.test(labelOf(b)) && !editors.some((e) => e.contains(b)))
    .map((remove) => ({ card: remove.closest("li, [role=listitem], article, tr, [data-test$='-entry']") ?? remove.parentElement ?? remove, remove }));
}

/** The committed history cards on the page: a parse that lands changes their count. */
export const historyCardCount = (doc: Document): number => sectionsOf(doc).reduce((n, s) => n + cardsOf(s).length, 0);

const addButton = (section: Section): HTMLElement | null => buttons(section.root).find((b) => ADD.test(labelOf(b)) && !editorsOf(section).some((e) => e.contains(b))) ?? null;

/** Takes the cards on the page before the run touches anything. */
export function snapshotSrCards(run: Run): void {
  if (run.site !== "smartrecruiters") return;
  states.set(run, { start: new Set(sectionsOf(run.doc).flatMap((s) => cardsOf(s).map((c) => c.card))), own: new Set() });
}

async function deleteCard(ctx: WriteCtx, card: { card: Element; remove: HTMLElement }): Promise<boolean> {
  if (isUnsafeRemoval(clickLabel(card.remove), card.card, card.remove) || pressOn(card.remove, { removeWithin: card.card }) === "unsafe-click") return false;
  await ctx.sleep(ctx.timing.sectionConfirmMs);
  const confirm = openConfirm(ctx.doc);
  if (confirm) {
    const dialog = confirm.closest("[role=dialog], [role=alertdialog], dialog") ?? confirm;
    pointerSequence(confirm, { removeWithin: dialog });
  } else {
    // The live page asks in an spl-dialog whose primary button confirms.
    const dialog = Array.from(ctx.doc.querySelectorAll("spl-dialog[open]")).find((d) => /\b(delete|remove)\b/i.test(flatText(d)));
    const ok = dialog?.querySelector<HTMLElement>("spl-button[type=primary]");
    if (dialog && ok) pressOn(ok, { removeWithin: dialog });
  }
  return (await waitFor(ctx, () => !card.card.isConnected, ctx.timing.sectionRemoveWaitMs)) === true;
}

/**
 * The editors the parse left open are cancelled (it opens one for every entry
 * it could not complete, and a blank required Title blocks the form), and the
 * cards it made that name no profile entry are deleted. Cards that stood on
 * the page first stay, and so does a parsed card that names a profile entry.
 */
async function clearParsed(run: Run, section: Section, state: State, entries: Entry[]): Promise<number> {
  const ctx = run.ctx();
  let removed = 0;
  const names = (c: Element) => entries.some((e) => represented(section, e, [c]));
  const cancelled = new Set<Element>();
  for (let pass = 0; pass < MAX_DELETES; pass++) {
    await run.checkpoint();
    const open = editorsOf(section).find((e) => !cancelled.has(e));
    if (open) cancelled.add(open);
    const cancel = open ? buttons(open).find((b) => CANCEL.test(labelOf(b))) : undefined;
    if (open && cancel && pressOn(cancel) === "ok") {
      await waitFor(ctx, () => !open.isConnected || !isRendered(open), ctx.timing.sectionRemoveWaitMs);
      continue;
    }
    const target = cardsOf(section).find((c) => !state.start.has(c.card) && !state.own.has(c.card) && !names(c.card));
    if (!target) break;
    if (!(await deleteCard(ctx, target))) {
      run.log.add("sr", "delete-stuck", { entity: section.entity });
      break;
    }
    removed++;
  }
  return removed;
}

/** The profile keeps months: a month field gets the first of the entry's month, which is the whole value it holds. */
function monthValue(entity: Kind, field: Field, entry: Entry, info: ControlInfo): string | null {
  if ((field !== "start" && field !== "end") || !isSrMonthField(info.el)) return null;
  const y = rowValue(entity, `${field}Year`, entry, info);
  const m = rowValue(entity, `${field}Month`, entry, info);
  return y && m ? `${y}-${m}-01` : null;
}

async function fillEditor(run: Run, section: Section, editor: Element, entry: Entry): Promise<number> {
  const ctx = run.ctx();
  let unplaced = 0;
  for (const info of scanControls(run.doc).filter((i) => within(i.el, [editor]))) {
    await run.checkpoint();
    if (isOfficeLocation(info.label.text)) continue;
    const field = fieldOf(section.entity, info);
    const value = rowValue(section.entity, field, entry, info) ?? monthValue(section.entity, field, entry, info);
    if (value === null) continue;
    if (info.kind === "checkbox") {
      const box = info.el as HTMLInputElement;
      if (!box.checked && /^yes$/i.test(value)) pointerSequence(box);
      continue;
    }
    if (!isSrCardComponent(info.el)) {
      unplaced++;
      continue;
    }
    const out = await writeSrComponent(info.el as HTMLInputElement, value, ctx);
    if (!out.ok) unplaced++;
  }
  return unplaced;
}

async function addCard(run: Run, section: Section, entry: Entry, state: State): Promise<boolean> {
  const ctx = run.ctx();
  const add = addButton(section);
  if (!add || pressOn(add) === "unsafe-click") return false;
  const editor = await waitFor(ctx, () => editorsOf(section)[0] ?? null, ctx.timing.srEditorWaitMs);
  if (!editor) return false;
  const unplaced = await fillEditor(run, section, editor, entry);
  if (unplaced) run.log.add("sr", "unplaced", { entity: section.entity, fields: unplaced });
  const known = new Set(cardsOf(section).map((c) => c.card));
  const made = pressEditorSave(editor) === "ok" ? await waitFor(ctx, () => cardsOf(section).find((c) => !known.has(c.card)) ?? null, ctx.timing.srEditorWaitMs) : null;
  if (made) state.own.add(made.card);
  else await abandon(run, section, editor, entry);
  return made !== null;
}

/**
 * An editor whose Save the page refused is cancelled, so it never holds the
 * walk; its empty required fields are left to the user as rows of their own.
 */
async function abandon(run: Run, section: Section, editor: Element, entry: Entry): Promise<void> {
  const ctx = run.ctx();
  for (const info of scanControls(run.doc).filter((i) => within(i.el, [editor]))) {
    if (!info.required || readInfo(info) !== "") continue;
    const had = rowValue(section.entity, fieldOf(section.entity, info), entry, info) !== null;
    run.setRow(makeRow(info, "profile", "failed", { reason: had ? "write-not-committed" : "no-answer" }));
  }
  const cancel = buttons(editor).find((b) => CANCEL.test(labelOf(b)));
  if (cancel && pressOn(cancel) === "ok") await waitFor(ctx, () => !editor.isConnected || !isRendered(editor), ctx.timing.sectionRemoveWaitMs);
  run.log.add("sr", "editor-cancelled", { entity: section.entity });
}

/** A card shows an entry's employer or school, as whole words. */
function represented(section: Section, entry: Entry, survivors: Element[]): boolean {
  const name = entryName(section.entity, entry);
  return survivors.some((c) => namesIn(flatText(c), name));
}

async function reconcileOnce(run: Run, state: State): Promise<void> {
  const applicant = run.applicant;
  if (!applicant) return;
  for (const section of sectionsOf(run.doc)) {
    const entries = entriesOf(applicant, section.entity);
    // A section the profile has nothing for is the person's.
    if (!entries.length) continue;
    const removed = await clearParsed(run, section, state, entries);
    let added = 0;
    for (const [at, entry] of entries.entries()) {
      // Two roles at one employer are two cards: the nth entry of a name has its card when n cards show the name.
      const name = normalize(entryName(section.entity, entry));
      const nth = entries.slice(0, at + 1).filter((e) => normalize(entryName(section.entity, e)) === name).length;
      // Read the cards again for each entry: a parse that lands mid-reconcile has made some.
      if (cardsOf(section).filter((c) => represented(section, entry, [c.card])).length >= nth) continue;
      await run.checkpoint();
      // An entry the page would not take is the user's; the next entry still gets its card.
      if (await addCard(run, section, entry, state)) added++;
      else run.log.add("sr", "add-stuck", { entity: section.entity });
    }
    run.log.add("sr", "reconciled", { entity: section.entity, removed, added });
  }
}

/**
 * One pass at a time per document: a frame reconciles only the sections its
 * own document holds, and a second caller there gets the first's result. A
 * helper that does not answer the flight's own requests coordinates nothing,
 * and this frame then reconciles by itself.
 */
async function reconcile(run: Run, state: State): Promise<void> {
  if (!sectionsOf(run.doc).length) return;
  if (!run.main) return reconcileOnce(run, state);
  let ran = false;
  try {
    await singleFlight(run.main, "sr-reconcile", async () => {
      ran = true;
      await reconcileOnce(run, state);
      return true;
    });
  } catch (e) {
    if (ran) throw e;
    run.log.add("sr", "flight-unavailable", {});
    await reconcileOnce(run, state);
  }
}

/** Replaces the cards the parse made with one card per profile entry. */
export async function reconcileSrCards(run: Run): Promise<void> {
  const state = states.get(run);
  if (state) await reconcile(run, state);
}

/** A section holds more cards (or open editors) than the profile has entries. */
function surplus(run: Run): boolean {
  const applicant = run.applicant;
  if (!applicant) return false;
  return sectionsOf(run.doc).some((s) => {
    const have = entriesOf(applicant, s.entity).length;
    return have > 0 && cardsOf(s).length + editorsOf(s).length > have;
  });
}

/**
 * The parse can land after the first pass: when a parse ran and was not seen to settle, wait for a surplus
 * for up to the late wait and reconcile it, at most twice. A settled parse is looked at once, with no wait.
 */
export async function lateParseSweep(run: Run): Promise<void> {
  const state = states.get(run);
  if (!state || !run.parseTriggered) return;
  for (let pass = 0; pass < LATE_PASSES; pass++) {
    if (!(await waitFor(run.ctx(), () => surplus(run), run.parseSettled ? 0 : run.timing.srLateMs))) return;
    run.log.add("sr", "late-parse", { pass: pass + 1 });
    await reconcile(run, state);
  }
}
