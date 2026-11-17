import { useId, useState, type FormEvent } from "react";
import { del, get, patch, post } from "../../api/client";
import { agentPrompt, agentPromptDeleted, agentPromptList } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { ConfirmDialog } from "../../ui/Modal";
import { CopyButton } from "../../ui/CopyButton";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import type { AgentPrompt, AgentPromptCreate, AgentPromptPatch, GoalCadence, PromptFiring, PromptTriggerSpec } from "../../wire";
import { useAction, when } from "../action";

const MAX_TEXT = 2000;
const DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;
const SIGNALS: Record<string, string> = {
  interview_scheduled: "Interview invitation",
  recruiter_reply_needed: "Recruiter reply needed",
  rejection: "Rejection",
  online_assessment: "Assessment",
};

const STATE_TONE: Record<string, "good" | "warn" | "neutral"> = { active: "good", proposed: "warn" };
const RESULT_TONE: Record<string, "good" | "bad" | "neutral"> = { answered: "good", failed: "bad", timed_out: "bad" };

const cadenceText = (c: GoalCadence) => `${c.days.join(", ")} at ${c.time}`;
const signalText = (t: PromptTriggerSpec) => `${t.signals.map((s) => SIGNALS[s] ?? s).join(", ")}${t.company ? ` from ${t.company}` : ""}`;

interface FormValues {
  text: string;
  cadence?: GoalCadence;
  trigger?: PromptTriggerSpec;
}

// PromptForm creates a prompt or edits one. The kind of an existing prompt is
// fixed: a cadence prompt stays on a cadence and a trigger on its signals.
function PromptForm({ initial, label, onSubmit, onCancel }: { initial?: AgentPrompt; label: string; onSubmit: (v: FormValues) => Promise<void>; onCancel?: () => void }) {
  const [kind, setKind] = useState(initial?.kind ?? "cadence");
  const [text, setText] = useState(initial?.text ?? "");
  const [days, setDays] = useState<string[]>(initial?.cadence?.days ?? []);
  const [time, setTime] = useState(initial?.cadence?.time ?? "");
  const [signals, setSignals] = useState<string[]>(initial?.trigger?.signals ?? []);
  const [company, setCompany] = useState(initial?.trigger?.company ?? "");
  const ids = [useId(), useId(), useId()];
  const act = useAction();
  const toggle = (list: string[], set: (v: string[]) => void, item: string) => set(list.includes(item) ? list.filter((x) => x !== item) : [...list, item]);
  const valid = text.trim() !== "" && (kind === "cadence" ? days.length > 0 && time !== "" : signals.length > 0);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const values: FormValues = kind === "cadence" ? { text: text.trim(), cadence: { days: DAYS.filter((d) => days.includes(d)), time } } : { text: text.trim(), trigger: { signals, ...(company.trim() ? { company: company.trim() } : {}) } };
    const ok = await act.run(async () => {
      await onSubmit(values);
      return true;
    });
    if (ok && !initial) {
      setText("");
      setDays([]);
      setTime("");
      setSignals([]);
      setCompany("");
    }
  };

  return (
    <form onSubmit={submit} className="form-grid">
      {!initial && (
        <fieldset>
          <legend>Runs</legend>
          <label>
            <input type="radio" name={ids[0]} checked={kind === "cadence"} onChange={() => setKind("cadence")} /> On a schedule
          </label>
          <label>
            <input type="radio" name={ids[0]} checked={kind === "trigger"} onChange={() => setKind("trigger")} /> When a Gmail signal arrives
          </label>
        </fieldset>
      )}
      <div>
        <label htmlFor={ids[1]}>Prompt</label>
        <textarea id={ids[1]} value={text} maxLength={MAX_TEXT} onChange={(e) => setText(e.target.value)} />
        <span className="muted">
          {text.length} of {MAX_TEXT}. A scheduled turn can only read and draft; it cannot apply, submit or write data.
        </span>
      </div>
      {kind === "cadence" ? (
        <>
          <fieldset>
            <legend>Days</legend>
            <div className="chips">
              {DAYS.map((d) => (
                <label key={d} className="check">
                  <input type="checkbox" checked={days.includes(d)} onChange={() => toggle(days, setDays, d)} /> {d}
                </label>
              ))}
            </div>
          </fieldset>
          <div>
            <label htmlFor={ids[2]}>Time</label>
            <input id={ids[2]} type="time" value={time} onChange={(e) => setTime(e.target.value)} />
          </div>
        </>
      ) : (
        <>
          <fieldset>
            <legend>Signals</legend>
            <div className="chips">
              {Object.entries(SIGNALS).map(([id, name]) => (
                <label key={id} className="check">
                  <input type="checkbox" checked={signals.includes(id)} onChange={() => toggle(signals, setSignals, id)} /> {name}
                </label>
              ))}
            </div>
          </fieldset>
          <div>
            <label htmlFor={ids[2]}>Only for this company (optional)</label>
            <input id={ids[2]} value={company} maxLength={120} onChange={(e) => setCompany(e.target.value)} />
          </div>
        </>
      )}
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

function Firing({ f }: { f: PromptFiring }) {
  return (
    <li>
      <p>
        {SIGNALS[f.signalType] ?? f.signalType}
        {f.employer ? ` from ${f.employer}` : ""}, {when(f.at)} <Chip tone={RESULT_TONE[f.state] ?? "neutral"}>{f.state.replace("_", " ")}</Chip>
        {f.sessionId && (
          <>
            {" "}
            <Link to={`/ask?conversation=${encodeURIComponent(f.sessionId)}`}>Open the conversation</Link>
          </>
        )}
      </p>
      {f.detail && <p className="muted">{f.detail}</p>}
      {f.proposedStatus && (
        <p>
          Proposed status: <strong>{f.proposedStatus}</strong>
          {f.proposedReason ? `. ${f.proposedReason}` : ""} It waits in your needs-you list; nothing changed.
        </p>
      )}
      {f.draft && (
        <div className="note" role="group" aria-label="Reply draft">
          <p className="label">Reply draft</p>
          <pre className="pre">{f.draft}</pre>
          <p className="muted">A draft for you to review and send yourself. The product sends nothing.</p>
          <CopyButton text={f.draft} label="Copy the reply draft" />
        </div>
      )}
    </li>
  );
}

function PromptCard({ prompt: p, reload }: { prompt: AgentPrompt; reload: () => void }) {
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const act = useAction();
  const base = `/api/agent/prompts/${encodeURIComponent(p.id)}`;
  const name = p.text.length > 40 ? `${p.text.slice(0, 40)}...` : p.text;

  const setState = async (state: string) => {
    const body: AgentPromptPatch = { state };
    if (await act.run(() => patch(base, agentPrompt, body))) reload();
  };
  const confirm = async () => {
    if (await act.run(() => post(`${base}/confirm`, agentPrompt))) reload();
  };
  const remove = async () => {
    setRemoving(false);
    if (await act.run(() => del(base, agentPromptDeleted))) reload();
  };

  return (
    <li className="panel">
      <header className="row">
        <h3>{p.kind === "trigger" ? "When a signal arrives" : "On a schedule"}</h3>
        <span>
          <Chip tone={STATE_TONE[p.state] ?? "neutral"}>{p.state}</Chip> {p.origin === "agent" && <Chip>made by the agent</Chip>}
        </span>
      </header>
      {editing ? (
        <PromptForm
          initial={p}
          label="Save prompt"
          onCancel={() => setEditing(false)}
          onSubmit={async (v) => {
            const body: AgentPromptPatch = { text: v.text, ...(v.cadence ? { cadence: v.cadence } : {}), ...(v.trigger ? { trigger: v.trigger } : {}) };
            await patch(base, agentPrompt, body);
            setEditing(false);
            reload();
          }}
        />
      ) : (
        <>
          <p className="pre">{p.text}</p>
          <p className="muted">
            {p.cadence && `${cadenceText(p.cadence)}.`}
            {p.trigger && `${signalText(p.trigger)}.`}
            {p.nextRun && ` Next run ${when(p.nextRun)}.`}
          </p>
          {p.last && (
            <p>
              Last result {when(p.last.at)} <Chip tone={RESULT_TONE[p.last.status] ?? "neutral"}>{p.last.status.replace("_", " ")}</Chip>
              {p.last.summary && <span className="muted"> {p.last.summary}</span>}{" "}
              {p.last.sessionId && <Link to={`/ask?conversation=${encodeURIComponent(p.last.sessionId)}`}>Open the conversation</Link>}
            </p>
          )}
        </>
      )}
      {p.kind === "trigger" && !editing && (
        <>
          <h4>Recent firings</h4>
          {p.firings.length === 0 ? <Empty>It has not fired yet.</Empty> : <ul className="plain stack">{p.firings.map((f, i) => <Firing key={i} f={f} />)}</ul>}
        </>
      )}
      <div className="actions">
        {p.state === "proposed" && (
          <button type="button" disabled={act.busy} onClick={() => void confirm()} aria-label={`Confirm ${name}`}>
            Confirm
          </button>
        )}
        {!editing && (
          <button type="button" onClick={() => setEditing(true)} aria-label={`Edit ${name}`}>
            Edit
          </button>
        )}
        {p.state === "active" && (
          <button type="button" disabled={act.busy} onClick={() => void setState("paused")} aria-label={`Pause ${name}`}>
            Pause
          </button>
        )}
        {p.state === "paused" && (
          <button type="button" disabled={act.busy} onClick={() => void setState("active")} aria-label={`Resume ${name}`}>
            Resume
          </button>
        )}
        <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRemoving(true)} aria-label={`Delete ${name}`}>
          Delete
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {removing && <ConfirmDialog title={`Delete the prompt "${name}"`} consequence="The prompt and its firing history are removed." verb="Delete prompt" onCancel={() => setRemoving(false)} onConfirm={() => void remove()} />}
    </li>
  );
}

// PromptsPanel lists the scheduled prompts of both kinds. A prompt the agent
// proposed does nothing until it is confirmed here.
export function PromptsPanel() {
  const [res, reload] = useResource(() => get("/api/agent/prompts", agentPromptList), [], ["runs", "gmail"]);
  return (
    <Panel title="Scheduled prompts and triggers" id="ask-prompts" res={res} retry={reload}>
      {(data) => (
        <>
          {data.prompts.length === 0 ? (
            <Empty>No scheduled prompt yet. Add one below.</Empty>
          ) : (
            <ul className="plain stack">
              {data.prompts.map((p) => (
                <PromptCard key={p.id} prompt={p} reload={reload} />
              ))}
            </ul>
          )}
          <h3>New prompt</h3>
          {data.prompts.length >= data.limit ? (
            <p className="muted">
              {data.prompts.length} of {data.limit} prompts exist. Delete one to add another.
            </p>
          ) : (
            <PromptForm
              label="Add prompt"
              onSubmit={async (v) => {
                const body: AgentPromptCreate = v;
                await post("/api/agent/prompts", agentPrompt, body);
                reload();
              }}
            />
          )}
        </>
      )}
    </Panel>
  );
}
