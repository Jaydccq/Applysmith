import { useEffect, useRef, useState } from "react";
import { get } from "../../api/client";
import { documentRun } from "../../api/decoders_interview";
import { useInterval } from "../../hooks";

const REASON: Record<string, string> = {
  "no-valid-draft": "the text did not pass the grounding checks",
  "posting-required": "the job description is needed",
  "contact-missing": "your profile has no name or email for the contact block",
  "write-failed": "the files could not be saved",
  "below-floor": "the posting score is below the floor for a PDF",
  "document-toolchain-unavailable": "the document tools are missing",
  "stale-source": "the stored text changed after it was written",
  "input-invalid": "the request was refused",
  cancelled: "it was cancelled",
  timeout: "it took too long",
  interrupted: "the app stopped while it ran",
};

// What a working run is doing, by its stage (internal/documents types.go).
const STAGE: Record<string, string> = {
  retrieve: "reading your knowledge base",
  profile: "reading your profile",
  manifest: "choosing the evidence to use",
  draft: "writing the text",
  review: "reviewing the draft",
  revise: "applying the review",
  compile: "making the PDF",
  verify: "checking the PDF",
  publish: "saving the new version",
};

// stageWords says what a working document or prep-pack run is doing.
export const stageWords = (stage: string | undefined) => STAGE[stage ?? ""] ?? stage ?? "";

// runReason says in words why a document or prep-pack run ended without a result.
export const runReason = (reason: string | undefined, status: string) => REASON[reason ?? ""] ?? (reason || status);

export interface DocumentRunState {
  busy: boolean;
  runId: string;
  stage: string;
  // left is the most time the run has before its deadline stops it, in seconds.
  left: number;
  what: string;
  failure: string;
  // reason is the code of the last run that ended without a result.
  reason: string;
  seconds: number;
  begin: (runId: string, what: string) => void;
}

// useDocumentRun follows one document run at a time: a regeneration, an edit
// or a PDF. A failed run publishes nothing, so the failure line says the
// earlier versions are as they were. activeRunId is a run of the application
// that was already working when the drawer opened; it is followed like one
// started here.
export function useDocumentRun(onEnd: () => void, activeRunId?: string): DocumentRunState {
  const [run, setRun] = useState<{ id: string; what: string; at: number } | null>(null);
  const [seconds, setSeconds] = useState(0);
  const [stage, setStage] = useState("");
  const [left, setLeft] = useState(0);
  const [failure, setFailure] = useState("");
  const [reason, setReason] = useState("");
  const followed = useRef(new Set<string>());
  const begin = (id: string, what: string) => {
    followed.current.add(id);
    setFailure("");
    setReason("");
    setSeconds(0);
    setRun({ id, what, at: Date.now() });
  };
  useEffect(() => {
    if (activeRunId && !run && !followed.current.has(activeRunId)) begin(activeRunId, "A document run you started earlier");
  }, [activeRunId, run]);

  useInterval(
    async () => {
      if (!run) return;
      try {
        const r = await get(`/api/documents/runs/${run.id}`, documentRun);
        if (r.status === "queued" || r.status === "running") {
          setStage(stageWords(r.stage));
          setLeft(r.remainingSeconds ?? 0);
          setSeconds(Math.round((Date.now() - run.at) / 1000));
          return;
        }
        setRun(null);
        setStage("");
        setFailure(r.status === "succeeded" ? "" : `${run.what} was not made (${runReason(r.reason, r.status)}). Your earlier versions are unchanged.`);
        setReason(r.status === "succeeded" ? "" : (r.reason ?? ""));
      } catch {
        setRun(null);
        setFailure(`${run.what} could not be followed. Your earlier versions are unchanged; reload to see whether it finished.`);
      }
      onEnd();
    },
    600,
    run !== null,
  );

  return { busy: run !== null, runId: run?.id ?? "", stage, left, what: run?.what ?? "", failure, reason, seconds, begin };
}
