import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { ApiError, get, put } from "../../api/client";
import { applySkipList } from "../../api/decoders_apply";
import { asApiError, useResource } from "../../hooks";
import { Modal } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Loading } from "../../ui/parts";
import type { ApplySkipList } from "../../wire";

const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);
const has = (list: string[], name: string) => list.some((x) => x.toLowerCase() === name.toLowerCase());

// Editor owns the draft. The field to add a name comes before the list, so a
// long list never pushes it out of view. A new revision (a save, or a reload
// after a conflict) starts the draft again from the server's list in place,
// not by remounting, so a save keeps its note and the focus on it.
function Editor({ saved, onSaved, onClose, onReload }: { saved: ApplySkipList; onSaved: () => void; onClose: () => void; onReload: () => void }) {
  const [draft, setDraft] = useState(saved.entries);
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);
  // The list the last save wrote; while the draft still holds it, it is saved.
  const [savedList, setSavedList] = useState<string[] | null>(null);
  const field = useRef<HTMLInputElement>(null);
  const fieldId = useId();
  const status = useRef<HTMLParagraphElement>(null);
  const dirty = !sameList(draft, saved.entries);
  const done = savedList !== null && sameList(savedList, draft);
  const conflict = failure?.kind === "revision_conflict" || failure?.kind === "conflict";
  useEffect(() => {
    setDraft(saved.entries);
    setFailure(null);
  }, [saved.revision]);
  // The save button is disabled once the list is saved, and a disabled control
  // cannot keep focus, so the saved note takes it.
  useEffect(() => {
    if (savedList) status.current?.focus();
  }, [savedList]);

  function add(e: FormEvent) {
    e.preventDefault();
    const n = name.trim();
    if (!n) return setNote("Type an employer name to add.");
    if (has(draft, n)) return setNote(`${n} is already on the list.`);
    setDraft([...draft, n]);
    setName("");
    setNote("");
    field.current?.focus();
  }

  async function save() {
    setBusy(true);
    setFailure(null);
    try {
      await put("/api/apply/skip-list", applySkipList, { entries: draft, revision: saved.revision });
      setSavedList(draft);
      onSaved();
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <p>
        {saved.defaulted ? <Chip>Built-in list</Chip> : saved.differs ? <Chip tone="warn">Differs from the default</Chip> : <Chip>Same as the default</Chip>}{" "}
        {saved.defaulted ? "Your profile has no list of its own, so the built-in employers apply." : saved.differs ? "This list is not the built-in one." : "This list matches the built-in one."}
      </p>
      <p className="muted">A batch leaves these employers to you. A single application you start yourself is not blocked by the list.</p>
      {failure && !conflict && <ErrorNote error={failure} />}
      {conflict && (
        <div role="alert" className="note error">
          <p>The list changed since you opened it, so nothing was saved. Reload to see the current list, then make your change again.</p>
          <button type="button" onClick={onReload}>
            Reload the list
          </button>
        </div>
      )}
      <form onSubmit={add} aria-label="Add an employer">
        <label htmlFor={fieldId}>Employer</label>
        <p className="add-line">
          <input ref={field} id={fieldId} type="text" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
          <button type="submit">Add</button>
        </p>
      </form>
      <p aria-live="polite" className="muted">
        {note}
      </p>
      {draft.length === 0 ? (
        <Empty>No employer is on the list, so a batch applies everywhere.</Empty>
      ) : (
        <>
          <h3>
            {draft.length} {draft.length === 1 ? "employer" : "employers"}
          </h3>
          <ul className="plain stack skip-entries" aria-label="Employers on the skip list">
            {draft.map((entry) => (
              <li key={entry} className="row">
                <span>{entry}</span>
                <button type="button" className="small" aria-label={`Remove ${entry}`} onClick={() => setDraft(draft.filter((x) => x !== entry))}>
                  Remove
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
      <p role="status" tabIndex={-1} ref={status} className={done ? "note ok" : dirty ? undefined : "sr-only"}>
        {done ? "Your changes are saved." : dirty ? "You have unsaved changes." : ""}
      </p>
      <div className="actions">
        <button type="button" className="primary" disabled={!dirty || busy || done} onClick={() => void save()}>
          Save the list
        </button>
        <button type="button" disabled={sameList(draft, saved.defaults)} onClick={() => setDraft(saved.defaults)}>
          Restore the default list
        </button>
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </>
  );
}

// SkipListDrawer edits the batch skip list. The whole list is written back
// with the revision it was read at, so an edit made elsewhere is a conflict
// and never silently overwritten.
export function SkipListDrawer({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [res, reload] = useResource(() => get("/api/apply/skip-list", applySkipList), []);
  return (
    <Modal title="Batch skip list" variant="drawer" onClose={onClose}>
      {res.status === "loading" && <Loading label="Loading the skip list" />}
      {res.status === "error" && !res.data && <ErrorNote error={res.error} onRetry={reload} />}
      {res.data && (
        <Editor
          saved={res.data}
          onSaved={() => {
            reload();
            onSaved();
          }}
          onReload={reload}
          onClose={onClose}
        />
      )}
      {!res.data && (
        <div className="actions">
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
      )}
    </Modal>
  );
}
