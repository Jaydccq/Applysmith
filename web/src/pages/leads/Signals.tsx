import { useCallback, useState } from "react";
import { get, newRequestId, post, request } from "../../api/client";
import { configDocument } from "../../api/decoders_pages";
import { signalList, signalRescoreResult, signalRow as signalRowDecoder, signalStatus } from "../../api/decoders_signals";
import { runAccepted } from "../../api/decoders_runs";
import { useInterval, usePageInRange, useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { Chip, Empty, ErrorNote, Loading, Pager, Panel, UndoNote, humanize } from "../../ui/parts";
import { ConfirmDialog } from "../../ui/Modal";
import { FilterChips } from "../../ui/FilterChips";
import { Button } from "../../ui/Button";
import { Calculator, RefreshCw } from "lucide-react";
import type { SignalRow, SignalStatus } from "../../wire";
import { useAction, when } from "../action";
import { keyOps, savePortals } from "../discovery/portals/edit";
import { SignalDrawer } from "./SignalDrawer";
import { SIGNAL_SOURCES, SIGNAL_STATES, TIERS, tierTone } from "./tiers";

const POLL_MS = 3000;
const PAGE_SIZE = 25;
// PORTALS_RAW is where a signal source's searches or feed URLs are written:
// `queries` under its entry in `sources` of portals.yml.
const PORTALS_RAW = "/discovery?tab=sources&view=raw";

// sourceTitle is the name of a signal source; an id the status does not list
// is shown as it is.
function sourceTitle(lanes: SignalStatus["lanes"], id: string): string {
  return lanes.find((l) => l.source === id)?.title ?? id;
}

function StatusStrip({ s, tier, onSourceChanged }: { s: SignalStatus; tier: string; onSourceChanged: () => void }) {
  const tick = s.lastTick;
  const act = useAction();
  // Before the first collection the source switches are shown open; after
  // that the list stays as the user leaves it.
  const [openSources] = useState(!tick);
  // toggle writes the `enabled` key of one source on portals.yml as it is now,
  // so no other key changes.
  const toggle = async (source: string, on: boolean) => {
    const done = await act.run(async () => {
      const doc = await get("/api/config/portals", configDocument);
      return savePortals(doc, keyOps(doc.values, [[`sources.${source}.enabled`, on]]));
    });
    if (done) onSourceChanged();
  };
  // A source that sends something of the user's (the SEC source sends the
  // profile email) says so before it is switched on.
  const [asking, setAsking] = useState<SignalStatus["lanes"][number]>();
  return (
    <section aria-labelledby="signals-status-h" className="panel">
      <h2 id="signals-status-h">Status</h2>
      <FilterChips
        label="Companies by tier"
        items={[
          ...TIERS.map((t) => ({ label: humanize(t), count: s.tiers[t] ?? 0, to: `/leads?tab=signals&tier=${t}`, current: t === tier })),
          { label: "Review queue", count: s.reviewOpen, to: "/leads?tab=review", current: false },
        ]}
      />
      {s.activeTick ? <p role="status">A collection is running.</p> : null}
      {tick ? (
        <p className="muted">
          Last collection {when(tick.startedAt)}, {tick.status}: {tick.ingested} documents, {tick.events} events, {tick.assessed} assessed, {tick.held} held for review, {tick.tierChanges} tier changes.
          {tick.error ? ` ${tick.error}` : ""}
        </p>
      ) : (
        <p className="muted">No collection has run yet.</p>
      )}
      {tick && tick.alarms.length > 0 && (
        <div role="alert" className="note error">
          <ul className="plain">
            {tick.alarms.map((a) => (
              <li key={a.source}>
                {a.title}: {a.status}. {a.requiredAction ?? ""}
              </li>
            ))}
          </ul>
        </div>
      )}
      <details open={openSources}>
        <summary>Sources ({s.lanes.length})</summary>
        <p className="muted">
          Hacker News: Who is hiring needs nothing more; SEC EDGAR filings needs your profile email. The others also read searches or feed URLs from <code>queries</code> under their entry in <Link to={PORTALS_RAW}>portals.yml</Link>.
        </p>
        <ul className="plain">
          {s.lanes.map((l) => (
            <li key={l.source}>
              <label className="check">
                <input type="checkbox" role="switch" checked={l.enabled} disabled={act.busy} onChange={(e) => (e.target.checked && l.disclosure ? setAsking(l) : void toggle(l.source, e.target.checked))} />
                {l.title}
              </label>{" "}
              <span className="muted">
                {l.lastProduced ? `produced ${when(l.lastProduced)}` : "has not produced yet"}
                {l.enabled && l.lastStatus && l.lastStatus !== "ok" && l.lastStatus !== "disabled" ? `; last collection ${l.lastStatus}` : ""}
              </span>
            </li>
          ))}
        </ul>
        {act.error && <ErrorNote error={act.error} />}
      </details>
      {asking && (
        <ConfirmDialog
          title={`Turn on ${asking.title}`}
          consequence={asking.disclosure}
          verb="Turn on"
          onCancel={() => setAsking(undefined)}
          onConfirm={() => {
            setAsking(undefined);
            void toggle(asking.source, true);
          }}
        />
      )}
    </section>
  );
}

function Row({ r, lanes, onOpen, onChange, onDismissed }: { r: SignalRow; lanes: SignalStatus["lanes"]; onOpen: () => void; onChange: () => void; onDismissed: () => void }) {
  const act = useAction();
  const run = async (verb: "watch" | "dismiss") => {
    const done = await act.run(() => post(`/api/signals/${r.companyId}/${verb}`, signalRowDecoder));
    if (done && verb === "dismiss") onDismissed();
    if (done) onChange();
  };
  return (
    <li>
      <div className="row">
        <span>
          <strong>{r.company}</strong> <Chip tone={tierTone(r.tier)}>{r.tier}</Chip> <Chip>{r.state}</Chip>
          {r.blocked && <Chip tone="warn">blocked</Chip>}
          <br />
          <span className="muted">
            {r.sources.map((id) => sourceTitle(lanes, id)).join(", ") || "no source"}
            {r.newestEvidence ? `. Newest evidence ${when(r.newestEvidence)}` : ""}
          </span>
        </span>
        <span className="actions">
          <button type="button" onClick={onOpen} aria-label={`Evidence for ${r.company}`}>
            Evidence
          </button>
          {r.state !== "watching" && (
            <button type="button" disabled={act.busy} onClick={() => void run("watch")} aria-label={`Watch ${r.company}`}>
              Watch
            </button>
          )}
          {r.state !== "dismissed" && (
            <button type="button" disabled={act.busy} onClick={() => void run("dismiss")} aria-label={`Dismiss ${r.company}`}>
              Dismiss
            </button>
          )}
        </span>
      </div>
      {act.error && <ErrorNote error={act.error} />}
    </li>
  );
}

export function SignalsTab() {
  const [params, setParams] = useSearchParams();
  const tier = params.get("tier") ?? "";
  const source = params.get("source") ?? "";
  const state = params.get("state") ?? "";
  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";
  const open = Number(params.get("signal")) || 0;
  const page = Math.max(1, Number(params.get("page")) || 1);
  const [list, retry] = useResource(() => get("/api/signals", signalList, { tier, source, state, from, to, page, pageSize: PAGE_SIZE }), [tier, source, state, from, to, page], ["scans", "runs"]);
  const toPage = useCallback((p: number) => setParams({ page: p > 1 ? String(p) : undefined }, { replace: true }), [setParams]);
  usePageInRange(list.data, toPage);
  const [status, retryStatus] = useResource(() => get("/api/signals/status", signalStatus), [], ["scans", "runs"]);
  useInterval(retryStatus, POLL_MS, Boolean(status.data?.activeTick));
  const [note, setNote] = useState<{ text: string; runId?: string } | null>(null);
  // dismissed is the latest dismissal with its Undo; n keys the note by the press.
  const [dismissed, setDismissed] = useState<{ r: SignalRow; n: number }>();
  const act = useAction();
  const set = (patch: Record<string, string>) => setParams({ ...patch, page: undefined }, { replace: true });
  const reload = () => {
    retry();
    retryStatus();
  };
  const restore = async (r: SignalRow) => {
    const done = await act.run(() => post(`/api/signals/${r.companyId}/restore`, signalRowDecoder));
    reload();
    return !!done;
  };

  const collect = async () => {
    const done = await act.run(() => request("POST", "/api/signals/ticks", runAccepted, { body: {}, headers: { "Idempotency-Key": newRequestId() } }));
    if (done) setNote({ text: "Collection started. It can take several minutes.", runId: done.runId });
    retryStatus();
  };
  const rescore = async () => {
    const done = await act.run(() => post("/api/signals/rescore", signalRescoreResult));
    if (done) setNote({ text: `Rescored ${done.companies} companies under the current policy.` });
    reload();
  };

  return (
    <>
      {status.status === "loading" && <Loading label="Loading the signal status" lines={2} />}
      {status.data && <StatusStrip s={status.data} tier={tier} onSourceChanged={retryStatus} />}
      {status.status === "error" && <ErrorNote error={status.error} onRetry={retryStatus} />}
      <form className="filters" aria-label="Signal filters" onSubmit={(e) => e.preventDefault()}>
        <label>
          Tier
          <select value={tier} onChange={(e) => set({ tier: e.target.value })}>
            <option value="">All tiers</option>
            {TIERS.map((t) => (
              <option key={t}>{t}</option>
            ))}
          </select>
        </label>
        <label>
          Source
          <select value={source} onChange={(e) => set({ source: e.target.value })}>
            <option value="">All sources</option>
            {SIGNAL_SOURCES.map((s) => (
              <option key={s} value={s}>
                {sourceTitle(status.data?.lanes ?? [], s)}
              </option>
            ))}
          </select>
        </label>
        <label>
          State
          <select value={state} onChange={(e) => set({ state: e.target.value })}>
            <option value="">All states</option>
            {SIGNAL_STATES.map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
        </label>
        <label>
          Newest evidence from
          <input type="date" value={from} onChange={(e) => set({ from: e.target.value })} />
        </label>
        <label>
          to
          <input type="date" value={to} onChange={(e) => set({ to: e.target.value })} />
        </label>
      </form>
      {act.error && <ErrorNote error={act.error} />}
      <div aria-live="polite">
        {note && (
          <p className="note ok">
            {note.text} {note.runId && <Link to={`/runs/${note.runId}`}>Follow the run</Link>}
          </p>
        )}
      </div>
      <div role="status">
        {dismissed && (
          <UndoNote key={dismissed.n} done={`Dismissed ${dismissed.r.company}.`} undone={`${dismissed.r.company} is back as it was.`} onUndo={() => restore(dismissed.r)} />
        )}
      </div>
      <Panel
        title="Companies"
        id="signals-list"
        res={list}
        retry={retry}
        actions={
          <div className="panel-actions">
            <Button size="sm" icon={RefreshCw} disabled={act.busy || Boolean(status.data?.activeTick)} onClick={() => void collect()}>
              Collect now
            </Button>
            <Button size="sm" icon={Calculator} disabled={act.busy} onClick={() => void rescore()}>
              Rescore
            </Button>
          </div>
        }
      >
        {(data) =>
          data.rows.length === 0 ? (
            <Empty>
              {tier || source || state || from || to ? (
                <>
                  No company matches. <Link to="/leads?tab=signals">Clear the filters</Link>
                </>
              ) : (
                <>
                  No hiring signal yet. Switch a source on under Sources above, then press Collect now.
                </>
              )}
            </Empty>
          ) : (
            <>
              <p aria-live="polite" className="count-line">
                {data.total} {data.total === 1 ? "company" : "companies"}
              </p>
              <ul className="entry-list plain">
                {data.rows.map((r) => (
                  <Row key={r.companyId} r={r} lanes={status.data?.lanes ?? []} onChange={reload} onOpen={() => setParams({ signal: String(r.companyId) })} onDismissed={() => setDismissed((d) => ({ r, n: (d?.n ?? 0) + 1 }))} />
                ))}
              </ul>
              <Pager page={data.page} pageSize={data.pageSize} total={data.total} onPage={toPage} />
            </>
          )
        }
      </Panel>
      {open > 0 && <SignalDrawer companyId={open} onClose={() => setParams({ signal: undefined }, { replace: true })} onChange={reload} />}
    </>
  );
}
