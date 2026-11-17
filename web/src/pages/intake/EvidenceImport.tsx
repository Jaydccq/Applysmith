import { useId, useRef, useState } from "react";
import { ApiError, post } from "../../api/client";
import { intakePreview, intakeResult } from "../../api/decoders_setup";
import { asApiError } from "../../hooks";
import { ErrorNote } from "../../ui/parts";
import type { IntakeDocument, IntakePreview } from "../../wire";
import { ChangeSetReview } from "./ChangeSet";
import { KINDS, MAX_BYTES, MAX_DOCUMENTS, selectionsOf, type Pick } from "./model";

// EvidenceImport is the optional way to bring in several documents at once.
// It sits beside the single-CV and manual forms, which stay as they are. A
// preview changes nothing; applying sends only what was ticked, against the
// profile revision the preview was built from.
export function EvidenceImport({ onApplied }: { onApplied?: () => void }) {
  const [open, setOpen] = useState(false);
  const [docs, setDocs] = useState<IntakeDocument[]>([]);
  const [preview, setPreview] = useState<IntakePreview | null>(null);
  const [picks, setPicks] = useState<Record<string, Pick>>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const picker = useRef<HTMLInputElement>(null);
  const panel = useId();

  const bytes = docs.reduce((n, d) => n + new TextEncoder().encode(d.text).length, 0);
  const sizeProblem = docs.length > MAX_DOCUMENTS ? `At most ${MAX_DOCUMENTS} documents.` : bytes > MAX_BYTES ? "The documents are over the 4 MiB limit together." : "";

  const reset = () => {
    setPreview(null);
    setPicks({});
    setFailure(null);
    setDone(null);
  };
  const add = async (files: FileList | null) => {
    if (!files) return;
    const read = await Promise.all(Array.from(files).map(async (f): Promise<IntakeDocument> => ({ name: f.name, kind: "cv", text: await f.text() })));
    setDocs((d) => [...d, ...read]);
    reset();
  };
  const update = (i: number, kind: string) => {
    setDocs((d) => d.map((x, j) => (j === i ? { ...x, kind } : x)));
    reset();
  };
  const remove = (i: number) => {
    setDocs((d) => d.filter((_, j) => j !== i));
    reset();
  };

  const run = async (go: () => Promise<void>) => {
    setBusy(true);
    setFailure(null);
    try {
      await go();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  };
  const previewIt = () =>
    run(async () => {
      setDone(null);
      const p = await post("/api/setup/intake/preview", intakePreview, { documents: docs });
      setPreview(p);
      setPicks({});
    });
  const selections = preview ? selectionsOf(preview.items, picks) : [];
  const apply = () =>
    run(async () => {
      if (!preview) return;
      const res = await post("/api/setup/intake/accept", intakeResult, { revision: preview.revision, documents: docs, selections });
      setPreview(null);
      setPicks({});
      setDone(res.wrote ? `Saved ${res.applied.length} ${res.applied.length === 1 ? "item" : "items"} to your profile.` : "Nothing changed.");
      onApplied?.();
    });

  return (
    <section className="panel" aria-labelledby={panel}>
      <h3 id={panel}>Add more documents (optional)</h3>
      <p className="muted">Single CV and manual entry are all you need. Select other career documents to compare them with your profile; nothing is stored until you apply the items you pick.</p>
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? "Hide document import" : "Import several documents"}
      </button>
      {open && (
        <div>
          <div className="actions">
            <button type="button" onClick={() => picker.current?.click()}>
              Choose documents
            </button>
            <input ref={picker} type="file" multiple accept=".md,.markdown,.txt,text/markdown,text/plain" className="sr-only" tabIndex={-1} aria-label="Career documents" onChange={(e) => void add(e.target.files).then(() => { if (picker.current) picker.current.value = ""; })} />
            <span className="muted">Markdown or text files, at most {MAX_DOCUMENTS}. Paste the text of a PDF or DOCX file into a .txt file first.</span>
          </div>
          {docs.length > 0 && (
            <ul className="plain stack">
              {docs.map((d, i) => (
                <li key={i} data-doc={d.name}>
                  <span>{d.name}</span>{" "}
                  <select aria-label={`What ${d.name} is`} value={d.kind} onChange={(e) => update(i, e.target.value)}>
                    {KINDS.map((k) => (
                      <option key={k.id} value={k.id}>
                        {k.label}
                      </option>
                    ))}
                  </select>{" "}
                  <button type="button" onClick={() => remove(i)}>
                    Remove {d.name}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {sizeProblem && <p role="alert" className="note error">{sizeProblem}</p>}
          <div className="actions">
            <button type="button" disabled={busy || docs.length === 0 || sizeProblem !== ""} onClick={() => void previewIt()}>
              Preview changes
            </button>
          </div>
        </div>
      )}
      {failure && <ErrorNote error={failure} />}
      {failure?.kind === "revision_conflict" && <p>Preview the documents again to review the change against your current profile.</p>}
      {preview && (
        <div aria-label="Change preview" role="region">
          <h4>Documents read</h4>
          <ul className="plain">
            {preview.documents.map((d) => (
              <li key={d.hash + d.name}>
                {d.label} ({d.kind}) <code>{d.hash.slice(0, 12)}</code>
                {d.duplicate && <span className="muted"> same content as an earlier document; read once</span>}
              </li>
            ))}
          </ul>
          <ChangeSetReview items={preview.items} picks={picks} setPick={(id, p) => setPicks((s) => ({ ...s, [id]: p }))} />
          {preview.notes.map((n) => (
            <p key={n} className="muted">{n}</p>
          ))}
          <div className="actions">
            <button type="button" disabled={busy || selections.length === 0} onClick={() => void apply()}>
              {selections.length === 0 ? "Apply selected items" : `Apply ${selections.length} selected ${selections.length === 1 ? "item" : "items"}`}
            </button>
          </div>
        </div>
      )}
      <div role="status">{done && <p className="note ok">{done}</p>}</div>
    </section>
  );
}
