import { useState } from "react";
import { Merge } from "lucide-react";
import { get, post } from "../../api/client";
import { duplicateMergeResult, trackerDuplicates } from "../../api/decoders";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { ErrorNote, UndoNote, formatDate, humanize } from "../../ui/parts";
import type { DuplicateGroup } from "../../wire";
import { useAction } from "../action";

const DISMISS = "/api/tracker/duplicates/dismiss";
const MERGE = "/api/tracker/duplicates/merge";

// Duplicates lists the groups `applysmith tracker dedup` finds. Merge, confirmed,
// folds a group into the row it keeps; its Undo puts the rows back for 15
// minutes (the server holds them in memory). Not duplicates keeps a group's
// rows apart until the group changes, with Undo. A group left to the user says
// why. After a merge the keyboard goes to the kept row when the page lists it
// (focusRow answers whether it did), else to Undo.
export function Duplicates({ onOpen, onChanged, focusRow }: { onOpen: (id: number) => void; onChanged: () => void; focusRow: (id: number) => boolean }) {
  const [res, retry] = useResource(() => get("/api/tracker/duplicates", trackerDuplicates), [], ["tracker"]);
  const [asking, setAsking] = useState<DuplicateGroup>();
  const [last, setLast] = useState<{ n: number; done: string; undone: string; undo: () => Promise<unknown>; keepFocus: boolean }>();
  const act = useAction();
  const groups = res.data?.groups ?? [];
  const refresh = () => (retry(), onChanged());
  const note = (done: string, undone: string, undo: () => Promise<unknown>, keepFocus = false) => setLast({ n: (last?.n ?? 0) + 1, done, undone, undo, keepFocus });
  const ids = (g: DuplicateGroup) => g.rows.map((r) => r.id);
  const merge = async (g: DuplicateGroup) => {
    setAsking(undefined);
    const got = await act.run(() => post(MERGE, duplicateMergeResult, { members: ids(g) }));
    refresh();
    if (got) note(`Merged ${g.rows.length} rows of ${got.kept.company} into row ${got.kept.id}.`, `The ${g.rows.length} rows of ${got.kept.company} are back as they were.`, () => post(MERGE, duplicateMergeResult, { undo: got.undo }), focusRow(got.kept.id));
  };
  const dismiss = async (g: DuplicateGroup) => {
    const rows = ids(g);
    const got = await act.run(() => post(DISMISS, trackerDuplicates, { members: rows }));
    refresh();
    if (got) note(`Rows ${rows.join(", ")} stay apart as different applications.`, `Rows ${rows.join(", ")} are listed as possible duplicates again.`, () => post(DISMISS, trackerDuplicates, { members: rows, undo: true }));
  };
  const undo = async (l: NonNullable<typeof last>) => {
    const ok = (await act.run(l.undo)) !== undefined;
    refresh();
    return ok;
  };
  return (
    <>
      {groups.length > 0 && (
        <section aria-labelledby="tracker-duplicates" className="panel">
          <header className="panel-head">
            <h2 id="tracker-duplicates"><Merge className="panel-icon" />Possible duplicates</h2>
          </header>
          <ul className="plain item-list">
            {groups.map((g) => (
              <li key={ids(g).join("-")} className="inbox-item">
                <span className="item-main">
                  <strong>{g.rows[0]?.company}</strong>: {g.rows.length} rows, same {g.rule === "identity key" ? "posting address" : "company and role"}
                  {g.rows.map((r) => (
                    <span key={r.id} className="item-detail">
                      <button type="button" className="small" aria-label={`Open row ${r.id}: ${r.company}, ${r.role}`} onClick={() => onOpen(r.id)}>Row {r.id}</button> {r.role} {"·"} {humanize(r.status)} {"·"} {formatDate(r.date)}
                    </span>
                  ))}
                  {g.needsUser && <span className="item-detail">Left to you: {g.reason}</span>}
                </span>
                <span className="actions">
                  {!g.needsUser && <button type="button" className="small" disabled={act.busy} onClick={() => setAsking(g)}>Merge into row {g.survivor}</button>}
                  <button type="button" className="small" disabled={act.busy} aria-label={`Not duplicates: rows ${ids(g).join(", ")} of ${g.rows[0]?.company}`} onClick={() => void dismiss(g)}>Not duplicates</button>
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}
      <div role="status">
        {last && <UndoNote key={last.n} keepFocus={last.keepFocus} done={last.done} undone={last.undone} onUndo={() => undo(last)} />}
        {act.error && <ErrorNote error={act.error} />}
      </div>
      {asking && (
        <ConfirmDialog
          title={`Merge ${asking.rows.length} rows of ${asking.rows[0]?.company}`}
          consequence={`Row ${asking.survivor} stays as ${humanize(asking.status)}; the other rows' notes, history and links move to it and they are deleted. Undo puts them back for 15 minutes.`}
          verb="Merge rows"
          onConfirm={() => void merge(asking)}
          onCancel={() => setAsking(undefined)}
        />
      )}
    </>
  );
}
