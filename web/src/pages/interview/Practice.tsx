import { useEffect, useRef, useState, type FormEvent } from "react";
import { newRequestId, post, postModel, request } from "../../api/client";
import { interviewFeedback } from "../../api/decoders_interview";
import { kbMiningSession } from "../../api/decoders_pages";
import { runAccepted, runView } from "../../api/decoders_runs";
import { useAction } from "../action";
import { useRunLines } from "../discovery/runLines";
import { runReason } from "../documents/run";
import { leave, Link } from "../../router";
import { ModelText } from "../../ui/Markdown";
import { useSaveKey, useUnsaved } from "../../ui/SaveBar";
import { ErrorNote, formatDate } from "../../ui/parts";
import type { InterviewCoachRun, InterviewFeedback, InterviewMockTurn, KBPrincipleGrade } from "../../wire";

// Turns stored before the coach note became its own paragraph put it on the
// line after the last check, where Markdown reads it as part of that bullet.
const coaching = (text: string) => text.replace(/([^\n])\nCoach: /, "$1\n\nCoach: ");

const FROM = [
  ["user", "Me"],
  ["email", "An email"],
  ["interviewer", "The interviewer"],
] as const;

// Each form holds the drawer while it has text not sent yet.
export function FeedbackForm({ base, feedback, onSaved }: { base: string; feedback: InterviewFeedback[]; onSaved: () => void }) {
  const [from, setFrom] = useState("user");
  const [text, setText] = useState("");
  const [sent, setSent] = useState(false);
  const box = useRef<HTMLTextAreaElement>(null);
  const a = useAction();
  const hold = useUnsaved(text.trim() !== "");
  const saveKey = useSaveKey(text.trim() !== "");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if ((await a.run(() => post(`${base}/feedback`, interviewFeedback, { from, text }))) !== undefined) {
      setText("");
      setSent(true);
      onSaved();
      box.current?.focus();
    }
  };
  return (
    <section aria-label="Feedback">
      <h4>Debrief and feedback</h4>
      <p className="muted">After the interview, note what they asked and how it went while you remember it. The next packs of this stage and of the later ones include it.</p>
      <form onSubmit={submit} className="form-grid" aria-label="Record feedback">
        <label>
          From
          <select value={from} onChange={(e) => setFrom(e.target.value)}>
            {FROM.map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <label>
          What was said
          <textarea ref={box} value={text} maxLength={2000} onChange={(e) => (setText(e.target.value), setSent(false))} />
        </label>
        <div className="actions">
          <button type="submit" disabled={a.busy || text.trim() === ""} ref={saveKey}>
            Record feedback
          </button>
        </div>
        {a.error && <ErrorNote error={a.error} />}
        {sent && <p className="muted" role="status">Recorded.</p>}
      </form>
      {hold}
      {feedback.length > 0 && (
        <ul className="plain stack">
          {feedback.map((f) => (
            <li key={f.id}>
              <span className="muted">
                {formatDate(f.recordedAt)}, {FROM.find(([v]) => v === f.from)?.[1] ?? f.from}:
              </span>{" "}
              {f.text}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// A drill question, by where it comes from.
type Pick = { group: string; label: string; prompt: string };

// drillQuestions lists the pack's questions, then the questions of the themes
// no story covers yet, then the rest of the competency library, each once.
function drillQuestions(prompts: string[], themes: KBPrincipleGrade[]): Pick[] {
  const out: Pick[] = prompts.map((p) => ({ group: "This pack", label: p, prompt: p }));
  const add = (group: string, ts: KBPrincipleGrade[]) => ts.forEach((t) => t.questions.forEach((q) => out.push({ group, label: `${t.name}: ${q}`, prompt: q })));
  add("Themes with no story yet", themes.filter((t) => t.stories.length === 0));
  add("Question library", themes.filter((t) => t.stories.length > 0));
  return out.filter((p, i) => out.findIndex((x) => x.prompt === p.prompt) === i);
}

// The three cuts of an answer, by the seconds each may take, and what each keeps.
const CUTS = [
  [45, "the 45-second cut: keep the tension, your key action and the result."],
  [90, "the 90-second cut, the one to rehearse: one clear trade-off and one result someone can check."],
  [180, "the 3-minute cut: give it only when the interviewer asks for more; add the numbers and what you learned."],
] as const;

// spoken is how long the answer takes at about 150 words a minute, and its cut.
function spoken(words: number): string {
  const s = Math.round((words / 150) * 60);
  const cut = CUTS.find(([max]) => s <= max)?.[1] ?? "longer than any cut: trim it to the 90-second version.";
  return `about ${s < 60 ? `${s} s` : `${(s / 60).toFixed(1)} min`} spoken, ${cut}`;
}

const AREAS = [["project", "A project"], ["team", "A team conflict"], ["customer", "A customer"], ["failure", "A failure and its lesson"]] as const;

// ToStory starts a story interview from a practised answer: the model copies
// the parts the answer states word for word, and the story bank asks for the rest.
function ToStory({ turn }: { turn: InterviewMockTurn }) {
  const [area, setArea] = useState("project");
  const [started, setStarted] = useState(false);
  const note = useRef<HTMLParagraphElement>(null);
  const a = useAction();
  useEffect(() => {
    if (started) note.current?.focus();
  }, [started]);
  const go = async () => {
    if (await a.run(() => postModel("/api/kb/stories/mining", kbMiningSession, { quadrant: area, notes: `Question: ${turn.question}\n\nMy answer: ${turn.answer}` }))) setStarted(true);
  };
  if (started) {
    return (
      <p role="status" tabIndex={-1} ref={note}>
        A story interview started from this answer, with the parts it states filled in. <Link to="/wiki?tab=stories">Finish it in the story bank</Link>
      </p>
    );
  }
  return (
    <div className="actions">
      <label>
        Story area{" "}
        <select value={area} onChange={(e) => setArea(e.target.value)}>
          {AREAS.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </select>
      </label>
      <button type="button" onClick={go} disabled={a.busy}>
        {a.busy ? "Reading your answer" : "Turn this answer into a story"}
      </button>
      {a.error && <ErrorNote error={a.error} />}
    </div>
  );
}

// seed is a question picked from the pack; n counts picks, so a repeat still focuses the answer.
// The answer stays after coaching, so it can be revised and coached again;
// Next question clears it, after asking when it was not coached.
// Coaching is a run that goes on when the drawer closes; coachRun is one still
// working when the drawer opens, followed again with its question and answer.
export function MockPractice({ base, prompts, storiesOf, themes, turns, coachRun, seed, onSaved }: { base: string; prompts: string[]; storiesOf: (question: string) => string[]; themes: KBPrincipleGrade[]; turns: InterviewMockTurn[]; coachRun?: InterviewCoachRun; seed: { q: string; n: number } | null; onSaved: () => void }) {
  const [question, setQuestion] = useState("");
  const answerRef = useRef<HTMLTextAreaElement>(null);
  const coachRef = useRef<HTMLDivElement>(null);
  const [answer, setAnswer] = useState("");
  const [last, setLast] = useState<InterviewMockTurn | null>(null);
  const [run, setRun] = useState<InterviewCoachRun>();
  const { final } = useRunLines(run?.runId, onSaved);
  const busy = run !== undefined && !final;
  useEffect(() => {
    if (coachRun && !run) (setRun(coachRun), setQuestion(coachRun.question), setAnswer(coachRun.answer));
  }, [coachRun?.runId]);
  // The coached answer lands as a turn; it shows once the turns hold it.
  useEffect(() => {
    const t = final?.status === "succeeded" && turns.find((x) => x.question === run?.question && x.answer === run.answer);
    if (t) (setLast(t), setRun(undefined));
  }, [final, turns]);
  const hold = useUnsaved(!busy && answer.trim() !== "" && answer.trim() !== last?.answer);
  useEffect(() => {
    if (last) coachRef.current?.focus();
  }, [last]);
  const ask = (q: string) => {
    setQuestion(q);
    setLast(null);
    answerRef.current?.focus();
  };
  // The form is mounted per stage: a pick older than it was for another stage.
  const stale = useRef(seed);
  useEffect(() => {
    if (seed && seed !== stale.current) ask(seed.q);
  }, [seed]);
  const a = useAction();
  const picks = drillQuestions(prompts, themes);
  const at = picks.findIndex((p) => p.prompt === question);
  const practised = new Set(turns.map((t) => t.question));
  const gaps = themes.filter((t) => t.stories.length === 0);
  const words = answer.trim() === "" ? 0 : answer.trim().split(/\s+/).length;
  const drawOn = storiesOf(question);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const sent = { question: question.trim(), answer: answer.trim() };
    setLast(null);
    const out = await a.run(() => request("POST", `${base}/mock-turns`, runAccepted, { body: sent, headers: { "Idempotency-Key": newRequestId() } }));
    if (out) setRun({ ...sent, runId: out.runId, stage: "" });
  };
  return (
    <section aria-label="Practice">
      <h4>Practice an answer</h4>
      <p className="muted">Pick a question, answer it as you would say it, and get coaching. The coaching checks your answer against the résumé you submitted and your story bank, and names the stories it draws on. It does not review the content.</p>
      {gaps.length > 0 && (
        <p className="note" role="note">
          {gaps.length === themes.length ? "Your story bank holds no story yet, so no theme is covered." : `No story in your bank covers ${gaps.map((t) => t.name).join(", ")} yet.`} Their questions are under Themes with no story yet in the picker; an answer you are happy with is worth adding as a story. <Link to="/wiki?tab=stories">Open the story bank</Link>
        </p>
      )}
      <form onSubmit={submit} className="form-grid" aria-label="Practice an answer form">
        {picks.length > 0 && (
          <label>
            Pick a question
            <select value={at < 0 ? "" : String(at)} onChange={(e) => e.target.value !== "" && ask(picks[Number(e.target.value)].prompt)}>
              <option value="">Choose a question</option>
              {[...new Set(picks.map((p) => p.group))].map((g) => (
                <optgroup key={g} label={g}>
                  {picks.map((p, i) => p.group === g && (
                    <option key={i} value={i}>
                      {p.label}
                      {practised.has(p.prompt) ? " (practised)" : ""}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
          </label>
        )}
        <label>
          Question
          <input value={question} maxLength={1000} onChange={(e) => setQuestion(e.target.value)} />
        </label>
        {drawOn.length > 0 && <p className="muted">Your {drawOn.length === 1 ? "story" : "stories"} for this question: {drawOn.join(", ")}. Tell one; the coaching checks the figures and claims against your story bank.</p>}
        <label>
          Your answer
          <textarea ref={answerRef} value={answer} maxLength={4000} onChange={(e) => setAnswer(e.target.value)} />
        </label>
        {words > 0 && <p className="muted">{words} {words === 1 ? "word" : "words"}, {spoken(words)}</p>}
        <div className="actions">
          <button type="submit" disabled={a.busy || busy || question.trim() === "" || answer.trim() === ""}>
            Get coaching
          </button>
          {busy && <button type="button" onClick={() => a.run(() => post(`/api/runs/${run?.runId}/cancel`, runView))}>Stop coaching</button>}
          {picks.length > 1 && (
            <button type="button" onClick={() => leave(() => { setAnswer(""); ask(picks[(at + 1) % picks.length].prompt); })}>
              Next question
            </button>
          )}
        </div>
        {a.error && <ErrorNote error={a.error} />}
        <div aria-live="polite">
          {busy && <p className="muted" role="status">Coaching your answer. It goes on if you close this drawer.</p>}
          {final && final.status !== "succeeded" && <p className="note error">The answer was not coached ({runReason(final.reason, final.status)}). It is still above to send again.</p>}
        </div>
      </form>
      {hold}
      {last && (
        <>
          <div className="note" role="status" aria-label="Coaching" tabIndex={-1} ref={coachRef}>
            <ModelText source={last.coaching} />
            <p className="muted">Revise your answer above and get coaching again, or go to the next question.</p>
          </div>
          <ToStory key={last.id} turn={last} />
        </>
      )}
      {turns.length > 0 && (
        <details>
          <summary>Earlier practice ({turns.length})</summary>
          <ul className="plain stack">
            {turns.map((t) => (
              <li key={t.id}>
                <strong>{t.question}</strong> <span className="muted">{formatDate(t.recordedAt)}</span>
                <details>
                  <summary>Your answer</summary>
                  <p className="pre">{t.answer}</p>
                </details>
                <ModelText source={coaching(t.coaching)} />
              </li>
            ))}
          </ul>
        </details>
      )}
    </section>
  );
}
