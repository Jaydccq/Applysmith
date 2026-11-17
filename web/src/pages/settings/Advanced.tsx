import { useState } from "react";
import { fetchBlob, get, postCheck } from "../../api/client";
import { health } from "../../api/decode";
import { doctorReport } from "../../api/decoders_setup";
import { logLines } from "../../api/decoders_system";
import { useResource } from "../../hooks";
import { Chip, Empty, ErrorNote, Loading } from "../../ui/parts";
import type { DoctorResult } from "../../wire";
import { useAction } from "../action";
import { DoctorFix } from "../setup/Doctor";
import { CommandLine } from "./CommandLine";

const STATUS_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { pass: "good", warn: "warn", fail: "bad", skipped: "neutral" };

function Versions() {
  const [res, reload] = useResource(() => get("/api/health", health), [], [], { keep: "settings.health" });
  return (
    <section aria-labelledby="settings-versions">
      <h3 id="settings-versions">Versions</h3>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="the versions" />}
      {res.data && (
        <dl className="facts">
          <dt>Application</dt>
          <dd>
            {res.data.version} <span className="muted">build {res.data.buildId}</span>
          </dd>
          <dt>API version</dt>
          <dd>{res.data.apiVersion}</dd>
          <dt>Database schema</dt>
          <dd>{res.data.schemaVersion ?? "unknown"}</dd>
        </dl>
      )}
    </section>
  );
}

function Doctor() {
  const run = useAction();
  const [results, setResults] = useState<DoctorResult[] | null>(null);
  const press = async () => {
    const done = await run.run(() => postCheck("/api/setup/doctor", doctorReport));
    if (done) setResults(done.results);
  };
  return (
    <section aria-labelledby="settings-doctor">
      <h3 id="settings-doctor">Doctor</h3>
      <p className="muted">Checks the workspace, the browser, the models and the integrations. It makes no network call.</p>
      <div className="actions">
        <button type="button" disabled={run.busy} onClick={() => void press()}>
          Run doctor
        </button>
      </div>
      {run.error && <ErrorNote error={run.error} />}
      {results && (
        <ul className="plain stack" aria-live="polite">
          {results.map((r) => (
            <li key={r.id}>
              <Chip tone={STATUS_TONE[r.status] ?? "neutral"}>{r.status}</Chip> <strong>{r.id}</strong>
              {r.required && <span className="muted"> required</span>} {r.summary}
              {" "}
              <DoctorFix r={r} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Logs() {
  const load = useAction();
  const [lines, setLines] = useState<string[] | null>(null);
  const press = async () => {
    const done = await load.run(() => get("/api/system/logs", logLines));
    if (done) setLines(done.lines);
  };
  return (
    <section aria-labelledby="settings-logs">
      <h3 id="settings-logs">Log</h3>
      <p className="muted">The last 200 lines of the server log. Secret values are replaced by [redacted] before they leave the server.</p>
      <div className="actions">
        <button type="button" disabled={load.busy} onClick={() => void press()}>
          {lines ? "Refresh log" : "Show log"}
        </button>
      </div>
      {load.error && <ErrorNote error={load.error} />}
      {lines && (lines.length === 0 ? <Empty>The log is empty.</Empty> : <pre className="log-lines" tabIndex={0} aria-label="Server log">{lines.join("\n")}</pre>)}
    </section>
  );
}

// Diagnostics saves the export the server builds: versions, health, the doctor
// results and the redacted log tail, with no secret in it. The route needs the
// page's credentials, so the file is fetched and saved from a blob address.
function Diagnostics() {
  const exp = useAction();
  const [saved, setSaved] = useState(false);
  const press = async () => {
    setSaved(false);
    const blob = await exp.run(() => fetchBlob("/api/system/diagnostics"));
    if (!blob) return;
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "applysmith-diagnostics.json";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
    setSaved(true);
  };
  return (
    <section aria-labelledby="settings-diagnostics">
      <h3 id="settings-diagnostics">Diagnostics</h3>
      <p className="muted">A file to attach to a problem report. It holds versions, health, the doctor results and the redacted log, and no secret.</p>
      <div className="actions">
        <button type="button" disabled={exp.busy} onClick={() => void press()}>
          Export diagnostics
        </button>
      </div>
      {exp.error && <ErrorNote error={exp.error} />}
      <div role="status">{saved && <p className="note ok">applysmith-diagnostics.json was saved.</p>}</div>
    </section>
  );
}

export function AdvancedSection() {
  return (
    <section aria-labelledby="settings-advanced" className="panel">
      <header className="panel-head">
        <h2 id="settings-advanced">Advanced</h2>
      </header>
      <Versions />
      <CommandLine />
      <Doctor />
      <Logs />
      <Diagnostics />
    </section>
  );
}
