// The board stream: one fetch-based reader per tab that keeps the revision of
// every topic. EventSource hides comment lines and the read timeout, so the
// 45 second silence rule needs the stream read by hand.
import { boardNotice, boardSnapshot, parseJSON, streamReset } from "./decode";
import { markSignedOut, withSessionKey } from "./session";

const BOARD_PATH = "/api/board/events";
const SILENCE_MS = 45_000;
const BANNER_MS = 5_000;
const STALE_MS = 60_000;
const BACKOFF_START_MS = 1_000;
const BACKOFF_MAX_MS = 30_000;
const JITTER = 0.2;

export interface BoardState {
  /** Revision of each topic, or null until the first snapshot arrives. */
  revisions: Record<string, number> | null;
  /**
   * Names the server run the revisions belong to. Revisions restart from zero
   * in every run, so a change of epoch means everything must be fetched again.
   */
  epoch: string | null;
  /** True once the connection has been down for 5 seconds. */
  reconnecting: boolean;
  /** True once data may be older than 60 seconds. */
  stale: boolean;
  /** Counts the snapshots, one per connection: a change means the stream is back. */
  links: number;
}

interface SseEvent {
  id: string;
  event: string;
  data: string;
}

// parseSse takes the buffered text and returns the complete events in it plus
// the unfinished remainder. Comment lines (": keep-alive") carry no event.
export function parseSse(buffer: string): { events: SseEvent[]; rest: string } {
  const events: SseEvent[] = [];
  const blocks = buffer.split(/\r?\n\r?\n/);
  const rest = blocks.pop() ?? "";
  for (const block of blocks) {
    let event = "message";
    let id = "";
    const data: string[] = [];
    for (const line of block.split(/\r?\n/)) {
      if (line === "" || line.startsWith(":")) continue;
      const i = line.indexOf(":");
      const field = i < 0 ? line : line.slice(0, i);
      const value = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
      if (field === "id") id = value;
      else if (field === "event") event = value;
      else if (field === "data") data.push(value);
    }
    if (data.length > 0) events.push({ id, event, data: data.join("\n") });
  }
  return { events, rest };
}

export interface Board {
  getState(): BoardState;
  subscribe(listener: () => void): () => void;
  start(): void;
  stop(): void;
  /** Reconnects at once instead of waiting out the backoff. */
  retryNow(): void;
}

// epochOf is the part of an event id before the sequence number.
function epochOf(id: string): string | null {
  const i = id.lastIndexOf("-");
  return i > 0 ? id.slice(0, i) : null;
}

export function createBoard(random: () => number = Math.random): Board {
  let state: BoardState = { revisions: null, epoch: null, reconnecting: false, stale: false, links: 0 };
  const listeners = new Set<() => void>();
  let ctl: AbortController | null = null;
  let running = false;
  let failures = 0;
  let silence: ReturnType<typeof setTimeout> | undefined;
  let retry: ReturnType<typeof setTimeout> | undefined;
  let bannerTimer: ReturnType<typeof setTimeout> | undefined;
  let staleTimer: ReturnType<typeof setTimeout> | undefined;

  const set = (next: Partial<BoardState>) => {
    state = { ...state, ...next };
    listeners.forEach((l) => l());
  };

  const armOutageTimers = () => {
    if (bannerTimer === undefined) bannerTimer = setTimeout(() => set({ reconnecting: true }), BANNER_MS);
    if (staleTimer === undefined) staleTimer = setTimeout(() => set({ stale: true }), STALE_MS);
  };
  const clearOutageTimers = () => {
    clearTimeout(bannerTimer);
    clearTimeout(staleTimer);
    bannerTimer = staleTimer = undefined;
  };

  const handle = (ev: SseEvent) => {
    if (ev.event === "snapshot") {
      const snap = boardSnapshot(parseJSON(ev.data), "$");
      failures = 0;
      clearOutageTimers();
      set({ revisions: { ...snap.revisions }, epoch: epochOf(ev.id) ?? state.epoch, reconnecting: false, stale: false, links: state.links + 1 });
    } else if (ev.event === "notice") {
      const n = boardNotice(parseJSON(ev.data), "$");
      set({ revisions: { ...(state.revisions ?? {}), [n.topic]: n.revision } });
    } else if (ev.event === "reset" || ev.event === "shutdown") {
      if (ev.event === "reset") streamReset(parseJSON(ev.data), "$");
      ctl?.abort();
    }
  };

  const scheduleRetry = () => {
    if (!running) return;
    armOutageTimers();
    const base = Math.min(BACKOFF_MAX_MS, BACKOFF_START_MS * 2 ** failures);
    failures++;
    const delay = base * (1 + (random() * 2 - 1) * JITTER);
    retry = setTimeout(() => {
      retry = undefined;
      void connect();
    }, delay);
  };

  async function connect() {
    if (!running) return;
    ctl = new AbortController();
    const mine = ctl;
    const lostSignal = () => {
      clearTimeout(silence);
      silence = setTimeout(() => mine.abort(), SILENCE_MS);
    };
    try {
      lostSignal();
      const res = await fetch(BOARD_PATH, { headers: withSessionKey({ Accept: "text/event-stream" }), credentials: "same-origin", signal: mine.signal });
      if (res.status === 401) {
        // The session is gone. Retrying cannot bring it back: only a new
        // link can, so the page says so instead of reconnecting forever.
        running = false;
        markSignedOut();
        return;
      }
      if (!res.ok || !res.body) throw new Error("no stream");
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        lostSignal();
        buffer += decoder.decode(value, { stream: true });
        const parsed = parseSse(buffer);
        buffer = parsed.rest;
        parsed.events.forEach(handle);
      }
    } catch {
      // A failed read and a deliberate abort end the same way: reconnect.
    } finally {
      clearTimeout(silence);
    }
    if (running && ctl === mine) scheduleRetry();
  }

  return {
    getState: () => state,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    start() {
      if (running) return;
      running = true;
      addEventListener("pagehide", pause);
      addEventListener("pageshow", resume);
      void connect();
    },
    stop() {
      removeEventListener("pagehide", pause);
      removeEventListener("pageshow", resume);
      halt();
    },
    retryNow() {
      if (!running || retry === undefined) return;
      clearTimeout(retry);
      retry = undefined;
      void connect();
    },
  };

  function halt() {
    running = false;
    ctl?.abort();
    clearTimeout(silence);
    clearTimeout(retry);
    retry = undefined;
    clearOutageTimers();
  }

  // A page kept in the back/forward cache would hold its stream open, and a
  // few such pages use up the browser's connections to this origin, so the
  // next page waits for the silence rule. The stream closes when the page is
  // hidden that way and opens again when it is shown.
  function pause() {
    halt();
  }
  function resume(ev: Event) {
    if (!(ev as PageTransitionEvent).persisted || running) return;
    running = true;
    failures = 0;
    void connect();
  }
}
