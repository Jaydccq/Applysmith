import { useId, useState } from "react";
import { fetchBlob, get, newRequestId, post, request } from "../api/client";
import { candidacy, candidacyDetail, documentList } from "../api/decoders";
import { runAccepted } from "../api/decoders_runs";
import { documentToolchain } from "../api/decoders_setup";
import { useResource } from "../hooks";
import { leave, Link } from "../router";
import { Modal } from "../ui/Modal";
import { Chip, Empty, ErrorNote, formatDate, Loading } from "../ui/parts";
import { RevealButton } from "../ui/Reveal";
import { ContentReviewUnavailable, ReasonBelowScoreFloor, ReasonToolchainUnavailable, ReviewPerformed } from "../wire";
import type { DocumentCheck, DocumentReview, DocumentView } from "../wire";
import { useAction } from "./action";
import { PdfPreview } from "./documents/Preview";
import { useDocumentRun, type DocumentRunState } from "./documents/run";
import { Refine } from "./documents/Refine";
import { VersionText } from "./documents/Text";
import { RenderTest } from "./setup/Toolchain";

const KIND: Record<string, string> = { resume: "Résumé", "cover-letter": "Cover letter" };
const TOOL: Record<string, string> = { verify_pdf: "Text layer", verify_layout: "Layout", final: "Content" };
const RESULT: Record<string, string> = { pass: "Pass", fail: "Fail", unverified: "Unverified" };
// What a regeneration makes: the pair, or one document by itself.
type Ask = "pair" | "resume" | "cover-letter";
const ASKED: Record<Ask, string> = { pair: "The new résumé and cover letter", resume: "The new résumé", "cover-letter": "The new cover letter" };

// What the user is told about a PDF. A green label needs the stored verdict to
// agree: a PDF whose verdict is missing or no longer matches reads as
// unverified even when the row says ready.
type Shown = { label: string; tone: "good" | "warn" | "bad" | "neutral" };

function shownState(d: DocumentView): Shown {
  if (d.pdfState === "none") return { label: "No PDF yet", tone: "neutral" };
  if (d.pdfState === "unavailable") return { label: "No PDF", tone: "neutral" };
  const v = d.verification;
  const state = !v || v.stale ? "unverified" : d.pdfState === "ready" ? v.state : d.pdfState;
  if (state === "ready") return { label: "Checks passed", tone: "good" };
  if (state === "failed") return { label: "Checks failed", tone: "bad" };
  return { label: "Not verified", tone: "warn" };
}

const short = (hash: string | undefined) => (hash ? hash.slice(0, 12) : "none");

function Checks({ checks }: { checks: DocumentCheck[] }) {
  const count = (s: string) => checks.filter((c) => c.status === s).length;
  const table = (
    <table className="cards">
      <caption className="sr-only">Mechanical checks</caption>
      <thead>
        <tr>
          <th scope="col">Check</th>
          <th scope="col">Result</th>
          <th scope="col">Detail</th>
        </tr>
      </thead>
      <tbody>
        {checks.map((c) => (
          <tr key={`${c.tool}/${c.id}`}>
            <td data-label="Check">
              {TOOL[c.tool] ?? c.tool}: {c.id}
            </td>
            <td data-label="Result">
              <Chip tone={c.status === "pass" ? "good" : c.status === "fail" ? "bad" : "warn"}>{RESULT[c.status] ?? c.status}</Chip>
            </td>
            <td data-label="Detail">{c.reason ?? ""}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
  // A table of passes says nothing the count does not; it opens on request.
  return (
    <>
      <p aria-live="polite">
        {count("pass")} passed, {count("fail")} failed, {count("unverified")} unverified of {checks.length} checks.
      </p>
      {count("pass") === checks.length ? (
        <details>
          <summary>Show the checks</summary>
          {table}
        </details>
      ) : (
        table
      )}
    </>
  );
}

// The refusal names a command-line override; here the button beside it is the
// override, so the page says what happened in its own words.
const BELOW_FLOOR = "This posting scored below 4.5, where tailored PDFs start, so none was made.";
const UNDER_FLOOR = "This posting scored below 4.5, where tailored PDFs start, so none is made until you allow it.";

// A refusal or a run that names missing PDF tools points at the Setup check
// that lists them with how to install each. When every tool is installed and
// only the render test is due (a tool or template changed), it runs here.
function ToolsFix({ reason }: { reason?: string }) {
  if (reason !== ReasonToolchainUnavailable) return null;
  return <ToolsState />;
}

function ToolsState() {
  const [res, reload] = useResource(() => get("/api/documents/toolchain", documentToolchain), [], ["setup"]);
  if (res.status === "loading") return null;
  if (res.data?.state === "verification-needed") return <RenderTest onDone={reload} />;
  if (res.data?.state === "ready") return <p role="status">The PDF tools passed their render test; try again.</p>;
  return (
    <p className="muted">
      What PDFs need on this computer and how to set it up: <Link to="/setup?step=resumes">Setup, Résumé PDFs</Link>.
    </p>
  );
}

function Verification({ d }: { d: DocumentView }) {
  const v = d.verification;
  if (d.pdfState === "none") return <p className="muted">Checks are pending. No PDF has been made for this version, so there is nothing to verify yet.</p>;
  if (!v) return <p className="muted">This version has no stored PDF checks.</p>;
  return (
    <>
      <h4>Mechanical checks</h4>
      <p className="muted">These check that the file can be parsed and laid out as intended. They are not a visual review and do not guarantee that every applicant tracking system accepts the file.</p>
      {v.stale && (
        <p role="note" className="note error">
          The stored checks no longer describe this file ({v.stale}), so it counts as not verified until it is checked again.
        </p>
      )}
      {v.checks.length > 0 && <Checks checks={v.checks} />}
      {v.checks.some((c) => c.status === "unverified") && <p className="muted">An unverified check could not run. It is not a pass.</p>}

      <h4>Page previews</h4>
      {v.previews.length === 0 ? (
        <p className="muted">No page previews were kept.</p>
      ) : (
        <ul>
          {v.previews.map((p) => (
            <li key={p.name}>
              Page {p.page}: {p.name} (hash {short(p.sha256)}), kept in {d.path}
            </li>
          ))}
        </ul>
      )}

      <h4>Visual review</h4>
      {v.visualReview.status === ReviewPerformed ? (
        <p>Performed on this PDF (hash {short(v.visualReview.pdfSha256)}).</p>
      ) : (
        <p>Pending. Nobody has looked at the page previews of this PDF yet; press Preview PDF and read each page before you send it, since the checks above do not replace that.</p>
      )}
    </>
  );
}

// What the independent content review did. A review that did not run is
// "not run", never a pass; it adds to the mechanical checks and replaces none.
function reviewLine(r: DocumentReview | undefined): string {
  if (!r) return "No review record.";
  if (r.status === ContentReviewUnavailable) return `Not run${r.reason ? ` (${r.reason})` : ""}. That is not an approval.`;
  const found = `${r.factual} factual or wording ${r.factual === 1 ? "finding" : "findings"}, ${r.narrative} narrative ${r.narrative === 1 ? "suggestion" : "suggestions"} (never applied automatically)`;
  const rejected = r.rejected > 0 ? `, ${r.rejected} rejected as unsupported` : "";
  const revision = r.revisions > 0 ? "One grounded revision was applied." : "No revision was applied.";
  return `One independent review: ${found}${rejected}. ${revision}`;
}

function Revisions({ d }: { d: DocumentView }) {
  const v = d.verification;
  return (
    <details>
      <summary>Revision details{d.warnings.length > 0 ? ` and ${d.warnings.length} ${d.warnings.length === 1 ? "note" : "notes"}` : ""}</summary>
      {d.warnings.length > 0 && (
        <ul className="note">
          {d.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
      <dl className="facts">
        <dt>Source revision</dt>
        <dd>
          Version {d.version}, content {short(d.contentHash)}, evidence {short(d.manifestHash)}
        </dd>
        <dt>PDF revision</dt>
        <dd>{d.pdfSha256 ? `${short(d.pdfSha256)}, ${d.pages ?? 0} ${d.pages === 1 ? "page" : "pages"}, ${d.paper.toUpperCase()}` : "No PDF"}</dd>
        <dt>Content review</dt>
        <dd>{reviewLine(d.review)}</dd>
        {v && !v.stale && (
          <>
            <dt>Checked with</dt>
            <dd>
              Rules {v.verifiers.rules}, text script {v.verifiers.pdfScript}, layout script {v.verifiers.layoutScript} ({v.verifiers.layoutProfile})
              {v.verifiers.poppler ? `, ${v.verifiers.poppler}` : ""}
            </dd>
          </>
        )}
      </dl>
    </details>
  );
}

// VersionActions are the things a user does with one version: read and edit
// its text, make its PDF (which stores it under documents/) and look at the
// stored PDF. evaluate, when set, is the address that evaluates the posting: the
// application links no evaluation report, so no PDF can be made and there is no
// floor to override; the button is not offered.
function VersionActions({ d, run, evaluate }: { d: DocumentView; run: DocumentRunState; evaluate?: string }) {
  const [panel, setPanel] = useState<"" | "text" | "pdf" | "refine">("");
  const pdf = useAction();
  const save = useAction();
  const toggle = (p: "text" | "pdf" | "refine") => leave(() => setPanel(panel === p ? "" : p));
  // A current PDF that did not pass can be rendered again; the checks that did
  // not pass are named beside the button.
  const again = d.current && ["bad", "warn"].includes(shownState(d).tone);
  // Only the newest version makes a PDF: the run publishes a version, so one of
  // an earlier text would replace the newer one.
  const missing = d.current && (d.pdfState === "none" || again);
  const offer = missing && !evaluate;
  const notPassed = d.verification && !d.verification.stale ? d.verification.checks.filter((c) => c.status !== "pass") : [];
  const makePdf = async () => {
    const accepted = await pdf.run(() => request("POST", `/api/documents/${d.id}/pdf`, runAccepted, { body: {}, headers: { "Idempotency-Key": newRequestId() } }));
    if (accepted) run.begin(accepted.runId, "The PDF");
  };
  // A PDF refused below the score floor can be allowed here: the override is
  // recorded on the application, then the PDF is asked for again.
  const below = pdf.error?.details?.reason === ReasonBelowScoreFloor && d.candidacyId !== undefined;
  const allow = async () => {
    if (await pdf.run(() => post(`/api/candidacies/${d.candidacyId}/documents-override`, candidacy))) await makePdf();
  };
  // Named by bundle so one application's PDF is told from another's.
  const download = async () => {
    const blob = await save.run(() => fetchBlob(`/api/documents/pdf/${d.id}`));
    if (!blob) return;
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([blob], { type: "application/pdf" }));
    a.download = `${d.bundle}-${d.kind}-v${d.version}.pdf`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 0);
  };
  return (
    <>
      <div className="actions">
        <button type="button" aria-expanded={panel === "text"} onClick={() => toggle("text")}>
          {panel === "text" ? "Hide text" : "Show text"}
        </button>
        <button type="button" aria-expanded={panel === "refine"} disabled={d.frozen || !d.current} aria-describedby={`revise-${d.id}`} onClick={() => toggle("refine")}>
          {panel === "refine" ? "Hide revisions" : "Revise with an instruction"}
        </button>
        {d.pdfPath && (
          <button type="button" aria-expanded={panel === "pdf"} onClick={() => toggle("pdf")}>
            {panel === "pdf" ? "Hide preview" : "Preview PDF"}
          </button>
        )}
        {d.pdfPath && (
          <button type="button" disabled={save.busy} onClick={() => void download()}>
            Download PDF
          </button>
        )}
        {offer && (
          <button type="button" disabled={pdf.busy || run.busy || d.refinePending} aria-describedby={`pdf-${d.id}`} onClick={makePdf}>
            {again ? "Make the PDF again" : "Make PDF"}
          </button>
        )}
      </div>
      {d.pdfPath && (
        <p className="muted">
          PDF saved at {d.pdfPath}. <RevealButton path={d.pdfPath} label={`Reveal the PDF of ${KIND[d.kind] ?? d.kind}, version ${d.version} in Finder`} />
        </p>
      )}
      {evaluate && missing && (
        <p className="muted">
          No PDF can be made yet: this application has no evaluation report, and a PDF needs the posting evaluated at 4.5 or more. <Link to={evaluate}>Evaluate the posting</Link> first.
        </p>
      )}
      {offer && (
        <p id={`pdf-${d.id}`} className="muted">
          {d.refinePending
            ? "The PDF is unavailable while a revision waits for your decision. Accept or discard it under Revise with an instruction."
            : again
              ? `Not passed: ${notPassed.length > 0 ? notPassed.map((c) => `${TOOL[c.tool] ?? c.tool} ${c.id}${c.reason ? ` (${c.reason})` : ""}`).join("; ") : "the stored checks do not describe this file"}. Making the PDF again renders this same text into a new version and checks it again; this one stays as it is. A check that could not run, or a stale verdict, can pass then. A check that failed on the text itself, such as clipping or a missing line, fails again: edit the text under Show text, then make the PDF of the new draft.`
              : "Making the PDF adds a new version of this text with the PDF, saved under documents/."}
        </p>
      )}
      <p id={`revise-${d.id}`} className="muted" hidden={!d.frozen && d.current}>
        {d.frozen ? "Attached to a submitted application, so it cannot be revised." : "A newer version exists; revise, edit or make the PDF of that one."}
      </p>
      <div aria-live="polite">
        {(pdf.error ?? save.error) && !below && <ErrorNote error={pdf.error ?? save.error} />}
        <ToolsFix reason={pdf.error?.details?.reason} />
        {below && (
          <p className="actions">
            <button type="button" disabled={pdf.busy || run.busy} onClick={() => void allow()}>
              Allow a PDF anyway
            </button>
            <span className="muted">{BELOW_FLOOR} Allowing one for this posting is recorded on the application.</span>
          </p>
        )}
      </div>
      {panel === "refine" && <Refine id={d.id} kind={d.kind} run={run} />}
      {panel === "text" && <VersionText id={d.id} run={run} current={d.current} />}
      {panel === "pdf" && <PdfPreview key={d.id} path={`/api/documents/pdf/${d.id}`} title={`PDF of ${KIND[d.kind] ?? d.kind}, version ${d.version}`} />}
    </>
  );
}

// alone, when set, regenerates this document by itself: it is the only current
// one made before its sources changed.
function Version({ d, run, alone, busy, evaluate }: { d: DocumentView; run: DocumentRunState; alone?: () => void; busy?: boolean; evaluate?: string }) {
  const shown = shownState(d);
  const heading = `${KIND[d.kind] ?? d.kind}, version ${d.version}`;
  return (
    <section aria-label={heading} className="panel">
      <header className="panel-head">
        <h3>{heading}</h3>
        <Chip tone={shown.tone}>{shown.label}</Chip>
      </header>
      <p className="muted">
        Made {formatDate(d.createdAt)}
        {d.frozen ? ", attached to a submitted application" : ""}.{d.generation === "deterministic" ? " Written without a model; Revision details says why." : ""}
      </p>
      {d.sourcesChanged && d.sourcesChanged.length > 0 && (
        <p role="note" className="note">
          Made before {new Intl.ListFormat("en").format(d.sourcesChanged)} changed. Regenerate to write from the new text; this version stays as it is.
          {alone && (
            <>
              {" "}
              <button type="button" className="small" disabled={busy} onClick={alone}>
                Regenerate only the {(KIND[d.kind] ?? d.kind).toLowerCase()}
              </button>
            </>
          )}
        </p>
      )}
      <Revisions d={d} />
      <VersionActions d={d} run={run} evaluate={evaluate} />
      <Verification d={d} />
    </section>
  );
}

// DocumentsDrawer shows the stored documents of one application: which source
// and PDF each version is, what the mechanical checks found, and, apart from
// them, whether anyone has looked at the page previews. It also reads and
// edits the text of a version, makes its PDF and regenerates the pair; every
// one of those adds a version and none changes an earlier one. returnFocus is
// where it sends focus when it closes after the address opened it (Modal).
export function DocumentsDrawer({ candidacyId, label, onClose, returnFocus }: { candidacyId: number; label: string; onClose: () => void; returnFocus?: () => HTMLElement | null | undefined }) {
  const [res, reload] = useResource(() => get("/api/documents", documentList, { candidacy: candidacyId }), [candidacyId], ["tracker"]);
  const run = useDocumentRun(reload, res.data?.activeRunId);
  const docs = res.data?.documents ?? [];
  const earlier = docs.filter((d) => !d.current);
  const regenerate = useAction();
  const [posting, setPosting] = useState("");
  const [asked, setAsked] = useState<Ask>("pair");
  const current = docs.filter((d) => d.current).sort((a, b) => Number(b.kind === "resume") - Number(a.kind === "resume"));
  const dated = current.filter((d) => (d.sourcesChanged ?? []).length > 0);
  // With no report, Home evaluates the row's posting from its address (/?post=).
  const noReport = res.data?.noReport;
  const [row] = useResource(async () => (noReport ? get(`/api/candidacies/${candidacyId}`, candidacyDetail) : null), [candidacyId, noReport]);
  const evaluate = noReport ? `/?post=${encodeURIComponent(row.data?.entry.postingUrl ?? "")}` : undefined;
  // One run makes the pair and both PDFs, or one document when only it is out
  // of date. A run refused below the score floor before it starts can be
  // allowed here: the override is recorded on the application, then the same
  // run is asked for again. An application with no report to read the posting
  // from takes the pasted posting instead, without a PDF: the floor needs an
  // evaluated posting.
  const start = async (what: Ask = asked) => {
    setAsked(what);
    const pasted = posting.trim();
    const body = { candidacyId, pdf: !pasted, ...(what === "pair" ? { includeCoverLetter: true } : {}), ...(pasted ? { posting: pasted } : {}) };
    const path = what === "cover-letter" ? "/api/documents/cover-letter" : "/api/documents/resume";
    const accepted = await regenerate.run(() => request("POST", path, runAccepted, { body, headers: { "Idempotency-Key": newRequestId() } }));
    if (accepted) run.begin(accepted.runId, ASKED[what]);
  };
  // The floor is known before a run is asked for, so the override is offered at once.
  const refused = regenerate.error?.details?.reason === ReasonBelowScoreFloor;
  const below = refused || (!!res.data?.belowFloor && !regenerate.error && !run.busy);
  // Under the floor every run asks for a PDF and is refused, so the way on is
  // Allow a PDF anyway; the button says why it waits.
  const held = !!res.data?.belowFloor;
  const floorNote = useId();
  const needsPosting = regenerate.error?.field === "posting";
  const allow = async () => {
    if (await regenerate.run(() => post(`/api/candidacies/${candidacyId}/documents-override`, candidacy))) await start();
  };
  return (
    <Modal title={`Documents: ${label}`} variant="drawer" onClose={() => leave(onClose)} returnFocus={returnFocus}>
      <div className="actions">
        <button type="button" disabled={regenerate.busy || run.busy || held} aria-describedby={held && below ? floorNote : undefined} onClick={() => void start("pair")}>
          {res.data && docs.length === 0 ? "Make résumé and cover letter" : "Regenerate résumé and cover letter"}
        </button>
      </div>
      <div aria-live="polite">
        {run.busy && (
          <p role="status">
            Working on {run.what.toLowerCase()}{run.stage ? `: ${run.stage}` : ""}. {run.seconds} s so far
            {run.left > 0 ? `, at most ${run.left < 90 ? `${run.left} s` : `${Math.ceil(run.left / 60)} min`} left` : ""}. <Link to={`/runs/${run.runId}`}>Follow the run</Link>
          </p>
        )}
        {run.failure && <p className="note error">{run.failure}</p>}
        {regenerate.error && !below && !needsPosting && <ErrorNote error={regenerate.error} />}
        <ToolsFix reason={run.failure ? run.reason : regenerate.error?.details?.reason} />
        {below && (
          <p className="actions">
            <button type="button" disabled={regenerate.busy || run.busy} onClick={() => void allow()}>
              Allow a PDF anyway
            </button>
            <span id={floorNote} className="muted">{refused ? BELOW_FLOOR : UNDER_FLOOR} Allowing them for this posting is recorded on the application.</span>
          </p>
        )}
      </div>
      {needsPosting && (
        <form
          className="form-grid"
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          <p className="note">
            This application has no evaluation report to read the job description from. Paste the posting to make the text from it. A PDF needs the posting evaluated at 4.5 or
            more, so none is made now.
          </p>
          <label>
            Job description
            <textarea className="editor" rows={6} value={posting} onChange={(e) => setPosting(e.target.value)} />
          </label>
          <div className="actions">
            <button type="submit" disabled={posting.trim() === "" || regenerate.busy || run.busy}>
              Make {asked === "pair" ? "them" : "it"} from this description
            </button>
          </div>
        </form>
      )}
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="the documents" />}
      {res.data && docs.length === 0 && (
        <Empty>
          {run.busy ? "No documents yet: the run above is making them." : `No documents have been made for this application. ${needsPosting ? "Paste its job description above to make them from it." : "Make them with the button above."}`}
        </Empty>
      )}
      {current.map((d) => (
        <Version key={d.id} d={d} run={run} evaluate={evaluate} busy={regenerate.busy || run.busy} alone={dated.length === 1 && dated[0] === d ? () => void start(d.kind as Ask) : undefined} />
      ))}
      {earlier.length > 0 && (
        <details>
          <summary>Earlier versions ({earlier.length})</summary>
          {earlier.map((d) => <Version key={d.id} d={d} run={run} evaluate={evaluate} />)}
        </details>
      )}
      <div className="actions">
        <button type="button" onClick={() => leave(onClose)}>
          Close
        </button>
      </div>
    </Modal>
  );
}
