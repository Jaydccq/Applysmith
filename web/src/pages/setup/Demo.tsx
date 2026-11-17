import { useState } from "react";
import { post } from "../../api/client";
import { demoCreated } from "../../api/decoders_setup";
import { runAccepted } from "../../api/decoders_runs";
import { Link } from "../../router";
import { ErrorNote } from "../../ui/parts";
import { hasShell } from "../../desktop";
import { CopyButton } from "../../ui/CopyButton";
import { BackToWorkspace, DEMO_NEVER, SwitchButton } from "../../ui/DemoBanner";
import type { DemoCreated } from "../../wire";
import { useAction } from "../action";

const SAMPLE_FORM = "/demo/sample-application";

// TryDemo creates the demo workspace in a folder of its own. Inside the app the
// result opens it in this window (the app restarts its server on the demo and
// the demo banner brings the user back); in a plain browser it gives the
// command to start the demo, since there is no process to restart.
export function TryDemo({ folded = false }: { folded?: boolean }) {
  const act = useAction();
  const [made, setMade] = useState<DemoCreated | null>(null);
  const create = async () => {
    const r = await act.run(() => post("/api/setup/demo", demoCreated));
    if (r) setMade(r);
  };
  const body = (
    <>
      <p>
        See the product working before you enter anything of your own. The demo is a separate folder, <code>~/Applysmith-Demo</code>, with the
        fictional persona Alex Example, three sample postings from fictional companies and a sample application form. It never touches your own
        workspace or data. {DEMO_NEVER}
      </p>
      {!made && (
        <div className="actions">
          <button type="button" disabled={act.busy} onClick={() => void create()}>
            {act.busy ? "Creating" : "Create the demo workspace"}
          </button>
        </div>
      )}
      {act.error && <ErrorNote error={act.error} />}
      {made && (
        <div role="status">
          <p>
            The demo workspace is ready at <code>{made.workspace}</code> with {made.postings} new sample posting(s) in its inbox.
          </p>
          {made.notes.map((n) => (
            <p key={n}>{n}</p>
          ))}
          {hasShell() ? (
            <>
              <p>
                Your own workspace is unchanged. Opening the demo restarts the server on it; <strong>Back to my workspace</strong> in the demo banner returns here.
              </p>
              <p>
                <SwitchButton target="demo">Open the demo workspace</SwitchButton>
              </p>
            </>
          ) : (
            <>
              <p>Your own workspace is unchanged and this dashboard keeps serving it. To open the demo, stop this server, then start the demo's:</p>
              <pre>
                <code>{made.start}</code>
              </pre>
              <p>
                <CopyButton text={made.start} label="Copy the command that starts the demo" />
              </p>
            </>
          )}
          <p>Then follow the tutorial on the demo's own Setup page.</p>
        </div>
      )}
    </>
  );
  // Once the person's own setup is under way the offer folds away.
  if (folded && !made) {
    return (
      <details className="panel">
        <summary>
          <h2 id="try-demo">Try the demo</h2>
        </summary>
        {body}
      </details>
    );
  }
  return (
    <section aria-labelledby="try-demo" className="panel">
      <h2 id="try-demo">Try the demo</h2>
      {body}
    </section>
  );
}

// TrySampleForm starts a dry run on the sample form this server serves, named
// after the sample posting the form belongs to (sample-application.html). The
// request never submits, so the run fills the form and stops before Submit.
function TrySampleForm() {
  const act = useAction();
  const [runId, setRunId] = useState("");
  const start = async () => {
    const r = await act.run(() => post("/api/apply/runs", runAccepted, { url: window.location.origin + SAMPLE_FORM, company: "Harborlight Analytics", role: "Senior Platform Engineer", submit: false, confirmed: false }));
    if (r) setRunId(r.runId);
  };
  return (
    <>
      <div className="actions">
        <button type="button" disabled={act.busy} onClick={() => void start()}>
          Try autofill on the sample form
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {runId && (
        <p role="status" className="note ok">
          The sample form is being filled and is never submitted. <Link to={`/runs/${runId}`}>Follow the run</Link>
        </p>
      )}
    </>
  );
}

// DemoTutorial is the short form of the guide's tutorial, shown in the demo
// workspace, where the banner links to it.
export function DemoTutorial() {
  return (
    <section id="demo-tutorial" aria-labelledby="demo-tutorial-title" className="panel">
      <h2 id="demo-tutorial-title">Demo tutorial</h2>
      <ol>
        <li>
          Choose a model provider: <Link to="/setup?step=llm">Language-model provider</Link>. It is stored in this demo workspace only.
        </li>
        <li>
          Look at what the persona brings: the CV and résumés on <Link to="/profile">Profile</Link> and the answers that fill forms on{" "}
          <Link to="/answer-bank">Answer Bank</Link>.
        </li>
        <li>
          Evaluate a sample posting: open <Link to="/discovery?tab=inbox">Discovery, Inbox</Link>, press <strong>Evaluate</strong> on a row and
          read the report on the <Link to="/reports">Reports</Link> page when it finishes. A scan from Discovery adds real postings beside them.
        </li>
        <li>
          From the report, open <strong>Résumé and cover letter</strong> and make the pair. A sample posting scores below 4.5, so its PDF asks
          you to <strong>Allow a PDF anyway</strong>.
        </li>
        <li>
          Fill the <a href={SAMPLE_FORM} target="_blank" rel="noreferrer">sample application form</a>: press <strong>Try autofill on the sample form</strong>{" "}
          below. It is filled and never submitted. The sample postings' own sites do not exist, so{" "}
          <Link to="/apply-next">Apply Next</Link> offers them no autofill.
        </li>
        <li>
          Read what the run did on the <Link to="/runs">Runs</Link> board, then mark the posting applied on the <Link to="/tracker">Tracker</Link>,
          where its follow-ups come due.
        </li>
        <li>
          Switch to your own workspace: <BackToWorkspace inline />
        </li>
      </ol>
      <TrySampleForm />
      <p className="muted">
        The <Link to="/guide?doc=tutorial">full walkthrough with screenshots</Link> is in the user guide.
      </p>
    </section>
  );
}
