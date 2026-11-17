import { useState, type FormEvent } from "react";
import { ApiError, patch } from "../../api/client";
import { llmRouting } from "../../api/decoders";
import { asApiError } from "../../hooks";
import { leave } from "../../router";
import { SaveBar } from "../../ui/SaveBar";
import { Chip, Empty, ErrorNote } from "../../ui/parts";
import type { LLMFallback, LLMGroup, LLMRouting } from "../../wire";
import { Feedback, ModelPick, modelTag, type Action } from "./modelsShared";

const MODES: [string, string, string][] = [
  ["order", "In the listed order", "in order"],
  ["rotate", "Take turns: each call starts with the next model", "taking turns"],
  ["usage", "Fewest calls today first", "fewest calls first"],
];

type Draft = { name: string; mode: string; members: LLMFallback[] };

// PoolEditor replaces one pool in full, as the job editor does a job; a saved
// pool keeps its name. The server's message is shown beside the fields.
function PoolEditor({ name, pool, routing, known, onSaved, onCancel }: { name: string; pool: LLMGroup; routing: LLMRouting; known: LLMFallback[]; onSaved: (name: string) => void; onCancel: () => void }) {
  const initial: Draft = { name, mode: pool.mode, members: pool.members };
  const [draft, setDraft] = useState<Draft>(initial);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (change: Partial<Draft>) => setDraft({ ...draft, ...change });
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const saved: LLMGroup = { mode: draft.mode, members: draft.members.map((m) => ({ provider: m.provider, model: m.model.trim() })) };
      await patch("/api/llm/routing", llmRouting, { groups: { [draft.name.trim()]: saved } });
      onSaved(draft.name.trim());
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} className="inline-form" aria-label={name ? `Pool ${name}` : "New pool"}>
      <h4>{name ? `Pool ${name}` : "New pool"}</h4>
      <label>
        Name (lowercase letters, digits, - and _)
        <input value={draft.name} onChange={(e) => set({ name: e.target.value })} readOnly={name !== ""} spellCheck={false} />
      </label>
      <label>
        How calls are spread
        <select value={draft.mode} onChange={(e) => set({ mode: e.target.value })}>
          {MODES.map(([mode, say]) => (
            <option key={mode} value={mode}>
              {say}
            </option>
          ))}
        </select>
      </label>
      {draft.members.map((m, i) => (
        <div key={i} className="pick-row">
          <ModelPick label={`Model ${i + 1}`} value={m} onChange={(v) => set({ members: draft.members.map((x, j) => (j === i ? v : x)) })} routing={routing} known={known} />
          <button type="button" onClick={() => set({ members: draft.members.filter((_, j) => j !== i) })} aria-label={`Remove model ${i + 1}`}>
            Remove
          </button>
        </div>
      ))}
      {draft.members.length < 8 && (
        <button type="button" onClick={() => set({ members: [...draft.members, { provider: "", model: "" }] })}>
          Add a model
        </button>
      )}
      {error && <ErrorNote error={error} />}
      <SaveBar dirty={JSON.stringify(draft) !== JSON.stringify(initial)} savedAt="">
        <button type="button" onClick={() => leave(onCancel)} disabled={busy}>
          Cancel
        </button>
        <button type="submit" disabled={busy}>
          Save pool
        </button>
      </SaveBar>
    </form>
  );
}

// Pools lists the model pools: a pool spreads one job's calls over several
// models. A job names a pool as its model; deleting one a job still names is
// refused, and the refusal says which.
export function Pools({ routing, known, reload }: { routing: LLMRouting; known: LLMFallback[]; reload: () => void }) {
  const [editing, setEditing] = useState<string | null>(null);
  const [result, setResult] = useState<Action>(null);
  const [busy, setBusy] = useState(false);
  const names = Object.keys(routing.groups).sort();
  const remove = async (name: string) => {
    setBusy(true);
    try {
      await patch("/api/llm/routing", llmRouting, { groups: { [name]: null } });
      setResult({ say: `Pool ${name} deleted.` });
      reload();
    } catch (err) {
      setResult({ fail: asApiError(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <details className="svc-more">
      <summary>Advanced: model pools</summary>
      <div className="stack">
        <p>A pool spreads one job&apos;s calls over several models, for example to share a rate limit. Pick a pool in the model list when you change a job.</p>
        <Feedback result={result} />
        {names.length === 0 ? (
          <Empty>No pools.</Empty>
        ) : (
          <ul className="plain stack">
            {names.map((n) => (
              <li key={n} className="row">
                <span>
                  <strong>{n}</strong> <Chip>{MODES.find(([m]) => m === routing.groups[n].mode)?.[2] ?? routing.groups[n].mode}</Chip> <span className="muted">{routing.groups[n].members.map((m) => modelTag(routing, m)).join(", ")}</span>
                </span>
                <span className="svc-actions">
                  <button type="button" disabled={busy} onClick={() => leave(() => setEditing(n))} aria-label={`Edit pool ${n}`}>
                    Edit
                  </button>
                  <button type="button" disabled={busy} onClick={() => void remove(n)} aria-label={`Delete pool ${n}`}>
                    Delete
                  </button>
                </span>
              </li>
            ))}
          </ul>
        )}
        <p>
          <button type="button" disabled={busy || routing.providers.length === 0} onClick={() => leave(() => setEditing(""))}>
            Add a pool
          </button>
        </p>
        {editing !== null && (
          <PoolEditor
            key={editing}
            name={editing}
            pool={routing.groups[editing] ?? { mode: "order", members: [{ provider: "", model: "" }] }}
            routing={routing}
            known={known}
            onCancel={() => setEditing(null)}
            onSaved={(saved) => {
              setResult({ say: `Pool ${saved} saved.` });
              setEditing(null);
              reload();
            }}
          />
        )}
      </div>
    </details>
  );
}
