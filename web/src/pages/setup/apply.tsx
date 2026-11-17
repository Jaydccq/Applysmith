import { useState } from "react";
import { ApiError, post } from "../../api/client";
import { setupApplied, setupStep } from "../../api/decoders_setup";
import { asApiError } from "../../hooks";
import { ErrorNote } from "../../ui/parts";
import type { SetupApplied, SetupStep } from "../../wire";
import { rejection, type ScreenProps } from "./model";

export interface Apply {
  busy: boolean;
  failure: ApiError | null;
  applied: SetupApplied | null;
  byField: Record<string, string[]>;
  send: (input: Record<string, unknown>, dryRun?: boolean) => Promise<SetupApplied | undefined>;
  clear: () => void;
}

// useApply sends one writer call at a time. A write is never repeated; a
// rejection keeps its per-field messages so a form can show them beside the
// fields, and a success reloads the state the page reads.
export function useApply(stepId: string, reload: () => void): Apply {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [applied, setApplied] = useState<SetupApplied | null>(null);

  const send = async (input: Record<string, unknown>, dryRun?: boolean) => {
    setBusy(true);
    setFailure(null);
    try {
      const res = await post(`/api/setup/steps/${encodeURIComponent(stepId)}/apply`, setupApplied, dryRun ? { input, dryRun } : { input });
      setApplied(res);
      reload();
      return res;
    } catch (e) {
      setApplied(null);
      setFailure(asApiError(e));
      return undefined;
    } finally {
      setBusy(false);
    }
  };
  return {
    busy,
    failure,
    applied,
    byField: failure ? rejection(failure).byField : {},
    send,
    clear: () => {
      setFailure(null);
      setApplied(null);
    },
  };
}

// decide records an acknowledgement, a skip or an unskip; none takes a body.
export function decide(id: string, kind: "ack" | "skip" | "unskip"): Promise<SetupStep> {
  return post(`/api/setup/steps/${encodeURIComponent(id)}/${kind}`, setupStep);
}

// ApplyOutcome says what a writer call did: the failure, or whether it saved
// and the notes it returned (a check that writes nothing shows only its
// notes). A rejection with fields is shown beside the fields, so only a
// short line is repeated here.
export function ApplyOutcome({ apply }: { apply: Apply }) {
  const { failure, applied } = apply;
  return (
    <div aria-live="polite">
      {failure && (Object.keys(apply.byField).length > 0 ? <p role="alert" className="note error">{rejection(failure).general}</p> : <ErrorNote error={failure} />)}
      {applied && (
        <div className="note ok">
          {(applied.wrote || applied.notes.length === 0) && <p>{applied.wrote ? "Saved." : "Nothing changed."}</p>}
          {applied.notes.length > 0 && (
            <ul className="plain stack">
              {applied.notes.map((n) => (
                <li key={n}>{n.charAt(0).toUpperCase() + n.slice(1)}</li>
              ))}
            </ul>
          )}
          {applied.untouched.length > 0 && <p className="muted">Kept as stored: {applied.untouched.join(", ")}.</p>}
        </div>
      )}
    </div>
  );
}

// ApplyButton runs a writer with a fixed input, for the steps that only check
// or create something.
export function ApplyButton({ step, reload, label, input = {} }: { step: SetupStep; reload: () => void; label: string; input?: Record<string, unknown> }) {
  const apply = useApply(step.id, reload);
  return (
    <div>
      <div className="actions">
        <button type="button" disabled={apply.busy} onClick={() => void apply.send(input)}>
          {apply.busy ? "Working" : label}
        </button>
      </div>
      <ApplyOutcome apply={apply} />
    </div>
  );
}

// Workspace takes no path: this server already runs in a workspace, so the
// step only creates the files that are missing and never overwrites one.
export function Workspace({ step, reload }: ScreenProps) {
  return (
    <div>
      <p>The workspace is the folder this app runs in. Creating the missing files leaves every existing file as it is.</p>
      <ApplyButton step={step} reload={reload} label="Create missing files" />
    </div>
  );
}

// Prerequisites has no form: the machine is probed again on request.
export function Prerequisites({ step, reload }: ScreenProps) {
  return (
    <div>
      <p>This step only looks at your computer. Fix what the list above names, then check again.</p>
      <ApplyButton step={step} reload={reload} label="Check again" />
    </div>
  );
}
