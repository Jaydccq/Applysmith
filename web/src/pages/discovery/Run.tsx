import { useId, useState, type FormEvent } from "react";
import { get, newRequestId, request } from "../../api/client";
import { runAccepted } from "../../api/decoders_runs";
import { portalRegistry, scanSourceList } from "../../api/decoders_discovery";
import { useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { useDemo } from "../../ui/DemoBanner";
import { Modal } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel, humanize } from "../../ui/parts";
import type { PortalRegistry, ScanRecord, ScanRequest, ScanSource } from "../../wire";
import { useAction, when } from "../action";
import { SCOPES, countsLine, readinessTone, scanLabel, scanTone, why } from "./format";
import { deletePreset, loadPresets, savePreset } from "./presets";
import { EMPTY, toRequest, type Draft } from "./request";
import { useRunLines } from "./runLines";

// whyBlocked is the reason the Start control of a source is disabled, or "".
function whyBlocked(s: ScanSource): string {
  if (s.running) return `A ${s.title} scan is already running.`;
  if (s.readiness === "needs key") return `${s.title} needs its API key.`;
  if (s.readiness === "needs browser") return s.reason ?? `${s.title} needs the managed browser.`;
  if (s.readiness === "needs boards") return s.reason ?? `${s.title} has no board to read.`;
  if (s.readiness === "off") return s.reason ?? `${s.title} is switched off.`;
  if (s.readiness === "needs searches") return s.reason ?? `${s.title} has no saved search.`;
  if (s.readiness === "needs companies") return s.reason ?? "No company is tracked yet.";
  return "";
}

// PORTALS_LINK is what a source that reads portals.yml needs added there.
const PORTALS_LINK: Record<string, string> = {
  "needs searches": "Save a search on the Sources and portals tab",
  "needs companies": "Add a company on the Sources and portals tab",
};

// SeedBoards downloads the public lists of company boards into the registry,
// the seed of the Sources and portals tab, from the row whose source has
// nothing to read without them. The row turns ready once the run added boards.
function SeedBoards({ onSeeded }: { onSeeded: () => void }) {
  const [runId, setRunId] = useState<string>();
  const start = useAction();
  const { final } = useRunLines(runId, onSeeded);
  const seeding = Boolean(runId) && !final;
  const seed = async () => {
    const accepted = await start.run(() => request("POST", "/api/registry/seed", runAccepted, { headers: { "Idempotency-Key": newRequestId() } }));
    if (accepted) setRunId(accepted.runId);
  };
  return (
    <div className="actions">
      <button type="button" onClick={() => void seed()} disabled={start.busy || seeding}>
        {seeding ? "Downloading the board lists…" : "Download the board lists"}
      </button>
      {final && (
        <span role="status" className="muted">
          {final.status === "succeeded" ? `Downloaded: ${final.outcome ?? "done"}.` : final.status === "cancelled" ? "The download was cancelled." : `The download failed: ${final.reason ?? "unknown reason"}`}
        </span>
      )}
      {start.error && <ErrorNote error={start.error} />}
    </div>
  );
}

// SourceRow is one line per source: what it is, how its last run went and what
// it needs, whether it is ready, and its controls. Seventeen sources read as a
// list, not a wall of cards.
function SourceRow({ s, onStart, onOptions, onSeeded, busy }: { s: ScanSource; onStart: () => void; onOptions: () => void; onSeeded: () => void; busy: boolean }) {
  const reason = whyBlocked(s);
  const reasonId = useId();
  return (
    <li className="source-row" aria-label={s.title}>
      <div className="source-name">
        <h3>{s.title}</h3>
        <span className="muted">
          {humanize(s.kind)}
          {s.needs && (s.needs === "key" ? " \u00b7 API key" : " \u00b7 site sign-in")}
        </span>
      </div>
      <div className="source-state">
        {s.lastScan ? (
          <p>
            Last run <Chip tone={scanTone(s.lastScan.status)}>{scanLabel(s.lastScan.status)}</Chip> <span className="muted">{when(s.lastScan.startedAt)}</span>
            <br />
            <span className="muted">
              {countsLine(s.lastScan)}
              {/* The row's own lines below already say a sign-in or a cooldown. */}
              {why(s.lastScan) && !["needs-sign-in", "cooldown"].includes(s.lastScan.reason ?? "") && (
                <>
                  {" "}
                  (<ScanWhy rec={s.lastScan} />)
                </>
              )}
            </span>
          </p>
        ) : (
          <p className="muted">Never run.</p>
        )}
        {s.cooldownUntil && <p className="note compact">Cooling down after a blocked run until {when(s.cooldownUntil)}. Tick "Ignore the cooldown" in the options to run it anyway.</p>}
        {(s.readiness === "needs sign-in" || s.readiness === "sign-in unchecked") && (
          <p>
            {s.reason && <span className="muted">{s.reason} </span>}
            <Link to="/browser">{s.readiness === "needs sign-in" ? "Sign in on the Browser page" : "Check sign-in on the Browser page"}</Link>
          </p>
        )}
        {reason && (
          <p id={reasonId} className="muted">
            {reason}
            {s.readiness === "needs key" && (
              <>
                {" "}
                <Link to="/settings#settings-scan">Add the key in Settings</Link>
              </>
            )}
            {s.readiness === "off" && (
              <>
                {" "}
                <Link to="/discovery?tab=sources&view=raw">Switch it on in portals.yml</Link>
              </>
            )}
            {PORTALS_LINK[s.readiness] && (
              <>
                {" "}
                <Link to="/discovery?tab=sources">{PORTALS_LINK[s.readiness]}</Link>
              </>
            )}
          </p>
        )}
        {s.readiness === "needs boards" && <SeedBoards onSeeded={onSeeded} />}
      </div>
      <Chip tone={readinessTone(s.readiness)}>{s.readiness}</Chip>
      <div className="actions">
        <button type="button" onClick={onStart} disabled={Boolean(reason) || busy} aria-label={`Start ${s.title}`} aria-describedby={reason ? reasonId : undefined}>
          Start
        </button>
        <button type="button" onClick={onOptions} disabled={s.running || (Boolean(reason) && s.readiness !== "needs searches")} aria-label={`Options for ${s.title}`}>
          Options
        </button>
      </div>
    </li>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label>
      {label}
      {children}
    </label>
  );
}

function OptionsForm({ source, onClose, onStarted }: { source: ScanSource; onClose: () => void; onStarted: (runId: string) => void }) {
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [presets, setPresets] = useState(() => loadPresets(source.name));
  const [presetName, setPresetName] = useState("");
  const [problem, setProblem] = useState("");
  const act = useAction();
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }));
  const text = (k: keyof Draft) => ({ value: draft[k] as string, onChange: (e: { target: { value: string } }) => set(k, e.target.value as never) });
  const browser = source.kind === "browser";

  const start = async (e: FormEvent) => {
    e.preventDefault();
    const built = toRequest(source.name, draft);
    if (built.error) return setProblem(built.error);
    setProblem("");
    const accepted = await act.run(() => request("POST", "/api/scans", runAccepted, { body: built.body as ScanRequest, headers: { "Idempotency-Key": newRequestId() } }));
    if (accepted) onStarted(accepted.runId);
  };
  const save = () => {
    const name = presetName.trim();
    if (!name) return setProblem("Name the preset before saving it.");
    setProblem("");
    setPresets(savePreset(source.name, name, draft));
    setPresetName("");
  };

  return (
    <Modal title={`Scan ${source.title}`} onClose={onClose}>
      <form onSubmit={start} noValidate className="form-grid" aria-label={`Scan ${source.title}`}>
        {presets.length > 0 && (
          <Field label="Preset">
            <select
              value=""
              onChange={(e) => {
                const p = presets.find((x) => x.name === e.target.value);
                if (p) setDraft(p.draft);
              }}
            >
              <option value="">Load a saved preset</option>
              {presets.map((p) => (
                <option key={p.name} value={p.name}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
        )}
        <Field label="Search text">
          <input type="text" {...text("search")} />
        </Field>
        <Field label="Title">
          <input type="text" {...text("title")} />
        </Field>
        <Field label="City">
          <input type="text" {...text("city")} />
        </Field>
        <Field label="State">
          <input type="text" {...text("state")} />
        </Field>
        <Field label="List URL">
          <input type="url" {...text("url")} />
        </Field>
        <Field label="Location scope">
          <select {...text("scope")}>
            <option value="">Default</option>
            {SCOPES.map(([v, name]) => (
              <option key={v} value={v}>
                {name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Posted within (6h, 3d, 2w or any)">
          <input type="text" {...text("postedWithin")} />
        </Field>
        <Field label="Results per search (0 to 1000)">
          <input type="number" min={0} max={1000} {...text("limit")} />
        </Field>
        <Field label="Rank by">
          <select {...text("sort")}>
            <option value="">Default</option>
            <option value="score">Score</option>
            <option value="recency">Recency</option>
          </select>
        </Field>
        <Field label="Evaluation mode">
          <select {...text("mode")}>
            <option value="">Default</option>
            <option value="auto">Quick screen first</option>
            <option value="screen">Screen only</option>
            <option value="deep">Deep</option>
          </select>
        </Field>
        <Field label="Send the top N to evaluation (0 to 500)">
          <input type="number" min={0} max={500} {...text("topN")} />
        </Field>
        <Field label="Time budget in minutes">
          <input type="number" min={0} max={360} {...text("budgetMinutes")} />
        </Field>
        {browser && (
          <Field label="Result pages (0 to 50)">
            <input type="number" min={0} max={50} {...text("pages")} />
          </Field>
        )}
        {source.name === "speedrun" && (
          <>
            <Field label="Craft">
              <input type="text" {...text("fn")} />
            </Field>
            <Field label="Seniority">
              <input type="text" {...text("seniority")} />
            </Field>
            <Field label="Employment type">
              <input type="text" {...text("employment")} />
            </Field>
            <label className="check">
              <input type="checkbox" checked={draft.remote} onChange={(e) => set("remote", e.target.checked)} />
              Remote only
            </label>
          </>
        )}
        {source.name === "linkedin" && (
          <label className="check">
            <input type="checkbox" checked={draft.easyApplyOnly} onChange={(e) => set("easyApplyOnly", e.target.checked)} />
            Keep only Easy Apply postings
          </label>
        )}
        <label className="check">
          <input type="checkbox" checked={draft.retryKnown} onChange={(e) => set("retryKnown", e.target.checked)} />
          Look at postings seen before again
        </label>
        {(browser || source.cooldownUntil) && (
          <label className="check">
            <input type="checkbox" checked={draft.ignoreCooldown} onChange={(e) => set("ignoreCooldown", e.target.checked)} />
            Ignore the cooldown
          </label>
        )}
        {browser && (
          <label className="check">
            <input type="checkbox" checked={draft.force} onChange={(e) => set("force", e.target.checked)} />
            Run even when the computer is under pressure
          </label>
        )}
        <fieldset>
          <legend>Save these options</legend>
          <div className="row">
            <input type="text" aria-label="Preset name" value={presetName} onChange={(e) => setPresetName(e.target.value)} />
            <button type="button" onClick={save}>
              Save preset
            </button>
          </div>
          {presets.length > 0 && (
            <ul className="plain">
              {presets.map((p) => (
                <li key={p.name} className="row">
                  {p.name}
                  <button type="button" className="danger-outline" aria-label={`Delete preset ${p.name}`} onClick={() => setPresets(deletePreset(source.name, p.name))}>
                    Delete
                  </button>
                </li>
              ))}
            </ul>
          )}
        </fieldset>
        {problem && (
          <p role="alert" className="note error">
            {problem}
          </p>
        )}
        {act.error && <ErrorNote error={act.error} />}
        <div className="actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={act.busy}>
            Start scan
          </button>
        </div>
      </form>
    </Modal>
  );
}

// latestRuns names the three sources that ran most recently. The rows keep the
// catalog's order, so a row does not move when its scan starts, and the ones
// that ran can sit below a screen of rows that never did.
function latestRuns(sources: readonly ScanSource[]): string | null {
  const ran = sources.flatMap((s) => (s.lastScan ? [{ title: s.title, scan: s.lastScan }] : [])).sort((a, b) => b.scan.startedAt.localeCompare(a.scan.startedAt));
  if (ran.length === 0) return null;
  return `Latest runs: ${ran.slice(0, 3).map((r) => `${r.title} ${scanLabel(r.scan.status).toLowerCase()} ${when(r.scan.startedAt)}`).join(", ")}.`;
}

// NoBoards says what the registry's source needed when a scan found the
// registry empty, and links the download.
function NoBoards() {
  return (
    <>
      nothing to read until its list of boards is downloaded: <Link to="/discovery?tab=sources">download it on the Sources and portals tab</Link>
    </>
  );
}

// ScanWhy is why a scan did not complete (why). A registry scan skipped for
// want of boards (discovery.ReasonRegistryEmpty) and a scan that needs a
// sign-in are put right with a link, so their reasons carry it.
export function ScanWhy({ rec }: { rec: ScanRecord }) {
  if (rec.reason === "registry-empty") return <NoBoards />;
  const text = why(rec);
  return (
    <>
      {text}
      {rec.reason === "needs-sign-in" && (
        <>
          {/[.!?]$/.test(text) ? " " : ". "}
          <Link to="/browser">Sign in on the Browser page</Link>
        </>
      )}
    </>
  );
}

// FirstRun is the line above the list while no source has run. A person who has
// run nothing cannot tell the ready rows apart, so it names the registry's
// source, which reads a great many companies' boards and needs nothing from
// them, by its board count. It says nothing more when the registry scan is off
// or has no board, whose row then offers the download itself.
function FirstRun({ sources, registry }: { sources: readonly ScanSource[]; registry?: PortalRegistry }) {
  const boards = sources.find((s) => s.name === "registry");
  const idle = "No source has run yet.";
  if (!boards || !registry?.enabled || boards.readiness !== "ready" || boards.needs || registry.active === 0) return <>{idle}</>;
  return <>{`${idle} ${boards.title} has ${new Intl.NumberFormat("en-US").format(registry.active)} company boards to read and needs no key or sign-in, so start there.`}</>;
}

export function RunTab({ onStarted }: { onStarted: (runId: string) => void }) {
  const [params, setParams] = useSearchParams();
  const [res, retry] = useResource(() => get("/api/scan-sources", scanSourceList), [], ["scans", "runs"]);
  const [registry] = useResource(() => get("/api/registry", portalRegistry), [], ["scans", "runs"]);
  const act = useAction();
  const optionsFor = params.get("source") ?? "";
  const chosen = res.data?.sources.find((s) => s.name === optionsFor);
  const demo = useDemo();

  const quickStart = async (s: ScanSource) => {
    const accepted = await act.run(() => request("POST", "/api/scans", runAccepted, { body: { source: s.name }, headers: { "Idempotency-Key": newRequestId() } }));
    if (accepted) onStarted(accepted.runId);
  };

  return (
    <Panel title="Sources" id="discovery-sources" res={res} retry={retry}>
      {(data) => (
        <>
          {act.error && <ErrorNote error={act.error} />}
          {demo && (
            <p className="note info">
              In the demo a scan reads these real job sites and adds what it finds to the persona's <Link to="/discovery?tab=inbox">Inbox</Link>,
              beside the sample postings already there. A scan never applies.
            </p>
          )}
          {data.sources.length === 0 ? (
            <Empty>No source is available.</Empty>
          ) : (
            <>
              <p className="muted sources-latest">{latestRuns(data.sources) ?? <FirstRun sources={data.sources} registry={registry.data} />}</p>
              <ul className="source-list plain">
                {data.sources.map((s) => (
                  <SourceRow key={s.name} s={s} busy={act.busy} onStart={() => void quickStart(s)} onOptions={() => setParams({ source: s.name }, { replace: true })} onSeeded={retry} />
                ))}
              </ul>
            </>
          )}
          {chosen && <OptionsForm key={chosen.name} source={chosen} onClose={() => setParams({ source: undefined }, { replace: true })} onStarted={(id) => { setParams({ source: undefined }, { replace: true }); onStarted(id); }} />}
        </>
      )}
    </Panel>
  );
}
