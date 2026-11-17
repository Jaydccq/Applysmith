import { createContext, useCallback, useContext, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { ApiError, quietly, writesSent } from "./api/client";
import type { Board, BoardState } from "./api/board";

const idle: BoardState = { revisions: null, epoch: null, reconnecting: false, stale: false, links: 0 };
const idleBoard: Board = { getState: () => idle, subscribe: () => () => {}, start() {}, stop() {}, retryNow() {} };

export const BoardContext = createContext<Board>(idleBoard);

export function useBoardState(): BoardState {
  const board = useContext(BoardContext);
  return useSyncExternalStore(board.subscribe, board.getState);
}

// useInterval calls fn every ms while active. A stage change inside a running
// board entry is silent on the event stream, so a page that shows one asks
// again while it works.
// While the page is hidden (the window closed to the menu bar, minimized or
// behind another) nobody sees the answer, so the timer stops; the page asks
// once as it is shown again and then keeps the pace.
export function useInterval(fn: () => void, ms: number, active: boolean): void {
  const ref = useRef(fn);
  ref.current = fn;
  const visible = usePageVisible();
  const missed = useRef(false);
  useEffect(() => {
    if (!active) return;
    if (!visible) {
      missed.current = true;
      return;
    }
    if (missed.current) {
      missed.current = false;
      ref.current();
    }
    const t = setInterval(() => ref.current(), ms);
    return () => clearInterval(t);
  }, [ms, active, visible]);
}

// usePageInRange moves a paged list off a page left empty, as when its last
// rows were dismissed, to the last page that still has rows.
export function usePageInRange(data: { rows: unknown[]; page: number; pageSize: number; total: number } | undefined, go: (page: number) => void): void {
  useEffect(() => {
    if (data && data.rows.length === 0 && data.page > 1) go(Math.max(1, Math.ceil(data.total / data.pageSize)));
  }, [data, go]);
}

// usePageVisible is true while the browser shows this page; a hidden tab
// stops the work that only a viewer would see.
export function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden");
  useEffect(() => {
    const on = () => setVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  return visible;
}

// useLocalDay is this computer's calendar day. It moves at local midnight while
// a page stays open, so a card that says "today" can be read again (an input
// of its useResource).
export function useLocalDay(): string {
  const [turns, setTurns] = useState(0);
  useEffect(() => {
    const midnight = new Date().setHours(24, 0, 0, 0);
    const timer = setTimeout(() => setTurns((n) => n + 1), midnight - Date.now() + 1000);
    return () => clearTimeout(timer);
  }, [turns]);
  return new Date().toDateString();
}

// useExpiring holds pairing material only while it is valid: the item, the
// whole seconds it has left, counted each second, and whether the last one ran
// out. hold takes a new item, or null to drop it; asking takes the note of the
// last one's end away as a new one is requested.
export function useExpiring<T extends { expiresAt: string }>(): { item: T | null; seconds: number; expired: boolean; hold: (item: T | null) => void; asking: () => void } {
  const [item, setItem] = useState<T | null>(null);
  const [now, setNow] = useState(Date.now());
  const [expired, setExpired] = useState(false);
  const seconds = item ? Math.max(0, Math.ceil((Date.parse(item.expiresAt) - now) / 1000)) : 0;
  useEffect(() => {
    if (!item) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [item]);
  useEffect(() => {
    if (item && seconds === 0) {
      setItem(null);
      setExpired(true);
    }
  }, [item, seconds]);
  const hold = useCallback((next: T | null) => {
    setItem(next);
    setNow(Date.now());
  }, []);
  const asking = useCallback(() => setExpired(false), []);
  return { item, seconds, expired, hold, asking };
}

// useNarrow is true on a phone: the width at which the tab bar replaces the
// sidebar.
export function useNarrow(): boolean {
  const query = "(max-width: 767px)";
  const [narrow, setNarrow] = useState(() => window.matchMedia?.(query).matches ?? false);
  useEffect(() => {
    const mq = window.matchMedia?.(query);
    if (!mq) return;
    const on = () => setNarrow(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return narrow;
}

// useRevisionKey names the state of the board topics a resource depends on.
// The component subscribes to that string alone, so a notice for any other
// topic does not render it again. The epoch is part of the key: after a
// server restart the revisions start over, and equal numbers no longer mean
// unchanged data.
function useRevisionKey(topics: readonly string[]): string {
  const board = useContext(BoardContext);
  const names = topics.join(",");
  const getKey = useCallback(() => {
    const { revisions, epoch } = board.getState();
    return revisions ? epoch + "|" + names.split(",").map((t) => (t && revisions[t]) || 0).join(",") : "";
  }, [board, names]);
  return useSyncExternalStore(board.subscribe, getKey);
}

// useTopicsSettled calls fn once the board revisions of topics have stopped
// moving for ms: a burst of notices (a scan announces "runs" on each
// evaluation phase) is one call, after the last. The first snapshot only
// records revisions, as in useResource.
export function useTopicsSettled(topics: readonly string[], ms: number, fn: () => void): void {
  const key = useRevisionKey(topics);
  const seen = useRef(key);
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    if (seen.current === key) return;
    const first = seen.current === "";
    seen.current = key;
    if (first) return;
    const t = setTimeout(() => ref.current(), ms);
    return () => clearTimeout(t);
  }, [key, ms]);
}

export type Resource<T> =
  | { status: "loading"; data?: undefined; error?: undefined; at?: undefined }
  | { status: "ready"; data: T; error?: undefined; at?: number }
  | { status: "error"; data?: T; error: ApiError; at?: number };

export function asApiError(e: unknown): ApiError {
  if (e instanceof ApiError) return e;
  return new ApiError("malformed", e instanceof Error ? e.message : "Unexpected failure.", { requestId: "" });
}

// kept holds the last answer of each load that names itself (`keep`, one
// call site per name: the minified build can give two loads the same code),
// by name and inputs, with the writes sent, its topics' revision key when
// asked (before the first snapshot: as of it) and when it came. See dashboard.md.
const kept = new Map<string, { data: unknown; writes: number; rev: string; at: number }>();
const KEPT_MAX = 200;

export function forgetKept(): void {
  kept.clear();
}

// A burst of notices (one write announces several topics, a working run
// announces each change) is one reload, once the topics have been quiet for
// TOPIC_SETTLE_MS; notices that never pause still reload every
// TOPIC_MAX_WAIT_MS.
const TOPIC_SETTLE_MS = 250;
const TOPIC_MAX_WAIT_MS = 2000;

// useResource loads data and reloads it when the inputs change or when the
// board revision of one of its topics moves. The first snapshot only records
// revisions: a page that has just loaded is already current. A revision that
// moves while the page is hidden waits: the page asks once as it is shown
// again, as useInterval does. A load whose answer shows outside the page (the
// waiting count in the tab's title) follows its topics while hidden too.
export function useResource<T>(load: () => Promise<T>, inputs: readonly unknown[], topics: readonly string[] = [], opts: { keep?: string; whileHidden?: boolean } = {}): [Resource<T>, () => void] {
  const revisionKey = useRevisionKey(topics);
  const visible = usePageVisible() || opts.whileHidden === true;
  const key = opts.keep && opts.keep + JSON.stringify(inputs);
  const usable = () => {
    const k = key ? kept.get(key) : undefined;
    return k && k.writes === writesSent() && k.rev === revisionKey ? { status: "ready" as const, data: k.data as T, at: k.at } : undefined;
  };
  const [res, setRes] = useState<Resource<T>>(() => usable() ?? { status: "loading" });
  const [tick, setTick] = useState(0);
  const seenKey = useRef(revisionKey);
  const loadRef = useRef(load);
  loadRef.current = load;
  const moved = useRef<{ timer: ReturnType<typeof setTimeout>; since: number }>(undefined);

  useEffect(() => {
    if (seenKey.current === "" && revisionKey !== "") {
      seenKey.current = revisionKey;
      const k = key ? kept.get(key) : undefined;
      if (k?.rev === "") k.rev = revisionKey;
    } else if (seenKey.current !== revisionKey && visible) {
      seenKey.current = revisionKey;
      const since = moved.current?.since ?? Date.now();
      clearTimeout(moved.current?.timer);
      const timer = setTimeout(() => setTick((n) => n + 1), Math.min(TOPIC_SETTLE_MS, since + TOPIC_MAX_WAIT_MS - Date.now()));
      moved.current = { timer, since };
    }
  }, [revisionKey, visible]);
  useEffect(() => () => clearTimeout(moved.current?.timer), []);

  const reload = useCallback(() => setTick((n) => n + 1), []);
  const loadedTick = useRef(tick);

  // A load that failed is asked again once the board stream is back (a
  // restarted or reachable-again server), so no card waits for a press.
  const board = useContext(BoardContext);
  const links = useSyncExternalStore(board.subscribe, () => board.getState().links);
  const failed = useRef(false);
  failed.current = res.status === "error";
  const seenLinks = useRef(links);
  useEffect(() => {
    if (seenLinks.current === links) return;
    seenLinks.current = links;
    if (failed.current) reload();
  }, [links, reload]);

  // A reload (a retry, a poll, a topic's revision) keeps a failure on screen
  // until it ends, so its alert is not drawn and announced again on every
  // attempt and the data beside it stays. A first load, or one for new
  // inputs, draws what was kept for them if it is still usable. Any load
  // reads what the notices before it announced, so none is still waiting.
  useEffect(() => {
    let live = true;
    clearTimeout(moved.current?.timer);
    moved.current = undefined;
    const again = loadedTick.current !== tick;
    loadedTick.current = tick;
    const hit = again ? undefined : usable();
    setRes((prev) => (hit ? (prev.status === "ready" && prev.data === hit.data ? prev : hit) : prev.status === "ready" || (again && prev.status === "error") ? prev : { status: "loading" }));
    const [writes, asked] = [writesSent(), revisionKey];
    (hit ? quietly(loadRef.current) : loadRef.current()).then(
      (data) => {
        if (!live) return;
        const at = Date.now();
        if (key) {
          kept.delete(key);
          kept.set(key, { data, writes, rev: asked || seenKey.current, at });
          if (kept.size > KEPT_MAX) kept.delete(kept.keys().next().value!);
        }
        setRes({ status: "ready", data, at });
      },
      (e) => live && setRes((prev) => ({ status: "error", data: prev.data, at: prev.at, error: asApiError(e) })),
    );
    return () => {
      live = false;
    };
  }, [tick, ...inputs]);

  return [res, reload];
}
