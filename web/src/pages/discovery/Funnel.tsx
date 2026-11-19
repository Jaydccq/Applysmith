import { Funnel } from "lucide-react";
import { get } from "../../api/client";
import { funnelWeek, sourceFunnel } from "../../api/decoders_discovery";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { Empty, Panel } from "../../ui/parts";
import type { FunnelLane, FunnelStages } from "../../wire";

// A count with its share of the stage before, whole percent in parentheses;
// the share is left out when nothing reached the stage before.
const share = (n: number, of: number) => (of > 0 ? `${n} (${Math.round((n / of) * 100)}%)` : `${n}`);

function Row({ l }: { l: FunnelLane }) {
  return (
    <tr>
      <th scope="row" data-label="Source">{l.lane}</th>
      <td data-label="Fetched">{l.discovered}</td>
      <td data-label="New">{share(l.inserted, l.discovered)}</td>
      <td data-label="Evaluated">{l.evaluated}</td>
      <td data-label="Failed or gated">{l.failed + l.gateRejected}</td>
      <td data-label="Strong">{share(l.high, l.evaluated)}</td>
      <td data-label="Applied">{share(l.applied, l.evaluated)}</td>
      <td data-label="Answered">{share(l.responded, l.applied)}</td>
      <td data-label="Interview">{l.interview}</td>
    </tr>
  );
}

// FunnelPanel shows, per source, how far its postings got over the last 30 days:
// fetched, new, evaluated, strong (score 4.5 or more), applied, answered, interview.
export function FunnelPanel() {
  const [res, retry] = useResource(() => get("/api/discovery/funnel", sourceFunnel, { days: 30 }), [], ["scans", "runs"]);
  return (
    <Panel title="Source funnel" id="history-funnel" res={res} retry={retry}>
      {(f) =>
        f.lanes.length === 0 ? (
          <Empty>No scan or evaluation in the last {f.days} days.</Empty>
        ) : (
          <>
            <p className="muted">
              Last {f.days} days. Fetched counts each posting a scan read, again whenever a later scan reads it; new is what reached the inbox. An evaluated posting is counted once,
              under the source of its first evaluation; where it stands comes from the tracker. Percentages are of the stage before: new of fetched, strong and applied of evaluated,
              answered of applied.
            </p>
            <table className="cards">
              <caption className="sr-only">Postings by source and how far they got</caption>
              <thead>
                <tr>
                  <th scope="col">Source</th>
                  <th scope="col">Fetched</th>
                  <th scope="col">New</th>
                  <th scope="col">Evaluated</th>
                  <th scope="col">Failed or gated</th>
                  <th scope="col">Strong</th>
                  <th scope="col">Applied</th>
                  <th scope="col">Answered</th>
                  <th scope="col">Interview</th>
                </tr>
              </thead>
              <tbody>
                {f.lanes.map((l) => (
                  <Row key={l.lane} l={l} />
                ))}
                <Row l={f.total} />
              </tbody>
            </table>
          </>
        )
      }
    </Panel>
  );
}

// Each stage with the list of exactly what it counts since the window's first
// day: inbox rows first seen, each posting's newest report, tracker rows moved
// to the stage (Responded: to any reply, a rejection or interview included).
const reached = (status: string) => (since: string) => `/tracker?reached=${status}&since=${since}`;
const STAGES: [keyof FunnelStages, string, (since: string) => string][] = [
  ["found", "Found", (since) => `/discovery?tab=inbox&since=${since}`],
  ["evaluated", "Evaluated", (since) => `/reports?since=${since}`],
  ["applied", "Applied", reached("applied")],
  ["responded", "Responded", reached("responded,interview,offer,rejected")],
  ["interview", "Interview", reached("interview")],
  ["offer", "Offer", reached("offer")],
];

// WeekFunnel: each stage's postings in the last 7 days, the change on the 7
// before and the share of the stage before (left out above 100%).
export function WeekFunnel({ className }: { className?: string }) {
  const [res, retry] = useResource(() => get("/api/discovery/funnel/week", funnelWeek), [], ["tracker", "scans", "runs"], { keep: "funnel.week" });
  return (
    <Panel title="Last 7 days" id="week-funnel" res={res} retry={retry} icon={Funnel} className={className} what="the weekly funnel">
      {(w) =>
        STAGES.every(([k]) => w.current[k] + w.previous[k] === 0) ? (
          <Empty>Nothing moved in these 7 days or the 7 before.</Empty>
        ) : (
          <ul className="tiles plain week-tiles" aria-label="Postings by stage, last 7 days against the 7 before">
            {STAGES.map(([k, label, to], i) => {
              const [n, d, before] = [w.current[k], w.current[k] - w.previous[k], i > 0 ? w.current[STAGES[i - 1][0]] : 0];
              const change = d === 0 ? "same as last week" : `${d > 0 ? "+" : "\u2212"}${Math.abs(d)} vs last week`;
              const rate = before > 0 && n <= before ? `${Math.round((n / before) * 100)}% of ${STAGES[i - 1][1].toLowerCase()}` : "";
              return <li key={k}><Link to={to(w.since)}>
                <span className="label">{label}</span><span className="figure">{n}</span><span className="label">{change}</span>{rate && <span className="label">{rate}</span>}
              </Link></li>;
            })}
          </ul>
        )
      }
    </Panel>
  );
}
