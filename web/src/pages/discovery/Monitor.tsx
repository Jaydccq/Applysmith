import { useState } from "react";
import { get, post } from "../../api/client";
import { inboxPage, inboxPromoteResult, inboxRowDecoder, scanList } from "../../api/decoders_discovery";
import { runView } from "../../api/decoders_runs";
import { useInterval, useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { ConfirmDialog, Modal } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel, Loading, UndoNote } from "../../ui/parts";
import type { InboxRow, ScanRecord } from "../../wire";
import { useAction, when } from "../action";
import { countsLine, droppedLine, elapsed, scanLabel, scanTone, why } from "./format";
import { startedLine, statusLabel } from "./Inbox";
import { ScanWhy } from "./Run";
import { useRunLines } from "./runLines";
import { EmptyState } from "../../ui/EmptyState";
import { Radar } from "lucide-react";

const POLL_MS = 3000;

// A scan that counted postings but wrote no line points at its counts.
function Log({ lines, noRun, counted = false }: { lines: string[]; noRun: boolean; counted?: boolean }) {
  if (noRun) return <p className="muted">This scan has no run, so no log is kept for it. The numbers above update every few seconds.</p>;
  if (counted && lines.length === 0) return <p className="muted">No log line yet. The numbers above are its progress and update every few seconds.</p>;
  return (
    <pre className="pre" role="log" aria-label="Scan log" aria-live="off" tabIndex={0}>
      {lines.length === 0 ? "Waiting for the first line." : lines.join("\n")}
    </pre>
  );
}

function RunningScan({ rec, onChange }: { rec: ScanRecord; onChange: () => void }) {
  const { lines } = useRunLines(rec.runId, onChange);
  const [asking, setAsking] = useState(false);
  const [now, setNow] = useState(Date.now());
  const act = useAction();
  useInterval(() => setNow(Date.now()), 1000, true);
  const cancel = async () => {
    if (rec.runId) await act.run(() => post(`/api/runs/${rec.runId}/cancel`, runView));
    setAsking(false);
    onChange();
  };
  return (
    <li className="panel" aria-label={`${rec.source} scan`}>
      <header className="panel-head">
        <h3>{rec.source}</h3>
        <span className="chips">
          <Chip>running</Chip>
          <span className="muted">{elapsed(rec, now)}</span>
        </span>
      </header>
      <p aria-live="polite">{countsLine(rec)}</p>
      <Log lines={lines} noRun={!rec.runId} counted={rec.counts.discovered > 0} />
      {act.error && <ErrorNote error={act.error} />}
      {rec.runId && (
        <div className="actions">
          <button type="button" onClick={() => setAsking(true)} aria-label={`Cancel the ${rec.source} scan`}>
            Cancel
          </button>
        </div>
      )}
      {asking && (
        <ConfirmDialog
          title={`Cancel the ${rec.source} scan`}
          consequence="The scan stops where it can. What it already added to the pipeline inbox stays there."
          verb="Stop scan"
          busy={act.busy}
          onConfirm={() => void cancel()}
          onCancel={() => setAsking(false)}
        />
      )}
    </li>
  );
}

// StartingScan covers the moment between accepting a run and its scan record.
function StartingScan({ runId, onChange }: { runId: string; onChange: () => void }) {
  const { lines } = useRunLines(runId, onChange);
  return (
    <li className="panel" aria-label="Starting a scan">
      <h3>Starting</h3>
      <Log lines={lines} noRun={false} />
    </li>
  );
}

function NewItems({ scanId, rec, onClose }: { scanId: string; rec?: ScanRecord; onClose: () => void }) {
  const [res, reload] = useResource(() => get("/api/inbox", inboxPage, { runId: scanId, pageSize: 50 }), [scanId], ["scans"]);
  const act = useAction();
  const [note, setNote] = useState("");
  const [gone, setGone] = useState<{ row: InboxRow; n: number } | null>(null);
  // evaluate promotes one row or every waiting one.
  const evaluate = async (rows: InboxRow[]) => {
    setNote("");
    const got = await act.run(() => post("/api/inbox/promote", inboxPromoteResult, { ids: rows.map((r) => r.id) }));
    if (got) setNote(startedLine(rows, got));
    reload();
  };
  const waiting = (res.data?.rows ?? []).filter((r) => r.status === "pending");
  const dismiss = async (r: InboxRow) => {
    setGone(null);
    if (await act.run(() => post(`/api/inbox/${r.id}/dismiss`, inboxRowDecoder))) setGone({ row: r, n: Date.now() });
    reload();
  };
  const restore = async (r: InboxRow) => {
    const ok = await act.run(() => post(`/api/inbox/${r.id}/restore`, inboxRowDecoder));
    reload();
    return Boolean(ok);
  };
  return (
    <Modal title="New items from this scan" variant="drawer" onClose={onClose}>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="the new items" />}
      {act.error && <ErrorNote error={act.error} />}
      {rec && (
        <p className="muted">
          {countsLine(rec)}. {droppedLine(rec)}
        </p>
      )}
      <div aria-live="polite">
        {note && <p className="note ok">{note}</p>}
        {gone && <UndoNote key={gone.n} done={`Dismissed ${gone.row.company}, ${gone.row.role}.`} undone={`${gone.row.company}, ${gone.row.role} is waiting again.`} onUndo={() => restore(gone.row)} />}
      </div>
      {waiting.length > 1 && (
        <p className="actions">
          <button type="button" disabled={act.busy} onClick={() => void evaluate(waiting)}>
            Evaluate the {waiting.length} waiting
          </button>
        </p>
      )}
      {res.data &&
        (res.data.rows.length === 0 ? (
          <Empty>This scan added nothing to the pipeline inbox.</Empty>
        ) : (
          <ul className="entry-list plain">
            {res.data.rows.map((r) => (
              <li key={r.id}>
                <strong>{r.company}</strong>, {r.role} <span className="muted">{r.location}</span> <Chip>{statusLabel(r.status)}</Chip> <span className="muted">rank {r.score}</span>
                {r.status === "pending" && r.notes && (
                  <>
                    <br />
                    <span className="muted">{r.notes}</span>
                  </>
                )}
                {r.status === "pending" && (
                  <span className="actions">
                    <button type="button" disabled={act.busy} onClick={() => void evaluate([r])} aria-label={`Evaluate ${r.company}, ${r.role}`}>
                      Evaluate
                    </button>
                    <button type="button" disabled={act.busy} onClick={() => void dismiss(r)} aria-label={`Dismiss ${r.company}, ${r.role}`}>
                      Dismiss
                    </button>
                  </span>
                )}
              </li>
            ))}
          </ul>
        ))}
      <div className="actions">
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </Modal>
  );
}

export function MonitorTab({ runId }: { runId?: string }) {
  const [params, setParams] = useSearchParams();
  const [running, reloadRunning] = useResource(() => get("/api/scans", scanList, { status: "running", limit: 20 }), [], ["scans", "runs"]);
  const [recent, reloadRecent] = useResource(() => get("/api/scans", scanList, { limit: 10 }), [], ["scans", "runs"]);
  const reload = () => {
    reloadRunning();
    reloadRecent();
  };
  const live = running.data?.scans ?? [];
  useInterval(reload, POLL_MS, live.length > 0);
  const finished = (recent.data?.scans ?? []).filter((s) => s.status !== "running");
  const itemsFor = params.get("items") ?? "";
  const starting = runId && !live.some((s) => s.runId === runId) && !finished.some((s) => s.runId === runId) ? runId : undefined;

  return (
    <>
      <Panel title="Running now" id="monitor-running" res={running} retry={reloadRunning}>
        {() =>
          live.length === 0 && !starting ? (
            <EmptyState
              icon={Radar}
              title="No scan is running."
              action={
                <Link to="/discovery" className="button primary">
                  Start a scan
                </Link>
              }
            >
              Start one on the Run tab, or let a schedule start it.
            </EmptyState>
          ) : (
            <ul className="stack plain">
              {starting && <StartingScan runId={starting} onChange={reload} />}
              {live.map((s) => (
                <RunningScan key={s.id} rec={s} onChange={reload} />
              ))}
            </ul>
          )
        }
      </Panel>
      <Panel title="Finished" id="monitor-finished" res={recent} retry={reloadRecent}>
        {() =>
          finished.length === 0 ? (
            <Empty>No scan has finished yet.</Empty>
          ) : (
            <ul className="entry-list plain">
              {finished.map((s) => (
                <li key={s.id} className="row hold">
                  <span>
                    <strong>{s.source}</strong> <Chip tone={scanTone(s.status)}>{scanLabel(s.status)}</Chip> <span className="muted">{when(s.startedAt)}</span>
                    <br />
                    <span className="muted">{countsLine(s)}</span>
                    {why(s) && (
                      <span className="muted">
                        {" "}
                        (<ScanWhy rec={s} />)
                      </span>
                    )}
                    {droppedLine(s) && <span className="sub muted">{droppedLine(s)}</span>}
                  </span>
                  {s.counts.inserted > 0 && (
                    <button type="button" onClick={() => setParams({ items: s.id }, { replace: true })} aria-label={`New items from the ${s.source} scan`}>
                      New items ({s.counts.inserted})
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )
        }
      </Panel>
      {itemsFor && <NewItems scanId={itemsFor} rec={finished.find((s) => s.id === itemsFor)} onClose={() => setParams({ items: undefined }, { replace: true })} />}
    </>
  );
}
