import { useId, useState } from "react";
import { post } from "../../api/client";
import { cvDraft } from "../../api/decoders_pages";
import { asApiError } from "../../hooks";
import { useUnsaved } from "../../ui/SaveBar";
import type { SetupDraft, SetupStep } from "../../wire";
import { FieldErrors } from "./Form";
import { ApplyOutcome, useApply } from "./apply";

type Kind = "job" | "study" | "skill";

interface Entry {
  key: string;
  kind: Kind;
  label: string;
  /** What the CV gave, by field. */
  row: Record<string, string>;
}

// The fields of each kind, in the profile's names; the first is required.
const FIELDS: Record<Kind, readonly (readonly [string, string])[]> = {
  job: [["employer", "Employer"], ["title", "Job title"], ["start", "Start"], ["end", "End"]],
  study: [["school", "School"], ["degree", "Degree"], ["field", "Field of study"], ["start", "Start"], ["end", "End"]],
  skill: [["name", "Skill"]],
};
const KIND_WORD: Record<Kind, string> = { job: "Job", study: "Study", skill: "Skill" };
// The confirmation's lists, which are also the profile's (history.employment...).
const LISTS = [["employment", "job"], ["education", "study"], ["skills", "skill"]] as const;

const span = (a?: string, b?: string) => [a, b].filter(Boolean).join(" to ");

// draftKey names an entry as the CV gave it, the way a discard is recorded
// (POST /api/config/cv/draft/dismiss), so Profile's draft and this review
// leave out the same entries.
export const draftKey = (kind: Kind, r: { [k in "employer" | "title" | "start" | "end" | "school" | "degree" | "name"]?: string }) =>
  kind === "job" ? `job|${r.employer}|${r.title ?? ""}|${r.start ?? ""}` : kind === "study" ? `study|${r.school}|${r.degree ?? ""}|${r.end ?? ""}` : `skill|${(r.name ?? "").toLowerCase()}`;

function entriesOf(draft: SetupDraft): Entry[] {
  const row = (r: Record<string, string | undefined>) => Object.fromEntries(Object.entries(r).map(([k, v]) => [k, v ?? ""]));
  return [
    ...draft.employment.map((j, i): Entry => ({ key: `job:${i}`, kind: "job", label: [j.title, j.employer, span(j.start, j.end)].filter(Boolean).join(", "), row: row({ ...j }) })),
    ...draft.education.map((s, i): Entry => ({ key: `study:${i}`, kind: "study", label: [s.degree, s.school, span(s.start, s.end)].filter(Boolean).join(", "), row: row({ ...s }) })),
    ...draft.skills.map((name, i): Entry => ({ key: `skill:${i}`, kind: "skill", label: name, row: { name } })),
  ];
}

// problem says why a field cannot be stored as it is: the profile holds a date
// as YYYY-MM (an end may be present), so a year the CV gave alone needs its
// month typed, and an entry needs its name.
export function problem(field: string, v: string, required: boolean): string {
  if (required && v === "") return "Needed to keep this entry.";
  if ((field !== "start" && field !== "end") || v === "" || /^\d{4}-(0[1-9]|1[0-2])$/.test(v) || (field === "end" && v === "present")) return "";
  return /^\d{4}$/.test(v) ? `Add the month: ${v}-MM.` : `Use YYYY-MM${field === "end" ? " or present" : ""}.`;
}

// EntryInput is one field of a CV entry under review, with why its text cannot
// be stored. Setup's review and Profile's draft both use it, so a date is typed
// the same way in both.
export function EntryInput({ id, field, label, of, value, required, onChange, ariaLabel }: { id: string; field: string; label?: string; of: string; value: string; required: boolean; onChange: (v: string) => void; ariaLabel?: string }) {
  const why = problem(field, value.trim(), required);
  return (
    <div>
      {label && <label htmlFor={id}>{label}<span className="sr-only"> of {of}</span></label>}
      <input id={id} type="text" aria-label={ariaLabel} placeholder={field === "end" ? "YYYY-MM or present" : field === "start" ? "YYYY-MM" : undefined} value={value} onChange={(ev) => onChange(ev.target.value)} aria-invalid={!!why || undefined} aria-describedby={why ? `${id}-why` : undefined} />
      {why && <span id={`${id}-why`} className="muted">{why}</span>}
    </div>
  );
}

// DraftReview lists what the CV parser found. Each entry is kept or removed,
// and a kept one can be corrected field by field before it is confirmed.
// Nothing is stored until the confirmation is sent, and only the kept entries
// are in it. A refusal is shown beside the entry it names.
export function DraftReview({ step, draft, reload, onConfirmed }: { step: SetupStep; draft: SetupDraft; reload: () => void; onConfirmed?: () => void }) {
  const id = useId();
  const entries = entriesOf(draft);
  const [removed, setRemoved] = useState<Set<string>>(new Set());
  const [edits, setEdits] = useState<Record<string, Record<string, string>>>({});
  const apply = useApply(step.id, reload);
  const [sent, setSent] = useState(false);
  const [unrecorded, setUnrecorded] = useState("");
  const ask = useUnsaved(!sent && (removed.size > 0 || Object.keys(edits).length > 0));
  const kept = entries.filter((e) => !removed.has(e.key));
  const value = (e: Entry, f: string) => (edits[e.key]?.[f] ?? e.row[f] ?? "").trim();
  const edit = (e: Entry, f: string, v: string) => {
    apply.clear();
    setEdits((m) => ({ ...m, [e.key]: { ...m[e.key], [f]: v } }));
  };
  const blocked = kept.some((e) => FIELDS[e.kind].some(([f], i) => problem(f, value(e, f), i === 0)));

  // The server names a refused entry by its place in the list sent, which
  // holds the kept entries of its kind in order.
  const refused: Record<string, string[]> = {};
  const elsewhere: string[] = [];
  for (const [path, message] of Object.entries(apply.failure?.details?.fields ?? {})) {
    const m = /^(?:history\.)?(employment|education|skills)\[(\d+)\]\.?(\w*)/.exec(path);
    const kind = LISTS.find(([list]) => list === m?.[1])?.[1];
    const e = kind && kept.filter((k) => k.kind === kind)[Number(m![2])];
    if (e) (refused[e.key] ??= []).push(m![3] ? `${FIELDS[e.kind].find(([f]) => f === m![3])?.[1] ?? m![3]}: ${message}` : message);
    else elsewhere.push(`${path}: ${message}`);
  }

  const confirm = async () => {
    const input: Record<string, unknown> = {};
    for (const [list, kind] of LISTS) {
      const rows = kept.filter((e) => e.kind === kind).map((e) => Object.fromEntries(FIELDS[kind].map(([f]) => [f, value(e, f)]).filter(([, v]) => v !== "")));
      if (rows.length > 0) input[list] = rows;
    }
    if (kept.length === 0) input.review_complete = true;
    if (await apply.send(input)) {
      // A removed entry is discarded for good, so Profile's draft does not offer it again.
      const gone = entries.filter((e) => removed.has(e.key)).map((e) => draftKey(e.kind, e.row));
      if (gone.length > 0) await post("/api/config/cv/draft/dismiss", cvDraft, { keys: gone }).catch((e) => setUnrecorded(asApiError(e).message));
      setSent(true);
      onConfirmed?.();
    }
  };

  if (sent)
    return (
      <>
        <ApplyOutcome apply={apply} />
        {unrecorded && <p role="alert" className="note error">The entries you removed were not recorded ({unrecorded}), so Profile may offer them again from your CV.</p>}
      </>
    );
  return (
    <section aria-labelledby="cv-draft" className="panel">
      <h3 id="cv-draft">Review what was found in your CV</h3>
      <p>Nothing is stored until you confirm. Correct what was read wrong, remove what is not yours, and keep the rest.</p>
      {entries.length === 0 ? <p className="muted">No jobs, studies or skills were found.</p> : (
        <ul className="plain stack">
          {entries.map((e) => {
            const on = !removed.has(e.key);
            const skill = e.kind === "skill";
            // A skill is its name alone, edited on the line of its checkbox.
            const inputs = FIELDS[e.kind].map(([f, label], i) => (
              <EntryInput key={f} id={`${id}-${e.key}-${f}`} field={f} label={skill ? undefined : label} of={e.label} ariaLabel={skill ? `Skill name, read as ${e.label}` : undefined} value={edits[e.key]?.[f] ?? e.row[f] ?? ""} required={i === 0} onChange={(v) => edit(e, f, v)} />
            ));
            return (
              <li key={e.key} data-entry={e.key} className={skill ? "draft-skill" : undefined}>
                <label className="check">
                  <input type="checkbox" checked={on} onChange={() => setRemoved((s) => { const n = new Set(s); if (on) n.add(e.key); else n.delete(e.key); return n; })} />
                  <span>{KIND_WORD[e.kind]}{skill ? <span className="sr-only">: {e.label}</span> : `: ${e.label}`}</span>
                </label>
                {on && (skill ? inputs : <div className="draft-fields">{inputs}</div>)}
                {on && <FieldErrors id={`${id}-${e.key}-err`} errors={refused[e.key]} />}
              </li>
            );
          })}
        </ul>
      )}
      {blocked && <p className="note compact" role="status">Each kept entry needs its name, and its dates as YYYY-MM, before it can be confirmed.</p>}
      <FieldErrors id={`${id}-err`} errors={elsewhere} />
      <div className="actions">
        <button type="button" disabled={apply.busy || blocked} onClick={() => void confirm()}>
          {kept.length === 0 ? "Confirm that nothing is kept" : `Confirm ${kept.length} ${kept.length === 1 ? "entry" : "entries"}`}
        </button>
      </div>
      <ApplyOutcome apply={apply} />
      {ask}
    </section>
  );
}
