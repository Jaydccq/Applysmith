import { useEffect, useState, type FormEvent } from "react";
import { get, newRequestId, post, request } from "../../api/client";
import { gapList, gapReport } from "../../api/decoders_insights";
import { runAccepted } from "../../api/decoders_runs";
import { useResource } from "../../hooks";
import { Chip, Empty, ErrorNote, formatDate, Loading } from "../../ui/parts";
import { useAction } from "../action";
import type { Gap, GapReport } from "../../wire";

const MAX_PLAN = 5;
const STATE_LABEL: Record<string, string> = { "confirmed-gap": "Confirmed gap", "not-evidenced": "Not evidenced", "needs-review": "Needs review" };
const STATE_NOTE: Record<string, string> = {
  "confirmed-gap": "You said you lack this skill.",
  "not-evidenced": "Your profile shows nothing for it. That does not prove you lack the skill.",
  "needs-review": "The profile shows partial or unsupported evidence. Check it before treating this as a gap.",
};
const REASON_LABEL: Record<string, string> = {
  "not-found": "no such report",
  unreadable: "the report file could not be read",
  "no-requirement-evidence": "no saved requirement evidence (not a final report)",
  "duplicate-posting": "the same posting as a newer report",
};

function stateTone(state: string) {
  return state === "confirmed-gap" ? "bad" : state === "needs-review" ? "warn" : "neutral";
}

function Sources({ gap }: { gap: Gap }) {
  return (
    <details>
      <summary>
        {gap.sources.length} {gap.sources.length === 1 ? "posting" : "postings"}
      </summary>
      <ul className="plain stack">
        {gap.sources.map((s) => (
          <li key={`${s.report}-${s.class}`}>
            Report {s.report}, {s.company}: {s.role}. {s.class === "required" ? "Required" : "Preferred"}: {s.requirement}.{" "}
            {s.evidence ? <span>Profile evidence: {s.evidence}</span> : <span className="muted">No profile evidence.</span>}
            {s.unsupported && <span className="muted"> A claimed match named no profile source.</span>}
          </li>
        ))}
      </ul>
    </details>
  );
}

function GapRow({ gap, base, picked, onPick, onChange }: { gap: Gap; base: string; picked: boolean; onPick: (on: boolean) => void; onChange: (r: GapReport) => void }) {
  const a = useAction();
  const confirmed = gap.state === "confirmed-gap";
  const toggle = async () => {
    const r = await a.run(() => post(`${base}/confirm`, gapReport, { skill: gap.skill, confirmed: !confirmed }));
    if (r) onChange(r);
  };
  return (
    <tr>
      <td data-label="Plan">
        <input type="checkbox" aria-label={`Plan ${gap.label}`} checked={picked} disabled={!confirmed} onChange={(e) => onPick(e.target.checked)} />
      </td>
      <td data-label="Skill">{gap.label}</td>
      <td data-label="State">
        <span title={STATE_NOTE[gap.state]}>
          <Chip tone={stateTone(gap.state)}>{STATE_LABEL[gap.state] ?? gap.state}</Chip>
        </span>
      </td>
      <td data-label="Required">{gap.required}</td>
      <td data-label="Preferred">{gap.preferred}</td>
      <td data-label="Postings">
        <Sources gap={gap} />
      </td>
      <td>
        <button type="button" onClick={toggle} disabled={a.busy} aria-label={`${confirmed ? "Take back" : "Confirm"} ${gap.label}`}>
          {confirmed ? "Take back" : "I lack this"}
        </button>
        {a.error && <ErrorNote error={a.error} />}
      </td>
    </tr>
  );
}

function Plan({ rep, base, onChange }: { rep: GapReport; base: string; onChange: (r: GapReport) => void }) {
  const a = useAction();
  if (!rep.plan) return null;
  const mark = async (skill: string, done: boolean) => {
    const r = await a.run(() => post(`${base}/progress`, gapReport, { skill, done }));
    if (r) onChange(r);
  };
  const lookup = rep.plan.lookup;
  return (
    <section aria-label="Learning plan">
      <h3>Learning plan</h3>
      {lookup.status === "unavailable" && <p role="note" className="note">Resource lookup unavailable: {lookup.reason}. The plan has exercises only.</p>}
      {lookup.status === "failed" && <p role="note" className="note error">{lookup.reason}</p>}
      <ul className="plain stack">
        {rep.plan.items.map((it) => (
          <li key={it.skill}>
            <h4>{it.label}</h4>
            <p>Exercise: {it.exercise}</p>
            <p>Done when: {it.criterion}</p>
            <p className="muted">{it.effort}</p>
            {it.resources.length > 0 && (
              <ul aria-label={`Resources for ${it.label}`}>
                {it.resources.map((r) => (
                  <li key={r.url}>
                    <a href={r.url} target="_blank" rel="noopener noreferrer">
                      {r.title}
                    </a>
                  </li>
                ))}
              </ul>
            )}
            <label className="check">
              <input type="checkbox" checked={it.done} disabled={a.busy} onChange={(e) => mark(it.skill, e.target.checked)} /> Exercise complete: {it.label}
            </label>
          </li>
        ))}
      </ul>
      <p className="muted">Marking an exercise complete records study progress here only. It adds no experience or skill to your profile, résumé or answers.</p>
      {a.error && <ErrorNote error={a.error} />}
    </section>
  );
}

function Analysis({ id }: { id: string }) {
  const base = `/api/evaluations/gaps/${id}`;
  const [res, retry] = useResource(() => get(base, gapReport), [base], ["reports"]);
  const [local, setLocal] = useState<GapReport | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [lookup, setLookup] = useState(false);
  const plan = useAction();
  useEffect(() => setLocal(null), [res.data]);
  useEffect(() => {
    setPicked([]);
    setLocal(null);
  }, [id]);
  if (res.status === "loading" && !res.data) return <Loading />;
  if (res.status === "error" && !res.data) return <ErrorNote error={res.error} onRetry={retry} />;
  const rep = local ?? res.data!;
  const confirmedKeys = new Set(rep.gaps.filter((g) => g.state === "confirmed-gap").map((g) => g.skill));
  const chosen = picked.filter((s) => confirmedKeys.has(s));
  const makePlan = async () => {
    const r = await plan.run(() => post(`${base}/plan`, gapReport, { skills: chosen, lookupResources: lookup }));
    if (r) setLocal(r);
  };
  const pick = (skill: string, on: boolean) => setPicked((p) => (on ? [...p.filter((s) => s !== skill), skill] : p.filter((s) => s !== skill)));
  return (
    <article aria-label="Skill-gap analysis">
      <p>
        {rep.sample.length} {rep.sample.length === 1 ? "posting" : "postings"} analyzed from {rep.selected.length} selected on {formatDate(rep.generatedAt)}. This analysis has no fit score: it lists what the postings ask for that your profile does not evidence.
      </p>
      {rep.excluded.length > 0 && (
        <details>
          <summary>{rep.excluded.length} excluded</summary>
          <ul className="plain">
            {rep.excluded.map((x) => (
              <li key={x.report}>
                Report {x.report}: {REASON_LABEL[x.reason] ?? x.reason}
                {x.duplicateOf > 0 && ` (report ${x.duplicateOf})`}
              </li>
            ))}
          </ul>
        </details>
      )}
      {rep.gaps.length === 0 ? (
        <Empty>No requirement in this sample is missing profile evidence.</Empty>
      ) : (
        <table className="cards">
          <caption>Gaps, most demanded first</caption>
          <thead>
            <tr>
              <th scope="col">Plan</th>
              <th scope="col">Skill</th>
              <th scope="col">State</th>
              <th scope="col">Required</th>
              <th scope="col">Preferred</th>
              <th scope="col">Postings</th>
              <th scope="col">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {rep.gaps.map((g) => (
              <GapRow key={g.skill} gap={g} base={base} picked={chosen.includes(g.skill)} onPick={(on) => pick(g.skill, on)} onChange={setLocal} />
            ))}
          </tbody>
        </table>
      )}
      <p className="muted">Required and preferred count distinct postings that state the requirement. Confirm a gap before planning to learn it.</p>
      {rep.gaps.length > 0 && (
        <div className="actions">
          <label className="check">
            <input type="checkbox" checked={lookup} onChange={(e) => setLookup(e.target.checked)} /> Look up learning resources
          </label>
          <button type="button" onClick={makePlan} disabled={plan.busy || chosen.length === 0 || chosen.length > MAX_PLAN}>
            Build a plan for {chosen.length} {chosen.length === 1 ? "gap" : "gaps"}
          </button>
          {chosen.length > MAX_PLAN && <span className="muted">A plan covers at most {MAX_PLAN} gaps.</span>}
        </div>
      )}
      {plan.error && <ErrorNote error={plan.error} />}
      <Plan rep={rep} base={base} onChange={setLocal} />
      {rep.inferred.length > 0 && (
        <details>
          <summary>{rep.inferred.length} inferred, not counted</summary>
          <p className="muted">The evaluation read these between the lines of a posting. They are not explicit requirements and are left out of the counts above.</p>
          <ul className="plain">
            {rep.inferred.map((x, i) => (
              <li key={`${x.report}-${i}`}>
                Report {x.report}, {x.company}: {x.label}
              </li>
            ))}
          </ul>
        </details>
      )}
    </article>
  );
}

// parseNumbers reads a list of report numbers; it answers undefined when any
// part is not a positive whole number.
function parseNumbers(text: string): number[] | undefined {
  const parts = text.split(/[\s,]+/).filter(Boolean);
  const out = parts.map((p) => (/^\d+$/.test(p) ? Number(p) : 0));
  return parts.length > 0 && out.every((n) => n > 0 && Number.isSafeInteger(n)) ? out : undefined;
}

// SkillGaps is the optional skill-gap analysis of Reports. It reads the saved
// requirement evidence of the reports named and keeps its own results apart
// from the scored posting reports.
export function SkillGaps({ shown }: { shown: number[] }) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [active, setActive] = useState("");
  const start = useAction();
  const numbers = parseNumbers(text);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!numbers) return;
    const accepted = await start.run(() => request("POST", "/api/evaluations/gaps", runAccepted, { body: { reports: numbers }, headers: { "Idempotency-Key": newRequestId() } }));
    if (accepted) setActive(accepted.runId);
  };
  return (
    <section aria-labelledby="skill-gaps" className="panel">
      <header className="panel-head">
        <h2 id="skill-gaps">Skill gaps</h2>
        <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? "Hide" : "Show"}
        </button>
      </header>
      {open && (
        <>
          <form onSubmit={submit} className="form-grid" aria-label="Analyze skill gaps">
            <label>
              Report numbers, 1 to 100
              <input value={text} onChange={(e) => setText(e.target.value)} placeholder="12, 14, 15" inputMode="numeric" aria-invalid={text !== "" && !numbers} />
            </label>
            <div className="actions">
              <button type="button" onClick={() => setText(shown.join(", "))} disabled={shown.length === 0}>
                Use the reports listed above
              </button>
              <button type="submit" disabled={start.busy || !numbers || numbers.length > 100}>
                Analyze gaps
              </button>
            </div>
            {start.error && <ErrorNote error={start.error} />}
          </form>
          <Earlier active={active} onOpen={setActive} />
          {active && <Analysis id={active} />}
        </>
      )}
    </section>
  );
}

function Earlier({ active, onOpen }: { active: string; onOpen: (id: string) => void }) {
  const [res, retry] = useResource(() => get("/api/evaluations/gaps", gapList), [], ["reports"]);
  if (res.status === "error") return <ErrorNote error={res.error} onRetry={retry} />;
  const items = res.data?.analyses ?? [];
  if (items.length === 0) return null;
  return (
    <ul className="plain stack" aria-label="Earlier analyses">
      {items.map((x) => (
        <li key={x.id}>
          <button type="button" aria-pressed={x.id === active} onClick={() => onOpen(x.id)}>
            {formatDate(x.generatedAt)}
          </button>{" "}
          <span className="muted">
            {x.sample} {x.sample === 1 ? "posting" : "postings"}, {x.gaps} {x.gaps === 1 ? "gap" : "gaps"}
          </span>
        </li>
      ))}
    </ul>
  );
}
