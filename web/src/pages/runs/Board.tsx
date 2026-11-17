import { useEffect, useRef, useState } from "react";
import { post } from "../../api/client";
import { boardEntry } from "../../api/decoders_runs";
import { useInterval } from "../../hooks";
import { Link } from "../../router";
import { Chip, Empty, ShowMore, formatDateTime, humanize, useCapped } from "../../ui/parts";
import type { BoardEntry } from "../../wire";
import { WEEK_MS, clockIn, dayGroup, durationLabel, groupRepeats, plainDetail, stageDoing, stateLabel, tone, waitedLabel } from "./format";

// A run that waits for the user and is not acknowledged is in the Needs you
// inbox above the board, with its actions; the Waiting column holds only the
// acknowledged ones that still wait, and is left out while there are none.
const COLUMNS = [
  { id: "working", title: "Running", empty: "Nothing is running." },
  { id: "waiting", title: "Still waiting", empty: "" },
  { id: "done", title: "Finished", empty: "Nothing finished in the last 7 days." },
] as const;

function columnOf(e: BoardEntry): string {
  if (e.state === "working") return "working";
  if (e.state === "needs-you" || e.state === "blocked") return "waiting";
  return "done";
}

// useHeldBack keeps a card from moving under the pointer. While the user
// scrolls or points at the board, an entry that was not on it stays out of it
// and is counted; showing them all is the user's act.
function useHeldBack(entries: BoardEntry[], frozen: boolean): { shown: BoardEntry[]; fresh: number; showAll: () => void } {
  const known = useRef<Set<string> | null>(null);
  const [, bump] = useState(0);
  if (known.current === null || !frozen) known.current = new Set(entries.map((e) => e.id));
  const shown = entries.filter((e) => known.current?.has(e.id));
  return {
    shown,
    fresh: entries.length - shown.length,
    showAll: () => {
      known.current = new Set(entries.map((e) => e.id));
      bump((n) => n + 1);
    },
  };
}

function useScrolled(): boolean {
  const [scrolled, setScrolled] = useState(() => window.scrollY > 0);
  useEffect(() => {
    const on = () => setScrolled(window.scrollY > 0);
    window.addEventListener("scroll", on, { passive: true });
    return () => window.removeEventListener("scroll", on);
  }, []);
  return scrolled;
}

// useNow ticks once a second while on, so a running card's duration moves
// between polls. It reads the client clock only.
function useNow(on: boolean): number {
  const [now, setNow] = useState(Date.now);
  useInterval(() => setNow(Date.now()), 1000, on);
  return now;
}

// kindLine says what kind of run a card is and where it stands.
function kindLine(e: BoardEntry): string {
  const working = e.state === "working";
  const doing = stageDoing(e.stage, working);
  if (!doing || e.outcome === "ok") return humanize(e.kind);
  return `${humanize(e.kind)}, ${working ? doing : `stopped while ${doing}`}`;
}

// timeLine is a card's time: when a running card started and how long it has
// run, how long a waiting card has waited, and when a finished card ended
// (inside a day group that already names the day, the time alone).
function timeLine(e: BoardEntry, group: string, now: number): string {
  if (e.state === "working") {
    const today = dayGroup(e.startedAt, now) === "Today";
    return `Started ${today ? clockIn(e.startedAt, "Today") : formatDateTime(e.startedAt)}, running ${durationLabel(e.startedAt, undefined, now)}`;
  }
  if (e.state === "needs-you" || e.state === "blocked") return `Waiting ${waitedLabel((now - Date.parse(e.stateAt)) / 1000)}, worked ${durationLabel(e.startedAt, e.stateAt, now)}`;
  const end = e.finishedAt ?? e.updatedAt;
  return `Finished ${group ? clockIn(end, group) : formatDateTime(end)}, took ${durationLabel(e.startedAt, end, now)}`;
}

// Card is one run; with earlier runs it is a posting stopped for one reason
// more than once, as the inbox folds it: the newest run shows, the others are
// counted and listed on request.
function Card({ entry, earlier = [], group, now: boardNow }: { entry: BoardEntry; earlier?: BoardEntry[]; group: string; now: number }) {
  const [open, setOpen] = useState(false);
  const waiting = entry.state === "needs-you" || entry.state === "blocked";
  const working = entry.state === "working";
  const ticking = useNow(working);
  const now = working ? ticking : boardNow;
  // A waiting or failed run's detail says why; a finished one's says what it
  // came to ("7 found, 3 new, 3 queued for evaluation", a report's score).
  const why = waiting || entry.state === "done" ? plainDetail(entry.detail, entry.reason, entry.kind) : null;
  // A bare kind the title already starts with ("Apply" over "Apply to Acme")
  // says nothing more.
  const kind = kindLine(entry);
  const showKind = !entry.title.toLowerCase().startsWith(kind.toLowerCase());
  return (
    <li className="run-card">
      <div className="run-card-head">
        <span>
          {working && <span className="live-dot" aria-hidden="true" />}
          <Link to={`/runs/${entry.id}`}>{entry.title}</Link>
          {earlier.length > 0 && (
            <>
              {" "}
              <Chip>{`${earlier.length + 1} runs`}</Chip>
            </>
          )}
        </span>
        <Chip tone={tone(entry)}>{stateLabel(entry)}</Chip>
      </div>
      {showKind && <p className="muted">{kind}</p>}
      {why && (
        <p className="muted clamp" title={why.line}>
          {why.line}
        </p>
      )}
      <p className="muted">{timeLine(entry, group, now)}</p>
      {earlier.length > 0 && (
        <>
          <button type="button" className="small ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? "Hide earlier runs" : `Show ${earlier.length} earlier ${earlier.length === 1 ? "run" : "runs"}`}
          </button>
          {open && (
            <ul className="plain inbox-repeats">
              {earlier.map((e) => (
                <li key={e.id}>
                  <Link to={`/runs/${e.id}`}>Waiting {waitedLabel((now - Date.parse(e.stateAt)) / 1000)}</Link>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </li>
  );
}

// COLUMN_CAP is how many cards a column draws before "Show more": the Needs
// you list above the board carries the actions, so the board stays short.
const COLUMN_CAP = 10;

// A card is a run with the older runs of the same posting and stop folded in
// (only the waiting column folds; a finished run is its own record).
interface CardRuns {
  entry: BoardEntry;
  earlier: BoardEntry[];
}

// Cards are drawn in groups: finished runs by the day they ended, the other
// columns as one group without a heading.
function groups(colId: string, cards: CardRuns[], now: number): { name: string; cards: CardRuns[] }[] {
  if (colId !== "done") return [{ name: "", cards }];
  const out: { name: string; cards: CardRuns[] }[] = [];
  for (const c of cards) {
    const name = dayGroup(c.entry.finishedAt ?? c.entry.updatedAt, now);
    const last = out[out.length - 1];
    if (last?.name === name) last.cards.push(c);
    else out.push({ name, cards: [c] });
  }
  return out;
}

// AckAll acknowledges every finished run not yet acknowledged, one request
// each, and says how many went through.
function AckAll({ cards, onChange }: { cards: BoardEntry[]; onChange: () => void }) {
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState("");
  const todo = cards.filter((e) => !e.acknowledgedAt);
  if (todo.length === 0 && !said) return null;
  async function run() {
    setBusy(true);
    setSaid("");
    const results = await Promise.allSettled(todo.map((e) => post(`/api/runs/${e.id}/ack`, boardEntry)));
    const failed = results.filter((r) => r.status === "rejected").length;
    setSaid(`Acknowledged ${results.length - failed}${failed ? `; ${failed} failed` : ""}.`);
    setBusy(false);
    onChange();
  }
  return (
    <>
      {todo.length > 0 && (
        <button type="button" className="small" disabled={busy} onClick={() => void run()}>
          Acknowledge all
        </button>
      )}
      <span role="status" className="muted">
        {said}
      </span>
    </>
  );
}

function Column({ col, cards, filtered, onClear, onChange, now }: { col: (typeof COLUMNS)[number]; cards: BoardEntry[]; filtered: boolean; onClear: () => void; onChange: () => void; now: number }) {
  const folded: CardRuns[] = col.id === "waiting" ? groupRepeats(cards, (e) => e).map(([entry, ...earlier]) => ({ entry, earlier })) : cards.map((entry) => ({ entry, earlier: [] }));
  const { shown, hidden, more } = useCapped(folded, COLUMN_CAP);
  return (
    <section aria-labelledby={`col-${col.id}`} className="panel">
      <header className="panel-head">
        <h2 id={`col-${col.id}`}>{col.title}</h2>
        <span className="panel-head-end">
          {col.id === "done" && <AckAll cards={cards} onChange={onChange} />}
          <Chip>{cards.length}</Chip>
        </span>
      </header>
      {cards.length === 0 ? (
        filtered ? (
          <Empty>
            No runs match these filters.{" "}
            <button type="button" className="small" onClick={onClear}>
              Clear filters
            </button>
          </Empty>
        ) : (
          <Empty>{col.empty}</Empty>
        )
      ) : (
        groups(col.id, shown, now).map((g) => (
          <div key={g.name || "all"} className="run-group">
            {g.name && <h3 className="run-group-name">{g.name}</h3>}
            <ul className="plain stack">
              {g.cards.map((c) => (
                <Card key={c.entry.id} entry={c.entry} earlier={c.earlier} group={g.name} now={now} />
              ))}
            </ul>
          </div>
        ))
      )}
      <ShowMore hidden={hidden} onMore={more} />
    </section>
  );
}

export function Board({ entries, inInbox = new Set(), filtered = false, onClear = () => {}, onChange = () => {}, now = Date.now() }: { entries: BoardEntry[]; inInbox?: Set<string>; filtered?: boolean; onClear?: () => void; onChange?: () => void; now?: number }) {
  const scrolled = useScrolled();
  const [pointing, setPointing] = useState(false);
  const recent = entries.filter((e) => !inInbox.has(e.id) && (e.state !== "done" || now - Date.parse(e.finishedAt ?? e.updatedAt) <= WEEK_MS));
  const { shown, fresh, showAll } = useHeldBack(recent, scrolled || pointing);
  return (
    <div onPointerEnter={() => setPointing(true)} onPointerLeave={() => setPointing(false)}>
      {/* The status region stays, so a screen reader hears each new count. */}
      <div role="status">
        {fresh > 0 && (
          <p>
            <button type="button" onClick={showAll}>
              {fresh} new
            </button>
          </p>
        )}
      </div>
      <div className="grid board">
        {COLUMNS.map((col) => {
          const cards = shown.filter((e) => columnOf(e) === col.id);
          if (col.id === "waiting" && cards.length === 0) return null;
          return <Column key={col.id} col={col} cards={cards} filtered={filtered} onClear={onClear} onChange={onChange} now={now} />;
        })}
      </div>
    </div>
  );
}
