import { Link } from "../../router";
import { Chip } from "../../ui/parts";
import { LaneMarkedApplied, type ApplyItem } from "../../wire";
import { STATE_ORDER, outcomeLine, ownerWords, stateWords } from "./words";

// RowStatus is the state of a row, why a list started now would leave it out,
// and, when a run has been tried, what the newest one did, who has the next
// step, and a link to that run's record.
// Nothing here is a form value. A row the user marked applied that no run
// touched, or whose run stopped before, reads as theirs.
export function RowStatus({ item }: { item: ApplyItem }) {
  const mine = item.lane === LaneMarkedApplied && item.state === "idle";
  const s = mine ? { label: "Applied by you", tone: "good" as const } : stateWords(item.state);
  const o = item.attempt?.outcome;
  // A run stopped on questions the user answers in its record.
  const ask = item.state === "needs-user" && o?.class === "answer-gap";
  return (
    <>
      <Chip tone={s.tone}>{s.label}</Chip>
      {item.leftOut && (
        <p className="muted clamp" title={item.leftOut}>
          Left out: {item.leftOut}
        </p>
      )}
      {o && (
        <p className="muted clamp" title={outcomeLine(o)}>
          {mine ? `Earlier run: ${outcomeLine(o)}` : outcomeLine(o)}
        </p>
      )}
      {!mine && o?.owner && ownerWords(o.owner) && <p className="muted">{ownerWords(o.owner)}</p>}
      {item.formWaiting && <p className="muted">The form is filled and waits at Submit in its kept tab.</p>}
      {item.runId && (
        <p className="sub">
          <Link to={`/runs/${encodeURIComponent(item.runId)}`} aria-label={`${ask ? "Answer the questions" : "Open the run"}: ${item.company}`}>
            {ask ? "Answer the questions" : "Open the run"}
          </Link>
        </p>
      )}
    </>
  );
}

// StateCounts is the page's rows by state, from the server's counts. A strip
// that would only say how many rows are not started says nothing the lane
// counts do not, so it is left out.
export function StateCounts({ states }: { states: Record<string, number> }) {
  const shown = STATE_ORDER.filter((k) => (states[k] ?? 0) > 0);
  const extra = Object.keys(states).filter((k) => states[k] > 0 && !(STATE_ORDER as readonly string[]).includes(k));
  if (shown.length + extra.length === 0 || (extra.length === 0 && shown.every((k) => k === "idle"))) return null;
  return (
    <p className="chips" aria-label="Rows on this page by state">
      {[...shown, ...extra].map((k) => (
        <Chip key={k} tone={stateWords(k).tone}>
          {states[k]} {stateWords(k).label.toLowerCase()}
        </Chip>
      ))}
    </p>
  );
}
