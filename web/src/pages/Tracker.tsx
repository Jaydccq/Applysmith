import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { ApiError, get, patch, post, postModel, withQuery } from "../api/client";
import { gmailInboxPage } from "../api/decoders_gmail";
import { bulkArchiveResult, bulkStatusResult, candidacy as candidacyDecoder, candidacyDetail, candidacyPage, candidacyTransitions, followupList, llmRouting, messageDraft, statusChangeResult, trackerSummary } from "../api/decoders";
import { asApiError, usePageInRange, useResource } from "../hooks";
import { Link, leave, useSearchParams } from "../router";
import { ConfirmDialog, Modal } from "../ui/Modal";
import { useAction } from "./action";
import { DocumentsDrawer } from "./Documents";
import { InterviewDrawer } from "./Interview";
import { OutcomeReview } from "./tracker/Outcomes";
import { Duplicates } from "./tracker/Duplicates";
import { Empty, ErrorNote, ExternalLink, FileLink, Loading, ModelsLinked, OfflineScoresNote, Pager, ScoreBadge, ShowMore, StatusPill, TRACKER_STATUSES, Truncate, UndoNote, formatDate, formatScore, humanize, today, useCapped, useMoreFilters } from "../ui/parts";
import { ClearChip, FilterChips, isDay, sinceWords } from "../ui/FilterChips";
import { SortHeader } from "../ui/SortHeader";
import { CodeConflict, CodeRevisionConflict, ReasonDuplicateCandidacy } from "../wire";
import type { Candidacy, CandidacyEvent, FollowupSuggestion, LLMRouting, MessageDraft } from "../wire";
import { CopyButton } from "../ui/CopyButton";
import { PageHeader } from "../ui/PageHeader";
import { useSaveKey, useUnsaved } from "../ui/SaveBar";
import { Archive, ArchiveRestore, Plus } from "lucide-react";
import { EmptyState } from "../ui/EmptyState";
import { WeekFunnel } from "./discovery/Funnel";
import { BellRing, Compass, Download, FileText, ListChecks, Mail, MessagesSquare, Send } from "lucide-react";
import { Button } from "../ui/Button";
import { ActionMenu } from "../ui/ActionMenu";

const SORTS: Record<string, string> = {
  updated_desc: "Recently updated",
  score_desc: "Highest score",
  date_desc: "Newest date",
  company_asc: "Company, A to Z",
  role_asc: "Role, A to Z",
  status_asc: "Status, by stage",
};
const PAGE_SIZES = [25, 50, 100];
// SUBMITTED are the statuses of a row that was applied for, which carry an application date.
const SUBMITTED = new Set(["applied", "responded", "interview", "offer", "rejected"]);
const AFTER_APPLY = new Set(["applied", "responded", "interview", "offer"]);
// IN_FLIGHT is every submitted application still open; its chip filters by all four.
const IN_FLIGHT = ["applied", "responded", "interview", "offer"];
const MAIL = [["needs_me", "Needs me"], ["waiting", "Waiting on them"]] as const;
const APPLIED_WITHIN = [["7", "Last 7 days"], ["30", "Last 30 days"], ["90", "Last 90 days"]] as const;
// Who moved a row, as the movedBy filter names them.
const MOVERS: Record<string, string> = { gmail: "Gmail", user: "you", apply_chain: "Applysmith", agent: "the Ask Agent", evaluation: "an evaluation" };

function filtersOf(params: URLSearchParams) {
  const size = Number(params.get("pageSize"));
  // A hand-written link may spell statuses in capitals, list several with
  // commas or name one that does not exist; the server refuses an unknown one,
  // so the page keeps the known ones in table order and reads none as "all".
  const named = (params.get("status") ?? "").toLowerCase().split(",");
  const sort = params.get("sort") ?? "";
  const minScore = params.get("minScore") ?? "";
  const reached = TRACKER_STATUSES.find((st) => st === params.get("reached")) ?? "";
  const movedBy = Object.hasOwn(MOVERS, params.get("movedBy") ?? "") ? (params.get("movedBy") as string) : "";
  const since = params.get("since") ?? "";
  return {
    status: TRACKER_STATUSES.filter((st) => named.includes(st)).join(","),
    company: params.get("company") ?? "",
    source: params.get("source") ?? "",
    appliedWithin: APPLIED_WITHIN.some(([days]) => days === params.get("appliedWithin")) ? (params.get("appliedWithin") as string) : "",
    mail: MAIL.some(([v]) => v === params.get("mail")) ? (params.get("mail") as string) : "",
    // The rows that moved to a status, or that Gmail moved, on or after a
    // day: what a Home or funnel count counts. A day and one of the others.
    ...((reached || movedBy) && isDay(since) ? { reached, movedBy, since } : { reached: "", movedBy: "", since: "" }),
    q: params.get("q") ?? "",
    minScore: minScore !== "" && Number.isFinite(Number(minScore)) ? minScore : "",
    sort: Object.hasOwn(SORTS, sort) ? sort : "",
    archived: params.get("archived") === "true" ? "true" : "",
    pageSize: PAGE_SIZES.includes(size) ? size : 50,
    page: Math.max(1, Number(params.get("page")) || 1),
  };
}

const NOTE_LIMIT = 1000;
const NOTES_KEPT = 10;

// WriteFailure explains a failed write. A stale version says the row changed
// and offers a reload; any other failure shows its message and request id.
function WriteFailure({ failure, onReload }: { failure: ApiError; onReload: () => void }) {
  if (failure.kind !== CodeRevisionConflict) return <ErrorNote error={failure} />;
  return (
    <div role="alert" className="note error">
      <p>This row changed since you loaded it, so nothing was saved. Reload it to see the current version.</p>
      <p className="muted">Request {failure.requestId}</p>
      <button type="button" onClick={onReload}>
        Reload row
      </button>
    </div>
  );
}

// A note line is "{date} {author}: {text}"; a merged row's lines also start with the folded row's id after a hash.
const NOTE_PREFIX = /^(#\d+\s+)?\d{4}-\d{2}-\d{2}\s+[^:]{1,40}:\s*/;

// latestNote is the newest line of a row's notes without its date and author
// prefix, for the line under the role; the whole notes are in its title.
function latestNote(notes: string): string {
  const last = notes.trim().split("\n").pop() ?? "";
  return last.replace(NOTE_PREFIX, "");
}

// shownNote is the line under the role: the newest note, or, for a row the
// search found only through its notes, the newest note holding a search word.
function shownNote(e: Candidacy, query: string): string {
  const words = query.toLowerCase().split(/\s+/).filter((w) => w.length >= 2);
  const named = `${e.company} ${e.role}`.toLowerCase();
  if (words.length === 0 || words.some((w) => named.includes(w))) return latestNote(e.notes);
  const lines = e.notes.split("\n").map((l) => l.replace(NOTE_PREFIX, ""));
  return lines.reverse().find((l) => words.some((w) => l.toLowerCase().includes(w))) ?? latestNote(e.notes);
}

const OWES: Record<string, string> = { user: "your move", company: "their move" };

// mailAge is how long ago the thread's latest incoming message came.
const mailAge = (days: number) => (days < 1 ? "today" : `${days}d`);

// RowMail is the line under a row's status: its most pressing open email
// thread, as the Gmail inbox reads it (the same link and lifecycle lane), with
// the age of its latest incoming message. It opens that thread.
function RowMail({ mail }: { mail: NonNullable<Candidacy["mail"]> }) {
  return (
    <span className="sub">
      <Link to={`/gmail?thread=${encodeURIComponent(mail.threadId)}`} title={`Email: ${humanize(mail.lane)}, ${OWES[mail.owner] ?? "no one's move"}${mail.open > 1 ? `; ${mail.open} open threads` : ""}`}>
        <Mail size={12} aria-hidden="true" /> {mail.nextAction} {"\u00b7"} {mailAge(mail.ageDays)}
      </Link>
    </span>
  );
}

// APPLIERS names the person who sent an application as "you"; an agent
// submission keeps its "by agent" mark.
const APPLIERS: Record<string, string> = { user: "you" };

function StatusCell({ entry }: { entry: Candidacy }) {
  return (
    <>
      <StatusPill status={entry.status} />
      {AFTER_APPLY.has(entry.status) && entry.appliedBy && <span className="muted"> by {APPLIERS[entry.appliedBy] ?? entry.appliedBy}</span>}
    </>
  );
}

// SummaryStrip is the row of status counts above the list; each count is the
// filter for that status and keeps the other filters.
function SummaryStrip({ selected, params, archived }: { selected: string; params: URLSearchParams; archived: string }) {
  // The counts include archived rows when the list does.
  const [res, retry] = useResource(() => get("/api/tracker/summary", trackerSummary, { archived }), [archived], ["tracker"], { keep: `tracker.summary${archived}` });
  if (res.status === "error" && !res.data) return <ErrorNote error={res.error} onRetry={retry} what="the status counts" />;
  if (!res.data) return <Loading lines={1} label="Loading the counts" />;
  const s = res.data;
  const to = (status: string) => {
    const p = new URLSearchParams(params);
    for (const k of ["status", "page", "row"]) p.delete(k);
    if (status) p.set("status", status);
    return p.size ? `/tracker?${p}` : "/tracker";
  };
  const inFlight = IN_FLIGHT.join(",");
  return (
    <FilterChips
      label="Applications by status"
      items={[
        { label: "All", count: s.total, to: to(""), current: selected === "" },
        { label: "In flight", count: IN_FLIGHT.reduce((n, st) => n + (s.counts[st] ?? 0), 0), to: to(inFlight), current: selected === inFlight },
        ...TRACKER_STATUSES.map((st) => ({ label: humanize(st), count: s.counts[st] ?? 0, to: to(st), current: selected === st, status: st })),
      ]}
    />
  );
}

function Filters({ f }: { f: ReturnType<typeof filtersOf> }) {
  const [, setParams] = useSearchParams();
  // One field searches company, role and note text; a link that names a company (a
  // report's "Find in tracker") shows it here, and a search replaces it.
  const [text, setText] = useState(f.q || f.company);
  const [minScore, setMinScore] = useState(f.minScore);
  const [source, setSource] = useState(f.source);
  useEffect(() => setText(f.q || f.company), [f.q, f.company]);
  useEffect(() => setMinScore(f.minScore), [f.minScore]);
  useEffect(() => setSource(f.source), [f.source]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setParams({ q: text.trim(), company: undefined, minScore: minScore.trim(), source: source.trim(), page: undefined }, { replace: true });
  };
  const more = useMoreFilters([f.minScore, f.appliedWithin, f.mail, f.source, f.sort, f.archived, f.pageSize === 50 ? "" : f.pageSize].filter(Boolean).length);
  return (
    <form onSubmit={submit} className="filters sized" aria-label="Filters">
      <label>
        Company, role or note
        <input type="search" data-page-search value={text} onChange={(e) => setText(e.target.value)} />
      </label>
      {more.toggle}
      {!more.hidden && (
        <>
          <label>
            Minimum score
            <input type="number" inputMode="decimal" min={0} max={5} step={0.1} value={minScore} onChange={(e) => setMinScore(e.target.value)} />
          </label>
          <label>
            Applied
            <select value={f.appliedWithin} onChange={(e) => setParams({ appliedWithin: e.target.value || undefined, page: undefined }, { replace: true })}>
              <option value="">Any time</option>
              {APPLIED_WITHIN.map(([days, label]) => (
                <option key={days} value={days}>
                  {label}
                </option>
              ))}
            </select>
          </label>
          <label>
            Email
            <select value={f.mail} onChange={(e) => setParams({ mail: e.target.value || undefined, page: undefined }, { replace: true })}>
              <option value="">Any</option>
              {MAIL.map(([v, label]) => (
                <option key={v} value={v}>
                  {label}
                </option>
              ))}
            </select>
          </label>
          <label>
            Source
            <input value={source} onChange={(e) => setSource(e.target.value)} />
          </label>
          <label>
            Sort
            <select value={f.sort || "updated_desc"} onChange={(e) => setParams({ sort: e.target.value === "updated_desc" ? undefined : e.target.value, page: undefined }, { replace: true })}>
              {Object.entries(SORTS).map(([k, label]) => (
                <option key={k} value={k}>
                  {label}
                </option>
              ))}
            </select>
          </label>
          <label>
            Rows per page
            <select value={f.pageSize} onChange={(e) => setParams({ pageSize: e.target.value === "50" ? undefined : e.target.value, page: undefined }, { replace: true })}>
              {PAGE_SIZES.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </label>
        </>
      )}
      <div className="filters-end">
        {!more.hidden && (
          <label className="check">
            <input type="checkbox" checked={!!f.archived} onChange={(e) => setParams({ archived: e.target.checked ? "true" : undefined, page: undefined }, { replace: true })} />
            Include archived rows
          </label>
        )}
        <button type="submit">Apply filters</button>
      </div>
    </form>
  );
}

const FOLLOWUP_TEXT: Record<string, (n: number, due: string) => string> = {
  follow_up: (n, due) => `Follow-up ${n} has been due since ${due}.`,
  cadence_exhausted: () => "Every follow-up the cadence allows is sent, with no reply yet.",
  consider_closing: (_, due) => `No reply since applying; consider closing it (since ${due}).`,
  reply_check: (_, due) => `They replied; check that you answered (since ${due}).`,
};

const SNOOZES = [3, 7, 14];
const FOLLOWUP_KINDS = [["", "Chosen from the status"], ["recruiter_ping", "Recruiter ping"], ["status_check", "Status check"], ["thank_you", "Thank-you after an interview"]] as const;
const CONTACTS = [["recruiter", "Recruiter"], ["hiring_manager", "Hiring manager"], ["peer", "Someone on the team"], ["interviewer", "Interviewer"]] as const;

function Lines({ title, lines }: { title: string; lines: string[] }) {
  return lines.length > 0 ? <><p className="label">{title}</p><ul>{lines.map((t) => <li key={t}>{t}</li>)}</ul></> : null;
}

// noMessageModel says why no message can be drafted here: the offline
// simulation writes none, and an unrouted message-draft task has no model
// (internal/api draftMessage refuses both). Empty while drafting can work or
// the routing is not known.
function noMessageModel(r: LLMRouting | undefined): string {
  if (!r) return "";
  if (r.offline) return "The offline simulation writes no messages; choose a language model under Settings, Models to draft one.";
  return r.tasks.find((t) => t.task === "message-draft")?.unrouted ? "No model can draft messages; choose one under Settings, Models." : "";
}

// MessageDrafter drafts a follow-up or an outreach note on one row to copy:
// the suggested message and its alternatives, each within the message's
// character limit, and what each claim rests on. Nothing is sent. The form,
// the draft and the one it replaced are kept in this tab's session storage, so
// closing the dialog or reloading loses neither; a blocked store only loses
// them. A follow-up is drafted on opening unless one is kept or no model can
// draft it; then the dialog says so instead of showing a refusal. I sent this adds
// the message to the row's notes and, for a recruiter ping on an applied row,
// records the follow-up, each with the row's version.
function MessageDrafter({ id, label, mode, version: rowVersion, applied, onClose, onChanged }: { id: number; label: string; mode: "followup" | "outreach"; version: number; applied: boolean; onClose: () => void; onChanged: () => void }) {
  const key = `applysmith-draft:${mode}:${id}`;
  const [kept] = useState<{ form?: Record<string, string>; draft?: MessageDraft; previous?: MessageDraft }>(() => {
    try {
      return JSON.parse(window.sessionStorage.getItem(key) ?? "{}");
    } catch {
      return {};
    }
  });
  const [kind, setKind] = useState(kept.form?.kind ?? (mode === "outreach" ? "recruiter" : ""));
  const [name, setName] = useState(kept.form?.name ?? "");
  const [note, setNote] = useState(kept.form?.note ?? "");
  const [date, setDate] = useState(kept.form?.date ?? "");
  const [draft, setDraft] = useState(kept.draft);
  const [previous, setPrevious] = useState(kept.previous);
  const [version, setVersion] = useState(rowVersion);
  const [sent, setSent] = useState<{ n: number; done: string; undone: string; undo: () => Promise<unknown> }>();
  const top = useRef<HTMLParagraphElement>(null);
  const moved = useRef(false);
  const a = useAction();
  const s = useAction();
  const [routing] = useResource(() => get("/api/llm/routing", llmRouting), [], ["runs"]);
  const noModel = noMessageModel(routing.data);
  const opened = useRef(false);
  const modelNote = useId();
  useEffect(() => setVersion(rowVersion), [rowVersion]);
  useEffect(() => {
    try {
      window.sessionStorage.setItem(key, JSON.stringify({ form: { kind, name, note, date }, draft, previous }));
    } catch {
      // A blocked store only loses the kept draft.
    }
    if (moved.current) top.current?.focus();
    moved.current = false;
  }, [kind, name, note, date, draft, previous]);
  const ask = async () => {
    const body = mode === "outreach" ? { contact: kind, name, about: note, date } : { kind, contact: name, context: note };
    const d = await a.run(() => postModel(`/api/candidacies/${id}/${mode}-draft`, messageDraft, body));
    if (!d) return;
    moved.current = true;
    if (draft) setPrevious(draft);
    setDraft(d);
    setSent(undefined);
  };
  useEffect(() => {
    if (mode !== "followup" || kept.draft || opened.current || routing.status === "loading" || noModel) return;
    opened.current = true;
    void ask();
  }, [routing.status, noModel]);
  const what = mode === "outreach" ? "outreach note" : "follow-up";
  const counts = mode === "followup" && applied && draft?.kind === "recruiter_ping";
  // Undo takes back the follow-up, then the note, each against the version the
  // write before it made.
  const markSent = (message: string) => s.run(async () => {
    const notes = `/api/candidacies/${id}/notes`;
    const e = await post(notes, candidacyDecoder, { text: `Sent the ${what}${name.trim() ? ` to ${name.trim()}` : ""}: ${message}`, expectedVersion: version });
    setVersion(e.version);
    const n = (sent?.n ?? 0) + 1;
    const unnote = async (v: number) => {
      const back = await post(notes, candidacyDecoder, { undo: true, expectedVersion: v });
      setVersion(back.version);
      return back;
    };
    setSent({ n, done: "Added to the row's notes.", undone: "The note is off the row.", undo: () => unnote(e.version) });
    if (!counts) return;
    const path = `/api/candidacies/${id}/followup`;
    const f = await post(path, candidacyDecoder, { expectedVersion: e.version });
    setVersion(f.version);
    setSent({ n, done: "Added to the row's notes and recorded as a follow-up, so the cadence counts it.", undone: "The note and the follow-up are taken back.",
      undo: async () => unnote((await post(path, candidacyDecoder, { undo: true, expectedVersion: f.version })).version) });
  }).finally(onChanged);
  const variants = (d: MessageDraft, older: boolean) => [d.primary, ...d.alternatives].map((v, i) => {
    const title = (older ? "Previous " : "") + (i === 0 ? "suggested" : `alternative ${i}`);
    return (
      <section key={`${i}-${v.message}`} className="draft-variant" aria-label={title}>
        <p className="label" ref={i === 0 && !older ? top : undefined} tabIndex={-1}>{i === 0 ? (older ? "Previous" : "Suggested") : `Alternative ${i}`}{v.angle ? `: ${v.angle}` : ""}</p>
        <pre className="pre">{v.message}</pre>
        <p className="actions">
          <CopyButton text={v.message} label={`Copy the ${title}`} />
          <button type="button" className="small" disabled={s.busy} onClick={() => void markSent(v.message)} aria-label={`I sent the ${title}`}>I sent this</button>
          <span className="muted">{[...v.message].length} of {d.maxChars} characters</span>
        </p>
      </section>
    );
  });
  return (
    <Modal title={`${mode === "outreach" ? "Outreach note" : "Follow-up"} for ${label}`} onClose={onClose}>
      <form className="form-grid" aria-label={`Draft a ${what}`} onSubmit={(e) => { e.preventDefault(); void ask(); }}>
        <label>
          {mode === "outreach" ? "To" : "Kind"}
          <select value={kind} onChange={(e) => setKind(e.target.value)}>
            {(mode === "outreach" ? CONTACTS : FOLLOWUP_KINDS).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </select>
        </label>
        <label>
          Their name
          <input value={name} maxLength={120} onChange={(e) => setName(e.target.value)} />
        </label>
        {mode === "outreach" && kind === "interviewer" && (
          <label>
            Interview date
            <input value={date} maxLength={40} onChange={(e) => setDate(e.target.value)} />
          </label>
        )}
        <label>
          {mode === "outreach" ? "What you know about them" : "What you remember of the last exchange"}
          <textarea value={note} maxLength={1000} rows={2} onChange={(e) => setNote(e.target.value)} />
        </label>
        <p className="muted">Every field but the first is optional. What you write here is the only source about the person.</p>
        <div className="actions">
          <button type="submit" disabled={a.busy || !!noModel} aria-describedby={noModel ? modelNote : undefined}>{a.busy ? "Drafting..." : draft ? "Draft again" : `Draft the ${what}`}</button>
        </div>
        {noModel && <p id={modelNote} className="note" role="status"><ModelsLinked text={noModel} /></p>}
      </form>
      <div aria-live="polite">
        {a.error && <ErrorNote error={a.error} />}
        {s.error && <ErrorNote error={s.error} />}
        {sent && <UndoNote key={sent.n} done={sent.done} undone={sent.undone} onUndo={async () => (await s.run(sent.undo).finally(onChanged)) !== undefined} />}
      </div>
      {draft && (
        <>
          {variants(draft, false)}
          <Lines title="Rests on your own material" lines={draft.confirmed} />
          <Lines title="Check before sending" lines={draft.toConfirm} />
          <Lines title="Removed by the checks" lines={draft.dropped} />
          {draft.notes.map((n) => <p key={n} className="muted">{n}</p>)}
        </>
      )}
      {previous && (
        <details>
          <summary>The draft this one replaced</summary>
          {variants(previous, true)}
        </details>
      )}
      <div className="actions">
        <button type="button" onClick={onClose}>Close</button>
      </div>
    </Modal>
  );
}

// FOLLOWUPS_SHOWN keeps a long list of follow-ups from pushing the
// applications below the fold; the rest are one press away.
const FOLLOWUPS_SHOWN = 3;

// FollowupsDue lists what the follow-up cadence (policy.yml) has due today,
// and is left out when nothing is. Recording, snoozing or drafting sends nothing.
function FollowupsDue({ onOpen, onChanged }: { onOpen: (id: number) => void; onChanged: () => void }) {
  const [res, retry] = useResource(() => get("/api/tracker/followups", followupList), [], ["tracker"], { keep: "tracker.followups" });
  // The rows with a write in flight: each is held until its own write ends.
  const [busy, setBusy] = useState<ReadonlySet<number>>(new Set());
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [drafting, setDrafting] = useState<FollowupSuggestion>();
  // last is the latest press with its Undo; n keys the note by the press, so a
  // second press on the same row shows Undo again.
  const [last, setLast] = useState<{ n: number; done: string; undone: string; undo: () => Promise<unknown> }>();
  const { shown, hidden, more } = useCapped(res.data?.suggestions ?? [], FOLLOWUPS_SHOWN);
  if (res.status === "error" && !res.data) return <ErrorNote error={res.error} onRetry={retry} what="the follow-ups due" />;
  const refresh = () => (retry(), onChanged());
  // Each press quotes the row's version (the card's own, then the one the press
  // made, for Undo), so a row that moved since is refused and the list reloads.
  // Undo appends an event that takes the press back; the history keeps both.
  const act = async (s: FollowupSuggestion, write: () => Promise<number>, done: string, undone: string, undo: (version: number) => Promise<unknown>) => {
    setBusy((b) => new Set(b).add(s.id));
    setFailure(null);
    try {
      const version = await write();
      setLast({ n: (last?.n ?? 0) + 1, done, undone, undo: () => undo(version) });
    } catch (err) {
      setFailure(asApiError(err));
    } finally {
      refresh();
      setBusy((b) => new Set([...b].filter((id) => id !== s.id)));
    }
  };
  const followup = (s: FollowupSuggestion, snoozeDays?: number) =>
    act(s, () => post(`/api/candidacies/${s.id}/followup`, candidacyDecoder, { snoozeDays, expectedVersion: s.version }).then((e) => e.version),
      snoozeDays ? `The follow-up to ${s.company} is snoozed for ${snoozeDays} days.` : `Recorded your follow-up to ${s.company}.`,
      `The follow-up to ${s.company} is due again.`,
      (version) => post(`/api/candidacies/${s.id}/followup`, candidacyDecoder, { undo: true, expectedVersion: version }));
  // A reply check ends with any press of the user's on the row: I answered
  // notes it, and Undo takes the note back (NoteRequest.undo), so the check is due again.
  const answered = (s: FollowupSuggestion) => {
    const notes = `/api/candidacies/${s.id}/notes`;
    return act(s, () => post(notes, candidacyDecoder, { text: "Answered their reply.", expectedVersion: s.version }).then((e) => e.version),
      `Noted that you answered ${s.company}.`, `${s.company} is waiting for your answer again.`,
      (version) => post(notes, candidacyDecoder, { undo: true, expectedVersion: version }));
  };
  // Closing is one press: no reply is a closed row (discarded, not rejected:
  // nobody declined). Undo reopens it as applied, the status the card was drawn for.
  const close = (s: FollowupSuggestion) =>
    act(s, () => patch(`/api/candidacies/${s.id}`, statusChangeResult, { status: "discarded", expectedVersion: s.version }).then((r) => r.entry.version),
      `${s.company} is closed as no reply.`, `${s.company} is applied again.`,
      (version) => patch(`/api/candidacies/${s.id}`, statusChangeResult, { status: "applied", expectedVersion: version }));
  const undo = async (l: NonNullable<typeof last>) => {
    try {
      await l.undo();
      return true;
    } catch (err) {
      setFailure(asApiError(err));
      return false;
    } finally {
      refresh();
    }
  };
  // The drafter stays open when its row leaves the list (I sent this moves the cadence on).
  const note = (
    <>
      <div role="status">
        {last && <UndoNote key={last.n} done={last.done} undone={last.undone} onUndo={() => undo(last)} />}
      </div>
      {drafting && <MessageDrafter id={drafting.id} label={drafting.company} mode="followup" version={drafting.version} applied={drafting.kind === "follow_up"} onChanged={refresh} onClose={() => setDrafting(undefined)} />}
    </>
  );
  // The note keeps its place in the fragment, so it survives the list emptying and refilling.
  if (!res.data?.suggestions.length) return <>{null}{note}{failure && <ErrorNote error={failure} />}</>;
  return (
    <>
    <section aria-labelledby="tracker-followups" className="panel">
      <header className="panel-head">
        <h2 id="tracker-followups"><BellRing className="panel-icon" />Follow-ups due</h2>
      </header>
      <ul className="plain item-list">
        {shown.map((s) => (
          <li key={`${s.id}-${s.kind}`} className="inbox-item">
            <span className="item-main">
              <strong>{s.company}</strong>, {s.role}
              <span className="item-detail">{FOLLOWUP_TEXT[s.kind]?.(s.number ?? 1, formatDate(s.due ?? "")) ?? humanize(s.kind)}</span>
            </span>
            <span className="actions">
              {(s.kind === "follow_up" || s.kind === "reply_check") && (
                <button type="button" className="small" aria-label={`Draft a follow-up to ${s.company}`} onClick={() => setDrafting(s)}>
                  Draft a follow-up
                </button>
              )}
              {s.kind === "reply_check" && (
                <button type="button" className="small" disabled={busy.has(s.id)} aria-label={`I answered ${s.company}, ${s.role}`} onClick={() => void answered(s)}>I answered</button>
              )}
              {s.kind === "follow_up" && <>
                <button type="button" className="small" disabled={busy.has(s.id)} onClick={() => void followup(s)} aria-label={`I followed up with ${s.company}, ${s.role}`}>I followed up</button>
                <ActionMenu label={`More actions: ${s.company}, ${s.role}`} disabled={busy.has(s.id)} items={SNOOZES.map((days) => ({ label: `Snooze ${days} days`, onSelect: () => void followup(s, days) }))} />
              </>}
              {(s.kind === "consider_closing" || s.kind === "cadence_exhausted") && (
                <button type="button" className="small" disabled={busy.has(s.id)} aria-label={`Close as no reply: ${s.company}, ${s.role}`} onClick={() => void close(s)}>
                  Close as no reply
                </button>
              )}
              <button type="button" className="small" aria-label={`Open ${s.company}, ${s.role}`} onClick={() => onOpen(s.id)}>Open</button>
            </span>
          </li>
        ))}
      </ul>
      <ShowMore hidden={hidden} onMore={more} what="follow-ups" />
      {failure && <ErrorNote error={failure} />}
    </section>
    {note}
    </>
  );
}

function NoteForm({ entry, onSaved }: { entry: Candidacy; onSaved: () => void }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  // added is the latest note with the version it made, for its Undo; n keys the
  // Undo by the note, so the next one offers Undo again.
  const [added, setAdded] = useState<{ n: number; version: number }>();
  const box = useRef<HTMLTextAreaElement>(null);
  const ask = useUnsaved(text.trim() !== "" && !busy);
  const saveKey = useSaveKey(text.trim() !== "");
  const path = `/api/candidacies/${entry.id}/notes`;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!text.trim()) return;
    setBusy(true);
    setFailure(null);
    setAdded(undefined);
    try {
      const row = await post(path, candidacyDecoder, { text: text.trim(), expectedVersion: entry.version });
      setText("");
      setAdded({ n: (added?.n ?? 0) + 1, version: row.version });
      box.current?.focus();
      onSaved();
    } catch (err) {
      setFailure(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  // Undo quotes the version the note made, so a row written since is refused
  // rather than losing a later note (NoteRequest.undo).
  const undo = async (version: number) => {
    setFailure(null);
    try {
      await post(path, candidacyDecoder, { undo: true, expectedVersion: version });
      return true;
    } catch (err) {
      setFailure(asApiError(err));
      return false;
    } finally {
      onSaved();
    }
  };
  return (
    <>
      <form onSubmit={submit} className="inline-form" aria-label="Add a note">
        <label>
          Add a note
          <textarea ref={box} value={text} maxLength={NOTE_LIMIT} rows={3} onChange={(e) => setText(e.target.value)} />
        </label>
        <p className="help">
          {text.length} of {NOTE_LIMIT} characters. The row keeps its latest {NOTES_KEPT} notes.
        </p>
        <button type="submit" disabled={!text.trim() || busy} ref={saveKey}>
          Add note
        </button>
      </form>
      <div aria-live="polite">{added && <UndoNote key={added.n} keepFocus done="Note added." undone="The note is off the row." onUndo={() => undo(added.version)} />}</div>
      {failure && <WriteFailure failure={failure} onReload={() => { setFailure(null); onSaved(); }} />}
      {ask}
    </>
  );
}

function AddApplication({ onClose, onAdded }: { onClose: () => void; onAdded: (id: number) => void }) {
  const first = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [status, setStatus] = useState("applied");
  const [typed, setTyped] = useState(false);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const field = (name: string) => String(f.get(name) ?? "").trim();
    const body: Record<string, string> = { company: field("company"), role: field("role"), status: field("status") };
    for (const name of ["url", "location", "source", "appliedAt"]) if (field(name)) body[name] = field(name);
    setBusy(true);
    setFailure(null);
    try {
      const added = await post("/api/candidacies", candidacyDecoder, body);
      onAdded(added.id);
    } catch (err) {
      setFailure(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  const matchId = failure?.kind === CodeConflict && failure.details?.reason === ReasonDuplicateCandidacy ? failure.details.matchId : undefined;
  // A duplicate's fields describe a row that is already kept.
  const ask = useUnsaved(typed && !busy && matchId === undefined);
  const saveKey = useSaveKey(typed);
  return (
    <Modal title="Add application" onClose={() => leave(onClose)} initialFocus={first}>
      <p className="muted">Record a job you applied for outside Applysmith.</p>
      <form onSubmit={submit} onInput={() => setTyped(true)} className="inline-form" aria-label="Add application">
        <label>
          Company
          <input ref={first} name="company" required />
        </label>
        <label>
          Role
          <input name="role" required />
        </label>
        <label>
          Posting link
          <input name="url" type="url" inputMode="url" />
        </label>
        <label>
          Location
          <input name="location" />
        </label>
        <label>
          Status
          <select name="status" value={status} onChange={(e) => setStatus(e.target.value)}>
            {TRACKER_STATUSES.map((st) => (
              <option key={st} value={st}>
                {humanize(st)}
              </option>
            ))}
          </select>
        </label>
        {SUBMITTED.has(status) && (
          <label>
            Applied on
            <input name="appliedAt" type="date" defaultValue={today()} max={today()} />
          </label>
        )}
        <label>
          Source
          <input name="source" placeholder="manual" maxLength={80} />
        </label>
        {failure &&
          (matchId !== undefined ? (
            <div role="alert" className="note error">
              <p className="sentence">{failure.message}</p>
              <p>
                <Link to={`/tracker?row=${matchId}`} onClick={onClose}>
                  Open the matching row
                </Link>
              </p>
              <p className="muted">Request {failure.requestId}</p>
            </div>
          ) : (
            <ErrorNote error={failure} />
          ))}
        <div className="actions">
          <button type="button" onClick={() => leave(onClose)}>
            Cancel
          </button>
          <button type="submit" disabled={busy} ref={saveKey}>
            Add application
          </button>
        </div>
      </form>
      {ask}
    </Modal>
  );
}

// BULK_MAX is the most rows one bulk change takes (the server's maxBulkRows).
const BULK_MAX = 100;

// BulkStatus acts on the ticked rows, which may span pages: one status in one
// request (more than 5 ask first), or Archive / Unarchive. Undo sends each
// changed row back to its own old status, quoting the version the move made (a
// row that changed since is left and counted), or archives back what it
// unarchived. Select all N matching ticks every row the filters match, up to
// BULK_MAX. The buttons go with the ticks, so the Undo note takes focus.
function BulkStatus({ rows, total, picked, setPicked, selectAll, onDone, end }: { rows: Candidacy[]; total: number; picked: Map<number, Candidacy>; setPicked: (p: Map<number, Candidacy>) => void; selectAll: () => Promise<Candidacy[]>; onDone: () => void; end: ReactNode }) {
  const [target, setTarget] = useState("");
  const [asking, setAsking] = useState(false);
  const [done, setDone] = useState<{ n: number; to: string; back: [number, string, number][]; same: number } | null>(null);
  const [archived, setArchived] = useState<{ n: number; on: boolean; changed: number[]; open: number } | null>(null);
  const [left, setLeft] = useState(0);
  const act = useAction();
  const chosen = [...picked.values()];
  const send = (ids: number[], status: string) => post("/api/candidacies/bulk-status", bulkStatusResult, { ids, status });
  const archive = (ids: number[], on: boolean) => post("/api/candidacies/bulk-archive", bulkArchiveResult, { ids, archived: on });
  const n = (done?.n ?? 0) + (archived?.n ?? 0) + 1;
  const apply = async () => {
    setAsking(false);
    setDone(null);
    setArchived(null);
    setLeft(0);
    const got = await act.run(() => send(chosen.map((r) => r.id), target));
    if (!got) return;
    const was = new Map(chosen.map((r) => [r.id, r.status]));
    const back = got.results.filter((r) => r.changed).map((r): [number, string, number] => [r.entry.id, was.get(r.entry.id) ?? "", r.entry.version]);
    setDone({ n, to: target, back, same: got.results.length - back.length });
    setPicked(new Map());
    onDone();
  };
  const move = async (on: boolean) => {
    setDone(null);
    const got = await act.run(() => archive(chosen.map((r) => r.id), on));
    if (!got) return;
    setArchived({ n, on, changed: got.changed, open: got.open.length });
    setPicked(new Map());
    onDone();
  };
  const undo = async (back: [number, string, number][]) => {
    const skipped = await act.run(async () => {
      let stale = 0;
      for (const [id, status, expectedVersion] of back) {
        try {
          await patch(`/api/candidacies/${id}`, statusChangeResult, { status, expectedVersion });
        } catch (e) {
          if (asApiError(e).kind !== CodeRevisionConflict) throw e;
          stale++;
        }
      }
      return stale;
    });
    onDone();
    if (skipped === undefined) return false;
    setLeft(skipped);
    return true;
  };
  const rowsWord = (n: number) => `${n} ${n === 1 ? "row" : "rows"}`;
  // A row still in flight is left by Archive (the server refuses it) and counted.
  const archivedNote = ({ n, on, changed, open }: NonNullable<typeof archived>) => {
    const stays = open > 0 ? ` ${rowsWord(open)} still in flight ${open === 1 ? "stays" : "stay"} in the list until closed.` : "";
    if (changed.length === 0) return <p className="note ok">{on ? `No row was archived.${stays}` : "No selected row was archived."}</p>;
    const undo = async () => {
      const ok = (await act.run(() => archive(changed, !on))) !== undefined;
      onDone();
      return ok;
    };
    return <UndoNote key={n} done={`${rowsWord(changed.length)} ${on ? "archived, out of the default list." : "back in the list."}${stays}`} undone={`${rowsWord(changed.length)} ${on ? "back in the list" : "archived again"}.`} onUndo={undo} />;
  };
  const pageAll = rows.length > 0 && rows.every((r) => picked.has(r.id));
  const tick = (on: boolean) => {
    const next = new Map(picked);
    for (const r of rows) void (on ? next.set(r.id, r) : next.delete(r.id));
    setPicked(next);
  };
  return (
    <>
      <div className="toolbar">
        <label className="check">
          <input type="checkbox" checked={pageAll} ref={(el) => void (el && (el.indeterminate = !pageAll && rows.some((r) => picked.has(r.id))))} onChange={(e) => tick(e.target.checked)} />
          Select all {rows.length}
        </label>
        {pageAll && total > rows.length &&
          (picked.size >= total ? (
            <span>
              All {total} matching rows are selected.{" "}
              <button type="button" className="small" onClick={() => setPicked(new Map())}>Clear selection</button>
            </span>
          ) : total <= BULK_MAX ? (
            <button type="button" className="small" disabled={act.busy} onClick={() => void act.run(selectAll).then((list) => list && setPicked(new Map(list.map((r) => [r.id, r]))))}>
              Select all {total} matching
            </button>
          ) : (
            <span className="muted">One change takes up to {BULK_MAX} rows; narrow the filters to select all {total}.</span>
          ))}
        {chosen.length > 0 && (
          <>
            <select aria-label={`New status for the ${chosen.length} selected`} value={target} onChange={(e) => setTarget(e.target.value)}>
              <option value="">Choose a status</option>
              {TRACKER_STATUSES.map((st) => <option key={st} value={st}>{humanize(st)}</option>)}
            </select>
            <Button size="sm" disabled={!target || act.busy} onClick={() => (chosen.length > 5 ? setAsking(true) : void apply())}>
              Set status of {chosen.length} selected
            </Button>
            {chosen.some((r) => !r.archivedAt) && <Button size="sm" icon={Archive} disabled={act.busy} onClick={() => void move(true)}>Archive {chosen.length} selected</Button>}
            {chosen.some((r) => r.archivedAt) && <Button size="sm" icon={ArchiveRestore} disabled={act.busy} onClick={() => void move(false)}>Unarchive {chosen.length} selected</Button>}
          </>
        )}
        {end}
      </div>
      {act.error && <ErrorNote error={act.error} />}
      <div role="status">
        {done &&
          (done.back.length === 0 ? (
            <p className="note ok">All {rowsWord(done.same)} already had {humanize(done.to)}.</p>
          ) : (
            <UndoNote
              key={done.n}
              done={`${rowsWord(done.back.length)} moved to ${humanize(done.to)}${done.same ? `; ${done.same} already had it` : ""}.`}
              undone={[
                done.back.length > left && `${rowsWord(done.back.length - left)} back to the status ${done.back.length - left === 1 ? "it" : "each"} had.`,
                left > 0 && `${rowsWord(left)} changed since and ${left === 1 ? "was" : "were"} left as ${left === 1 ? "it is" : "they are"}.`,
              ].filter(Boolean).join(" ")}
              onUndo={() => undo(done.back)}
            />
          ))}
        {archived && archivedNote(archived)}
      </div>
      {asking && (
        <ConfirmDialog title={`Change the status of ${chosen.length} applications`} consequence={`All ${chosen.length} selected rows move to ${humanize(target)}.`} verb="Change status" onConfirm={() => void apply()} onCancel={() => setAsking(false)} />
      )}
    </>
  );
}

// RowDrawer is redrawn each time the Documents or Interview drawer closes over
// it, so it has no opener to give focus back to: backFrom names the drawer that
// just closed, and its button takes focus once the row has loaded (the buttons
// are not there when the dialog opens). returnFocus is where a drawer opened
// from the address sends focus when it closes.
// LinkedMail lists the Gmail threads linked to the row, each opening in Gmail.
// It reads only through the link (automatic or confirmed), so a thread that
// merely names the same company is not here.
function LinkedMail({ id }: { id: number }) {
  const [res, reload] = useResource(() => get("/api/gmail/inbox", gmailInboxPage, { candidacy: id, limit: 20 }), [id], ["gmail"]);
  return (
    <>
      <h3>Email</h3>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="the linked emails" />}
      {res.data && res.data.threads.length === 0 && <Empty>No email is linked to this application.</Empty>}
      {res.data && res.data.threads.length > 0 && (
        <ul className="plain rows-list mail-list">
          {res.data.threads.map((t) => (
            <li key={t.threadId}>
              <Link to={`/gmail?thread=${encodeURIComponent(t.threadId)}`}>{t.subject}</Link>
              <span className="sub muted">{[humanize(t.lane), t.nextAction, OWES[t.owner], `latest ${mailAge(t.stalenessDays)}`].filter(Boolean).join(" \u00b7 ")}</span>
            </li>
          ))}
        </ul>
      )}
      {res.data && res.data.total > res.data.threads.length && (
        <p>
          <Link to="/gmail">{`${res.data.total - res.data.threads.length} more in Gmail`}</Link>
        </p>
      )}
    </>
  );
}

// Only a closed or skipped row can be archived (the server refuses the rest);
// an archived row leaves the default list and is shown by "Include archived
// rows". Undo is the opposite press.
const ARCHIVABLE = new Set(["rejected", "discarded", "skipped"]);

function ArchiveRow({ entry, onChanged }: { entry: Candidacy; onChanged: () => void }) {
  const act = useAction();
  const [moved, setMoved] = useState<{ n: number; archived: boolean }>();
  const send = (verb: "archive" | "unarchive") => act.run(() => post(`/api/candidacies/${entry.id}/${verb}`, candidacyDecoder));
  const move = async (verb: "archive" | "unarchive") => {
    const row = await send(verb);
    if (!row) return false;
    setMoved({ n: (moved?.n ?? 0) + 1, archived: verb === "archive" });
    onChanged();
    return true;
  };
  const archived = entry.archivedAt !== undefined && entry.archivedAt !== "";
  if (!archived && !ARCHIVABLE.has(entry.status)) return null;
  return (
    <>
      <div className="actions">
        <Button icon={archived ? ArchiveRestore : Archive} disabled={act.busy} onClick={() => void move(archived ? "unarchive" : "archive")}>
          {archived ? "Unarchive" : "Archive"}
        </Button>
      </div>
      <div role="status">
        {moved && (
          <UndoNote
            key={moved.n}
            done={moved.archived ? "Archived: the row leaves the default list." : "Unarchived: the row is back in the list."}
            undone={moved.archived ? "The row is back in the list." : "The row is archived again."}
            onUndo={async () => {
              if (!(await send(moved.archived ? "unarchive" : "archive"))) return false;
              onChanged();
              return true;
            }}
          />
        )}
      </div>
      {act.error && <ErrorNote error={act.error} />}
    </>
  );
}

function RowDrawer({ id, backFrom, returnFocus, onClose, onChanged, onDocuments, onInterview }: { id: number; backFrom?: "documents" | "interview"; returnFocus: () => HTMLElement | undefined; onClose: () => void; onChanged: () => void; onDocuments: (entry: Candidacy) => void; onInterview: (entry: Candidacy) => void }) {
  const [res, reload] = useResource(
    async () => {
      const [detail, transitions] = await Promise.all([get(`/api/candidacies/${id}`, candidacyDetail), get(`/api/candidacies/${id}/transitions`, candidacyTransitions)]);
      return { detail, transitions };
    },
    [id],
    ["tracker"],
  );
  const [target, setTarget] = useState("");
  // on is the day the application went out, and note the line kept with the
  // change; both are optional and empty means the server's default (today, no note).
  const [on, setOn] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  // changed is the latest change with the version it made, for its Undo, which
  // puts the old status back as the bulk change's Undo does.
  const [changed, setChanged] = useState<{ n: number; from: string; to: string; version?: number }>();
  const [message, setMessage] = useState<"followup" | "outreach">();
  const picker = useRef<HTMLSelectElement>(null);
  const ask = useUnsaved(note.trim() !== "" && !busy);
  const saveKey = useSaveKey(target !== "");

  const entry = res.data?.detail.entry;
  // A change of the row resets the choice; the typed note goes only with this
  // form's own change or another row, so a note added below keeps it.
  useEffect(() => (setTarget(""), setOn("")), [entry?.status, entry?.version]);
  useEffect(() => setNote(""), [entry?.id]);
  const documentsButton = useRef<HTMLButtonElement>(null);
  const interviewButton = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (entry) (backFrom === "documents" ? documentsButton : backFrom === "interview" ? interviewButton : undefined)?.current?.focus();
  }, [entry?.id]);

  // move changes the status with its Undo; the form's change and the history's
  // Put it back of a mail's move both go through it.
  const move = async (body: { status: string; on?: string; note?: string }, after?: () => void) => {
    if (!entry) return;
    setBusy(true);
    setFailure(null);
    setChanged(undefined);
    try {
      const r = await patch(`/api/candidacies/${entry.id}`, statusChangeResult, { ...body, expectedVersion: entry.version });
      setChanged({ n: (changed?.n ?? 0) + 1, from: entry.status, to: r.entry.status, version: r.changed ? r.entry.version : undefined });
      after?.();
      reload();
      onChanged();
    } catch (err) {
      setFailure(asApiError(err));
      setTarget("");
    } finally {
      setBusy(false);
    }
  };
  const change = (e: FormEvent) => {
    e.preventDefault();
    if (target) void move({ status: target, ...(target === "applied" && on ? { on } : {}), ...(note.trim() ? { note: note.trim() } : {}) }, () => (setNote(""), picker.current?.focus()));
  };
  // A status Gmail moved can be put back while it is still the row's latest
  // status change and the row still has the status the mail gave it.
  const moves = res.data?.detail.events.filter((ev) => ev.kind === "status_changed" || ev.kind === "status_corrected") ?? [];
  const mailMove = moves.at(-1)?.actor === "gmail" && moves.at(-1)?.newStatus === entry?.status ? moves.at(-1) : undefined;
  // The history reads newest first; a long one shows its latest events, the rest one press away.
  const history = useCapped([...(res.data?.detail.events ?? [])].reverse(), HISTORY_SHOWN);

  const undoChange = async (status: string, expectedVersion: number) => {
    setFailure(null);
    try {
      await patch(`/api/candidacies/${id}`, statusChangeResult, { status, expectedVersion });
      return true;
    } catch (err) {
      setFailure(asApiError(err));
      return false;
    } finally {
      reload();
      onChanged();
    }
  };

  return (
    <Modal title={entry ? `${entry.company}: ${entry.role}` : "Application"} variant="drawer" onClose={() => leave(onClose)} returnFocus={returnFocus}>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="this application" />}
      {res.data && entry && (
        <>
          <dl className="facts">
            <dt>Status</dt>
            <dd>
              <StatusCell entry={entry} />
            </dd>
            <dt>Score</dt>
            <dd>{formatScore(entry.score)}</dd>
            <dt>Date</dt>
            <dd>{formatDate(entry.date)}</dd>
            {entry.archivedAt && (
              <>
                <dt>Archived</dt>
                <dd>{formatDate(entry.archivedAt)}</dd>
              </>
            )}
            {entry.location && (
              <>
                <dt>Location</dt>
                <dd>{entry.location}</dd>
              </>
            )}
            <dt>Posting</dt>
            <dd>{entry.postingUrl ? <ExternalLink href={entry.postingUrl}>Open the posting</ExternalLink> : "No link"}</dd>
            {entry.reportNumber !== undefined && (
              <>
                <dt>Report</dt>
                <dd>
                  <Link to={`/reports?id=${entry.reportNumber}`}>Report {entry.reportNumber}</Link>
                </dd>
              </>
            )}
            {entry.notes && (
              <>
                <dt>Notes</dt>
                <dd className="pre">{noteLines(entry.notes)}</dd>
              </>
            )}
          </dl>

          <div className="toolbar">
            <Button ref={documentsButton} icon={FileText} onClick={() => leave(() => onDocuments(entry))}>
              Documents
            </Button>
            <Button ref={interviewButton} icon={MessagesSquare} onClick={() => leave(() => onInterview(entry))}>
              Interview prep
            </Button>
            {AFTER_APPLY.has(entry.status) && (
              <Button icon={Mail} onClick={() => setMessage("followup")}>
                Follow-up
              </Button>
            )}
            <Button icon={Send} onClick={() => setMessage("outreach")}>
              Outreach note
            </Button>
          </div>
          {message && <MessageDrafter id={entry.id} label={entry.company} mode={message} version={entry.version} applied={entry.status === "applied"} onChanged={() => { reload(); onChanged(); }} onClose={() => setMessage(undefined)} />}

          <h3>Status</h3>

          <form onSubmit={change} className="inline-form" aria-label="Change status">
            <label>
              Change status to
              <select ref={picker} value={target} onChange={(e) => setTarget(e.target.value)}>
                <option value="">Choose a status</option>
                {res.data.transitions.ordinary.map((s) => (
                  <option key={s} value={s}>
                    {humanize(s)}
                  </option>
                ))}
                {res.data.transitions.correction.length > 0 && (
                  <optgroup label="As a correction">
                    {res.data.transitions.correction.map((s) => (
                      <option key={s} value={s}>
                        {humanize(s)}
                      </option>
                    ))}
                  </optgroup>
                )}
              </select>
            </label>
            {target === "applied" && (
              <label>
                Applied on
                <input type="date" value={on || (entry.appliedAt ? formatDate(entry.appliedAt) : today())} max={today()} onChange={(e) => setOn(e.target.value)} />
              </label>
            )}
            <label>
              Note (optional)
              <textarea value={note} maxLength={NOTE_LIMIT} rows={2} onChange={(e) => setNote(e.target.value)} />
            </label>
            <button type="submit" disabled={!target || busy} ref={saveKey}>
              Change status
            </button>
          </form>
          <div aria-live="polite">
            {changed &&
              (changed.version === undefined ? (
                <p className="note ok">Status is {humanize(changed.to)} already.</p>
              ) : (
                <UndoNote key={changed.n} keepFocus done={`Status is now ${humanize(changed.to)}.`} undone={`Status is back to ${humanize(changed.from)}.`} onUndo={() => undoChange(changed.from, changed.version!)} />
              ))}
          </div>
          {failure && (
            <WriteFailure
              failure={failure}
              onReload={() => {
                setFailure(null);
                reload();
              }}
            />
          )}

          <ArchiveRow key={entry.id} entry={entry} onChanged={() => { reload(); onChanged(); }} />

          <h3>Notes</h3>
          <NoteForm entry={entry} onSaved={() => { reload(); onChanged(); }} />

          <LinkedMail id={entry.id} />

          <h3>History</h3>
          {res.data.detail.events.length === 0 ? (
            <Empty>No history yet.</Empty>
          ) : (
            <>
            <ol className="timeline">
              {history.shown.map((ev) => (
                <li key={ev.id}>
                  <time dateTime={ev.at}>{formatDate(ev.at)}</time> {ACTORS[ev.actor] ?? humanize(ev.actor)}: {eventText(ev, res.data!.detail.events)}
                  {ev.newStatus && ev.kind !== "transition_refused" ? ` (${ev.previousStatus ? humanize(ev.previousStatus) + " to " : ""}${humanize(ev.newStatus)})` : ""}
                  {ev === mailMove && ev.previousStatus && (
                    <>
                      {" "}
                      <button type="button" className="small" disabled={busy} onClick={() => void move({ status: ev.previousStatus! })}>
                        Put it back to {humanize(ev.previousStatus)}
                      </button>
                    </>
                  )}
                </li>
              ))}
            </ol>
            <ShowMore hidden={history.hidden} onMore={history.more} what="events" />
            </>
          )}
        </>
      )}
      <div className="actions">
        <button type="button" onClick={() => leave(onClose)}>
          Close
        </button>
      </div>
      {ask}
    </Modal>
  );
}

const HISTORY_SHOWN = 10;
const ACTORS: Record<string, string> = { user: "You", apply_chain: "Applysmith", agent: "Ask Agent", system: "Applysmith" };

// noteLines names each stored note's author ("<day> user: ...") as the
// history does.
const noteLines = (notes: string) => notes.replace(/^(\d{4}-\d{2}-\d{2}) ([a-z_]+):/gm, (_, day: string, actor: string) => `${day} ${ACTORS[actor] ?? humanize(actor)}:`);

// eventText names an event; an undo event (FollowupRequest.undo,
// NoteRequest.undo or a merge's undo) and the press it took back say so, as
// both stay in the append-only history. A note shows its words (the row keeps
// only ten); a refused automatic move says what it tried and the tracker's reason.
function eventText(ev: CandidacyEvent, all: CandidacyEvent[]) {
  const what = ev.kind === "followup_snoozed" ? "snooze" : ev.kind === "note_added" ? "note" : ev.kind === "merged" ? "merge" : "follow-up";
  if (ev.detail?.undoes) return `undid the ${what}`;
  const undone = all.some((u) => u.detail?.undoes === ev.id) ? " (undone)" : "";
  if (ev.kind === "note_added" && typeof ev.detail?.text === "string") return `note: ${ev.detail.text}${undone}`;
  if (ev.kind === "merged" && Array.isArray(ev.detail?.removed)) return `merged ${ev.detail.removed.map((id: number) => `#${id}`).join(", ")} into this row${undone}`;
  if (ev.kind === "transition_refused") return `did not move it to ${humanize(ev.newStatus ?? "")}${typeof ev.detail?.reason === "string" ? `: ${ev.detail.reason}` : ""}`;
  const words = EVENT_WORDS[ev.kind];
  return (words ? words(ev.detail ?? {}) : humanize(ev.kind).toLowerCase()) + undone;
}

// EVENT_WORDS says the events whose kind alone reads as a code name, from the
// detail the tracker stores with each (internal/tracker).
const DOC_FLAGS: Record<string, string> = { resume_pdf: "résumé PDF", cover_letter: "cover letter" };
const EVENT_WORDS: Record<string, (d: Record<string, any>) => string> = {
  followup_recorded: () => "recorded a follow-up",
  followup_snoozed: (d) => `snoozed the follow-up ${d.days} days`,
  document_flag_set: (d) => `made the ${DOC_FLAGS[d.flag] ?? "document"}`,
  document_flag_cleared: (d) => `unmarked the ${DOC_FLAGS[d.flag] ?? "document"}`,
  documents_override: () => "allowed a PDF below the score floor",
  applied_date_set: (d) => `set the day applied to ${formatDate(d.applied_at)}`,
  score_updated: (d) => `scored it ${formatScore(d.new_score ?? undefined)}${d.old_score == null ? "" : ` (was ${formatScore(d.old_score)})`}`,
  report_linked: (d) => `linked report ${d.to}`,
  duplicate_attached: (d) => `attached a duplicate posting (report ${d.report_number})`,
};

export default function Tracker() {
  const [params, setParams] = useSearchParams();
  const f = filtersOf(params);
  const rowId = Number(params.get("row")) || 0;
  const docsId = Number(params.get("documents")) || 0;
  const interviewId = Number(params.get("interview")) || 0;
  const [docsLabel, setDocsLabel] = useState("");
  // The Open button of each listed row, and the drawer that closed over the row
  // drawer last. The drawer is remembered only while a row is open.
  const openers = useRef(new Map<number, HTMLButtonElement>());
  const [backFrom, setBackFrom] = useState<"documents" | "interview">();
  useEffect(() => {
    if (rowId === 0) setBackFrom(undefined);
  }, [rowId, docsId, interviewId]);
  const movedLabel = `Moved${f.reached ? ` to ${humanize(f.reached)}` : ""}${f.movedBy ? ` by ${MOVERS[f.movedBy]}` : ""} ${sinceWords(f.since)}`;
  const filter = { status: f.status, company: f.company, source: f.source, appliedWithin: f.appliedWithin, reached: f.reached, movedBy: f.movedBy, since: f.since, mail: f.mail, query: f.q, minScore: f.minScore, archived: f.archived };
  const query = { ...filter, sort: f.sort, page: f.page, pageSize: f.pageSize };
  const [res, retry] = useResource(() => get("/api/candidacies", candidacyPage, query), [JSON.stringify(query)], ["tracker"], { keep: "tracker.rows" });
  usePageInRange(res.data && { ...res.data, rows: res.data.entries }, (p) => setParams({ page: p > 1 ? String(p) : undefined }, { replace: true }));
  const [adding, setAdding] = useState(false);
  // picked holds the ticked rows, on any page; it clears when the filters or the sort change.
  const [picked, setPicked] = useState<Map<number, Candidacy>>(new Map());
  const listed = { ...filter, sort: f.sort || "updated_desc" };
  useEffect(() => setPicked(new Map()), [JSON.stringify(listed)]);
  const selectAll = () => get("/api/candidacies", candidacyPage, { ...query, page: 1, pageSize: BULK_MAX }).then((p) => p.entries);
  // A drawer opened from a link names its row from the list when the row is on this page.
  const named = (id: number) => {
    const row = res.data?.entries.find((e) => e.id === id);
    return docsLabel || (row ? `${row.company}, ${row.role}` : `application ${id}`);
  };
  // The archived rows the filters also match, which the list leaves out.
  const hidden = res.data?.archived ?? 0;
  const showArchived = <Link to={withQuery("/tracker", { ...Object.fromEntries(params), archived: "true", page: undefined, row: undefined })}>{`Show ${hidden} archived ${hidden === 1 ? "row" : "rows"}`}</Link>;
  // focusRow moves the keyboard to a listed row's Open button; false when this page does not list the row.
  const focusRow = (id: number) => {
    const el = openers.current.get(id);
    el?.focus();
    return el !== undefined;
  };
  // A sorted column pressed again goes back to the default order.
  const sortBy = (sort: string) => setParams({ sort: f.sort === sort ? undefined : sort, page: undefined }, { replace: true });

  return (
    <>
      <PageHeader
        path="/tracker"
        actions={
          <Button variant="primary" icon={Plus} onClick={() => setAdding(true)}>
            Add application
          </Button>
        }
      />
      <OfflineScoresNote />
      <SummaryStrip selected={f.status} params={params} archived={f.archived} />
      {f.since && <ClearChip label={movedLabel} to={withQuery("/tracker", Object.fromEntries([...params].filter(([k]) => !["reached", "movedBy", "since", "page", "row"].includes(k))))} />}
      <FollowupsDue onOpen={(id) => setParams({ row: String(id) })} onChanged={retry} />
      <Duplicates onOpen={(id) => setParams({ row: String(id) })} onChanged={retry} focusRow={focusRow} />
      <Filters f={f} />
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={retry} what="the applications" />}
      {res.data &&
        (res.data.entries.length === 0 ? (
          hidden > 0 ? (
            <EmptyState icon={Archive} title={`Only archived rows match: ${hidden}.`} action={showArchived} />
          ) : f.status || f.company || f.source || f.appliedWithin || f.since || f.mail || f.q || f.minScore || f.archived ? (
            <EmptyState icon={ListChecks} title="No applications match these filters." action={<Link to="/tracker">Clear filters</Link>} />
          ) : (
            <EmptyState
              icon={ListChecks}
              title="No applications are tracked yet."
              action={
                <Link to="/discovery" className="button primary">
                  <Compass />
                  Find postings
                </Link>
              }
            >
              Evaluate a posting to add the first row.
            </EmptyState>
          )
        ) : (
          <>
            {/* The row count and the export links close the selection
                bar's line instead of taking lines of their own above it;
                the export stays out of the count's live region. */}
            <BulkStatus
              rows={res.data.entries}
              total={res.data.total}
              picked={picked}
              setPicked={setPicked}
              selectAll={selectAll}
              onDone={retry}
              end={
                <>
                  <p aria-live="polite" className="count-line">
                    {res.data.total} {res.data.total === 1 ? "row" : "rows"}
                    {hidden > 0 && <> {"\u00b7"} {showArchived}</>}
                  </p>
                  <p className="export">
                    <Download aria-hidden="true" />
                    Export these rows:{" "}
                    <FileLink path={withQuery("/api/tracker/export", { ...listed, format: "csv" })} name="tracker.csv">
                      CSV
                    </FileLink>{" "}
                    <FileLink path={withQuery("/api/tracker/export", { ...listed, format: "md" })} name="tracker.md">
                      Markdown
                    </FileLink>
                  </p>
                </>
              }
            />
            <div className="panel table-card">
              <table className="cards dense">
                <caption className="sr-only">Applications</caption>
                <thead>
                  <tr>
                    <SortHeader label="Company" ascending sorted={f.sort === "company_asc"} onSort={() => sortBy("company_asc")} />
                    <SortHeader label="Role" className="fill" ascending sorted={f.sort === "role_asc"} onSort={() => sortBy("role_asc")} />
                    <SortHeader label="Score" className="num" sorted={f.sort === "score_desc"} onSort={() => sortBy("score_desc")} />
                    <SortHeader label="Status" ascending sorted={f.sort === "status_asc"} onSort={() => sortBy("status_asc")} />
                    <SortHeader label="Date" className="nowrap" sorted={f.sort === "date_desc"} onSort={() => sortBy("date_desc")} />
                    <th scope="col">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {res.data.entries.map((e) => (
                    <tr key={e.id} className={e.id === rowId || picked.has(e.id) ? "selected" : undefined}>
                      <td data-label="Company" className="key">
                        <label className="check">
                          <input type="checkbox" checked={picked.has(e.id)} onChange={(ev) => { const next = new Map(picked); void (ev.target.checked ? next.set(e.id, e) : next.delete(e.id)); setPicked(next); }} aria-label={`Select ${e.company}, ${e.role}`} />
                          <strong className="truncate" title={e.company}>
                            {e.company}
                          </strong>
                        </label>
                      </td>
                      <td data-label="Role" className="fill">
                        <Truncate text={e.role} />
                        {shownNote(e, f.q) && (
                          <span className="truncate muted sub" title={e.notes}>
                            {shownNote(e, f.q)}
                          </span>
                        )}
                      </td>
                      <td data-label="Score" className="num">
                        <ScoreBadge score={e.score} />
                      </td>
                      <td data-label="Status" className="nowrap">
                        <StatusCell entry={e} />
                        {e.appliedAt && <span className="sub muted">Applied {formatDate(e.appliedAt)}</span>}
                        {e.archivedAt && <span className="sub muted">Archived {formatDate(e.archivedAt)}</span>}
                        {e.mail && <RowMail mail={e.mail} />}
                      </td>
                      {/* The source reads under the date rather than in a
                          column of its own, which the role needs more. */}
                      <td data-label="Date" className="nowrap">
                        {formatDate(e.date)}
                        {e.source && <span className="truncate sub muted" title={e.source}>{e.source}</span>}
                      </td>
                      <td className="row-actions">
                        <button
                          type="button"
                          className="small"
                          aria-label={`Open ${e.company}, ${e.role}`}
                          ref={(el) => void (el ? openers.current.set(e.id, el) : openers.current.delete(e.id))}
                          onClick={() => setParams({ row: String(e.id) })}
                        >
                          Open
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {res.data.total > res.data.pageSize && <Pager page={res.data.page} pageSize={res.data.pageSize} total={res.data.total} onPage={(p) => setParams({ page: p > 1 ? String(p) : undefined })} />}
          </>
        ))}
      <WeekFunnel />
      <OutcomeReview />
      {adding && (
        <AddApplication
          onClose={() => setAdding(false)}
          onAdded={(id) => {
            setAdding(false);
            retry();
            setParams({ row: String(id) });
          }}
        />
      )}
      {rowId > 0 && docsId === 0 && interviewId === 0 && (
        <RowDrawer
          id={rowId}
          backFrom={backFrom}
          returnFocus={() => openers.current.get(rowId)}
          onClose={() => setParams({ row: undefined }, { replace: true })}
          onChanged={retry}
          onDocuments={(entry) => {
            setDocsLabel(`${entry.company}, ${entry.role}`);
            setParams({ documents: String(entry.id) });
          }}
          onInterview={(entry) => {
            setDocsLabel(`${entry.company}, ${entry.role}`);
            setParams({ interview: String(entry.id) });
          }}
        />
      )}
      {interviewId > 0 && (
        <InterviewDrawer
          candidacyId={interviewId}
          label={named(interviewId)}
          returnFocus={() => openers.current.get(interviewId)}
          onClose={() => {
            setBackFrom("interview");
            setParams({ interview: undefined }, { replace: true });
          }}
        />
      )}
      {docsId > 0 && (
        <DocumentsDrawer
          candidacyId={docsId}
          label={named(docsId)}
          returnFocus={() => openers.current.get(docsId)}
          onClose={() => {
            setBackFrom("documents");
            setParams({ documents: undefined }, { replace: true });
          }}
        />
      )}
    </>
  );
}
