import { useState } from "react";
import { get, getModel, post, postModel } from "../../api/client";
import { candidacy, candidacyDetail } from "../../api/decoders";
import { gmailSummary, gmailThreadDetail, gmailThreadDone, gmailThreadDraft } from "../../api/decoders_gmail";
import { useResource } from "../../hooks";
import { CopyButton } from "../../ui/CopyButton";
import { Modal } from "../../ui/Modal";
import { Link } from "../../router";
import { Chip, Empty, ErrorNote, ExternalLink, Loading, UndoNote, humanize } from "../../ui/parts";
import type { GmailDraft, GmailInboxThread, GmailTimelineEntry } from "../../wire";
import { useAction, when } from "../action";
import { ASKING, ATTENTION, LANES, OWNERS, reasons, type Decisions } from "./Inbox";
import { LinkQueue, Moved } from "./Links";

// What the matching did with a mail, by its link decision.
const MATCH: Record<string, string> = { auto: "Matched to your application", review: "Which application it belongs to needs your check", unlinked: "Not matched to an application" };

export const gmailUrl = (threadId: string) => `https://mail.google.com/mail/u/0/#all/${encodeURIComponent(threadId)}`;

function Timeline({ entries }: { entries: GmailTimelineEntry[] }) {
  if (entries.length === 0) return <Empty>No signal is stored for this thread.</Empty>;
  return (
    <ol className="timeline">
      {entries.map((e) => (
        <li key={e.messageId}>
          <p>
            <strong>{humanize(e.type)}</strong> <Chip>{e.direction === "outgoing" ? "From you" : "From them"}</Chip> {when(e.receivedAt)}
          </p>
          <p>{e.subject}</p>
          {e.snippet && <p className="muted">{e.snippet}</p>}
          <p className="muted" title={`Rule ${e.ruleId}`}>
            {MATCH[e.link] && `${MATCH[e.link]}${e.linkReasons.length > 0 ? `: ${reasons(e.linkReasons)}` : ""}.`}
            {e.reviewReasons.length > 0 ? ` Held for your check: ${reasons(e.reviewReasons)}.` : ""}
            {e.dueAt ? ` Due ${when(e.dueAt)}.` : ""}
          </p>
          {e.outcome && e.outcome.result !== "no_change" && <Moved o={e.outcome} now={{ status: e.rowStatus, putBack: e.putBack }} className="note" />}
        </li>
      ))}
    </ol>
  );
}

function Summary({ threadId }: { threadId: string }) {
  const [res, reload] = useResource(() => getModel(`/api/gmail/threads/${encodeURIComponent(threadId)}/summary`, gmailSummary), [threadId]);
  return (
    <section aria-labelledby="gmail-summary">
      <h3 id="gmail-summary">Summary</h3>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />}
      {res.data && (
        <>
          <p>{res.data.text}</p>
          <p className="muted">
            A summary made {when(res.data.generatedAt)}, {res.data.method === "model" ? "by a model" : "from the stored signals, without a model"}
            {res.data.cached ? ", reused because the thread has not changed" : ""}.
          </p>
        </>
      )}
    </section>
  );
}

function Lines({ title, lines = [] }: { title: string; lines?: string[] }) {
  return lines.length > 0 ? <><p className="label">{title}</p><ul>{lines.map((l) => <li key={l}>{l}</li>)}</ul></> : null;
}

// Draft shows the draft kept for the thread's current state (from a digest or
// a request) and writes a new one. The draft it replaced is kept folded in this
// tab's session storage, so closing the drawer or reloading does not lose it; a
// blocked store only loses it. Nothing is ever sent. I replied notes it on the
// linked row, records a follow-up on an applied row, as the Tracker's I sent
// this, and marks a thread that asked for the reply done, as Done does; Undo
// takes back all of them on the server, each against the version it made.
function Draft({ thread: t, d }: { thread: GmailInboxThread; d: Decisions }) {
  const { threadId, subject, candidacyId } = t;
  const path = `/api/gmail/threads/${encodeURIComponent(threadId)}/draft`;
  const key = `applysmith-draft:gmail:${threadId}`;
  const [res, reload] = useResource(() => get(path, gmailThreadDraft), [threadId], ["gmail"]);
  const [previous, setPrevious] = useState<GmailDraft | undefined>(() => {
    try {
      return JSON.parse(window.sessionStorage.getItem(key) ?? "null") ?? undefined;
    } catch {
      return undefined;
    }
  });
  const [replied, setReplied] = useState<{ n: number; done: string; undone: string; undo: () => Promise<unknown> }>();
  const a = useAction();
  const r = useAction();
  const draft = res.data?.draft;
  const what = res.data?.kind === "follow_up" ? "follow-up" : "reply";
  const write = async () => {
    if (await a.run(() => postModel(path, gmailThreadDraft))) {
      if (draft) {
        setPrevious(draft);
        try {
          window.sessionStorage.setItem(key, JSON.stringify(draft));
        } catch {
          // A blocked store only loses the replaced draft.
        }
      }
      reload();
    }
  };
  const markReplied = (id: number) => r.run(async () => {
    const notes = `/api/candidacies/${id}/notes`;
    const row = (await get(`/api/candidacies/${id}`, candidacyDetail)).entry;
    const e = await post(notes, candidacy, { text: `Replied by email to "${subject}".`, expectedVersion: row.version });
    const n = (replied?.n ?? 0) + 1;
    let [done, undone, undo] = ["Added to the row's notes", "The note is off the row", (): Promise<unknown> => post(notes, candidacy, { undo: true, expectedVersion: e.version })];
    const say = () => setReplied({ n, done: `${done}.`, undone: `${undone}.`, undo });
    say();
    if (what === "follow-up" && row.status === "applied") {
      const path = `/api/candidacies/${id}/followup`;
      const f = await post(path, candidacy, { expectedVersion: e.version });
      [done, undone] = ["Noted on the row and counted as a follow-up", "The note and the follow-up are taken back"];
      undo = async () => post(notes, candidacy, { undo: true, expectedVersion: (await post(path, candidacy, { undo: true, expectedVersion: f.version })).version });
      say();
    }
    if (!t.done && ASKING.includes(t.lane)) {
      const thread = `/api/gmail/inbox/${encodeURIComponent(threadId)}`;
      await post(`${thread}/done`, gmailThreadDone);
      const rest = undo;
      [done, undone, undo] = [`${done}; the thread is marked done`, `${undone}; the thread is open again`, async () => (await post(`${thread}/reopen`, gmailThreadDone), await rest(), d.refresh())];
      say();
      d.refresh();
    }
  });
  return (
    <section aria-labelledby="gmail-draft">
      <h3 id="gmail-draft">{what === "reply" ? "Reply draft" : "Follow-up draft"}</h3>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />}
      {res.data &&
        (draft ? (
          <>
            <pre className="pre">{draft.text}</pre>
            <Lines title="Rests on your own material" lines={draft.confirmed} />
            <Lines title="Check before sending" lines={draft.toConfirm} />
            <p className="muted">
              Written {when(draft.generatedAt)} from {draft.method === "application" ? "your application's material and this thread" : "this thread's messages"}
              {draft.note && <> alone: {draft.note}; choose one under <Link to="/settings#settings-models">Settings, Models</Link></>}. You review it and send it yourself; the product never sends mail.
            </p>
          </>
        ) : (
          <Empty>No draft yet. Write one from this thread's messages; you review it and send it yourself.</Empty>
        ))}
      {res.data && (
        <div className="actions">
          {draft && <CopyButton text={draft.text} label={`Copy the ${what} draft`} />}
          <button type="button" disabled={a.busy} onClick={() => void write()}>
            {a.busy ? "Drafting..." : draft ? "Regenerate" : `Draft a ${what}`}
          </button>
          {candidacyId !== undefined && (
            <button type="button" disabled={r.busy} onClick={() => void markReplied(candidacyId)} title="Notes the reply on the application and marks the thread done.">
              I replied
            </button>
          )}
        </div>
      )}
      <div aria-live="polite">
        {a.error && <ErrorNote error={a.error} />}
        {r.error && <ErrorNote error={r.error} />}
        {replied && <UndoNote key={replied.n} done={replied.done} undone={replied.undone} onUndo={async () => (await r.run(replied.undo)) !== undefined} />}
      </div>
      {previous && previous.text !== draft?.text && (
        <details>
          <summary>{draft ? "The draft this one replaced" : "An earlier draft"}</summary>
          <pre className="pre">{previous.text}</pre>
          <p className="muted">Written {when(previous.generatedAt)}.</p>
          <CopyButton text={previous.text} label={`Copy the replaced ${what} draft`} />
        </details>
      )}
    </section>
  );
}

export function ThreadDrawer({ threadId, d, onClose }: { threadId: string; d: Decisions; onClose: () => void }) {
  const [res, reload] = useResource(() => get(`/api/gmail/inbox/${encodeURIComponent(threadId)}`, gmailThreadDetail), [threadId, d.rev], ["gmail"]);
  const t = res.data?.thread;
  // A decision closes the drawer: the list takes focus on the next row, and
  // the note above the tabs carries the undo.
  const decide = async (kind: "done" | "dismiss") => {
    if (t && (await d.decide(t, kind))) onClose();
  };
  return (
    <Modal title={t ? `${t.employer}${t.role ? `: ${t.role}` : ""}` : "Thread"} variant="drawer" onClose={onClose}>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />}
      {res.data && t && (
        <>
          <p>{t.subject}</p>
          <p className="muted">
            <Chip>{LANES[t.lane] ?? t.lane}</Chip> <Chip>{ATTENTION[t.attention] ?? t.attention}</Chip> {t.done && <Chip tone="good">done</Chip>} {OWNERS[t.owner] ?? t.owner}. Next: {t.nextAction}.
          </p>
          <div className="actions">
            <ExternalLink href={gmailUrl(threadId)}>Open the thread in Gmail</ExternalLink>
            {t.candidacyId !== undefined && <Link to={`/tracker?row=${t.candidacyId}`}>Open the application</Link>}
          </div>
          <div className="actions">
            {t.done ? (
              <button type="button" disabled={d.busy} onClick={() => void d.reopen(threadId)}>
                Reopen
              </button>
            ) : (
              <>
                {ASKING.includes(t.lane) && (
                  <button type="button" disabled={d.busy} onClick={() => void decide("done")} title="You dealt with it. A new message on the thread brings it back.">
                    Mark done
                  </button>
                )}
                <button type="button" disabled={d.busy} onClick={() => void decide("dismiss")} title="Not a job email, or not for you. A new message on the thread brings it back.">
                  Dismiss
                </button>
              </>
            )}
          </div>
          <Summary threadId={threadId} />
          <Draft thread={t} d={d} />
          <h3>Timeline</h3>
          <Timeline entries={res.data.timeline} />
          <LinkQueue threadId={threadId} />
        </>
      )}
      <div className="actions">
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </Modal>
  );
}
