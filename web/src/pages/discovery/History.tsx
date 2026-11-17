import { get } from "../../api/client";
import { scanList } from "../../api/decoders_discovery";
import { usePageInRange, useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { Chip, Empty, Pager, Panel } from "../../ui/parts";
import { when } from "../action";
import { FunnelPanel } from "./Funnel";
import { countsLine, droppedLine, elapsed, scanLabel, scanTone, STATUSES, why } from "./format";
import { ScanWhy } from "./Run";

const PAGE_SIZE = 20;

export function HistoryTab({ sources }: { sources: string[] }) {
  const [params, setParams] = useSearchParams();
  const source = params.get("source") ?? "";
  const status = params.get("status") ?? "";
  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const [res, retry] = useResource(() => get("/api/scans", scanList, { source, status, from, to, page, limit: PAGE_SIZE }), [source, status, from, to, page], ["scans"]);
  const toPage = (p: number) => setParams({ page: p > 1 ? String(p) : undefined }, { replace: true });
  usePageInRange(res.data && { ...res.data, rows: res.data.scans, pageSize: res.data.limit }, toPage);
  const set = (patch: Record<string, string>) => setParams({ ...patch, page: undefined }, { replace: true });
  const filtered = Boolean(source || status || from || to);

  return (
    <>
      <form className="filters" aria-label="Scan history filters" onSubmit={(e) => e.preventDefault()}>
        <label>
          Source
          <select value={source} onChange={(e) => set({ source: e.target.value })}>
            <option value="">All sources</option>
            {sources.map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
        </label>
        <label>
          Status
          <select value={status} onChange={(e) => set({ status: e.target.value })}>
            <option value="">All statuses</option>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {scanLabel(s)}
              </option>
            ))}
          </select>
        </label>
        <label>
          From
          <input type="date" value={from} onChange={(e) => set({ from: e.target.value })} />
        </label>
        <label>
          To
          <input type="date" value={to} onChange={(e) => set({ to: e.target.value })} />
        </label>
      </form>
      <FunnelPanel />
      <Panel title="Scan history" id="history-table" res={res} retry={retry}>
        {(data) =>
          data.scans.length === 0 ? (
            <Empty>{filtered ? <>No scan matches. <Link to="/discovery?tab=history">Clear the filters</Link></> : "No scan has run yet."}</Empty>
          ) : (
            <>
              <p aria-live="polite">{data.total} {data.total === 1 ? "scan" : "scans"}</p>
              <table className="cards">
                <caption className="sr-only">Scans</caption>
                <thead>
                  <tr>
                    <th scope="col">Source</th>
                    <th scope="col">Status</th>
                    <th scope="col">Started</th>
                    <th scope="col">Took</th>
                    <th scope="col">Result</th>
                  </tr>
                </thead>
                <tbody>
                  {data.scans.map((s) => (
                    <tr key={s.id}>
                      <td data-label="Source">
                        <Chip raw>{s.source}</Chip>
                      </td>
                      <td data-label="Status">
                        <Chip tone={scanTone(s.status)}>{scanLabel(s.status)}</Chip>
                        {why(s) && (
                          <span className="muted">
                            {" "}
                            <ScanWhy rec={s} />
                          </span>
                        )}
                      </td>
                      <td data-label="Started" className="nowrap">
                        {when(s.startedAt)}
                      </td>
                      <td data-label="Took" className="nowrap">
                        {elapsed(s)}
                      </td>
                      <td data-label="Result">
                        {countsLine(s)}
                        {droppedLine(s) && <span className="sub muted">{droppedLine(s)}</span>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <Pager page={data.page} pageSize={data.limit} total={data.total} onPage={toPage} />
            </>
          )
        }
      </Panel>
    </>
  );
}
