import { useEffect, useState, type FormEvent } from "react";
import { get, withQuery } from "../../api/client";
import { usageCallPage } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { Chip, Empty, FileLink, Pager, Panel } from "../../ui/parts";
import { attemptOutcome, attemptReason, when } from "../action";

const PAGE_SIZE = 50;

type Filters = {
  task: string;
  provider: string;
  outcome: string;
  from: string;
  to: string;
};

const NONE: Filters = { task: "", provider: "", outcome: "", from: "", to: "" };

// Calls is the call-by-call table of the ledger: no prompt or response text.
// Filters apply when the form is submitted; the export link carries the same.
// A pick (a task chosen on the cost bars) filters the list at once; a call with
// a run opens that run's trace through onTrace.
export function Calls({ tasks, providers, pick, onTrace }: { tasks: string[]; providers: string[]; pick: { name: string } | null; onTrace: (runId: string) => void }) {
  const [draft, setDraft] = useState<Filters>(NONE);
  const [applied, setApplied] = useState<Filters>(NONE);
  const [page, setPage] = useState(1);
  useEffect(() => {
    if (!pick) return;
    setDraft((d) => ({ ...d, task: pick.name }));
    setApplied((a) => ({ ...a, task: pick.name }));
    setPage(1);
    document.getElementById("usage-calls")?.scrollIntoView?.({ block: "start" });
  }, [pick]);
  const query = { ...applied, page, pageSize: PAGE_SIZE };
  const [res, reload] = useResource(() => get("/api/llm/calls", usageCallPage, query), [JSON.stringify(query)], ["runs"]);
  const set = (key: keyof Filters) => (value: string) => setDraft({ ...draft, [key]: value });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setApplied(draft);
    setPage(1);
  };
  const clear = () => {
    setDraft(NONE);
    setApplied(NONE);
    setPage(1);
  };

  return (
    <Panel title="Model calls" id="usage-calls" res={res} retry={reload}>
      {(data) => (
        <>
          <form className="filters" onSubmit={submit} aria-label="Call filters">
            <label>
              Task
              <input list="usage-tasks" value={draft.task} onChange={(e) => set("task")(e.target.value)} />
              <datalist id="usage-tasks">
                {tasks.map((t) => (
                  <option key={t} value={t} />
                ))}
              </datalist>
            </label>
            <label>
              Provider
              <input list="usage-providers" value={draft.provider} onChange={(e) => set("provider")(e.target.value)} />
              <datalist id="usage-providers">
                {providers.map((p) => (
                  <option key={p} value={p} />
                ))}
              </datalist>
            </label>
            <label>
              Outcome
              <select value={draft.outcome} onChange={(e) => set("outcome")(e.target.value)}>
                <option value="">Any outcome</option>
                <option value="ok">Succeeded</option>
                <option value="failed">Failed</option>
              </select>
            </label>
            <label>
              From
              <input type="date" value={draft.from} onChange={(e) => set("from")(e.target.value)} />
            </label>
            <label>
              To
              <input type="date" value={draft.to} onChange={(e) => set("to")(e.target.value)} />
            </label>
            <span className="actions">
              <button type="submit">Apply filters</button>
              <button type="button" onClick={clear}>
                Clear
              </button>
            </span>
          </form>
          <p className="export">
            <span aria-live="polite">{data.total} {data.total === 1 ? "attempt" : "attempts"}. </span>
            <FileLink path={withQuery("/api/llm/calls/export", applied)} name="model-calls.csv">
              Export these calls as CSV
            </FileLink>
          </p>
          {data.calls.length === 0 ? (
            <Empty>{Object.values(applied).some(Boolean) ? "No model call matches these filters." : "No model call is recorded yet."}</Empty>
          ) : (
            <>
              <table className="cards dense">
                <caption className="sr-only">Model calls, newest first</caption>
                <thead>
                  <tr>
                    <th scope="col" className="nowrap">Task</th>
                    <th scope="col" className="fill">Model</th>
                    <th scope="col" className="num">Tokens in / out</th>
                    <th scope="col" className="num">Cost</th>
                    <th scope="col" className="num">Latency ms</th>
                    <th scope="col" className="state-col">Outcome</th>
                    <th scope="col">
                      <span className="sr-only">Trace</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {data.calls.map((c) => (
                    <tr key={c.id}>
                      {/* The time sits under its task and the provider under
                          its model, so the row fits a window beside the
                          browser panel with the model's name still read. */}
                      <td data-label="Task" className="nowrap">
                        {c.task}
                        <span className="sub muted">
                          <time dateTime={c.startedAt}>{when(c.startedAt)}</time>
                        </span>
                      </td>
                      <td data-label="Model" className="fill">
                        {c.model}
                        <span className="sub muted">{c.provider}</span>
                      </td>
                      <td data-label="Tokens in / out" className="num">
                        {c.inputTokens.toLocaleString("en-US")} / {c.outputTokens.toLocaleString("en-US")}
                      </td>
                      <td data-label="Cost" className="num">{c.costUsd === undefined ? "unpriced" : `$${c.costUsd.toFixed(4)}`}</td>
                      <td data-label="Latency ms" className="num">{c.latencyMs.toLocaleString("en-US")}</td>
                      <td data-label="Outcome" className="state-col">
                        <Chip tone={c.outcome === "ok" ? "good" : "bad"}>{attemptOutcome(c.outcome)}</Chip>
                        {c.attempt > 1 && <span className="muted"> attempt {c.attempt}</span>}
                        {c.reason ? <span className="muted"> {attemptReason(c.reason)}</span> : c.downgraded && <span className="muted"> downgraded</span>}
                      </td>
                      <td className="row-actions">
                        {c.runId && (
                          <button type="button" aria-label={`Open the trace of ${c.runId}`} onClick={() => onTrace(c.runId!)}>
                            Trace
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <Pager page={page} pageSize={PAGE_SIZE} total={data.total} onPage={setPage} />
            </>
          )}
        </>
      )}
    </Panel>
  );
}
