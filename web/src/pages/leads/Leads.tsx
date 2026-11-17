import { useCallback, useEffect, useState, type FormEvent } from "react";
import { del, get, newRequestId, post, request } from "../../api/client";
import { blockedCompany, blockedCompanyList, lead as leadDecoder, leadPage } from "../../api/decoders_discovery";
import { runAccepted } from "../../api/decoders_runs";
import { usePageInRange, useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote, ExternalLink, Pager, Panel, humanize } from "../../ui/parts";
import { FilterChips } from "../../ui/FilterChips";
import { Button } from "../../ui/Button";
import type { Lead } from "../../wire";
import { useAction, when } from "../action";
import { statusLabel } from "../discovery/Inbox";
import { EmptyState } from "../../ui/EmptyState";
import { Building2, ClipboardCheck, Radar } from "lucide-react";

const STATES = ["new", "evaluated", "dismissed"] as const;
const CATEGORIES = ["posting", "careers-page", "no-ats", "email", "dm", "comment", "generic-page", "unreachable", "low-confidence", "eval-cap", "eval-failed"];
const PAGE_SIZE = 25;

// linkStatus says what the lead gives the user to act on.
function linkStatus(l: Lead): string {
  if (l.applyLinks.length > 0) return l.expanded ? `link, careers page read: ${l.expanded}` : "has a link";
  if (l.contactMethod) return `contact only (${l.contactMethod})`;
  return "no link";
}

// mailable keeps the addresses a mailto link can carry as they are.
const mailable = (l: Lead) => l.contactEmails.filter((m) => /^[^\s@?#&]+@[^\s@?#&]+$/.test(m));

function excerpt(l: Lead): string {
  if (l.roles.length > 0) return l.roles.join(", ");
  const text = l.evidenceText ?? l.evidenceTitle ?? "";
  return text.length > 120 ? `${text.slice(0, 117)}...` : text;
}

function Evidence({ l }: { l: Lead }) {
  return (
    <div className="sources">
      <dl className="facts">
        {l.evidenceAdapter && (
          <>
            <dt>Found by</dt>
            <dd>{l.evidenceAdapter}</dd>
          </>
        )}
        {l.evidenceAt && (
          <>
            <dt>Dated</dt>
            <dd>{when(l.evidenceAt)}</dd>
          </>
        )}
        {l.evidenceUrl && (
          <>
            <dt>Source</dt>
            <dd>
              <ExternalLink href={l.evidenceUrl}>{l.evidenceTitle || l.evidenceUrl}</ExternalLink>
            </dd>
          </>
        )}
        {l.locations.length > 0 && (
          <>
            <dt>Locations</dt>
            <dd>{l.locations.join(", ")}</dd>
          </>
        )}
        {l.workModel && (
          <>
            <dt>Work model</dt>
            <dd>{l.workModel}</dd>
          </>
        )}
        {l.compensation && (
          <>
            <dt>Pay</dt>
            <dd>{l.compensation}</dd>
          </>
        )}
        {l.sponsorship && (
          <>
            <dt>Sponsorship</dt>
            <dd>{l.sponsorship}</dd>
          </>
        )}
        {l.applyLinks.length > 0 && (
          <>
            <dt>Apply links</dt>
            <dd>
              {l.applyLinks.map((a) => (
                <div key={a}>
                  <ExternalLink href={a}>{a}</ExternalLink>
                </div>
              ))}
            </dd>
          </>
        )}
        {l.reason && (
          <>
            <dt>Note</dt>
            <dd>{l.reason}</dd>
          </>
        )}
      </dl>
      {l.evidenceText ? <blockquote className="pre">{l.evidenceText}</blockquote> : <p className="muted">No evidence text was kept{l.evidenceAbsentReason ? `: ${l.evidenceAbsentReason}` : "."}</p>}
    </div>
  );
}

// Note is what the last action on the list did; a pass it started is linked,
// and a dismissal or a block offers its undo.
type Note = { text: string; runId?: string; undo?: () => Promise<unknown> };

const restore = (l: Lead) => post(`/api/leads/${l.id}/restore`, leadDecoder);

// Running says which passes are going; the server refuses another start of
// one until it ends.
type Running = { evaluate: boolean; expand: boolean };
const RUNNING = { evaluate: "An evaluation pass is running.", expand: "A careers-page pass is running." };

function LeadRow({ l, running, onChange, onRun }: { l: Lead; running: Running; onChange: () => void; onRun: (note: Note) => void }) {
  const [open, setOpen] = useState(false);
  const [blocking, setBlocking] = useState(false);
  const [started, setStarted] = useState<keyof Running>();
  const act = useAction();
  const startPass = async (op: keyof Running) => {
    const done = await act.run(() => request("POST", `/api/leads/${op}`, runAccepted, { body: { ids: [l.id] }, headers: { "Idempotency-Key": newRequestId() } }));
    if (!done) return;
    setStarted(op);
    onRun({ text: op === "evaluate" ? `Evaluation of ${l.company} started.` : `Reading the careers page of ${l.company} started.`, runId: done.runId });
    onChange();
  };
  const dismiss = async () => {
    const done = await act.run(() => post(`/api/leads/${l.id}/dismiss`, leadDecoder));
    if (done) {
      onRun({ text: `Dismissed ${l.company}.`, undo: () => restore(l) });
      onChange();
    }
  };
  const block = async () => {
    const pressed = Date.now();
    const done = await act.run(() => post("/api/blocked-companies", blockedCompany, { name: l.company, reason: "Blocked from a lead" }));
    setBlocking(false);
    // A company that was blocked before this press keeps its block: Undo is
    // offered only for the entry this press added.
    const added = done && Date.parse(done.addedAt) >= pressed - 5000;
    if (done) onRun({ text: `Blocked ${l.company}. Unblock it under Discovery, Blocked companies.`, undo: added ? () => del(`/api/blocked-companies?name=${encodeURIComponent(done.name)}`, blockedCompanyList) : undefined });
    onChange();
  };
  const putBack = async () => {
    if (await act.run(() => restore(l))) onChange();
  };
  return (
    <li>
      <div className="lead-row">
        <div>
          <div>
            <strong>{l.company}</strong> <span>{excerpt(l)}</span>
          </div>
          <span className="chips">
            <Chip raw>{l.source}</Chip>
            <Chip>{humanize(l.category)}</Chip>
            <Chip tone={l.state === "dismissed" ? "neutral" : l.state === "evaluated" ? "good" : "warn"}>{humanize(l.state)}</Chip>
            <span className="muted">confidence {l.confidence.toFixed(2)}</span>
            <span className="muted">{linkStatus(l)}</span>
            {mailable(l).map((m) => (
              <a key={m} href={`mailto:${m}`}>
                {m}
              </a>
            ))}
          </span>
          {(l.author || l.evidenceUrl) && (
            <div className="muted">
              {l.author && `Posted by ${l.author}${l.posterType ? ` (${l.posterType} page)` : ""}`}
              {l.author && l.evidenceUrl && " · "}
              {l.evidenceUrl && <ExternalLink href={l.evidenceUrl}>Original post</ExternalLink>}
            </div>
          )}
          {l.inboxStatus && (
            <div>
              <Link to={`/discovery?${new URLSearchParams({ tab: "inbox", status: l.inboxStatus, q: l.company })}`}>Inbox: {statusLabel(l.inboxStatus)}</Link>
              {l.reportNumber && (
                <>
                  {" · "}
                  <Link to={`/reports?id=${l.reportNumber}`}>Report {l.reportNumber}</Link>
                </>
              )}
              {l.trackerId && (
                <>
                  {" · "}
                  <Link to={`/tracker?row=${l.trackerId}`}>Tracker row</Link>
                </>
              )}
            </div>
          )}
        </div>
        <span className="row-actions-wrap">
          <button type="button" className="small" aria-expanded={open} onClick={() => setOpen(!open)} aria-label={`${open ? "Hide" : "Show"} evidence for ${l.company}`}>
            Evidence
          </button>
          {l.state === "new" && (
            <>
              {l.evaluable && (
                <button type="button" className="small" disabled={act.busy || running.evaluate} title={running.evaluate ? RUNNING.evaluate : undefined} onClick={() => void startPass("evaluate")} aria-label={`Evaluate ${l.company}`}>
                  {running.evaluate && started === "evaluate" ? "Evaluating..." : "Evaluate"}
                </button>
              )}
              {l.category === "careers-page" && (
                <button type="button" className="small" disabled={act.busy || running.expand} title={running.expand ? RUNNING.expand : undefined} onClick={() => void startPass("expand")} aria-label={`Read careers page: ${l.company}`}>
                  {running.expand && started === "expand" ? "Reading..." : "Read careers page"}
                </button>
              )}
              <button type="button" className="small" disabled={act.busy} onClick={() => void dismiss()} aria-label={`Dismiss ${l.company}`}>
                Dismiss
              </button>
            </>
          )}
          {l.state === "dismissed" && (
            <button type="button" className="small" disabled={act.busy} onClick={() => void putBack()} aria-label={`Restore ${l.company}`}>
              Restore
            </button>
          )}
          <button type="button" className="small danger-outline" onClick={() => setBlocking(true)} aria-label={`Block company: ${l.company}`}>
            Block company
          </button>
        </span>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {open && <Evidence l={l} />}
      {blocking && (
        <ConfirmDialog
          title={`Block ${l.company}`}
          consequence="Scans and evaluation leave this company out until you unblock it on the Discovery page."
          verb="Block"
          busy={act.busy}
          onConfirm={() => void block()}
          onCancel={() => setBlocking(false)}
        />
      )}
    </li>
  );
}

export function LeadsTab() {
  const [params, setParams] = useSearchParams();
  const state = STATES.find((s) => s === params.get("state")) ?? "new";
  const category = params.get("category") ?? "";
  const source = params.get("source") ?? "";
  const q = params.get("q") ?? "";
  const sort = params.get("sort") === "confidence" ? "confidence" : "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const [sourceText, setSourceText] = useState(source);
  useEffect(() => setSourceText(source), [source]);
  // The search asks the server 300 ms after typing stops, or at once on Enter.
  const [text, setText] = useState(q);
  useEffect(() => setText(q), [q]);
  useEffect(() => {
    if (text.trim() === q) return;
    const t = setTimeout(() => setParams({ q: text.trim() || undefined, page: undefined }, { replace: true }), 300);
    return () => clearTimeout(t);
  }, [text, q, setParams]);
  const [res, retry] = useResource(() => get("/api/leads", leadPage, { state, category, source, q, sort, page, pageSize: PAGE_SIZE }), [state, category, source, q, sort, page], ["scans"]);
  const toPage = useCallback((p: number) => setParams({ page: p > 1 ? String(p) : undefined }, { replace: true }), [setParams]);
  usePageInRange(res.data, toPage);
  const [note, setNote] = useState<Note | null>(null);
  const pass = useAction();
  const set = (patch: Record<string, string | undefined>) => setParams({ ...patch, page: undefined }, { replace: true });

  // A pass over the new leads has nothing to do when the unfiltered New view
  // is empty.
  const noneNew = state === "new" && !category && !source && !q && res.data?.total === 0;
  const batch = async (op: "evaluate" | "expand") => {
    const done = await pass.run(() => request("POST", `/api/leads/${op}`, runAccepted, { body: {}, headers: { "Idempotency-Key": newRequestId() } }));
    if (done) setNote({ text: op === "evaluate" ? "Evaluation of the new leads started." : "Reading the new careers pages started.", runId: done.runId });
    if (done) retry();
  };
  const running = { evaluate: Boolean(res.data?.evaluating), expand: Boolean(res.data?.expanding) };
  const undo = async (go: () => Promise<unknown>) => {
    if (await pass.run(go)) {
      setNote(null);
      retry();
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    set({ source: sourceText.trim(), q: text.trim() || undefined });
  };

  return (
    <>
      <form className="filters" aria-label="Lead filters" onSubmit={submit}>
        <label>
          Search
          <input type="search" data-page-search maxLength={200} placeholder="Company, role, poster" title="Every word must match the company, poster, roles or evidence title" value={text} onChange={(e) => setText(e.target.value)} />
        </label>
        <label>
          State
          <select value={state} onChange={(e) => set({ state: e.target.value === "new" ? undefined : e.target.value })}>
            {STATES.map((s) => (
              <option key={s} value={s}>
                {humanize(s)}
              </option>
            ))}
          </select>
        </label>
        <label>
          Category
          <select value={category} onChange={(e) => set({ category: e.target.value })}>
            <option value="">All categories</option>
            {CATEGORIES.map((c) => (
              <option key={c} value={c}>
                {humanize(c)}
              </option>
            ))}
          </select>
        </label>
        <label>
          Source
          <input type="search" value={sourceText} onChange={(e) => setSourceText(e.target.value)} />
        </label>
        <label>
          Sort
          <select value={sort} onChange={(e) => set({ sort: e.target.value || undefined })}>
            <option value="">Newest</option>
            <option value="confidence">Highest confidence</option>
          </select>
        </label>
        <button type="submit">Apply filters</button>
      </form>
      {pass.error && <ErrorNote error={pass.error} />}
      <div aria-live="polite">
        {note && (
          <p className="note ok">
            {note.text} {note.runId && <Link to={`/runs/${note.runId}`}>Follow the run</Link>}
            {note.undo && (
              <button type="button" disabled={pass.busy} onClick={() => void undo(note.undo!)}>
                Undo
              </button>
            )}
          </p>
        )}
      </div>
      <Panel
        title="Leads"
        id="leads-list"
        what="the leads"
        res={res}
        retry={retry}
        actions={
          <div className="panel-actions">
            <Button size="sm" icon={ClipboardCheck} disabled={pass.busy || noneNew || running.evaluate} title={running.evaluate ? RUNNING.evaluate : noneNew ? "No lead is new." : undefined} onClick={() => void batch("evaluate")}>
              Evaluate new leads
            </Button>
            <Button size="sm" icon={Building2} disabled={pass.busy || noneNew || running.expand} title={running.expand ? RUNNING.expand : noneNew ? "No lead is new." : undefined} onClick={() => void batch("expand")}>
              Read new careers pages
            </Button>
            <Link to="/runs">Runs</Link>
          </div>
        }
      >
        {(data) => (
          <>
            {Object.keys(data.counts).length > 0 && (
              <FilterChips
                label="Leads by category"
                items={Object.entries(data.counts)
                  .filter(([c, n]) => n > 0 || c === category)
                  .map(([c, n]) => ({ label: humanize(c), count: n, to: `/leads?${new URLSearchParams({ ...(state !== "new" ? { state } : {}), ...(source ? { source } : {}), ...(q ? { q } : {}), ...(sort ? { sort } : {}), category: c }).toString()}`, current: c === category }))}
              />
            )}
            {data.rows.length === 0 ? (
              category || source || q || state !== "new" ? (
                <EmptyState icon={Radar} title="No leads match these filters." action={<Link to="/leads">Clear filters</Link>} />
              ) : (
                <EmptyState icon={Radar} title="No new lead." action={<Link to="/discovery" className="button primary">Run a scan</Link>}>
                  Scans and hiring signals add leads here.
                </EmptyState>
              )
            ) : (
              <>
                <p aria-live="polite" className="count-line">
                  {data.total} {data.total === 1 ? "lead" : "leads"}
                </p>
                <ul className="entry-list plain">
                  {data.rows.map((l) => (
                    <LeadRow key={l.id} l={l} running={running} onChange={retry} onRun={setNote} />
                  ))}
                </ul>
                <Pager page={data.page} pageSize={data.pageSize} total={data.total} onPage={toPage} />
              </>
            )}
          </>
        )}
      </Panel>
    </>
  );
}
