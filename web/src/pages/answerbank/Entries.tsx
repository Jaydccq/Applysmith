import { Plus, Search } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { get, put, type ApiError } from "../../api/client";
import { asApiError } from "../../hooks";
import { SaveForm } from "../profile/shared";
import { answerBankFile, answerBankSaved } from "../../api/decoders_bank";
import { Link, useSearchParams } from "../../router";
import { RowsEditor, type Row } from "../profile/Rows";
import { FilterChips } from "../../ui/FilterChips";
import { Chip, Empty, ErrorNote, ShowMore, humanize, useCapped } from "../../ui/parts";
import type { AnswerBank, AnswerBankAsked, AnswerBankFile, AnswerBankEntry, AnswerBankOverride, AnswerBankUnanswered } from "../../wire";
import { QuestionRow } from "../runs/ApplyRecord";
import { EntryEditor } from "./EntryEditor";
import { ADD_PROMPT, SETUP_STEP, askTo, categoryOf, holdsAskMe, needsOf, previewOf, type Needs, type SiteOverride } from "./model";

const ALL = "";

// The states the chips above the list filter by. Blocking is the part of
// needs-an-answer that stops setup; the others stand on their own.
const SHOWS = [
  { id: "all", label: "All" },
  { id: "needs", label: "To answer" },
  { id: "blocking", label: "Blocking" },
  { id: "ask", label: "Ask me" },
  { id: "review", label: "To review" },
] as const;
type Show = (typeof SHOWS)[number]["id"];

function inShow(show: Show, id: string, needs: Needs): boolean {
  switch (show) {
    case "needs":
      return needs.unanswered.has(id);
    case "blocking":
      return needs.blocking.has(id);
    case "ask":
      return needs.askMe.has(id);
    case "review":
      return needs.review.has(id);
    default:
      return true;
  }
}

// groups splits the list by category, in rail order, when grouped is set.
function groups(list: AnswerBankEntry[], grouped: boolean): [string, AnswerBankEntry[]][] {
  if (!grouped) return [["", list]];
  const m = new Map<string, AnswerBankEntry[]>();
  for (const e of list) m.set(categoryOf(e), [...(m.get(categoryOf(e)) ?? []), e]);
  return [...m].sort(([a], [b]) => a.localeCompare(b));
}

// EntryRow is one entry: the question opens the editor, the stored answer is
// the line under it, and the chips say what the autofill will do with it. The
// entry's kind is left to the editor, so the chips carry only states.
function EntryRow({ e, needs, sites, selected, onSelect }: { e: AnswerBankEntry; needs: Needs; sites: number; selected: boolean; onSelect: (id: string) => void }) {
  const open = needs.unanswered.has(e.id);
  const blocking = needs.blocking.has(e.id);
  const asked = holdsAskMe(e);
  // An entry the file leaves blank may be answered by a setup step.
  const derived = needs.derived.get(e.id);
  const preview = previewOf(e) || derived?.answer;
  return (
    <li className="entry-row" data-state={asked ? "ask" : blocking ? "blocking" : open ? "open" : undefined}>
      <div className="entry-main">
        <button type="button" className="entry-open" onClick={() => onSelect(e.id)} aria-pressed={selected}>
          {e.description || e.id}
        </button>
        {preview && <span className="entry-preview">{preview}</span>}
      </div>
      <span className="chips entry-chips">
        {asked ? <Chip tone="info">Ask me</Chip> : open && <Chip tone={blocking ? "bad" : "warn"}>{blocking ? "Blocking" : "To answer"}</Chip>}
        {derived && <Chip>From Setup: {SETUP_STEP[derived.step] ?? derived.step}</Chip>}
        {sites > 0 && <Chip>{sites === 1 ? "Different on 1 site" : `Different on ${sites} sites`}</Chip>}
        {needs.review.has(e.id) && <Chip tone="warn">To review</Chip>}
      </span>
    </li>
  );
}

// Rows a list draws before "Show more": a whole bank runs to hundreds, which
// makes the page tens of thousands of pixels tall. A category group is cut
// short, since the rail and the search reach any of it; a single list (a search
// or one category) is the thing being read, so it runs longer. The "To answer"
// and "Blocking" views are never cut.
const GROUP_CAP = 5;
const LIST_CAP = 30;

// EntryGroup is one category's heading and its rows, cut at its cap. The group
// holding the open entry shows all of its rows, so a link to an entry far down
// the bank still lands on it and the group stays open when the editor closes.
function EntryGroup({ name, list, cap, selected, children }: { name: string; list: AnswerBankEntry[]; cap: number; selected: string; children: (e: AnswerBankEntry) => ReactNode }) {
  const { shown, hidden, more } = useCapped(list, cap);
  const holdsOpen = list.some((e) => e.id === selected);
  useEffect(() => {
    if (holdsOpen) more();
  }, [holdsOpen]);
  return (
    <section className="entry-group" aria-label={name ? `${humanize(name)}: ${list.length} entries` : undefined}>
      {name && (
        <h3>
          {humanize(name)} <span className="muted">{list.length}</span>
        </h3>
      )}
      <ul className="plain entry-list">{shown.map(children)}</ul>
      <ShowMore hidden={hidden} onMore={more} what={name ? `in ${humanize(name)}` : "entries"} />
    </section>
  );
}

// Asked heads the "To answer" view with the questions waiting runs left that
// the bank cannot answer yet, each once with how many runs wait on it. An
// answer is saved as a run's questions form saves it, and every waiting run it
// completes goes on in its kept tab or can be retried.
function Asked({ asked, onSaved }: { asked: AnswerBankAsked[]; onSaved: (a: AnswerBankAsked, continued: boolean) => void }) {
  return (
    <section className="entry-group" aria-labelledby="ab-asked">
      <h3 id="ab-asked">
        Questions your runs wait on <span className="muted">{asked.length}</span>
      </h3>
      <ol className="plain run-answers">
        {asked.map((a) => (
          <QuestionRow key={a.entryId || a.question} runId={a.runId} host={a.host} q={a} note={a.runs === 1 ? "1 run waits on this answer." : `${a.runs} runs wait on this answer.`} onSaved={(continued) => onSaved(a, continued)} />
        ))}
      </ol>
    </section>
  );
}

type Overrides = AnswerBank["overrides"];

// mapOverrides rewrites each host's override list and drops a host left empty.
const mapOverrides = (o: Overrides | null, f: (list: AnswerBankOverride[]) => AnswerBankOverride[]): Overrides =>
  Object.fromEntries(Object.entries(o ?? {}).map(([host, list]) => [host, f(list)] as const).filter(([, list]) => list.length > 0));

// Removed is what a delete took out of the bank, so Undo can put it back.
interface Removed {
  entry: AnswerBankEntry;
  at: number;
  overrides: Overrides;
}

// Entries is the bank itself: the category rail, the state chips, the search
// and the editor. Every change (a save, a delete, an undo) sends the whole
// bank with the revision it was loaded at; an undo reads the bank first. The
// state chip and the category live in the address, so Back and a link keep
// them. New answers are added through the Ask Agent, which saves them where
// the matcher finds them.
export function Entries({ file, unanswered, selected, onSelect, reload }: { file: AnswerBankFile; unanswered: AnswerBankUnanswered; selected: string; onSelect: (id: string) => void; reload: () => void }) {
  const [params, setParams] = useSearchParams();
  const show: Show = SHOWS.find((s) => s.id === params.get("show"))?.id ?? "all";
  const category = params.get("category") ?? ALL;
  const setCategory = (c: string) => setParams({ category: c }, { replace: true });
  const [query, setQuery] = useState("");
  // run links a waiting run an answer did not continue, where it is retried.
  const [note, setNote] = useState<{ say: string; undo?: Removed; fail?: ApiError; run?: string } | null>(null);
  const entries = file.bank.entries;
  const needs = useMemo(() => needsOf(unanswered, entries), [unanswered, entries]);
  // A blank entry a waiting run's question reaches is that question in the
  // "To answer" view, so it is listed once.
  const asked = unanswered.asked;
  const covered = new Set(asked.map((a) => a.entryId ?? ""));

  const rail = useMemo(() => {
    const m = new Map<string, { count: number; open: number }>();
    for (const e of entries) {
      const c = m.get(categoryOf(e)) ?? { count: 0, open: 0 };
      c.count++;
      if (needs.unanswered.has(e.id)) c.open++;
      m.set(categoryOf(e), c);
    }
    return [...m].sort(([a], [b]) => a.localeCompare(b));
  }, [entries, needs]);

  const overrides = file.bank.overrides ?? {};
  // sitesOf is the entry's overrides with their hosts, in file order.
  const sitesOf = (id: string): SiteOverride[] => Object.entries(overrides).flatMap(([host, list]) => list.filter((o) => o.id === id).map((o) => ({ host, o })));
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  // inView is the search and the category in force.
  const inView = (e: AnswerBankEntry) => {
    if (words.length > 0) {
      const text = [e.id, e.description, e.kind, previewOf(e), needs.derived.get(e.id)?.answer, ...(e.examples ?? []), ...sitesOf(e.id).map((s) => s.host)].join(" ").toLowerCase();
      if (!words.every((w) => text.includes(w))) return false;
    }
    return category === ALL || categoryOf(e) === category;
  };
  const inScope = entries.filter(inView);
  const shown = inScope.filter((e) => inShow(show, e.id, needs) && !(show === "needs" && covered.has(e.id)));
  // The questions runs wait on belong to no category and match no search.
  const askedInScope = words.length === 0 && category === ALL ? asked : [];
  const waiting = show === "needs" ? askedInScope : [];
  // The "To answer" view lists the blank links apart, as links the user may
  // add: no run waits on an optional field.
  const links = show === "needs" ? inScope.filter((e) => needs.optional.has(e.id)) : [];
  // Each number of the count line says what it counts; in the "To answer"
  // view, the first two add up to its chip.
  const parts =
    show === "needs"
      ? [
          waiting.length > 0 && `${waiting.length === 1 ? "1 question" : `${waiting.length} questions`} your runs wait on`,
          `${shown.length} of ${entries.length} entries to answer`,
          links.length > 0 && `${links.length === 1 ? "1 optional link" : `${links.length} optional links`} you may add`,
        ].filter((p) => typeof p === "string")
      : [shown.length === entries.length ? `${entries.length} entries` : `${shown.length} of ${entries.length} entries`];
  const countLine = parts.length > 1 ? `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}` : parts[0];
  const open = entries.find((e) => e.id === selected);
  // A link names an entry by id (usage, a suggestion, an edit to keep, the
  // tester); one renamed or deleted since opens nothing, so the page says so.
  const gone = !open && selected !== "";
  // The bank reads by category, under a state chip too; a search is one list.
  const grouped = category === ALL && words.length === 0;
  // The to-do views are read whole, in one pass.
  const todo = show === "needs" || show === "blocking";
  // Each chip counts what it lists under the category and search in force; one
  // with nothing behind it in the whole bank is left out, unless it is in force.
  const count = (list: AnswerBankEntry[], s: Show) => list.filter((e) => inShow(s, e.id, needs) && !(s === "needs" && covered.has(e.id))).length;
  const counts = Object.fromEntries(SHOWS.map((s) => [s.id, count(inScope, s.id) + (s.id === "needs" ? askedInScope.length : 0)])) as Record<Show, number>;
  const chips = SHOWS.filter((s) => s.id === "all" || s.id === "needs" || count(entries, s.id) > 0 || s.id === show);
  // A chip keeps the category in force.
  const chipTo = (s: Show) => {
    const p = new URLSearchParams();
    if (category) p.set("category", category);
    if (s !== "all") p.set("show", s);
    return `/answer-bank${p.toString() ? `?${p}` : ""}`;
  };

  const write = async (entries: AnswerBankEntry[], overrides: Overrides, base = file) => {
    await put("/api/answer-bank", answerBankSaved, { expectedRevision: base.revision, bank: { ...base.bank, entries, overrides } });
    reload();
  };
  // name ends the note's sentence once, after a question mark too.
  const name = (e: AnswerBankEntry) => (e.description || e.id).replace(/[^.?!]$/, "$&.");
  const close = () => onSelect("");
  const select = (id: string) => (setNote(null), onSelect(id));

  // save writes an edited answer back into its entry.
  const save = async (updated: AnswerBankEntry) => {
    await write(entries.map((e) => (e.id === updated.id ? updated : e)), overrides);
    setNote({ say: `Saved: ${name(updated)}` });
    close();
  };

  const remove = async (e: AnswerBankEntry) => {
    await write(entries.filter((x) => x !== e), mapOverrides(overrides, (list) => list.filter((o) => o.id !== e.id)));
    setNote({ say: `Deleted: ${name(e)}`, undo: { entry: e, at: entries.indexOf(e), overrides: mapOverrides(overrides, (list) => list.filter((o) => o.id === e.id)) } });
    close();
  };

  // undo puts a deleted entry back where it was, with its overrides, into the
  // bank as it is now, read again so a change made since is kept; an entry
  // that took its id since is not replaced.
  const undo = async (r: Removed) => {
    setNote({ say: `Restoring: ${name(r.entry)}` });
    try {
      const now = await get("/api/answer-bank", answerBankFile);
      const list = now.bank.entries;
      if (list.some((e) => e.id === r.entry.id)) {
        setNote({ say: `Not restored: the bank has an entry with the id ${r.entry.id} again.` });
        reload();
        return;
      }
      const back = { ...now.bank.overrides };
      for (const [host, list] of Object.entries(r.overrides)) back[host] = [...(back[host] ?? []), ...list];
      await write([...list.slice(0, r.at), r.entry, ...list.slice(r.at)], back, now);
      setNote({ say: `Restored: ${name(r.entry)}` });
    } catch (err) {
      setNote({ say: "", undo: r, fail: asApiError(err) });
    }
  };

  return (
    <div className="with-rail">
      <nav aria-label="Categories" className="rail sticky-rail">
        <button type="button" aria-current={category === ALL} onClick={() => setCategory(ALL)}>
          <span>All</span>
          <span className="rail-count">{entries.length}</span>
        </button>
        {/* A category's name is the words it shows, in order (WCAG 2.5.3), and says what the bare count counts. */}
        {rail.map(([name, c]) => (
          <button key={name} type="button" aria-current={category === name} onClick={() => setCategory(name)}>
            <span className="rail-name">{humanize(name)}</span>{" "}
            <span className="rail-count">
              {c.open > 0 && <span className="chip warn" title={`${c.open} to answer`}>{c.open} to answer</span>} {c.count}
              <span className="sr-only">{c.count === 1 ? " entry" : " entries"}</span>
            </span>
          </button>
        ))}
      </nav>
      <div>
        <div className="entries-tools">
          <label className="field entries-search">
            <span className="sr-only">Search entries</span>
            <span className="search-box">
              <Search aria-hidden="true" />
              <input type="search" data-page-search value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search a question or an answer" />
            </span>
          </label>
          <Link className="button" to={askTo(ADD_PROMPT)} title="Tell the Ask Agent the question and your answer; it saves them where forms find them">
            <Plus aria-hidden="true" /> Add with Ask Agent
          </Link>
        </div>
        <FilterChips
          label="Entries by state"
          items={chips.map((s) => ({ label: s.label, count: counts[s.id], to: chipTo(s.id), current: s.id === show }))}
        />
        {file.warnings.length > 0 && (
          <ul className="note">
            {file.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        )}
        <p role="status" className={note?.say ? "note ok" : "sr-only"}>
          {note?.say}
          {note?.undo && !note.fail && (
            <>
              {" "}
              <button type="button" onClick={() => void undo(note.undo!)}>
                Undo
              </button>
            </>
          )}
          {note?.run && (
            <>
              {" "}
              <Link to={`/runs/${encodeURIComponent(note.run)}`}>Open the run to retry it</Link>
            </>
          )}
        </p>
        {note?.fail && <ErrorNote error={note.fail} onRetry={() => void undo(note.undo!)} />}
        {gone && (
          <p role="alert" className="note warn">
            No entry has the id <code>{selected}</code> now: it was deleted.{" "}
            <button type="button" onClick={() => onSelect("")}>
              Dismiss
            </button>
          </p>
        )}
        <p className="count-line">{countLine}</p>
        {waiting.length > 0 && (
          <Asked
            asked={waiting}
            onSaved={(a, continued) => {
              setNote({ say: `Saved: ${a.question.replace(/[^.?!]$/, "$&.")}${continued ? " The run goes on in its kept tab." : ""}`, run: continued ? undefined : a.runId });
              reload();
            }}
          />
        )}
        {shown.length === 0 && waiting.length > 0 ? null : shown.length === 0 ? (
          <Empty>
            {words.length > 0 ? "No entry matches the search." : show === "needs" || show === "blocking" ? "Nothing here waits for your answer." : "No entry is in this view."}
            {(words.length > 0 || category !== ALL) && (
              <>
                {" "}
                <button
                  type="button"
                  className="ghost"
                  onClick={() => {
                    setQuery("");
                    setCategory(ALL);
                  }}
                >
                  Clear search and category
                </button>
              </>
            )}
          </Empty>
        ) : (
          groups(shown, grouped).map(([name, list]) => (
            <EntryGroup key={name} name={name} list={list} cap={todo ? Infinity : grouped ? GROUP_CAP : LIST_CAP} selected={selected}>
              {(e) => <EntryRow key={e.id} e={e} needs={needs} sites={sitesOf(e.id).length} selected={e.id === selected} onSelect={select} />}
            </EntryGroup>
          ))
        )}
        {links.length > 0 && (
          <section className="entry-group" aria-labelledby="ab-links">
            <h3 id="ab-links">
              Optional links you may add <span className="muted">{links.length}</span>
            </h3>
            <p className="muted">Forms ask for these as optional fields, so no run waits on them. Add the ones you have.</p>
            <ul className="plain entry-list">
              {links.map((e) => (
                <EntryRow key={e.id} e={e} needs={needs} sites={sitesOf(e.id).length} selected={e.id === selected} onSelect={select} />
              ))}
            </ul>
          </section>
        )}
        {open && (
          <EntryEditor
            key={open.id}
            entry={open}
            shape={file.shapes[open.id]}
            isDefault={file.defaultIds.includes(open.id)}
            overrides={sitesOf(open.id)}
            employers={file.bank.employers.length}
            derived={needs.derived.get(open.id)}
            gap={needs.blocking.has(open.id) ? "blocking" : needs.unanswered.has(open.id) ? "to answer" : undefined}
            onSave={save}
            onDelete={() => remove(open)}
            onClose={close}
            onReload={reload}
          />
        )}
      </div>
    </div>
  );
}

const EMPLOYER_COLUMNS = [
  { key: "name", label: "Company", placeholder: "Northwind" },
  { key: "aliases", label: "Other names, comma-separated", placeholder: "A former name, a parent, a brand" },
];
const employerRows = (f: AnswerBankFile): Row[] => f.bank.employers.map((e) => ({ name: e.name, aliases: e.aliases.join(", ") }));

// Employers edits the bank's employer list, which the employer-history
// entries answer from together with the jobs in the profile's work history.
// A save sends the whole bank with the revision it was loaded at; the save bar
// asks before unsaved rows are left and saves on Mod+S.
export function Employers({ file, reload }: { file: AnswerBankFile; reload: () => void }) {
  const [rows, setRows] = useState(() => employerRows(file));
  // A change made elsewhere comes into rows the user has not edited.
  const [base, setBase] = useState(file);
  if (base !== file) {
    if (JSON.stringify(rows) === JSON.stringify(employerRows(base))) setRows(employerRows(file));
    setBase(file);
  }
  // A row left wholly blank is not an employer; one with only other names is.
  const employers = rows.filter((r) => r.name.trim() || r.aliases.trim()).map((r) => ({ name: r.name.trim(), aliases: r.aliases.split(",").map((a) => a.trim()).filter(Boolean) }));
  const dirty = JSON.stringify(employers) !== JSON.stringify(file.bank.employers.map((e) => ({ name: e.name, aliases: e.aliases })));
  const unnamed = employers.some((e) => e.name === "");
  const save = async () => {
    await put("/api/answer-bank", answerBankSaved, { expectedRevision: file.revision, bank: { ...file.bank, employers } });
    reload();
  };
  return (
    <SaveForm onSubmit={save} onDiscard={() => setRows(employerRows(file))} reload={reload} dirty={dirty} blocked={unnamed} label="Save employers">
      <p className="muted">
        Questions such as &ldquo;Have you ever worked for Northwind?&rdquo; are answered from these companies and the jobs in your <Link to="/profile?tab=identity">work history</Link>. List a company here when it is missing from your history or goes by other names.
      </p>
      <RowsEditor title={`Employers (${employers.length})`} noun="employer" columns={EMPLOYER_COLUMNS} rows={rows} onChange={setRows} />
      {unnamed && <p className="note error">Each employer needs a company name.</p>}
    </SaveForm>
  );
}
