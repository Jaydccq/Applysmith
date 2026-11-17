import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { get, quietly } from "../../api/client";
import { usageCallPage } from "../../api/decoders_agent";
import { useInterval } from "../../hooks";
import { Chip, Empty, ErrorNote, formatDateTime, usd } from "../../ui/parts";
import type { UsageCall } from "../../wire";
import { attemptOutcome, attemptReason } from "../action";

const MAX_CALLS = 100;
const POLL_MS = 2000;
const WINDOW_MS = 15 * 60 * 1000;

// LiveCall is one model call: its attempts in the order they ran, as far as
// the ledger has them. A call still on its way shows the attempts that ended.
interface LiveCall {
  callId: string;
  attempts: UsageCall[];
  lastId: number;
}

// foldCalls adds ledger attempts to the calls already shown: attempts of one
// call are grouped by their call id, one the page already has is not counted
// twice, and the newest MAX_CALLS calls are kept, newest first.
function foldCalls(prev: LiveCall[], incoming: UsageCall[]): LiveCall[] {
  const byId = new Map<number, UsageCall>();
  for (const a of [...prev.flatMap((c) => c.attempts), ...incoming]) byId.set(a.id, a);
  const groups = new Map<string, UsageCall[]>();
  for (const a of byId.values()) groups.set(a.callId, [...(groups.get(a.callId) ?? []), a]);
  return [...groups.entries()]
    .map(([callId, attempts]) => ({ callId, attempts: attempts.sort((a, b) => a.attempt - b.attempt || a.id - b.id), lastId: Math.max(...attempts.map((a) => a.id)) }))
    .sort((a, b) => b.lastId - a.lastId)
    .slice(0, MAX_CALLS);
}

const sum = (xs: number[]) => xs.reduce((n, x) => n + x, 0);
const started = (c: LiveCall) => c.attempts[0].startedAt;
const final = (c: LiveCall) => c.attempts[c.attempts.length - 1];
const priced = (c: LiveCall) => c.attempts.filter((a) => a.costUsd !== undefined);
const cost = (c: LiveCall) => sum(priced(c).map((a) => a.costUsd!));

export interface Summary {
  calls: number;
  fallbacks: number;
  avgTtftMs?: number;
  cacheRate?: number;
  spendUsd: number;
}

// summarize reads the calls that started in the 15 minutes before now. A call
// with more than one attempt had to fall back; the first-token time is that of
// the attempt that answered; the cache share is of all input tokens sent.
function summarize(calls: LiveCall[], now: number): Summary {
  const recent = calls.filter((c) => now - Date.parse(started(c)) <= WINDOW_MS);
  const ttfts = recent.flatMap((c) => final(c).ttftMs ?? []);
  const input = sum(recent.flatMap((c) => c.attempts.map((a) => a.inputTokens)));
  return {
    calls: recent.length,
    fallbacks: recent.filter((c) => c.attempts.length > 1).length,
    avgTtftMs: ttfts.length ? Math.round(sum(ttfts) / ttfts.length) : undefined,
    cacheRate: input ? sum(recent.flatMap((c) => c.attempts.map((a) => a.cachedTokens))) / input : undefined,
    spendUsd: sum(recent.map(cost)),
  };
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <li className="tile">
      <span className="figure">{value}</span>
      <span className="label">{label}</span>
    </li>
  );
}

// why says in words why the answering attempt ran on its model; an unknown
// reason is shown as the ledger wrote it.
const why = (a: UsageCall) => (a.reason ? attemptReason(a.reason) : a.downgraded ? "cheaper model: daily cap reached" : "first choice");

function Chain({ call }: { call: LiveCall }) {
  return (
    <>
      {call.attempts.map((a, i) => (
        <Fragment key={a.id}>
          {i > 0 && " → "}
          <span className="nowrap">
            {a.provider}/{a.model} <Chip tone={a.outcome === "ok" ? "good" : "bad"}>{attemptOutcome(a.outcome)}</Chip>
          </span>
        </Fragment>
      ))}
    </>
  );
}

// Live follows the ledger as model calls are made: it reads the newest calls
// once, then asks every two seconds for the attempts written past the last id
// it has. The timer stops while the page is hidden, while paused and when the
// tab is left. Only calls from the last 100 are kept.
export function Live({ onTrace }: { onTrace: (runId: string) => void }) {
  const [calls, setCalls] = useState<LiveCall[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [paused, setPaused] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const cursor = useRef<number | null>(null);
  const flying = useRef(false);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => void (alive.current = false);
  }, []);

  const poll = useCallback(async () => {
    if (flying.current) return;
    flying.current = true;
    try {
      const first = cursor.current === null;
      const page = await quietly(() => get("/api/llm/calls", usageCallPage, first ? { pageSize: MAX_CALLS } : { after: cursor.current!, pageSize: MAX_CALLS }));
      if (!alive.current) return;
      const rows = first ? [...page.calls].reverse() : page.calls;
      cursor.current = rows.length ? rows[rows.length - 1].id : (cursor.current ?? 0);
      setCalls((prev) => foldCalls(prev ?? [], rows));
      setError(null);
    } catch (e) {
      if (alive.current) setError(e);
    } finally {
      flying.current = false;
      if (alive.current) setNow(Date.now());
    }
  }, []);
  useEffect(() => void poll(), [poll]);
  useInterval(poll, POLL_MS, !paused);

  const stats = summarize(calls ?? [], now);
  return (
    <section aria-labelledby="usage-live" className="panel">
      <header className="panel-head">
        <h2 id="usage-live">Live routing</h2>
        <button
          type="button"
          aria-pressed={paused}
          onClick={() => {
            setPaused(!paused);
            if (paused) void poll();
          }}
        >
          {paused ? "Resume" : "Pause"}
        </button>
      </header>
      <p className="muted" role="status">
        {paused ? "Paused." : "Updates every 2 seconds while this page is open."} The newest {MAX_CALLS} calls are kept; the figures below cover the last 15 minutes of them.
      </p>
      {error !== null && <ErrorNote error={error} onRetry={() => void poll()} what="the live calls" />}
      <ul className="tiles plain" aria-label="Last 15 minutes">
        <Stat label="Calls" value={String(stats.calls)} />
        <Stat label="Fell back" value={String(stats.fallbacks)} />
        <Stat label="Avg first token" value={stats.avgTtftMs === undefined ? "-" : `${stats.avgTtftMs.toLocaleString("en-US")} ms`} />
        <Stat label="Cache hit" value={stats.cacheRate === undefined ? "-" : `${Math.round(stats.cacheRate * 100)}%`} />
        <Stat label="Spend" value={usd(stats.spendUsd)} />
      </ul>
      {calls?.length === 0 && <Empty>No model call is recorded yet. New calls appear here as they are made.</Empty>}
      {calls !== null && calls.length > 0 && (
        <table className="cards dense">
          <caption className="sr-only">Model calls as they are made, newest first</caption>
          <thead>
            <tr>
              <th scope="col" className="nowrap">Task</th>
              <th scope="col">Route</th>
              <th scope="col">Why this model</th>
              <th scope="col" className="num">First token ms</th>
              <th scope="col" className="num">Latency ms</th>
              <th scope="col" className="num">Cached</th>
              <th scope="col" className="num">Cost</th>
              <th scope="col">
                <span className="sr-only">Trace</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {calls.map((c) => {
              const last = final(c);
              return (
                <tr key={c.callId}>
                  {/* The time sits under its task, so the route keeps its room and the table fits a window beside the browser panel. */}
                  <td data-label="Task" className="nowrap">
                    {c.attempts[0].task}
                    <span className="sub muted">
                      <time dateTime={started(c)}>{formatDateTime(started(c), true)}</time>
                    </span>
                  </td>
                  <td data-label="Route"><Chain call={c} /></td>
                  <td data-label="Why this model">{why(last)}</td>
                  <td data-label="First token ms" className="num">{last.ttftMs === undefined ? "-" : last.ttftMs.toLocaleString("en-US")}</td>
                  <td data-label="Latency ms" className="num">{sum(c.attempts.map((a) => a.latencyMs)).toLocaleString("en-US")}</td>
                  <td data-label="Cached" className="num">{last.inputTokens ? `${Math.round((last.cachedTokens / last.inputTokens) * 100)}%` : "-"}</td>
                  <td data-label="Cost" className="num">{priced(c).length ? `$${cost(c).toFixed(4)}` : "unpriced"}</td>
                  <td>
                    {last.runId && (
                      <button type="button" aria-label={`Open the trace of ${last.runId}`} onClick={() => onTrace(last.runId!)}>
                        Trace
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}
