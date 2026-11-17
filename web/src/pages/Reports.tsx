import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { ApiError, get, newRequestId, patch, post, request, withQuery } from "../api/client";
import { applicationLookup, reportDetail, reportPage, statusChangeResult } from "../api/decoders";
import { runAccepted } from "../api/decoders_runs";
import { asApiError, usePageInRange, useResource } from "../hooks";
import { Link, useSearchParams } from "../router";
import { Markdown, headingsOf } from "../ui/Markdown";
import { Chip, ErrorNote, ExternalLink, OfflineScoresNote, Pager, safeHref, ScoreBadge, StatusPill, Truncate, UndoNote, formatDate, humanize, Loading, useMoreFilters } from "../ui/parts";
import { SkillGaps } from "./reports/Gaps";
import { ClearChip, FilterChips, isDay, sinceWords } from "../ui/FilterChips";
import { reportBody } from "./reports/body";
import { ReportChips } from "./applynext/ReportDrawer";
import { RowSubmitDialog } from "./applynext/SubmitDialogs";
import { SAMPLE, sampleHost } from "./applynext/words";
import type { ApplicationLookup, ApplyItem, ReportSummary } from "../wire";
import { PageHeader } from "../ui/PageHeader";
import { EmptyState } from "../ui/EmptyState";
import { Ban, BookOpenText, ChevronLeft, Copy, FileText, RefreshCw, Send, WandSparkles } from "lucide-react";
import { Button } from "../ui/Button";

const RECOMMENDATIONS = ["skip", "consider", "apply", "strong"];
const LEGITIMACY = ["credible", "unclear", "doubtful", "unassessed"];
const SORTS: Record<string, string> = { date_desc: "Newest", score_desc: "Highest score" };
// Quick views set the filters in one press; a view is current while the
// address holds exactly its filters.
const VIEWS: { label: string; query: Record<string, string> }[] = [
  { label: "To decide", query: { undecided: "true", sort: "score_desc" } },
  { label: "Score 4+", query: { scoreMin: "4", sort: "score_desc" } },
  { label: "Manual review", query: { legitimacy: "unclear" } },
  { label: "Newest", query: {} },
];
const FILTER_KEYS = ["recommendation", "legitimacy", "company", "scoreMin", "scoreMax", "from", "to", "since", "undecided", "sort"];
// A tracker row in one of these states has already been sent, so the report
// offers no new start for it.
const SENT = new Set(["applied", "responded", "interview", "offer", "rejected"]);

// A run started from a report, with the way to it.
const started = (text: ReactNode, runId: string) => (
  <p className="note ok">
    {text} <Link to={`/runs/${runId}`}>Follow the run</Link>
  </p>
);

// skippedWhy says why the posting was skipped, or "" when it was not: its row
// is skipped or discarded, or, with no row, its report recommends a skip.
function skippedWhy(report: ReportSummary, lookup?: ApplicationLookup): string {
  const entry = lookup?.found ? lookup.entry : undefined;
  const last = [...(lookup?.history ?? [])].reverse().find((h) => h.status === entry?.status);
  const on = last ? ` on ${formatDate(last.at)}` : "";
  if (entry?.status === "discarded") return `You discarded this posting${on}.`;
  if (entry?.status === "skipped" && last?.actor === "user") return `You skipped this posting${on}.`;
  if (entry?.status === "skipped" && last?.actor === "agent") return `The Ask Agent skipped this posting${on}.`;
  if (entry ? entry.status !== "skipped" : report.recommendation !== "skip") return "";
  if (report.kind === "gated") return "It failed your eligibility gate.";
  return report.recommendation === "skip" ? "Its evaluation recommends skipping it." : `It was skipped${on}.`;
}

// StartFromReport starts an application to the report's posting the way an
// Apply Next row does: Autofill only stops before Submit, and Autofill and
// submit asks first, with the skip-list status read from the server. A skipped
// posting may still be applied to (owner decision in apply.md): override says
// why it was skipped and the buttons read as the override they are; the
// server takes an archived row out of the archive when the run records its
// submission. Lists never pick a skipped row.
function StartFromReport({ report, start, override, onStarted, children }: { report: ReportSummary; start: boolean; override?: string; onStarted?: (note: ReactNode) => void; children: ReactNode }) {
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState<ApiError | { text: string; runId: string } | null>(null);
  let host = "";
  try {
    host = new URL(report.postingUrl).host;
  } catch {
    start = false;
  }
  const sample = start && sampleHost(host);
  const item: ApplyItem = { id: report.number, company: report.company, role: report.role, score: report.score, lane: "", postingUrl: report.postingUrl, postingHost: host, date: "", version: 0, state: "idle" };
  async function run(submit: boolean) {
    setBusy(true);
    setSaid(null);
    try {
      const acc = await post("/api/apply/runs", runAccepted, { url: report.postingUrl, company: report.company, role: report.role, submit, confirmed: submit });
      const text = submit ? `Submitting the ${report.company} application.` : `Filling the ${report.company} application; it stops before Submit.`;
      setSaid({ text, runId: acc.runId });
      onStarted?.(started(text, acc.runId));
    } catch (e) {
      setSaid(asApiError(e));
    } finally {
      setBusy(false);
      setAsking(false);
    }
  }
  const anyway = start && !sample && override ? `${override} Applying anyway overrides that; Apply Next and batches still leave it out.` : "";
  return (
    <>
      {anyway && <p className="note">{anyway}</p>}
      <div className="toolbar report-actions">
        {sample && <span className="muted">{SAMPLE}</span>}
        {start && !sample && (
          <>
            <Button size="sm" variant="primary" icon={WandSparkles} disabled={busy} title="Fills the form in the apply window and stops before Submit." onClick={() => void run(false)}>
              {anyway ? "Autofill anyway" : "Autofill only"}
            </Button>
            <Button size="sm" icon={Send} disabled={busy} onClick={() => setAsking(true)}>
              {anyway ? "Autofill and submit anyway" : "Autofill and submit"}&hellip;
            </Button>
          </>
        )}
        {children}
      </div>
      {said instanceof ApiError ? (
        <ErrorNote error={said} />
      ) : (
        <div role="status">{said && started(said.text, said.runId)}</div>
      )}
      {asking && <RowSubmitDialog item={item} checked override={anyway} busy={busy} onConfirm={() => void run(true)} onCancel={() => setAsking(false)} />}
    </>
  );
}

function Filters({ filtered }: { filtered: boolean }) {
  const [params, setParams] = useSearchParams();
  const urlCompany = params.get("company") ?? "";
  const urlScoreMin = params.get("scoreMin") ?? "";
  const [company, setCompany] = useState(urlCompany);
  const [scoreMin, setScoreMin] = useState(urlScoreMin);
  const urlScoreMax = params.get("scoreMax") ?? "";
  const urlFrom = params.get("from") ?? "";
  const urlTo = params.get("to") ?? "";
  const [scoreMax, setScoreMax] = useState(urlScoreMax);
  const [from, setFrom] = useState(urlFrom);
  const [to, setTo] = useState(urlTo);
  // Keyed on the strings: the params object is new on every render, so keying
  // on it would reset the field on each keystroke.
  useEffect(() => setCompany(urlCompany), [urlCompany]);
  useEffect(() => setScoreMin(urlScoreMin), [urlScoreMin]);
  useEffect(() => setScoreMax(urlScoreMax), [urlScoreMax]);
  useEffect(() => setFrom(urlFrom), [urlFrom]);
  useEffect(() => setTo(urlTo), [urlTo]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    // A report number (such as 12) opens that report instead of filtering by company.
    const number = /^#?0*(\d{1,9})$/.exec(company.trim())?.[1];
    if (number) return setParams({ id: number, company: undefined, page: undefined });
    setParams({ company: company.trim(), scoreMin: scoreMin.trim(), scoreMax: scoreMax.trim(), from, to, page: undefined }, { replace: true });
  };
  const select = (name: string, label: string, options: string[], all: string) => (
    <label>
      {label}
      <select value={params.get(name) ?? ""} onChange={(e) => setParams({ [name]: e.target.value, page: undefined }, { replace: true })}>
        <option value="">{all}</option>
        {options.map((o) => (
          <option key={o} value={o}>
            {humanize(o)}
          </option>
        ))}
      </select>
    </label>
  );
  const set = FILTER_KEYS.filter((k) => params.get(k));
  const more = useMoreFilters(["recommendation", "legitimacy", "sort", "scoreMin", "scoreMax", "from", "to"].filter((k) => params.get(k)).length);
  return (
    <form className="filters stacked" onSubmit={submit} aria-label="Filters">
      <FilterChips
        label="Quick views"
        items={VIEWS.map((v) => ({
          label: v.label,
          to: `/reports${Object.keys(v.query).length ? `?${new URLSearchParams(v.query).toString()}` : ""}`,
          current: set.length === Object.keys(v.query).length && set.every((k) => params.get(k) === v.query[k]),
        }))}
      />
      <div className="filter-group">
        {!more.hidden && select("recommendation", "Recommendation", RECOMMENDATIONS, "Any")}
        {!more.hidden && select("legitimacy", "Legitimacy", LEGITIMACY, "Any")}
        <label>
          Company or report number
          <input type="search" data-page-search value={company} onChange={(e) => setCompany(e.target.value)} />
        </label>
        {more.toggle}
        {!more.hidden && (
          <label>
            Sort
            <select value={params.get("sort") ?? "date_desc"} onChange={(e) => setParams({ sort: e.target.value === "date_desc" ? undefined : e.target.value, page: undefined }, { replace: true })}>
              {Object.entries(SORTS).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
      <div className="filter-group">
        {!more.hidden && (
          <>
            <label>
              Minimum score
              <input type="number" inputMode="decimal" min={0} max={5} step={0.1} value={scoreMin} onChange={(e) => setScoreMin(e.target.value)} />
            </label>
            <label>
              Maximum score
              <input type="number" inputMode="decimal" min={0} max={5} step={0.1} value={scoreMax} onChange={(e) => setScoreMax(e.target.value)} />
            </label>
            <label>
              From
              <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
            </label>
            <label>
              To
              <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
            </label>
          </>
        )}
        <button type="submit">Apply filters</button>
        {filtered && <Link to="/reports">Clear filters</Link>}
      </div>
    </form>
  );
}

// onDecided, given in a view of reports to decide, hears each decision taken
// here (a skip, a start, an evaluation) with the note that says it; focusTitle
// puts the keyboard on the report's heading once it is read.
function Viewer({ number, onDecided, focusTitle }: { number: number; onDecided?: (note: ReactNode) => void; focusTitle?: boolean }) {
  const [res, retry] = useResource(() => get(`/api/reports/${number}`, reportDetail), [number], ["reports"]);
  const [copied, setCopied] = useState("");
  // The report passed on from here, for its Undo; n makes each skip a new note.
  const [skipped, setSkipped] = useState<{ number: number; id: number; version: number; n: number }>();
  const [skipFailure, setSkipFailure] = useState<ApiError | null>(null);
  // The run evaluating this posting again, kept with the report it started from.
  const [again, setAgain] = useState<{ number: number; runId?: string; error?: ApiError }>();
  const posting = res.data?.report.postingUrl ?? "";
  // The report carries no row id, so the tracker is asked which row holds this posting.
  const [row, reloadRow] = useResource(() => (posting ? get("/api/applications/lookup", applicationLookup, { url: posting }) : Promise.resolve(undefined)), [posting], ["tracker"]);
  const title = useRef<HTMLHeadingElement>(null);
  const read = res.data?.report.number;
  useEffect(() => {
    if (focusTitle && read === number) title.current?.focus();
  }, [focusTitle, read, number]);
  // What a press said about one report is not said about the next one.
  useEffect(() => {
    setCopied("");
    setSkipFailure(null);
  }, [number]);
  if (res.status === "loading") return <Loading />;
  if (res.status === "error") return <ErrorNote error={res.error} onRetry={retry} what="this report" />;
  const { report, content: whole } = res.data!;
  const entry = row.data?.found ? row.data.entry : undefined;
  const content = reportBody(whole);
  const toc = headingsOf(content);
  // Skipping a posting here takes it off Apply Next; Undo makes it evaluated
  // again, the status the Skip button is offered for. Both presses quote the
  // row's version (the lookup's own, then the one the skip made), so a row that
  // was applied since this page was read is refused and the row is read again.
  const move = async (id: number, status: string, version: number) => {
    setSkipFailure(null);
    try {
      return await patch(`/api/candidacies/${id}`, statusChangeResult, { status, expectedVersion: version });
    } catch (err) {
      setSkipFailure(asApiError(err));
    } finally {
      reloadRow();
    }
  };
  const skipNote = (s: { id: number; version: number; n: number }, keepFocus?: boolean) => (
    <UndoNote key={s.n} keepFocus={keepFocus} done={`${report.company} is skipped and no longer offered in Apply Next.`} undone={`${report.company} is evaluated again.`} onUndo={async () => !!(await move(s.id, "evaluated", s.version))} />
  );
  const skip = async (e: { id: number; version: number }) => {
    const done = await move(e.id, "skipped", e.version);
    if (!done?.changed) return;
    const s = { number, id: e.id, version: done.entry.version, n: (skipped?.n ?? 0) + 1 };
    setSkipped(s);
    onDecided?.(skipNote(s, true));
  };
  // Force reads the posting again and writes a new report even when a final
  // one exists; a screening or gated report gets its full evaluation.
  const evaluateAgain = async () => {
    setAgain({ number });
    try {
      const acc = await request("POST", "/api/evaluations", runAccepted, {
        body: { url: report.postingUrl, company: report.company, title: report.role, mode: "deep", force: true },
        headers: { "Idempotency-Key": newRequestId() },
      });
      setAgain({ number, runId: acc.runId });
      onDecided?.(started(againText, acc.runId));
    } catch (err) {
      setAgain({ number, error: asApiError(err) });
    }
  };
  const againText = `Evaluating ${report.company} again; the new report joins the catalog when it is written.`;
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(whole);
      setCopied("Markdown copied.");
    } catch {
      setCopied("The browser did not allow copying.");
    }
  };
  return (
    <article aria-labelledby="report-title" className="viewer">
      <h2 id="report-title" ref={title} tabIndex={-1}>
        {report.company}: {report.role}
      </h2>
      <ReportChips report={report} content={whole} />
      <StartFromReport
        key={report.number}
        report={report}
        onStarted={onDecided}
        start={row.status !== "loading" && !(entry && SENT.has(entry.status.toLowerCase()))}
        override={skippedWhy(report, row.data)}
      >
        {entry && SENT.has(entry.status.toLowerCase()) && (
          <span>
            Tracker: <StatusPill status={entry.status} />
          </span>
        )}
        {entry?.status === "evaluated" && (
          <Button size="sm" icon={Ban} title="Marks the posting skipped, so Apply Next stops offering it; Undo brings it back." onClick={() => void skip(entry)}>
            Skip this posting
          </Button>
        )}
        {safeHref(report.postingUrl) && (
          <Button size="sm" icon={RefreshCw} disabled={again?.number === number && !again.error} title="Reads the posting again and writes a new report with the model; this one stays." onClick={() => void evaluateAgain()}>
            {report.kind === "final" ? "Evaluate again" : "Evaluate in full"}
          </Button>
        )}
        <Button size="sm" icon={Copy} onClick={copy}>
          Copy Markdown
        </Button>
        {report.postingUrl && <ExternalLink href={report.postingUrl}>Open the posting</ExternalLink>}
        {entry && <Link to={`/tracker?row=${entry.id}&documents=${entry.id}`}>Résumé and cover letter</Link>}
        <Link to={entry ? `/tracker?row=${entry.id}` : `/tracker?company=${encodeURIComponent(report.company)}`}>Find in tracker</Link>
      </StartFromReport>
      <div role="status">
        {skipped?.number === number && skipNote(skipped)}
      </div>
      {skipFailure && <ErrorNote error={skipFailure} />}
      {again?.number === number &&
        (again.error ? (
          <ErrorNote error={again.error} />
        ) : (
          again.runId && <div role="status">{started(againText, again.runId)}</div>
        ))}
      <p aria-live="polite" className="muted">{copied}</p>
      {toc.length > 1 && (
        <nav aria-label="Contents">
          <ul className="plain toc">
            {toc.map((h) => (
              <li key={h.id}>
                <a href={`#${h.id}`}>{h.text}</a>
              </li>
            ))}
          </ul>
        </nav>
      )}
      <Markdown source={content} />
    </article>
  );
}

function row(r: ReportSummary, selected: boolean, open: (n: number) => void) {
  return (
    <tr key={r.number} className={selected ? "selected" : undefined}>
      <td data-label="Posting">
        <button type="button" className="row-pick" aria-current={selected ? "true" : undefined} onClick={() => open(r.number)}>
          <strong className="truncate" title={r.company}>
            {r.company}
          </strong>
          <Truncate text={r.role} />
          <span className="muted">
            {r.trackerStatus && <StatusPill status={r.trackerStatus} />} Report {r.number} · <time dateTime={r.createdAt}>{formatDate(r.createdAt)}</time> · {humanize(r.recommendation)}
            {(r.legitimacy === "unclear" || r.legitimacy === "doubtful") && ` · Legitimacy ${r.legitimacy}`}
          </span>
        </button>
      </td>
      <td data-label="Score" className="num">
        <ScoreBadge score={r.score} />
      </td>
    </tr>
  );
}

export default function Reports() {
  const [params, setParams] = useSearchParams();
  const selected = Number(params.get("id")) || 0;
  const page = Math.max(1, Number(params.get("page")) || 1);
  const query = {
    recommendation: params.get("recommendation") ?? "", legitimacy: params.get("legitimacy") ?? "", company: params.get("company") ?? "",
    scoreMin: params.get("scoreMin") ?? "", scoreMax: params.get("scoreMax") ?? "", from: params.get("from") ?? "", to: params.get("to") ?? "", sort: params.get("sort") ?? "", page,
    // The newest report of each posting evaluated since a day: what Home's and
    // the funnel's evaluated counts count.
    since: isDay(params.get("since") ?? "") ? (params.get("since") as string) : "",
    // The current report of each posting the tracker still holds as evaluated.
    undecided: params.get("undecided") === "true" ? "true" : "",
  };
  const [res, retry] = useResource(() => get("/api/reports", reportPage, query), [JSON.stringify(query)], ["reports", "tracker"]);
  usePageInRange(res.data && { ...res.data, rows: res.data.reports }, (p) => setParams({ page: p > 1 ? String(p) : undefined }, { replace: true }));
  // The reports of the page, for stepping through them from the open one;
  // the steps go on to the next or previous page at its ends.
  const numbers = res.data?.reports.map((r) => r.number) ?? [];
  const at = numbers.indexOf(selected);
  const size = res.data?.pageSize ?? 25;
  const total = res.data?.total ?? 0;
  // The report a step or a decision opened, with its place in the whole view
  // (so the count holds while its page loads) and the note of the decision.
  const [target, setTarget] = useState<{ id: number; place: number; note?: ReactNode }>();
  useEffect(() => setTarget((t) => (t && t.id !== selected ? undefined : t)), [selected]);
  const place = at >= 0 ? (page - 1) * size + at + 1 : target?.id === selected ? target.place : 0;
  const moved = target?.id === selected ? target.note : undefined;
  const [stepFailure, setStepFailure] = useState<ApiError | null>(null);
  const numbersOf = async (p: number) => (await get("/api/reports", reportPage, { ...query, page: p })).reports.map((r) => r.number);
  // open opens the i-th report (counted from the end when negative) of page p.
  const open = (list: number[], i: number, p: number, note?: ReactNode) => {
    const k = i < 0 ? list.length + i : i;
    if (list[k] === undefined) return;
    setTarget({ id: list[k], place: (p - 1) * size + k + 1, note });
    setParams({ id: String(list[k]), page: p > 1 ? String(p) : undefined });
  };
  const failing = async (work: () => Promise<void>) => {
    setStepFailure(null);
    try {
      await work();
    } catch (e) {
      setStepFailure(asApiError(e));
    }
  };
  const step = (dir: 1 | -1) =>
    at >= 0 && failing(async () => (numbers[at + dir] !== undefined ? open(numbers, at + dir, page) : open(await numbersOf(page + dir), dir > 0 ? 0 : -1, page + dir)));
  // After a decision the page is read again: a decided report has left the
  // view, so the next one now holds its place; a full page goes on to the next.
  const advance = (from: number, fromAt: number, note: ReactNode) =>
    failing(async () => {
      const fresh = await numbersOf(page);
      const i = fresh.indexOf(from);
      const k = i >= 0 ? i + 1 : fromAt;
      const [list, p, j] = k < fresh.length || fresh.length < size ? [fresh, page, k] : [await numbersOf(page + 1), page + 1, 0];
      if (new URLSearchParams(window.location.search).get("id") === String(from)) open(list, j, p, note);
    });
  const filtered = Boolean(query.recommendation || query.legitimacy || query.company || query.scoreMin || query.scoreMax || query.from || query.to || query.since || query.undecided);
  // A report picked while the page is scrolled down opens at its start.
  useEffect(() => {
    if (selected > 0 && window.scrollY > 0) window.scrollTo?.({ top: 0 });
  }, [selected]);
  return (
    <>
      <PageHeader path="/reports" />
      <OfflineScoresNote />
      <div className="reports" data-open={selected > 0 ? "" : undefined}>
        {query.since && <ClearChip label={`Evaluated ${sinceWords(query.since)}, one per posting`} to={withQuery("/reports", Object.fromEntries([...params].filter(([k]) => k !== "since" && k !== "page")))} />}
        <Filters filtered={filtered} />
        <div className="reports-layout">
          <section aria-labelledby="catalog" className="panel reports-list">
            <header className="panel-head">
              <h2 id="catalog">Catalog</h2>
              {res.data && <Chip>{`${res.data.total} ${res.data.total === 1 ? "report" : "reports"}`}</Chip>}
            </header>
            {res.status === "loading" && <Loading />}
            {res.status === "error" && <ErrorNote error={res.error} onRetry={retry} what="the reports" />}
            {res.data &&
              (res.data.reports.length === 0 ? (
                <EmptyState icon={FileText} title={filtered ? "No report matches these filters." : "No evaluation reports yet."} action={filtered && <Link to="/reports">Clear the filters</Link>}>
                  {filtered ? "Widen or clear the filters to see more." : "Evaluating a posting writes the first one."}
                </EmptyState>
              ) : (
                <>
                  <table className="cards">
                    <caption className="sr-only">Reports</caption>
                    <thead>
                      <tr>
                        <th scope="col">Posting</th>
                        <th scope="col" className="num">
                          Score
                        </th>
                      </tr>
                    </thead>
                    <tbody>{res.data.reports.map((r) => row(r, r.number === selected, (n) => setParams({ id: String(n) })))}</tbody>
                  </table>
                  <Pager page={res.data.page} pageSize={res.data.pageSize} total={res.data.total} onPage={(p) => setParams({ page: p > 1 ? String(p) : undefined })} />
                </>
              ))}
          </section>
          <section aria-label="Report" className="panel reports-view">
            {selected > 0 ? (
              <>
                {/* Previous and Next stay at every width, so the keyboard walks the page's reports from here; the way back only where the catalog is hidden. */}
                <p className="reports-back toolbar">
                  <Button size="sm" icon={ChevronLeft} className="reports-catalog" onClick={() => setParams({ id: undefined })}>
                    Back to the catalog
                  </Button>
                  {place > 0 && total > 1 && (
                    <span className="reports-step">
                      <Button size="sm" disabled={place <= 1} aria-label="Previous report" onClick={() => void step(-1)}>
                        Previous
                      </Button>
                      <span className="muted" aria-live="polite">
                        {place} of {total}
                      </span>
                      <Button size="sm" disabled={place >= total} aria-label="Next report" onClick={() => void step(1)}>
                        Next
                      </Button>
                    </span>
                  )}
                </p>
                {stepFailure && <ErrorNote error={stepFailure} />}
                {moved && <div role="status">{moved}</div>}
                <Viewer number={selected} focusTitle={!!moved} onDecided={query.undecided && at >= 0 ? (note) => void advance(selected, at, note) : undefined} />
              </>
            ) : (
              <EmptyState icon={BookOpenText} title="Choose a report to read it here." />
            )}
          </section>
        </div>
      </div>
      <SkillGaps shown={res.data?.reports.map((r) => r.number) ?? []} />
    </>
  );
}
