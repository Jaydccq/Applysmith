import { useEffect, useRef, useState } from "react";
import { ApiError, get, patch, post, withQuery } from "../api/client";
import { candidacy, statusChangeResult } from "../api/decoders";
import { applyBatchView, applyList } from "../api/decoders_apply";
import { applyBatchAccepted, runAccepted, runView } from "../api/decoders_runs";
import { asApiError, useInterval, useResource, useTopicsSettled } from "../hooks";
import { Link, useSearchParams } from "../router";
import { Chip, ErrorNote, ExternalLink, Loading, OfflineScoresNote, Pager, ScoreBadge, SelectionBar, Truncate, UndoNote, ageLabel } from "../ui/parts";
import { BoardStageQueued, SourceCheckFailed, type ApplyBatchView, type ApplyItem, type ApplyLane, type ApplySkippedRow } from "../wire";
import { DocumentsDrawer } from "./Documents";
import { ApplyProgress } from "./applynext/Progress";
import { ReportDrawer } from "./applynext/ReportDrawer";
import { SkipListDrawer } from "./applynext/SkipListDrawer";
import { RowStatus, StateCounts } from "./applynext/Status";
import { showTab } from "./applynext/showTab";
import { SAMPLE, sampleHost } from "./applynext/words";
import { plainDetail, stateLabel, tone } from "./runs/format";
import { ChosenSubmitDialog, LaneSubmitDialog, MarkAppliedDialog, RowSubmitDialog } from "./applynext/SubmitDialogs";
import { ConfirmDialog } from "../ui/Modal";
import { PageHeader } from "../ui/PageHeader";
import { AppWindow, Ban, CircleCheck, FileText, ListX, Send, WandSparkles } from "lucide-react";
import { ActionMenu } from "../ui/ActionMenu";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/EmptyState";
import { ClearChip } from "../ui/FilterChips";

// Each lane says why its rows are there; an empty lane says so in the same
// line instead of a box of its own. The order is the page's Order choice.
const LANES: Record<string, { title: string; why: string; empty: string }> = {
  "apply-now": { title: "Apply now", why: "Evaluated postings that score 4.0 or higher.", empty: "No evaluated posting scores 4.0 or higher." },
  selective: { title: "Selective", why: "Scores from 3.5 to below 4.0. Read the report before you apply.", empty: "No evaluated posting scores from 3.5 to below 4.0." },
  "marked-applied": { title: "Marked applied", why: "Postings you marked applied yourself. Nothing here is run.", empty: "You have not marked any posting applied." },
  awaiting: { title: "Awaiting receipt", why: "Applysmith submitted these; the employer's receipt has not been seen yet.", empty: "No automated submission is waiting for its receipt." },
};

// Each lane pages on its own, under the server's page.<lane>, so one table's
// pager leaves the others where they are.
const pageKey = (lane: string) => `page.${lane}`;

// Only these lanes hold rows the chain can run.
const RUNNABLE = new Set(["apply-now", "selective"]);
const MAX_BATCH = 100;
const FILL_HINT = "Fills the form in the apply window and stops before Submit.";
const isSample = (i: ApplyItem) => sampleHost(i.postingHost);
// A scan announces "runs" on each evaluation phase; the list reads once they settle.
const RUNS_SETTLE_MS = 1000;

type Pending =
  | { kind: "submit"; item: ApplyItem }
  | { kind: "kept"; item: ApplyItem }
  | { kind: "lane"; lane: ApplyLane }
  | { kind: "mark"; item: ApplyItem }
  | { kind: "chosen" }
  | { kind: "confirm"; item: ApplyItem };

// A row can be ticked for a run of chosen rows when a run could open it.
const choosable = (i: ApplyItem) => RUNNABLE.has(i.lane) && Boolean(i.postingUrl) && !isSample(i);

interface Actions {
  disabled: boolean;
  chosen: (item: ApplyItem) => boolean;
  choose: (item: ApplyItem, on: boolean) => void;
  fill: (item: ApplyItem) => void;
  ask: (p: Pending) => void;
  fillLane: (lane: ApplyLane) => void;
  jump: (item: ApplyItem) => void;
  openReport: (n: number) => void;
  openDocuments: (item: ApplyItem) => void;
  skip: (item: ApplyItem) => void;
}

// A run that is working on a row owns it; the row is skipped once the run ends.
const working = (i: ApplyItem) => i.state === "queued" || i.state === "running";

// RowActions keeps one action in view and the rest behind "More", so a row's
// data keeps its width: a runnable row fills in the form, everything else is in
// the menu; a row that cannot run shows its two actions as they are. A label
// that ends in an ellipsis asks before it acts.
function RowActions({ item, act }: { item: ApplyItem; act: Actions }) {
  const who = `${item.company} ${item.role}`;
  const runnable = RUNNABLE.has(item.lane);
  const noUrl = !item.postingUrl || isSample(item);
  const kept = Boolean(item.keptTab && item.runId);
  const jump = { label: "Jump to tab", ariaLabel: `Jump to tab: ${who}`, icon: AppWindow, disabled: act.disabled || !kept, title: kept ? undefined : "No tab is kept for this row.", onSelect: () => act.jump(item) };
  const documents = { label: "Documents", ariaLabel: `Documents: ${who}`, icon: FileText, onSelect: () => act.openDocuments(item) };
  // A tab a run kept for the user is the step that waits, so it leads the row;
  // a filled form waiting at Submit there is submitted after the same dialog
  // as Autofill and submit.
  const jumpButton = kept && (
    <button type="button" className="small" disabled={jump.disabled} aria-label={jump.ariaLabel} onClick={jump.onSelect}>
      <AppWindow />
      {jump.label}
    </button>
  );
  const submitKept = kept && item.formWaiting && (
    <button type="button" className="small primary" disabled={act.disabled} aria-label={`Submit this form: ${who}`} onClick={() => act.ask({ kind: "kept", item })}>
      <Send />
      Submit this form&hellip;
    </button>
  );
  if (!runnable) {
    return (
      <div className="row-actions-wrap">
        {submitKept}
        {jumpButton}
        {item.lane === "awaiting" && (
          <button type="button" className="small" disabled={act.disabled} aria-label={`Confirm submitted: ${who}`} onClick={() => act.ask({ kind: "confirm", item })}>
            <CircleCheck />
            Confirm submitted&hellip;
          </button>
        )}
        <button type="button" className="small" aria-label={documents.ariaLabel} onClick={documents.onSelect}>
          {documents.label}
        </button>
      </div>
    );
  }
  const reason = !item.postingUrl ? "The row has no posting address." : isSample(item) ? SAMPLE : undefined;
  return (
    <div className="row-actions-wrap">
      {submitKept}
      {jumpButton || (
        <button type="button" className="small" disabled={act.disabled || noUrl} title={reason ?? FILL_HINT} aria-label={`Autofill only: ${who}`} onClick={() => act.fill(item)}>
          <WandSparkles />
          Autofill only
        </button>
      )}
      <ActionMenu
        label={`More actions: ${who}`}
        items={[
          ...(kept ? [{ label: "Autofill only", ariaLabel: `Autofill only: ${who}`, icon: WandSparkles, disabled: act.disabled || noUrl, title: reason, onSelect: () => act.fill(item) }] : []),
          { label: "Autofill and submit…", ariaLabel: `Autofill and submit: ${who}`, icon: Send, disabled: act.disabled || noUrl, title: reason, onSelect: () => act.ask({ kind: "submit", item }) },
          { label: "Mark applied…", ariaLabel: `Mark applied: ${who}`, icon: CircleCheck, disabled: act.disabled, onSelect: () => act.ask({ kind: "mark", item }) },
          ...(kept ? [] : [jump]),
          documents,
          { label: "Skip this posting", ariaLabel: `Skip this posting: ${who}`, icon: Ban, disabled: act.disabled || working(item), title: working(item) ? "A run is working on this row." : "Marks the posting skipped; it leaves this list, and Undo brings it back.", onSelect: () => act.skip(item) },
        ]}
      />
    </div>
  );
}

function countBy(items: ApplyItem[]): Record<string, number> {
  const out: Record<string, number> = {};
  for (const i of items) out[i.state] = (out[i.state] ?? 0) + 1;
  return out;
}

// pickNext is the row the page suggests first: the top row, in the page's
// order, of the runnable lanes that a run can start on now (a posting address,
// not on the skip list, never tried). A row the list would leave out is never
// idle: the server marks it skipped.
function pickNext(lanes: ApplyLane[]): ApplyItem | undefined {
  const ready = lanes.flatMap((l) => (RUNNABLE.has(l.lane) ? l.items.filter((i) => i.postingUrl && !i.skipEntry && i.state === "idle") : []));
  return ready.find((i) => !isSample(i)) ?? ready[0];
}

// NextUp names the row to apply to next, why it leads, and what each of its
// two buttons does. Submitting goes through the same confirmation as a row.
function NextUp({ item, act, newest }: { item: ApplyItem; act: Actions; newest: boolean }) {
  return (
    <section aria-labelledby="next-up" className="panel next-up">
      <div className="next-up-body">
        <div className="next-up-what">
          <h2 id="next-up">Next up</h2>
          <p className="next-up-name">
            <strong>{item.company}</strong> <ScoreBadge score={item.score} />
          </p>
          <p className="next-up-role">
            <Truncate text={item.role} />
          </p>
          <p className="muted next-up-links">
            <span>
              {newest ? "Newest" : "Best score"} in {LANES[item.lane]?.title ?? item.lane}.
            </span>
            {item.postingUrl && <ExternalLink href={item.postingUrl}>{item.postingHost ?? "Posting"}</ExternalLink>}
            {item.reportNumber !== undefined && (
              <button type="button" className="small ghost" aria-label={`Read report ${item.reportNumber} for next up: ${item.company}`} onClick={() => act.openReport(item.reportNumber!)}>
                Report {item.reportNumber}
              </button>
            )}
            <button type="button" className="small ghost" disabled={act.disabled || working(item)} aria-label={`Skip this posting for next up: ${item.company}`} onClick={() => act.skip(item)}>
              Skip this posting
            </button>
          </p>
        </div>
        <div className="panel-actions">
          <Starts item={item} act={act} label="Next up" />
        </div>
      </div>
      {isSample(item) ? (
        <p className="muted next-up-help">
          {SAMPLE} Try autofill on the sample form from the <Link to="/setup?step=orientation#demo-tutorial">demo tutorial</Link> instead.
        </p>
      ) : (
        <p className="muted next-up-help">Autofill only stops before Submit, so you can check the form. Autofill and submit asks you first.</p>
      )}
    </section>
  );
}

// Starts are a row's two starts as buttons; `first` runs before either.
function Starts({ item, act, label, first }: { item: ApplyItem; act: Actions; label?: string; first?: () => void }) {
  const named = (what: string) => (label ? `${label}, ${what}: ${item.company} ${item.role}` : undefined);
  const off = act.disabled || isSample(item);
  return (
    <>
      <Button variant="primary" icon={WandSparkles} disabled={off} title={isSample(item) ? SAMPLE : FILL_HINT} aria-label={named("autofill only")} onClick={() => (first?.(), act.fill(item))}>
        Autofill only
      </Button>
      <Button icon={Send} disabled={off} aria-label={named("autofill and submit")} onClick={() => (first?.(), act.ask({ kind: "submit", item }))}>
        Autofill and submit&hellip;
      </Button>
    </>
  );
}

function Lane({ lane, act, page, pageSize, onPage }: { lane: ApplyLane; act: Actions; page: number; pageSize: number; onPage: (page: number) => void }) {
  const def = LANES[lane.lane] ?? { title: lane.lane, why: "", empty: "Nothing here." };
  const headingId = `lane-${lane.lane}`;
  const runnable = RUNNABLE.has(lane.lane) && lane.total > 0 && !lane.items.every(isSample);
  const empty = lane.items.length === 0;
  const picks = RUNNABLE.has(lane.lane);
  return (
    <section aria-labelledby={headingId} className={empty ? "panel lane-empty" : "panel"} id={headingId + "-section"}>
      <header className="panel-head">
        <div className="panel-title">
          <h2 id={headingId}>{def.title}</h2>
          <Chip>{lane.total}</Chip>
        </div>
        {runnable && (
          <div className="panel-actions">
            <Button size="sm" icon={WandSparkles} disabled={act.disabled} title="Fills each form in turn and stops before Submit." onClick={() => act.fillLane(lane)}>
              Autofill all
            </Button>
            <Button size="sm" icon={Send} disabled={act.disabled} title="Fills each form and presses its final Submit; asks you first." onClick={() => act.ask({ kind: "lane", lane })}>
              Submit all&hellip;
            </Button>
          </div>
        )}
      </header>
      <p className="lane-why muted">
        {empty ? def.empty : def.why}
        {RUNNABLE.has(lane.lane) && lane.items.some(isSample) && " Sample postings offer no autofill: their sites do not exist."}
      </p>
      <StateCounts states={countBy(lane.items)} />
      {!empty && (
        <table className="cards dense">
          <caption className="sr-only">{def.title}</caption>
          <thead>
            <tr>
              {picks && (
                <th scope="col">
                  <span className="sr-only">Choose</span>
                </th>
              )}
              <th scope="col" className="fill">
                Posting
              </th>
              <th scope="col" className="num">
                Score
              </th>
              <th scope="col" className="nowrap">
                Age
              </th>
              <th scope="col" className="state-col">
                State
              </th>
              <th scope="col">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {lane.items.map((i) => (
              <tr key={i.id}>
                {picks && (
                  <td data-label="Choose">
                    <input
                      type="checkbox"
                      aria-label={`Choose ${i.company} ${i.role}`}
                      checked={act.chosen(i)}
                      disabled={!choosable(i)}
                      onChange={(e) => act.choose(i, e.target.checked)}
                    />
                  </td>
                )}
                {/* The company and its role share a column, so a window beside
                    the browser panel still shows the role in words. */}
                <td data-label="Posting" className="key fill">
                  <strong className="truncate" title={i.company}>
                    {i.company}
                  </strong>
                  <Truncate text={i.role} />
                  {(i.postingUrl || i.postingHost || i.reportNumber !== undefined || i.sourceCheck || i.skipEntry) && (
                    <span className="sub row-links">
                      {i.postingUrl ? <ExternalLink href={i.postingUrl}>{i.postingHost ?? "Posting"}</ExternalLink> : i.postingHost && <span className="muted">{i.postingHost}</span>}
                      {i.reportNumber !== undefined && (
                        <button type="button" className="small ghost" aria-label={`Open report ${i.reportNumber}: ${i.company}`} onClick={() => act.openReport(i.reportNumber!)}>
                          Report {i.reportNumber}
                        </button>
                      )}
                      {i.sourceCheck && <SourceChip check={i.sourceCheck} />}
                      {i.skipEntry && <Chip tone="warn">Skip list</Chip>}
                    </span>
                  )}
                </td>
                <td data-label="Score" className="num">
                  <ScoreBadge score={i.score} />
                </td>
                <td data-label="Age" className="nowrap muted">
                  {ageLabel(i.date)}
                </td>
                <td data-label="State" className="state-col">
                  <RowStatus item={i} />
                </td>
                <td data-label="Actions" className="row-actions">
                  <RowActions item={i} act={act} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {lane.total > pageSize && <Pager page={page} pageSize={pageSize} total={lane.total} onPage={onPage} />}
    </section>
  );
}

// SourceChip marks a row whose posting's last check did not see it live.
function SourceChip({ check }: { check: string }) {
  const failed = check === SourceCheckFailed;
  const why = failed
    ? "The last check of this posting failed: the page showed another posting or no posting. Open it before you apply."
    : "No check has seen this posting live: its text was pasted or its page did not read as the posting. Open it before you apply.";
  return (
    <Chip tone={failed ? "bad" : "warn"} title={why}>
      Source unverified
    </Chip>
  );
}

type Started = { text: string; runId: string; batchId?: string; skipped: ApplySkippedRow[]; ahead?: number };

// batchLine is how far a batch has got: rows done of all, by outcome, and the
// row it works on now.
function batchLine(b: ApplyBatchView): string {
  const done = b.rows.filter((r) => r.state === "ended");
  const kinds = ["submitted", "filled", "stopped"].map((k) => [done.filter((r) => r.outcome?.kind === k).length, k] as const);
  const notRun = done.length - kinds.reduce((n, [c]) => n + c, 0);
  const parts = [...kinds, [notRun, "not run"] as const].filter(([n]) => n > 0).map(([n, k]) => `${n} ${k}`);
  const now = b.rows.find((r) => r.state === "started");
  return `${done.length} of ${b.rows.length} done${parts.length ? ` (${parts.join(", ")})` : ""}.${now ? ` Now: ${now.company}.` : ""}`;
}

// queueLine is what a start that waits for the apply window says first.
const queueLine = (ahead: number) => `Queued (${ahead} ahead): it starts when the apply window is free.`;

// StartedNote follows its run: once the run ends or stops for you, the note
// says how, so a run that failed at once does not read as still filling. A
// start that waits for the apply window says its place and can be cancelled.
// The keyboard comes to its run link with the note, which is scrolled into
// view and whose press may be gone (a start of ticked rows clears the ticks),
// rather than falling to a neighbour such as another lane's start. A batch
// says how far it has got while its list is open; a run that stopped for a
// setting links that setting.
function StartedNote({ started, onCancel, onDismiss }: { started: Started; onCancel: () => void; onDismiss: () => void }) {
  const [res] = useResource(() => get(`/api/runs/${started.runId}`, runView), [started.runId], ["runs"]);
  const batchId = started.batchId;
  const [batch, reloadBatch] = useResource(() => (batchId ? get(`/api/apply/batches/${encodeURIComponent(batchId)}`, applyBatchView) : Promise.resolve(undefined)), [batchId], ["runs"]);
  useInterval(reloadBatch, 3000, batch.data?.state === "open");
  const note = useRef<HTMLDivElement>(null);
  useEffect(() => note.current?.querySelector("a")?.focus(), [started.runId]);
  // The start's own answer says it waits only until the run answers for
  // itself; from then on its entry alone says so, and a run with no entry, or
  // the answer kept from the run before, does not.
  const view = res.data?.runId === started.runId ? res.data : undefined;
  const live = view?.entry;
  const waiting = view ? live?.state === "working" && live.stage === BoardStageQueued : Boolean(started.ahead);
  const e = live?.state === "working" ? undefined : live;
  const said = e && plainDetail(e.detail, e.reason)?.line;
  const ended = e && [`${e.title}: ${stateLabel(e)}`, said && said[0].toUpperCase() + said.slice(1)].filter(Boolean).join(". ").replace(/\.?$/, ".");
  const shade = e ? { good: "ok", bad: "error", warn: "warn", neutral: "info" }[tone(e)] : "ok";
  return (
    <div role="status" className={`note ${shade} started-note`} ref={note}>
      <div>
        <p>
          {ended || started.text}{" "}
          <Link to={`/runs/${started.runId}`}>{e ? "Open the run" : "Follow the run"}</Link>
          {e?.fix && (
            <>
              {" "}
              <Link to={e.fix.to}>{e.fix.label}</Link>
            </>
          )}
        </p>
        {batch.data && batch.data.rows.length > 0 && <p>{batchLine(batch.data)}</p>}
        {waiting && (
          <p>
            {live?.detail || queueLine(started.ahead ?? 1)}{" "}
            <button type="button" className="small" onClick={onCancel}>
              Cancel
            </button>
          </p>
        )}
        {started.skipped.length > 0 && (
          <>
            <p>Left out ({started.skipped.length}):</p>
            <ul>
              {started.skipped.map((s, i) => (
                <li key={i}>
                  {s.company}: {s.reason}
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
      <button type="button" className="small ghost" onClick={onDismiss}>
        Dismiss
      </button>
    </div>
  );
}

export default function ApplyNext() {
  const [params, setParams] = useSearchParams();
  const pageOf = (l: string) => Math.max(1, Number(params.get(pageKey(l))) || 1);
  const pages = Object.fromEntries(Object.keys(LANES).flatMap((l) => (pageOf(l) > 1 ? [[pageKey(l), pageOf(l)]] : [])));
  // lane keeps only these lanes (comma-separated), as a Home count of them opens.
  const lane = (params.get("lane") ?? "").split(",").filter((l) => Object.hasOwn(LANES, l)).join(",");
  // Best score first unless the user asks for the newest reports first.
  const sort = params.get("sort") === "newest" ? "newest" : "";
  // A run starting, stopping or ending is a "runs" notice, not a tracker one,
  // and it moves a row's state.
  const [res, retry] = useResource(() => get("/api/apply/list", applyList, { ...pages, lane, sort }), [JSON.stringify(pages), lane, sort], ["tracker"]);
  useTopicsSettled(["runs"], RUNS_SETTLE_MS, retry);
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [started, setStarted] = useState<Started | null>(null);
  const [skipOpen, setSkipOpen] = useState(false);
  const [notice, setNotice] = useState("");
  const [skipped, setSkipped] = useState<{ item: ApplyItem; n: number; version: number }>();
  // The rows ticked for a run of chosen rows, in the order they were ticked.
  const [chosen, setChosen] = useState<Map<number, ApplyItem>>(new Map());
  const [retryToday, setRetryToday] = useState(false);
  const feedback = useRef<HTMLDivElement>(null);
  const reportOpen = Number(params.get("report")) || 0;
  const docsOpen = Number(params.get("documents")) || 0;
  const items = res.data?.lanes.flatMap((l) => l.items) ?? [];
  const docsItem = items.find((i) => i.id === docsOpen);
  // The reports of the rows on the page, in the order the lanes list them, for
  // stepping through them in the drawer.
  const reportNumbers = [...new Set(items.flatMap((i) => (i.reportNumber === undefined ? [] : [i.reportNumber])))];
  // A report opened from a runnable row ends on that row's two starts.
  const reportItem = items.find((i) => i.reportNumber === reportOpen && RUNNABLE.has(i.lane) && i.postingUrl);
  const largest = res.data ? Math.max(0, ...res.data.lanes.map((l) => l.total)) : 0;
  const goPage = (l: string, p: number, opts?: { replace: boolean }) => setParams({ [pageKey(l)]: p > 1 ? String(p) : undefined }, opts);
  // A lane left empty on a later page, as when its last rows were skipped,
  // moves to its last page that still has rows.
  useEffect(() => {
    for (const l of res.data?.lanes ?? []) if (l.items.length === 0 && pageOf(l.lane) > 1) goPage(l.lane, Math.ceil(l.total / res.data!.pageSize), { replace: true });
  }, [res.data]);
  // The next row is suggested from the top of the runnable lanes only.
  const next = res.data && [...RUNNABLE].every((l) => pageOf(l) === 1) ? pickNext(res.data.lanes) : undefined;

  // A ticked row on the page that can no longer be chosen (it moved to another
  // lane) leaves the selection, so the bar never counts or sends it.
  useEffect(() => {
    const gone = items.filter((i) => chosen.has(i.id) && !choosable(i));
    if (gone.length > 0) setChosen((was) => new Map([...was].filter(([id]) => !gone.some((i) => i.id === id))));
  }, [res.data]);
  const unchoose = (item: ApplyItem) => setChosen((was) => new Map([...was].filter(([id]) => id !== item.id)));

  // A start or a refusal is answered at the top of the page, so a press made
  // far down a lane brings that answer into view.
  useEffect(() => {
    if (failure || started || notice || skipped) feedback.current?.scrollIntoView?.({ block: "nearest" });
  }, [failure, started, notice, skipped]);

  // run performs one start or write; a refusal is shown and the page stays as it was.
  async function run(work: () => Promise<void>) {
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      await work();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
      setPending(null);
    }
  }

  // allowLetters records the user's override of the document floor for each
  // row's report before the run starts, so a form's cover-letter slot can be
  // filled; a refused override starts no run.
  const allowLetters = async (rows: ApplyItem[]) => {
    for (const r of rows) await post(`/api/candidacies/${r.id}/documents-override`, candidacy);
  };

  const startRow = (item: ApplyItem, submit: boolean, allowLetter = false) =>
    run(async () => {
      await allowLetters(allowLetter ? [item] : []);
      const acc = await post("/api/apply/runs", runAccepted, { url: item.postingUrl, company: item.company, role: item.role, submit, confirmed: submit });
      const text = submit ? `Submitting the application to ${item.company}.` : `Filling the application to ${item.company}; it stops before Submit.`;
      setStarted({ text, runId: acc.runId, skipped: [], ahead: acc.ahead });
    });

  // A batch says how many rows it took and from where; retry is sent only when
  // the user ticked "Retry today's stops". The overrides the user ticked are
  // recorded before the batch starts, so its letters see them.
  const startBatch = (body: Record<string, unknown>, from: string, submit: boolean, retry: boolean, letters: ApplyItem[] = []) =>
    run(async () => {
      await allowLetters(letters);
      const acc = await post("/api/apply/batches", applyBatchAccepted, { ...body, submit, confirmed: submit, ...(retry ? { retryToday: true } : {}) });
      const rows = `${acc.rows} ${acc.rows === 1 ? "posting" : "postings"} ${from}`;
      setStarted({ text: submit ? `Applying to ${rows}.` : `Filling ${rows}, one at a time; nothing is submitted.`, runId: acc.runId, batchId: acc.batchId, skipped: acc.skipped, ahead: acc.ahead });
      if ("candidacies" in body) setChosen(new Map());
    });

  const startLane = (lane: ApplyLane, submit: boolean, retry = false, letters: ApplyItem[] = []) =>
    startBatch({ lanes: [lane.lane], limit: Math.min(lane.total, MAX_BATCH) }, `from ${LANES[lane.lane]?.title ?? lane.lane}`, submit, retry, letters);

  const startChosen = (submit: boolean, retry = false, letters: ApplyItem[] = []) =>
    startBatch({ candidacies: [...chosen.keys()] }, "you chose", submit, retry, letters);

  const cancelStart = (runId: string) =>
    run(async () => {
      await post(`/api/runs/${encodeURIComponent(runId)}/cancel`, runView);
      setNotice("The queued start is cancelled; nothing ran.");
    });

  const confirmSent = (item: ApplyItem) =>
    run(async () => {
      await post(`/api/candidacies/${item.id}/confirm-submitted`, candidacy);
      setNotice(`${item.company} is recorded as applied by Applysmith.`);
      retry();
    });

  // A skipped row leaves the list; Undo makes it evaluated again, where it was.
  // Both presses quote the row's version (the list's own, then the one the skip
  // made), so a row that moved since the list was drawn (applied by a run, a
  // tab or the extension) is refused with the server's message and the list
  // reloads, instead of being skipped or "restored" over the newer fact.
  const skipRow = (item: ApplyItem) =>
    run(async () => {
      try {
        const r = await patch(`/api/candidacies/${item.id}`, statusChangeResult, { status: "skipped", expectedVersion: item.version });
        setSkipped(r.changed ? { item, n: (skipped?.n ?? 0) + 1, version: r.entry.version } : undefined);
        if (r.changed) unchoose(item);
      } finally {
        retry();
      }
    });
  const unskip = async (s: NonNullable<typeof skipped>) => {
    try {
      await patch(`/api/candidacies/${s.item.id}`, statusChangeResult, { status: "evaluated", expectedVersion: s.version });
      retry();
      return true;
    } catch (e) {
      setFailure(asApiError(e));
      retry();
      return false;
    }
  };

  const ask = (p: Pending | null) => {
    setRetryToday(false);
    setPending(p);
  };

  // Mark applied quotes the list's version of the row too, so a row that moved
  // since is refused and the list reloads.
  const markApplied = (item: ApplyItem) =>
    run(async () => {
      try {
        await patch(`/api/candidacies/${item.id}`, statusChangeResult, { status: "applied", expectedVersion: item.version });
        unchoose(item);
      } finally {
        retry();
      }
    });

  const submitKept = (item: ApplyItem) =>
    run(async () => {
      const acc = await post(`/api/apply/runs/${encodeURIComponent(item.runId!)}/submit`, runAccepted, { confirmed: true });
      setStarted({ text: `Submitting the filled form to ${item.company} from its kept tab, after its checks run again.`, runId: acc.runId, skipped: [] });
    });

  const jump = (item: ApplyItem) =>
    run(async () => {
      await showTab(item.runId!);
      setNotice(`The tab kept for ${item.company} is in front.`);
    });

  const act: Actions = {
    disabled: busy,
    chosen: (i) => chosen.has(i.id),
    choose: (i, on) =>
      setChosen((was) => {
        const next = new Map(was);
        if (on && next.size < MAX_BATCH) next.set(i.id, i);
        else next.delete(i.id);
        return next;
      }),
    fill: (i) => void startRow(i, false),
    ask,
    fillLane: (l) => void startLane(l, false),
    jump: (i) => void jump(i),
    openReport: (n) => setParams({ report: String(n) }),
    openDocuments: (i) => setParams({ documents: String(i.id) }),
    skip: (i) => void skipRow(i),
  };
  const laneIn = (lane: ApplyLane) => res.data?.lanes.find((l) => l.lane === lane.lane) ?? lane;

  return (
    <>
      <PageHeader
        path="/apply-next"
        actions={
          <>
            <label>
              Order
              <select value={sort} onChange={(e) => setParams({ sort: e.target.value || undefined, ...Object.fromEntries(Object.keys(pages).map((k) => [k, undefined])) }, { replace: true })}>
                <option value="">Best score</option>
                <option value="newest">Newest</option>
              </select>
            </label>
            <Button icon={ListX} onClick={() => setSkipOpen(true)}>
              Edit the batch skip list
            </Button>
          </>
        }
      >
        Evaluated postings to apply to. Nothing is submitted until you confirm it.
      </PageHeader>
      <OfflineScoresNote />
      <div ref={feedback}>
        {failure && <ErrorNote error={failure} />}
        <p role="status" className={notice ? "note ok" : "sr-only"}>
          {notice}
        </p>
        <div role="status">
          {skipped && <UndoNote key={skipped.n} done={`${skipped.item.company} is skipped and has left the list.`} undone={`${skipped.item.company} is back on the list.`} onUndo={() => unskip(skipped)} />}
        </div>
        {started && <StartedNote started={started} onCancel={() => void cancelStart(started.runId)} onDismiss={() => setStarted(null)} />}
      </div>
      {res.status === "loading" && <Loading label="Loading the apply list" />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={retry} />}
      {lane && <ClearChip label={`Only ${lane.split(",").map((l) => LANES[l].title).join(" and ")}`} to={withQuery("/apply-next", Object.fromEntries([...params].filter(([k]) => k !== "lane" && !k.startsWith("page."))))} />}
      {next && <NextUp item={next} act={act} newest={sort === "newest"} />}
      <ApplyProgress />
      {res.data && largest === 0 && !lane && (
        <EmptyState icon={Send} title="Nothing is ready to apply to." action={<Link to="/discovery">Find postings in Discovery</Link>}>
          A posting appears here once it is evaluated and scores 3.5 or higher.
        </EmptyState>
      )}
      {res.data && (largest > 0 || lane) && (
        <>
          <StateCounts states={res.data.counts.states} />
          {res.data.lanes.some((l) => RUNNABLE.has(l.lane)) && !res.data.lanes.some((l) => RUNNABLE.has(l.lane) && l.total > 0) && (
            <p className="note">
              No posting is ready to apply to. <Link to="/discovery">Find postings in Discovery</Link>
            </p>
          )}
          <div className="lanes">
            {res.data.lanes.map((l) => (
              <Lane key={l.lane} lane={l} act={act} page={pageOf(l.lane)} pageSize={res.data!.pageSize} onPage={(p) => goPage(l.lane, p)} />
            ))}
          </div>
          <SelectionBar count={chosen.size}>
            <Button size="sm" icon={WandSparkles} disabled={busy} title="Fills each chosen form in turn and stops before Submit." onClick={() => void startChosen(false)}>
              Autofill selected
            </Button>
            <Button size="sm" icon={Send} disabled={busy} title="Fills each chosen form and presses its final Submit; asks you first." onClick={() => ask({ kind: "chosen" })}>
              Submit selected&hellip;
            </Button>
            <button type="button" className="small ghost" onClick={() => setChosen(new Map())}>
              Clear
            </button>
          </SelectionBar>
        </>
      )}
      {skipOpen && <SkipListDrawer onClose={() => setSkipOpen(false)} onSaved={retry} />}
      {reportOpen > 0 && (
        <ReportDrawer
          number={reportOpen}
          onClose={() => setParams({ report: undefined })}
          reports={reportNumbers}
          onStep={(n) => setParams({ report: String(n) }, { replace: true })}
          actions={reportItem && <Starts item={reportItem} act={act} first={() => setParams({ report: undefined })} />}
        />
      )}
      {docsOpen > 0 && (
        <DocumentsDrawer candidacyId={docsOpen} label={docsItem ? `${docsItem.company}, ${docsItem.role}` : `row ${docsOpen}`} onClose={() => setParams({ documents: undefined })} />
      )}
      {pending?.kind === "submit" && (
        <RowSubmitDialog item={pending.item} busy={busy} onConfirm={(letter) => void startRow(pending.item, true, letter)} onCancel={() => setPending(null)} />
      )}
      {/* A filled form already holds its letter, so its confirmation offers no override. */}
      {pending?.kind === "kept" && (
        <RowSubmitDialog item={{ ...pending.item, letterOffer: undefined }} busy={busy} onConfirm={() => void submitKept(pending.item)} onCancel={() => setPending(null)} />
      )}
      {pending?.kind === "mark" && <MarkAppliedDialog item={pending.item} busy={busy} onConfirm={() => void markApplied(pending.item)} onCancel={() => setPending(null)} />}
      {pending?.kind === "lane" && (
        <LaneSubmitDialog
          title={LANES[pending.lane.lane]?.title ?? pending.lane.lane}
          count={Math.min(pending.lane.total, MAX_BATCH)}
          items={laneIn(pending.lane).items}
          hidden={laneIn(pending.lane).total > laneIn(pending.lane).items.length}
          busy={busy}
          retry={retryToday}
          onRetry={setRetryToday}
          onConfirm={(letters) => void startLane(pending.lane, true, retryToday, letters)}
          onCancel={() => setPending(null)}
        />
      )}
      {pending?.kind === "chosen" && (
        <ChosenSubmitDialog
          items={[...chosen.values()]}
          busy={busy}
          retry={retryToday}
          onRetry={setRetryToday}
          onConfirm={(letters) => void startChosen(true, retryToday, letters)}
          onCancel={() => setPending(null)}
        />
      )}
      {pending?.kind === "confirm" && (
        <ConfirmDialog
          title={`Confirm the application to ${pending.item.company} went through`}
          consequence={`Use this once you have seen that the employer received it. The tracker row for ${pending.item.role} at ${pending.item.company} becomes applied, as applied by Applysmith, and leaves Awaiting receipt. Nothing is sent to the employer.`}
          verb="Confirm submitted"
          busy={busy}
          onConfirm={() => void confirmSent(pending.item)}
          onCancel={() => setPending(null)}
        />
      )}
    </>
  );
}
