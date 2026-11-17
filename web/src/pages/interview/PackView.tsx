import type { ReactNode } from "react";
import { Chip, formatDateTime } from "../../ui/parts";
import type { InterviewPack, InterviewStage, InterviewStep } from "../../wire";

const STATE_TONE: Record<string, "good" | "warn" | "neutral"> = { frozen: "good", "user-confirmed": "good", current: "neutral", unverified: "warn" };
const STATE_LABEL: Record<string, string> = { frozen: "Frozen at submission", current: "Current version", "user-confirmed": "Confirmed by you", unverified: "Unverified" };
const KIND_LABEL: Record<string, string> = { dossier: "Application", report: "Report", "company-brief": "Company brief", story: "Story", feedback: "Feedback" };

function Lines({ title, lines }: { title: string; lines: string[] }) {
  if (lines.length === 0) return null;
  return (
    <>
      <h4>{title}</h4>
      <ul>
        {lines.map((l) => (
          <li key={l}>{l}</li>
        ))}
      </ul>
    </>
  );
}

// Steps are gaps or next actions, each with the way to its fix (fix), unless
// a gap above already offers that fix (shown).
const fixKey = (s: InterviewStep) => `${s.kind ?? ""}:${s.target ?? ""}`;
function Steps({ title, steps, fix, shown }: { title: string; steps: InterviewStep[]; fix: (s: InterviewStep) => ReactNode; shown?: Set<string> }) {
  if (steps.length === 0) return null;
  return (
    <>
      <h4>{title}</h4>
      <ul>
        {steps.map((s) => (
          <li key={s.text}>
            {s.text} {!shown?.has(fixKey(s)) && fix(s)}
          </li>
        ))}
      </ul>
    </>
  );
}

// asText is the pack as Markdown, to keep in notes for the interview: the
// stage, then the pack with the stories each question can draw on.
export function asText(p: InterviewPack, st: InterviewStage, titles: Map<string, string>): string {
  const part = (title: string, lines: string[]) => (lines.length > 0 ? [`## ${title}`, ...lines.map((l) => `- ${l}`), ""] : []);
  const stories = (ids: string[]) => (ids.length > 0 ? ` (your stories: ${ids.map((id) => titles.get(id) ?? id).join(", ")})` : "");
  return [
    `# Stage ${st.id}, ${st.type.replaceAll("_", " ")}`,
    [st.at && `When: ${formatDateTime(st.at)}`, st.format !== "unknown" && `Format: ${st.format}`, st.interviewers.length > 0 && `With: ${st.interviewers.join(", ")}`].filter(Boolean).join(". "),
    "",
    ...(p.roleBrief ? ["## The role", p.roleBrief, ""] : []),
    ...(p.companyBrief ? ["## The company", p.companyBrief, ""] : []),
    ...part("Company hooks", p.hooks.map((h) => h.text)),
    ...part("Stay consistent with what you submitted", p.claims),
    ...part("Likely questions", p.questions.map((q) => `${q.category}: ${q.prompt}${stories(q.storyIds)}`)),
    ...part("Earlier feedback", p.priorFeedback),
    ...part("Gaps", p.gaps.map((s) => s.text)),
    ...part("Next actions", p.nextActions.map((s) => s.text)),
  ].join("\n");
}

// PackView shows one immutable revision of a prep pack with the sources each
// part rests on. A part without a source does not appear in a pack. fix is
// the link or button to the place that settles a gap or next action.
export function PackView({ pack, storyTitles, onPractice, fix }: { pack: InterviewPack; storyTitles: Map<string, string>; onPractice: (prompt: string) => void; fix: (s: InterviewStep) => ReactNode }) {
  const label = new Map(pack.sources.map((s) => [s.ref, s.label]));
  return (
    <div aria-label={`Prep pack revision ${pack.revision}`}>
      {pack.warnings.map((w) => (
        <p key={w} className="note" role="note">
          {w}
        </p>
      ))}
      {pack.roleBrief && (
        <>
          <h4>The role</h4>
          <p>{pack.roleBrief}</p>
        </>
      )}
      {pack.companyBrief && (
        <>
          <h4>The company</h4>
          <p>{pack.companyBrief}</p>
        </>
      )}
      {pack.hooks.length > 0 && (
        <>
          <h4>Company hooks</h4>
          <ul>
            {pack.hooks.map((h) => (
              <li key={h.text}>
                {h.text} <span className="muted">(source: {h.sources.map((s) => label.get(s) ?? s).join(", ")})</span>
              </li>
            ))}
          </ul>
        </>
      )}
      <Lines title="Stay consistent with what you submitted" lines={pack.claims} />
      <h4>Likely questions</h4>
      <ol>
        {pack.questions.map((q) => (
          <li key={q.category + q.prompt}>
            <strong>{q.category}</strong>: {q.prompt}
            {q.storyIds.length > 0 && <span className="muted"> Your stories: {q.storyIds.map((id) => storyTitles.get(id) ?? id).join(", ")}.</span>}{" "}
            <button type="button" className="small" onClick={() => onPractice(q.prompt)} aria-label={`Practice: ${q.prompt}`}>
              Practice
            </button>
          </li>
        ))}
      </ol>
      <Lines title="Earlier feedback" lines={pack.priorFeedback} />
      <Steps title="Gaps" steps={pack.gaps} fix={fix} />
      <Steps title="Next actions" steps={pack.nextActions} fix={fix} shown={new Set(pack.gaps.map(fixKey))} />
      {pack.sources.length > 0 && (
        <>
          <h4>Sources</h4>
          <table className="cards">
            <caption className="sr-only">Sources of this pack</caption>
            <thead>
              <tr>
                <th scope="col">Source</th>
                <th scope="col">Kind</th>
                <th scope="col">State</th>
              </tr>
            </thead>
            <tbody>
              {pack.sources.map((s) => (
                <tr key={s.ref}>
                  <td data-label="Source">{s.label}</td>
                  <td data-label="Kind">{KIND_LABEL[s.kind] ?? s.kind}</td>
                  <td data-label="State">
                    <Chip tone={STATE_TONE[s.state] ?? "neutral"}>{STATE_LABEL[s.state] ?? s.state}</Chip>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}
