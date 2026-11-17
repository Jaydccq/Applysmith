import { useEffect, useRef, useState } from "react";
import { get, post } from "../../api/client";
import { gmailInboxPage, gmailLinkRestored, gmailThreadDismissed, gmailThreadDone } from "../../api/decoders_gmail";
import { useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { Chip, ErrorNote, Panel } from "../../ui/parts";
import type { GmailInboxThread } from "../../wire";
import { useAction, when } from "../action";
import { EmptyState } from "../../ui/EmptyState";
import { MailCheck } from "lucide-react";

const label = (v: string) => v.replaceAll("_", " ");

export const PAGE_SIZE = 25;

// The lanes, owners, links and attention of a thread in the words a job
// seeker uses; the raw ids stay on the wire and in tooltips.
export const LANES: Record<string, string> = { do_now: "Do now", prepare: "Prepare", review: "To check", waiting: "Waiting on them", closed: "Closed", receipt_only: "Receipts" };
export const OWNERS: Record<string, string> = { user: "Yours to do", company: "Their move", none: "Nothing to do", unclear: "Unclear whose move" };
const LINKS: Record<string, string> = { auto: "Matched", review: "Match to check", unlinked: "Not matched" };
export const ATTENTION: Record<string, string> = { urgent: "Urgent", action: "Needs action", stale: "Waiting long", info: "For your information" };
// Why a mail was or was not matched to an application, or held for a check.
const REASONS: Record<string, string> = {
  clear_single_match: "the only application matching the company and role",
  ambiguous_runner_up: "another application matches almost as well",
  below_auto_threshold: "the match is too weak to be sure",
  no_viable_candidate: "no application in your tracker matches",
  thread_continuity: "an earlier mail on this thread was matched to it",
  thread_multi_linked: "this thread was matched to more than one application",
  duplicate_cluster: "your tracker holds more than one row for it",
  role_qualifier_mismatch: "the role differs from the application's",
  tracker_refused: "the tracker refused the status change",
  confidence_below_threshold: "the reading of the mail is not sure",
  company_unknown: "the company is unknown",
  company_identity_conflict: "the company is unclear",
  company_is_job_board: "it came through a job board",
  sender_unverified: "the sender is not at the company's address",
  signup_window_unknown: "the sign-up window is unknown",
};
export const reasons = (list: string[]) => list.map((r) => REASONS[r] ?? label(r)).join("; ");

// The kinds a thread can be filtered by, in the words the user knows the mail by.
export const KINDS: Record<string, string> = {
  recruiter_reply_needed: "Reply needed",
  action_required: "Action required",
  online_assessment: "Assessment",
  interview_scheduled: "Interview",
  interview_reminder: "Interview reminder",
  offer: "Offer",
  rejection: "Rejection",
  applied_confirmation: "Application received",
  review: "Needs review",
};


// A thread asks something of the user in these lanes, so only there can it be marked done.
export const ASKING = ["do_now", "prepare", "review"];
const ATTENTION_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { urgent: "bad", action: "warn", stale: "warn" };
const LINK_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { auto: "good", review: "warn" };

function Select({ name, value, options, all, onChange }: { name: string; value: string; options: Record<string, string>; all: string; onChange: (v: string) => void }) {
  return (
    <label>
      {name}
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{all}</option>
        {Object.entries(options).map(([o, text]) => (
          <option key={o} value={o}>
            {text}
          </option>
        ))}
      </select>
    </label>
  );
}

type Note = { threadId: string; subject: string; kind: "done" | "dismiss"; signalIds: string[] };

// useDecisions holds what the user decided about threads: a thread marked done
// or dismissed leaves the lane that asked for it, a note with Undo says so, and
// a new message on the thread brings it back whatever was decided. rev changes
// after every write, so the lists and the open thread read again; decided names
// the thread just decided (or, with undo, restored), for the list to move focus on.
export function useDecisions() {
  const [notes, setNotes] = useState<Note[]>([]);
  const [rev, setRev] = useState(0);
  const notesEl = useRef<HTMLDivElement>(null);
  const [decided, setDecided] = useState<{ id: string; seq: number; undo?: boolean } | null>(null);
  const act = useAction();
  const path = (id: string, verb: string) => `/api/gmail/inbox/${encodeURIComponent(id)}/${verb}`;
  const written = () => setRev((n) => n + 1);
  const drop = (id: string) => setNotes((all) => all.filter((n) => n.threadId !== id));
  const decide = async (t: { threadId: string; subject: string }, kind: Note["kind"]) => {
    const out = await act.run(async () => ({ ids: kind === "done" ? (await post(path(t.threadId, "done"), gmailThreadDone), []) : (await post(path(t.threadId, "dismiss"), gmailThreadDismissed)).signalIds }));
    if (!out) return false;
    drop(t.threadId);
    setNotes((all) => [...all, { threadId: t.threadId, subject: t.subject, kind, signalIds: out.ids }]);
    setDecided((d) => ({ id: t.threadId, seq: (d?.seq ?? 0) + 1 }));
    written();
    return true;
  };
  const reopen = async (id: string, undo?: Note) => {
    const ok = await act.run(async () => {
      if (undo?.kind === "dismiss") await Promise.all(undo.signalIds.map((s) => post(`/api/gmail/links/${encodeURIComponent(s)}/restore`, gmailLinkRestored)));
      else await post(path(id, "reopen"), gmailThreadDone);
      return true;
    });
    if (ok) {
      drop(id);
      setDecided((d) => ({ id, seq: (d?.seq ?? 0) + 1, undo: true }));
      written();
    }
  };
  return { notes, notesEl, rev, decided, busy: act.busy, error: act.error, decide, reopen, refresh: written };
}

export type Decisions = ReturnType<typeof useDecisions>;

// DecisionNotes says what was just decided and offers the undo. It sits above
// the tabs, so a decision made from a thread opened on any tab stays undoable.
export function DecisionNotes({ d }: { d: Decisions }) {
  return (
    <div aria-live="polite" ref={d.notesEl} tabIndex={-1}>
      {d.notes.map((n) => (
        <p key={n.threadId} className="note ok">
          {n.kind === "done" ? "Marked done" : "Dismissed"}: {n.subject}. A new message on the thread brings it back.{" "}
          <button type="button" disabled={d.busy} onClick={() => void d.reopen(n.threadId, n)}>
            Undo
          </button>
        </p>
      ))}
      {d.error && <ErrorNote error={d.error} />}
    </div>
  );
}

function Row({ t, d, onOpen }: { t: GmailInboxThread; d: Decisions; onOpen: (id: string) => void }) {
  return (
    <li className="panel thread-row">
      <header className="row">
        <h3>
          {t.employer}
          {t.role ? `: ${t.role}` : ""}
        </h3>
        {/* An offer or a rejection sorts first as urgent but asks nothing of the user, so a thread with nothing to do shows no urgency. */}
        <span>
          <Chip>{LANES[t.lane] ?? label(t.lane)}</Chip>{" "}
          {t.owner !== "none" && (
            <>
              <Chip tone={ATTENTION_TONE[t.attention] ?? "neutral"}>{ATTENTION[t.attention] ?? t.attention}</Chip>{" "}
            </>
          )}
          <Chip tone={LINK_TONE[t.link] ?? "neutral"}>{LINKS[t.link] ?? t.link}</Chip>
        </span>
      </header>
      <p>{t.subject}</p>
      <p className="muted">
        {KINDS[t.kind] ?? label(t.kind)}. {OWNERS[t.owner] ?? t.owner}. Next: {label(t.nextAction)}.{t.dueAt ? ` Due ${when(t.dueAt)}.` : ""} Last mail {when(t.lastIncomingAt)}
        {t.stalenessDays > 0 ? `, ${t.stalenessDays} ${t.stalenessDays === 1 ? "day" : "days"} waiting` : ""}.{t.candidacyId !== undefined && (
          <>
            {" "}Linked to <Link to={`/tracker?row=${t.candidacyId}`}>application {t.candidacyId}</Link>.
          </>
        )}
      </p>
      <div className="actions">
        <button type="button" onClick={() => onOpen(t.threadId)} aria-label={`Open the thread: ${t.subject}`}>
          Open
        </button>
        {t.done ? (
          <button type="button" disabled={d.busy} onClick={() => void d.reopen(t.threadId)} aria-label={`Reopen the thread: ${t.subject}`}>
            Reopen
          </button>
        ) : (
          <>
            {ASKING.includes(t.lane) && (
              <button type="button" disabled={d.busy} onClick={() => void d.decide(t, "done")} aria-label={`Mark done: ${t.subject}`} title="You dealt with it. A new message on the thread brings it back.">
                Done
              </button>
            )}
            <button type="button" disabled={d.busy} onClick={() => void d.decide(t, "dismiss")} aria-label={`Dismiss the thread: ${t.subject}`} title="Not a job email, or not for you. A new message on the thread brings it back.">
              Dismiss
            </button>
          </>
        )}
      </div>
    </li>
  );
}

// Inbox is the action inbox. The server pages it with an opaque cursor, so the
// cursors of the pages already seen are kept to go back; a change of filter
// starts over from the first page.
export function Inbox({ stale, syncing, d }: { stale: boolean; syncing: boolean; d: Decisions }) {
  const [params, setParams] = useSearchParams();
  const lane = params.get("lane") ?? "";
  const owner = params.get("owner") ?? "";
  const link = params.get("link") ?? "";
  const kind = params.get("kind") ?? "";
  const q = params.get("q") ?? "";
  const [seen, setSeen] = useState<{ key: string; cursors: string[] }>({ key: "", cursors: [] });
  const key = `${lane}|${owner}|${link}|${kind}|${q}`;
  const filtered = lane !== "" || owner !== "" || link !== "" || kind !== "" || q !== "";
  const cursors = seen.key === key ? seen.cursors : [];
  const cursor = cursors.at(-1);
  const [res, reload] = useResource(() => get("/api/gmail/inbox", gmailInboxPage, { lane, owner, link, kind, q, cursor, limit: PAGE_SIZE }), [lane, owner, link, kind, q, cursor, d.rev], ["gmail"]);
  // The sidebar counts the do-now lane: with every lane listed, that count
  // opens it. The request is the sidebar's own, so the two share it.
  const [now] = useResource(() => get("/api/gmail/inbox", gmailInboxPage, { lane: "do_now", limit: 1 }), [], ["gmail"]);
  const filter = (patch: Record<string, string | undefined>) => setParams(patch, { replace: true });
  const open = (id: string) => setParams({ thread: id });
  // The search applies a moment after typing stops, or at once on Enter.
  const [text, setText] = useState(q);
  useEffect(() => setText(q), [q]);
  useEffect(() => {
    if (text.trim() === q) return;
    const wait = setTimeout(() => filter({ q: text.trim() || undefined }), 300);
    return () => clearTimeout(wait);
  }, [text]);
  // After a decision, focus goes to the row that took the decided one's place
  // once the list has read again, never back to the top of the page. After an
  // Undo it goes to the restored row, or to the notes when the filter hides it.
  const list = useRef<HTMLUListElement>(null);
  const after = useRef<{ id: string; at: number; from: unknown; undo?: boolean } | null>(null);
  const ids = res.data?.threads.map((t) => t.threadId) ?? [];
  const mounted = useRef(d.decided?.seq);
  useEffect(() => {
    if (d.decided && d.decided.seq !== mounted.current) after.current = { id: d.decided.id, at: ids.indexOf(d.decided.id), from: res.data, undo: d.decided.undo };
  }, [d.decided]);
  useEffect(() => {
    const a = after.current;
    if (!a || !res.data || res.data === a.from) return;
    after.current = null;
    const rows = [...(list.current?.querySelectorAll<HTMLElement>(":scope > li") ?? [])];
    const i = ids.indexOf(a.id);
    if (a.undo) return void ((i >= 0 ? rows[i].querySelector<HTMLElement>(".actions button") : d.notesEl.current)?.focus());
    const next = rows[Math.min(i >= 0 ? i + 1 : a.at, rows.length - 1)];
    if (i >= 0 || a.at >= 0) next?.querySelector<HTMLElement>(".actions button:not(:disabled)")?.focus();
  }, [res.data]);

  return (
    <Panel title="Action inbox" id="gmail-inbox" res={res} retry={reload}>
      {(page) => (
        <>
          {stale && <p role="status" className="note">The connection has expired. This is the last known data.</p>}
          <form className="filters" aria-label="Inbox filters" onSubmit={(e) => (e.preventDefault(), filter({ q: text.trim() || undefined }))}>
            <label>
              Search
              <input type="search" data-page-search value={text} placeholder="Employer or role" onChange={(e) => setText(e.target.value)} />
            </label>
            <Select name="Lane" value={lane} options={LANES} all="All lanes" onChange={(v) => filter({ lane: v })} />
            <Select name="Whose move" value={owner} options={OWNERS} all="Anyone's" onChange={(v) => filter({ owner: v })} />
            <Select name="Application" value={link} options={LINKS} all="Matched or not" onChange={(v) => filter({ link: v })} />
            <Select name="Kind" value={kind} options={KINDS} all="Any kind" onChange={(v) => filter({ kind: v })} />
          </form>
          {lane === "" && (now.data?.total ?? 0) > 0 && (
            <p className="count-line">
              <Link to="/gmail?lane=do_now">{now.data?.total} need you now</Link>
            </p>
          )}
          {page.threads.length === 0 ? (
            <EmptyState
              icon={MailCheck}
              title={cursors.length > 0 ? "No more emails." : filtered ? "Nothing matches these filters." : syncing ? "Reading your mail." : "Nothing needs action."}
              action={filtered && cursors.length === 0 ? <button type="button" onClick={() => filter({ lane: undefined, owner: undefined, link: undefined, kind: undefined, q: undefined })}>Clear filters</button> : undefined}
            >
              {!filtered && cursors.length === 0 && (syncing ? "A sync is running; its threads are listed here when it ends." : "A thread appears here when a job email needs a reply, a document or a date from you.")}
            </EmptyState>
          ) : (
            <ul className="plain stack" ref={list}>
              {page.threads.map((t) => (
                <Row key={t.threadId} t={t} d={d} onOpen={open} />
              ))}
            </ul>
          )}
          <nav aria-label="Inbox pages" className="pager">
            <button type="button" disabled={cursors.length === 0} onClick={() => setSeen({ key, cursors: cursors.slice(0, -1) })}>
              Previous
            </button>
            <span aria-live="polite">
              {page.total} {page.total === 1 ? "thread" : "threads"}, page {cursors.length + 1} of {Math.max(1, Math.ceil(page.total / PAGE_SIZE))}
            </span>
            <button type="button" disabled={!page.next} onClick={() => setSeen({ key, cursors: [...cursors, page.next!] })}>
              Next
            </button>
          </nav>
        </>
      )}
    </Panel>
  );
}
