import { Bot } from "lucide-react";
import { useEffect, useRef } from "react";
import { get, post } from "../../api/client";
import { agentIdeaStarted, agentTask, agentTaskList } from "../../api/decoders_agent";
import { useInterval, useResource } from "../../hooks";
import { navigate } from "../../router";
import { Button } from "../../ui/Button";
import { Chip, ErrorNote, type Tone } from "../../ui/parts";
import type { AgentTask } from "../../wire";
import { useAction } from "../action";

// TASK_POLL_MS is how often an open task is read again: it takes turns of its
// own, which no event of this page announces.
const TASK_POLL_MS = 5000;

const STATE: Record<string, { label: string; tone: Tone }> = {
  working: { label: "Working", tone: "info" },
  waiting_runs: { label: "Waiting for its runs", tone: "info" },
  waiting_you: { label: "Waiting for you", tone: "warn" },
  paused: { label: "Paused", tone: "neutral" },
  done: { label: "Done", tone: "good" },
  stopped: { label: "Stopped", tone: "neutral" },
};

const STEP_TONE: Record<string, Tone> = { done: "good", doing: "info", skipped: "neutral" };

export const open = (t: AgentTask) => t.state !== "done" && t.state !== "stopped";

const ask = (sessionId: string) => `/ask?conversation=${encodeURIComponent(sessionId)}`;

// LetAgentDo hands a Needs you item over to the Ask Agent as a delegated task
// (the server writes the request from what waits now) and opens its
// conversation; while a task works on the item, it opens that one instead.
export function LetAgentDo({ idea, task }: { idea: string; task?: AgentTask }) {
  const act = useAction();
  if (task) {
    return (
      <div className="takeover compact">
        <Button size="sm" icon={Bot} onClick={() => navigate(ask(task.sessionId))}>
          The agent is on it
        </Button>
      </div>
    );
  }
  const start = async () => {
    const out = await act.run(() => post(`/api/agent/ideas/${encodeURIComponent(idea)}`, agentIdeaStarted));
    if (out) navigate(ask(out.sessionId));
  };
  return (
    <div className="takeover compact">
      <Button size="sm" icon={Bot} disabled={act.busy} onClick={() => void start()}>
        {act.busy ? "Starting the agent…" : "Let the agent do it"}
      </Button>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// TaskPanel shows the delegated task of the open conversation above its
// transcript: the plan the agent keeps, where the task stands, and the user's
// controls. A task goes on by itself, so the panel reads it again while it is
// open and tells the conversation to read its turns again when it moved.
export function TaskPanel({ sessionId, onMoved }: { sessionId: string; onMoved: () => void }) {
  const [list, reload] = useResource(() => get("/api/agent/tasks", agentTaskList), [], ["runs"]);
  const task = list.data?.tasks.find((t) => t.sessionId === sessionId);
  useInterval(reload, TASK_POLL_MS, task !== undefined && open(task));
  const seen = useRef<string | null>(null);
  const mark = task ? `${task.id}|${task.updatedAt}` : null;
  useEffect(() => {
    if (seen.current !== null && mark !== seen.current) onMoved();
    seen.current = mark;
  }, [mark]);
  const act = useAction();
  if (!task) return null;
  const control = async (verb: "pause" | "resume" | "stop") => {
    if (await act.run(() => post(`/api/agent/tasks/${encodeURIComponent(task.id)}/${verb}`, agentTask))) reload();
  };
  const state = STATE[task.state] ?? { label: task.state, tone: "neutral" as Tone };
  const done = task.steps.filter((s) => s.status === "done" || s.status === "skipped").length;
  return (
    <section className="note" aria-label={`Task: ${task.title}`}>
      <p>
        <strong>Task: {task.title}</strong> <Chip tone={state.tone}>{state.label}</Chip>{" "}
        <span className="muted">
          {done} of {task.steps.length} steps · {task.turns} of {task.maxTurns} turns on its own
        </span>
      </p>
      {task.steps.length > 0 && (
        <ol>
          {task.steps.map((s, i) => (
            <li key={i}>
              {s.text} <Chip tone={STEP_TONE[s.status] ?? "neutral"}>{s.status}</Chip>
              {s.note && <span className="muted"> {s.note}</span>}
            </li>
          ))}
        </ol>
      )}
      {task.question && (
        <p>
          <strong>It asks:</strong> {task.question} <span className="muted">Answer below, or resume it to let it go on.</span>
        </p>
      )}
      {task.outcome && <p>{task.outcome}</p>}
      {open(task) && (
        <div className="actions">
          {(task.state === "paused" || task.state === "waiting_you") && (
            <button type="button" disabled={act.busy} onClick={() => void control("resume")}>
              Resume
            </button>
          )}
          {task.state !== "paused" && (
            <button type="button" disabled={act.busy} onClick={() => void control("pause")}>
              Pause
            </button>
          )}
          <button type="button" disabled={act.busy} onClick={() => void control("stop")}>
            Stop task
          </button>
        </div>
      )}
      {act.error && <ErrorNote error={act.error} />}
    </section>
  );
}
