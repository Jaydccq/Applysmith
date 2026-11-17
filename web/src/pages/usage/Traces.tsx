import { Fragment, useEffect, useRef, useState } from "react";
import { get } from "../../api/client";
import { usageTraceDetail, usageTraceList } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { Chip, Empty, ErrorNote, Panel, Loading } from "../../ui/parts";
import { SpanCall, SpanRun, type UsageSpan, type UsageTrace, type UsageTraceDetail } from "../../wire";
import { attemptOutcome, attemptReason, when } from "../action";
import { EmptyState } from "../../ui/EmptyState";
import { Waypoints } from "lucide-react";

const spanKey = (s: UsageSpan, i: number) => `${i}-${s.kind}-${s.name}`;

function SpanDetail({ span }: { span: UsageSpan }) {
  const rows: [string, string | number | undefined][] =
    span.kind === SpanRun
      ? [["Kind", "run"], ["Outcome", span.outcome], ["Duration ms", span.durationMs], ["Failure", span.error]]
      : [
          ["Task", span.name],
          ["Provider", span.provider],
          ["Model", span.model],
          ["Attempt", span.attempt],
          ["Outcome", attemptOutcome(span.outcome)],
          ["Why this model", attemptReason(span.reason)],
          ["Starts at ms", span.startMs],
          ["Duration ms", span.durationMs],
          ["Tokens in", span.inputTokens],
          ["Tokens out", span.outputTokens],
          ["Cost", span.costUsd === undefined ? "unpriced" : `$${span.costUsd.toFixed(4)}`],
          ["Error (redacted)", span.error],
        ];
  return (
    <dl className="facts" aria-label="Span detail">
      {rows
        .filter(([, v]) => v !== undefined && v !== "")
        .map(([k, v]) => (
          <Fragment key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </Fragment>
        ))}
    </dl>
  );
}

// Waterfall draws one bar per span on a shared time scale. The outcome and
// the duration are text, so the bar's colour carries nothing alone.
function Waterfall({ detail }: { detail: UsageTraceDetail }) {
  const [picked, setPicked] = useState(0);
  const total = Math.max(1, ...detail.spans.map((s) => s.startMs + s.durationMs));
  const span = detail.spans[picked] ?? detail.spans[0];
  if (!span) return <Empty>This run made no model call.</Empty>;
  return (
    <>
      <ul className="plain waterfall" aria-label="Spans">
        {detail.spans.map((s, i) => (
          <li key={spanKey(s, i)}>
            <button type="button" aria-pressed={i === picked} onClick={() => setPicked(i)}>
              <span>
                {s.kind === SpanRun ? `run: ${s.name}` : s.name}
                {s.kind === SpanCall && s.attempt && s.attempt > 1 ? ` (attempt ${s.attempt})` : ""}
              </span>
              <span className="track" aria-hidden="true">
                <span className={s.outcome === "ok" || s.outcome === "succeeded" ? "" : "failed"} style={{ marginLeft: `${(s.startMs / total) * 100}%`, width: `${Math.max((s.durationMs / total) * 100, 0.5)}%` }} />
              </span>
              <span>
                {s.durationMs} ms{s.outcome ? `, ${s.outcome}` : ""}
              </span>
            </button>
          </li>
        ))}
      </ul>
      <SpanDetail span={span} />
    </>
  );
}

function Trace({ id }: { id: string }) {
  const [res, reload] = useResource(() => get(`/api/llm/traces/${encodeURIComponent(id)}`, usageTraceDetail), [id]);
  if (res.status === "loading") return <Loading />;
  if (res.status === "error") return <ErrorNote error={res.error} onRetry={reload} />;
  return <Waterfall key={id} detail={res.data} />;
}

function label(t: UsageTrace): string {
  return t.title || t.kind || "run";
}

export function Traces({ open, setOpen }: { open: string | null; setOpen: (id: string | null) => void }) {
  const [res, reload] = useResource(() => get("/api/llm/traces", usageTraceList, { limit: 25 }), [], ["runs"]);
  const shown = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (open) shown.current?.scrollIntoView?.({ block: "nearest" });
  }, [open]);
  return (
    <Panel title="Run traces" id="usage-traces" res={res} retry={reload}>
      {({ traces }) =>
        traces.length === 0 ? (
          <EmptyState icon={Waypoints} title="No run has made a model call yet." />
        ) : (
          <>
            <table className="cards">
              <caption className="sr-only">Runs and conversations that made model calls</caption>
              <thead>
                <tr>
                  <th scope="col" className="nowrap">Started</th>
                  <th scope="col">What</th>
                  <th scope="col">Stage</th>
                  <th scope="col" className="num">Calls</th>
                  <th scope="col" className="num">Cost</th>
                  <th scope="col">
                    <span className="sr-only">Open</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {traces.map((t) => (
                  <tr key={t.runId}>
                    <td data-label="Started" className="nowrap">{when(t.startedAt)}</td>
                    <td data-label="What">{label(t)}</td>
                    <td data-label="Stage">
                      {t.status && <Chip tone={t.status === "failed" ? "bad" : "neutral"}>{t.status}</Chip>} {t.stage}
                    </td>
                    <td data-label="Calls" className="num">{t.calls}</td>
                    <td data-label="Cost" className="num">${t.costUsd.toFixed(4)}</td>
                    <td>
                      <button type="button" aria-pressed={open === t.runId} aria-label={`${open === t.runId ? "Hide" : "Show"} the waterfall of ${label(t)} ${t.runId}`} onClick={() => setOpen(open === t.runId ? null : t.runId)}>
                        {open === t.runId ? "Hide waterfall" : "Waterfall"}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div ref={shown}>{open && <Trace id={open} />}</div>
          </>
        )
      }
    </Panel>
  );
}
