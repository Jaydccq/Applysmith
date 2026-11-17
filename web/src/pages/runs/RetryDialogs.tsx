import type { ReactNode } from "react";
import { get } from "../../api/client";
import { applySkipStatus } from "../../api/decoders_apply";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";

interface Target {
  company?: string;
  role?: string;
  host: string;
}

const who = (t: Target) => t.company ?? t.host;

// SkipLine says whether the employer is on the batch skip list, read from the
// server so the answer is the one the batch gate uses. `what` names the
// explicit start the list does not block.
export function SkipLine({ company, what = "retry" }: { company: string | undefined; what?: string }) {
  const [res] = useResource(() => (company ? get("/api/apply/skip-status", applySkipStatus, { company }) : Promise.resolve(undefined)), [company]);
  if (!company) return <p>The employer is not known, so the batch skip list cannot be checked.</p>;
  if (res.status === "loading") return <p role="status">Checking the batch skip list</p>;
  if (res.status === "error" || !res.data) return <p>The batch skip list could not be checked.</p>;
  if (!res.data.listed) return <p>{company} is not on your batch skip list.</p>;
  return (
    <p>
      {company} is on your batch skip list (as &ldquo;{res.data.entry}&rdquo;). This is an explicit {what} of one posting, so the list does not block it.
    </p>
  );
}

// RetryDialog confirms a retry that fills and presses nothing. A note says
// what is recorded before the retry starts.
export function RetryDialog({ target, note, busy, onConfirm, onCancel }: { target: Target; note?: ReactNode; busy: boolean; onConfirm: () => void; onCancel: () => void }) {
  return (
    <ConfirmDialog
      title={`Retry the application to ${who(target)}`}
      consequence={
        <>
          Applysmith will fill the form for {target.role ?? "this role"} at {who(target)} on {target.host} again. Nothing is submitted.
        </>
      }
      details={note}
      verb="Retry"
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}

// RetrySubmitDialog is the submit-class dialog: it names the company, the role
// and the host, says that the final Submit is pressed and shows the skip list.
export function RetrySubmitDialog({ target, note, busy, onConfirm, onCancel }: { target: Target; note?: ReactNode; busy: boolean; onConfirm: () => void; onCancel: () => void }) {
  return (
    <ConfirmDialog
      title={`Submit the application to ${who(target)} again`}
      consequence={
        <>
          Applysmith will fill the form for {target.role ?? "this role"} at {who(target)} on {target.host} and press its final Submit button. This sends a real
          application and cannot be undone.
        </>
      }
      details={
        <>
          {note}
          <SkipLine company={target.company} />
        </>
      }
      verb="Retry and submit"
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}

// SubmitKeptDialog confirms "Submit this form" as Autofill and submit is
// confirmed: it names the company, the role and the host, says that the final
// Submit is pressed and shows the skip list.
export function SubmitKeptDialog({ target, busy, onConfirm, onCancel }: { target: Target; busy: boolean; onConfirm: () => void; onCancel: () => void }) {
  return (
    <ConfirmDialog
      title={`Submit the application to ${who(target)}`}
      consequence={
        <>
          Applysmith will check the filled form for {target.role ?? "this role"} at {who(target)} on {target.host} in the tab it kept, run every check again and
          press its final Submit button. This sends a real application and cannot be undone.
        </>
      }
      details={<SkipLine company={target.company} what="application" />}
      verb="Submit this form"
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
