import { useId, useState, type FormEvent } from "react";
import { get, patch } from "../../api/client";
import { agentSettings } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { ErrorNote, Panel } from "../../ui/parts";
import type { AgentSettings, AgentSettingsPatch } from "../../wire";
import { useAction } from "../action";
import { HarnessView } from "./AgentHarness";

function Switch({ label, hint, checked, busy, onChange }: { label: string; hint: string; checked: boolean; busy: boolean; onChange: (on: boolean) => void }) {
  return (
    <li>
      <label className="check">
        <input type="checkbox" checked={checked} disabled={busy} onChange={(e) => onChange(e.target.checked)} />
        {label}
      </label>
      <span className="muted">{hint}</span>
    </li>
  );
}

// Budget edits the number of tokens a prompt may use. The limits are the
// server's, which checks them again.
function Budget({ s, busy, save }: { s: AgentSettings; busy: boolean; save: (n: number) => Promise<void> }) {
  const [draft, setDraft] = useState(String(s.tokenBudget));
  const id = useId();
  const n = Number(draft);
  const inRange = Number.isInteger(n) && n >= s.tokenBudgetMin && n <= s.tokenBudgetMax;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (inRange) void save(n);
  };
  return (
    <form onSubmit={submit} className="form-grid">
      <div>
        <label htmlFor={id}>Prompt budget (tokens)</label>
        <input id={id} inputMode="numeric" value={draft} onChange={(e) => setDraft(e.target.value)} />
        <span className="muted">
          Between {s.tokenBudgetMin} and {s.tokenBudgetMax}; the default is {s.tokenBudgetDefault}. Older turns are summarised when a conversation grows past 80% of it.
        </span>
      </div>
      {!inRange && (
        <p className="note error">
          The budget is a whole number from {s.tokenBudgetMin} to {s.tokenBudgetMax}.
        </p>
      )}
      <div className="actions">
        <button type="submit" disabled={busy || !inRange || n === s.tokenBudget}>
          Save budget
        </button>
      </div>
    </form>
  );
}

function Controls({ settings, reload }: { settings: AgentSettings; reload: () => void }) {
  const act = useAction();
  const send = async (body: AgentSettingsPatch) => {
    if (await act.run(() => patch("/api/agent/settings", agentSettings, body))) reload();
  };
  return (
    <>
      <ul className="plain stack">
        <Switch
          label="Summarise long conversations"
          hint="When off, a long conversation drops its oldest turns instead of keeping a summary of them."
          checked={settings.compaction}
          busy={act.busy}
          onChange={(on) => void send({ compaction: on })}
        />
        <Switch
          label="Nightly memory review"
          hint="When off, the 03:30 pass that proposes facts and rules from your conversations does not run. Nothing it proposes is used until you approve it."
          checked={settings.consolidation}
          busy={act.busy}
          onChange={(on) => void send({ consolidation: on })}
        />
      </ul>
      <h3>Self-improvement</h3>
      <ul className="plain stack">
        <Switch
          label="Improve from conversations"
          hint="Rounds run nightly, replay conversations that went wrong without side effects, and adopt better guidance automatically. The fixed rules, gates and tools never change."
          checked={settings.selfImprove}
          busy={act.busy}
          onChange={(on) => void send({ selfImprove: on })}
        />
      </ul>
      <HarnessView />
      <Budget key={settings.tokenBudget} s={settings} busy={act.busy} save={(n) => send({ tokenBudget: n })} />
      {act.error && <ErrorNote error={act.error} />}
    </>
  );
}

// AskAgentSection holds the switches of the Ask Agent. None of them touches a
// safety gate: those are code and have no setting.
export function AskAgentSection() {
  const [res, reload] = useResource(() => get("/api/agent/settings", agentSettings), [], [], { keep: "settings.agent" });
  return (
    <Panel title="Ask Agent" id="settings-askagent" res={res} retry={reload} what="the Ask Agent settings">
      {(s) => <Controls settings={s} reload={reload} />}
    </Panel>
  );
}
