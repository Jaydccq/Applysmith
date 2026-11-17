import { ListChecks } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { get, postCheck } from "../api/client";
import { doctorReport, setupState, setupStatus } from "../api/decoders_setup";
import { useResource } from "../hooks";
import { useSearchParams } from "../router";
import { PageHeader } from "../ui/PageHeader";
import { ErrorNote, Loading } from "../ui/parts";
import type { DoctorReport, SetupState } from "../wire";
import { useAction } from "./action";
import { DemoTutorial, TryDemo } from "./setup/Demo";
import { DoctorResults } from "./setup/Doctor";
import { SETUP_TOPICS } from "./setup/model";
import { Progress, StepNav } from "./setup/Nav";
import { StepPage } from "./setup/StepPage";
import { Button } from "../ui/Button";

// The step the wizard opens on: the first incomplete required step, or
// orientation when every required step is satisfied.
const OPENING = "orientation";

function Wizard({ state, reload }: { state: SetupState; reload: () => void }) {
  const [params, setParams] = useSearchParams();
  const [report, setReport] = useState<DoctorReport | null>(null);
  const doctor = useAction();
  const wanted = params.get("step") ?? "";
  const current = state.steps.find((s) => s.id === wanted) ?? state.steps.find((s) => s.id === (state.next || OPENING)) ?? state.steps[0];

  // The chosen step is written into the address once, so a reload resumes
  // there even after the first incomplete step has moved on.
  useEffect(() => {
    if (current && wanted !== current.id) setParams({ step: current.id }, { replace: true });
  }, [current, wanted, setParams]);

  // Another step opens at its top, wherever it was chosen from.
  const article = useRef<HTMLDivElement>(null);
  const shown = useRef(current?.id);
  useEffect(() => {
    if (shown.current === current?.id) return;
    shown.current = current?.id;
    const el = article.current;
    if (el && el.getBoundingClientRect().top < 0) el.scrollIntoView?.({ block: "start" });
    // The link that was pressed went with the old step (a step rail link stays
    // and keeps focus): the new step's title takes it, so the keyboard does not
    // start again from the top of the page.
    const at = document.activeElement;
    if (!at || at === document.body || at.id === "main") document.getElementById(`step-${current?.id}`)?.focus({ preventScroll: true });
  }, [current?.id]);

  const check = async () => {
    const r = await doctor.run(() => postCheck("/api/setup/doctor", doctorReport));
    if (r) setReport(r);
  };
  // The results land below the step, often out of sight: they take the focus
  // and come into view, so the button visibly did something.
  useEffect(() => {
    const h = report && document.getElementById("setup-doctor");
    if (!h) return;
    h.focus({ preventScroll: true });
    h.scrollIntoView?.({ block: "start" });
  }, [report]);

  if (!current) return <p className="empty">The setup catalog is empty.</p>;
  return (
    <>
      <section aria-label="Progress" className="panel setup-progress">
        <Progress percent={state.progress} steps={state.steps} />
        <div className="actions">
          <Button icon={ListChecks} disabled={doctor.busy} onClick={() => void check()}>
            {doctor.busy ? "Checking" : "Check everything"}
          </Button>
        </div>
        {doctor.error && <ErrorNote error={doctor.error} />}
      </section>
      <div className="with-rail">
        <StepNav steps={state.steps} current={current.id} />
        <div ref={article} className="step-column">
          <StepPage key={current.id} step={current} state={state} reload={reload} />
          {report && <DoctorResults report={report} titles={Object.fromEntries(state.steps.map((s) => [s.id, s.title]))} />}
        </div>
      </div>
    </>
  );
}

export default function Setup() {
  const [res, reload] = useResource(() => get("/api/setup/state", setupState), [], SETUP_TOPICS);
  const [status] = useResource(() => get("/api/setup/status", setupStatus), [], ["setup"]);
  return (
    <>
      <PageHeader path="/setup" />
      {status.data && (status.data.demo ? <DemoTutorial /> : <TryDemo folded={res.data?.steps.some((s) => s.id === "identity" && s.status === "complete")} />)}
      {res.status === "loading" && <Loading lines={6} />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="the setup steps" />}
      {res.data && <Wizard state={res.data} reload={reload} />}
    </>
  );
}
