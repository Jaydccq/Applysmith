import { useState } from "react";
import { get } from "../api/client";
import { llmCost, llmRouting } from "../api/decoders";
import { useResource } from "../hooks";
import { Link, useSearchParams } from "../router";
import { Empty, Panel, usd } from "../ui/parts";
import { Tabs } from "../ui/Tabs";
import type { LLMCost, LLMCostRow, LLMTaskRoute } from "../wire";
import { CostChart } from "./usage/Chart";
import { Calls } from "./usage/Calls";
import { CostByDay } from "./usage/Days";
import { Live } from "./usage/Live";
import { Traces } from "./usage/Traces";
import { PageHeader } from "../ui/PageHeader";

const sum = (rows: LLMCostRow[]) => rows.reduce((total, r) => total + r.costUsd, 0);
const percent = (rate: number) => `${Math.round(rate * 100)}%`;

function Tile({ label, value }: { label: string; value: string }) {
  return (
    <li className="tile">
      <span className="figure">{value}</span>
      <span className="label">{label}</span>
    </li>
  );
}

// CapMeter shows the spend the daily cap is held to (this computer's day's
// metered spend) against it. A cap of zero is no cap.
function CapMeter({ spent, cap }: { spent: number; cap: number }) {
  if (cap <= 0) return <p>No daily cost cap is set.</p>;
  const used = Math.min(100, Math.round((spent / cap) * 100));
  const reached = spent >= cap;
  const what = "Over the cap, calls move to the cheapest model and unattended evaluation waits until midnight.";
  return (
    <div>
      <label>
        <span>
          Today against the daily cap of {usd(cap)}: <strong>{used}%</strong>
        </span>
        <meter min={0} max={cap} value={Math.min(spent, cap)} aria-label={`Spent today against the daily cap, ${used} percent`} />
      </label>
      {reached ? <p role="status" className="note error">The daily cap is reached. {what}</p> : <p className="muted">{what}</p>}
    </div>
  );
}

function CostTable({ rows, caption, nameLabel }: { rows: LLMCostRow[]; caption: string; nameLabel: string }) {
  return (
    <table className="cards">
      <caption className="sr-only">{caption}</caption>
      <thead>
        <tr>
          <th scope="col">{nameLabel}</th>
          <th scope="col" className="num">Calls</th>
          <th scope="col" className="num">Cost</th>
          <th scope="col" className="num">Success</th>
          <th scope="col" className="num">Fallback</th>
          <th scope="col" className="num">Rate-limited</th>
          <th scope="col" className="num">Cache hit</th>
          <th scope="col" className="num">Unpriced</th>
          <th scope="col" className="num">Median ms</th>
          <th scope="col" className="num">Slowest 5% ms</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.name}>
            <td data-label={nameLabel}>{r.name}</td>
            <td data-label="Calls" className="num">{r.calls}</td>
            <td data-label="Cost" className="num">{usd(r.costUsd)}</td>
            <td data-label="Success" className="num">{percent(r.successRate)}</td>
            <td data-label="Fallback" className="num">{percent(r.fallbackRate)}</td>
            <td data-label="Rate-limited" className="num">{percent(r.rateLimitedRate)}</td>
            <td data-label="Cache hit" className="num">{r.cacheHitRate === undefined ? "-" : percent(r.cacheHitRate)}</td>
            <td data-label="Unpriced" className="num">{r.unpriced}</td>
            <td data-label="Median ms" className="num">{r.p50Ms}</td>
            <td data-label="Slowest 5% ms" className="num">{r.p95Ms}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// byCost lists the most expensive first, so the bars read as a ranking.
const byCost = (rows: LLMCostRow[]) => [...rows].sort((a, b) => b.costUsd - a.costUsd || a.name.localeCompare(b.name));

function TaskChart({ rows: unsorted, onPick }: { rows: LLMCostRow[]; onPick: (task: string) => void }) {
  const rows = byCost(unsorted);
  return (
    <CostChart
      caption="Cost by task"
      empty="No model call is recorded in this period."
      items={rows.map((r) => ({ key: r.name, label: r.name, costUsd: r.costUsd }))}
      table={<CostTable rows={rows} caption="Cost by task" nameLabel="Task" />}
      onPick={onPick}
    />
  );
}

interface Report {
  spent: number;
  week: LLMCost;
  month: LLMCost;
  cap: number;
  over: LLMTaskRoute[];
}

function Body({ spent, week, month, cap, over, onPick }: Report & { onPick: (task: string) => void }) {
  const calls = week.byProvider.reduce((n, r) => n + r.calls, 0);
  return (
    <>
      <ul className="tiles plain" aria-label="Cost summary">
        <Tile label="Today" value={usd(spent)} />
        <Tile label="Last 7 days" value={usd(sum(week.byProvider))} />
        <Tile label="Last 30 days" value={usd(sum(month.byProvider))} />
        <Tile label="Calls in 7 days" value={String(calls)} />
      </ul>
      <CapMeter spent={spent} cap={cap} />
      {over.length > 0 && (
        <p role="status" className="note error">
          At {over.length === 1 ? "its" : "their"} daily budget, so on the cheapest model until midnight: {over.map((t) => `${t.task} (${usd(t.spentTodayUsd ?? 0)} of ${usd(t.budgetUsd ?? 0)})`).join(", ")}. <Link to="/settings#settings-models">Budgets</Link>
        </p>
      )}
      {calls === 0 ? (
        <Empty>
          No model call in the last 7 days. A call is recorded when a scan, an evaluation, a fill or the Ask Agent uses a model; <Link to="/settings#settings-models">Settings, Models</Link> shows which tasks have one.
        </Empty>
      ) : (
        <>
          <h3>Cost by task, last 7 days</h3>
          <p className="muted">
            A task&apos;s model is chosen, and changed, in <Link to="/settings#settings-models">Settings, Models</Link>.
          </p>
          <TaskChart rows={week.byTask} onPick={onPick} />
          <h3>Cost by provider, last 7 days</h3>
          <CostTable rows={byCost(week.byProvider)} caption="Cost by provider" nameLabel="Provider" />
        </>
      )}
    </>
  );
}

const TABS = [
  { id: "cost", label: "Cost" },
  { id: "live", label: "Live" },
] as const;

export default function Usage() {
  const [params, setParams] = useSearchParams();
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? "cost";
  // A picked task filters the call list; an opened trace shows its waterfall.
  // The object identity makes a second pick of the same task apply again.
  const [pick, setPick] = useState<{ name: string } | null>(null);
  const [trace, setTrace] = useState<string | null>(null);
  const [res, reload] = useResource(
    async (): Promise<Report> => {
      const [week, month, routing] = await Promise.all([get("/api/llm/cost", llmCost, { days: 7 }), get("/api/llm/cost", llmCost, { days: 30 }), get("/api/llm/routing", llmRouting)]);
      return { spent: routing.capSpentUsd, week, month, cap: routing.dailyUsdCap, over: routing.tasks.filter((t) => t.budgetReached) };
    },
    [],
    ["runs"],
  );
  // A trace is shown in the Cost tab, so a call in the Live tab brings it there.
  const showTrace = (id: string) => {
    setTrace(id);
    setParams({ tab: undefined }, { replace: true });
    requestAnimationFrame(() => document.getElementById("usage-traces")?.scrollIntoView?.({ block: "start" }));
  };
  return (
    <>
      <PageHeader path="/usage" />
      <Tabs label="Usage sections" tabs={TABS} active={tab} onSelect={(id) => setParams({ tab: id === "cost" ? undefined : id }, { replace: true })}>
        {tab === "live" ? (
          <Live onTrace={showTrace} />
        ) : (
          <>
            <Panel title="Model cost" id="usage-cost" res={res} retry={reload}>
              {(r) => <Body {...r} onPick={(name) => setPick({ name })} />}
            </Panel>
            <CostByDay />
            <Calls pick={pick} onTrace={setTrace} tasks={res.data?.week.byTask.map((r) => r.name) ?? []} providers={res.data?.week.byProvider.map((r) => r.name) ?? []} />
            <Traces open={trace} setOpen={setTrace} />
          </>
        )}
      </Tabs>
    </>
  );
}
