// Clean-room: docs/provenance.md#CR-ENGINE
// Repeated rows: work history, education and website rows. A row is read from
// the structure around its controls, bound to the profile entry it already
// shows (or to its position), and answered only from that entry; the
// applicant's home values never reach it.

import { collapse, composedAncestors, flatText, normalize } from "./dom";
import type { ApplicantSnapshot } from "./protocol";
import type { ControlInfo } from "./scan";

export type Entity = "employment" | "education" | "websites";

/** A history entry as the snapshot carries it; the engine accepts the profile's own key spellings too. */
export type Entry = Record<string, unknown>;

export interface RowRef {
  entity: Entity;
  /** Rows of one section share a group key. */
  group: string;
  /** The page's own row number, in whatever base the page uses. */
  num: number;
  /** A heading or one-based id number: the entry is num - 1. */
  fixedBase: boolean;
}

export interface Row {
  entity: Entity;
  group: string;
  /** The place in the page's order, and the entry index when the page numbers its rows. */
  ordinal: number;
  fixedBase: boolean;
  root: Element;
  controls: { info: ControlInfo; field: Field }[];
  /** The employer or school the row shows. */
  identity: string;
  /** The title the row shows, or its degree and major. */
  detail: string;
  /** The year the row's start date shows. */
  since: string;
}

export type Field =
  | "company"
  | "title"
  | "location"
  | "start"
  | "end"
  | "startMonth"
  | "startYear"
  | "endMonth"
  | "endYear"
  | "current"
  | "school"
  | "degree"
  | "major"
  | "gpa"
  | "gpaScale"
  | "url"
  | "graduated"
  | "country"
  | "state"
  | "postal"
  | "city"
  | "address1"
  | "address2"
  | "other";

const text = (e: Entry | undefined, ...keys: string[]): string => {
  for (const k of keys) {
    const v = e?.[k];
    if (typeof v === "string" && v.trim()) return v.trim();
  }
  return "";
};

export const entriesOf = (a: ApplicantSnapshot, entity: Entity): Entry[] => {
  if (entity === "websites") return [];
  return ((entity === "employment" ? a.history?.employment : a.history?.education) ?? []) as Entry[];
};

export function entryName(entity: Entity, e: Entry): string {
  return entity === "employment" ? text(e, "employer") : text(e, "school", "institution");
}

/** The profile's links in the order a row list takes them. */
export function linksOf(a: ApplicantSnapshot): string[] {
  const l = a.links ?? {};
  return Array.from(new Set([l.linkedin, l.github, l.portfolio].filter((v): v is string => !!v && v.trim() !== "")));
}

// --- which row a control belongs to --------------------------------------------

const EDU = /education|school|universit|college|academic|degree/i;
const JOB = /work|employ|experience|job|position|history|career/i;
const WEB = /website|web site|link|portfolio|url/i;

function entityOf(word: string): Entity | null {
  if (EDU.test(word)) return "education";
  if (WEB.test(word) && !JOB.test(word)) return "websites";
  if (JOB.test(word)) return "employment";
  return null;
}

/** A history of something else: a heading or an Add control that names one names no job section. */
export const OTHER_HISTORY = /\b(address|residence|volunteer|military|salary)/i;
const JOB_WORD = /\b(work|employ|experience|job|position|history|career)/i;

/** A numbered heading is prose: a job section is named by a word that starts with a job word, so "Network 1" and "Coursework 1" name none. */
function headingEntity(word: string): Entity | null {
  if (OTHER_HISTORY.test(word)) return null;
  const entity = entityOf(word);
  return entity === "employment" && !JOB_WORD.test(word) ? null : entity;
}

const WORKDAY_ID = /^(workExperience|education|websites?|relevantWebsites?)-(\d+)--/i;
const UNDERSCORE_ROW = /(?:^|[\s_.])([A-Za-z]*(?:History|employer|employment|education|school|experience)[A-Za-z]*)[_.](\d+)[_.]/;
/** Rails-style nested names number a row inside brackets: profile[education][new0][school] (Citadel), user[employments][1][title]. */
const NESTED_ROW = /\[([A-Za-z_]*(?:History|histor|employer|employment|education|school|experience)[A-Za-z_]*)\]\[(?:new_?)?(\d+)\]/i;
/** Phenom numbers a row in brackets after the section's name: educationData[1].degree (HPE). */
const INDEXED_ROW = /^([A-Za-z]*(?:History|histor|employer|employment|education|school|experience)[A-Za-z]*)\[(\d+)\]\./i;
/** Paylocity names a row's field first and its zero-based number last: workHistory.companyName.0, txt-workHistory-startDate-0. */
const PCTY_ROW = /^(?:txt-)?(workHistory|educationHistory)[.-][A-Za-z]+[.-](\d+)$/;
const HEADING = /^(?:[A-Za-z()&/'-]+\s+){0,4}?[A-Za-z()&/'-]+\s+(\d{1,2})$/;
const HEADING_TAGS = "h1, h2, h3, h4, h5, h6, legend, [role=heading], summary";

function idName(el: Element): string {
  const own = `${el.getAttribute("id") ?? ""} ${el.getAttribute("name") ?? ""}`.trim();
  if (UNDERSCORE_ROW.test(own) || WORKDAY_ID.test(own)) return own;
  // A select built from a table keeps its name on the hidden input inside it, and only there.
  const inner = el.querySelector('input[type="hidden"]');
  return inner ? `${own} ${idName0(inner)}`.trim() : own;
}

const idName0 = (el: Element): string => `${el.getAttribute("id") ?? ""} ${el.getAttribute("name") ?? ""}`.trim();

/** The element's parent in the composed tree: a shadow root's host counts as the parent of its children. */
const up = (el: Element): Element | null => el.parentElement ?? ((el.getRootNode() as ShadowRoot).host ?? null);

/** The outermost host in the light tree: a control inside a component's shadow root sits where the component sits. */
const lightOf = (el: Element): Element => {
  let node = el;
  for (let host = (node.getRootNode() as ShadowRoot).host; host; host = (node.getRootNode() as ShadowRoot).host) node = host;
  return node;
};

/** The nearest heading text above the control that ends in a row number. Only the direct children and the nearest siblings of each ancestor are looked at, so the cost stays small on a large form. */
function headingNumber(el: Element): { num: number; word: string } | null {
  let node: Element | null = el;
  for (let depth = 0; node && depth < 7; depth++, node = up(node)) {
    // A heading numbers the controls that follow it, not the ones above it.
    const own = Array.from(node.children).find((c) => c.matches(HEADING_TAGS) && (c.compareDocumentPosition(el) & 4) !== 0);
    const candidates: Element[] = own ? [own] : [];
    let sib = node.previousElementSibling;
    for (let i = 0; sib && i < 3; i++, sib = sib.previousElementSibling) {
      if (sib.matches(HEADING_TAGS)) candidates.push(sib);
      else {
        const inner = Array.from(sib.children).find((c) => c.matches(HEADING_TAGS));
        if (inner) candidates.push(inner);
      }
    }
    for (const c of candidates) {
      const t = collapse(flatText(c, (e) => e.matches("input, select, textarea, button")));
      const m = HEADING.exec(t);
      if (m) return { num: parseInt(m[1] as string, 10), word: t };
    }
  }
  return null;
}

/** Row-bearing names of hidden inputs in an ancestor, when every one of them agrees. */
function hiddenRow(el: Element): { group: string; num: number } | null {
  let node = up(el);
  for (let depth = 0; node && depth < 5; depth++, node = up(node)) {
    const found = new Set<string>();
    node.querySelectorAll('input[type="hidden"]').forEach((h) => {
      const m = UNDERSCORE_ROW.exec(idName(h));
      if (m) found.add(`${m[1]}|${m[2]}`);
    });
    if (found.size === 1) {
      const [group, num] = (Array.from(found)[0] as string).split("|");
      return { group: group as string, num: parseInt(num as string, 10) };
    }
    if (found.size > 1) return null;
  }
  return null;
}

const GREENHOUSE_ROW = /^([a-z][a-z-]*)--(\d+)$/i;
/** Greenhouse's employment block numbers its fields with one dash (company-name-0, start-date-month-0); the Current role box adds _1. */
const GREENHOUSE_WORK_ROW = /^(?:company-name|title|start-date-month|start-date-year|end-date-month|end-date-year|current-role)-(\d+)(?:_\d+)?$/;

function baseEntity(word: string): Entity | null {
  return /discipline|major/.test(word) ? "education" : entityOf(word);
}

/**
 * The section of a Greenhouse field whose id names none (start-year--1): that of
 * the nearest field with the same number that does (school--1). Greenhouse titles
 * its education block with a paragraph, which gives the year boxes no section.
 */
function suffixEntity(el: Element, num: string): Entity | null {
  for (let node = el.parentElement; node; node = node.parentElement) {
    for (const other of Array.from(node.querySelectorAll(`[id$="--${num}"]`))) {
      const m = GREENHOUSE_ROW.exec(other.id);
      const entity = m && other !== el ? baseEntity((m[1] as string).replace(/-/g, " ")) : null;
      if (entity) return entity;
    }
  }
  return null;
}

/** Which row of which section a control sits in, from the evidence the page gives. */
export function rowRef(info: ControlInfo, doc: Document): RowRef | null {
  const el = info.members[0] ?? info.el;
  const key = idName(el);
  let m = WORKDAY_ID.exec(info.el.getAttribute("id") ?? key);
  if (m) {
    const entity = entityOf(m[1] as string);
    // Workday numbers its rows from a counter that keeps rising as rows are added and deleted
    // (workExperience-6, workExperience-15): the number orders the rows, it is no position.
    return entity ? { entity, group: entity, num: parseInt(m[2] as string, 10), fixedBase: false } : null;
  }
  m = PCTY_ROW.exec(info.el.getAttribute("id") ?? "");
  if (m) {
    const entity = entityOf(m[1] as string);
    if (entity) return { entity, group: entity, num: parseInt(m[2] as string, 10), fixedBase: false };
  }
  m = UNDERSCORE_ROW.exec(key);
  if (m) {
    const entity = entityOf(m[1] as string);
    // The fields of one section name the section differently (employerCity, employerState): the section is the entity.
    if (entity) return { entity, group: entity, num: parseInt(m[2] as string, 10), fixedBase: false };
  }
  m = NESTED_ROW.exec(el.getAttribute("name") ?? "");
  if (m) {
    const entity = entityOf(m[1] as string);
    if (entity) return { entity, group: entity, num: parseInt(m[2] as string, 10), fixedBase: false };
  }
  m = INDEXED_ROW.exec(info.el.getAttribute("id") ?? "");
  if (m) {
    const entity = entityOf(m[1] as string);
    if (entity) return { entity, group: entity, num: parseInt(m[2] as string, 10), fixedBase: false };
  }
  // Avature numbers a row's controls group-field-row; a choice's own id carries its option suffix, so its group name is the number.
  const avature = /^(\d+)-(\d+)-(\d+)$/;
  m = avature.exec(info.el.getAttribute("id") ?? "") ?? (info.kind === "radio-group" ? avature.exec(el.getAttribute("name") ?? "") : null);
  if (m) {
    // A field whose own words name no entity ("Company", "Current?") takes the entity its row's labels name.
    const [, gid, , row] = m;
    const rowLabels = () =>
      Array.from(doc.querySelectorAll(`label[for^="${gid}-"]`))
        .filter((l) => new RegExp(`^${gid}-\\d+-${row}(?:_|$)`).test(l.getAttribute("for") ?? ""))
        .map((l) => l.textContent ?? "")
        .join(" ");
    const entity = entityOf(`${info.section.join(" ")} ${info.label.text}`) ?? entityOf(rowLabels());
    if (entity) return { entity, group: `n${m[1]}`, num: parseInt(m[3] as string, 10), fixedBase: false };
  }
  // Greenhouse numbers its entries with a `--N` suffix on every field id.
  m = GREENHOUSE_ROW.exec(info.el.getAttribute("id") ?? "");
  if (m) {
    const word = (m[1] as string).replace(/-/g, " ");
    const entity = baseEntity(`${word} ${info.section.join(" ")}`) ?? suffixEntity(el, m[2] as string);
    if (entity && entity !== "websites") return { entity, group: `suffix-${entity}`, num: parseInt(m[2] as string, 10), fixedBase: false };
  }
  // A row of it is one only beside its own company box, so a lone title-0 elsewhere is no row.
  m = GREENHOUSE_WORK_ROW.exec(info.el.getAttribute("id") ?? "");
  if (m && doc.getElementById(`company-name-${m[1]}`)) return { entity: "employment", group: "greenhouse-employment", num: parseInt(m[1] as string, 10), fixedBase: false };
  // Workable draws the entries its résumé parser found (or the "+ Add" button opened) as editors in
  // `li` items of a [data-ui=experience|education] block, with no number on any field.
  const block = el.closest('[data-ui="experience"], [data-ui="education"]');
  if (block) {
    const entry = el.closest("li");
    const siblings = entry?.parentElement ? Array.from(entry.parentElement.children) : [];
    const entity: Entity = block.getAttribute("data-ui") === "education" ? "education" : "employment";
    return { entity, group: `workable-${entity}`, num: Math.max(1, siblings.indexOf(entry as Element) + 1), fixedBase: false };
  }
  // A per-entity form scopes its row; a single page-wide form scopes nothing.
  const form = el.closest("form[id]");
  if (form && doc.querySelectorAll("form[id]").length >= 2) {
    const entity = entityOf(form.id) ?? entityOf(info.section.join(" "));
    const n = /(\d+)$/.exec(form.id);
    if (entity && n) return { entity, group: form.id.replace(/\d+$/, ""), num: parseInt(n[1] as string, 10), fixedBase: false };
  }
  // A control with a name of its own says its own row; only a nameless one adopts the row its surroundings state.
  const hidden = key.trim() === "" ? hiddenRow(el) : null;
  if (hidden) {
    const entity = entityOf(hidden.group);
    if (entity) return { entity, group: entity, num: hidden.num, fixedBase: false };
  }
  const head = headingNumber(el);
  if (head) {
    const entity = headingEntity(head.word);
    if (entity) return { entity, group: head.word.replace(/\s*\d+$/, ""), num: head.num, fixedBase: true };
  }
  return null;
}

// --- what a control in a row is --------------------------------------------------

function classify(entity: Entity, w: string, info: ControlInfo): Field {
  if (entity === "education" && /\bdid you graduate\b|\bgraduated\b/.test(w)) return "graduated";
  // Month and year boxes drawn as one date (Workday's) are the whole date.
  const split = info.driver === "date-segments" ? "" : /\bmonth\b/.test(w) ? "Month" : /\byear\b/.test(w) ? "Year" : "";
  if (entity === "websites") return /\b(url|website|link|address)\b/.test(w) ? "url" : "other";
  if (info.kind === "checkbox" && /\b(current|currently|present|still)\b/.test(w)) return "current";
  // Workday's year-only boxes are named only by their ids: firstYearAttended, lastYearAttended.
  const isEnd = /\b(end|to|until|graduat\w*|completion|finish\w*)\b|\blast (year|month|date|day)\b/.test(w);
  const isStart = /\b(start|from|begin\w*)\b|\bfirst (year|month|date|day)\b/.test(w);
  if (isEnd || isStart || /\bdate\b/.test(w)) {
    const edge = isEnd && !isStart ? "end" : "start";
    if (split) return `${edge}${split}` as Field;
    if (info.inputType === "date" || /\bdate\b|\b(start|end|from|to)\b/.test(w)) return edge;
  }
  if (/\bcountry\b/.test(w)) return "country";
  if (/\b(state|province|region|admin\w*)\b/.test(w) && !/\bstatement\b/.test(w)) return "state";
  if (/\b(zip|postal)\b/.test(w)) return "postal";
  if (/\b(address 2|address line 2|address2)\b/.test(w)) return "address2";
  if (/\b(address 1|address line 1|address1|street)\b/.test(w)) return "address1";
  if (/\b(locality|city|town)\b/.test(w)) return "city";
  if (entity === "employment") {
    // The employer's phone, email or supervisor is not the employer's name or the job's title.
    if (/\b(phone|telephone|fax|e ?mail|supervisor)\b/.test(w)) return "other";
    if (/\b(job title|position|title|role)\b/.test(w)) return "title";
    if (/\b(employer|company|organi[sz]ation)\b/.test(w)) return "company";
    if (/\blocation\b/.test(w)) return "location";
  } else {
    // "School location" is where the school is; "School name and location" is still its name.
    if (/\blocation\b/.test(w) && !/\bname\b/.test(w)) return "location";
    if (/\b(school|universit\w*|college|institution|institute)\b/.test(w)) return "school";
    if (GRADE.test(w)) return GRADE_SCALE.test(w) ? "gpaScale" : "gpa";
    if (/\b(major|field of study|discipline|area of study|subject|concentration)\b/.test(w)) return "major";
    if (/\b(degree|qualification|level)\b/.test(w)) return "degree";
  }
  return "other";
}

/** A grade measure: the row's grade average, or the scale it is on, which the entry does not state. */
const GRADE = /\b(gpa|cgpa|grade point average|grade average|overall result)\b/;
const GRADE_SCALE = /\b(scale|out of|maximum|max|highest)\b/;

/** A label that asks something or gives an instruction is a question, not a field name: "please state the reason" has no state in it. */
const NARRATIVE = /^(please|kindly|if|describe|explain|tell us|why|how|what|do you|are you|have you|provide|list)\b|\?/i;

/** A question about whether the row's job or study is still going on, as a pair of radios asks it. */
const STILL_GOING = /\b(currently|still|ongoing|in progress)\b|\bcurrent (job|position|role|employer|employment)\b|\bpresent (job|position|role|employer)\b|^current$/;

/** The role of a control within its row: by its label, and only then by the words of its id and name. */
export function fieldOf(entity: Entity, info: ControlInfo): Field {
  const el = info.members[0] ?? info.el;
  const asked = collapse(info.label.direct || info.label.text);
  // "Do you currently work here?" reads as a question, but inside a row it is the row's own flag.
  if (info.kind === "radio-group" && entity !== "websites" && STILL_GOING.test(normalize(asked))) return "current";
  if (NARRATIVE.test(asked)) return "other";
  const clean = (s: string) => normalize(s.replace(/([a-z])([A-Z])/g, "$1 $2"));
  const labelWords = clean(`${info.label.direct || info.label.text} ${el.getAttribute("aria-label") ?? ""}`);
  const byLabel = classify(entity, labelWords, info);
  // A label that names a grade measure decides: Citadel's "GPA Scale" is named degree_measure.
  if (byLabel !== "other" || (entity === "education" && GRADE.test(labelWords))) return byLabel;
  return classify(entity, clean(`${el.getAttribute("id") ?? ""} ${el.getAttribute("name") ?? ""} ${el.getAttribute("placeholder") ?? ""}`), info);
}

// --- Ashby education entries ----------------------------------------------------

const ASHBY_EDUCATION = '[data-field-path="_systemfield_education_history"]';
const SCHOOL_LABEL = /school|universit|college|institution/;

/**
 * Ashby's education entries carry no numbered ids. An entry is the widest
 * container, inside the section, that holds exactly one School control, and
 * its number is the place of that control among the section's School controls.
 */
function ashbyRefs(infos: ControlInfo[]): Map<ControlInfo, RowRef> {
  const out = new Map<ControlInfo, RowRef>();
  const inside = infos.filter((i) => i.el.closest(ASHBY_EDUCATION));
  const schools = inside.filter((i) => SCHOOL_LABEL.test(normalize(i.label.direct || i.label.text)));
  if (!schools.length) return out;
  for (const info of inside) {
    const section = info.el.closest(ASHBY_EDUCATION) as Element;
    let owner: ControlInfo | null = null;
    for (let node: Element | null = info.el; node; node = node === section ? null : node.parentElement) {
      const held = schools.filter((s) => node?.contains(s.el));
      if (held.length > 1) break;
      if (held.length === 1) owner = held[0] as ControlInfo;
    }
    if (owner) out.set(info, { entity: "education", group: "ashby-education", num: schools.indexOf(owner) + 1, fixedBase: false });
  }
  return out;
}

// --- flat blocks with numbered lead boxes ----------------------------------------

/** A history box that carries its row's number in its label ("Company 2", "School 1"). */
const NUMBERED_LEAD = /^(company|employer|organi[sz]ation|school|university|college)(?: name)? ?#? ?(\d{1,2})$/;

/**
 * A flat block whose rows are told apart only by a numbered lead box (GoodLeap
 * on Lever: "Company 1", Title, Years Worked, "Company 2", Title, ...): each
 * lead opens its row, and the unnumbered row fields after it, up to the next
 * lead and inside the block the leads share, belong to it. A question in the
 * block is no row field, and a single lead is no block.
 */
function numberedLeadRefs(infos: ControlInfo[]): Map<ControlInfo, RowRef> {
  const out = new Map<ControlInfo, RowRef>();
  const lead = (info: ControlInfo) => (info.kind === "text" ? NUMBERED_LEAD.exec(normalize(info.label.direct || info.label.text)) : null);
  const leads = infos.filter((i) => lead(i));
  if (leads.length < 2) return out;
  const els = leads.map((l) => lightOf(l.el));
  const block = sharedRoot(els[0] as Element, els);
  let open: RowRef | null = null;
  for (const info of infos) {
    const m = lead(info);
    if (m) {
      const entity: Entity = /^(company|employer|organi[sz]ation)$/.test(m[1] as string) ? "employment" : "education";
      open = { entity, group: `numbered-${entity}`, num: parseInt(m[2] as string, 10), fixedBase: true };
      out.set(info, open);
    } else if (open && block.contains(lightOf(info.el)) && !/\d$/.test(normalize(info.label.direct || info.label.text)) && fieldOf(open.entity, info) !== "other") {
      out.set(info, open);
    }
  }
  return out;
}

// --- collecting rows ------------------------------------------------------------

function sharedRoot(a: Element, others: Element[]): Element {
  let node: Element = a;
  while (node.parentElement && !others.every((o) => node.contains(o))) node = node.parentElement;
  return node;
}

/** Rows of every repeated section the controls make up, in page order. */
export function collectRows(infos: ControlInfo[], doc: Document): Row[] {
  const ashby = ashbyRefs(infos);
  const numbered = numberedLeadRefs(infos);
  const refs = infos
    .map((info) => ({ info, ref: ashby.get(info) ?? rowRef(info, doc) ?? numbered.get(info) ?? null }))
    .filter((x): x is { info: ControlInfo; ref: RowRef } => x.ref !== null);
  const sections = new Map<string, typeof refs>();
  for (const x of refs) {
    const key = `${x.ref.entity}|${x.ref.group}`;
    sections.set(key, [...(sections.get(key) ?? []), x]);
  }
  const rows: Row[] = [];
  for (const [, members] of sections) {
    const nums = Array.from(new Set(members.map((m) => m.ref.num))).sort((a, b) => a - b);
    for (const num of nums) {
      const mine = members.filter((m) => m.ref.num === num);
      const first = mine[0] as (typeof refs)[number];
      const controls = mine.map((m) => ({ info: m.info, field: fieldOf(first.ref.entity, m.info) }));
      const els = mine.map((m) => lightOf(m.info.el));
      let root = sharedRoot(els[0] as Element, els);
      // The row's own remove control sits in its wrapper, outside the lowest ancestor of its fields.
      const others = refs.filter((m) => m.ref.num !== num || m.ref.group !== first.ref.group).map((m) => lightOf(m.info.el));
      for (let i = 0; i < 2 && root.parentElement && !others.some((o) => root.parentElement?.contains(o)); i++) root = root.parentElement;
      rows.push({
        entity: first.ref.entity,
        group: first.ref.group,
        ordinal: first.ref.fixedBase ? num - 1 : nums.indexOf(num),
        fixedBase: first.ref.fixedBase,
        root,
        controls,
        identity: "",
        detail: "",
        since: "",
      });
    }
  }
  return rows;
}

/** Fills in what each row shows, read with the engine's own reader. */
export function readRows(rows: Row[], read: (info: ControlInfo) => string): void {
  for (const row of rows) {
    const idField: Field = row.entity === "employment" ? "company" : "school";
    const get = (...fields: Field[]) => row.controls.filter((c) => fields.includes(c.field)).map((c) => read(c.info)).find((v) => v !== "") ?? "";
    row.identity = get(idField);
    row.detail = row.entity === "education" ? [get("degree"), get("major")].filter(Boolean).join(" ") : get("title");
    row.since = /\b(?:19|20)\d{2}\b/.exec(get("start", "startYear"))?.[0] ?? "";
  }
}

// --- binding rows to entries ------------------------------------------------------

const MIN_NAME = 3;

/** Two names are the same place when one holds the other, at least three characters long. */
export function sameName(a: string, b: string): boolean {
  const x = normalize(a);
  const y = normalize(b);
  if (x.length < MIN_NAME || y.length < MIN_NAME) return false;
  return x === y || x.includes(y) || y.includes(x);
}

/** A text names an employer or school when it holds the name as whole words; a blank name names nothing. */
export function namesIn(text: string, name: string): boolean {
  const n = normalize(name);
  return n !== "" && ` ${normalize(text)} `.includes(` ${n} `);
}

/** A degree or major shown in a row agrees with its entry when either holds the other, or the entry states none. */
export function detailAgrees(shown: string, entry: Entry): boolean {
  const want = [text(entry, "degree"), text(entry, "field")].filter(Boolean).join(" ");
  if (!shown || !want) return true;
  const s = normalize(shown);
  return s.includes(normalize(want)) || normalize(want).includes(s) || normalize(want).split(" ").some((t) => t.length > 3 && s.includes(t));
}

export interface Binding {
  row: Row;
  /** The entry the row answers from, or null when the row has none (junk, duplicate or beyond the entries). */
  entry: Entry | null;
  /** Why a row has no entry. */
  why?: "unmatched" | "duplicate" | "beyond";
  blank: boolean;
  /** The degree or major the row shows agrees with its entry. */
  detailOk: boolean;
}

/** How much of an entry a row shows besides its name: its title (or its degree, else its major) counts before its start year. */
function closeness(entity: Entity, row: Row, entry: Entry): number {
  const want = normalize(entity === "employment" ? text(entry, "title") : text(entry, "degree") || text(entry, "field"));
  const titled = want !== "" && (entity === "employment" ? normalize(row.detail) === want : normalize(row.detail).includes(want));
  return (titled ? 2 : 0) + (row.since !== "" && row.since === ymd(text(entry, "startDate", "start"))?.y ? 1 : 0);
}

/**
 * Each row to an entry whose employer or school it already shows (a parser
 * orders them its own way), then the rest by position onto the entries still
 * unclaimed. Two roles at one employer are two entries: a row takes the free
 * one whose title and start year it also shows, else the first free one. A
 * row is a duplicate only when every entry of the name it shows is held by
 * another row; a row past the last entry has none.
 */
export function bindRows(rows: Row[], entries: Entry[], entity: Entity): Binding[] {
  const mine = rows.filter((r) => r.entity === entity).sort((a, b) => a.ordinal - b.ordinal);
  const claimed = new Set<number>();
  const out: Binding[] = mine.map((row) => ({ row, entry: null, detailOk: true, blank: row.identity === "" && row.controls.every((c) => c.info.kind === "checkbox" || !c.info.answered) }));
  const pairs = out.flatMap((b) => entries.flatMap((e, at) => (b.row.identity && sameName(entryName(entity, e), b.row.identity) ? [{ b, at, score: closeness(entity, b.row, e) }] : [])));
  // The closest pair first; the sort keeps page order, then entry order, among equals.
  for (const { b, at } of pairs.slice().sort((x, y) => y.score - x.score)) {
    if (b.entry || claimed.has(at)) continue;
    claimed.add(at);
    b.entry = entries[at] as Entry;
    // The school decides the entry; a degree that contradicts it is rewritten, or the row is junk where the host deletes such records.
    b.detailOk = entity === "employment" || detailAgrees(b.row.detail, b.entry);
  }
  for (const b of out) {
    if (!b.row.identity || b.entry) continue;
    b.why = pairs.some((p) => p.b === b) ? "duplicate" : "unmatched";
  }
  for (const b of out) {
    if (b.entry || b.why) continue;
    // A blank row takes the entry at its place when it is free, else the first free one.
    const want = b.row.ordinal;
    const at = want < entries.length && !claimed.has(want) ? want : entries.findIndex((_, i) => !claimed.has(i));
    if (at < 0 || (b.row.fixedBase && want >= entries.length)) {
      b.why = "beyond";
      continue;
    }
    claimed.add(at);
    b.entry = entries[at] as Entry;
  }
  return out;
}

// --- values -----------------------------------------------------------------------

const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

/** An entry's date: the day only when the entry states one, never a default. */
function ymd(raw: string): { y: string; m: string; d: string | null } | null {
  const m = /^(\d{4})-(\d{1,2})(?:-(\d{1,2}))?/.exec(raw.trim());
  return m ? { y: m[1] as string, m: (m[2] as string).padStart(2, "0"), d: m[3] ? m[3].padStart(2, "0") : null } : null;
}

/** Month and year boxes drawn as one date with no day box (Workday's From/To) take a month and year. */
const monthDate = (info: ControlInfo): boolean => info.driver === "date-segments" && !info.members.some((p) => /day/i.test(p.id));

/** A whole date needs a stated day: a month and year alone give a combined date box no value. */
const whole = (d: { y: string; m: string; d: string | null } | null): string | null => (d?.d ? `${d.y}-${d.m}-${d.d}` : null);

/** Address line 1 cut at its box's length on a word boundary, and the overflow that goes on in line 2. */
export function splitAddress(line1: string, line2: string, max: number | undefined): { first: string; second: string } {
  if (!max || line1.length <= max) return { first: line1, second: line2 };
  const cut = line1.lastIndexOf(" ", max);
  const at = cut > 0 ? cut : max;
  return { first: line1.slice(0, at).trim(), second: [line1.slice(at).trim(), line2].filter(Boolean).join(" ") };
}

/** The value an entry gives one field of a row, or null when the entry has none for it. */
export function rowValue(entity: Entity, field: Field, entry: Entry, info: ControlInfo, address1Max?: number): string | null {
  const start = ymd(text(entry, "startDate", "start"));
  const current = entry.current === true || entry.inProgress === true || /^(present|current|now)$/i.test(text(entry, "endDate", "end"));
  const end = current ? null : ymd(text(entry, "endDate", "end"));
  const month = (d: { m: string } | null) => (d ? (info.kind === "custom-listbox" ? (MONTHS[parseInt(d.m, 10) - 1] as string) : d.m) : null);
  switch (field) {
    case "company":
      return text(entry, "employer") || null;
    case "title":
      return text(entry, "title") || null;
    case "location":
      return text(entry, "location", "city") || null;
    case "city":
      return text(entry, "city", "location") || null;
    case "country":
      return text(entry, "country") || null;
    case "state":
      return text(entry, "state", "region") || null;
    case "postal":
      return text(entry, "postalCode", "zip") || null;
    case "address1":
      return splitAddress(text(entry, "address1", "line1"), text(entry, "address2", "line2"), address1Max).first || null;
    case "address2":
      return splitAddress(text(entry, "address1", "line1"), text(entry, "address2", "line2"), address1Max).second || null;
    case "school":
      return text(entry, "school", "institution") || null;
    case "degree":
      return text(entry, "degree") || null;
    case "major":
      return text(entry, "field") || null;
    case "gpa":
      // The number the entry records ("3.4/4.0" is 3.4); a grade with no number is not typed.
      return /\d+(?:\.\d+)?/.exec(text(entry, "gradeAverage", "gpa"))?.[0] ?? null;
    case "gpaScale": {
      // The scale the entry writes with its grade ("3.4/4.0" is 4.0). A bare grade of at most 4 is on the
      // 4.0 scale, as the bare number typed beside it reads; above 4 (a 4.3 or a 5.0 scale) names none.
      const grade = text(entry, "gradeAverage", "gpa");
      const stated = /\d(?:\s*\/\s*|\s+out\s+of\s+)(\d+(?:\.\d+)?)/i.exec(grade)?.[1];
      if (stated) return stated;
      const bare = /^\s*(\d(?:\.\d+)?)\s*$/.exec(grade)?.[1];
      return bare !== undefined && Number(bare) <= 4 ? "4.0" : null;
    }
    case "start":
      return monthDate(info) && start ? `${start.y}-${start.m}` : whole(start);
    case "end":
      return monthDate(info) && end ? `${end.y}-${end.m}` : whole(end);
    case "startMonth":
      return month(start);
    case "startYear":
      return start?.y ?? null;
    case "endMonth":
      return month(end);
    case "endYear":
      return end?.y ?? null;
    case "current":
      // A pair of radios needs its No spelled out; a box is left unticked.
      if (info.kind === "radio-group") return current ? "Yes" : "No";
      return current ? "Yes" : null;
    case "graduated":
      // An end date in the past is a degree finished.
      // An end month with no day is over once the next month has begun.
      if (entity !== "education" || !end) return null;
      return (end.d ? new Date(+end.y, +end.m - 1, +end.d) : new Date(+end.y, +end.m, 1)).getTime() <= Date.now() ? "Yes" : "No";
    default:
      return null;
  }
}

/** Whether an element is inside any of the given roots. */
export const within = (el: Element, roots: Element[]): boolean => roots.some((r) => r === el || r.contains(el)) || Array.from(composedAncestors(el)).some((a) => roots.includes(a));
