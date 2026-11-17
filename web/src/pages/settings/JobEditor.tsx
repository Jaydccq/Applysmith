import { useId, useState, type FormEvent } from "react";
import { ApiError, patch } from "../../api/client";
import { llmRouting } from "../../api/decoders";
import { asApiError } from "../../hooks";
import { leave } from "../../router";
import { Modal } from "../../ui/Modal";
import { useSaveKey, useUnsaved } from "../../ui/SaveBar";
import { ErrorNote, usd } from "../../ui/parts";
import type { LLMFallback, LLMRoute, LLMRouting, LLMRoutingPatch, LLMTaskRoute } from "../../wire";
import { ModelPick, THINKING } from "./modelsShared";

type Draft = { model: LLMFallback; thinking: string; maxOutputTokens: string; timeoutSeconds: string; firstTokenSeconds: string; backups: LLMFallback[]; budget: string };

// A job on the default has no settings of its own: what the page knows of it
// (thinking, limits) is the job's default, shown as a placeholder, so it is
// sent only when the user types it. A Custom job starts from its row.
const draftOf = (t: LLMTaskRoute): Draft => {
  const own = t.source !== "preset";
  const text = (n?: number) => (own && n ? String(n) : "");
  return {
    model: { provider: t.provider ?? "", model: t.model ?? "" },
    thinking: own ? (t.thinking ?? "") : "",
    maxOutputTokens: text(t.maxOutputTokens),
    timeoutSeconds: text(t.timeoutSeconds),
    firstTokenSeconds: text(t.firstTokenSeconds),
    backups: t.fallbacks ?? [],
    budget: t.budgetUsd ? String(t.budgetUsd) : "",
  };
};

// jobDefault is the placeholder of a field a job on the default leaves blank.
const jobDefault = (t: LLMTaskRoute, n?: number) => (t.source === "preset" && n ? `${n} (job default)` : undefined);

// The part of the draft that is the job's route; the budget is saved apart.
const routeOf = ({ budget: _, ...route }: Draft) => JSON.stringify(route);

// wholeNumber reads an optional count; blank is 0 (inherit or off).
function wholeNumber(label: string, text: string): number {
  if (text.trim() === "") return 0;
  const n = Number(text);
  if (!Number.isInteger(n) || n < 0) throw new ApiError("validation_failed", `${label} must be a whole number of zero or more.`, { requestId: "" });
  return n;
}

// JobEditor changes what one job uses. A saved route replaces the job's row in
// full, so the job turns Custom; a change to the budget alone leaves a job on
// the default. The server validates the patch and its message is shown here,
// beside the fields. While it holds an edit, leaving the page or closing asks
// first, and Mod+S saves.
export function JobEditor({ task, routing, known, onSaved, onClose, returnFocus }: { task: LLMTaskRoute; routing: LLMRouting; known: LLMFallback[]; onSaved: (say: string) => void; onClose: () => void; returnFocus?: () => HTMLElement | null | undefined }) {
  const initial = draftOf(task);
  const [draft, setDraft] = useState<Draft>(initial);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const ids = useId();
  const set = (change: Partial<Draft>) => setDraft({ ...draft, ...change });
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const ask = useUnsaved(dirty);
  const saveKey = useSaveKey(dirty);
  const moveBackup = (i: number, by: number) => {
    const next = [...draft.backups];
    [next[i], next[i + by]] = [next[i + by], next[i]];
    set({ backups: next });
  };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: LLMRoutingPatch = {};
      if (routeOf(draft) !== routeOf(initial)) {
        if (!draft.model.provider || !draft.model.model.trim()) throw new ApiError("validation_failed", "Choose a model.", { requestId: "" });
        const row: LLMRoute = { provider: draft.model.provider, model: draft.model.model.trim() };
        if (draft.thinking) row.thinking = draft.thinking;
        const max = wholeNumber("The longest answer", draft.maxOutputTokens);
        const timeout = wholeNumber("The time limit", draft.timeoutSeconds);
        const first = wholeNumber("The wait for a first word", draft.firstTokenSeconds);
        if (max) row.maxOutputTokens = max;
        if (timeout) row.timeoutSeconds = timeout;
        if (first) row.firstTokenSeconds = first;
        if (draft.backups.length > 0) row.fallbacks = draft.backups.map((f) => ({ provider: f.provider, model: f.model.trim() }));
        body.routes = { [task.task]: row };
      }
      if (draft.budget !== initial.budget) {
        const n = draft.budget.trim() === "" ? 0 : Number(draft.budget);
        if (!Number.isFinite(n) || n < 0) throw new ApiError("validation_failed", "The budget must be zero or more.", { requestId: "" });
        body.budgets = { [task.task]: n };
      }
      await patch("/api/llm/routing", llmRouting, body);
      onSaved(`${task.task} saved.`);
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title="Change the model" variant="drawer" onClose={() => leave(onClose)} returnFocus={returnFocus}>
      <form onSubmit={save} className="job-form" aria-label={`Change ${task.task}`}>
        <p>
          <strong>{task.purpose ?? task.task}</strong>
        </p>
        <p className="muted">
          Job <code>{task.task}</code>
        </p>
        {task.source === "preset" && <p className="note info">This job follows the default. Saving a different model makes it Custom; Use default puts it back.</p>}
        <ModelPick label="Model" value={draft.model} onChange={(model) => set({ model })} routing={routing} known={known} pools />
        <h3>If it fails, try</h3>
        {draft.backups.length === 0 && <p className="muted">No backup. If this model fails the job fails.</p>}
        {draft.backups.map((b, i) => (
          <div key={i} className="backup">
            <ModelPick label={`Backup ${i + 1}`} value={b} onChange={(v) => set({ backups: draft.backups.map((x, j) => (j === i ? v : x)) })} routing={routing} known={known} />
            <span className="svc-actions">
              {i > 0 && (
                <button type="button" className="small" onClick={() => moveBackup(i, -1)} aria-label={`Move backup ${i + 1} up`}>
                  Move up
                </button>
              )}
              {i < draft.backups.length - 1 && (
                <button type="button" className="small" onClick={() => moveBackup(i, 1)} aria-label={`Move backup ${i + 1} down`}>
                  Move down
                </button>
              )}
              <button type="button" className="small" onClick={() => set({ backups: draft.backups.filter((_, j) => j !== i) })} aria-label={`Remove backup ${i + 1}`}>
                Remove
              </button>
            </span>
          </div>
        ))}
        {draft.backups.length < 4 && (
          <p>
            <button type="button" onClick={() => set({ backups: [...draft.backups, { provider: "", model: "" }] })}>
              Add a backup
            </button>
          </p>
        )}
        <details className="svc-more">
          <summary>Advanced</summary>
          <div className="fields">
            <label>
              How hard it thinks
              <select value={draft.thinking} onChange={(e) => set({ thinking: e.target.value })}>
                {THINKING.map(([v, say]) => (
                  <option key={v} value={v}>
                    {v === "" && task.source === "preset" && task.thinking ? `${say} (${task.thinking})` : say}
                  </option>
                ))}
              </select>
            </label>
            <label htmlFor={`${ids}-max`}>Longest answer, in tokens (blank: job default)</label>
            <input id={`${ids}-max`} inputMode="numeric" placeholder={jobDefault(task, task.maxOutputTokens)} value={draft.maxOutputTokens} onChange={(e) => set({ maxOutputTokens: e.target.value })} />
            <label htmlFor={`${ids}-timeout`}>Give up after, in seconds (blank: job default)</label>
            <input id={`${ids}-timeout`} inputMode="numeric" placeholder={jobDefault(task, task.timeoutSeconds)} value={draft.timeoutSeconds} onChange={(e) => set({ timeoutSeconds: e.target.value })} />
            <label htmlFor={`${ids}-first`}>Try the backup if no reply starts within, in seconds (blank: off)</label>
            <input id={`${ids}-first`} inputMode="numeric" placeholder={jobDefault(task, task.firstTokenSeconds)} value={draft.firstTokenSeconds} onChange={(e) => set({ firstTokenSeconds: e.target.value })} />
            {task.budgetable && (
              <>
                <label htmlFor={`${ids}-budget`}>Daily budget, in US dollars (blank: none)</label>
                <input id={`${ids}-budget`} type="number" inputMode="decimal" min={0} step={0.01} value={draft.budget} onChange={(e) => set({ budget: e.target.value })} />
                <p className="muted">
                  Spent today: {usd(task.spentTodayUsd ?? 0)}. At its budget the job moves to the cheapest model until midnight{task.group === "evaluation" ? ", and unattended evaluation waits" : ""}.
                </p>
              </>
            )}
          </div>
        </details>
        {error && <ErrorNote error={error} />}
        <div className="actions foot">
          <button type="button" onClick={() => leave(onClose)} disabled={busy}>
            Cancel
          </button>
          <button type="submit" ref={saveKey} disabled={busy || !dirty}>
            Save
          </button>
        </div>
        {ask}
      </form>
    </Modal>
  );
}
