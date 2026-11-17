import { useEffect, useRef, useState, type FormEvent } from "react";
import { del, get, newRequestId, request } from "../../api/client";
import { kbDocument, kbDocumentList, kbUndo } from "../../api/decoders_pages";
import { runAccepted } from "../../api/decoders_runs";
import { useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { Markdown } from "../../ui/Markdown";
import { ConfirmDialog } from "../../ui/Modal";
import { Empty, ErrorNote, ExternalLink, Panel, ageLabel } from "../../ui/parts";
import { SortHeader } from "../../ui/SortHeader";
import { KBDocBrief, type KBDocEntry } from "../../wire";
import { useAction } from "../action";
import { useRunLines } from "../discovery/runLines";
import { UndoDelete, type Gone } from "./Pages";

// The model task a brief build runs under. The route fixes it, so the page can
// name it before the call returns (internal/kb BriefModelTask).
const BRIEF_TASK = "research-search";

function Viewer({ entry, onClose }: { entry: KBDocEntry; onClose: () => void }) {
  const [res, reload] = useResource(() => get(`/api/kb/documents/${encodeURIComponent(entry.id)}`, kbDocument), [entry.id]);
  const at = useRef<HTMLDivElement>(null);
  useEffect(() => {
    at.current?.scrollIntoView?.({ block: "start" });
  }, []);
  return (
    <div ref={at}>
    <Panel title={`Brief: ${entry.title || entry.id}`} id={`kb-brief-${entry.id}`} res={res} retry={reload} actions={<button type="button" onClick={onClose}>Close</button>}>
      {(doc) => (
        <div>
          <Markdown source={doc.body} />
          <h3>Sources</h3>
          {doc.entry.sourceUrl ? (
            <ul className="plain">
              <li>
                <ExternalLink href={doc.entry.sourceUrl}>{doc.entry.sourceUrl}</ExternalLink>
              </li>
            </ul>
          ) : (
            <p className="muted">This brief was written by hand, so it has no source page.</p>
          )}
        </div>
      )}
    </Panel>
    </div>
  );
}

export function Briefs() {
  const [res, reload] = useResource(() => get("/api/kb/documents", kbDocumentList), []);
  const [company, setCompany] = useState("");
  const [link, setLink] = useState("");
  const [run, setRun] = useState<string>();
  const act = useAction();
  const { final } = useRunLines(run, reload);
  // A build started before the page opened is followed like one started here.
  const building = res.data?.briefRunId;
  useEffect(() => {
    if (building && !run) setRun(building);
  }, [building]);
  const busy = act.busy || (run !== undefined && !final);
  const [open, setOpen] = useState<KBDocEntry | null>(null);
  const [query, setQuery] = useState("");
  const [byCompany, setByCompany] = useState(false);
  const [doomed, setDoomed] = useState<KBDocEntry | null>(null);
  const [gone, setGone] = useState<Gone | null>(null);
  const remover = useAction();
  const remove = async (b: KBDocEntry) => {
    setGone(null);
    const out = await remover.run(() => del(`/api/kb/documents/${encodeURIComponent(b.id)}`, kbUndo));
    if (!out) return;
    const name = b.title || b.id;
    setGone({ undo: out.undo, done: `Deleted ${name}.`, undone: `${name} is back.` });
    if (open?.id === b.id) setOpen(null);
    reload();
  };
  const companyField = useRef<HTMLInputElement>(null);
  // Refreshing a brief fills the form with its company and posting; the user
  // presses the button, because a build can call a model.
  const refill = (b: KBDocEntry) => {
    setCompany((b.title || b.id).replace(/ company brief$/i, ""));
    setLink(b.sourceUrl ?? "");
    companyField.current?.focus();
  };

  // An address ?company=...&url=... (an interview pack missing its brief) fills
  // the form the same way.
  const [params, setParams] = useSearchParams();
  const offered = params.get("company");
  useEffect(() => {
    if (offered == null) return;
    setCompany(offered);
    setLink(params.get("url") ?? "");
    setParams({ company: undefined, url: undefined }, { replace: true });
  }, [offered]);

  const build = async (e: FormEvent) => {
    e.preventDefault();
    setRun(undefined);
    const body = { company: company.trim(), jobUrl: link.trim(), refresh: true };
    const out = await act.run(() => request("POST", "/api/kb/briefs", runAccepted, { body, headers: { "Idempotency-Key": newRequestId() } }));
    if (out) setRun(out.runId);
  };

  return (
    <Panel title="Company briefs" id="kb-briefs" res={res} retry={reload}>
      {(list) => {
        const briefs = list.entries.filter((e) => e.kind === KBDocBrief);
        const nameOf = (b: KBDocEntry) => b.title || b.id;
        const q = query.trim().toLowerCase();
        const shown = briefs.filter((b) => [nameOf(b), b.id, b.sourceUrl].some((s) => s.toLowerCase().includes(q)))
          .sort((a, b) => (byCompany ? nameOf(a).localeCompare(nameOf(b)) : Date.parse(b.modifiedAt) - Date.parse(a.modifiedAt)));
        return (
          <div>
            <form onSubmit={build} className="form-grid" aria-label="Generate a brief">
              <label>
                Company
                <input ref={companyField} value={company} onChange={(e) => setCompany(e.target.value)} />
              </label>
              <label>
                Link to one of its postings
                <input value={link} onChange={(e) => setLink(e.target.value)} placeholder="https://jobs.example.com/role" />
              </label>
              <p className="muted">The company&rsquo;s own website is read from the posting, never guessed from its name.</p>
              <p className="muted" role="status">
                {busy ? <>Building the brief with the {BRIEF_TASK} model task. It goes on if you leave this page.{run && <> <Link to={`/runs/${run}`}>Follow the run</Link></>}</>
                  : <>A brief is condensed with the {BRIEF_TASK} model task when one is routed (<Link to="/settings#settings-models">Settings, Models</Link>).</>}
              </p>
              <div className="actions">
                <button type="submit" disabled={busy || company.trim() === "" || link.trim() === ""}>
                  Generate or refresh brief
                </button>
              </div>
            </form>
            <div aria-live="polite">
              {final?.status === "succeeded" && <p className="note ok">{final.outcome}</p>}
              {final?.status === "failed" && <p className="note">{final.reason}</p>}
              {final?.status === "cancelled" && <p className="muted">The brief was cancelled.</p>}
              {act.error && <ErrorNote error={act.error} />}
              {gone && <UndoDelete key={gone.undo} gone={gone} onBack={reload} />}
              {remover.error && <ErrorNote error={remover.error} />}
            </div>
            {briefs.length > 0 && (
              <div className="toolbar">
                <label className="search-field">
                  Search briefs by company or site
                  <input type="search" data-page-search value={query} onChange={(e) => setQuery(e.target.value)} />
                </label>
                <span className="count-line">{q === "" ? briefs.length : `${shown.length} of ${briefs.length}`} {briefs.length === 1 ? "brief" : "briefs"}</span>
              </div>
            )}
            {briefs.length === 0 ? (
              <Empty>No company brief yet. Enter a company and a link to one of its postings above to build the first one.</Empty>
            ) : shown.length === 0 ? (
              <Empty>No brief matches the search.</Empty>
            ) : (
              <table className="cards">
                <caption className="sr-only">Company briefs</caption>
                <thead>
                  <tr>
                    <SortHeader label="Company" ascending sorted={byCompany} onSort={() => setByCompany(true)} />
                    <SortHeader label="Updated" sorted={!byCompany} onSort={() => setByCompany(false)} />
                    <th scope="col">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {shown.map((b) => (
                    <tr key={b.id}>
                      <td data-label="Company">{b.title || b.id}</td>
                      <td data-label="Updated">{ageLabel(b.modifiedAt)}</td>
                      <td>
                        <div className="actions">
                          <button type="button" onClick={() => setOpen(b)} aria-label={`View ${b.title || b.id}`}>
                            View
                          </button>
                          {b.sourceUrl && (
                            <button type="button" onClick={() => refill(b)} aria-label={`Refresh ${b.title || b.id}`}>
                              Refresh
                            </button>
                          )}
                          <button type="button" className="danger-outline" disabled={remover.busy} onClick={() => setDoomed(b)} aria-label={`Delete ${b.title || b.id}`}>
                            Delete
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {open && <Viewer key={open.id} entry={open} onClose={() => setOpen(null)} />}
            {doomed && <ConfirmDialog title={`Delete ${doomed.title || doomed.id}`} verb="Delete brief"
              consequence="The brief file and everything the search index holds from it are removed. You can undo it for a few minutes."
              onCancel={() => setDoomed(null)} onConfirm={() => { const b = doomed; setDoomed(null); void remove(b); }} />}
          </div>
        );
      }}
    </Panel>
  );
}
