import { useRef, useState } from "react";
import { patch, post } from "../../api/client";
import { llmRouting } from "../../api/decoders";
import { asApiError } from "../../hooks";
import { leave } from "../../router";
import { Modal } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, usd } from "../../ui/parts";
import type { LLMBench, LLMBenchResult, LLMCostRow, LLMFallback, LLMRest, LLMRouting, LLMTaskRoute } from "../../wire";
import { JobEditor } from "./JobEditor";
import { Pools } from "./Pools";
import { Feedback, byArea, COST_DAYS, modelTag, providerName, serviceName, usable, type Action } from "./modelsShared";

const REST_WHY: Record<string, string> = {
  quota: "out of credit or plan",
  "rate-limit": "rate limited",
  unavailable: "timing out or failing",
  "dead-key": "the key was refused",
  "model-gone": "the model is gone",
};

// A resting model is tried after the others, never skipped; it comes back by
// itself when its time is up or it answers.
function RestNote({ rests, routing }: { rests: LLMRest[]; routing: LLMRouting }) {
  return (
    <div role="status" className="banner">
      <strong>Resting for now</strong> (asked after the others, not dropped):
      <ul className="plain">
        {rests.map((r) => (
          <li key={`${r.provider}/${r.model ?? ""}`}>
            {providerName(routing, r.provider)}
            {r.model ? ` · ${r.model}` : " (every model)"}: {REST_WHY[r.why] ?? r.why} until {new Date(r.until).toLocaleTimeString()}
            {r.failures > 1 ? `, ${r.failures} failures in a row` : ""}
          </li>
        ))}
      </ul>
    </div>
  );
}

// BenchNote is the latest measured comparison for a job, from `applysmith llm
// bench`: the best candidate on the line, every candidate under it.
function BenchNote({ bench, routing }: { bench: LLMBench; routing: LLMRouting }) {
  const best = bench.results[0];
  if (!best) return null;
  const score = (r: LLMBenchResult) => `${r.correct}/${bench.cases} correct, ${r.wrong} wrong, ${r.noAnswer} no answer, median ${r.p50Ms} ms`;
  return (
    <details className="muted">
      <summary>
        Bench {new Date(bench.at).toLocaleDateString()}: {modelTag(routing, best)}, {score(best)}
      </summary>
      <ul className="plain">
        {bench.results.map((r) => (
          <li key={`${r.provider}/${r.model}`}>
            {modelTag(routing, r)}: {score(r)}, slowest 1 in 20 {r.p95Ms} ms, {r.costUsd === undefined ? "unpriced" : `$${r.costUsd.toFixed(4)}`}
          </li>
        ))}
      </ul>
    </details>
  );
}

// spend says what a job's calls cost over the last COST_DAYS days; a job with
// no call says nothing.
function spend(row: LLMCostRow | undefined): string {
  if (!row || row.calls === 0) return "";
  const calls = `${row.calls} ${row.calls === 1 ? "call" : "calls"}`;
  return `${usd(row.costUsd)} for ${calls} in ${COST_DAYS} days${row.unpriced > 0 ? ` (${row.unpriced} unpriced)` : ""}`;
}

const SOURCE: Record<string, { word: string; tone: "info" | "neutral" }> = {
  preset: { word: "Default", tone: "neutral" },
  table: { word: "Custom", tone: "info" },
  env: { word: "Set by environment", tone: "neutral" },
};

function JobRow({ routing, t, cost, busy, onChange, onDefault }: { routing: LLMRouting; t: LLMTaskRoute; cost?: LLMCostRow; busy: boolean; onChange: () => void; onDefault: () => void }) {
  const source = t.source ? SOURCE[t.source] : undefined;
  const why = t.unrouted || t.broken;
  const backups = t.fallbacks ?? [];
  const money = spend(cost);
  return (
    <li className="job">
      <div className="job-main">
        <p className="job-purpose">{t.purpose ?? t.task}</p>
        <p className="job-id muted">{t.task}</p>
        <p className="job-model">
          {t.provider && t.model ? <span className="model-tag">{modelTag(routing, { provider: t.provider, model: t.model })}</span> : null}
          {backups.length > 0 && (
            <span className="muted">
              If it fails: {backups.map((b: LLMFallback) => modelTag(routing, b)).join(", then ")}
            </span>
          )}
          {t.thinking && <span className="muted">Thinking: {t.thinking}</span>}
        </p>
        {why ? <div className="note error compact">{why}</div> : !t.model && <p className="muted">No model chosen. Change it to turn this job on.</p>}
        {(money || (t.budgetUsd ?? 0) > 0 || (t.spentTodayUsd ?? 0) > 0) && (
          <p className="job-meta muted">
            {[money, t.budgetUsd ? `Budget ${usd(t.budgetUsd)} a day` : "", t.spentTodayUsd ? `${usd(t.spentTodayUsd)} today` : ""].filter(Boolean).join(" · ")} {t.budgetReached && <Chip tone="bad">Budget reached</Chip>}
          </p>
        )}
        {t.bench && <BenchNote bench={t.bench} routing={routing} />}
      </div>
      <div className="job-side">
        {source && <Chip tone={source.tone}>{source.word}</Chip>}
        <button type="button" disabled={busy} onClick={onChange} aria-label={`Change ${t.task}`}>
          Change
        </button>
        {t.source === "table" && (
          <button type="button" disabled={busy} onClick={onDefault} aria-label={`Use default for ${t.task}`}>
            Use default
          </button>
        )}
      </div>
    </li>
  );
}

const SIZES: [string, string][] = [["fast", "Quick jobs"], ["standard", "Standard jobs"], ["deep", "Heavy jobs"]];

// DefaultLine says in one sentence what a job nobody has set uses, and changes
// it. The default is the active preset and its backup; changing it reroutes
// every job that follows it, so the change is made in a dialog that says so.
function DefaultLine({ routing, signedIn, onChanged }: { routing: LLMRouting; signedIn: boolean; onChanged: (say: string) => void }) {
  const [open, setOpen] = useState(false);
  const following = routing.tasks.filter((t) => t.source === "preset" && t.provider && t.model);
  const tags = [...new Set(following.map((t) => modelTag(routing, { provider: t.provider!, model: t.model! })))];
  const sizes = SIZES.map(([size, say]) => [say, following.find((t) => t.class === size)] as const).filter(([, t]) => t);
  const preset = routing.presets.find((p) => p.name === routing.preset);
  const backup = routing.presets.find((p) => p.name === routing.presetFallback);
  return (
    <div className="default-line">
      <p id="settings-default">
        {preset ? (
          <>
            Jobs you haven&apos;t set use <strong>{tags.length === 1 ? tags[0] : serviceName(preset)}</strong>
            {backup ? (
              <>
                ; if that fails, <strong>{serviceName(backup)}</strong>
              </>
            ) : null}
            .
          </>
        ) : (
          <>No default is set. Jobs you haven&apos;t set have no model until you choose one.</>
        )}
      </p>
      {tags.length > 1 && (
        <p className="muted">
          {sizes.map(([say, t]) => `${say}: ${new Set(following.map((x) => x.provider)).size === 1 ? t!.model : modelTag(routing, { provider: t!.provider!, model: t!.model! })}`).join(" · ")}
        </p>
      )}
      <p>
        <button type="button" onClick={() => setOpen(true)}>
          Change default
        </button>
      </p>
      {open && <ChangeDefault routing={routing} signedIn={signedIn} onClose={() => setOpen(false)} onChanged={onChanged} />}
    </div>
  );
}

function ChangeDefault({ routing, signedIn, onClose, onChanged }: { routing: LLMRouting; signedIn: boolean; onClose: () => void; onChanged: (say: string) => void }) {
  const cancel = useRef<HTMLButtonElement>(null);
  const [name, setName] = useState(routing.preset);
  const [backup, setBackup] = useState(routing.presetFallback ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const presets = routing.presets.filter((p) => p.kind !== "fake");
  const option = (p: (typeof presets)[number], current: string) => (
    <option key={p.name} value={p.name} disabled={!usable(p, routing, signedIn) && p.name !== current}>
      {serviceName(p)}
      {usable(p, routing, signedIn) ? "" : " (not connected)"}
    </option>
  );
  const chosen = presets.find((p) => p.name === name);
  const save = async () => {
    setBusy(true);
    try {
      await post("/api/llm/preset", llmRouting, backup && backup !== name ? { name, fallback: backup } : { name });
      onChanged(`The default is now ${chosen ? serviceName(chosen) : name}.`);
      onClose();
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };
  return (
    <Modal title="Change the default" onClose={onClose} initialFocus={cancel}>
      <p>
        Every job marked Default switches to the service you choose{chosen ? <> ({serviceName(chosen)})</> : null}. Jobs marked Custom keep the model you picked for them.
      </p>
      <div className="stack">
        <label>
          Default service
          <select value={name} onChange={(e) => setName(e.target.value)}>
            {!name && <option value="">Choose a service</option>}
            {presets.map((p) => option(p, routing.preset))}
          </select>
        </label>
        <label>
          If it fails, use
          <select value={backup} onChange={(e) => setBackup(e.target.value)}>
            <option value="">Nothing</option>
            {presets.filter((p) => p.name !== name).map((p) => option(p, routing.presetFallback ?? ""))}
          </select>
        </label>
      </div>
      {error !== null && <ErrorNote error={error} />}
      <div className="actions">
        <button type="button" ref={cancel} onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="primary" disabled={busy || !name || (name === routing.preset && backup === (routing.presetFallback ?? ""))} onClick={() => void save()}>
          Change default
        </button>
      </div>
    </Modal>
  );
}

// Jobs is the routing, said by what each job is for: the default on top, then
// the jobs by area. A job's model, backups, cost and whether it is Default or
// Custom are on its row; Change opens the editor.
export function Jobs({ routing, cost, reload, signedIn, extraModels }: { routing: LLMRouting; cost?: LLMCostRow[]; reload: () => void; signedIn: boolean; extraModels: LLMFallback[] }) {
  const [editing, setEditing] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Action>(null);
  const task = routing.tasks.find((t) => t.task === editing);
  const known = [...routing.knownModels, ...extraModels];
  const opener = useRef<HTMLElement | null>(null);

  const useDefault = async (id: string) => {
    setBusy(true);
    try {
      await patch("/api/llm/routing", llmRouting, { routes: { [id]: null } });
      setResult({ say: `${id} follows the default again.` });
      reload();
    } catch (err) {
      setResult({ fail: asApiError(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <h3 id="settings-jobs">What each job uses</h3>
      <p>Each job below runs on the model shown. A job marked Default follows the default; change a job to choose its model yourself.</p>
      <DefaultLine
        routing={routing}
        signedIn={signedIn}
        onChanged={(say) => {
          setResult({ say });
          reload();
        }}
      />
      <Feedback result={result} />
      {routing.rests.length > 0 && <RestNote rests={routing.rests} routing={routing} />}
      {routing.tasks.length === 0 ? (
        <Empty>No jobs are routed.</Empty>
      ) : (
        byArea(routing.tasks).map((area) => (
          <section key={area.id} className="job-area" aria-labelledby={`job-area-${area.id}`}>
            <h4 id={`job-area-${area.id}`}>{area.title}</h4>
            <ul className="plain job-list">
              {area.tasks.map((t) => (
                <JobRow
                  key={t.task}
                  routing={routing}
                  t={t}
                  cost={cost?.find((c) => c.name === t.task)}
                  busy={busy}
                  onChange={() => {
                    opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
                    leave(() => setEditing(t.task));
                  }}
                  onDefault={() => void useDefault(t.task)}
                />
              ))}
            </ul>
          </section>
        ))
      )}
      {task && (
        <JobEditor
          key={task.task}
          task={task}
          routing={routing}
          known={known}
          returnFocus={() => opener.current}
          onClose={() => setEditing(null)}
          onSaved={(say) => {
            setResult({ say });
            setEditing(null);
            reload();
          }}
        />
      )}
      <Pools routing={routing} known={known} reload={reload} />
    </>
  );
}
