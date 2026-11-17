import { useContext } from "react";
import { ArrowRight, CircleAlert, Rocket } from "lucide-react";
import { get, postCheck } from "../../api/client";
import { doctorReport, setupState } from "../../api/decoders_setup";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import type { DoctorReport, DoctorResult, SetupStep } from "../../wire";
import { Settled } from "../../ui/Settle";
import { Progress } from "./Nav";

// problemsOf names each failing check in the user's words: the title of the
// setup step the check belongs to, or the check's own summary when it belongs
// to none. A step named by several checks is listed once.
function problemsOf(failing: readonly DoctorResult[], steps: readonly SetupStep[]): { key: string; label: string; step?: string }[] {
  const out = new Map<string, { key: string; label: string; step?: string }>();
  for (const r of failing) {
    const step = steps.find((s) => s.id === r.id.split(".")[0]);
    const label = step ? step.title : r.summary;
    if (!out.has(label)) out.set(label, { key: r.id, label, step: step?.id });
  }
  return [...out.values()];
}

// SetupCard is the Home card for setup. It shows while a required step is not
// satisfied, and it returns when the doctor finds a required failure even
// though every step is satisfied. When neither holds nothing is rendered. The
// doctor (most of a second) runs once the page has settled, so it neither
// holds the page nor slows the reads that do; a card that shows for an
// unfinished step keeps a line for its answer, so the answer does not push the
// page down.
export function SetupCard() {
  const [res] = useResource(() => get("/api/setup/state", setupState), [], ["setup", "configuration", "gmail", "answerbank"], { keep: "home.setup" });
  // A check that failed (the server was away) runs again once it is back.
  const settled = useContext(Settled);
  const [doctor] = useResource(() => (settled ? postCheck("/api/setup/doctor", doctorReport) : new Promise<DoctorReport>(() => {})), [settled]);
  const report: DoctorReport | "failed" | null = doctor.status === "error" ? "failed" : (doctor.data ?? null);

  const state = res.data;
  if (!state) return null;
  const failing = (typeof report === "object" && report ? report.results : []).filter((r) => r.status === "fail" && r.required);
  const unfinished = state.next !== "" || state.progress < 100;
  if (!unfinished && failing.length === 0) return null;
  const next = state.steps.find((s) => s.id === state.next);
  // A step not done yet is listed as left to do; the check's failures inside
  // it are expected, so only the others are named as problems.
  const left = state.steps.filter((s) => s.required && s.status !== "complete");
  const problems = problemsOf(failing, state.steps).filter((p) => !left.some((s) => s.id === p.step));
  // Before anything of the user's own is entered, the demo is the way to look first.
  const fresh = state.steps.some((s) => s.id === "identity" && s.status !== "complete");
  const stepLink = (id: string, title: string) => <Link to={`/setup?step=${encodeURIComponent(id)}`}>{title}</Link>;

  return (
    <section aria-labelledby="home-setup" className="panel hero" data-card="setup">
      <span className="hero-icon" aria-hidden="true">
        <Rocket />
      </span>
      <div>
        <header className="panel-head">
          <h2 id="home-setup">Finish setting up</h2>
        </header>
        {next && <p className="muted">Next: {next.title}</p>}
        <Progress percent={state.progress} steps={state.steps} />
        {left.length > 0 && (
          <div className="problems left">
            <p className="muted">Left to do:</p>
            <ul className="chips plain">
              {left.map((s) => (
                <li key={s.id}>{stepLink(s.id, s.title)}</li>
              ))}
            </ul>
          </div>
        )}
        <div role="status" className="problems">
          {problems.length > 0 ? (
            <>
              <p>
                <CircleAlert />
                The check found a required problem with:
              </p>
              <ul className="chips plain">
                {problems.map((p) => (
                  <li key={p.key}>{p.step ? stepLink(p.step, p.label) : <span className="chip bad">{p.label}</span>}</li>
                ))}
              </ul>
            </>
          ) : (
            <p className="muted">
              {report === null ? "Checking the setup." : report === "failed" ? "The setup check could not run." : failing.length > 0 ? "The setup check found nothing beyond the steps left to do." : "The setup check found no required problem."}
            </p>
          )}
        </div>
        {fresh && (
          <p className="muted">
            Want to look around first? <Link to="/setup#try-demo">Try the demo</Link>: a fictional job seeker with sample postings, in a folder
            of its own.
          </p>
        )}
        <div className="actions">
          <Link to={next ? `/setup?step=${encodeURIComponent(next.id)}` : "/setup"} className="button primary">
            {next ? "Continue setup" : "Open setup"}
            <ArrowRight />
          </Link>
        </div>
      </div>
    </section>
  );
}
