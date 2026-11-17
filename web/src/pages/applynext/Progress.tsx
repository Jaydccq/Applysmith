import { get } from "../../api/client";
import { boardEntryList } from "../../api/decoders_runs";
import { useInterval, useResource } from "../../hooks";
import { Link } from "../../router";
import { Chip, ErrorNote } from "../../ui/parts";
import { groupRepeats, plainDetail, stageDoing } from "../runs/format";

const POLL_MS = 3000;
// SHOWN is how many runs the panel lists; the Runs page has the rest.
const SHOWN = 3;

// ApplyProgress follows the application runs on the board that are running or
// stopped and waiting for the user. A stage change inside a running entry is
// silent on the event stream, so while one works the panel asks again every
// few seconds; a start, an end or a hand-over to the user arrives as a notice
// and refetches at once. Running runs list first; the heading says which of
// the two it holds.
export function ApplyProgress() {
  const [res, retry] = useResource(
    () => get("/api/runs", boardEntryList, { kind: "apply", state: "working,needs-you,blocked", acknowledged: "false", limit: 10 }),
    [],
    ["runs"],
  );
  const entries = res.data?.entries ?? [];
  const working = entries.filter((e) => e.state === "working");
  const waiting = entries.filter((e) => e.state !== "working");
  useInterval(retry, POLL_MS, working.length > 0);

  if (res.status === "error" && !res.data) return <ErrorNote error={res.error} onRetry={retry} />;
  if (entries.length === 0) return null;
  const title = waiting.length === 0 ? "Running now" : working.length === 0 ? "Waiting runs" : "Running and waiting runs";
  // Repeats of one posting stopped for one reason are one row, as in the Runs inbox.
  const rows = groupRepeats([...working, ...waiting], (e) => e);
  const shown = rows.slice(0, SHOWN);
  const rest = rows.slice(SHOWN).reduce((n, g) => n + g.length, 0);
  return (
    <section aria-labelledby="apply-progress" className="panel">
      <header className="panel-head">
        <h2 id="apply-progress">{title}</h2>
        <Link to="/runs?kind=apply">All application runs</Link>
      </header>
      <ul className="plain stack" aria-live="polite">
        {shown.map(([e, ...more]) => {
          const said = e.state === "working" ? stageDoing(e.stage, true) : plainDetail(e.detail, e.reason)?.line;
          const line = said ? said[0].toUpperCase() + said.slice(1) : "";
          return (
            <li key={e.id} className="row">
              <span className="run-line">
                <Link to={e.link}>{e.title}</Link>
                {more.length > 0 && <span className="muted"> ({more.length + 1} runs)</span>}
                {line && (
                  <span className="item-detail clamp" title={line}>
                    {line}
                  </span>
                )}
              </span>
              <Chip tone={e.state === "working" ? "neutral" : e.state === "blocked" ? "bad" : "warn"}>{e.state === "working" ? "Working" : e.state === "blocked" ? "Blocked" : "Needs you"}</Chip>
            </li>
          );
        })}
      </ul>
      {rest > 0 && (
        <p className="more-line muted">
          {entries.length >= 10 ? "More runs are" : `${rest} more ${rest === 1 ? "run is" : "runs are"}`} on the Runs page.
        </p>
      )}
    </section>
  );
}
