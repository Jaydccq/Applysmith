import { useState } from "react";
import { get } from "../../../api/client";
import { portalCompanyList } from "../../../api/decoders_discovery";
import { useResource } from "../../../hooks";
import { ConfirmDialog } from "../../../ui/Modal";
import { Chip, Empty, Panel } from "../../../ui/parts";
import type { ConfigDocument, PortalCompany } from "../../../wire";
import { text } from "../../profile/shared";
import { when } from "../../action";
import { usePortalsWrite } from "./WriteError";
import { companiesOf, type Change } from "./edit";

const where = (c: PortalCompany) => (c.ats ? `${c.ats}: ${c.slug}` : c.board ? `${c.board} (from its careers address)` : c.careersUrl);

function Table({ rows, busy, onToggle, onRemove }: { rows: PortalCompany[]; busy: boolean; onToggle: (i: number, on: boolean) => void; onRemove: (i: number) => void }) {
  return (
    <table className="cards">
      <caption className="sr-only">Companies in portals.yml</caption>
      <thead>
        <tr>
          <th scope="col">Company</th>
          <th scope="col">Board</th>
          <th scope="col">Enabled</th>
          <th scope="col">Last scan</th>
          <th scope="col">Jobs</th>
          <th scope="col">Actions</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((c, i) => (
          <tr key={`${c.name}-${i}`}>
            <td data-label="Company">
              <strong>{c.name}</strong> {c.ownTitleFilter && <Chip>own title filter</Chip>} {c.status && c.status !== "active" && <Chip tone="warn">{c.status}</Chip>}
            </td>
            <td data-label="Board">{where(c)}</td>
            <td data-label="Enabled">
              <input type="checkbox" checked={c.enabled} disabled={busy} aria-label={`Scan ${c.name}`} onChange={(e) => onToggle(i, e.target.checked)} />
            </td>
            <td data-label="Last scan">{c.lastScannedAt ? when(c.lastScannedAt) : "never"}</td>
            <td data-label="Jobs">{c.jobCount ?? "unknown"}</td>
            <td data-label="Actions">
              <button type="button" className="danger-outline" aria-label={`Remove ${c.name}`} disabled={busy} onClick={() => onRemove(i)}>
                Remove
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Companies is the company table of portals.yml with what the registry knows of
// each board: when it was last scanned and how many postings it listed. The
// enabled box and Remove change that one entry through the revision-checked
// patch; row n of the table is entry n of the file.
export function Companies({ doc, reloadDoc }: { doc: ConfigDocument; reloadDoc: () => void }) {
  const [res, reload] = useResource(() => get("/api/portals/companies", portalCompanyList), [doc.revision], ["scans", "configuration"]);
  const [removing, setRemoving] = useState<number | null>(null);
  const { write: save, busy, notice } = usePortalsWrite(doc, reloadDoc);
  const entries = companiesOf(doc);
  const change = (i: number, c: { kind: "remove" } | { kind: "set"; key: string; value: unknown }): Change => ({ list: "companies", item: entries[i], label: text(entries[i]?.name), ...c });

  const write = async (c: Change) => {
    await save(c);
    setRemoving(null);
  };

  return (
    <Panel title="Companies" id="portals-companies" res={res} retry={reload}>
      {(data) => (
        <>
          {data.companies.length === 0 && <Empty>No company is listed. Add one below.</Empty>}
          {data.companies.length > 0 && data.companies.length !== entries.length && (
            <p role="status">
              The file changed while this loaded. <button type="button" onClick={() => { reloadDoc(); reload(); }}>Reload</button>
            </p>
          )}
          {data.companies.length > 0 && data.companies.length === entries.length && (
            <Table
              rows={data.companies}
              busy={busy}
              onToggle={(i, enabled) => void write(change(i, { kind: "set", key: "enabled", value: enabled }))}
              onRemove={setRemoving}
            />
          )}
          {notice}
          {removing !== null && (
            <ConfirmDialog
              title={`Remove ${String(entries[removing]?.name ?? "this company")}`}
              consequence="The entry leaves portals.yml. Postings it already added to the pipeline inbox stay there."
              verb="Remove"
              busy={busy}
              onConfirm={() => void write(change(removing, { kind: "remove" }))}
              onCancel={() => setRemoving(null)}
            />
          )}
        </>
      )}
    </Panel>
  );
}
