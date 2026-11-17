import { get } from "../../api/client";
import { riskView } from "../../api/decoders_pages";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { Chip, Empty, Panel } from "../../ui/parts";
import type { RiskView } from "../../wire";
import { when } from "../action";

// Challenges per 100 pages opened, one decimal. A source whose scans opened no
// page has no rate.
function rate(challenges: number, visits: number): string {
  return visits === 0 ? "none" : `${((challenges / visits) * 100).toFixed(1)} per 100`;
}

// held names what a signal holds: a named system, or the one site the generic
// flow serves, which a signal there holds alone.
export const held = (s: { system: string; host?: string }) => (s.host ? `${s.host} (other site)` : s.system);

function Body({ v }: { v: RiskView }) {
  return (
    <div className="stack">
      <p className="muted">
        Read-only. A cooldown starts by itself when a detection signal is recorded or a scan is blocked, and ends by itself; change its length under Applications.
      </p>

      <h3>Cooling down now</h3>
      {v.cooldowns.length === 0 ? (
        <Empty>Nothing is cooling down.</Empty>
      ) : (
        <ul>
          {v.cooldowns.map((c) => (
            <li key={`${c.kind}-${c.system}-${c.host ?? ""}`}>
              <strong>{held(c)}</strong> <Chip tone="warn">{c.kind === "apply" ? "account creation" : "scans"}</Chip> until {when(c.until)} ({c.signal.replaceAll("_", " ")})
            </li>
          ))}
        </ul>
      )}

      <h3>Challenge rate, last {v.days} days</h3>
      {v.baseline.length === 0 ? (
        <Empty>No browser scan has run in this period.</Empty>
      ) : (
        <table className="cards">
          <caption className="sr-only">Challenges per browser source</caption>
          <thead>
            <tr>
              <th scope="col">Source</th>
              <th scope="col">Scans</th>
              <th scope="col">Pages opened</th>
              <th scope="col">Stopped by a challenge</th>
              <th scope="col">Stopped by a rate limit</th>
              <th scope="col">Challenge rate</th>
            </tr>
          </thead>
          <tbody>
            {v.baseline.map((b) => (
              <tr key={b.source}>
                <td data-label="Source">{b.source}</td>
                <td data-label="Scans">{b.runs}</td>
                <td data-label="Pages opened">{b.visits}</td>
                <td data-label="Stopped by a challenge">{b.challenges}</td>
                <td data-label="Stopped by a rate limit">{b.rateLimited}</td>
                <td data-label="Challenge rate">{rate(b.challenges, b.visits)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p className="muted">More than a handful of challenges per few hundred pages means something is off. Compare this after a browser or engine change.</p>

      <h3>Recent detection signals</h3>
      {v.signals.length === 0 ? (
        <Empty>No application run has recorded a signal in this period.</Empty>
      ) : (
        <table className="cards">
          <caption className="sr-only">Detection signals recorded by application runs</caption>
          <thead>
            <tr>
              <th scope="col">When</th>
              <th scope="col">System</th>
              <th scope="col">Signal</th>
              <th scope="col">Cooldown until</th>
              <th scope="col">Run</th>
            </tr>
          </thead>
          <tbody>
            {v.signals.map((g) => (
              <tr key={`${g.at}-${g.system}-${g.host ?? ""}-${g.signal}`}>
                <td data-label="When">{when(g.at)}</td>
                <td data-label="System">{held(g)}</td>
                <td data-label="Signal">{g.signal.replaceAll("_", " ")}</td>
                <td data-label="Cooldown until">{g.endsAt ? when(g.endsAt) : "none"}</td>
                <td data-label="Run">{g.runId ? <Link to={`/runs/${encodeURIComponent(g.runId)}`}>{g.runId}</Link> : ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// RiskSection is the read-only view of detection: active cooldowns, the
// challenge rate of each browser source and the recent detection signals.
export function RiskSection() {
  const [res, reload] = useResource(() => get("/api/policy/risk", riskView), [], ["configuration", "scans", "runs"], { keep: "settings.risk" });
  return (
    <Panel title="Detection and cooldowns" id="settings-risk" res={res} retry={reload} what="the detection signals">
      {(v) => <Body v={v} />}
    </Panel>
  );
}
