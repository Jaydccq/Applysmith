import { useRef, useState, type FormEvent } from "react";
import { del, get, post } from "../../api/client";
import { blockedCompany, blockedCompanyList } from "../../api/decoders_discovery";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Empty, ErrorNote, Panel } from "../../ui/parts";
import type { BlockedCompany } from "../../wire";
import { useAction, when } from "../action";

export function BlockedTab() {
  const [res, retry] = useResource(() => get("/api/blocked-companies", blockedCompanyList), [], ["scans"]);
  const [name, setName] = useState("");
  const [reason, setReason] = useState("");
  const [removing, setRemoving] = useState<BlockedCompany | null>(null);
  const act = useAction();
  const nameField = useRef<HTMLInputElement>(null);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const done = await act.run(() => post("/api/blocked-companies", blockedCompany, { name: name.trim(), reason: reason.trim() || undefined }));
    if (done) {
      setName("");
      setReason("");
      retry();
      // The button is disabled while the request runs and again once the field is empty.
      nameField.current?.focus();
    }
  };
  const remove = async () => {
    if (!removing) return;
    const done = await act.run(() => del(`/api/blocked-companies?name=${encodeURIComponent(removing.name)}`, blockedCompanyList));
    setRemoving(null);
    if (done) retry();
  };

  return (
    <>
      <p className="muted">A blocked company is left out of scans and evaluation. The same list is behind <code>applysmith blocked</code>.</p>
      <form onSubmit={add} className="inline-form" aria-label="Block a company">
        <label>
          Company
          <input ref={nameField} type="text" required value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          Reason (optional)
          <input type="text" value={reason} onChange={(e) => setReason(e.target.value)} />
        </label>
        <button type="submit" disabled={act.busy || name.trim() === ""}>
          Block company
        </button>
      </form>
      {act.error && <ErrorNote error={act.error} />}
      <Panel title="Blocked companies" id="blocked-list" res={res} retry={retry}>
        {(data) =>
          data.companies.length === 0 ? (
            <Empty>No company is blocked.</Empty>
          ) : (
            <ul className="entry-list plain">
              {data.companies.map((c) => (
                <li key={c.id} className="row">
                  <span>
                    <strong>{c.name}</strong> {c.reason && <span>{c.reason}</span>} <span className="muted">added {when(c.addedAt)} by {c.addedBy}</span>
                  </span>
                  <button type="button" aria-label={`Unblock ${c.name}`} onClick={() => setRemoving(c)}>
                    Unblock
                  </button>
                </li>
              ))}
            </ul>
          )
        }
      </Panel>
      {removing && (
        <ConfirmDialog
          title={`Unblock ${removing.name}`}
          consequence="Scans and evaluation consider this company again."
          verb="Unblock"
          busy={act.busy}
          onConfirm={() => void remove()}
          onCancel={() => setRemoving(null)}
        />
      )}
    </>
  );
}
