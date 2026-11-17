import { useState } from "react";
import { Link } from "../../router";
import { ApplyOutcome, useApply } from "./apply";
import { FieldErrors } from "./Form";
import { ASK_ME } from "../answerbank/model";
import { blockingEntry, type ScreenProps } from "./model";
import { ReasonList } from "./Reasons";

interface Blocking {
  group: string;
  id: string;
  description: string;
}

// AnswerBank lists the entries that still block the step, by group and with
// their count, one input each. An answer can be the ask-me marker, which leaves
// the question for the form's own prompt. What the earlier steps state never
// blocks: the bank answers it live and the Answer Bank page shows it "From
// Setup". Defaults that need a person's confirmation are confirmed with one
// button, apart from the answers.
export function AnswerBank({ step, reload }: ScreenProps) {
  const apply = useApply(step.id, reload);
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [askMe, setAskMe] = useState<Set<string>>(new Set());

  const parsed = step.reasons.map((r) => ({ r, e: r.field ? blockingEntry(r) : null }));
  const entries: Blocking[] = parsed.flatMap(({ r, e }) => (e && r.field ? [{ group: r.field, ...e }] : []));
  // The server's own count line is shown as the count, not as something to answer.
  const countLine = step.reasons.find((r) => r.field === "blocking")?.text;
  const others = parsed.filter((p) => !p.e && p.r.field !== "blocking").map((p) => p.r);
  const groups = new Map<string, Blocking[]>();
  for (const e of entries) groups.set(e.group, [...(groups.get(e.group) ?? []), e]);

  const given = entries.flatMap((e) => (askMe.has(e.id) ? [[e.id, ASK_ME]] : answers[e.id]?.trim() ? [[e.id, answers[e.id].trim()]] : []));
  const save = () => void apply.send({ answers: Object.fromEntries(given) }).then((a) => a && (setAnswers({}), setAskMe(new Set())));
  // The listed entries not answered here are left to the person; what was
  // typed or ticked is kept.
  const typed = Object.fromEntries(given);
  const leaveRest = () => void apply.send({ answers: Object.fromEntries(entries.map((e) => [e.id, typed[e.id] ?? ASK_ME])) }).then((a) => a && (setAnswers({}), setAskMe(new Set())));
  const rest = (
    <button type="button" disabled={apply.busy} onClick={leaveRest}>
      {given.length > 0 ? "Save these and leave the rest to me" : "Leave all of them to me"}
    </button>
  );
  const toggle = (id: string) => setAskMe((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; });

  return (
    <div>
      <p role="status">
        {countLine ? `${countLine.replace(/\.$/, "")}.` : entries.length === 0 ? "No entry blocks this step." : `${entries.length} blocking ${entries.length === 1 ? "entry remains" : "entries remain"}.`}{" "}
        Only Blocking entries hold it; the others <Link to="/answer-bank?show=needs">to answer</Link> on the Answer Bank page can wait.
      </p>
      {entries.length > 0 && (
        <>
          <p className="muted">Answer the ones you know. One you leave to yourself is never guessed: a form that asks it is left for you to answer.</p>
          <div className="actions">{rest}</div>
        </>
      )}
      {[...groups].map(([group, list]) => (
        <fieldset key={group} data-group={group}>
          <legend>
            {group} ({list.length})
          </legend>
          <ul className="plain stack">
            {list.map((e) => (
              <li key={e.id} data-entry={e.id} className="form-grid">
                <label>
                  {e.description} <code>{e.id}</code>
                  <input type="text" disabled={askMe.has(e.id)} value={askMe.has(e.id) ? "" : (answers[e.id] ?? "")} onChange={(ev) => setAnswers({ ...answers, [e.id]: ev.target.value })} />
                </label>
                <label className="check">
                  <input type="checkbox" checked={askMe.has(e.id)} onChange={() => toggle(e.id)} />
                  Leave this to me
                </label>
              </li>
            ))}
          </ul>
        </fieldset>
      ))}
      <FieldErrors id="answers-errors" errors={apply.byField.answers} />
      <div className="actions">
        <button type="button" disabled={apply.busy || given.length === 0} onClick={save}>
          Save answers
        </button>
        {entries.length > 0 && rest}
      </div>
      {others.length > 0 && (
        <>
          <h3>Also needed</h3>
          <ReasonList reasons={others} />
        </>
      )}
      <h3>Reviewed defaults</h3>
      <p>The self-identification and compliance defaults ship answered. Read them in the full answer bank, then confirm them as they are.</p>
      <div className="actions">
        <button type="button" disabled={apply.busy} onClick={() => void apply.send({ confirm_review: true })}>
          Confirm the reviewed defaults
        </button>
        <Link to="/answer-bank">Open the full answer bank</Link>
      </div>
      <ApplyOutcome apply={apply} />
    </div>
  );
}
