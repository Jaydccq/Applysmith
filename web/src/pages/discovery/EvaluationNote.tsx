import { get } from "../../api/client";
import { evaluationView } from "../../api/decoders_discovery";
import { useInterval, useResource } from "../../hooks";
import { Link } from "../../router";
import { ModelsLinked } from "../../ui/parts";

const POLL_MS = 3000;

// EvaluationNote follows the run an Evaluate press started: it says started
// while the run works and what came of it once it ends, so the note never
// outlives the row it talks about. Without a run id there is nothing to
// follow, and it links the evaluations under way instead.
export function EvaluationNote({ name, jobId }: { name: string; jobId?: string }) {
  const [res, reload] = useResource(() => (jobId ? get(`/api/evaluations/${jobId}`, evaluationView) : Promise.resolve(undefined)), [jobId], ["runs"]);
  const view = res.data;
  const ended = Boolean(view?.outcome);
  useInterval(reload, POLL_MS, Boolean(jobId) && !ended);

  // A failure that explains itself says what to do, with the place it names a
  // link (no model: Settings, Models); the code is the fallback.
  const why = view?.explanation || view?.reason;
  if (view?.outcome === "failed")
    return (
      <p role="status" className="note error">
        Evaluation of {name} failed{why && <>: <ModelsLinked text={why} /></>}. {jobId && <Link to={`/runs/${jobId}`}>Open the run</Link>}
      </p>
    );
  if (view?.outcome)
    return (
      <p role="status" className="note ok">
        Evaluation of {name} finished.{" "}
        {view.reportNumber ? <Link to={`/reports?id=${view.reportNumber}`}>Report {view.reportNumber}</Link> : <>No report was written ({view.outcome}). {jobId && <Link to={`/runs/${jobId}`}>Open the run</Link>}</>}
      </p>
    );
  return (
    <p role="status" className="note ok">
      Evaluation of {name} started. {jobId ? <Link to={`/runs/${jobId}`}>Follow the run</Link> : <Link to="/discovery?tab=inbox&status=queued">See what is evaluating</Link>}
    </p>
  );
}
