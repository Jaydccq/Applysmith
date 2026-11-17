import { useState } from "react";
import { get, newRequestId, post, request } from "../../../api/client";
import { portalRegistry } from "../../../api/decoders_discovery";
import { runAccepted, runView } from "../../../api/decoders_runs";
import { useResource } from "../../../hooks";
import { ConfirmDialog } from "../../../ui/Modal";
import { Chip, ErrorNote, Panel } from "../../../ui/parts";
import { useAction, when } from "../../action";
import { useRunLines } from "../runLines";

// Registry shows how many boards the ATS registry holds and seeds it from the
// public lists, the same work as `applysmith registry seed`. A seed is a run: its
// lines stream here and it can be stopped between lists.
export function Registry() {
  const [res, reload] = useResource(() => get("/api/registry", portalRegistry), [], ["scans", "runs"]);
  const [runId, setRunId] = useState<string>();
  const [asking, setAsking] = useState(false);
  const start = useAction();
  const stop = useAction();
  const { lines, final } = useRunLines(runId, reload);
  const seeding = Boolean(runId) && !final;

  const seed = async () => {
    const accepted = await start.run(() => request("POST", "/api/registry/seed", runAccepted, { headers: { "Idempotency-Key": newRequestId() } }));
    if (accepted) setRunId(accepted.runId);
  };
  const cancel = async () => {
    await stop.run(() => post(`/api/runs/${runId}/cancel`, runView));
    setAsking(false);
  };

  return (
    <Panel title="Registry" id="portals-registry" res={res} retry={reload}>
      {(r) => (
        <>
          <p aria-live="polite">
            <strong>{r.total}</strong> boards ({r.active} active, {r.dead} dead, {r.blocked} blocked).{" "}
            <span className="muted">{r.lastSeededAt ? `Last seeded ${when(r.lastSeededAt)}.` : "Never seeded."}</span> {!r.enabled && <Chip tone="warn">registry scan switched off</Chip>}
          </p>
          <div className="actions">
            <button type="button" onClick={() => void seed()} disabled={start.busy || seeding}>
              {r.total === 0 ? "Download the board lists" : "Refresh the registry"}
            </button>
            {seeding && (
              <button type="button" onClick={() => setAsking(true)}>
                Cancel the seed
              </button>
            )}
          </div>
          {r.total === 0 && <p className="muted">A registry scan has nothing to read until the public lists are downloaded.</p>}
          {start.error && <ErrorNote error={start.error} />}
          {stop.error && <ErrorNote error={stop.error} />}
          {runId && (
            <pre className="pre" role="log" aria-label="Seed log" aria-live="off" tabIndex={0}>
              {lines.length === 0 ? (final ? "" : "Waiting for the first list.") : lines.join("\n")}
            </pre>
          )}
          {final && <p role="status">{final.status === "succeeded" ? "Seeding finished." : final.status === "cancelled" ? "Seeding was cancelled." : `Seeding failed: ${final.reason ?? "unknown reason"}`}</p>}
          {asking && (
            <ConfirmDialog
              title="Cancel the seed"
              consequence="The download stops between lists. Boards already added stay in the registry."
              verb="Stop seeding"
              busy={stop.busy}
              onConfirm={() => void cancel()}
              onCancel={() => setAsking(false)}
            />
          )}
        </>
      )}
    </Panel>
  );
}
