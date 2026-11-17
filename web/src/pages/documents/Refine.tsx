import { useState } from "react";
import { get, newRequestId, request } from "../../api/client";
import { documentRefinement } from "../../api/decoders_documents";
import { runAccepted } from "../../api/decoders_runs";
import { useResource } from "../../hooks";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import { useAction } from "../action";
import { useUnsaved } from "../../ui/SaveBar";
import type { DocumentProposal, DocumentRefineTurn } from "../../wire";
import type { DocumentRunState } from "./run";

const MAX_INSTRUCTION = 2000;
const MAX_TURNS = 8;
// Common asks for each kind of document; a click only fills the box.
const POSTING_WORDS = "Use the posting's wording where my evidence supports it";
const SUGGESTIONS: Record<string, string[]> = {
  resume: ["Shorten the bullets so it fits on one page", "Lead with the work closest to this posting", POSTING_WORDS],
  "cover-letter": ["Make the opening more direct", "Make it shorter and more confident", POSTING_WORDS],
};

// Changed lines carry a sign as well as the browser's insertion and deletion
// marks, so the change does not rest on colour or underline alone.
function Diff({ proposal }: { proposal: DocumentProposal }) {
  const changed = proposal.diff.some((l) => l.op !== "same");
  return (
    <pre className="pre" aria-label="Changes against the current version">
      {proposal.diff.map((l, i) =>
        l.op === "add" ? (
          <ins key={i}>{`+ ${l.text}\n`}</ins>
        ) : l.op === "del" ? (
          <del key={i}>{`- ${l.text}\n`}</del>
        ) : (
          <span key={i}>{`  ${l.text}\n`}</span>
        ),
      )}
      {!changed && "The text reads the same; only the evidence references changed."}
    </pre>
  );
}

function Turns({ turns }: { turns: DocumentRefineTurn[] }) {
  if (turns.length === 0) return null;
  return (
    <ol aria-label="Earlier requests">
      {turns.map((t, i) => (
        <li key={i}>
          {t.kind === "instruction" && <>You asked: {t.text}</>}
          {t.kind === "decision" && <>{t.decision === "accepted" ? "You accepted the revision." : "You discarded the revision."}</>}
          {t.kind === "error" && <>The revision failed: {t.text}</>}
        </li>
      ))}
    </ol>
  );
}

// The revision asked for in words. The model's answer waits as a proposal with
// its diff; nothing changes until it is accepted, which makes a new version.
export function Refine({ id, kind, run }: { id: number; kind: string; run: DocumentRunState }) {
  const [res, reload] = useResource(() => get(`/api/documents/refine/${id}`, documentRefinement), [id, run.busy]);
  const [instruction, setInstruction] = useState("");
  const held = useUnsaved(instruction.trim() !== "");
  const ask = useAction();
  const decide = useAction();
  if (res.status === "error" && !res.data) return <ErrorNote error={res.error} onRetry={reload} />;
  if (!res.data) return <Loading label="Loading revisions" />;
  const { pending, turns } = res.data;

  const send = async () => {
    // Error turns stay on screen but are not sent: a request carries instructions and decisions.
    const prior = turns.filter((t) => t.kind !== "error").slice(-MAX_TURNS);
    const accepted = await ask.run(() =>
      request("POST", `/api/documents/${id}/refine`, runAccepted, { body: { instruction: instruction.trim(), turns: prior }, headers: { "Idempotency-Key": newRequestId() } }),
    );
    if (accepted) {
      setInstruction("");
      run.begin(accepted.runId, "The revision");
    }
  };
  const accept = async () => {
    const accepted = await decide.run(() =>
      request("POST", `/api/documents/${id}/refine/accept`, runAccepted, { body: { proposalId: pending!.id }, headers: { "Idempotency-Key": newRequestId() } }),
    );
    if (accepted) run.begin(accepted.runId, "The accepted revision");
  };
  const discard = async () => {
    const done = await decide.run(() =>
      request("POST", `/api/documents/${id}/refine/discard`, documentRefinement, { body: { proposalId: pending!.id }, headers: { "Idempotency-Key": newRequestId() } }),
    );
    if (done) reload();
  };

  const tooLong = instruction.length > MAX_INSTRUCTION;
  return (
    <section aria-label="Revise with an instruction">
      <Turns turns={turns} />
      {pending ? (
        <>
          <p>
            <Chip tone="warn">Waiting for your decision</Chip> You asked: {pending.instruction}
          </p>
          <Diff proposal={pending} />
          <p className="muted">Accepting saves this as a new draft version with no PDF; the current version stays as it is. Discarding changes nothing.</p>
          <div className="actions">
            <button type="button" disabled={decide.busy || run.busy} onClick={accept}>
              Accept revision
            </button>
            <button type="button" disabled={decide.busy || run.busy} onClick={discard}>
              Discard revision
            </button>
          </div>
        </>
      ) : (
        <form
          className="form-grid"
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          <label>
            What should change?
            <textarea className="editor" rows={3} value={instruction} maxLength={MAX_INSTRUCTION + 1} onChange={(e) => setInstruction(e.target.value)} />
          </label>
          <div className="actions" role="group" aria-label="Suggested instructions">
            {(SUGGESTIONS[kind] ?? [POSTING_WORDS]).map((t) => (
              <button key={t} type="button" className="small" onClick={() => setInstruction(t)}>
                {t}
              </button>
            ))}
          </div>
          <p className="muted">
            The model rewrites only from your evidence; a number or name your sources do not hold is refused. {instruction.length} of {MAX_INSTRUCTION} characters.
          </p>
          {tooLong && <p role="alert" className="note error">The instruction is longer than {MAX_INSTRUCTION} characters.</p>}
          <div className="actions">
            <button type="submit" disabled={instruction.trim() === "" || tooLong || ask.busy || run.busy}>
              Ask for a revision
            </button>
          </div>
        </form>
      )}
      <div aria-live="polite">
        {ask.error && <ErrorNote error={ask.error} />}
        {decide.error && <ErrorNote error={decide.error} />}
      </div>
      {held}
    </section>
  );
}
