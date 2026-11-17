import { useRef, useState } from "react";
import type { ApiError } from "../api/client";
import { asApiError } from "../hooks";
import { formatDateTime } from "../ui/parts";

// useAction runs one write at a time and keeps its failure for the caller to
// show. A write is never repeated: the caller decides whether to press again.
// A second run while one is in flight does nothing and answers undefined, so a
// key that fires twice before the button is drawn disabled cannot send twice.
export function useAction(): { busy: boolean; error: ApiError | null; run: <T>(go: () => Promise<T>) => Promise<T | undefined>; clear: () => void } {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const flying = useRef(false);
  const run = async <T,>(go: () => Promise<T>): Promise<T | undefined> => {
    if (flying.current) return undefined;
    flying.current = true;
    setBusy(true);
    setError(null);
    try {
      return await go();
    } catch (e) {
      setError(asApiError(e));
      return undefined;
    } finally {
      flying.current = false;
      setBusy(false);
    }
  };
  return { busy, error, run, clear: () => setError(null) };
}

// attemptReason says in words why a model call ran on the model it did (the
// ledger's reason); empty for the configured first choice.
export function attemptReason(reason: string | undefined): string {
  switch (reason) {
    case "fallback":
      return "fallback after an error";
    case "retry":
      return "the same model again after its rate-limit delay";
    case "first-token":
      return "fallback: the first model was too slow to start";
    case "rested":
      return "an earlier model is resting";
    case "cap-downgrade":
      return "cheaper model: daily cap reached";
    case "affinity":
      return "kept on the model that answered this conversation";
    case "rotate":
      return "group: its turn in the rotation";
    case "usage":
      return "group: the least used model today";
    default:
      return reason ?? "";
  }
}

// attemptOutcome says a model call's outcome (the ledger's "ok" or failure
// kind, internal/llm) in words; a kind not named here reads with spaces.
export function attemptOutcome(outcome: string | undefined): string {
  switch (outcome) {
    case "fake-fixture-missing":
      return "not simulated offline";
    case "no-route":
      return "no model";
    case "cost-cap":
      return "daily cap reached";
    case "invalid-structured-output":
      return "malformed answer";
    default:
      return (outcome ?? "").replaceAll("-", " ");
  }
}

// when is a timestamp in the form every page uses.
export function when(iso: string): string {
  return formatDateTime(iso);
}
