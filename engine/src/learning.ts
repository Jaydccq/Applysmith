// Clean-room: docs/provenance.md#CR-ENGINE
// Uncertainty events: one for every control the fill could not answer with
// confidence, with the evidence a person would want and nothing a person would
// not want to leave the page.

import { collapse, winOf } from "./dom";
import { controlSnapshot, maskContacts } from "./fragment";
import { reduceUrl } from "./frames";
import { recognise } from "./profile";
import type { LearningEvent, Row } from "./protocol";
import type { Run } from "./run";
import { rowControlSelectors } from "./row-plan";
import { readInfo, scanControls, type ControlInfo } from "./scan";

const MAX_EVENT_OPTIONS = 25;
/** The longest option label the server accepts, in characters: it refuses a whole event that holds a longer one. */
const MAX_OPTION_CHARS = 200;
const MAX_QUESTION = 1000;

/** Identity fields the applicant's own value is never recorded for. */
const OWN_VALUE = new Set(["fullName", "signatureName", "firstName", "lastName", "preferredLastName", "email", "confirmEmail", "phone"]);

export function eventId(win: Window): string {
  const c = win.crypto as Crypto | undefined;
  if (c?.randomUUID) return c.randomUUID().toLowerCase();
  const hex = () => Math.floor(Math.random() * 16).toString(16);
  return "xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx".replace(/x/g, hex);
}

/** The question as it may leave the page: contact strings masked, bounded. */
function sanitizeQuestion(text: string): string {
  return maskContacts(collapse(text)).slice(0, MAX_QUESTION);
}

/** Whether nothing about a control's value may leave the page. */
export function valueWithheld(info: ControlInfo): boolean {
  return info.sensitivity !== null || OWN_VALUE.has(recognise(info) ?? "");
}

/**
 * The option labels an event carries, and an inventory row a host turns into
 * one: the first 25, each cut to what the server accepts. A sensitive
 * control's options describe a category, never an answer, so it names none.
 */
export function eventOptions(info: ControlInfo): string[] {
  if (valueWithheld(info)) return [];
  return info.options.slice(0, MAX_EVENT_OPTIONS).map((o) => Array.from(o.label).slice(0, MAX_OPTION_CHARS).join(""));
}

export interface EventContext {
  win: Window;
  site: string | null;
}

/** The fields every event carries. */
export function baseEvent(info: ControlInfo, kind: LearningEvent["kind"], c: EventContext): LearningEvent {
  const loc = c.win.location;
  const ev: LearningEvent = {
    eventId: eventId(c.win),
    kind,
    host: loc?.hostname ?? "",
    url: reduceUrl(loc?.href ?? ""),
    selector: info.selector,
    question: sanitizeQuestion(info.label.text),
    // The server knows five control kinds; a textarea or an editable region is a text box to it.
    controlKind: info.wireKind,
    inputType: info.inputType,
    time: Date.now(),
    required: info.required,
    labelSource: info.label.source,
  };
  if (c.site) ev.site = c.site;
  if (info.section.length) ev.section = info.section.map(sanitizeQuestion);
  const ac = info.el.getAttribute("autocomplete");
  if (ac) ev.autocomplete = ac;
  const options = eventOptions(info);
  if (options.length) ev.options = options;
  return ev;
}

function reasonOf(row: Row, info: ControlInfo, inRows: ReadonlySet<string>): string | null {
  if (row.outcome === "abstained") {
    // A control of a repeated work or education row belongs to the profile's history entry, never to the answer bank: its abstain teaches nothing.
    if (inRows.has(info.selector)) return null;
    // An identity control the profile pass owns, or one that holds a value now, is not uncertain.
    if (info.owner === "profile" && OWN_VALUE.has(recognise(info) ?? "")) return null;
    if (readInfo(info) !== "") return null;
    const why = row.matcherReason ?? row.reason ?? "no-entry";
    return why === "no-match" || why === "no-answer" ? "no-entry" : why;
  }
  if (row.outcome === "failed") return row.reason === "unsafe-click" ? "unsafe-click" : "failed";
  if (row.outcome === "filled" && row.review) return "review-flagged";
  if (row.outcome === "skipped" && row.reason === "no-lane-claimed" && info.kind !== "file" && info.admitted && readInfo(info) === "") return "unrecognised";
  return null;
}

/** What an event says about a control no label could name: the server refuses an empty question. */
const UNLABELED = "(unlabeled control)";

function eventFor(run: Run, row: Row, info: ControlInfo, reason: string): LearningEvent {
  const win = winOf(run.doc.documentElement);
  const ev = baseEvent(info, "uncertainty", { win, site: run.site });
  ev.question = ev.question || UNLABELED;
  ev.reason = reason;
  ev.fillOutcome = row.outcome;
  if (!valueWithheld(info)) {
    const now = readInfo(info);
    if (now !== "") ev.current = now;
    // A failed write and a control with no label of its own carry the block around them, redacted.
    if (row.outcome === "failed" || info.label.source === "none") {
      const frag = controlSnapshot(info.el, info.label.text);
      if (frag) ev.fragment = frag.html;
    }
  }
  return ev;
}

/** The reason of the observe-only event for a required control the page had already answered; the server keeps it out of triage. */
const ALREADY_SATISFIED = "already-satisfied";

/** A required row the page itself answered (its parser, the browser, a returning candidate's saved value) while this run wrote nothing there. */
function alreadySatisfied(row: Row, written: ReadonlyMap<string, string>): boolean {
  return row.outcome === "prefilled" && row.required && !written.has(row.selector);
}

/** An observe-only record of a satisfied control: the same evidence as an uncertainty event, never its value. */
function satisfiedEvent(run: Run, info: ControlInfo): LearningEvent {
  const ev = baseEvent(info, "uncertainty", { win: winOf(run.doc.documentElement), site: run.site });
  ev.question = ev.question || UNLABELED;
  ev.reason = ALREADY_SATISFIED;
  ev.fillOutcome = "prefilled";
  if (!valueWithheld(info)) {
    const frag = controlSnapshot(info.el, info.label.text);
    if (frag) ev.fragment = frag.html;
  }
  return ev;
}

/**
 * The events of a finished fill, from its rows and a fresh look at the page. A control whose evidence cannot be read is left out; the others still count.
 * `written` holds the controls this run wrote: a required row that ended prefilled without one is reported as already satisfied.
 */
export function uncertaintyEvents(run: Run, written: ReadonlyMap<string, string> = new Map()): LearningEvent[] {
  const scanned = scanControls(run.doc);
  // A closed listbox shows a fresh scan no options: its event names the ones the run read when it opened it.
  for (const info of scanned) if (info.needsOptions) info.options = run.listOptions.get(info.selector) ?? info.options;
  const infos = new Map(scanned.map((i) => [i.selector, i]));
  const inRows = rowControlSelectors(scanned, run.doc);
  const out: LearningEvent[] = [];
  for (const row of run.rows.values()) {
    // A row whose control no longer resolves has no evidence worth sending.
    const info = infos.get(row.selector);
    if (!info) continue;
    try {
      if (alreadySatisfied(row, written)) {
        out.push(satisfiedEvent(run, info));
        continue;
      }
      const reason = reasonOf(row, info, inRows);
      if (reason) out.push(eventFor(run, row, info, reason));
    } catch {
      /* the telemetry of one control never fails the fill */
    }
  }
  return out;
}
