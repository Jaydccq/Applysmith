import { useId, useRef, useState, type ReactNode } from "react";
import { get, post } from "../../api/client";
import { llmRouting } from "../../api/decoders";
import { answerBankSaved, answerCandidateDecision, answerCandidateGenerateResult, answerEdit } from "../../api/decoders_bank";
import { Link, useSearchParams } from "../../router";
import { FilterChips } from "../../ui/FilterChips";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, ModelsLinked, UndoNote, formatDate, humanize } from "../../ui/parts";
import type { AnswerCandidate, AnswerEdit } from "../../wire";
import { sourceWords } from "../runs/ApplyRecord";
import { suggested } from "./model";

// Result is what the last press did, what it was refused, or both: a bulk
// approval the server refuses part way says how far it got.
type Result = { say?: string; fail?: unknown } | null;

// Decided is the latest one-press decision with its Undo: the route that takes
// it back and what the note says before and after; n keys the note by the press.
interface Decided {
  c: AnswerCandidate;
  n: number;
  done: ReactNode;
  undone: string;
  back: "restore" | "unapprove";
}

// sorting says what becomes of the n waiting suggestions no review has sorted:
// after a look the server sorts them in the background (learn.ReviewLater) with
// the suggestion-review model; with no model they stay in the queue unsorted.
async function sorting(n: number): Promise<string> {
  const r = await get("/api/llm/routing", llmRouting).catch(() => undefined);
  if (r?.offline || r?.tasks.find((t) => t.task === "suggestion-review")?.unrouted) return "No model is set to sort suggestions; choose one under Settings, Models.";
  return `${n === 1 ? "One is" : `${n} are`} being sorted in the background; the list changes when that is done.`;
}

const KIND_WORDS: Record<string, string> = { fixed: "fixed answer", "pick-option": "option to pick" };

// draftOf is the answer the review drafted from the user's profile and CV for a
// waiting new entry that proposes none of its own; empty when there is none.
const draftOf = (c: AnswerCandidate) =>
  c.state === "pending" && c.type === "new-entry" && !c.longForm && !c.proposedAnswer && c.review.verdict === "reusable" ? c.review.draft : "";

// Proposal says what approving writes, or for an approved one what it wrote.
function Proposal({ c }: { c: AnswerCandidate }) {
  const made = c.state === "approved";
  if (c.type === "extend-examples") return <p>{made ? "Added" : "Adds"} this phrasing to the entry <strong>{c.targetEntryId}</strong>.{made ? "" : " It proposes no answer."}</p>;
  if (made && (c.longForm || !c.proposedAnswer)) return <p className="muted">It added an empty entry for you to fill.</p>;
  if (c.longForm) return <p className="muted">No answer is proposed: the answers you gave were long prose. Approving creates an empty entry for you to fill.</p>;
  const kind = c.multiple ? "options to tick" : KIND_WORDS[c.proposedKind] ?? humanize(c.proposedKind).toLowerCase();
  if (!made && !c.proposedAnswer && c.review.verdict === "reusable") {
    const unsaid = (c.review.reason || "Your profile and CV don't say.").replace(/\.$/, "");
    return <p className="muted">{draftOf(c) ? "No form gave this an answer, so one is drafted below." : `${unsaid} \u2014 type your answer.`}</p>;
  }
  if (!c.proposedAnswer) return <p className="muted">No answer is proposed for this {kind}. Approving creates an empty entry for you to fill.</p>;
  return (
    <p>
      {made ? "Approved" : "Proposed"} {kind}: <strong>{c.proposedAnswer}</strong>
    </p>
  );
}

// comesBack says when a rejected suggestion is proposed again on its own: a
// phrasing never, a new entry at twice the support it was rejected with.
const comesBack = (c: AnswerCandidate) =>
  c.type === "extend-examples" ? "It is not proposed again." : `It is proposed again once the question is seen ${2 * c.support} times.`;

// waitsAgain is what a take-back of the decision leaves.
const waitsAgain = (c: AnswerCandidate) =>
  `${c.seedQuestion} waits for your decision again${c.state === "rejected" ? "." : `; the ${c.type === "extend-examples" ? "phrasing" : "entry"} it added is removed.`}`;

// A decided row takes its decision back (onBack) once its Undo note is gone:
// an approval after a confirmation, since it removes what it wrote; a refusal
// stays on the row. A drafted one keeps the draft in an open answer box, and
// its approval sends what the box holds; onText tells the page what a drafted
// box holds, since its bulk approval sends it too.
function Row({ c, checked, onCheck, busy, onApprove, onEditApprove, onReject, onBack, onText }: { c: AnswerCandidate; checked: boolean; onCheck: (v: boolean) => void; busy: boolean; onApprove: () => void; onEditApprove: (text: string) => void; onReject: () => void; onBack: () => Promise<void>; onText: (text: string) => void }) {
  const draft = draftOf(c);
  const [editing, setEditing] = useState(draft !== "");
  const [text, setText] = useState(c.proposedAnswer || draft);
  const [asking, setAsking] = useState(false);
  const [fail, setFail] = useState<unknown>();
  const editable = c.type === "new-entry" && !c.longForm;
  const pending = c.state === "pending";
  const approved = c.state === "approved";
  const back = () => (setAsking(false), setFail(undefined), onBack().catch(setFail));
  return (
    <li className="panel candidate">
      <div className="row">
        {pending ? (
          <label className="check">
            <input type="checkbox" checked={checked} onChange={(e) => onCheck(e.target.checked)} aria-label={`Select ${c.seedQuestion}`} />
            <strong>{c.seedQuestion}</strong>
          </label>
        ) : (
          <strong>{c.seedQuestion}</strong>
        )}
        <Chip>{c.support} {c.support === 1 ? "occurrence" : "occurrences"}</Chip>
      </div>
      <p className="muted">
        Seen on {c.hosts.length > 0 ? c.hosts.join(", ") : "an unknown host"}, last {formatDate(c.lastSeenAt)}.
      </p>
      {c.questions.length > 1 && (
        <details>
          <summary>Asked {c.questions.length} ways</summary>
          <ul>
            {c.questions.map((q) => (
              <li key={q}>{q}</li>
            ))}
          </ul>
        </details>
      )}
      {(c.sections.length > 0 || c.options.length > 0) && (
        <p className="muted">
          {c.sections.length > 0 && <>Section: {c.sections.join("; ")}. </>}
          {c.options.length > 0 && <>Options: {c.options.join(", ")}.</>}
        </p>
      )}
      <div className="proposal">
        <Proposal c={c} />
      </div>
      {c.flags.length > 0 && <p className="chips">{c.flags.map((f) => <Chip key={f} tone="warn">{humanize(f)}</Chip>)}</p>}
      {/* The press that opens the box goes, so the box takes the keyboard; Enter approves. */}
      {editing && (
        <form className="inline-form" onSubmit={(e) => (e.preventDefault(), text.trim() && !busy && onEditApprove(text.trim()))}>
          <label>
            {draft ? "Drafted from your profile and CV \u2014 check it before approving" : "Answer to store"}
            <input value={text} onChange={(e) => (setText(e.target.value), draft && onText(e.target.value))} autoFocus={!draft} />
          </label>
          {c.multiple && <p className="muted">Name the options to tick as the list above words them.</p>}
          {draft && c.review.cited.length > 0 && <p className="muted">Written from: {[...new Set(c.review.cited.map(sourceWords))].join(", ")}.</p>}
          {!draft && (
            <span className="actions">
              <button type="submit" disabled={busy || text.trim() === ""}>
                Save edit and approve
              </button>
              <button type="button" className="ghost" onClick={() => (setEditing(false), setText(c.proposedAnswer))}>
                Cancel
              </button>
            </span>
          )}
        </form>
      )}
      {!pending && (
        <p className="muted">
          {humanize(c.state)} {c.decidedAt ? formatDate(c.decidedAt) : ""}
          {c.state === "rejected" && `. ${comesBack(c)}`}
          {c.state === "approved" && (
            <>
              {" · "}
              <Link to={`/answer-bank?entry=${encodeURIComponent(c.targetEntryId || c.entryId)}`}>Open the entry</Link>
            </>
          )}
        </p>
      )}
      {!pending && (
        <div className="actions">
          <button type="button" disabled={busy} onClick={() => (approved ? setAsking(true) : void back())}>
            {approved ? "Take back" : "Restore"}
          </button>
        </div>
      )}
      {fail !== undefined && <ErrorNote error={fail} />}
      {asking && (
        <ConfirmDialog
          title="Take back this approval?"
          consequence={`${c.type === "extend-examples" ? `The phrasing "${c.seedQuestion}" leaves the entry ${c.targetEntryId}` : `The entry it added for "${c.seedQuestion}" leaves your Answer Bank`}, and the suggestion waits for your decision again.`}
          verb="Take back"
          busy={busy}
          onCancel={() => setAsking(false)}
          onConfirm={() => void back()}
        />
      )}
      {pending && (
        <div className="actions">
          <button type="button" className="primary" disabled={busy || (draft !== "" && text.trim() === "")} onClick={() => (draft ? onEditApprove(text.trim()) : onApprove())}>
            Approve
          </button>
          {editable && !editing && (
            <button type="button" disabled={busy} onClick={() => setEditing(true)}>
              Edit and approve
            </button>
          )}
          <button type="button" className="ghost" disabled={busy} onClick={onReject}>
            Reject
          </button>
        </div>
      )}
    </li>
  );
}

// clip keeps a long value short in a sentence or on a button.
const clip = (s: string) => (s.length > 60 ? `${s.slice(0, 59)}\u2026` : s);

// KeepEdits offers the keep press on each value you changed by hand over the
// one an Answer Bank entry wrote (flow round 9, K-1): one press makes it that
// entry's answer from now on, the other leaves the entry's answer and stops
// that one change counting against it (flow round 10, K2-1). Undo takes either
// press back. The server lists only the edits the press can keep; reload reads
// them again after a press.
export function KeepEdits({ edits, reload }: { edits: AnswerEdit[]; reload: () => void }) {
  const id = useId();
  const [busy, setBusy] = useState(false);
  const [fail, setFail] = useState<unknown>();
  const [pressed, setPressed] = useState<{ n: number; e: AnswerEdit; done: string; undone: string }>();
  const presses = useRef(0);
  if (edits.length === 0 && fail === undefined && !pressed) return null;
  const press = async (e: AnswerEdit, keep: boolean) => {
    setBusy(true);
    setFail(undefined);
    setPressed(undefined);
    try {
      await (keep ? post(`/api/answer-bank/manual-fills/${e.fillId}/keep`, answerBankSaved) : post(`/api/answer-bank/manual-fills/${e.fillId}/leave`, answerEdit));
      setPressed({
        n: ++presses.current,
        e,
        done: keep ? `${e.question} is answered ${clip(e.after)} from now on.` : `${e.question} keeps its saved answer, and your change to ${clip(e.after)} no longer counts against it.`,
        undone: keep ? `${e.question} is answered ${clip(e.before)} again, and your change is offered again.` : `Your change to ${clip(e.after)} counts again and is offered again.`,
      });
    } catch (err) {
      setFail(err);
    } finally {
      setBusy(false);
      reload();
    }
  };
  // undo takes the press back; one the entry changed since is refused, and the
  // refusal says why.
  const undo = async (e: AnswerEdit) => {
    setFail(undefined);
    try {
      await post(`/api/answer-bank/manual-fills/${e.fillId}/undo`, answerBankSaved);
      return true;
    } catch (err) {
      setFail(err);
      return false;
    } finally {
      reload();
    }
  };
  return (
    <section aria-labelledby={id}>
      <h3 id={id}>Answers you changed</h3>
      <div role="status">{pressed && <UndoNote key={pressed.n} done={pressed.done} undone={pressed.undone} onUndo={() => undo(pressed.e)} />}</div>
      <div aria-live="polite">{fail !== undefined && <ErrorNote error={fail} />}</div>
      <ul className="plain stack">
        {edits.map((e) => (
          <li key={e.fillId} className="panel candidate">
            <p>
              You changed <strong>{e.question}</strong> from <span title={e.before}>{clip(e.before)}</span> to <strong title={e.after}>{clip(e.after)}</strong>. Use{" "}
              {clip(e.after)} from now on?
            </p>
            <p className="muted">
              It is saved to your Answer Bank entry <Link to={`/answer-bank?entry=${encodeURIComponent(e.entryId)}`}>{e.entryId}</Link>.
            </p>
            <div className="actions">
              <button type="button" className="primary" disabled={busy} onClick={() => void press(e, true)}>
                Use {clip(e.after)}
              </button>
              <button type="button" disabled={busy} onClick={() => void press(e, false)}>
                Keep {clip(e.before)}
              </button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

// The decisions the chips above the queue filter by; waiting is the queue.
const STATES = [
  { id: "pending", label: "Waiting", empty: "No suggested answers are waiting. One appears after the same question is left unanswered or filled by hand three times." },
  { id: "approved", label: "Approved", empty: "No suggestion has been approved yet." },
  { id: "rejected", label: "Rejected", empty: "No suggestion has been rejected." },
] as const;

// Candidates is the queue of answers learned from abstentions and from the
// user's own fills; the page calls them suggestions, as Home does. Nothing here approves on its own: every approval is a
// button the user presses. The decided ones are a chip away, each with its take-back. The waiting ones the review
// found not worth an entry are listed apart under "Not suggested", each with its reason and Reject.
export function Candidates({ candidates, edits, reload }: { candidates: AnswerCandidate[]; edits: AnswerEdit[]; reload: () => void }) {
  const [params] = useSearchParams();
  const view = STATES.find((s) => s.id === params.get("state")) ?? STATES[0];
  const [picked, setPicked] = useState<Set<number>>(new Set());
  const [confirmBulk, setConfirmBulk] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Result>(null);
  const [decided, setDecided] = useState<Decided>();
  const presses = useRef(0);
  // typed holds what each row's answer box says now, by candidate id.
  const [typed, setTyped] = useState(new Map<number, string>());
  const pending = candidates.filter((c) => c.state === "pending" && suggested(c));
  const hidden = candidates.filter((c) => c.state === "pending" && !suggested(c));
  // The picks still waiting: a decision made since takes its row out.
  const chosen = pending.filter((c) => picked.has(c.id));
  const shown = view.id === "pending" ? pending : candidates.filter((c) => c.state === view.id);
  const count = (id: string) => (id === "pending" ? pending : candidates.filter((c) => c.state === id)).length;
  // A chip with nothing behind it is left out, unless it is the one in force.
  const chips = STATES.filter((s) => s.id === "pending" || count(s.id) > 0 || s === view);

  const run = async (go: () => Promise<NonNullable<Result>>) => {
    setBusy(true);
    setDecided(undefined);
    try {
      const r = await go();
      setResult(r);
      if (r.fail === undefined) setPicked(new Set());
      reload();
    } catch (e) {
      setResult({ fail: e });
      reload();
    } finally {
      setBusy(false);
    }
  };

  const approve = (id: number, answer?: string) => post(`/api/answer-bank/candidates/${id}/approve`, answerCandidateDecision, { answer });
  // decide presses one decision and offers its Undo.
  const decide = (c: AnswerCandidate, go: () => Promise<Omit<Decided, "c" | "n">>) =>
    run(async () => {
      const d = await go();
      setDecided({ ...d, c, n: ++presses.current });
      return {};
    });
  const approveOne = (c: AnswerCandidate, answer?: string) =>
    decide(c, async () => {
      const { entryId } = await approve(c.id, answer);
      return {
        done: (
          <>
            {answer ? "Approved with your answer" : "Approved"}: {c.seedQuestion}. <Link to={`/answer-bank?entry=${encodeURIComponent(entryId)}`}>Open the entry</Link>
          </>
        ),
        undone: waitsAgain({ ...c, state: "approved" }),
        back: "unapprove",
      };
    });
  const reject = (c: AnswerCandidate) =>
    decide(c, async () => {
      await post(`/api/answer-bank/candidates/${c.id}/reject`, answerCandidateDecision);
      return { done: `Rejected: ${c.seedQuestion}. ${comesBack(c)}`, undone: waitsAgain({ ...c, state: "rejected" }), back: "restore" };
    });
  // takeBack undoes the decision; one the bank has settled or changed since is
  // refused, and the refusal says why.
  const takeBack = async ({ c, back }: Decided) => {
    try {
      await post(`/api/answer-bank/candidates/${c.id}/${back}`, answerCandidateDecision);
      return true;
    } catch (e) {
      setResult({ fail: e });
      return false;
    } finally {
      reload();
    }
  };
  // backFromRow takes a decision back from the Approved or Rejected list; the
  // row shows a refusal.
  const backFromRow = async (c: AnswerCandidate) => {
    setBusy(true);
    setDecided(undefined);
    setResult(null);
    try {
      await post(`/api/answer-bank/candidates/${c.id}/${c.state === "approved" ? "unapprove" : "restore"}`, answerCandidateDecision);
      setResult({ say: waitsAgain(c) });
    } finally {
      setBusy(false);
      reload();
    }
  };
  // A drafted suggestion is approved with what its answer box holds. One whose
  // box was emptied waits for an answer: its own Approve is off, and the bulk
  // approval leaves it out.
  const boxOf = (c: AnswerCandidate) => (draftOf(c) ? (typed.get(c.id) ?? draftOf(c)).trim() || undefined : undefined);
  const ready = chosen.filter((c) => !draftOf(c) || boxOf(c) !== undefined);
  const drafted = ready.filter((c) => boxOf(c) !== undefined).length;
  const approveMany = () =>
    run(async () => {
      let done = 0;
      try {
        for (const c of ready) {
          await approve(c.id, boxOf(c));
          done++;
        }
      } catch (fail) {
        if (done === 0) throw fail;
        return { say: `Approved ${done} of ${ready.length} suggestions. The ones not approved are still selected.`, fail };
      }
      return { say: `Approved ${done} suggestions.` };
    });
  const generate = () =>
    run(async () => {
      const r = await post("/api/answer-bank/candidates/generate", answerCandidateGenerateResult, {});
      const say = `Looked for new suggestions: ${r.created} new, ${r.refreshed} refreshed.`;
      const unsorted = r.candidates.filter((c) => c.state === "pending" && c.review.verdict === "").length;
      return { say: unsorted === 0 ? say : `${say} ${await sorting(unsorted)}` };
    });

  return (
    <div>
      {chips.length > 1 && (
        <FilterChips
          label="Suggestions by decision"
          items={chips.map((s) => ({ label: s.label, count: count(s.id), to: `/answer-bank?tab=candidates${s.id === "pending" ? "" : `&state=${s.id}`}`, current: s === view }))}
        />
      )}
      <div className="toolbar">
        {view.id === "pending" && pending.length > 1 && (
          <label className="check">
            <input type="checkbox" checked={chosen.length === pending.length} onChange={(e) => setPicked(e.target.checked ? new Set(pending.map((c) => c.id)) : new Set())} />
            Select all
          </label>
        )}
        {view.id === "pending" && pending.length > 0 && (
          <button type="button" disabled={busy || ready.length === 0} onClick={() => (ready.length > 1 ? setConfirmBulk(true) : void approveMany())}>
            Approve selected ({ready.length})
          </button>
        )}
        <button type="button" className="ghost" disabled={busy} onClick={() => void generate()}>
          Look for new suggestions
        </button>
      </div>
      <div aria-live="polite">
        {result?.say && <p className="note ok"><ModelsLinked text={result.say} /></p>}
        {result?.fail !== undefined && <ErrorNote error={result.fail} />}
      </div>
      <div role="status">
        {decided && <UndoNote key={decided.n} done={decided.done} undone={decided.undone} onUndo={() => takeBack(decided)} />}
      </div>
      {view.id === "pending" && <KeepEdits edits={edits} reload={reload} />}
      {shown.length === 0 ? (
        (view.id !== "pending" || edits.length === 0) && <Empty>{view.empty}</Empty>
      ) : (
        <ul className="plain stack">
          {shown.map((c) => (
            <Row
              key={`${c.id}:${c.review.verdict}`}
              c={c}
              busy={busy}
              checked={picked.has(c.id)}
              onCheck={(v) => setPicked((s) => (v ? new Set(s).add(c.id) : new Set([...s].filter((x) => x !== c.id))))}
              onApprove={() => void approveOne(c)}
              onEditApprove={(t) => void approveOne(c, t)}
              onReject={() => void reject(c)}
              onBack={() => backFromRow(c)}
              onText={(t) => setTyped((m) => new Map(m).set(c.id, t))}
            />
          ))}
        </ul>
      )}
      {view.id === "pending" && hidden.length > 0 && (
        <details className="earlier">
          <summary>Not suggested ({hidden.length})</summary>
          <ul className="plain stack">
            {hidden.map((c) => (
              <li key={c.id} className="panel candidate">
                <div className="row">
                  <strong>{c.seedQuestion}</strong>
                  <Chip>{humanize(c.review.verdict)}</Chip>
                </div>
                <p className="muted">{c.review.reason}</p>
                <div className="actions">
                  <button type="button" className="ghost" disabled={busy} onClick={() => void reject(c)}>
                    Reject
                  </button>
                </div>
              </li>
            ))}
          </ul>
        </details>
      )}
      {confirmBulk && (
        <ConfirmDialog
          title={`Approve ${ready.length} suggestions`}
          consequence={`${ready.length} new entries or phrasings are written to your answer bank${drafted > 0 ? `, ${drafted} with the answer drafted from your profile and CV as its box shows it` : ""}. Each new entry is marked for review.`}
          verb="Approve all"
          busy={busy}
          onCancel={() => setConfirmBulk(false)}
          onConfirm={() => {
            setConfirmBulk(false);
            void approveMany();
          }}
        />
      )}
    </div>
  );
}
