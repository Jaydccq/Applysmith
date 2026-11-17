import { useId, useState, type FormEvent } from "react";
import { ApiError, get, post, postModel } from "../../api/client";
import { candidacy } from "../../api/decoders";
import { applyAnswerSaved, applyRunView } from "../../api/decoders_apply";
import { answerComposeResult } from "../../api/decoders_bank";
import { browserStatus } from "../../api/decoders_browser";
import { runAccepted } from "../../api/decoders_runs";
import { asApiError, useInterval, useResource } from "../../hooks";
import { Link, navigate } from "../../router";
import { CopyButton } from "../../ui/CopyButton";
import { useDemo } from "../../ui/DemoBanner";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import { useUnsaved } from "../../ui/SaveBar";
import { KeepEdits } from "../answerbank/Candidates";
import { showTab } from "../applynext/showTab";
import { ShowInPanel, Still } from "../browser/Lanes";
import { durationMs, outcomeLine, ownerWords } from "../applynext/words";
import { BatchPanel } from "./BatchPanel";
import { RetryDialog, RetrySubmitDialog, SubmitKeptDialog } from "./RetryDialogs";
import { stageName } from "./format";
import { ComposeFilled, ComposeNoRoute, type ApplyQuestion } from "../../wire";

const POLL_MS = 3000;
const TAB_EVERY_MS = 5000;

// Where a run was started from (internal/gates, Caller).
const CALLERS: Record<string, string> = { ask_agent: "Ask Agent", apply_list: "The apply list", cli: "The command line", dashboard: "The dashboard", scheduler: "A schedule", telegram: "Telegram" };

const STAGE_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { ok: "good", done: "good", passed: "good", failed: "bad", stopped: "bad", skipped: "neutral" };

// RunPage shows the page of the apply tab this run holds: live while the run
// fills it, as it was left when the run kept it for you. Only the browser
// status is asked, which never starts Chrome; no tab, no section.
function RunPage({ id, working }: { id: string; working: boolean }) {
  const [res, reload] = useResource(() => get("/api/browser/status", browserStatus), [], ["browser"]);
  useInterval(reload, TAB_EVERY_MS, working);
  const tab = res.data?.apply.tabs.find((t) => t.owner === id);
  if (!tab) return null;
  return (
    <section aria-labelledby="run-page">
      <h3 id="run-page">Page</h3>
      <p className="muted">
        {tab.kept ? "The tab kept for you, as the run left it" : "What the run is filling now"}: {tab.url}
      </p>
      <Still lane="apply" tab={tab} />
    </section>
  );
}

// QuestionRow saves the user's answer to one question a run stopped on as
// their own Answer Bank entry, on every site unless the server says the
// question names the company (F-4). A choice of up to six options is radios, a
// longer one (a school list) a dropdown. note is a line under the question;
// onSaved hears whether the run went on. With posting given, a free-text
// question the composer would draft offers Draft (Drafter).
export function QuestionRow({ runId, host, q, note, posting, onSaved }: { runId: string; host: string; q: ApplyQuestion; note?: string; posting?: { company?: string; role?: string }; onSaved: (continued: boolean) => void }) {
  const id = useId();
  const [value, setValue] = useState("");
  const [everywhere, setEverywhere] = useState(q.everywhere);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  // saved is the answer last saved, so a row whose reload has not come back
  // yet does not hold the page; a typed answer not saved does.
  const [saved, setSaved] = useState("");
  const options = q.options ?? [];
  const label = `${id}-q`;
  const ask = useUnsaved(options.length === 0 && !q.answeredBy && !busy && value.trim() !== "" && value !== saved);
  if (q.answeredBy) {
    return (
      <li>
        <p className="q">{q.question}</p>
        <p className="muted">Your Answer Bank answers this now{q.answer ? `: ${q.answer}` : ""}.</p>
      </li>
    );
  }
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(null);
    try {
      const r = await post(`/api/apply/runs/${encodeURIComponent(runId)}/answers`, applyAnswerSaved, { question: q.question, answer: value.trim(), everywhere });
      setSaved(value);
      onSaved(Boolean(r.continued));
    } catch (err) {
      setFailure(asApiError(err));
    } finally {
      setBusy(false);
    }
  }
  const text = { "aria-labelledby": label, value, onChange: (e: { target: { value: string } }) => setValue(e.target.value) };
  return (
    <li>
      <form className="field" onSubmit={(e) => void save(e)}>
        <p className="q" id={label}>
          {q.question}
        </p>
        {note && <p className="muted">{note}</p>}
        {options.length > 0 && options.length <= 6 ? (
          <fieldset className="choice" aria-labelledby={label}>
            {options.map((o) => (
              <label key={o} className="check">
                <input type="radio" name={`${id}-a`} checked={value === o} onChange={() => setValue(o)} /> {o}
              </label>
            ))}
          </fieldset>
        ) : options.length > 0 ? (
          <select {...text}>
            <option value="">Choose an option</option>
            {options.map((o) => (
              <option key={o}>{o}</option>
            ))}
          </select>
        ) : q.kind === "textarea" ? (
          <textarea rows={3} {...text} />
        ) : (
          <input {...text} />
        )}
        {posting && q.draftable && <Drafter question={q.question} host={host} posting={posting} value={value} onDraft={setValue} />}
        <label className="check">
          <input type="checkbox" checked={everywhere} onChange={(e) => setEverywhere(e.target.checked)} /> Use this answer on every site
        </label>
        <span className="help">Otherwise it answers this question on {host} only.</span>
        {failure && <ErrorNote error={failure} />}
        <p className="actions">
          <button type="submit" disabled={busy || !value.trim()}>
            Save to Answer Bank
          </button>
        </p>
        {ask}
      </form>
    </li>
  );
}

// WHY words for a draft the composer withheld (wire Compose* reasons).
const NO_DRAFT: Record<string, string> = {
  unsupported: "Nothing in your profile, CV or Answer Bank supports an answer to this.",
  "model-abstain": "The model gave no answer.",
  "upstream-error": "The model could not be reached.",
  "budget-exhausted": "The model budget for this session ran out.",
};

// sourceWords names a fact the compose route cited in the user's words. The
// keys are cv.md#<heading>, profile.yml#<path>, profile#<field>, cv#fact-N,
// answer-bank#<id>, correction#N, company-brief and knowledge/<dir>/<file>.md#<heading>;
// any other key is shown as it is.
export function sourceWords(key: string): string {
  const [head, rest = ""] = key.split("#");
  const name = (rest.split(".").pop() ?? "").replace(/-\d+$/, "").replace(/-/g, " ");
  const cap = name.charAt(0).toUpperCase() + name.slice(1);
  if (head === "cv.md") return `your CV (${rest})`;
  if (head === "cv") return "your CV";
  if (head === "profile" || head === "profile.yml") return `your profile (${cap})`;
  if (head === "answer-bank") return "your Answer Bank";
  if (head === "correction") return "your earlier correction";
  if (head === "company-brief") return "the company brief";
  if (head.startsWith("knowledge/")) return `your notes (${rest || head.replace(/^.*\//, "").replace(/\.md$/, "")})`;
  return key;
}

// Drafter writes a draft of a free-text answer from the user's own facts into
// the answer box (POST /api/answers/compose, which changes nothing), names the
// facts it rests on, and rewrites the text in the box on an instruction. The
// user edits it and saves it with Save to Answer Bank; nothing here saves or
// submits. A question the composer would refuse is never offered (q.draftable).
function Drafter({ question, host, posting, value, onDraft }: { question: string; host: string; posting: { company?: string; role?: string }; value: string; onDraft: (text: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [sources, setSources] = useState<string[] | null>(null);
  const [withheld, setWithheld] = useState("");
  const [instruction, setInstruction] = useState("");
  async function compose(rewrite: boolean) {
    setBusy(true);
    setFailure(null);
    setWithheld("");
    try {
      const r = await postModel("/api/answers/compose", answerComposeResult, { question, host, posting, ...(rewrite ? { current: value, instruction: instruction.trim() } : {}) });
      if (r.outcome === ComposeFilled && r.answer) {
        onDraft(r.answer);
        setSources(r.sources);
        setInstruction("");
      } else {
        setWithheld(r.reason ?? "");
      }
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="drafter">
      {sources === null ? (
        <p className="actions">
          <button type="button" className="small" disabled={busy} onClick={() => void compose(false)}>
            {busy ? "Drafting..." : "Draft with evidence"}
          </button>
          <span className="help">Written from your profile, CV and Answer Bank. You edit it; nothing is saved or sent until you save it.</span>
        </p>
      ) : (
        <>
          <p className="muted">{sources.length > 0 ? `Written from: ${[...new Set(sources.map(sourceWords))].join(", ")}.` : "It quotes none of your saved facts by name: check every claim before you save it."}</p>
          <div className="actions">
            <input aria-label="How to change the draft" placeholder="How to change it, e.g. shorter" value={instruction} onChange={(e) => setInstruction(e.target.value)} />
            <button type="button" className="small" disabled={busy || !instruction.trim() || !value.trim()} onClick={() => void compose(true)}>
              {busy ? "Rewriting..." : "Rewrite"}
            </button>
          </div>
        </>
      )}
      <div aria-live="polite">
        {failure && <ErrorNote error={failure} />}
        {withheld === ComposeNoRoute ? (
          <p className="muted">
            No model is routed to the narrative task, so nothing was drafted. <Link to="/settings#settings-models">Settings, Models</Link> routes one.
          </p>
        ) : (
          withheld && <p className="muted">No draft: {NO_DRAFT[withheld] ?? "this question is left to you."}</p>
        )}
      </div>
    </div>
  );
}

// Questions answers, in the run's record, what it stopped on. While the run
// keeps its tab, the last saved answer continues it there; without a tab it
// retries.
function Questions({ v, kept, submits, busy, onSaved, onRetry }: {
  v: { runId: string; host: string; company?: string; role?: string; questions: ApplyQuestion[] };
  kept: boolean;
  submits: boolean;
  busy: boolean;
  onSaved: (continued: boolean) => void;
  onRetry: () => void;
}) {
  const open = v.questions.filter((q) => !q.answeredBy).length;
  return (
    <section aria-labelledby="run-answers">
      <h3 id="run-answers">Answer the questions</h3>
      <p className="muted">
        Each answer is saved to your Answer Bank, so this run and later applications use it. Passwords, codes, self-identification, signatures and contact
        details are left to the tab and your Profile.
      </p>
      {kept && (
        <p className="muted">
          When the last answer is saved, the run goes on in the tab it kept
          {submits ? " and, as you asked when you started it, presses Submit after its checks pass." : " and stops before Submit."}
        </p>
      )}
      <ol className="plain run-answers">
        {v.questions.map((q) => (
          <QuestionRow key={q.question} runId={v.runId} host={v.host} q={q} posting={{ company: v.company, role: v.role }} onSaved={onSaved} />
        ))}
      </ol>
      <p className="actions">
        <span className="muted">{open === 0 ? "Every question has an answer now." : `${open} of ${v.questions.length} still open.`}</span>
        {!kept && (
          <button type="button" className={open === 0 ? "primary" : undefined} disabled={busy} onClick={onRetry}>
            Retry with these answers
          </button>
        )}
      </p>
    </section>
  );
}

// Uploads names the upload slots the form asked for that the run left empty:
// a file is no Answer Bank entry, so the questions leave it out. A cover
// letter left while the row's final report is below the 4.5 floor can be
// allowed here, then retried.
function Uploads({ uploads, letter, busy, onLetter }: { uploads: string[]; letter: boolean; busy: boolean; onLetter: () => void }) {
  return (
    <section aria-labelledby="run-uploads">
      <h3 id="run-uploads">Files the form asks for</h3>
      {uploads.length > 0 && (
        <ul className="plain">
          {uploads.map((u) => (
            <li key={u}>{u}</li>
          ))}
        </ul>
      )}
      <p className="muted">A file is not an Answer Bank answer, so add it in the tab.</p>
      {letter && (
        <p className="actions">
          <span className="muted">The report scores below the 4.5 floor for tailored documents.</span>
          <button type="button" disabled={busy} onClick={onLetter}>
            Allow a tailored cover letter and retry
          </button>
        </p>
      )}
    </section>
  );
}

// ApplyRecord is the application-specific record of a run: the verdict, the
// stage timeline with durations, links to the tracker row and the report, the
// kept tab and the retry. The board entry above it only holds the current stage;
// canSubmit is its offer to submit the filled form waiting in the kept tab, and
// focusTab that tab's id, which the browser panel can show.
export function ApplyRecord({ id, working, focusTab, canSubmit, onChange }: { id: string; working: boolean; focusTab: string; canSubmit: boolean; onChange: () => void }) {
  const [res, retry] = useResource(() => get(`/api/apply/runs/${encodeURIComponent(id)}`, applyRunView), [id], ["runs"]);
  const [asking, setAsking] = useState<"fill" | "submit" | "kept" | null>(null);
  // letter is set while a retry asked from "Allow a tailored cover letter and
  // retry" waits for its confirmation.
  const [letter, setLetter] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [notice, setNotice] = useState("");
  // The demo never presses Submit, so it offers no retry that would.
  const demo = useDemo();
  useInterval(retry, POLL_MS, working);

  if (res.status === "loading") return <Loading label="Loading the application record" />;
  if (!res.data) return res.status === "error" ? <ErrorNote error={res.error} onRetry={retry} /> : null;
  const v = res.data;
  const kept = v.keptTab || focusTab !== "";
  const target = { company: v.company, role: v.role, host: v.host };
  const finished = v.finishedAt !== undefined;
  const canRetry = finished && v.outcome.kind !== "submitted";
  const total = v.stages.reduce((n, s) => n + s.ms, 0);

  async function act(work: () => Promise<void>) {
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      await work();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
      setAsking(null);
      setLetter(false);
    }
  }

  const doRetry = (submit: boolean) =>
    act(async () => {
      // The override is recorded for the row's report before the retry starts.
      if (letter) await post(`/api/candidacies/${v.candidacyId}/documents-override`, candidacy);
      const acc = await post(`/api/apply/runs/${encodeURIComponent(id)}/retry`, runAccepted, { submit, confirmed: true });
      onChange();
      navigate(`/runs/${acc.runId}${window.location.search}`);
    });

  const cancelRetry = () => {
    setAsking(null);
    setLetter(false);
  };
  const letterNote = letter && <p>Your override of the 4.5 floor is recorded for this row&rsquo;s report first, so the run can tailor a cover letter for the form.</p>;

  const doShow = () =>
    act(async () => {
      await showTab(id);
      setNotice("The kept tab is in front.");
    });

  const doSubmitKept = () =>
    act(async () => {
      await post(`/api/apply/runs/${encodeURIComponent(id)}/submit`, runAccepted, { confirmed: true });
      setNotice("The run checks the filled form in its kept tab again, then presses Submit.");
      onChange();
      retry();
    });

  return (
    <>
      {failure && <ErrorNote error={failure} />}
      <p role="status" className="muted">
        {notice}
      </p>
      <h3>Application</h3>
      <dl className="facts">
        <dt>Result</dt>
        <dd>{outcomeLine(v.outcome, false)}</dd>
        {v.outcome.owner && ownerWords(v.outcome.owner) && (
          <>
            <dt>Next step</dt>
            <dd>{ownerWords(v.outcome.owner)}</dd>
          </>
        )}
        <dt>Mode</dt>
        <dd>{v.mode === "submit" ? "Fills and submits" : "Dry run: fills, presses nothing"}</dd>
        <dt>Posting host</dt>
        <dd>{v.host}{v.ats ? ` (${v.ats})` : ""}</dd>
        <dt>Pages</dt>
        <dd>{v.pages}</dd>
        <dt>Started from</dt>
        <dd>{CALLERS[v.caller] ?? v.caller}</dd>
      </dl>
      {/* What the user does with the run comes before its record: the kept
          tab, a retry, and the row and report it belongs to. */}
      <div className="actions">
        {canSubmit && !demo && (
          <button type="button" className="primary" disabled={busy} onClick={() => setAsking("kept")}>
            Submit this form&hellip;
          </button>
        )}
        <button type="button" disabled={busy || !kept} title={kept ? undefined : "No tab is kept for this run."} onClick={() => void doShow()}>
          Show tab
        </button>
        {focusTab && <ShowInPanel tabId={focusTab} />}
        {canRetry && (
          <>
            <button type="button" disabled={busy} onClick={() => setAsking("fill")}>
              Retry (fill only)
            </button>
            {!demo && (
              <button type="button" disabled={busy} onClick={() => setAsking("submit")}>
                Retry and submit
              </button>
            )}
          </>
        )}
        {v.candidacyId !== undefined && <Link to={`/tracker?row=${v.candidacyId}`}>Tracker row</Link>}
        {v.reportNumber !== undefined && <Link to={`/reports?id=${v.reportNumber}`}>Report {v.reportNumber}</Link>}
      </div>
      {!finished && <p className="muted">A run that is still working cannot be retried.</p>}
      {canRetry && demo && <p className="muted">A demo workspace never presses Submit, so a retry here fills only.</p>}
      {canRetry && v.questions && v.questions.length > 0 && (
        <Questions
          v={{ runId: id, host: v.host, company: v.company, role: v.role, questions: v.questions }}
          kept={kept}
          submits={v.mode === "submit" && !demo}
          busy={busy}
          onSaved={(continued) => {
            retry();
            if (continued) {
              setNotice("Every question has an answer: the run goes on in its kept tab.");
              onChange();
            }
          }}
          onRetry={() => setAsking(v.mode === "submit" && !demo ? "submit" : "fill")}
        />
      )}
      {canRetry && ((v.uploads?.length ?? 0) > 0 || v.letterOverride) && (
        <Uploads
          uploads={v.uploads ?? []}
          letter={v.letterOverride === true && v.candidacyId !== undefined}
          busy={busy}
          onLetter={() => {
            setLetter(true);
            setAsking(v.mode === "submit" && !demo ? "submit" : "fill");
          }}
        />
      )}
      {v.correction && (
        // The page sends nothing: the user sends the message, and the
        // correction door decides on that text.
        <div className="note" role="group" aria-labelledby="correct-application">
          <p className="label" id="correct-application">
            Correct this application
          </p>
          <p className="muted">
            Send this message in Ask Agent to fill the form again and send it once more. It replaces the earlier submission, the tracker row stays as it
            is, and a posting is corrected once.
          </p>
          <pre className="pre">{v.correction.message}</pre>
          <p className="actions">
            <CopyButton text={v.correction.message} label="Copy the correction message" />
            <Link to="/ask">Open Ask Agent</Link>
          </p>
        </div>
      )}

      <KeepEdits edits={v.edits ?? []} reload={retry} />

      {(working || kept) && <RunPage id={id} working={working} />}

      <h3>Stages</h3>
      {v.stages.length === 0 ? (
        <p className="muted">No stage has been recorded yet.</p>
      ) : (
        <table className="cards">
          <caption className="sr-only">Stage timeline</caption>
          <thead>
            <tr>
              <th scope="col">Stage</th>
              <th scope="col">Status</th>
              <th scope="col">Detail</th>
              <th scope="col">Duration</th>
            </tr>
          </thead>
          <tbody>
            {v.stages.map((s, i) => (
              <tr key={`${s.stage}-${i}`}>
                <td data-label="Stage">{stageName(s.stage)}</td>
                <td data-label="Status">
                  <Chip tone={STAGE_TONE[s.status] ?? "neutral"}>{s.status}</Chip>
                </td>
                <td data-label="Detail">{s.detail}</td>
                <td data-label="Duration">{durationMs(s.ms)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr>
              <th scope="row" colSpan={3}>
                Total
              </th>
              <td>{durationMs(total)}</td>
            </tr>
          </tfoot>
        </table>
      )}

      {v.batchId && <BatchPanel batchId={v.batchId} />}
      {asking === "fill" && <RetryDialog target={target} note={letterNote} busy={busy} onConfirm={() => void doRetry(false)} onCancel={cancelRetry} />}
      {asking === "submit" && <RetrySubmitDialog target={target} note={letterNote} busy={busy} onConfirm={() => void doRetry(true)} onCancel={cancelRetry} />}
      {asking === "kept" && <SubmitKeptDialog target={target} busy={busy} onConfirm={() => void doSubmitKept()} onCancel={() => setAsking(null)} />}
    </>
  );
}
