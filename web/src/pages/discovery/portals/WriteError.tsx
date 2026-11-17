import { useState } from "react";
import { get } from "../../../api/client";
import { configDocument } from "../../../api/decoders_pages";
import { asApiError } from "../../../hooks";
import { ErrorNote } from "../../../ui/parts";
import { Changed } from "../../../ui/SaveBar";
import type { ConfigDocument } from "../../../wire";
import { useAction } from "../../action";
import { opsFor, savePortals, type Change } from "./edit";

// usePortalsWrite saves one change to portals.yml against the revision the
// page read. When the file changed meanwhile, the change is worked out again
// on the new file and offered in the shared Changed notice: Keep my change
// saves it there, Load the new version drops it. A change whose entry was
// removed or edited meanwhile is never re-applied; the note says so instead.
export function usePortalsWrite(doc: ConfigDocument, reloadDoc: () => void) {
  const act = useAction();
  const [again, setAgain] = useState<{ change: Change; doc?: ConfigDocument; why?: string; stale?: boolean }>();
  const write = async (change: Change, from = doc): Promise<boolean> => {
    setAgain(undefined);
    const done = await act.run(async () => {
      const ops = opsFor(from, change);
      if (typeof ops === "string") return setAgain({ change, why: ops }), false;
      try {
        return await savePortals(from, ops);
      } catch (e) {
        if (asApiError(e).kind !== "revision_conflict") throw e;
        const fresh = await get("/api/config/portals", configDocument);
        const redo = opsFor(fresh, change);
        setAgain({ change, stale: true, ...(typeof redo === "string" ? { why: `portals.yml changed since this page read it: ${redo}` } : { doc: fresh }) });
        return false;
      }
    });
    if (done) reloadDoc();
    return done === true;
  };
  const drop = () => (setAgain(undefined), reloadDoc());
  const notice = again?.doc ? (
    <Changed keep="Keep my change" onKeep={() => void write(again.change, again.doc)} onLoad={drop}>
      portals.yml changed since this page read it, so your change is not saved yet.
    </Changed>
  ) : again?.why ? (
    <div role="alert" className="note error">
      <p>{again.why} Your change was not saved.</p>
      {again.stale && (
        <div className="actions">
          <button type="button" onClick={drop}>Load the new version</button>
        </div>
      )}
    </div>
  ) : (
    act.error && <ErrorNote error={act.error} />
  );
  return { write, busy: act.busy, notice, clear: () => (act.clear(), setAgain(undefined)) };
}
