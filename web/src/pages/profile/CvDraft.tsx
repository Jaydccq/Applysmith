import { useId, useState } from "react";
import { get, post } from "../../api/client";
import { cvDraft } from "../../api/decoders_pages";
import { asApiError, useResource } from "../../hooks";
import { Chip, ErrorNote, UndoNote } from "../../ui/parts";
import type { CVDraft as CVDraftWire } from "../../wire";
import { EntryInput, draftKey, problem } from "../setup/CvDraft";
import { defaultSkillYears, rowsOf, saveProfile, setOp, text, valueAt } from "./shared";

type Values = { [key: string]: unknown };
type Entry = { key: string; label: string; path: string; row: { [key: string]: string } };

// A date the CV gave as a bare year: the profile holds only YYYY-MM, so the
// month is the user's to type (as in setup's review) and is never defaulted.
const yearOnly = (v: string | undefined) => /^\d{4}$/.test(v ?? "");
const sameDate = (saved: string, drafted: string) => saved === drafted || (yearOnly(drafted) && saved.startsWith(`${drafted}-`));
const missingMonths = (e: Entry) => ["start", "end"].filter((f) => yearOnly(e.row[f]));

const same = (a: string, b: string) => a.trim().toLowerCase() === b.trim().toLowerCase();
const without = (row: { [key: string]: string }) => Object.fromEntries(Object.entries(row).filter(([, v]) => v !== ""));

// entriesOf lists the draft entries the profile does not hold yet. An entry that
// is already saved is not offered again.
function entriesOf(draft: CVDraftWire, values: Values): Entry[] {
  const jobs = rowsOf(valueAt(values, "history.employment"));
  const study = rowsOf(valueAt(values, "history.education"));
  const skills = rowsOf(valueAt(values, "skills"));
  const out: Entry[] = [];
  for (const j of draft.employment) {
    if (jobs.some((x) => same(text(x.employer), j.employer) && same(text(x.title), j.title ?? "") && sameDate(text(x.start), j.start ?? ""))) continue;
    const when = [j.start, j.end].filter(Boolean).join(" to ");
    out.push({ key: draftKey("job", j), label: [j.title, j.employer, when].filter(Boolean).join(", "), path: "history.employment", row: without({ employer: j.employer, title: j.title ?? "", start: j.start ?? "", end: j.end ?? "" }) });
  }
  for (const s of draft.education) {
    if (study.some((x) => same(text(x.school), s.school) && same(text(x.degree), s.degree ?? "") && sameDate(text(x.end), s.end ?? ""))) continue;
    const when = [s.start, s.end].filter(Boolean).join(" to ");
    out.push({ key: draftKey("study", s), label: [s.degree, s.school, when].filter(Boolean).join(", "), path: "history.education", row: without({ school: s.school, degree: s.degree ?? "", start: s.start ?? "", end: s.end ?? "" }) });
  }
  for (const name of draft.skills) {
    if (skills.some((x) => same(text(x.name), name))) continue;
    out.push({ key: draftKey("skill", { name }), label: name, path: "skills", row: { name } });
  }
  return out;
}

// CvDraft shows what the CV parser found in cv.md, entry by entry. Nothing is
// saved until the user confirms an entry, and a confirmed entry is the only
// thing that is written to the profile. A discarded entry is recorded on the
// server, so it stays gone, until its Undo puts it back.
export function CvDraft({ values, revision, reload, locked }: { values: Values; revision: string; reload: () => void; locked: boolean }) {
  const id = useId();
  const [res] = useResource(() => get("/api/config/cv/draft", cvDraft), []);
  const [discarded, setDiscarded] = useState<string[] | null>(null);
  const [dates, setDates] = useState<{ [key: string]: string }>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [undo, setUndo] = useState<{ e: Entry; n: number }>();
  if (res.status === "error") return <ErrorNote error={res.error} />;
  if (!res.data) return null;
  const gone = discarded ?? res.data.dismissed;
  const entries = entriesOf(res.data, values).filter((e) => !gone.includes(e.key));
  const dismiss = async (e: Entry, restore: boolean) => setDiscarded((await post("/api/config/cv/draft/dismiss", cvDraft, { keys: [e.key], restore })).dismissed);
  // The last discard can be taken back; the note stays when it emptied the draft.
  const note = undo && (
    <div role="status">
      <UndoNote key={undo.n} done={`Discarded ${undo.e.label}.`} undone={`${undo.e.label} is back in the draft.`} onUndo={() => dismiss(undo.e, true).then(() => true, (err) => (setError(err), false))} />
      {entries.length === 0 && error != null && <ErrorNote error={error} />}
    </div>
  );
  if (entries.length === 0) return note || null;

  const dateKey = (e: Entry, field: string) => `${e.key}|${field}`;
  const date = (e: Entry, f: string) => dates[dateKey(e, f)] ?? e.row[f];
  const needsMonth = (e: Entry) => missingMonths(e).some((f) => problem(f, date(e, f).trim(), false) !== "");
  const act = async (work: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
    } catch (err) {
      setError(asApiError(err));
      // A save refused for its revision is sent again against the one on file.
      reload();
    } finally {
      setBusy(false);
    }
  };
  const confirm = (e: Entry) =>
    act(async () => {
      const row = { ...e.row };
      for (const f of missingMonths(e)) row[f] = date(e, f).trim();
      await saveProfile(revision, [setOp(e.path, [...rowsOf(valueAt(values, e.path)), row])]);
      reload();
    });
  const discard = (e: Entry) =>
    act(async () => {
      await dismiss(e, false);
      setUndo((u) => ({ e, n: (u?.n ?? 0) + 1 }));
    });
  // A CV can list a hundred skills: those left after the user's discards are
  // confirmed with one press, in one change after the skills already saved.
  const skillsLeft = entries.filter((e) => e.path === "skills");
  const confirmSkills = () =>
    act(async () => {
      await saveProfile(revision, [setOp("skills", [...rowsOf(valueAt(values, "skills")), ...skillsLeft.map((e) => e.row)])]);
      reload();
    });

  return (
    <section aria-label="Draft from your CV" className="cv-draft">
      <h4>Draft from your CV</h4>
      <p className="muted">
        These entries were read from your CV. None is saved until you confirm it; a field the CV does not state stays empty, and a date the CV gives only as a year needs its month. A confirmed skill counts as {defaultSkillYears} years when a form asks, until you set its Years.
        {locked && " Save or discard your edits below before confirming an entry."}
      </p>
      {skillsLeft.length > 1 && (
        <div className="actions">
          <button type="button" className="small" disabled={busy || locked} onClick={() => void confirmSkills()}>
            Confirm all {skillsLeft.length} skills
          </button>
        </div>
      )}
      <ul className="plain draft-list">
        {entries.map((e) => (
          <li key={e.key} className="row draft-row">
            <div>
              <Chip>{e.path === "skills" ? "Skill" : e.path === "history.education" ? "Education" : "Job"}</Chip>{" "}
              {e.label}
              {missingMonths(e).length > 0 && (
                <div className="draft-fields">
                  {missingMonths(e).map((f) => (
                    <EntryInput key={f} id={`${id}-${e.key}-${f}`} field={f} label={f === "start" ? "Start" : "End"} of={e.label} value={date(e, f)} required={false} onChange={(v) => setDates((m) => ({ ...m, [dateKey(e, f)]: v }))} />
                  ))}
                </div>
              )}
            </div>
            <span className="actions">
              <button type="button" className="small" disabled={busy || locked || needsMonth(e)} onClick={() => void confirm(e)} aria-label={`Confirm ${e.label}`}>
                Confirm
              </button>
              <button type="button" className="small ghost" disabled={busy} onClick={() => void discard(e)} aria-label={`Discard ${e.label}`}>
                Discard
              </button>
            </span>
          </li>
        ))}
      </ul>
      {note}
      {error != null && <ErrorNote error={error} />}
    </section>
  );
}
