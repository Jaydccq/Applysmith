import { useState } from "react";
import { get, post } from "../../api/client";
import { scheduleProposal, scheduleProposalDecided, scheduleProposalList } from "../../api/decoders_discovery";
import { useResource } from "../../hooks";
import { Button } from "../../ui/Button";
import { ErrorNote, Panel, UndoNote } from "../../ui/parts";
import type { ScheduleProposal } from "../../wire";
import { useAction, when } from "../action";
import { describePreset } from "./schedule";

function change(p: ScheduleProposal): string {
  const parts: string[] = [];
  if (p.change.enabled === true) parts.push("turn it on");
  if (p.change.enabled === false) parts.push("pause it");
  if (p.change.preset) {
    const d = describePreset(p.change.preset);
    parts.push(`run it ${d.startsWith("Every") ? d[0].toLowerCase() + d.slice(1) : d}`);
  }
  return parts.join(" and ");
}

// Proposals lists the schedule changes the Ask Agent suggested. A proposal
// changes nothing until it is accepted here. With nothing waiting the section
// is left out, so the schedules lead the tab; once the last one is decided it
// stays to say what happened, with an Undo after a dismissal.
export function Proposals({ onAccepted }: { onAccepted: () => void }) {
  const [res, retry] = useResource(() => get("/api/agent/schedule-proposals", scheduleProposalList), [], ["schedules"]);
  const [decided, setDecided] = useState("");
  const [dismissed, setDismissed] = useState<{ p: ScheduleProposal; n: number }>();
  const act = useAction();
  const name = (p: ScheduleProposal) => p.scheduleName || `schedule ${p.scheduleId}`;
  const decide = async (p: ScheduleProposal, verb: "accept" | "dismiss") => {
    const done = await act.run(() => post(`/api/agent/schedule-proposals/${encodeURIComponent(p.id)}/${verb}`, scheduleProposalDecided));
    if (done) setDecided(verb === "accept" ? `Accepted the change to ${name(p)}.` : "");
    if (done && verb === "dismiss") setDismissed((d) => ({ p, n: (d?.n ?? 0) + 1 }));
    retry();
    if (done?.accepted) onAccepted();
  };
  const restore = async (p: ScheduleProposal) => {
    const done = await act.run(() => post(`/api/agent/schedule-proposals/${encodeURIComponent(p.id)}/restore`, scheduleProposal));
    retry();
    return !!done;
  };
  if (res.status === "loading" || (res.status === "ready" && res.data.proposals.length === 0 && !decided && !dismissed)) return null;
  return (
    <Panel title="Suggested changes" id="schedule-proposals" res={res} retry={retry}>
      {(data) => (
        <>
          <div aria-live="polite">{decided && <p className="note ok">{decided}</p>}</div>
          <div role="status">{dismissed && <UndoNote key={dismissed.n} done={`Dismissed the change to ${name(dismissed.p)}.`} undone={`The change to ${name(dismissed.p)} waits for you again.`} onUndo={() => restore(dismissed.p)} />}</div>
          {act.error && <ErrorNote error={act.error} />}
          {data.proposals.length === 0 ? (
            <p className="empty">The Ask Agent has no other schedule change waiting for you.</p>
          ) : (
            <ul className="plain stack">
              {data.proposals.map((p) => (
                <li key={p.id}>
                  <p>
                    <strong>{p.scheduleName || `Schedule ${p.scheduleId}`}</strong>: {change(p)}
                  </p>
                  <p className="muted">
                    {p.reason} Suggested {when(p.createdAt)}.
                  </p>
                  <div className="actions">
                    <Button variant="primary" size="sm" disabled={act.busy} onClick={() => void decide(p, "accept")} aria-label={`Accept the change to ${p.scheduleName || p.scheduleId}`}>
                      Accept
                    </Button>
                    <Button size="sm" disabled={act.busy} onClick={() => void decide(p, "dismiss")} aria-label={`Dismiss the change to ${p.scheduleName || p.scheduleId}`}>
                      Dismiss
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Panel>
  );
}
