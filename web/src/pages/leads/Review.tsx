import { useState } from "react";
import { get, post } from "../../api/client";
import { signalReviewItem, signalReviewList } from "../../api/decoders_signals";
import { useResource } from "../../hooks";
import { Chip, Empty, ErrorNote, Panel, UndoNote, humanize } from "../../ui/parts";
import type { SignalAssessment, SignalReviewItem } from "../../wire";
import { useAction, when } from "../action";

const REASONS: Record<string, string> = {
  mid_confidence: "The model was not sure enough.",
  quote_failed: "A quote could not be found in the source.",
  contradictory: "Two sources disagree.",
  schema_invalid: "The model's answer was malformed.",
};

export function Quotes({ a }: { a: SignalAssessment }) {
  return (
    <ul className="plain" aria-label="Quotes">
      {a.quotes.map((q, i) => (
        <li key={i}>
          <blockquote className="pre quote">{q.quote}</blockquote>
          <Chip tone={q.verified ? "good" : "bad"}>{q.verified ? "found in the source" : "not verified"}</Chip>
        </li>
      ))}
    </ul>
  );
}

export function AssessmentFacts({ a }: { a: SignalAssessment }) {
  return (
    <dl className="facts">
      <dt>Event</dt>
      <dd>{humanize(a.eventType)}</dd>
      <dt>Hiring intent</dt>
      <dd>{humanize(a.hiringIntent)}</dd>
      {a.affectedTeams.length > 0 && (
        <>
          <dt>Teams</dt>
          <dd>{a.affectedTeams.join(", ")}</dd>
        </>
      )}
      <dt>Scale</dt>
      <dd>{humanize(a.scale)}</dd>
      <dt>Strength</dt>
      <dd>{a.strength} of 10</dd>
      <dt>Confidence</dt>
      <dd>{a.confidence.toFixed(2)}</dd>
      <dt>Reasoning</dt>
      <dd>{a.reasoning}</dd>
    </dl>
  );
}

function Item({ it, onDecided }: { it: SignalReviewItem; onDecided: (verb: "approve" | "reject") => void }) {
  const act = useAction();
  const decide = async (verb: "approve" | "reject") => {
    const done = await act.run(() => post(`/api/signals/review/${it.id}/${verb}`, signalReviewItem));
    if (done) onDecided(verb);
  };
  return (
    <li className="panel">
      <h3>{it.company}</h3>
      <p>
        <Chip tone="warn">{it.reason}</Chip> {REASONS[it.reason] ?? ""} <span className="muted">Held {when(it.createdAt)}</span>
      </p>
      <AssessmentFacts a={it.assessment} />
      <Quotes a={it.assessment} />
      {act.error && <ErrorNote error={act.error} />}
      <div className="actions">
        <button type="button" disabled={act.busy} onClick={() => void decide("approve")} aria-label={`Approve the assessment for ${it.company}`}>
          Approve
        </button>
        <button type="button" disabled={act.busy} onClick={() => void decide("reject")} aria-label={`Reject the assessment for ${it.company}`}>
          Reject
        </button>
      </div>
    </li>
  );
}

// ReviewTab lists the assessments the system held back. Approving counts one
// toward the company's tier; rejecting leaves it out, and its Undo puts the
// item back in the queue.
export function ReviewTab() {
  const [res, retry] = useResource(() => get("/api/signals/review", signalReviewList), [], ["scans", "runs"]);
  const [rejected, setRejected] = useState<{ it: SignalReviewItem; n: number }>();
  // approved says what an approval did; it has no undo, so the item's leaving
  // is not the only sign it was taken.
  const [approved, setApproved] = useState("");
  const undo = useAction();
  const restore = async (it: SignalReviewItem) => {
    const done = await undo.run(() => post(`/api/signals/review/${it.id}/restore`, signalReviewItem));
    retry();
    return !!done;
  };
  return (
    <Panel title="Review queue" id="signals-review" res={res} retry={retry}>
      {(data) => (
        <>
          <div role="status">
            {approved && <p className="note ok">{approved}</p>}
            {rejected && (
              <UndoNote key={rejected.n} done={`Rejected the assessment for ${rejected.it.company}.`} undone={`The assessment for ${rejected.it.company} waits for review again.`} onUndo={() => restore(rejected.it)} />
            )}
          </div>
          {undo.error && <ErrorNote error={undo.error} />}
          {data.items.length === 0 ? (
            <Empty>Nothing is waiting for review.</Empty>
          ) : (
            <ul className="stack plain">
              {data.items.map((it) => (
                <Item
                  key={it.id}
                  it={it}
                  onDecided={(verb) => {
                    setApproved(verb === "approve" ? `Approved the assessment for ${it.company}. It now counts toward the company's tier.` : "");
                    if (verb === "reject") setRejected((r) => ({ it, n: (r?.n ?? 0) + 1 }));
                    else setRejected(undefined);
                    retry();
                  }}
                />
              ))}
            </ul>
          )}
        </>
      )}
    </Panel>
  );
}
