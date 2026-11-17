import { useEffect, useState } from "react";
import { get, newRequestId, request } from "../../api/client";
import { documentSource } from "../../api/decoders_documents";
import { runAccepted } from "../../api/decoders_runs";
import { useResource } from "../../hooks";
import { CopyButton } from "../../ui/CopyButton";
import { ErrorNote, Loading } from "../../ui/parts";
import { SaveBar } from "../../ui/SaveBar";
import { leave } from "../../router";
import { useAction } from "../action";
import type { DocumentBullet, DocumentEntryContent, DocumentLetterContent, DocumentResumeContent, DocumentSource } from "../../wire";
import type { DocumentRunState } from "./run";

type Draft = { resume?: DocumentResumeContent; letter?: DocumentLetterContent };

// sendable leaves out the bullets cleared to nothing: an empty bullet is no
// sentence, and the PDF would print it as an empty item. The fields stay in
// the form while they are empty, so a bullet being retyped is not removed.
const filled = (bullets: DocumentBullet[]) => bullets.filter((b) => b.text.trim() !== "");
const filledEntries = (entries: DocumentEntryContent[]) => entries.map((e) => ({ ...e, bullets: filled(e.bullets) }));
function sendable(d: Draft): Draft {
  return {
    resume: d.resume && { ...d.resume, experience: filledEntries(d.resume.experience), projects: filledEntries(d.resume.projects) },
    letter: d.letter && { ...d.letter, bullets: filled(d.letter.bullets) },
  };
}

function Field({ label, value, onChange, rows = 3 }: { label: string; value: string; onChange: (v: string) => void; rows?: number }) {
  return (
    <label>
      {label}
      <textarea className="editor" rows={rows} value={value} onChange={(e) => onChange(e.target.value)} />
    </label>
  );
}

function Bullets({ name, bullets, onChange }: { name: string; bullets: DocumentBullet[]; onChange: (b: DocumentBullet[]) => void }) {
  return (
    <>
      {bullets.map((b, i) => (
        <Field key={i} label={`${name}, bullet ${i + 1}`} value={b.text} onChange={(text) => onChange(bullets.map((x, j) => (j === i ? { ...x, text } : x)))} />
      ))}
    </>
  );
}

function Entries({ name, entries, onChange }: { name: string; entries: DocumentEntryContent[]; onChange: (e: DocumentEntryContent[]) => void }) {
  return (
    <>
      {entries.map((e, i) => (
        <Bullets key={e.entry} name={`${name} ${i + 1}`} bullets={e.bullets} onChange={(bullets) => onChange(entries.map((x, j) => (j === i ? { ...x, bullets } : x)))} />
      ))}
    </>
  );
}

function ResumeForm({ value, onChange }: { value: DocumentResumeContent; onChange: (v: DocumentResumeContent) => void }) {
  return (
    <>
      <Field label="Profile" value={value.profile} onChange={(profile) => onChange({ ...value, profile })} />
      {value.competencies.map((c, i) => (
        <Field key={i} label={`Competency: ${c.label}`} rows={2} value={c.text} onChange={(text) => onChange({ ...value, competencies: value.competencies.map((x, j) => (j === i ? { ...x, text } : x)) })} />
      ))}
      <Entries name="Experience" entries={value.experience} onChange={(experience) => onChange({ ...value, experience })} />
      <Entries name="Project" entries={value.projects} onChange={(projects) => onChange({ ...value, projects })} />
    </>
  );
}

function LetterForm({ value, onChange }: { value: DocumentLetterContent; onChange: (v: DocumentLetterContent) => void }) {
  return (
    <>
      <Field label="Opening" value={value.opening} onChange={(opening) => onChange({ ...value, opening })} />
      <Field label="Body" value={value.body} rows={2} onChange={(body) => onChange({ ...value, body })} />
      <Bullets name="Letter" bullets={value.bullets} onChange={(bullets) => onChange({ ...value, bullets })} />
      <Field label="Closing" value={value.closing} onChange={(closing) => onChange({ ...value, closing })} />
    </>
  );
}

// Editor keeps what was typed until the run that stores it has succeeded, so a
// run that ends without a new version leaves the text here to fix and send
// again. A refusal at once (the grounding checks) stays beside the button.
// Unsent text asks before Cancel, the drawer's close or leaving the page drops it.
function Editor({ src, run, onDone }: { src: DocumentSource; run: DocumentRunState; onDone: () => void }) {
  const [draft, setDraft] = useState<Draft>({ resume: src.resume, letter: src.letter });
  const [sent, setSent] = useState(false);
  const [lost, setLost] = useState(false);
  const save = useAction();
  const changed = JSON.stringify(draft) !== JSON.stringify({ resume: src.resume, letter: src.letter });

  const submit = async () => {
    const accepted = await save.run(() =>
      request("POST", `/api/documents/${src.id}/edit`, runAccepted, { body: sendable(draft), headers: { "Idempotency-Key": newRequestId() } }),
    );
    if (accepted) {
      run.begin(accepted.runId, "Your edit");
      setSent(true);
      setLost(false);
    }
  };
  useEffect(() => {
    if (!sent || run.busy) return;
    if (run.failure) {
      setSent(false);
      setLost(true);
    } else onDone();
  }, [sent, run.busy]);

  return (
    <form
      className="form-grid"
      aria-label={`Edit version ${src.version}`}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <p className="muted">
        Saving makes a new draft version, whose PDF you make next (the other document keeps its own); version {src.version} stays as it is. Each sentence keeps the evidence it was written from, and text with a figure or name your sources do not support is refused.
        Clear a bullet to remove it.
      </p>
      {draft.resume && <ResumeForm value={draft.resume} onChange={(resume) => setDraft({ resume })} />}
      {draft.letter && <LetterForm value={draft.letter} onChange={(letter) => setDraft({ letter })} />}
      <SaveBar dirty={changed && !sent} savedAt="">
        <button type="button" disabled={sent} onClick={() => leave(onDone)}>
          Cancel
        </button>
        <button type="submit" disabled={!changed || save.busy || run.busy}>
          Save as new draft
        </button>
      </SaveBar>
      <div aria-live="polite">
        {save.error && <ErrorNote error={save.error} />}
        {lost && <p className="note error">The edit was not saved. Your text is still here.</p>}
      </div>
    </form>
  );
}

// sentences lists each sentence of the content with the evidence ids it cites.
function sentences(src: DocumentSource): DocumentBullet[] {
  const r = src.resume;
  const l = src.letter;
  if (r) return [{ text: r.profile, evidence: r.profileEvidence }, ...r.competencies.map((c) => ({ text: `${c.label}: ${c.text}`, evidence: c.evidence })), ...[...r.experience, ...r.projects].flatMap((e) => e.bullets)];
  return l ? [{ text: l.opening, evidence: l.openingEvidence }, ...l.bullets] : [];
}

// Grounding shows the line of the user's own material behind each sentence.
function Grounding({ src }: { src: DocumentSource }) {
  const lines = new Map(src.evidence.map((e) => [e.id, e]));
  const rows = sentences(src).filter((s) => s.text && s.evidence.some((id) => lines.has(id)));
  if (rows.length === 0) return null;
  return (
    <details>
      <summary>What each sentence rests on</summary>
      <ul className="plain stack">
        {rows.map((s, i) => (
          <li key={i}>
            {s.text}
            <ul>
              {s.evidence.flatMap((id) => lines.get(id) ?? []).map((e) => (
                <li key={e.id} className="muted">{e.text} ({e.source})</li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </details>
  );
}

// VersionText shows the stored text of one version and, unless the version is
// attached to a submitted application, lets the user edit it.
export function VersionText({ id, run, current }: { id: number; run: DocumentRunState; current: boolean }) {
  const [res, reload] = useResource(() => get(`/api/documents/source/${id}`, documentSource), [id]);
  const [editing, setEditing] = useState(false);
  // A failed reload keeps the loaded text, and an open editor with what was typed.
  const failed = res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />;
  if (!res.data) return failed || <Loading label="Loading the text" />;
  const src = res.data;
  if (editing) return <>{failed}<Editor src={src} run={run} onDone={() => setEditing(false)} /></>;
  return (
    <>
      {failed}
      <pre className="pre" aria-label={`Text of version ${src.version}`}>{src.text}</pre>
      <Grounding src={src} />
      <div className="actions">
        <CopyButton text={src.text} label={`Copy the text of version ${src.version}`} />
        <button type="button" disabled={src.frozen || !current || run.busy} onClick={() => setEditing(true)}>
          Edit
        </button>
        {src.frozen && <span className="muted">Attached to a submitted application, so it cannot be edited. Regenerate to start a new draft.</span>}
      </div>
    </>
  );
}
