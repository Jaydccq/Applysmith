import { useEffect, useRef, useState } from "react";
import { ApiError, get, newRequestId, post, request } from "../../api/client";
import { evaluationView } from "../../api/decoders_discovery";
import { boardEntry, runAccepted, runView } from "../../api/decoders_runs";
import { asApiError, useInterval, useResource } from "../../hooks";
import { Link, navigate } from "../../router";
import { Chip, ErrorNote, Loading, formatDateTime, humanize, safeHref } from "../../ui/parts";
import { ConfirmDialog, Modal } from "../../ui/Modal";
import { ApplyRecord } from "./ApplyRecord";
import { evaluateInFull, skipReview } from "./Inbox";
import { LetAgentFinish } from "./Takeover";
import type { BoardEntry } from "../../wire";
import { durationLabel, fixOf, nextStep, plainDetail, stageDoing, stateLabel, summaryLine, tone } from "./format";

const POLL_MS = 3000;

const ORIGINS: Record<string, string> = { user: "You", schedule: "A schedule", agent: "Ask Agent" };

// How a run record without a board entry stands, by its status.
const RUN_WORDS: Record<string, string> = {
  queued: "waits to start",
  running: "is still running",
  cancelling: "is stopping",
  succeeded: "succeeded",
  failed: "failed",
  cancelled: "was cancelled",
  review_required: "waits for your review",
};

// Where the fix of a stop is made, when it is not in the kept tab.
const SETTINGS_SIGNIN = { to: "/settings#settings-signin", label: "Open the sign-in credentials" };
const FIX: Record<string, { to: string; label: string }> = {
  answer: { to: "/answer-bank?show=needs", label: "Open the Answer Bank" },
  "credential-refused": SETTINGS_SIGNIN,
  lockout: SETTINGS_SIGNIN,
  "lane-unavailable": { to: "/browser", label: "Open the Browser page" },
};

function Facts({ e, conversation, now }: { e: BoardEntry; conversation?: string; now: number }) {
  const waiting = e.state === "needs-you" || e.state === "blocked";
  return (
    <dl className="facts">
      <dt>Kind</dt>
      <dd>{humanize(e.kind)}</dd>
      <dt>Started by</dt>
      <dd>{ORIGINS[e.origin] ?? humanize(e.origin)}</dd>
      {/* A run the Ask Agent started opens the conversation that asked for it. */}
      {conversation && (
        <>
          <dt>Asked in</dt>
          <dd>
            <Link to={`/ask?conversation=${encodeURIComponent(conversation)}`}>Open the conversation</Link>
          </dd>
        </>
      )}
      {/* A scan or an inbox pass names its evaluations' batch "scan:" and its own run id. */}
      {e.batchId?.startsWith("scan:") && (
        <>
          <dt>Queued by</dt>
          <dd>
            <Link to={`/runs/${e.batchId.slice(5)}${window.location.search}`}>Open the run that queued it</Link>
          </dd>
        </>
      )}
      <dt>Status</dt>
      <dd>
        <Chip tone={tone(e)}>{stateLabel(e)}</Chip>
      </dd>
      <dt>Started</dt>
      <dd>
        <time dateTime={e.startedAt}>{formatDateTime(e.startedAt)}</time>
      </dd>
      {e.finishedAt ? (
        <>
          <dt>Finished</dt>
          <dd>
            <time dateTime={e.finishedAt}>{formatDateTime(e.finishedAt)}</time> <span className="muted">(took {durationLabel(e.startedAt, e.finishedAt)})</span>
          </dd>
        </>
      ) : waiting ? (
        <>
          <dt>Stopped</dt>
          <dd>
            <time dateTime={e.stateAt}>{formatDateTime(e.stateAt)}</time> <span className="muted">(worked {durationLabel(e.startedAt, e.stateAt)})</span>
          </dd>
          <dt>{e.state === "blocked" ? "Blocked for" : "Waiting for"}</dt>
          <dd>{durationLabel(e.stateAt, undefined, now)}</dd>
        </>
      ) : (
        <>
          <dt>Running for</dt>
          <dd>{durationLabel(e.startedAt, undefined, now)}</dd>
        </>
      )}
    </dl>
  );
}

// Why says where the run stands in words: the questions or answers it stopped
// on as a list, else one sentence, then what the user does next. The machine
// text stays behind a disclosure for debugging.
function Why({ e, failure }: { e: BoardEntry; failure?: string }) {
  const waiting = e.state === "needs-you" || e.state === "blocked";
  const p = plainDetail(e.detail || failure, waiting ? e.reason : undefined, e.kind);
  if (!p) return null;
  const next = waiting ? nextStep(e.reason, e.kind) : "";
  // A fix the server names (a setting only the user changes, a sign-in to
  // sync) is the next step itself.
  const own = waiting ? fixOf(e) : undefined;
  const fix = waiting && e.reason ? FIX[e.reason] : undefined;
  const raw = [p.raw, failure && failure !== e.detail ? failure : ""].filter(Boolean).join("\n\n");
  return (
    <section aria-labelledby="run-why" className="run-why">
      {p.groups.length > 0 ? (
        p.groups.map((g, i) => (
          <div key={g.heading}>
            <h3 id={i === 0 ? "run-why" : undefined}>{g.heading}</h3>
            <ul className="run-questions">
              {g.items.map((q) => (
                <li key={q}>{q}</li>
              ))}
            </ul>
          </div>
        ))
      ) : (
        <h3 id="run-why">{p.line}</h3>
      )}
      {own ? (
        <p className="muted">
          <Link to={own.to}>{own.label}</Link>
          {own.linked ? "." : `, then ${own.scan ? "run the scan again" : "retry"}.`}
        </p>
      ) : (
        next && (
          <p className="muted">
            {next} {fix && <Link to={fix.to}>{fix.label}</Link>}
          </p>
        )
      )}
      {raw && (
        <details className="raw-detail">
          <summary>Technical detail</summary>
          <pre className="pre">{raw}</pre>
        </details>
      )}
    </section>
  );
}

// ReviewActions answer a quick screen's review: the report, Evaluate in full (a
// full evaluation of the posting; the review resolves when it ends) and Skip
// (the posting's inbox row is dismissed and the review acknowledged).
function ReviewActions({ id, onChange }: { id: string; onChange: () => void }) {
  const [res] = useResource(() => get(`/api/evaluations/${id}`, evaluationView), [id], ["runs"]);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [started, setStarted] = useState("");
  const v = res.data;
  if (!v) return null;
  async function act(f: () => Promise<void>) {
    setFailure(null);
    setBusy(true);
    try {
      await f();
      onChange();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  }
  const full = () => act(async () => setStarted((await evaluateInFull(v)).runId));
  const skip = () => act(() => skipReview(v, [id]));
  return (
    <section aria-labelledby="run-review" className="run-why">
      <h3 id="run-review">Evaluate in depth or skip</h3>
      {v.reviewReason && <p>{v.reviewReason}</p>}
      {failure && <ErrorNote error={failure} />}
      {started ? (
        <p className="note ok">
          The full evaluation started; this review closes when it ends. <Link to={`/runs/${started}`}>Follow it</Link>
        </p>
      ) : (
        <div className="actions">
          {v.reportNumber !== undefined && <Link to={`/reports?id=${v.reportNumber}`}>Open report {v.reportNumber}</Link>}
          <button type="button" className="primary" disabled={busy || !v.postingUrl} onClick={() => void full()}>
            Evaluate in full
          </button>
          <button type="button" disabled={busy} onClick={() => void skip()}>
            Skip
          </button>
        </div>
      )}
    </section>
  );
}

// EvaluationLinks lead a finished evaluation to the report it wrote and the
// posting it read (the detail above says its score). A failed or cancelled one
// starts again with the request it was started with, mode and pasted text
// included, and the drawer follows the new run.
function EvaluationLinks({ id, again, onChange }: { id: string; again: boolean; onChange: () => void }) {
  const [res] = useResource(() => get(`/api/evaluations/${id}`, evaluationView), [id], ["runs"]);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const v = res.data;
  const posting = v?.postingUrl && safeHref(v.postingUrl);
  if (!v || (v.reportNumber === undefined && !posting && !again)) return null;
  async function retry() {
    setFailure(null);
    setBusy(true);
    try {
      const acc = await request("POST", `/api/evaluations/${id}/retry`, runAccepted, { headers: { "Idempotency-Key": newRequestId() } });
      onChange();
      navigate(`/runs/${acc.runId}${window.location.search}`);
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      {failure && <ErrorNote error={failure} />}
      <div className="actions">
        {v.reportNumber !== undefined && <Link to={`/reports?id=${v.reportNumber}`}>Open report {v.reportNumber}</Link>}
        {posting && (
          <a href={posting} target="_blank" rel="noopener noreferrer">
            Open the posting
          </a>
        )}
        {again && (
          <button type="button" className="primary" disabled={busy} onClick={() => void retry()}>
            Evaluate again
          </button>
        )}
      </div>
    </>
  );
}

// RunDetail is the drawer of one run. Opening a run that waits for the user
// marks it seen. Cancel is confirmed in a dialog that names the run.
export function RunDetail({ id, onClose, onChange }: { id: string; onClose: () => void; onChange: () => void }) {
  const [res, retry] = useResource(() => get(`/api/runs/${id}`, runView), [id], ["runs"]);
  const [asking, setAsking] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const entry = res.data?.entry;
  const marked = useRef("");
  useInterval(retry, POLL_MS, entry?.state === "working");

  useEffect(() => {
    if (!entry || entry.seenAt || (entry.state !== "needs-you" && entry.state !== "blocked") || marked.current === entry.id) return;
    marked.current = entry.id;
    post(`/api/runs/${entry.id}/seen`, boardEntry).then(onChange, () => {});
  }, [entry, onChange]);

  async function cancel() {
    setFailure(null);
    setBusy(true);
    try {
      await post(`/api/runs/${id}/cancel`, runView);
      retry();
      onChange();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
      setAsking(false);
    }
  }

  async function done() {
    setFailure(null);
    setBusy(true);
    try {
      await post(`/api/runs/${id}/done`, boardEntry);
      onChange();
      retry();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  }

  async function acknowledge() {
    setFailure(null);
    setBusy(true);
    try {
      await post(`/api/runs/${id}/ack`, boardEntry);
      onChange();
      retry();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  }

  const title = entry?.title ?? "Run";
  return (
    <>
      <Modal title={title} variant="drawer" onClose={onClose}>
        {res.status === "loading" && <Loading />}
        {res.status === "error" && !res.data && asApiError(res.error).kind === "not_found" ? (
          // An old link, such as a notification's, can outlive its run: a
          // finished run leaves the board DoneRetention after it ends.
          <div className="note">
            <p>This run is no longer recorded here. A finished run leaves the board 14 days after it ends.</p>
            <div className="actions">
              <button type="button" onClick={onClose}>
                Back to the board
              </button>
            </div>
          </div>
        ) : (
          res.status === "error" && !res.data && <ErrorNote error={res.error} onRetry={retry} />
        )}
        {failure && <ErrorNote error={failure} />}
        {res.data && entry && (
          <>
            <p className="run-summary">{summaryLine(entry)}</p>
            {/* The summary ticks with the clock; a screen reader hears the state and stage once each time one changes. */}
            <p role="status" className="sr-only">
              {stateLabel(entry)}
              {entry.state === "working" && entry.stage ? `, ${stageDoing(entry.stage, true)}` : ""}
            </p>
            <Why e={entry} failure={res.data.failureReason} />
            <LetAgentFinish entry={entry} />
            <Facts e={entry} conversation={res.data.conversation} now={Date.now()} />
            {entry.kind === "evaluate" &&
              entry.state !== "working" &&
              (entry.state === "needs-you" && entry.reason === "review" && !entry.acknowledgedAt ? (
                <ReviewActions id={id} onChange={() => (onChange(), retry())} />
              ) : (
                <EvaluationLinks id={id} again={entry.state === "done" && entry.outcome !== "ok"} onChange={onChange} />
              ))}
            {entry.kind === "apply" && (
              <ApplyRecord id={id} working={entry.state === "working"} focusTab={entry.focusTab ?? ""} canSubmit={Boolean(entry.canSubmit)} onChange={onChange} />
            )}
          </>
        )}
        {res.data && !entry && (
          // A run record outlives its board entry, which a finished run leaves 14 days after it ends.
          <p>
            This run started {formatDateTime(res.data.createdAt)} and {RUN_WORDS[res.data.status] ?? "has ended"}. The board does not list it.
          </p>
        )}
        <div className="actions">
          {entry?.state === "working" && (
            <button type="button" className="danger-outline" disabled={busy} onClick={() => setAsking(true)}>
              Cancel run
            </button>
          )}
          {entry?.canDone && (
            <button type="button" disabled={busy} title="Press after you finished the step in the browser tab." onClick={() => void done()}>
              Done
            </button>
          )}
          {entry && !entry.acknowledgedAt && entry.state !== "working" && (
            <button type="button" disabled={busy} onClick={() => void acknowledge()}>
              Acknowledge
            </button>
          )}
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
      </Modal>
      {asking && (
        <ConfirmDialog
          title={`Cancel ${title}`}
          consequence="The run stops where it can. A Submit that was already pressed cannot be taken back, and the run still records how it ended."
          verb="Stop run"
          busy={busy}
          onConfirm={() => void cancel()}
          onCancel={() => setAsking(false)}
        />
      )}
    </>
  );
}
