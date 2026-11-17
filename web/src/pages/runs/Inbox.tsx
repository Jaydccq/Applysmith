import { useEffect, useRef, useState } from "react";
import { get, newRequestId, post, request } from "../../api/client";
import { evaluationView, inboxRowDecoder } from "../../api/decoders_discovery";
import { boardEntry, runAccepted } from "../../api/decoders_runs";
import { asApiError, useResource } from "../../hooks";
import { Link } from "../../router";
import { ReportDrawer } from "../applynext/ReportDrawer";
import { showTab } from "../applynext/showTab";
import { ShowInPanel } from "../browser/Lanes";
import { Chip, Empty, ErrorNote, ScoreBadge, ShowMore, useCapped } from "../../ui/parts";
import type { ApiError } from "../../api/client";
import type { BoardEntry, EvaluationView, NeedsYouItem } from "../../wire";
import { ActionFocusTab } from "../../wire";
import { fixOf, groupWaiting, plainDetail, waitedLabel, type WaitingGroup } from "./format";

// primary is the one action an item offers: bring its kept tab forward, review
// what the run produced, or resolve what stopped it. An item whose fix is made
// on another page offers that page instead of the run (Row).
function primary(item: NeedsYouItem): { label: string; tab: boolean } {
  if (item.action === ActionFocusTab) return { label: "Show tab", tab: true };
  return { label: item.entry.reason === "answer" ? "Answer" : item.entry.reason === "review" ? "Review" : "Resolve", tab: false };
}

// isReview is a quick screen's review: the posting is the user's to send to an
// evaluation in depth or to skip.
const isReview = (e: BoardEntry) => e.kind === "evaluate" && e.reason === "review";

// INBOX_CAP is how many rows the list draws before "Show more".
const INBOX_CAP = 20;

interface RowActs {
  ack: (key: string, ids: string[]) => void;
  done: (key: string, id: string) => void;
  show: (key: string, id: string) => void;
  scan: (key: string, source: string) => void;
  full: (key: string, v: EvaluationView) => void;
  skip: (key: string, ids: string[], v: EvaluationView) => void;
  read: (key: string, v: EvaluationView) => void;
}

// Row is one posting stopped for one reason. Repeated runs of it fold into the
// row: it shows the newest, counts the others and lists them on request, and
// Acknowledge clears them all.
function Row({ group, pending, act, started }: { group: WaitingGroup; pending: string | null; act: RowActs; started?: string }) {
  const [open, setOpen] = useState(false);
  const [newest, ...older] = group.items;
  const e = newest.entry;
  const action = primary(newest);
  const fix = fixOf(e);
  const scan = fix?.scan;
  // A review is decided here: the screen's score, its reason and the report
  // come from the evaluation, and each answer is one press.
  const review = isReview(e);
  const [res] = useResource(() => (review ? get(`/api/evaluations/${e.id}`, evaluationView) : Promise.resolve(undefined)), [e.id, review]);
  const v = review ? res.data : undefined;
  // Until the evaluation is read, a review offers nothing rather than the
  // generic buttons it is about to lose.
  const deciding = review && res.status !== "error";
  const why = plainDetail(e.detail, e.reason, e.kind);
  const reason = v?.reviewReason ? [v.reviewReason, v.explanation].filter(Boolean).join(" ") : why?.line;
  const busy = pending === group.key;
  const unseen = group.items.some((i) => !i.entry.seenAt);
  const n = group.items.length;
  return (
    <li className="inbox-item">
      <span className="item-main">
        <Link to={`/runs/${e.id}`}>{e.title}</Link>
        {n > 1 && (
          <>
            {" "}
            <Chip>{`${n} runs`}</Chip>
          </>
        )}
        {unseen && (
          <>
            {" "}
            <span className="unseen-dot" role="img" aria-label="Unseen" title="Unseen" />
          </>
        )}
        {reason && (
          <span className="item-detail clamp" title={reason}>
            {v?.score !== undefined && (
              <>
                <ScoreBadge score={v.score} />{" "}
              </>
            )}
            {reason}
          </span>
        )}
        <span className="item-detail">
          {e.state === "blocked" ? "Blocked" : "Waiting"} {waitedLabel(newest.waitingSeconds)}
          {n > 1 && (
            <>
              {" "}
              <button type="button" className="small ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
                {open ? "Hide earlier runs" : `Show ${older.length} earlier ${older.length === 1 ? "run" : "runs"}`}
              </button>
            </>
          )}
        </span>
        {open && (
          <ul className="plain inbox-repeats">
            {older.map((i) => (
              <li key={i.entry.id}>
                <Link to={`/runs/${i.entry.id}`}>Waiting {waitedLabel(i.waitingSeconds)}</Link>
              </li>
            ))}
          </ul>
        )}
      </span>
      <span className="actions">
        {v && (
          <>
            {v.reportNumber !== undefined && (
              <button type="button" className="small ghost" aria-label={`Read report ${v.reportNumber}: ${e.title}`} onClick={() => act.read(group.key, v)}>
                Report {v.reportNumber}
              </button>
            )}
            {started ? (
              <span className="note ok">
                Evaluating in full. <Link to={`/runs/${started}`}>Follow it</Link>
              </span>
            ) : (
              <>
                <button type="button" className="primary" aria-label={`Evaluate in full: ${e.title}`} disabled={busy || !v.postingUrl} title={v.postingUrl ? undefined : "The posting has no address to evaluate."} onClick={() => act.full(group.key, v)}>
                  Evaluate in full
                </button>
                <button type="button" aria-label={n > 1 ? `Skip ${n} runs of ${e.title}` : `Skip ${e.title}`} disabled={busy} onClick={() => act.skip(group.key, group.items.map((i) => i.entry.id), v)}>
                  Skip
                </button>
              </>
            )}
          </>
        )}
        {fix && (
          <Link to={fix.to} className="button">
            {fix.label}
          </Link>
        )}
        {deciding ? null : action.tab ? (
          <button type="button" disabled={!e.canFocusTab || busy} title={e.canFocusTab ? undefined : "No tab is kept for this run."} onClick={() => act.show(group.key, e.id)}>
            {action.label}
          </button>
        ) : (
          !fix && (
            <Link to={`/runs/${e.id}`} className="button">
              {action.label}
            </Link>
          )
        )}
        {!deciding && e.focusTab && <ShowInPanel tabId={e.focusTab} />}
        {scan && (
          <button type="button" aria-label={`Run the ${scan} scan again`} disabled={busy} title="Run the scan again once the sign-in is synced." onClick={() => act.scan(group.key, scan)}>
            Run the scan again
          </button>
        )}
        {e.canDone && (
          <button type="button" className="ghost" aria-label={`Done ${e.title}`} disabled={busy} title="Press after you finished the step in the browser tab." onClick={() => act.done(group.key, e.id)}>
            Done
          </button>
        )}
        {!deciding && (
          <button type="button" className="ghost" aria-label={n > 1 ? `Acknowledge ${n} runs of ${e.title}` : `Acknowledge ${e.title}`} disabled={busy} onClick={() => act.ack(group.key, group.items.map((i) => i.entry.id))}>
            {n > 1 ? `Acknowledge ${n}` : "Acknowledge"}
          </button>
        )}
      </span>
    </li>
  );
}

// evaluateInFull starts the full evaluation of the posting a quick screen's
// review is about; the review resolves when that evaluation ends.
export const evaluateInFull = (v: EvaluationView) =>
  request("POST", "/api/evaluations", runAccepted, { body: { url: v.postingUrl, inboxRowId: v.inboxRowId, mode: "deep" }, headers: { "Idempotency-Key": newRequestId() } });

// skipReview dismisses the posting's inbox row (one already dismissed elsewhere
// is skipped already) and acknowledges each run of the review.
export async function skipReview(v: EvaluationView, ids: string[]) {
  if (v.inboxRowId) await post(`/api/inbox/${v.inboxRowId}/dismiss`, inboxRowDecoder).catch((e) => (asApiError(e).kind === "conflict" ? null : Promise.reject(e)));
  await Promise.all(ids.map((id) => post(`/api/runs/${id}/ack`, boardEntry)));
}

export function Inbox({ items, onChange }: { items: NeedsYouItem[]; onChange: () => void }) {
  const [failure, setFailure] = useState<ApiError | null>(null);
  // The row whose write is in flight; its buttons wait for the answer.
  const [pending, setPending] = useState<string | null>(null);
  // Reviews sent to an evaluation in depth, by row key: they stay listed until
  // that evaluation ends, but are no longer the user's to decide.
  const [started, setStarted] = useState<Record<string, string>>({});
  const groups = groupWaiting(items);
  const { shown, hidden, more } = useCapped(groups, INBOX_CAP);
  // The review whose report is open in the drawer, read without leaving the list.
  const [reading, setReading] = useState<{ key: string; v: EvaluationView } | null>(null);
  const list = useRef<HTMLOListElement>(null);
  // after is the position of the row just decided: once the list has settled,
  // focus goes to the next row still undecided, never back to the page's top.
  const after = useRef<number | null>(null);
  const undecided = (g: WaitingGroup) => !started[g.key];
  const reviews = groups.filter((g) => isReview(g.items[0].entry));
  const left = reviews.filter(undecided).length;
  const settled = groups.map((g) => g.key).join("|") + Object.keys(started).join("|");
  useEffect(() => {
    if (after.current === null) return;
    const rows = [...(list.current?.querySelectorAll<HTMLElement>(":scope > li") ?? [])];
    const open = rows.map((r, i) => ({ r, g: shown[i] })).filter((x) => x.g && undecided(x.g));
    const next = open.find((x) => shown.indexOf(x.g) >= after.current!) ?? open[open.length - 1];
    after.current = null;
    (next?.r.querySelector<HTMLElement>(".actions .primary:not(:disabled)") ?? next?.r.querySelector<HTMLElement>(".actions button:not(:disabled), .actions a"))?.focus();
  }, [settled]);
  async function write(key: string, work: () => Promise<unknown>, reload = true) {
    setFailure(null);
    setPending(key);
    try {
      await work();
      if (reload) onChange();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setPending(null);
    }
  }
  const decided = (key: string) => {
    after.current = shown.findIndex((g) => g.key === key);
  };
  const act: RowActs = {
    // Every run of the row is acknowledged; a refusal is reported and the
    // others still go through.
    ack: (key, ids) =>
      void write(key, async () => {
        const results = await Promise.allSettled(ids.map((id) => post(`/api/runs/${id}/ack`, boardEntry)));
        const refused = results.find((r): r is PromiseRejectedResult => r.status === "rejected");
        if (refused) {
          onChange();
          throw refused.reason;
        }
      }),
    done: (key, id) => void write(key, () => post(`/api/runs/${id}/done`, boardEntry)),
    show: (key, id) => void write(key, () => showTab(id), false),
    read: (key, v) => setReading({ key, v }),
    // Evaluate in full leaves the review listed until that evaluation ends.
    full: (key, v) => {
      void write(
        key,
        async () => {
          const run = await evaluateInFull(v);
          decided(key);
          setStarted((was) => ({ ...was, [key]: run.runId }));
        },
        false,
      );
    },
    // Skip skips every run of the row.
    skip: (key, ids, v) => {
      void write(key, async () => {
        await skipReview(v, ids);
        decided(key);
      });
    },
    // The item stays until a scan of the source gets past the sign-in page.
    scan: (key, source) => void write(key, () => request("POST", "/api/scans", runAccepted, { body: { source }, headers: { "Idempotency-Key": newRequestId() } })),
  };
  // A review already sent to an evaluation in depth is no longer decided here.
  const reader = reading && groups.find((g) => g.key === reading.key && !started[g.key]);
  return (
    <section aria-labelledby="runs-inbox" className="panel">
      <header className="panel-head">
        <h2 id="runs-inbox">Needs you</h2>
        <Chip>{items.length}</Chip>
      </header>
      {failure && <ErrorNote error={failure} />}
      {items.length === 0 ? (
        <Empty>Nothing waits for you.</Empty>
      ) : (
        <>
          {reviews.length > 0 && (
            <p role="status" className="count-line">
              {left} {left === 1 ? "review" : "reviews"} left to decide
              {left < reviews.length && `; ${reviews.length - left} in full evaluation, listed until it ends`}
            </p>
          )}
          <ol ref={list} className="plain item-list">
            {shown.map((g) => (
              <Row key={g.key} group={g} pending={pending} act={act} started={started[g.key]} />
            ))}
          </ol>
        </>
      )}
      <ShowMore hidden={hidden} onMore={more} />
      {reading?.v.reportNumber !== undefined && (
        <ReportDrawer
          number={reading.v.reportNumber}
          onClose={() => setReading(null)}
          actions={
            reader && (
              <>
                <button type="button" className="primary" disabled={!reading.v.postingUrl} onClick={() => (setReading(null), act.full(reader.key, reading.v))}>
                  Evaluate in full
                </button>
                <button type="button" onClick={() => (setReading(null), act.skip(reader.key, reader.items.map((i) => i.entry.id), reading.v))}>
                  Skip
                </button>
              </>
            )
          }
        />
      )}
    </section>
  );
}
