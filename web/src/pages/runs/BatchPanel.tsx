import { get } from "../../api/client";
import { applyBatchView } from "../../api/decoders_apply";
import { useInterval, useResource } from "../../hooks";
import { Link } from "../../router";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import { batchRowWords, outcomeLine, ownerTitle, ownerWords } from "../applynext/words";

const POLL_MS = 3000;

// BatchPanel is the compact view of the list a run belongs to: each row with
// its state and note, the stopped rows grouped by who has the next step, and
// the distinct defects with their companies.
export function BatchPanel({ batchId }: { batchId: string }) {
  const [res, retry] = useResource(() => get(`/api/apply/batches/${encodeURIComponent(batchId)}`, applyBatchView), [batchId], ["runs"]);
  useInterval(retry, POLL_MS, res.data?.state === "open");
  if (res.status === "loading") return <Loading label="Loading the batch" />;
  if (!res.data) return res.status === "error" ? <ErrorNote error={res.error} onRetry={retry} /> : null;
  const b = res.data;
  return (
    <section aria-labelledby="batch-heading" className="panel">
      <header className="panel-head">
        <h3 id="batch-heading">Batch of {b.rows.length} {b.rows.length === 1 ? "posting" : "postings"}</h3>
        <Chip tone={b.state === "open" ? "neutral" : "good"}>{b.state === "open" ? "Open" : "Closed"}</Chip>
      </header>
      <table className="cards">
        <caption className="sr-only">Rows of the batch</caption>
        <thead>
          <tr>
            <th scope="col">Company</th>
            <th scope="col">Role</th>
            <th scope="col">State</th>
            <th scope="col">Result</th>
          </tr>
        </thead>
        <tbody>
          {b.rows.map((r) => (
            <tr key={r.index}>
              <td data-label="Company">{r.runId ? <Link to={`/runs/${r.runId}`}>{r.company}</Link> : r.company}</td>
              <td data-label="Role">
                {r.role}
                {r.mirrors ? ` (+${r.mirrors} ${r.mirrors === 1 ? "mirror" : "mirrors"})` : ""}
              </td>
              <td data-label="State">{batchRowWords(r.state)}</td>
              <td data-label="Result">{r.outcome ? outcomeLine(r.outcome) : (r.note ?? "")}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {b.owners.length > 0 && (
        <>
          <h4>Who has the next step</h4>
          <ul aria-label="Stopped rows by owner">
            {b.owners.map((o) => (
              <li key={o.owner}>
                <strong>{ownerTitle(o.owner)}</strong>: {o.companies.join(", ")}
                <span className="muted"> {ownerWords(o.owner)}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {b.defects.length > 0 && (
        <>
          <h4>Defects</h4>
          <ul aria-label="Defects of the batch">
            {b.defects.map((d) => (
              <li key={`${d.stage}/${d.class}/${d.reason}`}>
                {d.stage} ({d.class}): {d.reason} <span className="muted">{d.companies.join(", ")}</span>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}
