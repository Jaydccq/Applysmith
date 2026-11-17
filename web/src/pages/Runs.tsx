import { useContext } from "react";
import { get, quietly, withQuery } from "../api/client";
import { boardEntryList, boardSummary, needsYouInbox } from "../api/decoders_runs";
import { BoardContext, useInterval, useResource } from "../hooks";
import { navigate, useLocation, useSearchParams } from "../router";
import { ErrorNote, Loading, humanize } from "../ui/parts";
import { Board } from "./runs/Board";
import { RunDetail } from "./runs/Detail";
import { Inbox } from "./runs/Inbox";
import { KINDS, STATUSES, statusOf } from "./runs/format";
import { PageHeader } from "../ui/PageHeader";
import { ClearChip, isDay, sinceWords } from "../ui/FilterChips";

const POLL_MS = 3000;

export default function Runs() {
  const [params, setParams] = useSearchParams();
  const { pathname } = useLocation();
  const open = pathname.startsWith("/runs/") ? decodeURIComponent(pathname.slice("/runs/".length)) : "";
  const kind = KINDS.find((k) => k === params.get("kind")) ?? "";
  const status = Object.hasOwn(STATUSES, params.get("status") ?? "") ? (params.get("status") ?? "") : "";
  // since, a local day a count's link carries, keeps the runs that ended or
  // stopped since, acknowledged ones too; the server reads it.
  const since = isDay(params.get("since") ?? "") ? (params.get("since") ?? "") : "";
  const [board, retryBoard] = useResource(() => get("/api/runs", boardEntryList, { limit: 500, since }), [since], ["runs"]);
  const [inbox, retryInbox] = useResource(() => get("/api/needs-you", needsYouInbox), [], ["runs"]);
  const refresh = () => {
    retryBoard();
    retryInbox();
  };
  // A run record's progress reaches the board when a read syncs it, and the
  // board announces what changed, which reads both lists again. So while a run
  // works the page asks for the summary alone, the smallest read that syncs.
  // Without a live stream no notice would follow, and it reads the lists.
  const stream = useContext(BoardContext);
  const poll = () => {
    const { revisions, reconnecting } = stream.getState();
    if (revisions && !reconnecting) void quietly(() => get("/api/runs/summary", boardSummary)).catch(() => undefined);
    else refresh();
  };
  useInterval(poll, POLL_MS, board.data?.entries.some((e) => e.state === "working") ?? false);

  const entries = (board.data?.entries ?? []).filter((e) => (!kind || e.kind === kind) && (!status || statusOf(e) === status));
  const listed = new Set(board.data?.entries.map((e) => e.id));
  const waiting = (inbox.data?.items ?? []).filter((i) => (!kind || i.entry.kind === kind) && (!status || statusOf(i.entry) === status) && (!since || listed.has(i.entry.id)));
  const inInbox = new Set(waiting.map((i) => i.entry.id));
  return (
    <>
      <PageHeader path="/runs">Everything running, waiting for you or finished in the last 7 days.</PageHeader>
      <form className="filters compact" onSubmit={(e) => e.preventDefault()} aria-label="Filters">
        <label>
          Kind
          <select value={kind} onChange={(e) => setParams({ kind: e.target.value }, { replace: true })}>
            <option value="">All kinds</option>
            {KINDS.map((k) => (
              <option key={k} value={k}>
                {humanize(k)}
              </option>
            ))}
          </select>
        </label>
        <label>
          Status
          <select value={status} onChange={(e) => setParams({ status: e.target.value }, { replace: true })}>
            <option value="">All statuses</option>
            {Object.entries(STATUSES).map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </select>
        </label>
      </form>
      {since && <ClearChip label={`Ended ${sinceWords(since)}`} to={withQuery("/runs", Object.fromEntries([...params].filter(([k]) => k !== "since")))} />}
      {inbox.status === "error" && !inbox.data && <ErrorNote error={inbox.error} onRetry={retryInbox} />}
      {/* The inbox is drawn only when it has something to act on; the board
          below leaves out what it lists. */}
      {/* The board waits for the inbox too, so an inbox that arrives second
          does not push a drawn board down. */}
      {(board.status === "loading" || inbox.status === "loading") && <Loading />}
      {inbox.data && waiting.length > 0 && <Inbox items={waiting} onChange={refresh} />}
      {board.status === "error" && <ErrorNote error={board.error} onRetry={retryBoard} />}
      {board.data && inbox.status !== "loading" && (
        <Board entries={entries} inInbox={inInbox} filtered={Boolean(kind || status || since)} onClear={() => setParams({ kind: "", status: "", since: "" }, { replace: true })} onChange={refresh} />
      )}
      {open && <RunDetail key={open} id={open} onClose={() => navigate(`/runs${window.location.search}`)} onChange={refresh} />}
    </>
  );
}
