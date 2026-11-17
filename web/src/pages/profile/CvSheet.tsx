import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { Fragment, memo, useId, useLayoutEffect, useMemo, useRef, useState, type TextareaHTMLAttributes } from "react";
import { inline, parseHeading } from "../../ui/Markdown";

// A CV laid out the way an applicant tracking system and a recruiter read a
// résumé: one column, the name over a plain contact line, standard section
// headings, and each entry as what and when on one line with the role and the
// place under it. It reads both cv.md shapes in use: "### Title, Employer,
// Place (2020-01 to present)" headings, and a heading followed by a bold role
// line, a place line and a date line. The same reading backs the form that
// edits the CV field by field and writes it back in the shape it came in.

export type Block = { kind: "p"; text: string } | { kind: "ul"; items: string[] };

export interface Contact {
  label: string;
  value: string;
}

export interface Entry {
  id: number;
  heading: string;
  dates: string;
  // datesInHeading keeps "### Title, Employer (dates)" in that shape on write.
  datesInHeading: boolean;
  role: string;
  place: string[];
  body: Block[];
}

export interface Section {
  id: number;
  title: string;
  body: Block[];
  entries: Entry[];
}

interface Sheet {
  name: string;
  contact: Contact[];
  about: string[];
  sections: Section[];
}

let lastId = 0;
const nextId = () => ++lastId;

const MONTH = "(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\\.?\\s+";
const POINT = `(?:(?:${MONTH})?\\d{4}(?:[-/.](?:0?[1-9]|1[0-2])(?!\\d))?|present|current|now)`;
const RANGE = new RegExp(`\\b${POINT}(?:\\s*(?:\\u2013|\\u2014|-|to)\\s*${POINT})?`, "gi");
const BULLET = /^\s*(?:[-*+]|\d{1,3}[.)])\s+(.*)$/;
const LABEL = /^(?:\*\*([^*]{1,30}):\*\*|\*\*([^*]{1,30})\*\*:|(e-?mail|phone|mobile|tel|github|linkedin|website|portfolio|location|address):)\s*/i;
const EDGES = /^[\s,|()\u00b7\u2022]+|[\s,|()\u00b7\u2022]+$/g;
const SEPARATOR = /\s+[|\u00b7\u2022]\s+/;
// CONTACTISH is a line under the name that reads as a contact detail (an
// address, a link or a number) rather than a headline or a summary.
const CONTACTISH = /@|https?:\/\/|www\.|\.[a-z]{2,}\/|\d{3}/i;

// splitDates takes the first date or date range (one holding a year) out of a
// line and answers what is left beside it.
function splitDates(line: string): { dates: string; rest: string } | undefined {
  for (const m of line.matchAll(RANGE)) {
    if (!/\d{4}/.test(m[0])) continue;
    const at = m.index ?? 0;
    const rest = `${line.slice(0, at)} ${line.slice(at + m[0].length)}`.replace(/\(\s*\)/g, "").replace(/\s{2,}/g, " ").replace(EDGES, "");
    return { dates: m[0].trim(), rest };
  }
  return undefined;
}

// emphasised answers the text of a line that is one bold or italic run, and
// splits a leading bold run from what follows it.
function emphasised(line: string): { lead: string; rest: string } | undefined {
  for (const mark of ["**", "__"]) {
    if (!line.startsWith(mark)) continue;
    const end = line.indexOf(mark, mark.length);
    if (end > mark.length) return { lead: line.slice(mark.length, end).trim(), rest: line.slice(end + mark.length).replace(EDGES, "") };
  }
  const one = line[0];
  if ((one === "*" || one === "_") && line.length > 2 && line.endsWith(one) && line.indexOf(one, 1) === line.length - 1) {
    return { lead: line.slice(1, -1).trim(), rest: "" };
  }
  return undefined;
}

function parseContact(part: string): Contact {
  const m = LABEL.exec(part);
  return m ? { label: (m[1] ?? m[2] ?? m[3]).trim(), value: part.slice(m[0].length).replace(EDGES, "") } : { label: "", value: part.replace(EDGES, "") };
}

export function parseSheet(source: string): Sheet {
  const sheet: Sheet = { name: "", contact: [], about: [], sections: [] };
  let phase: "lead" | "header" | "body" = "lead";
  let section: Section | undefined;
  let entry: Entry | undefined;
  // heading is true while an entry is still reading its role, place and date
  // lines; a bullet, a long sentence or a blank line after them ends it.
  let heading = false;
  let joinable = false;
  let fence = false;

  const blocks = (): Block[] => {
    if (entry) return entry.body;
    if (!section) {
      section = { id: nextId(), title: "", body: [], entries: [] };
      sheet.sections.push(section);
    }
    return section.body;
  };
  const addText = (line: string) => {
    const body = blocks();
    const last = body[body.length - 1];
    const bullet = BULLET.exec(line);
    if (bullet) {
      if (last?.kind === "ul") last.items.push(bullet[1].trim());
      else body.push({ kind: "ul", items: [bullet[1].trim()] });
    } else if (joinable && last?.kind === "ul") {
      last.items[last.items.length - 1] += ` ${line.trim()}`;
    } else if (joinable && last?.kind === "p") {
      last.text += ` ${line.trim()}`;
    } else {
      body.push({ kind: "p", text: line.trim() });
    }
    joinable = true;
  };

  for (const raw of source.split(/\r?\n/)) {
    if (/^\s*```/.test(raw)) fence = !fence;
    const line = raw.trimEnd();
    const h = fence ? undefined : parseHeading(line);
    if (h && h.level === 1 && phase === "lead") {
      sheet.name = h.text;
      phase = "header";
    } else if (h && h.level <= 2) {
      section = { id: nextId(), title: h.text, body: [], entries: [] };
      sheet.sections.push(section);
      entry = undefined;
      phase = "body";
    } else if (h && h.level === 3) {
      const split = splitDates(h.text);
      entry = { id: nextId(), heading: split ? split.rest : h.text, dates: split?.dates ?? "", datesInHeading: !!split, role: "", place: [], body: [] };
      if (!section) {
        section = { id: nextId(), title: "", body: [], entries: [] };
        sheet.sections.push(section);
      }
      section.entries.push(entry);
      heading = true;
      phase = "body";
    } else if (h) {
      addText(`**${h.text}**`);
      joinable = false;
    } else if (line.trim() === "" || line.trim().startsWith("<!--")) {
      joinable = false;
      if (entry && (entry.role || entry.place.length > 0 || entry.dates)) heading = false;
    } else if (phase === "header") {
      const text = line.trim();
      if (SEPARATOR.test(text) || LABEL.test(text) || CONTACTISH.test(text)) sheet.contact.push(...text.split(SEPARATOR).map(parseContact).filter((c) => c.value));
      else sheet.about.push(text);
    } else if (entry && heading && !BULLET.test(line) && line.trim().length <= 80) {
      let text = line.trim();
      const em = emphasised(text);
      if (em && !entry.role) {
        entry.role = em.lead;
        text = em.rest;
      }
      const split = text ? splitDates(text) : undefined;
      if (split && !entry.dates) {
        entry.dates = split.dates;
        text = split.rest;
      }
      if (text) entry.place.push(text);
    } else {
      heading = false;
      addText(line);
    }
  }
  return sheet;
}

const blockLines = (b: Block): string[] => (b.kind === "p" ? [b.text] : b.items.map((i) => `- ${i}`));

// sheetToMarkdown writes a sheet back as cv.md; parseSheet reads the result
// as the same sheet.
export function sheetToMarkdown(s: Sheet): string {
  const out: string[] = [];
  const gap = () => {
    if (out.length > 0 && out[out.length - 1] !== "") out.push("");
  };
  const blocks = (body: Block[]) => {
    for (const b of body) {
      gap();
      out.push(...blockLines(b));
    }
  };
  if (s.name.trim()) out.push(`# ${s.name.trim()}`);
  const contact = s.contact.filter((c) => c.value.trim());
  if (contact.length > 0) {
    gap();
    if (contact.some((c) => c.label.trim())) out.push(...contact.map((c) => (c.label.trim() ? `**${c.label.trim()}:** ${c.value.trim()}` : c.value.trim())));
    else out.push(contact.map((c) => c.value.trim()).join(" | "));
  }
  for (const a of s.about) {
    if (!a.trim()) continue;
    gap();
    out.push(a.trim());
  }
  for (const sec of s.sections) {
    gap();
    // A section without a title still needs its heading, or its text would
    // read as part of the section before it.
    if (sec.title.trim() || out.length > 0) out.push(`## ${sec.title.trim() || "Untitled"}`);
    blocks(sec.body);
    for (const e of sec.entries) {
      gap();
      const dates = e.dates.trim();
      out.push(`### ${e.heading.trim() || "Untitled"}${e.datesInHeading && dates ? ` (${dates})` : ""}`);
      if (e.role.trim()) out.push(`**${e.role.trim()}**`);
      out.push(...e.place.map((p) => p.trim()).filter(Boolean));
      if (dates && !e.datesInHeading) out.push(dates);
      blocks(e.body);
    }
  }
  return out.length > 0 ? `${out.join("\n")}\n` : "";
}

// formDrops reports whether cv.md holds something the form does not keep:
// comments, code, tables, nested lists or headings below the entry level.
export function formDrops(source: string): boolean {
  return /<!--|^\s*```|^#{4,6}\s|^ {2,}(?:[-*+]|\d{1,3}[.)])\s|^\s*\|.*\|\s*$/m.test(source);
}

function Blocks({ blocks, at }: { blocks: Block[]; at: string }) {
  return blocks.map((b, i) =>
    b.kind === "p" ? (
      <p key={i}>{inline(b.text, `${at}p${i}`)}</p>
    ) : (
      <ul key={i}>
        {b.items.map((item, j) => (
          <li key={j}>{inline(item, `${at}l${i}-${j}`)}</li>
        ))}
      </ul>
    ),
  );
}

// CvSheet previews cv.md as the résumé it describes.
export const CvSheet = memo(function CvSheet({ source }: { source: string }) {
  const sheet = useMemo(() => parseSheet(source), [source]);
  if (!sheet.name && sheet.contact.length === 0 && sheet.about.length === 0 && sheet.sections.length === 0) {
    return <p className="muted">Nothing to preview yet.</p>;
  }
  return (
    <article className="cv-sheet">
      {(sheet.name || sheet.contact.length > 0 || sheet.about.length > 0) && (
        <header>
          {sheet.name && <p className="cv-name">{inline(sheet.name, "n")}</p>}
          {sheet.contact.length > 0 && (
            <p className="cv-contact">
              {sheet.contact.map((c, i) => (
                <Fragment key={i}>
                  {i > 0 && " | "}
                  {inline(c.value, `c${i}`)}
                </Fragment>
              ))}
            </p>
          )}
          {sheet.about.map((a, i) => (
            <p key={i}>{inline(a, `a${i}`)}</p>
          ))}
        </header>
      )}
      {sheet.sections.map((s, i) => (
        <section key={s.id}>
          {s.title && <h4>{inline(s.title, `s${i}`)}</h4>}
          <Blocks blocks={s.body} at={`s${i}`} />
          {s.entries.map((e, j) => (
            <div className="cv-entry" key={e.id}>
              <p className="cv-line">
                <strong>{inline(e.heading, `e${i}-${j}`)}</strong>
                {e.dates && <span className="cv-when">{e.dates}</span>}
              </p>
              {(e.role || e.place.length > 0) && (
                <p className="cv-line">
                  <em>{inline(e.role, `r${i}-${j}`)}</em>
                  {e.place.length > 0 && <span className="cv-where">{e.place.join(", ")}</span>}
                </p>
              )}
              <Blocks blocks={e.body} at={`e${i}-${j}`} />
            </div>
          ))}
        </section>
      ))}
    </article>
  );
});

// GrowArea is a textarea as tall as its text, so a form shows everything it
// holds without a scroll box of its own.
export function GrowArea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const ref = useRef<HTMLTextAreaElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight + 2}px`;
  }, [props.value]);
  return <textarea ref={ref} rows={2} {...props} className={["grow", props.className].filter(Boolean).join(" ")} />;
}

const blocksOf = (text: string): Block[] => parseSheet(`## _\n\n${text}`).sections[0]?.body ?? [];
const textOf = (body: Block[]) => body.map((b) => blockLines(b).join("\n")).join("\n\n");
const bulletsOnly = (body: Block[]) => body.length === 0 || (body.length === 1 && body[0].kind === "ul");

// BodyField edits the bullets and text under a section or an entry: one
// bullet per line when that is all there is, else the Markdown of it. It keeps
// its own text, so a line being typed is never rewritten under the cursor.
function BodyField({ label, body, onChange }: { label: string; body: Block[]; onChange: (body: Block[]) => void }) {
  const id = useId();
  const [lines] = useState(() => bulletsOnly(body));
  const [text, setText] = useState(() => (lines ? (body[0]?.kind === "ul" ? body[0].items.join("\n") : "") : textOf(body)));
  return (
    <div data-wide>
      <label htmlFor={id}>{lines ? `${label}, one per line` : label}</label>
      <GrowArea
        id={id}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          if (!lines) return onChange(blocksOf(e.target.value));
          const items = e.target.value.split("\n").map((l) => l.replace(/^\s*[-*+]\s+/, "").trim()).filter(Boolean);
          onChange(items.length > 0 ? [{ kind: "ul", items }] : []);
        }}
      />
    </div>
  );
}

function Field({ label, value, onChange, placeholder }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
  const id = useId();
  return (
    <div>
      <label htmlFor={id}>{label}</label>
      <input id={id} type="text" value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}

// Order is the move up, move down and remove buttons of one item in a list.
function Order({ name, index, count, onMove, onRemove }: { name: string; index: number; count: number; onMove: (to: number) => void; onRemove: () => void }) {
  return (
    <span className="cv-order">
      <button type="button" className="ghost small icon-only" disabled={index === 0} onClick={() => onMove(index - 1)} aria-label={`Move ${name} up`} title="Move up">
        <ArrowUp aria-hidden="true" />
      </button>
      <button type="button" className="ghost small icon-only" disabled={index === count - 1} onClick={() => onMove(index + 1)} aria-label={`Move ${name} down`} title="Move down">
        <ArrowDown aria-hidden="true" />
      </button>
      <button type="button" className="ghost small icon-only" onClick={onRemove} aria-label={`Remove ${name}`} title="Remove">
        <Trash2 aria-hidden="true" />
      </button>
    </span>
  );
}

function move<T>(list: T[], from: number, to: number): T[] {
  const next = list.slice();
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next;
}

// CvForm edits the CV field by field and hands back the Markdown it writes.
// It reads the text once when it opens; the editor opens it again for text
// that arrives from elsewhere.
export function CvForm({ source, onChange }: { source: string; onChange: (markdown: string) => void }) {
  const [sheet, setSheet] = useState(() => parseSheet(source));
  const update = (next: Sheet) => {
    setSheet(next);
    onChange(sheetToMarkdown(next));
  };
  const setSection = (i: number, s: Section) => update({ ...sheet, sections: sheet.sections.map((x, j) => (j === i ? s : x)) });
  const setContact = (i: number, c: Contact) => update({ ...sheet, contact: sheet.contact.map((x, j) => (j === i ? c : x)) });

  return (
    <div className="cv-form">
      <section className="cv-card" aria-label="Name and contact">
        <div className="form-grid two">
          <Field label="Full name" value={sheet.name} onChange={(name) => update({ ...sheet, name })} />
          <h3>Contact details</h3>
          <ul className="plain cv-contacts" data-wide>
            {sheet.contact.map((c, i) => (
              <li key={i}>
                <input type="text" aria-label={`Label of contact ${i + 1}`} placeholder="Label" value={c.label} onChange={(e) => setContact(i, { ...c, label: e.target.value })} />
                <input type="text" aria-label={`Contact ${i + 1}`} placeholder="Email, phone or link" value={c.value} onChange={(e) => setContact(i, { ...c, value: e.target.value })} />
                <button type="button" className="ghost small icon-only" onClick={() => update({ ...sheet, contact: sheet.contact.filter((_, j) => j !== i) })} aria-label={`Remove contact ${i + 1}`} title="Remove">
                  <Trash2 aria-hidden="true" />
                </button>
              </li>
            ))}
          </ul>
          <div data-wide>
            <button type="button" className="ghost small" onClick={() => update({ ...sheet, contact: [...sheet.contact, { label: "", value: "" }] })}>
              <Plus aria-hidden="true" />
              Add contact detail
            </button>
          </div>
          <BodyField label="Summary under the name" body={[{ kind: "p", text: sheet.about.join(" ") }]} onChange={(b) => update({ ...sheet, about: b.flatMap(blockLines) })} />
        </div>
      </section>
      {sheet.sections.map((sec, i) => (
        <section key={sec.id} className="cv-card" aria-label={sec.title || `Section ${i + 1}`}>
          <div className="cv-card-head">
            <input type="text" className="cv-title" aria-label={`Title of section ${i + 1}`} placeholder="Section title, such as Experience" value={sec.title} onChange={(e) => setSection(i, { ...sec, title: e.target.value })} />
            <Order name={sec.title || `section ${i + 1}`} index={i} count={sheet.sections.length} onMove={(to) => update({ ...sheet, sections: move(sheet.sections, i, to) })} onRemove={() => update({ ...sheet, sections: sheet.sections.filter((_, j) => j !== i) })} />
          </div>
          <div className="form-grid two">
            <BodyField label={sec.entries.length > 0 ? "Text above the entries" : "Bullets"} body={sec.body} onChange={(body) => setSection(i, { ...sec, body })} />
          </div>
          {sec.entries.map((e, j) => {
            const name = e.heading || `entry ${j + 1}`;
            const setEntry = (next: Entry) => setSection(i, { ...sec, entries: sec.entries.map((x, k) => (k === j ? next : x)) });
            return (
              <div key={e.id} className="cv-entry-form" role="group" aria-label={name}>
                <div className="form-grid two">
                  <Field label="School, employer or project" value={e.heading} onChange={(heading) => setEntry({ ...e, heading })} />
                  <Field label="Degree or title" value={e.role} onChange={(role) => setEntry({ ...e, role })} />
                  <Field label="Dates" value={e.dates} placeholder={"Aug. 2024 \u2013 May 2026"} onChange={(dates) => setEntry({ ...e, dates })} />
                  <Field label="Place" value={e.place.join(", ")} placeholder="City, Country" onChange={(p) => setEntry({ ...e, place: p ? [p] : [] })} />
                  <BodyField label="Bullets" body={e.body} onChange={(body) => setEntry({ ...e, body })} />
                </div>
                <Order name={name} index={j} count={sec.entries.length} onMove={(to) => setSection(i, { ...sec, entries: move(sec.entries, j, to) })} onRemove={() => setSection(i, { ...sec, entries: sec.entries.filter((_, k) => k !== j) })} />
              </div>
            );
          })}
          <button type="button" className="ghost small" onClick={() => setSection(i, { ...sec, entries: [...sec.entries, { id: nextId(), heading: "", dates: "", datesInHeading: false, role: "", place: [], body: [] }] })}>
            <Plus aria-hidden="true" />
            Add entry to {sec.title || `section ${i + 1}`}
          </button>
        </section>
      ))}
      <button type="button" onClick={() => update({ ...sheet, sections: [...sheet.sections, { id: nextId(), title: "", body: [], entries: [] }] })}>
        <Plus aria-hidden="true" />
        Add section
      </button>
    </div>
  );
}
