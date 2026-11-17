// Clean-room: docs/provenance.md#CR-ENGINE
// The profile pass for repeated rows: each row is answered from the entry it
// is bound to and from nothing else, and on a host whose résumé parser guesses
// into the rows the applicant's entry replaces the guess.

import { normalize } from "./dom";
import type { ProfileItem } from "./profile";
import type { ApplicantSnapshot } from "./protocol";
import { bindRows, collectRows, entriesOf, entryName, fieldOf, linksOf, readRows, rowValue, sameName, type Entity, type Entry, type Row } from "./rows";
import { readInfo, type ControlInfo } from "./scan";

/** Hosts whose own résumé parser fills these rows with guesses; the applicant's entry wins there. */
const PARSER_HOSTS = new Set([
  "workday",
  "workday-site",
  "successfactors",
  "dayforce",
  "paylocity",
  "smartrecruiters",
  "breezy",
  "rippling",
  "ukg",
  "oracle",
  "icims",
  "ashby",
  "lever",
  "adp-workforcenow",
  "adp-recruiting",
]);

export const isParserHost = (site: string | null): boolean => site !== null && PARSER_HOSTS.has(site);

/** The rows of the page, with what each shows. */
export function pageRows(infos: ControlInfo[], doc: Document): Row[] {
  const rows = collectRows(infos, doc);
  readRows(rows, readInfo);
  return rows;
}

/** Selectors of every control that sits in a repeated row: the home profile never reaches them. */
export function rowControlSelectors(infos: ControlInfo[], doc: Document): Set<string> {
  return new Set(collectRows(infos, doc).flatMap((r) => r.controls.map((c) => c.info.selector)));
}

const ROW_CAPTION: Partial<Record<Entity, string>> = { education: "Education", employment: "Employment" };

/**
 * Each history row control's row as a numbered caption ("Education 2"): the
 * matcher reads it as one row of several and keeps a single-value answer (the
 * latest major) out of it, so the row's own entry answers.
 */
export function rowCaptions(infos: ControlInfo[], doc: Document): Map<string, string> {
  const out = new Map<string, string>();
  for (const r of collectRows(infos, doc)) {
    const name = ROW_CAPTION[r.entity];
    if (name) for (const c of r.controls) out.set(c.info.selector, `${name} ${r.ordinal + 1}`);
  }
  return out;
}

/** The boxes of an education block on a page that numbers nothing (Oracle's education editor): one entry fills them all. */
export function standaloneEducationSelectors(infos: ControlInfo[], inRows: Set<string>): Set<string> {
  return new Set(standaloneBoxes(infos, inRows).map((i) => i.selector));
}

function standaloneBoxes(infos: ControlInfo[], inRows: Set<string>): ControlInfo[] {
  return infos.filter((i) => (i.kind === "text" || i.kind === "select") && !inRows.has(i.selector) && STANDALONE_EDU.test(normalize(i.label.direct)));
}

const same = (a: string, b: string): boolean => normalize(a) === normalize(b);

function item(info: ControlInfo, field: string, value: string, site: string | null, claimed: Set<string>): ProfileItem {
  const key = `row:${field}` as const;
  if (claimed.has(info.selector)) return { info, key, action: "no-data" };
  const shown = readInfo(info);
  const parser = isParserHost(site);
  if (shown !== "" && (!parser || same(shown, value) || info.kind === "checkbox")) return { info, key, action: "prefilled", value };
  return { info, key, action: "write", value, overwrite: parser && shown !== "" };
}

/** The boxes of an education block on a page that numbers nothing. */
const STANDALONE_EDU = /^(school|school name|name of school|university|college|institution|degree|degree type|highest degree|major|field of study|area of study|discipline|graduation date|date of graduation)$/;
const STANDALONE_FIELDS = new Set(["school", "degree", "major", "end"]);

/** The entry that ends last; an entry with no end date is still going on and is the latest. */
function latestEntry(entries: Entry[]): Entry {
  const end = (e: Entry): number => {
    const raw = String(e.endDate ?? e.end ?? "");
    return /^(present|current|now)?$/i.test(raw.trim()) ? Number.MAX_SAFE_INTEGER : Date.parse(raw) || 0;
  };
  return entries.reduce((best, e) => (end(e) > end(best) ? e : best), entries[0] as Entry);
}

/**
 * A school, degree, major and graduation date outside any numbered row all
 * come from one entry: the school the page already shows, else the latest.
 */
function standaloneEducation(infos: ControlInfo[], rows: Row[], applicant: ApplicantSnapshot, site: string | null, claimed: Set<string>): ProfileItem[] {
  const boxes = standaloneBoxes(infos, new Set(rows.flatMap((r) => r.controls.map((c) => c.info.selector))));
  const entries = entriesOf(applicant, "education");
  if (!boxes.length || !entries.length) return [];
  const shown = boxes.map((b) => ({ b, field: fieldOf("education", b) })).find((x) => x.field === "school" && readInfo(x.b) !== "");
  const named = shown ? entries.find((e) => sameName(entryName("education", e), readInfo(shown.b))) : undefined;
  const entry = named ?? (shown ? undefined : latestEntry(entries));
  if (!entry) return [];
  const out: ProfileItem[] = [];
  for (const b of boxes) {
    const field = fieldOf("education", b);
    const value = STANDALONE_FIELDS.has(field) ? rowValue("education", field, entry, b) : null;
    if (value !== null) out.push(item(b, field, value, site, claimed));
  }
  return out;
}

/**
 * Items for the history rows and the website rows. A row with no entry
 * (junk, a duplicate, or past the last entry) is left alone, and a section
 * the profile has no entries for is never touched.
 */
export function planRows(infos: ControlInfo[], applicant: ApplicantSnapshot, claimed: Set<string>, site: string | null, doc: Document): ProfileItem[] {
  const rows = pageRows(infos, doc);
  const out: ProfileItem[] = [];
  for (const entity of ["employment", "education"] as Entity[]) {
    const entries = entriesOf(applicant, entity);
    if (!entries.length) continue;
    const bound = bindRows(rows, entries, entity);
    // A lone education row the page numbers by its ids has no place among others: it answers from the latest entry.
    const lone = entity === "education" && bound.length === 1 ? bound[0] : undefined;
    if (lone?.entry && lone.row.identity === "" && !lone.row.fixedBase) lone.entry = latestEntry(entries);
    for (const b of bound) {
      if (!b.entry) continue;
      // A row's country first: it decides the shape of the rest of the block.
      const gate = (f: string) => f === "country" || f === "graduated";
      const ordered = [...b.row.controls.filter((c) => gate(c.field)), ...b.row.controls.filter((c) => !gate(c.field))];
      const max = parseInt((b.row.controls.find((x) => x.field === "address1")?.info.el.getAttribute("maxlength")) ?? "", 10);
      for (const c of ordered) {
        const value = rowValue(entity, c.field, b.entry, c.info, max > 0 ? max : undefined);
        if (value === null) continue;
        const it = item(c.info, c.field, value, site, claimed);
        if (c.field === "major") it.degree = rowValue(entity, "degree", b.entry, c.info) ?? undefined;
        out.push(it);
      }
    }
  }
  out.push(...standaloneEducation(infos, rows, applicant, site, claimed));
  const links = linksOf(applicant);
  const web = rows.filter((r) => r.entity === "websites").sort((a, b) => a.ordinal - b.ordinal);
  if (links.length && web.length) {
    const urlOf = (r: Row) => r.controls.find((c) => c.field === "url");
    const current = web.map((r) => (urlOf(r) ? readInfo((urlOf(r) as { info: ControlInfo }).info) : ""));
    // Rows that already hold distinct addresses of the applicant's own are left as they are.
    const settled = current.every((v) => v !== "") && new Set(current.map(normalize)).size === current.length && current.every((v) => links.some((l) => same(l, v)));
    // Where a row keeps what it shows, a blank row takes the first link no row shows: a kept link is never written twice.
    const free = links.filter((l) => !current.some((v) => same(l, v)));
    for (const [i, r] of web.entries()) {
      const c = urlOf(r);
      if (!c) continue;
      const want = !isParserHost(site) && !settled && current[i] === "" ? free.shift() : links[i];
      if (want === undefined) continue;
      out.push(settled ? { info: c.info, key: "row:url", action: "prefilled", value: current[i] } : item(c.info, "url", want, site, claimed));
    }
  }
  return out;
}
