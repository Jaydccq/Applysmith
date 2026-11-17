import { useId, useState, type FormEvent } from "react";
import { del, get, patch, post } from "../../api/client";
import { goal as goalDecoder, goalDeleted, goalList } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import type { Goal, GoalCadence, GoalCreate, GoalPatch, GoalProposal, GoalStep } from "../../wire";
import { useAction, when } from "../action";

const MAX_TITLE = 120;
const MAX_STEP = 200;
const MAX_STEPS = 20;

const STEP_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { done: "good", in_progress: "warn", blocked: "bad" };

function cadenceText(c: GoalCadence): string {
  return c.days.length === 0 && c.time === "" ? "none" : `${c.days.join(", ")} at ${c.time}`;
}

function Steps({ steps }: { steps: GoalStep[] }) {
  if (steps.length === 0) return <p className="muted">No steps yet.</p>;
  return (
    <ol>
      {steps.map((s, i) => (
        <li key={i}>
          {s.text} <Chip tone={STEP_TONE[s.status ?? "pending"] ?? "neutral"}>{(s.status ?? "pending").replace("_", " ")}</Chip>
          {s.note && <span className="muted"> {s.note}</span>}
        </li>
      ))}
    </ol>
  );
}

function Proposal({ goal, proposal, onChanged }: { goal: Goal; proposal: GoalProposal; onChanged: () => void }) {
  const act = useAction();
  const base = `/api/agent/goals/${encodeURIComponent(goal.id)}/proposals/${encodeURIComponent(proposal.id)}`;
  const decide = async (verb: "accept" | "dismiss") => {
    if (await act.run(() => post(`${base}/${verb}`, goalDecoder))) onChanged();
  };
  return (
    <div className="note" role="group" aria-label={`Proposed change to ${goal.title}`}>
      <h4>The agent proposes a change</h4>
      <p>{proposal.reason}</p>
      <p className="muted">
        Replaces the steps that are not done
        {proposal.dueDate ? `, due ${proposal.dueDate}` : ""}
        {proposal.cadence ? `, check-ins ${cadenceText(proposal.cadence)}` : ""}.
      </p>
      <Steps steps={proposal.steps} />
      <div className="actions">
        <button type="button" disabled={act.busy} onClick={() => void decide("accept")}>
          Accept proposal
        </button>
        <button type="button" disabled={act.busy} onClick={() => void decide("dismiss")}>
          Dismiss proposal
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

const linesOf = (steps: GoalStep[]) => steps.map((s) => s.text).join("\n");

// stepsFrom turns the editor lines back into steps; a line that still matches
// an existing step keeps that step's status and note.
function stepsFrom(text: string, old: GoalStep[]): GoalStep[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "")
    .map((l) => old.find((s) => s.text === l) ?? { text: l });
}

interface GoalValues {
  title: string;
  dueDate: string;
  steps: string;
}

function GoalForm({ initial, label, onSubmit, onCancel }: { initial?: Goal; label: string; onSubmit: (v: GoalValues) => Promise<void>; onCancel?: () => void }) {
  const [title, setTitle] = useState(initial?.title ?? "");
  const [dueDate, setDueDate] = useState(initial?.dueDate ?? "");
  const [steps, setSteps] = useState(initial ? linesOf(initial.steps) : "");
  const ids = [useId(), useId(), useId()];
  const act = useAction();
  const lines = steps.split("\n").map((l) => l.trim()).filter((l) => l !== "");
  const tooLong = lines.some((l) => l.length > MAX_STEP);
  const valid = title.trim() !== "" && lines.length <= MAX_STEPS && !tooLong;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.run(async () => {
      await onSubmit({ title: title.trim(), dueDate, steps });
      return true;
    });
    if (ok && !initial) {
      setTitle("");
      setDueDate("");
      setSteps("");
    }
  };
  return (
    <form onSubmit={submit} className="form-grid">
      <div>
        <label htmlFor={ids[0]}>Goal</label>
        <input id={ids[0]} value={title} maxLength={MAX_TITLE} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <div>
        <label htmlFor={ids[1]}>Due date</label>
        <input id={ids[1]} type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} />
      </div>
      <div>
        <label htmlFor={ids[2]}>Steps, one per line</label>
        <textarea id={ids[2]} value={steps} onChange={(e) => setSteps(e.target.value)} />
        <span className="muted">
          {lines.length} of {MAX_STEPS} steps{tooLong ? `; a step is at most ${MAX_STEP} characters` : ""}
        </span>
      </div>
      <div className="actions">
        <button type="submit" disabled={act.busy || !valid}>
          {label}
        </button>
        {onCancel && (
          <button type="button" onClick={onCancel}>
            Cancel
          </button>
        )}
      </div>
      {act.error && <ErrorNote error={act.error} />}
    </form>
  );
}

function GoalCard({ goal, reload }: { goal: Goal; reload: () => void }) {
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const act = useAction();
  const base = `/api/agent/goals/${encodeURIComponent(goal.id)}`;

  const setState = async (state: string) => {
    const body: GoalPatch = { state };
    if (await act.run(() => patch(base, goalDecoder, body))) reload();
  };
  const remove = async () => {
    setRemoving(false);
    if (await act.run(() => del(base, goalDeleted))) reload();
  };

  return (
    <li className="panel">
      <header className="row">
        <h3>{goal.title}</h3>
        <Chip tone={goal.state === "active" ? "good" : "neutral"}>{goal.state}</Chip>
      </header>
      <p className="muted">
        {goal.dueDate ? `Due ${goal.dueDate}. ` : ""}
        Check-ins: {cadenceText(goal.cadence)}.{goal.nextCheckin ? ` Next ${when(goal.nextCheckin)}.` : ""}
      </p>
      {editing ? (
        <GoalForm
          initial={goal}
          label="Save goal"
          onCancel={() => setEditing(false)}
          onSubmit={async (v) => {
            const body: GoalPatch = { title: v.title, dueDate: v.dueDate, steps: stepsFrom(v.steps, goal.steps) };
            await patch(base, goalDecoder, body);
            setEditing(false);
            reload();
          }}
        />
      ) : (
        <Steps steps={goal.steps} />
      )}
      {goal.proposal && <Proposal goal={goal} proposal={goal.proposal} onChanged={reload} />}
      <div className="actions">
        {!editing && (
          <button type="button" onClick={() => setEditing(true)} aria-label={`Edit ${goal.title}`}>
            Edit
          </button>
        )}
        {goal.state === "active" && (
          <button type="button" disabled={act.busy} onClick={() => void setState("paused")} aria-label={`Pause ${goal.title}`}>
            Pause
          </button>
        )}
        {goal.state === "paused" && (
          <button type="button" disabled={act.busy} onClick={() => void setState("active")} aria-label={`Resume ${goal.title}`}>
            Resume
          </button>
        )}
        {(goal.state === "active" || goal.state === "paused") && (
          <button type="button" disabled={act.busy} onClick={() => void setState("completed")} aria-label={`Complete ${goal.title}`}>
            Complete
          </button>
        )}
        <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRemoving(true)} aria-label={`Delete ${goal.title}`}>
          Delete
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {removing && (
        <ConfirmDialog title={`Delete the goal "${goal.title}"`} consequence="The goal, its steps and any pending proposal are removed." verb="Delete goal" onCancel={() => setRemoving(false)} onConfirm={() => void remove()} />
      )}
    </li>
  );
}

// GoalsPanel is the user's own goals. The agent can only propose a change to
// one; a proposal takes effect when it is accepted here.
export function GoalsPanel() {
  const [res, reload] = useResource(() => get("/api/agent/goals", goalList), []);
  return (
    <Panel title="Goals" id="ask-goals" res={res} retry={reload}>
      {(data) => (
        <>
          {data.goals.length === 0 ? (
            <Empty>No goal yet. Add one below; up to 5 can be active.</Empty>
          ) : (
            <ul className="plain stack">
              {data.goals.map((g) => (
                <GoalCard key={g.id} goal={g} reload={reload} />
              ))}
            </ul>
          )}
          <h3>New goal</h3>
          <GoalForm
            label="Add goal"
            onSubmit={async (v) => {
              const body: GoalCreate = { title: v.title, ...(v.dueDate ? { dueDate: v.dueDate } : {}), steps: stepsFrom(v.steps, []) };
              await post("/api/agent/goals", goalDecoder, body);
              reload();
            }}
          />
        </>
      )}
    </Panel>
  );
}
