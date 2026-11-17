import { Compass, Inbox as InboxIcon } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { get, post } from "../../api/client";
import { inboxPage, inboxPromoteResult, inboxRowDecoder } from "../../api/decoders_discovery";
import { usePageInRange, useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { Button } from "../../ui/Button";
import { EmptyState } from "../../ui/EmptyState";
import { Empty, ErrorNote, ExternalLink, ModelsLinked, Pager, Panel, ScoreBadge, SelectionBar, Truncate, UndoNote, formatDate } from "../../ui/parts";
import { ClearChip, FilterChips, isDay, sinceWords } from "../../ui/FilterChips";
import type { InboxPromoteResult, InboxRow } from "../../wire";
import { useAction } from "../action";
import { EvaluationNote } from "./EvaluationNote";

const PAGE_SIZE = 25;

const VIEWS = [
  { status: "pending", label: "Waiting" },
  { status: "queued", label: "Evaluating" },
  { status: "evaluated", label: "Evaluated" },
  { status: "dismissed", label: "Dismissed" },
  { status: "expired", label: "Expired" },
] as const;

// statusLabel is a row status in the inbox's own words (pending is Waiting).
export const statusLabel = (status: string) => VIEWS.find((v) => v.status === status)?.label ?? status;

// RANK_HELP says what the Rank column is: a local sort order, not the 0 to 5
// evaluation score (see rank.go in the discovery package).
const RANK_HELP =
  "Rank is a local sort order, not the evaluation score: title matches, a location in your scope, early-career wording and a recent posting add to it; senior wording and an old posting subtract. A higher rank is shown first. Point at a number for its reasons.";

const name = (r: InboxRow) => `${r.company}, ${r.role}`;
const names = (rows: InboxRow[]) => (rows.length === 1 ? name(rows[0]) : `${rows.length} postings`);

// startedLine says what a promotion did. The gate's rule is kept nowhere
// else, so each row it kept waiting is named with that rule.
export function startedLine(rows: InboxRow[], got: InboxPromoteResult): string {
  const kept = rows.filter((r) => got.rejected[String(r.id)]).map((r) => `${name(r)} (${got.rejected[String(r.id)]})`);
  return `Started ${got.queued.length} evaluation${got.queued.length === 1 ? "" : "s"}.${kept.length ? ` Not started: ${kept.join("; ")}.` : ""}`;
}

// Finished links the report an evaluated row's run wrote, with its score;
// the list carries both, so no row asks for its run.
function Finished({ row }: { row: InboxRow }) {
  return (
    <>
      {row.reportNumber ? (
        <>
          {row.reportScore !== undefined && <ScoreBadge score={row.reportScore} />} <Link to={`/reports?id=${row.reportNumber}`}>Report {row.reportNumber}</Link>
        </>
      ) : (
        <span className="muted">{row.outcome ?? "evaluated"}</span>
      )}{" "}
      {row.jobId && <Link to={`/runs/${row.jobId}`}>Evaluation run</Link>}
    </>
  );
}

function Actions({ row, busy, onEvaluate, onDismiss, onRestore }: { row: InboxRow; busy: boolean; onEvaluate: () => void; onDismiss: () => void; onRestore: () => void }) {
  if (row.status === "pending")
    return (
      <span className="row-actions-wrap">
        <Button variant="primary" size="sm" disabled={busy} onClick={onEvaluate} aria-label={`Evaluate ${name(row)}`}>
          Evaluate
        </Button>
        <Button size="sm" disabled={busy} onClick={onDismiss} aria-label={`Dismiss ${name(row)}`}>
          Dismiss
        </Button>
        <ExternalLink href={row.url}>Open posting<span className="sr-only">: {name(row)}</span></ExternalLink>
      </span>
    );
  return (
    <span className="row-actions-wrap">
      {row.status === "dismissed" && (
        <Button size="sm" disabled={busy} onClick={onRestore} aria-label={`Restore ${name(row)}`}>
          Restore
        </Button>
      )}
      {row.status === "queued" && row.jobId && <Link to={`/runs/${row.jobId}`}>Open run</Link>}
      {row.status === "evaluated" && <Finished row={row} />}
      <ExternalLink href={row.url}>Open posting<span className="sr-only">: {name(row)}</span></ExternalLink>
    </span>
  );
}

// runOf finds the run a promoted row was queued under, so its note can follow
// it. A failed lookup only means the note cannot follow the run.
async function runOf(id: number): Promise<string | undefined> {
  try {
    const queued = await get("/api/inbox", inboxPage, { status: "queued", pageSize: 100 });
    return queued.rows.find((x) => x.id === id)?.jobId;
  } catch {
    return undefined;
  }
}

export function InboxTab() {
  const [params, setParams] = useSearchParams();
  // since keeps the rows first seen on or after a day, whatever became of
  // them: what Home's and the funnel's found counts count, so it lists every
  // status unless one is picked.
  const since = isDay(params.get("since") ?? "") ? (params.get("since") as string) : "";
  const status = VIEWS.find((v) => v.status === params.get("status"))?.status ?? (since ? "" : "pending");
  const pageNo = Math.max(1, Number(params.get("page")) || 1);
  const q = params.get("q") ?? "";
  const [res, retry] = useResource(() => get("/api/inbox", inboxPage, { status, since, page: pageNo, pageSize: PAGE_SIZE, q }), [status, since, pageNo, q], ["inbox", "scans", "runs"]);
  const toPage = useCallback((p: number) => setParams({ page: p > 1 ? String(p) : undefined }, { replace: true }), [setParams]);
  usePageInRange(res.data, toPage);
  const act = useAction();
  // note is what the last press led to.
  const [note, setNote] = useState<{ refused: string } | { started: string; jobId?: string } | { dismissed: InboxRow[]; n: number } | { bulk: string } | null>(null);
  // picked holds the ticked rows; it clears when the list shows other rows.
  const [picked, setPicked] = useState<Set<number>>(new Set());
  useEffect(() => setPicked(new Set()), [status, since, pageNo, q]);
  // The search asks the server 300 ms after typing stops.
  const [text, setText] = useState(q);
  useEffect(() => setText(q), [q]);
  useEffect(() => {
    if (text.trim() === q) return;
    const t = setTimeout(() => setParams({ q: text.trim() || undefined, page: undefined }, { replace: true }), 300);
    return () => clearTimeout(t);
  }, [text, q, setParams]);
  const viewLink = (st: string, day = since) => `/discovery?${new URLSearchParams({ tab: "inbox", ...(st !== (day ? "" : "pending") ? { status: st } : {}), ...(day ? { since: day } : {}), ...(q ? { q } : {}) }).toString()}`;
  const done = () => {
    setPicked(new Set());
    retry();
  };

  // Evaluate goes through promote, the one route that moves a row from
  // waiting to evaluating, so the row settles to evaluated when the run ends.
  // A batch is one request: a scan evaluates only its top rows, so the rest
  // wait here for the user or the scheduled inbox evaluation.
  const evaluate = async (rows: InboxRow[]) => {
    setNote(null);
    const got = await act.run(() => post("/api/inbox/promote", inboxPromoteResult, { ids: rows.map((r) => r.id) }));
    if (!got) return;
    const refused = Object.keys(got.rejected).length;
    if (rows.length === 1 && refused) setNote({ refused: `${name(rows[0])} was not started: ${got.rejected[String(rows[0].id)]}.` });
    else if (rows.length === 1) setNote({ started: name(rows[0]), jobId: await runOf(rows[0].id) });
    else setNote({ bulk: startedLine(rows, got) });
    done();
  };
  // Each dismissal is its own request; Undo restores the ones that went through.
  const dismiss = async (rows: InboxRow[]) => {
    setNote(null);
    const gone: InboxRow[] = [];
    await act.run(async () => {
      for (const r of rows) gone.push(await post(`/api/inbox/${r.id}/dismiss`, inboxRowDecoder).then(() => r));
    });
    if (gone.length) setNote({ dismissed: gone, n: Date.now() });
    done();
  };
  // Restore puts dismissed rows back to waiting: the undo of a dismissal,
  // whose note then says so.
  const restore = async (rows: InboxRow[], undo = false) => {
    if (!undo) setNote(null);
    const ok = await act.run(async () => {
      for (const r of rows) await post(`/api/inbox/${r.id}/restore`, inboxRowDecoder);
      return true;
    });
    done();
    return ok === true;
  };
  // A batch's rows leave the list with the control pressed, so the keyboard
  // goes on to the note's link, as a dismissal's goes to Undo.
  const bulk = useRef<HTMLParagraphElement>(null);
  useEffect(() => void (note && "bulk" in note && bulk.current?.querySelector("a")?.focus()), [note]);
  // after is the place of a row just evaluated or dismissed on its own: once
  // the list has reloaded, the keyboard goes on to the row now in that place,
  // not back to the top of the page.
  const after = useRef<number | null>(null);
  const body = useRef<HTMLTableSectionElement>(null);
  useEffect(() => {
    const rows = body.current?.querySelectorAll("tr") ?? [];
    if (after.current === null || !rows.length) return;
    rows[Math.min(after.current, rows.length - 1)].querySelector<HTMLElement>(".row-actions button:not(:disabled)")?.focus();
    after.current = null;
  }, [res.data]);
  const one = (r: InboxRow, go: (rows: InboxRow[]) => Promise<unknown>) => {
    after.current = res.data?.rows.indexOf(r) ?? null;
    void go([r]);
  };
  const pick = (id: number, on: boolean) => setPicked((was) => new Set(on ? [...was, id] : [...was].filter((x) => x !== id)));

  const chosen = (res.data?.rows ?? []).filter((r) => picked.has(r.id));
  const waiting = status === "pending" && Boolean(res.data?.rows.length);

  return (
    <Panel title="Pipeline inbox" id="inbox-list" res={res} retry={retry} icon={InboxIcon} what="the pipeline inbox">
      {(data) => (
        <>
          {since && <ClearChip label={`First seen ${sinceWords(since)}`} to={viewLink(status === "" ? "pending" : status, "")} />}
          <FilterChips
            label="Inbox status"
            items={[
              ...(since ? [{ label: "All", count: Object.values(data.counts).reduce((a, b) => a + b, 0), to: viewLink(""), current: status === "" }] : []),
              ...VIEWS.filter((v) => v.status !== "expired" || data.counts.expired || status === "expired").map((v) => ({ label: v.label, count: data.counts[v.status] ?? 0, to: viewLink(v.status), current: v.status === status })),
            ]}
          />
          <div className="toolbar">
            <input type="search" data-page-search aria-label="Search the inbox" placeholder="Search the inbox" title="Company, role, location or source" value={text} onChange={(e) => setText(e.target.value)} />
            {waiting && (
              <>
                <label className="check">
                  <input
                    type="checkbox"
                    checked={chosen.length === data.rows.length}
                    ref={(el) => void (el && (el.indeterminate = chosen.length > 0 && chosen.length < data.rows.length))}
                    onChange={(e) => setPicked(new Set(e.target.checked ? data.rows.map((r) => r.id) : []))}
                  />
                  Select all {data.rows.length}
                </label>
                {chosen.length === 0 && (
                  <Button disabled={act.busy} onClick={() => void evaluate(data.rows)}>
                    Evaluate the {data.rows.length} on this page
                  </Button>
                )}
              </>
            )}
          </div>
          {act.error && <ErrorNote error={act.error} />}
          {note && "started" in note && <EvaluationNote name={note.started} jobId={note.jobId} />}
          <div role="status">
            {note && "refused" in note && <p className="note ok">{note.refused}</p>}
            {note && "bulk" in note && (
              <p className="note ok" ref={bulk}>
                {note.bulk} <Link to="/discovery?tab=inbox&status=queued">See them evaluating</Link>
              </p>
            )}
            {note && "dismissed" in note && (
              <UndoNote key={note.n} keepFocus={note.dismissed.length === 1} done={`Dismissed ${names(note.dismissed)}.`} undone={`${names(note.dismissed)} ${note.dismissed.length === 1 ? "is" : "are"} back in the inbox.`} onUndo={() => restore(note.dismissed, true)} />
            )}
          </div>
          {data.rows.length === 0 ? (
            status === "pending" && !q ? (
              <EmptyState
                icon={InboxIcon}
                title="Nothing is waiting to be evaluated."
                action={
                  <Link to="/discovery" className="button primary">
                    <Compass />
                    Run a scan
                  </Link>
                }
              >
                A scan puts the postings it finds here. For a posting you found yourself, <Link to="/?post=">paste it on Home</Link> to evaluate it.
              </EmptyState>
            ) : (
              <Empty>
                No postings match these filters. <Link to="/discovery?tab=inbox">Clear filters</Link>
              </Empty>
            )
          ) : (
            <>
              <p aria-live="polite" className="count-line">
                {data.total} {VIEWS.find((v) => v.status === status)?.label.toLowerCase() ?? (data.total === 1 ? "posting" : "postings")}
                {since ? ` first seen ${sinceWords(since)}` : ""}
                {q ? ` matching "${q}"` : ""}
              </p>
              {status === "pending" &&
                (data.unrouted ? (
                  <p className="note" role="note">
                    <ModelsLinked text={`Nothing is sent to evaluation while the ${data.unrouted} task has no model it can use: scans and the scheduled inbox evaluation leave every posting waiting here. Choose one under Settings, Models.`} />
                  </p>
                ) : (
                  <p className="muted">A scan evaluates only its top rows; the others wait here until you evaluate them or a scheduled inbox evaluation takes them (the daily packs run one after their scans).</p>
                ))}
              <details>
                <summary>What is rank?</summary>
                <p className="muted">{RANK_HELP}</p>
              </details>
              <table className="cards">
                <caption className="sr-only">Pipeline inbox</caption>
                <thead>
                  <tr>
                    <th scope="col" className="fill">
                      Posting
                    </th>
                    <th scope="col">Source</th>
                    <th scope="col" className="num">
                      <span title={RANK_HELP}>Rank</span>
                    </th>
                    <th scope="col" className="nowrap">
                      First seen
                    </th>
                    <th scope="col">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody ref={body}>
                  {data.rows.map((r) => (
                    <tr key={r.id} className={picked.has(r.id) ? "selected" : undefined}>
                      {/* The company, its role and its place share a column, so
                          the role and the place are read in words at 1180 px. */}
                      <td data-label="Posting" className="key fill">
                        {r.status === "pending" ? (
                          <label className="check">
                            <input type="checkbox" checked={picked.has(r.id)} onChange={(e) => pick(r.id, e.target.checked)} aria-label={`Select ${name(r)}`} />
                            <strong className="truncate" title={r.company}>
                              {r.company}
                            </strong>
                          </label>
                        ) : (
                          <strong className="truncate" title={r.company}>
                            {r.company}
                          </strong>
                        )}
                        <Truncate text={r.role} />
                        {r.location && (
                          <span className="truncate sub muted" title={r.location}>
                            {r.location}
                          </span>
                        )}
                        {status === "" && <span className="sub muted">{statusLabel(r.status)}</span>}
                        {r.status === "pending" && r.notes && (
                          <span className="truncate sub muted" title={r.notes}>
                            {r.notes}
                          </span>
                        )}
                      </td>
                      <td data-label="Source" className="muted">
                        {r.source}
                      </td>
                      <td data-label="Rank" className="num" title={r.reasons.join(", ")}>
                        {r.score}
                      </td>
                      <td data-label="First seen" className="nowrap">
                        {formatDate(r.firstSeenAt)}
                      </td>
                      <td className="row-actions">
                        <Actions row={r} busy={act.busy} onEvaluate={() => one(r, evaluate)} onDismiss={() => one(r, dismiss)} onRestore={() => one(r, restore)} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <Pager page={data.page} pageSize={data.pageSize} total={data.total} onPage={toPage} />
              <SelectionBar count={chosen.length}>
                <Button variant="primary" disabled={act.busy} onClick={() => void evaluate(chosen)}>
                  Evaluate {chosen.length} selected
                </Button>
                <Button disabled={act.busy} onClick={() => void dismiss(chosen)}>
                  Dismiss {chosen.length} selected
                </Button>
              </SelectionBar>
            </>
          )}
        </>
      )}
    </Panel>
  );
}
