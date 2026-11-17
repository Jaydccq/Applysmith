// Clean-room: docs/provenance.md#CR-ENGINE
// One control, one desired value: shape it, write it, read it back, and say
// what happened.

import { segmentIso, segmentOptions, writeCalendar, writeDateSegments } from "./calendar";
import { addressChooser, countryChooser, degreeChooser, degreeLevel, displayMatches, geoChooser, phoneCountryChooser, searchSeeds, seedKind, type AddressHints } from "./choosers";
import { countryNames, countryPart } from "./writers";
import { isPlaceholderOption, optionText } from "./reader";
import { committedTokens, holdsSearchText } from "./committed";
import { assentingOption, consentKind, consentRefusal, type ConsentRefusal } from "./consent";
import { normalize, signsAgree } from "./dom";
import { activeNow, timerOn, type WriteCtx } from "./env";
import { shapeValue } from "./guards";
import { writeDijit, writeSdf, writeTypeaheadJs, dijitHasChoice, tinymceEditor, writeTinymce } from "./libraries";
import { traitsOf, writeListbox, type ListboxRequest } from "./listbox";
import type { RowOutcome, RowReason } from "./protocol";
import { optionLabel } from "./labels";
import type { UserEdits } from "./restore";
import { inPhoneFieldset, showsDialCode } from "./phone-country";
import { writePlaces } from "./places";
import { readInfo, type ControlInfo } from "./scan";
import { select2Pick, writeSelect2 } from "./select2";
import { resolveAll, resolveSelector } from "./selector";
import { targetToggle, toggleGroupOf, toggleLabel, togglesHold, writeToggles } from "./toggles";
import { isRendered } from "./visibility";
import { ashbyActive, ashbyBefore, ashbyHolds, isAshbyYesNo, recommitYesNo } from "./ashby";
import { writeIcims } from "./icims";
import { writeLeverLocation } from "./lever";
import { writePctySelect, writePctyText, writeRwDropdown } from "./reactwidgets";
import { writeSrComponent, type SrPlace } from "./sr";
import { oracleDrive, writeOracleAddress, writeOracleSelect } from "./oracle";
import { dateSegments, drillWorkdayMenu, MAX_SKILLS, MENU_BUDGET_FACTOR, writeWorkdayPrompt } from "./workday";
import { readOnlyBlocks, selectionLimit } from "./widgets";
import { bestMember, choiceHolds, findOption, loneTarget, sameValue, writeChoice, writeEditable, writeSelect, writeText, type WriteOutcome } from "./writers";

export interface Desired {
  value: string;
  optionLabel?: string;
  optionLabels?: string[];
  /** The answer's ranked stand-ins (Resolution.alternatives): a listbox that holds none of the answer is searched for each in turn. */
  alternatives?: string[];
  /** The page default may be replaced (matcher outcome filled-review). */
  replaceDefault: boolean;
  currentDate?: boolean;
  phone?: boolean;
  /** Where the applicant lives: a street suggestion is committed only when it holds these. */
  address?: AddressHints;
  /** The applicant's region and country, for a city chosen from a place search. */
  place?: SrPlace;
  /** The host marked a plural payload as a multi-select binding. */
  multiSelectBinding?: boolean;
  /** The user's own bank answer for this question, which the consent rules let apply as written. */
  ownAnswer?: boolean;
  /** A field of study's row degree, which names the faculty a catalog may file the subject under. */
  degree?: string;
  /** The host asks for a box this run wrote to be written again: the page changed since, or a review doubts the value. */
  overwrite?: boolean;
}

export interface ApplyResult {
  outcome: RowOutcome;
  reason?: RowReason;
  value?: string;
  optionLabel?: string;
  /** The last stage a driver reached and the menu labels it saw, for the diagnostic log. */
  detail?: string;
  /** What a menu the question was asked without holds (see WriteOutcome). */
  options?: string[];
}

export interface ApplyEnv {
  ctx: WriteCtx;
  /** Values written by any lane in this run, by selector. */
  written: Map<string, string>;
  /** "keep" never overwrites a committed value; "replace" does unless another lane wrote it this run. */
  mode: "keep" | "replace";
  /** The user's legal-declarations setting for this run. */
  acceptLegalDeclarations: boolean;
  /** A control the user touched is theirs: every write path goes through here, so this is the one place it is enforced. */
  edits: UserEdits;
  /** The stage deadline on the run's active clock: one control's write is clamped to what is left of it. */
  deadline?: number;
}

const done = (outcome: RowOutcome, reason?: RowReason, value?: string, optionLabel?: string, detail?: string): ApplyResult => ({
  outcome,
  reason,
  value,
  optionLabel,
  detail,
});

function liveMembers(info: ControlInfo, doc: Document): HTMLInputElement[] {
  if (info.members.every((m) => m.isConnected)) return info.members;
  return resolveAll(doc, info.selector).filter((e): e is HTMLInputElement => e.localName === "input");
}

/** The same control with nodes that are still in the document, re-resolved by selector after a re-render. */
function refresh(info: ControlInfo, doc: Document): ControlInfo | null {
  if (info.toggles.length) {
    const el = info.el.isConnected ? info.el : (resolveSelector(doc, info.selector) as HTMLElement | null);
    const group = el ? toggleGroupOf(el) : null;
    return group ? { ...info, el: group[0] as HTMLElement, toggles: group } : null;
  }
  if (info.kind === "radio-group" || info.kind === "checkbox") {
    const members = liveMembers(info, doc);
    return members.length ? { ...info, el: members[0] as HTMLInputElement, members } : null;
  }
  const el = info.el.isConnected ? info.el : (resolveSelector(doc, info.selector) as HTMLElement | null);
  if (info.driver === "date-segments") {
    const seg = el ? dateSegments(el.querySelector("input") as Element) : null;
    return seg ? { ...info, el: seg.wrapper, members: seg.parts } : null;
  }
  return el ? { ...info, el, members: [el as HTMLInputElement] } : null;
}

/** A write abandoned at its budget: its next wait throws, so it cannot land a value after the page moved on to the next control. */
class BudgetSpent extends Error {
  constructor() {
    super("write budget spent");
    this.name = "BudgetSpent";
  }
}

/** The write's context, whose waits and checkpoints throw once its budget is spent. */
function budgeted(ctx: WriteCtx, ms: number): WriteCtx {
  const end = activeNow(ctx) + ms;
  const spent = () => {
    if (activeNow(ctx) >= end) throw new BudgetSpent();
  };
  return {
    ...ctx,
    sleep: async (wait) => {
      spent();
      await ctx.sleep(wait);
      spent();
    },
    checkpoint: async () => {
      spent();
      await ctx.checkpoint();
    },
  };
}

async function withBudget<T>(ctx: WriteCtx, work: Promise<T>, ms: number, onTimeout: T): Promise<T> {
  let cancel = () => {};
  const timeout = new Promise<T>((resolve) => {
    cancel = timerOn(() => activeNow(ctx), activeNow(ctx) + ms, () => resolve(onTimeout));
  });
  try {
    return await Promise.race([work, timeout]);
  } finally {
    cancel();
  }
}

function answersOf(desired: Desired): string[] {
  if (desired.optionLabels?.length) return desired.optionLabels;
  return [desired.optionLabel ?? desired.value];
}

/** A plural payload is refused unless the question is multi-select or the host marked the binding. */
function multiOf(info: ControlInfo, desired: Desired): boolean {
  return info.inputType === "multi_select" || desired.multiSelectBinding === true;
}

function plural(info: ControlInfo, desired: Desired): boolean {
  return answersOf(desired).length > 1 && !multiOf(info, desired);
}

const WHOLE_ADDRESS = /^((home|current|mailing|residential|your) )?(address|location)$/;

function listboxRequest(info: ControlInfo, el: HTMLElement, desired: Desired): ListboxRequest {
  const answers = answersOf(desired);
  const answer = answers[0] as string;
  const label = info.label.text;
  const multi = info.inputType === "multi_select";
  const base: ListboxRequest = { answers, label, multi, limit: multi ? selectionLimit(label, el) : undefined };
  if (desired.address && el.localName === "input") {
    // A box that is the whole address (no "line 1") is also a place search: when the street finds nothing in the applicant's city, the city and state find the place.
    const { city, state } = desired.address;
    const place = city && state && WHOLE_ADDRESS.test(normalize(info.label.direct)) ? [`${city}, ${state}`] : [];
    return { ...base, answers: [desired.value], seeds: [desired.value, ...place], chooser: addressChooser(desired.address), typedStands: place.length === 0 };
  }
  const kind = inPhoneFieldset(el, label) ? "phone-country" : seedKind(label);
  if (multi) return base;
  const seeds = searchSeeds(kind, answer);
  // A place with several comma parts typed into a search box is chosen like a location, whatever the label.
  if (kind === "location" || (kind === "plain" && answer.includes(",") && traitsOf(el).typeahead)) return { ...base, seeds, chooser: geoChooser(answer), cleanupSeed: (answer.split(",")[0] as string).trim() };
  if (kind === "phone-country") {
    // The trigger often shows only the dial code, so the option's own code verifies it.
    const accept = (display: string, row: { label: string }) => showsDialCode(display, row.label) || displayMatches(display, row.label);
    return { ...base, seeds, chooser: phoneCountryChooser(countryPart(answer) || answer), accept };
  }
  if (kind === "degree") return { ...base, seeds, chooser: degreeChooser(answer) };
  // A field of study a list spells differently is the one row that is a whole-word part of it or holds it, the rule a Select2 catalog keeps.
  if (kind === "study") return { ...base, seeds, chooser: (rows) => select2Pick(rows, answer, label) };
  if (countryNames(answer).length > 1) return { ...base, seeds, chooser: countryChooser(answer, traitsOf(el).reactSelect) };
  return { ...base, seeds };
}

/**
 * A degree a native list spells differently is chosen as a listbox chooses it:
 * the one option of its level (Citadel's "Bachelors" for "Bachelor of Science
 * (B.S)"); an answer no level names, or a tie, is left to the list's own match.
 */
function nativeDegree(sel: HTMLSelectElement, answers: string[], label: string): string[] {
  const answer = answers[0] as string;
  if (sel.multiple || answers.length !== 1 || seedKind(label) !== "degree" || !degreeLevel(answer) || "option" in findOption(sel, answer, false)) return answers;
  const rows = Array.from(sel.options)
    .filter((o, i) => !isPlaceholderOption(o, i))
    .map((o) => ({ el: o as HTMLElement, label: optionText(o), value: o.value, selected: o.selected, disabled: o.disabled }));
  const hit = degreeChooser(answer)(rows);
  return hit ? [hit.label] : answers;
}

const HIERARCHICAL = /how did you hear|hear about|referral source|source of/i;
/** General channels a source question offers, nearest to a job board first. */
const SOURCE_FALLBACK = ["Job Board", "Website", "Internet", "Other"];

/** A Workday search prompt: skills are a list, a menu of categories is drilled, every other prompt is searched. */
async function writePrompt(info: ControlInfo, input: HTMLInputElement, desired: Desired, ctx: WriteCtx): Promise<WriteOutcome> {
  if (plural(info, desired)) return { ok: false, reason: "format-not-accepted" };
  const answers = answersOf(desired);
  if (info.inputType === "multi_select") {
    const list = answers.length === 1 ? (answers[0] as string).split(",").map((s) => s.trim()).filter(Boolean) : answers;
    return writeWorkdayPrompt(input, { answers: list.slice(0, MAX_SKILLS), label: info.label.text }, ctx);
  }
  if (HIERARCHICAL.test(info.label.text)) {
    let menu: string[] = [];
    const out = await drillWorkdayMenu(input, answers, ctx, (labels) => (menu = labels));
    ctx.log("menu-drill", out.ok ? { selector: info.selector, option: out.optionLabel ?? "" } : { selector: info.selector, reason: out.reason, detail: out.detail ?? "" });
    if (out.ok || !out.abstain) return out;
    const searched = await writeWorkdayPrompt(input, { answers, label: info.label.text }, ctx);
    ctx.log("prompt-search", searched.ok ? { selector: info.selector, option: searched.optionLabel ?? "" } : { selector: info.selector, reason: searched.reason, detail: searched.detail ?? "" });
    if (searched.ok) return searched;
    // The owner's rule: the source question is not worth a stop.
    // When neither the menu nor a search holds the answer, the general channel
    // the tenant offers is taken, the nearest first.
    for (const seed of SOURCE_FALLBACK) {
      const near = await writeWorkdayPrompt(input, { answers: [seed], label: info.label.text }, ctx);
      ctx.log("prompt-fallback", near.ok ? { selector: info.selector, seed, option: near.optionLabel ?? "" } : { selector: info.selector, seed, reason: near.reason });
      if (near.ok) return near;
    }
    // The row carries what the menu holds, so the check can choose among it.
    return { ...searched, detail: `menu: ${out.detail}; search: ${searched.detail ?? ""}`, options: menu.length ? menu : undefined };
  }
  const out = await writeWorkdayPrompt(input, { answers, label: info.label.text, degree: desired.degree }, ctx);
  ctx.log("prompt-search", out.ok ? { selector: info.selector, option: out.optionLabel ?? "" } : { selector: info.selector, reason: out.reason, detail: out.detail ?? "" });
  return out;
}

async function writeOnce(info: ControlInfo, el: HTMLElement, shaped: string, perChar: boolean, desired: Desired, ctx: WriteCtx): Promise<WriteOutcome> {
  const answers = answersOf(desired);
  switch (info.driver) {
    case "toggle":
      if (plural(info, desired)) return { ok: false, reason: "format-not-accepted" };
      return writeToggles(info.toggles, answers, ctx);
    case "calendar":
      return writeCalendar(el, desired.value, ctx);
    case "select2":
      return writeSelect2(el as HTMLSelectElement, answers, info.label.text, ctx);
    case "dijit":
      return writeDijit(el, answers[0] as string, ctx);
    case "sdf":
      return writeSdf(el, answers[0] as string, ctx);
    case "typeahead-js":
      return writeTypeaheadJs(el as HTMLInputElement, shaped, ctx);
    case "date-segments":
      return writeDateSegments(info.members, desired.value, ctx, segmentOptions(info.label.text, info.owner === "matcher"));
    case "workday-prompt":
      return writePrompt(info, el as HTMLInputElement, desired, ctx);
    case "react-widgets":
      return writeRwDropdown(el, answers[0] as string, ctx);
    case "pcty-select":
      return writePctySelect(el as HTMLInputElement, answers[0] as string, ctx);
    case "pcty-text":
      return ctx.main ? writePctyText(el as HTMLInputElement, shaped, ctx) : writeText(el as HTMLInputElement, shaped, perChar, ctx);
    case "sr-component":
      if (plural(info, desired)) return { ok: false, reason: "format-not-accepted" };
      return writeSrComponent(el as HTMLInputElement, info.kind === "custom-listbox" ? (answers[0] as string) : desired.value, ctx, desired.place);
    case "lever-location":
      return writeLeverLocation(el as HTMLInputElement, desired.value, ctx);
    case "icims":
      return writeIcims(el as HTMLSelectElement, answers[0] as string, ctx);
    case "oracle-cx":
      if (plural(info, desired)) return { ok: false, reason: "format-not-accepted" };
      return writeOracleSelect(el as HTMLInputElement, answers[0] as string, ctx, oracleDrive(el, info.label.text), desired.address);
    case "oracle-address":
      return writeOracleAddress(el as HTMLInputElement, desired.value, desired.address ?? {}, ctx);
    case "listbox": {
      if (plural(info, desired)) return { ok: false, reason: "format-not-accepted" };
      let out = await writeListbox(el, listboxRequest(info, el, desired), ctx);
      // A list asked without its options was answered with the first of a ranking: one that holds none of it is searched for the next.
      for (const alt of multiOf(info, desired) ? [] : (desired.alternatives ?? [])) {
        if (out.ok || out.reason !== "option-not-found") break;
        await ctx.checkpoint();
        out = await writeListbox(el, listboxRequest(info, el, { ...desired, value: alt, optionLabel: alt, alternatives: undefined }), ctx);
      }
      return out;
    }
    default:
  }
  switch (info.kind) {
    case "text":
    case "textarea":
      if (el.localName === "input" && /\bpac-target-input\b/.test(el.getAttribute("class") ?? "")) return writePlaces(el as HTMLInputElement, shaped, ctx);
      if (tinymceEditor(el)) return writeTinymce(el as HTMLTextAreaElement, shaped, ctx);
      return writeText(el as HTMLInputElement | HTMLTextAreaElement, shaped, perChar, ctx);
    case "select":
      return writeSelect(el as HTMLSelectElement, nativeDegree(el as HTMLSelectElement, answers, info.label.text), ctx, info.label.text);
    case "contenteditable":
      return writeEditable(el, shaped, ctx);
    case "radio-group":
    case "checkbox": {
      const members = liveMembers(info, ctx.doc);
      if (!members.length) return { ok: false, reason: "control-detached" };
      return writeChoice(members, { labels: answers, value: desired.value, multi: multiOf(info, desired) }, ctx);
    }
    default:
      return { ok: false, reason: "driver-unavailable" };
  }
}

/** Whether the control already shows the desired answer. */
function alreadyHolds(info: ControlInfo, el: HTMLElement, shaped: string, desired: Desired): boolean {
  if (info.toggles.length) return togglesHold(info.toggles, answersOf(desired)[0] as string);
  if (info.driver === "date-segments") {
    // An answer the segments cannot take is never held by empty segments.
    const want = segmentIso(info.members, desired.value, segmentOptions(info.label.text, false));
    return want !== null && readInfo(info) === want;
  }
  if (info.driver === "listbox" || info.driver === "sdf" || info.driver === "dijit" || info.driver === "workday-prompt" || info.driver === "oracle-cx" || info.driver === "oracle-address" || info.driver === "icims" || info.driver === "lever-location" || info.driver === "react-widgets" || info.driver === "pcty-select") {
    const shown = readInfo(info);
    if (shown === "" || holdsSearchText(info.el)) return false;
    if (info.inputType === "multi_select") {
      // The widget's own chips: a label may hold the ", " its display joins them with ("Atlanta, GA").
      const have = committedTokens(info.el) ?? shown.split(", ");
      return answersOf(desired).every((a) => have.some((h) => normalize(h) === normalize(a) && signsAgree(h, a)));
    }
    return answersOf(desired).some((a) => displayMatches(shown, a) && signsAgree(shown, a));
  }
  if (info.driver === "calendar") return readInfo(info) === desired.value;
  if (info.kind === "select") {
    const sel = el as HTMLSelectElement;
    const found = findOption(sel, answersOf(desired)[0] as string, sel.options.length > 100);
    return "option" in found && found.option.selected && !sel.multiple;
  }
  if (info.kind === "radio-group" || info.kind === "checkbox") {
    return choiceHolds(info.members, { labels: answersOf(desired), value: desired.value, multi: multiOf(info, desired) });
  }
  return sameValue(readInfo(info), shaped, el);
}

/**
 * The text a tick would assent to: a lone box answers for its own label, a
 * choice group answered Yes for its question. Null when nothing is ticked.
 */
function assentText(info: ControlInfo, desired: Desired): string | null {
  if (info.toggles.length === 1) {
    const only = info.toggles[0] as HTMLElement;
    // The writer reads the first answer (the option label before the value), so the consent check reads the same one.
    return loneTarget(answersOf(desired)[0] as string, toggleLabel(only)) === true ? `${info.label.text} ${toggleLabel(only)}` : null;
  }
  if (info.toggles.length > 1) return loneTarget(answersOf(desired)[0] as string, "") === true ? info.label.text : null;
  const first = info.members[0] as HTMLInputElement;
  if (info.members.length === 1 && first.type === "checkbox") {
    return loneTarget(desired.value, optionLabel(first)) === true ? `${info.label.text} ${optionLabel(first)}` : null;
  }
  return loneTarget(answersOf(desired)[0] as string, "") === true ? info.label.text : null;
}

/** The option labels a write would choose in a group or a select, a lone box excluded. */
function chosenLabels(info: ControlInfo, desired: Desired): string[] {
  const answers = answersOf(desired);
  if (info.toggles.length > 1) {
    const pick = targetToggle(info.toggles, answers[0] as string);
    return pick ? [toggleLabel(pick)] : [];
  }
  if (info.toggles.length === 1) return [];
  // A menu the driver opens shows its options only once it is open, so the answer itself is what would be chosen.
  if (info.kind === "custom-listbox" || (info.kind === "select" && info.driver !== null)) return answers;
  if (info.kind === "select" && info.driver === null) {
    const sel = info.el as HTMLSelectElement;
    return answers.flatMap((a) => {
      const found = findOption(sel, a, sel.options.length > 100);
      return "option" in found ? [found.option.text] : [];
    });
  }
  const first = info.members[0] as HTMLInputElement | undefined;
  if (!first || !(info.kind === "radio-group" || info.kind === "checkbox") || (info.members.length === 1 && first.type === "checkbox")) return [];
  const multi = first.type === "checkbox" && multiOf(info, desired);
  const picks = multi ? answers.map((a) => bestMember(info.members, a)) : [bestMember(info.members, answers[0] ?? desired.value)];
  return picks.flatMap((m) => (m && m !== "tie" ? [optionLabel(m)] : []));
}

/** The first consent rule an option the write would choose runs into. */
function optionRefusal(info: ControlInfo, desired: Desired, accept: boolean): ConsentRefusal | null {
  for (const label of chosenLabels(info, desired)) {
    const text = assentingOption(label);
    if (text === null) continue;
    const refusal = consentRefusal({ text, markedRequired: info.markedRequired, optional: info.label.optional }, accept);
    if (refusal) return refusal;
  }
  return null;
}

/**
 * Why a "Yes" to a consent question must not be written. A tick box or a choice
 * group answers for any consent wording; a select or custom listbox only for a
 * marketing opt-in. Nothing is chosen instead, so a "No" is never invented.
 */
function answerRefusal(info: ControlInfo, desired: Desired, accept: boolean): ConsentRefusal | null {
  if (desired.ownAnswer) return null;
  const choosing = info.kind === "radio-group" || info.kind === "checkbox" || info.kind === "select" || info.kind === "custom-listbox";
  if (!choosing) return null;
  const assent = assentText(info, desired);
  const menu = info.kind === "select" || info.kind === "custom-listbox";
  const asked = assent !== null && (!menu || consentKind(assent) === "marketing");
  const refusal = asked
    ? consentRefusal({ text: assent, markedRequired: info.markedRequired, optional: info.label.optional }, accept)
    : null;
  return refusal ?? optionRefusal(info, desired, accept);
}

/** Controls that hold a genuine choice the engine keeps unless the matcher flagged it as a page default. */
function holdsGenuineChoice(info: ControlInfo): boolean {
  if (info.driver === "dijit") return dijitHasChoice(info.el);
  // A genuine selection is kept unless the matcher flagged it as a page default.
  if (info.driver === "icims") return true;
  if (info.driver === "listbox" || info.driver === "sdf" || info.driver === "calendar" || info.driver === "workday-prompt" || info.driver === "date-segments" || info.driver === "oracle-cx" || info.driver === "oracle-address" || info.driver === "lever-location" || info.driver === "react-widgets" || info.driver === "pcty-select") return false;
  return info.kind === "select" || info.kind === "radio-group" || info.kind === "checkbox";
}

export async function applyValue(first: ControlInfo, desired: Desired, env: ApplyEnv): Promise<ApplyResult> {
  const { ctx } = env;
  let info = refresh(first, ctx.doc);
  if (!info) return done("failed", "control-detached");
  if (env.edits.edited(info)) return done("skipped", "user-edited");
  const nativeChoice = (info.kind === "radio-group" || info.kind === "checkbox") && info.toggles.length === 0;
  if (!nativeChoice) {
    if ((info.el as HTMLInputElement).disabled || readOnlyBlocks(info.el, info.driver) || info.el.getAttribute("aria-disabled") === "true") return done("skipped", "readonly");
    // A textarea TinyMCE hides is shown as the editor drawn beside it.
    if (!isRendered(tinymceEditor(info.el) ?? info.el)) return done("skipped", "hidden");
  }

  let shaped = desired.value;
  let perChar = false;
  if ((info.kind === "text" || info.kind === "textarea") && (info.driver === null || info.driver === "pcty-text")) {
    const r = shapeValue(info.el, info.label.direct || info.label.text, desired.value, {
      now: ctx.now(),
      currentDate: desired.currentDate,
      phone: desired.phone,
    });
    if (!r.ok) return r.detail ? done("failed", "out-of-range", undefined, undefined, r.detail) : done("failed", "format-not-accepted");
    shaped = r.value;
    perChar = r.perChar;
  }

  const before = readInfo(info);
  if (alreadyHolds(info, info.el, shaped, desired)) {
    // A pre-set native choice still has to tell the page's own state about it.
    if (nativeChoice) await writeOnce(info, info.el, shaped, perChar, desired, ctx);
    // An Ashby Yes/No the page shows pressed is sent again when the saved copy lacks it.
    if (ashbyActive(ctx) && isAshbyYesNo(info) && !(await ashbyHolds(ctx, info.el))) {
      await recommitYesNo(ctx, info);
      if (!(await ashbyHolds(ctx, info.el))) return done("failed", "ashby-save-lost");
      return done("filled", undefined, readInfo(info));
    }
    return done("prefilled", undefined, before);
  }
  if (before !== "" && info.kind !== "checkbox") {
    const ownWrite = env.written.has(info.selector);
    const writtenThisRun = ownWrite && !desired.overwrite;
    // A choice this run made is no selection of the page's to keep when the host asks for it written again.
    const genuineChoice = holdsGenuineChoice(info) && !desired.replaceDefault && !(ownWrite && desired.overwrite);
    if (env.mode === "keep" || writtenThisRun || genuineChoice) return done("prefilled", "existing-value", before);
  }

  const refusal = answerRefusal(info, desired, env.acceptLegalDeclarations);
  if (refusal) return done("skipped", refusal);

  // A Workday referral menu is drilled a server round trip per category, and a skills prompt searches a round trip per skill.
  const roundTrips = info.driver === "workday-prompt" && (HIERARCHICAL.test(info.label.text) || info.inputType === "multi_select");
  const fieldBudget = roundTrips ? MENU_BUDGET_FACTOR * ctx.timing.fieldBudgetMs : ctx.timing.fieldBudgetMs;
  const budget = Math.max(0, Math.min(fieldBudget, env.deadline !== undefined ? env.deadline - activeNow(ctx) : Infinity));
  const timedOut = { ok: false, reason: "timeout" } as WriteOutcome;
  // A write past its budget stops at its next wait (10a Labs: an abandoned school search picked the next row's school).
  const attempt = (target: ControlInfo): Promise<WriteOutcome> =>
    withBudget(
      ctx,
      writeOnce(target, target.el, shaped, perChar, desired, budgeted(ctx, budget)).catch((e: unknown) => {
        if (e instanceof BudgetSpent) return timedOut;
        throw e;
      }),
      budget,
      timedOut,
    );

  // Ashby loses answers when its saves overlap: the earlier saves finish first.
  if (ashbyActive(ctx)) await ashbyBefore(ctx);
  let result = await attempt(info);
  // A listbox pick that landed inside the budget committed the answer even when the wait after it ran out: the choice the widget
  // shows is its committed state, so the row says what the page holds. A text box's typed value proves no commit.
  if (result === timedOut && info.kind === "custom-listbox" && alreadyHolds(info, info.el, shaped, desired)) result = { ok: true };
  // A page that replaces the control (a store-driven re-render) gets one more write; a write that landed before the replacement needs none.
  let retried = false;
  while (!info.el.isConnected || (!result.ok && result.reason === "control-detached")) {
    const next = refresh(info, ctx.doc);
    if (!next || !next.el.isConnected) return done("failed", "control-detached");
    info = next;
    if (result.ok && alreadyHolds(info, info.el, shaped, desired)) break;
    if (retried) return done("failed", "control-detached");
    retried = true;
    result = await attempt(info);
  }
  if (!result.ok) {
    if (result.abstain) return { ...done("abstained", result.reason, undefined, undefined, result.detail), options: result.options };
    return { ...done("failed", result.reason, undefined, undefined, result.detail), options: result.options };
  }

  if (ashbyActive(ctx) && !(await ashbyHolds(ctx, info.el))) {
    // The saved copy lacks the answer: the write is repeated once (a pressed Yes/No is cleared and
    // pressed again), and a second miss is a failed row.
    if (isAshbyYesNo(info)) await recommitYesNo(ctx, info);
    else await attempt(info);
    if (!(await ashbyHolds(ctx, info.el))) return done("failed", "ashby-save-lost");
  }
  const after = readInfo(info);
  env.written.set(info.selector, after);
  return done("filled", undefined, after, result.optionLabel);
}
