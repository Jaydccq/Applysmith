// Clean-room: docs/provenance.md#CR-ENGINE
// The summary of a run, the inventory of a page, and the closing passes that
// look at what the run left untouched.

import { DELIBERATE_BLANK, DELIBERATE_MATCHER, makeRow } from "./answers";
import { walkElements, type Win } from "./dom";
import { reduceUrl } from "./frames";
import type { InspectRow, Row, Summary } from "./protocol";
import { readUnclaimedListboxes } from "./prescan";
import type { Run } from "./run";
import { resolveLabel, sectionPath } from "./labels";
import { eventOptions } from "./learning";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import { select2Owning } from "./select2";
import { isParseOnly } from "./stage-resume";
import { selectorFor } from "./selector";
import { isRendered } from "./visibility";
import { resolveSelector } from "./selector";

function documentIndex(doc: Document): Map<Element, number> {
  const index = new Map<Element, number>();
  let i = 0;
  walkElements(doc.body ?? doc, (el) => {
    index.set(el, i++);
  });
  return index;
}

function sortRows(rows: Row[], doc: Document): Row[] {
  const index = documentIndex(doc);
  const pos = (r: Row): number => {
    const el = resolveSelector(doc, r.selector);
    return (el && index.get(el)) ?? Number.MAX_SAFE_INTEGER;
  };
  return rows
    .map((row, i) => ({ row, i, p: pos(row) }))
    .sort((a, b) => a.p - b.p || a.i - b.i)
    .map((x) => x.row);
}

export interface SummaryFlags {
  blockedByDialog?: boolean;
  notAnApplication?: boolean;
  partial?: boolean;
}

function requiredUnfilled(r: Row): boolean {
  if (!r.required) return false;
  if (r.outcome !== "skipped" && r.outcome !== "abstained" && r.outcome !== "failed") return false;
  if (r.reason && DELIBERATE_BLANK.has(r.reason) && r.matcherReason !== "ask-me") return false;
  if (r.matcherReason && DELIBERATE_MATCHER.has(r.matcherReason)) return false;
  return true;
}

export function buildSummary(run: Run, win: Win, flags: SummaryFlags = {}): Summary {
  const rows = sortRows(Array.from(run.rows.values()), run.doc);
  const count = (f: (r: Row) => boolean) => rows.filter(f).length;
  const summary: Summary = {
    host: win.location?.hostname ?? "",
    url: reduceUrl(win.location?.href ?? ""),
    rows,
    counts: {
      filled: count((r) => r.outcome === "filled"),
      prefilled: count((r) => r.outcome === "prefilled"),
      review: count((r) => r.review),
      abstained: count((r) => r.outcome === "abstained"),
      failed: count((r) => r.outcome === "failed"),
      skipped: count((r) => r.outcome === "skipped"),
      requiredUnfilled: count(requiredUnfilled),
    },
    drafts: run.drafts.slice(),
  };
  if (run.site) summary.site = run.site;
  if (run.matchUnavailable) summary.matchUnavailable = true;
  if (flags.blockedByDialog) summary.blockedByDialog = true;
  if (flags.notAnApplication) summary.notAnApplication = true;
  if (flags.partial) summary.partial = true;
  return summary;
}

const UNKNOWN_ROLES = "[role=slider], [role=spinbutton], [role=searchbox], [role=textbox], [role=tree], [role=meter]";

/**
 * Interactive widgets no scanner and no driver understands: each is listed
 * as skipped, so the person sees what the page holds that the engine cannot
 * fill. Nothing is written to them.
 */
export function unknownControlRows(run: Run): void {
  const infos = scanControls(run.doc);
  const covered = (el: Element) =>
    infos.some((i) => i.el === el || i.el.contains(el) || i.el === select2Owning(el) || i.members.some((m) => m === el || m.contains(el)) || i.toggles.some((t) => t === el || t.contains(el)));
  // The scan reads controls inside open shadow roots, so the widgets it leaves are looked for there too.
  walkElements(run.doc, (el) => {
    if (!el.matches(UNKNOWN_ROLES) || covered(el) || !isRendered(el) || el.closest("nav, footer, [role=search]") || el.getAttribute("aria-hidden") === "true") return;
    const selector = selectorFor(el);
    if (run.rows.has(selector)) return;
    const label = resolveLabel(el);
    run.setRow({
      selector,
      label: label.text,
      section: sectionPath(el, label.titleEl),
      lane: "profile",
      kind: "text",
      required: el.getAttribute("aria-required") === "true" || label.marked,
      outcome: "skipped",
      review: false,
      reason: "driver-unavailable",
    });
  });
}

/**
 * Every fillable control no lane touched gets a row: prefilled when the page
 * already filled it. An empty required custom listbox is opened to read its
 * options, so the check after the fill can answer it.
 */
export async function unclaimedRows(run: Run): Promise<void> {
  const empty: ControlInfo[] = [];
  for (const info of scanControls(run.doc)) {
    if (run.rows.has(info.selector)) continue;
    const value = readInfo(info);
    if (info.answered || value !== "") run.setRow(makeRow(info, "profile", "prefilled", { reason: "no-lane-claimed", value }));
    else empty.push(info);
  }
  const read = await readUnclaimedListboxes(run, empty);
  for (const info of empty) {
    info.options = read.get(info.selector) ?? info.options;
    run.setRow(makeRow(info, "profile", "skipped", { reason: "no-lane-claimed" }));
  }
}

/**
 * Read-only inventory: one row per control, with its read-back value. A
 * password or one-time-code box and an attachment slot are listed with
 * `inspectionOnly` so the host knows the page's structure; the engine never
 * fills them, and a withheld control carries no value and no options.
 */
export function inspectPage(doc: Document): InspectRow[] {
  return scanControls(doc, { inventory: true }).map((info) => {
    const row: InspectRow = {
      selector: info.selector,
      label: info.label.text,
      section: info.section,
      kind: info.kind,
      required: info.required,
    };
    const options = eventOptions(info);
    if (options.length) row.options = options;
    if (info.kind === "file") {
      row.inspectionOnly = true;
      if (isParseOnly(info)) row.parserOnly = true;
      const name = readInfo(info);
      if (name) row.fileName = name;
    } else if (info.sensitivity === "never") {
      row.inspectionOnly = true;
    } else if (info.sensitivity !== "withhold") {
      const v = readInfo(info);
      if (v !== "") row.value = v;
    }
    return row;
  });
}
