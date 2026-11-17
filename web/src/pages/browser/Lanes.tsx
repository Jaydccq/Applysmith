import { ArrowLeft, ArrowRight, ExternalLink, Globe, LoaderCircle, Maximize2, Minimize2, MousePointer2, Plus, RotateCw, X } from "lucide-react";
import {
  useCallback, useEffect, useRef, useState, useSyncExternalStore, type FormEvent, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent, type RefObject,
} from "react";
import { ApiError, fetchBlob, get, post } from "../../api/client";
import { parseSse } from "../../api/board";
import { parseJSON } from "../../api/decode";
import {
  browserLiveCursor, browserLiveEnd, browserLiveFrame, browserLiveTab, browserNewTab, browserShown, browserStatus, browserStopped, browserTabSent,
} from "../../api/decoders_browser";
import { markSignedOut, withSessionKey } from "../../api/session";
import { asApiError, useInterval, usePageVisible, useResource, type Resource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Button } from "../../ui/Button";
import { Chip, Empty, ErrorNote, Panel, humanize, lastRead, type Tone } from "../../ui/parts";
import type { BrowserInputEvent, BrowserLane, BrowserLiveTab, BrowserStatus, BrowserTab } from "../../wire";
import { Link } from "../../router";
import { useAction } from "../action";

// A still is asked for at most once in this time, and only while the page is
// shown.
const SHOT_EVERY_MS = 2000;
const STATUS_EVERY_MS = 5000;

const STATE_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { running: "good", starting: "warn", stopping: "warn", failed: "bad", stopped: "neutral" };

const LANES = [
  { key: "apply", title: "Apply window", note: "Where applications are filled and where you sign in." },
  { key: "scan", title: "Scan window", note: "Where scans and sign-in checks read pages in the background." },
] as const;

// Still shows the current tab of a lane, refreshed every two seconds while the
// page is visible. The route needs the page's credentials, so the image is
// fetched and shown from a blob address.
export function Still({ lane, tab }: { lane: "apply" | "scan"; tab: BrowserTab }) {
  const visible = usePageVisible();
  const [src, setSrc] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const id = tab.id;
  useEffect(() => {
    if (!visible) return;
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      const started = Date.now();
      try {
        const blob = await fetchBlob(`/api/browser/lanes/${lane}/tabs/${encodeURIComponent(id)}/screenshot`);
        if (live) {
          const url = URL.createObjectURL(blob);
          setSrc((prev) => {
            if (prev) URL.revokeObjectURL(prev);
            return url;
          });
          setProblem(null);
        }
      } catch (e) {
        if (live) setProblem(asApiError(e).message);
      }
      if (live) timer = setTimeout(() => void tick(), Math.max(0, SHOT_EVERY_MS - (Date.now() - started)));
    };
    void tick();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [visible, lane, id]);
  useEffect(
    () => () => {
      setSrc((prev) => {
        if (prev) URL.revokeObjectURL(prev);
        return null;
      });
    },
    [],
  );
  return (
    <figure>
      {src && <img className="still" src={src} alt={`Current tab of the ${lane} window`} />}
      {problem && (
        <p role="status" className="muted">
          {src ? "The picture is out of date: " : "No picture: "}
          {problem}
        </p>
      )}
    </figure>
  );
}

// tabName is how a tab reads in a list: a blank page is a new tab with no
// address line, as in Chrome's own tab strip.
function tabName(t: { title: string; url: string }): { title: string; url: string } {
  if (!t.url || t.url.startsWith("about:")) return { title: t.title && !t.title.startsWith("about:") ? t.title : "New tab", url: "" };
  return { title: t.title || shortAddress(t.url), url: t.url };
}

// Who has a tab, as an agent browser's tab strip says it: a run is using it, a
// run stopped and handed it to you, or it is yours. A run's tab comes first.
type Holder = { rank: number; label: string; tone: "good" | "warn" | "neutral" };
export function holder(t: BrowserTab): Holder {
  if (t.owner && !t.kept) return { rank: 0, label: "Applysmith is using it", tone: "good" };
  if (t.owner) return { rank: 1, label: "Waiting for you", tone: "warn" };
  return { rank: 2, label: "Yours", tone: "neutral" };
}

function byHolder(tabs: BrowserTab[]): BrowserTab[] {
  return tabs.map((t, i) => ({ t, i, r: holder(t).rank })).sort((a, b) => a.r - b.r || a.i - b.i).map((x) => x.t);
}

// TabList names each tab's holder. An apply tab no run is using can be brought
// to the front; a tab a run is using is left to the run.
function TabList({ laneKey, tabs, onChanged }: { laneKey: "apply" | "scan"; tabs: BrowserTab[]; onChanged: () => void }) {
  const show = useAction();
  if (tabs.length === 0) return <Empty>No tabs.</Empty>;
  const doShow = async (tabId: string) => {
    if (await show.run(() => post("/api/browser/apply-window/show", browserShown, { tabId }))) onChanged();
  };
  return (
    <>
      <ul className="plain stack lane-tabs">
        {byHolder(tabs).map((t) => {
          const h = holder(t);
          const name = tabName(t);
          return (
            <li key={t.id} aria-label={name.title}>
              <div className="lane-tab-name">
                <strong>{name.title}</strong> {name.url && <span className="muted lane-tab-url" title={name.url}>{name.url}</span>}
              </div>
              <div className="lane-tab-state">
                <Chip tone={h.tone}>{h.label}</Chip> {t.active && <Chip>Active</Chip>} {t.loading && <Chip tone="warn">Loading</Chip>}{" "}
                {t.owner && <Link to={`/runs/${encodeURIComponent(t.owner)}`}>{h.rank === 0 ? "See the run" : "What the run did"}</Link>}
                {/* The two ways to show a tab wrap together, never one beside the chips and one under them. */}
                {laneKey === "apply" && (
                  <span className="row-actions-wrap">
                    <button type="button" className="small" onClick={() => showInPanel(t.id)}>
                      Show in panel
                    </button>
                    {h.rank > 0 && (
                      <button type="button" className="small" disabled={show.busy} onClick={() => void doShow(t.id)}>
                        Show in Chrome
                      </button>
                    )}
                  </span>
                )}
              </div>
            </li>
          );
        })}
      </ul>
      {show.error && <ErrorNote error={show.error} />}
    </>
  );
}

function LaneCard({ laneKey, title, note, lane, running, onChanged }: { laneKey: "apply" | "scan"; title: string; note: string; lane: BrowserLane; running: boolean; onChanged: () => void }) {
  // The picture follows the tab a run is using, so you see what it is doing.
  const current = lane.tabs.find((t) => holder(t).rank === 0) ?? lane.tabs.find((t) => t.active) ?? lane.tabs[0];
  // The lane's state is its busiest tab's: a run working, a tab waiting for you, or idle.
  const rank = Math.min(3, ...lane.tabs.map((t) => holder(t).rank));
  const [word, tone]: [string, Tone] = !(running && lane.window) ? ["Closed", "neutral"] : rank === 0 ? ["A run is using it", "good"] : rank === 1 ? ["A tab waits for you", "warn"] : ["Idle", "neutral"];
  return (
    <section aria-label={title} className="panel">
      <header className="panel-head">
        <h3>{title}</h3>
        <Chip tone={tone}>{word}</Chip>
      </header>
      <p className="muted">{note}</p>
      <p>
        {lane.tabs.length} {lane.tabs.length === 1 ? "tab" : "tabs"} <span className="muted">(up to {lane.tabCap})</span>
      </p>
      <TabList laneKey={laneKey} tabs={lane.tabs} onChanged={onChanged} />
      {current && <Still lane={laneKey} tab={current} />}
    </section>
  );
}

function Controls({ status, onChanged }: { status: BrowserStatus; onChanged: () => void }) {
  const show = useAction();
  const stop = useAction();
  const [asking, setAsking] = useState(false);
  const [outcome, setOutcome] = useState<string | null>(null);
  const running = status.state === "running";

  const doShow = async () => {
    setOutcome(null);
    if (await show.run(() => post("/api/browser/apply-window/show", browserShown, {}))) onChanged();
  };
  const doStop = async (force: boolean) => {
    setAsking(false);
    setOutcome(null);
    const done = await stop.run(() => post("/api/browser/stop", browserStopped, force ? { force: true } : {}));
    if (done) {
      setOutcome(done.outcome === "not-running" ? "The browser was not running." : `The browser stopped (${done.outcome}).`);
      onChanged();
    }
  };
  // A stop that a run refused offers the forced one in the same place.
  const leased = stop.error instanceof ApiError && stop.error.details?.reason === "lane-leased";

  return (
    <div>
      <div className="actions">
        <button type="button" disabled={show.busy} onClick={() => void doShow()}>
          Show apply window
        </button>
        <button type="button" disabled={stop.busy || !running} onClick={() => setAsking(true)}>
          Stop browser
        </button>
        {leased && (
          <button type="button" className="danger" disabled={stop.busy} onClick={() => void doStop(true)}>
            Stop anyway
          </button>
        )}
      </div>
      {show.error && <ErrorNote error={show.error} />}
      {stop.error && <ErrorNote error={stop.error} />}
      <div role="status">{outcome && <p className="note ok">{outcome}</p>}</div>
      {asking && (
        <ConfirmDialog
          title="Stop the browser"
          consequence="Chrome closes with every tab in both windows. A run that holds a tab is refused unless you stop anyway."
          verb="Stop browser"
          onCancel={() => setAsking(false)}
          onConfirm={() => void doStop(false)}
        />
      )}
    </div>
  );
}

// useBrowserStatus reads the browser's state and tabs, again every five
// seconds while the page is shown.
export function useBrowserStatus(): [Resource<BrowserStatus>, () => void] {
  const [res, reload] = useResource(() => get("/api/browser/status", browserStatus), [], ["browser"]);
  useInterval(reload, STATUS_EVERY_MS, true);
  return [res, reload];
}

export function LanesPanel({ res, reload }: { res: Resource<BrowserStatus>; reload: () => void }) {
  return (
    <Panel title="Browser windows" id="browser-lanes" res={res} retry={reload}>
      {(s) => (
        <>
          <p>
            <Chip tone={STATE_TONE[s.state] ?? "neutral"}>{humanize(s.state)}</Chip> {s.chromeVersion && <span className="muted">{s.chromeVersion}</span>}
            {s.state === "stopped" && <span className="muted"> Chrome starts when a run, a sign-in check or Show apply window needs it.</span>}
          </p>
          {s.error && (
            <p role="alert" className="note error">
              {s.error}
            </p>
          )}
          <Controls status={s} onChanged={reload} />
          <div className="grid">
            {LANES.map((l) => (
              <LaneCard key={l.key} laneKey={l.key} title={l.title} note={l.note} lane={s[l.key]} running={s.state === "running"} onChanged={reload} />
            ))}
          </div>
        </>
      )}
    </Panel>
  );
}

// ---------------------------------------------------------------------------
// The browser panel: the apply window's tabs beside every page, shown live.
// The pages stay in Chrome; the panel shows a live picture of the chosen tab
// (GET .../live) and sends the user's own clicks, keys and text to it
// (POST .../input). A tab a run drives is only watched. Nothing here presses
// anything on the user's behalf.

interface PanelState {
  open: boolean;
  /** The width the user dragged to, in CSS pixels; the layout may narrow it. */
  width: number;
  full: boolean;
  tabId: string | null;
}

const PANEL_KEY = "applysmith-browser-panel";
const PANEL_MIN = 320;
const PANEL_DEFAULT = 520;
const PANEL_STATUS_MS = 3000;

function readPanel(): PanelState {
  const fresh: PanelState = { open: false, width: PANEL_DEFAULT, full: false, tabId: null };
  try {
    const saved = JSON.parse(window.localStorage.getItem(PANEL_KEY) ?? "null") as Partial<PanelState> | null;
    if (!saved || typeof saved !== "object") return fresh;
    return { ...fresh, open: saved.open === true, full: saved.full === true, width: typeof saved.width === "number" && saved.width >= PANEL_MIN ? Math.round(saved.width) : PANEL_DEFAULT };
  } catch {
    return fresh;
  }
}

let panel: PanelState | null = null;
const panelListeners = new Set<() => void>();

function panelState(): PanelState {
  panel ??= readPanel();
  return panel;
}

// setPanel changes the panel and remembers whether it is open, its width and
// full width. The tab shown is not remembered: tab ids do not outlive Chrome.
export function setPanel(change: Partial<PanelState>): void {
  panel = { ...panelState(), ...change };
  try {
    window.localStorage.setItem(PANEL_KEY, JSON.stringify({ open: panel.open, width: panel.width, full: panel.full }));
  } catch {
    // A blocked store only forgets the layout.
  }
  panelListeners.forEach((l) => l());
}

export function togglePanel(): void {
  setPanel({ open: !panelState().open });
}

export function showInPanel(tabId: string): void {
  setPanel({ open: true, tabId });
}

// ShowInPanel opens the browser panel on the tab a run kept for you, beside
// the page you are on (the deep link /browser?target=<id> does the same).
export function ShowInPanel({ tabId, small }: { tabId: string; small?: boolean }) {
  return (
    <button type="button" className={small ? "small show-panel" : "show-panel"} onClick={() => showInPanel(tabId)}>
      Show it in the browser panel
    </button>
  );
}

// resetPanel forgets the module's copy, for tests.
export function resetPanel(): void {
  panel = null;
  panelListeners.forEach((l) => l());
}

export function usePanel(): PanelState {
  return useSyncExternalStore(
    (l) => {
      panelListeners.add(l);
      return () => panelListeners.delete(l);
    },
    panelState,
  );
}

// toAddress reads what was typed in the address field: an http or https
// address, or a host name that gets https. Anything else is not an address.
export function toAddress(typed: string): string | null {
  const s = typed.trim();
  if (/^https?:\/\/[^\s]+$/i.test(s)) return s;
  if (/^[a-z0-9-]+(\.[a-z0-9-]+)+(:\d+)?([/?#]\S*)?$/i.test(s)) return `https://${s}`;
  return null;
}

// shortAddress is the address as the toolbar shows it at rest: no scheme, no
// "www.", no query or fragment and no trailing slash. The full address is its
// title and what the field holds while editing.
export function shortAddress(url: string): string {
  if (!url || url.startsWith("about:")) return "";
  return url.replace(/^https?:\/\//i, "").replace(/^www\./i, "").replace(/[?#].*$/, "").replace(/\/$/, "");
}

interface FrameSize {
  width: number;
  height: number;
}

interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}

// The frame is drawn whole inside the viewport, scaled down to fit and
// centred across, top-aligned: the page starts at the top as it does in a
// browser.
function fit(box: Box, frame: FrameSize): { scale: number; x: number } {
  const scale = Math.min(box.width / frame.width, box.height / frame.height);
  return { scale, x: (box.width - frame.width * scale) / 2 };
}

// toPagePoint maps a pointer position in the window to the page's CSS pixels,
// or null for a point outside the drawn frame.
function toPagePoint(clientX: number, clientY: number, box: Box, frame: FrameSize): { x: number; y: number } | null {
  if (frame.width <= 0 || frame.height <= 0 || box.width <= 0 || box.height <= 0) return null;
  const { scale, x } = fit(box, frame);
  const px = (clientX - box.left - x) / scale;
  const py = (clientY - box.top) / scale;
  if (px < 0 || py < 0 || px > frame.width || py > frame.height) return null;
  return { x: Math.round(px * 100) / 100, y: Math.round(py * 100) / 100 };
}

// fromPagePoint is the inverse, relative to the viewport: where the agent
// cursor is drawn.
function fromPagePoint(x: number, y: number, box: Box, frame: FrameSize): { left: number; top: number } {
  if (frame.width <= 0 || frame.height <= 0) return { left: 0, top: 0 };
  const fitted = fit(box, frame);
  return { left: fitted.x + x * fitted.scale, top: y * fitted.scale };
}

type Modded = { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean };

function modifiersOf(e: Modded): number {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
}

// KEYS edit, move or press, so they reach the page as keys; the others type
// into the hidden field, whose text is sent as text.
const KEYS = new Set(["Enter", "Backspace", "Delete", "Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", " "]);

// keyInput maps a key to the event the page gets, or null for a key down the
// field types. A key with Ctrl or Cmd is a shortcut and types nothing.
function keyInput(e: Modded & { key: string; code: string; keyCode: number }, type: "down" | "up"): BrowserInputEvent | null {
  const mod = e.ctrlKey || e.metaKey;
  if (type === "down" && !mod && !KEYS.has(e.key)) return null;
  const ev: BrowserInputEvent = { kind: "key", type, key: e.key, code: e.code, keyCode: e.keyCode || 0, modifiers: modifiersOf(e) };
  if (type === "down" && !mod && (e.key === "Enter" || e.key === " ")) ev.text = e.key === " " ? " " : "\r";
  return ev;
}

const BUTTONS = ["left", "middle", "right"] as const;

// InputSender posts the user's events in order, one request at a time; what
// arrives meanwhile waits and goes in the next request. Consecutive pointer
// moves keep only the newest, and hover moves are sent at most every 50 ms,
// so typing and moving stay inside the route's rate. A request the tab takes
// after a failed one clears that failure.
class InputSender {
  private queue: BrowserInputEvent[] = [];
  private busy = false;
  private lastHover = 0;
  constructor(
    private readonly tabId: string,
    private readonly onFail: (e: ApiError) => void,
    private readonly onSent: () => void,
  ) {}

  push(ev: BrowserInputEvent): void {
    if (ev.kind === "mouse" && ev.type === "move") {
      const now = Date.now();
      if (!ev.buttons && now - this.lastHover < 50) return;
      if (!ev.buttons) this.lastHover = now;
      const last = this.queue[this.queue.length - 1];
      if (last?.kind === "mouse" && last.type === "move" && last.buttons === ev.buttons) this.queue.pop();
    }
    this.queue.push(ev);
    void this.flush();
  }

  private async flush(): Promise<void> {
    if (this.busy || this.queue.length === 0) return;
    this.busy = true;
    const events = this.queue.splice(0, 64);
    try {
      await post(`/api/browser/lanes/apply/tabs/${encodeURIComponent(this.tabId)}/input`, browserTabSent, { events });
      this.onSent();
    } catch (e) {
      this.queue = [];
      this.onFail(asApiError(e));
    } finally {
      this.busy = false;
    }
    void this.flush();
  }
}

type LivePhase = "connecting" | "streaming" | "error" | "ended";

interface LiveHandlers {
  onFrame: (jpeg: string, size: FrameSize) => void;
  onTab: (tab: BrowserLiveTab) => void;
  onCursor: (x: number, y: number) => void;
  onFileChooser: () => void;
  onEnd: (reason: string) => void;
  onError: (message: string) => void;
}

// openLive reads the live view of one tab with fetch, so the session key
// travels with it. It reports a dropped stream as an error; the caller offers
// Try again.
function openLive(tabId: string, size: FrameSize, h: LiveHandlers): () => void {
  const ctl = new AbortController();
  void (async () => {
    try {
      const q = new URLSearchParams({ maxWidth: String(Math.round(size.width)), maxHeight: String(Math.round(size.height)) });
      const res = await fetch(`/api/browser/lanes/apply/tabs/${encodeURIComponent(tabId)}/live?${q}`, {
        headers: withSessionKey({ Accept: "text/event-stream" }), credentials: "same-origin", signal: ctl.signal,
      });
      if (res.status === 401) markSignedOut();
      if (!res.ok || !res.body) {
        let message = "The tab could not be shown.";
        try {
          message = (JSON.parse(await res.text()) as { error?: { message?: string } }).error?.message ?? message;
        } catch {
          // The status line is all there is.
        }
        return h.onError(message);
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) return h.onError("The live view stopped.");
        buffer += decoder.decode(value, { stream: true });
        const parsed = parseSse(buffer);
        buffer = parsed.rest;
        for (const ev of parsed.events) {
          const data = parseJSON(ev.data);
          if (ev.event === "frame") {
            const f = browserLiveFrame(data, "$");
            h.onFrame(f.jpeg, { width: f.width, height: f.height });
          } else if (ev.event === "tab") h.onTab(browserLiveTab(data, "$"));
          else if (ev.event === "cursor") {
            const c = browserLiveCursor(data, "$");
            h.onCursor(c.x, c.y);
          } else if (ev.event === "filechooser") h.onFileChooser();
          else if (ev.event === "end") return h.onEnd(browserLiveEnd(data, "$").reason);
        }
      }
    } catch (e) {
      if (!ctl.signal.aborted) h.onError(e instanceof Error ? e.message : "The live view stopped.");
    }
  })();
  return () => ctl.abort();
}

const END_TEXT: Record<string, string> = { "tab-closed": "This tab was closed.", "browser-stopped": "The browser stopped." };

interface LiveView {
  phase: LivePhase;
  error: string;
  end: string;
  tab: BrowserLiveTab | null;
  frame: FrameSize | null;
  cursor: { x: number; y: number } | null;
  chooser: number;
  retry: () => void;
}

// useLive keeps one live view open for the tab shown. Frames go straight to
// the image element, so a frame costs no render; the state changes only when
// the phase, the frame size or the toolbar state does.
function useLive(tabId: string | null, img: RefObject<HTMLImageElement | null>, view: RefObject<HTMLElement | null>): LiveView {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<Omit<LiveView, "retry">>({ phase: "connecting", error: "", end: "", tab: null, frame: null, cursor: null, chooser: 0 });
  const visible = usePageVisible();
  useEffect(() => {
    if (!tabId || !visible) return;
    setState({ phase: "connecting", error: "", end: "", tab: null, frame: null, cursor: null, chooser: 0 });
    const r = view.current?.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    const size = { width: Math.min(2400, Math.max(320, (r?.width || 800) * dpr)), height: Math.min(2400, Math.max(240, (r?.height || 600) * dpr)) };
    return openLive(tabId, size, {
      onFrame: (jpeg, f) => {
        if (img.current) img.current.src = `data:image/jpeg;base64,${jpeg}`;
        setState((s) => (s.phase === "streaming" && s.frame?.width === f.width && s.frame?.height === f.height ? s : { ...s, phase: "streaming", frame: f }));
      },
      onTab: (tab) => setState((s) => ({ ...s, tab, cursor: tab.driven ? s.cursor : null })),
      onCursor: (x, y) => setState((s) => ({ ...s, cursor: { x, y } })),
      onFileChooser: () => setState((s) => ({ ...s, chooser: s.chooser + 1 })),
      onEnd: (reason) => setState((s) => ({ ...s, phase: "ended", end: END_TEXT[reason] ?? "The live view ended." })),
      onError: (error) => setState((s) => ({ ...s, phase: "error", error })),
    });
  }, [tabId, attempt, visible, img, view]);
  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  return { ...state, retry };
}

// Viewport is the live picture of one tab. A tab the user holds is inert until
// a click activates it ("Click anywhere to use the browser"); then the
// pointer, the wheel and the keyboard go to the page until Escape or focus
// leaves. Keys go through a hidden text field, so an input method composes
// there and only the finished text is inserted.
// A tab a run drives is never activated.
function Viewport({ tab, live, img, view, driven, runId, title, onAddress }: {
  onAddress: () => void;
  tab: BrowserTab;
  live: LiveView;
  img: RefObject<HTMLImageElement | null>;
  view: RefObject<HTMLDivElement | null>;
  driven: boolean;
  runId: string;
  title: string;
}) {
  const [active, setActive] = useState(false);
  const [problem, setProblem] = useState("");
  const [chooserSeen, setChooserSeen] = useState(0);
  const ime = useRef<HTMLTextAreaElement>(null);
  const composing = useRef(false);
  const pressed = useRef(false);
  const down = useRef(new Set<string>());
  const sender = useRef<InputSender | null>(null);
  const showWindow = useAction();

  useEffect(() => {
    sender.current = new InputSender(tab.id, (e) => setProblem(e.details?.reason === "tab-driven" ? "" : e.message), () => setProblem(""));
    setActive(false);
    setProblem("");
  }, [tab.id]);
  useEffect(() => {
    if (driven) setActive(false);
  }, [driven]);
  useEffect(() => {
    if (active) ime.current?.focus({ preventScroll: true });
  }, [active]);

  const frame = live.frame;
  const point = (clientX: number, clientY: number) => {
    const box = view.current?.getBoundingClientRect();
    return box && frame ? toPagePoint(clientX, clientY, box, frame) : null;
  };
  const send = (ev: BrowserInputEvent) => sender.current?.push(ev);

  // Pointer events after a press follow the pointer outside the viewport
  // until it is released, as a drag does in a browser.
  const mouse = (type: "move" | "down" | "up", e: MouseEvent | ReactMouseEvent) => {
    const p = point(e.clientX, e.clientY);
    if (!p && type !== "up") return;
    send({ kind: "mouse", type, x: p?.x ?? 0, y: p?.y ?? 0, button: type === "move" ? "none" : BUTTONS[e.button] ?? "left", buttons: e.buttons, clickCount: type === "move" ? 0 : Math.max(1, e.detail), modifiers: modifiersOf(e) });
  };
  const onMouseDown = (e: ReactMouseEvent) => {
    if (!active) return;
    e.preventDefault();
    ime.current?.focus({ preventScroll: true });
    pressed.current = true;
    mouse("down", e);
    const move = (m: MouseEvent) => mouse("move", m);
    const up = (u: MouseEvent) => {
      pressed.current = false;
      mouse("up", u);
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };

  // React listens to the wheel passively. Every wheel over the picture is
  // cancelled, so the page behind never scrolls; only an active one is sent.
  useEffect(() => {
    const el = view.current;
    if (!el) return;
    const on = (e: WheelEvent) => {
      e.preventDefault();
      const p = active && point(e.clientX, e.clientY);
      if (!p) return;
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? (frame?.height ?? 800) : 1;
      send({ kind: "wheel", x: p.x, y: p.y, deltaX: e.deltaX * unit, deltaY: e.deltaY * unit, modifiers: modifiersOf(e) });
    };
    el.addEventListener("wheel", on, { passive: false });
    return () => el.removeEventListener("wheel", on);
  });

  // The user's tab is fitted: the server sizes its window to the panel. The
  // fit goes before the first frame too, as a window that has not painted
  // yet paints once resized.
  useEffect(() => {
    const el = view.current;
    if (!el || driven) return;
    let timer = 0;
    const fit = () => {
      clearTimeout(timer);
      timer = window.setTimeout(() => {
        const r = el.getBoundingClientRect();
        if (r.width > 0 && r.height > 0) send({ kind: "fit", x: Math.round(r.width), y: Math.round(r.height) });
      }, 300);
    };
    const ro = new ResizeObserver(fit);
    ro.observe(el);
    fit();
    return () => (clearTimeout(timer), ro.disconnect());
  }, [driven, view]);

  // Every key belongs to the page while the viewport is active: none reaches
  // the dashboard's shortcuts, Escape included, which only deactivates.
  const onKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    e.stopPropagation();
    if (e.nativeEvent.isComposing || e.keyCode === 229 || composing.current) return;
    if (e.key === "Escape") {
      e.preventDefault();
      setActive(false);
      view.current?.focus({ preventScroll: true });
      return;
    }
    const mod = e.metaKey || e.ctrlKey;
    const k = e.key.toLowerCase();
    if (mod && k === "l") {
      e.preventDefault();
      onAddress();
      return;
    }
    // Paste lands in the field.
    const ev = mod && k === "v" ? null : keyInput(e, "down");
    if (!ev) return;
    e.preventDefault();
    down.current.add(e.code);
    send(ev);
  };
  const onKeyUp = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    e.stopPropagation();
    if (down.current.delete(e.code)) send(keyInput(e, "up")!);
  };
  // The field is the source of text: what it holds while no composition runs
  // is sent and cleared at once, so a second read finds nothing to send. Its
  // own listeners read it, not only React's. compositionend and beforeinput
  // read it a task later: WebKit fires compositionend while the field still
  // holds the marked text and inserts the committed text after it, at times a
  // task later still, so a compositionend whose text the field does not hold
  // yet leaves the send to the commit's input. An input or key up that no
  // composition holds ends one whose compositionend never came, and a new
  // field or a blur never inherits a composition.
  useEffect(() => {
    const el = ime.current;
    if (!active || !el) return;
    composing.current = false;
    const flush = () => {
      if (composing.current || !el.value) return;
      send({ kind: "text", text: el.value });
      el.value = "";
    };
    const later = () => setTimeout(flush);
    const held = (e: Event) => (e as InputEvent | KeyboardEvent).isComposing || (e as InputEvent).inputType === "insertCompositionText";
    const on: [string, (e: Event) => void][] = [
      ["compositionstart", () => (composing.current = true)],
      ["compositionend", (e) => {
        composing.current = false;
        const data = (e as CompositionEvent).data;
        setTimeout(() => (!data || el.value === data) && flush());
      }],
      ["beforeinput", (e) => held(e) || later()],
      ["input", (e) => held(e) || ((composing.current = false), flush())],
      ["keyup", (e) => held(e) || (e as KeyboardEvent).keyCode === 229 || ((composing.current = false), flush())],
      ["blur", () => ((composing.current = false), flush())],
    ];
    on.forEach(([name, fn]) => el.addEventListener(name, fn));
    return () => on.forEach(([name, fn]) => el.removeEventListener(name, fn));
  }, [active]);

  const pill = !driven && !active && live.phase === "streaming";
  const cursor = driven && live.cursor && frame && view.current ? fromPagePoint(live.cursor.x, live.cursor.y, view.current.getBoundingClientRect(), frame) : null;
  const chooser = live.chooser > chooserSeen;
  return (
    <div
      ref={view}
      id="bp-view"
      role="tabpanel"
      tabIndex={-1}
      aria-label={`Live browser: ${title}`}
      className={`bp-view${driven ? " driven" : ""}${active ? " active" : ""}`}
      onMouseDown={onMouseDown}
      onMouseMove={(e) => active && !pressed.current && mouse("move", e)}
      onContextMenu={(e) => active && e.preventDefault()}
    >
      <img ref={img} className="bp-frame" alt="" hidden={live.phase !== "streaming" && live.phase !== "ended"} draggable={false} />
      {live.phase === "connecting" && (
        <p role="status" className="bp-state">
          <LoaderCircle className="spin" aria-hidden="true" /> Connecting to the tab
        </p>
      )}
      {live.phase === "error" && (
        <div className="bp-state">
          <p role="alert">{live.error}</p>
          <button type="button" onClick={live.retry}>
            Try again
          </button>
        </div>
      )}
      {live.phase === "ended" && (
        <p role="status" className="bp-state bp-ended">
          {live.end}
        </p>
      )}
      {pill && (
        <button type="button" className="bp-activate" onClick={() => setActive(true)}>
          <span className="bp-pill">Click anywhere to use the browser</span>
        </button>
      )}
      {active && (
        <textarea
          ref={ime}
          className="bp-ime"
          aria-label={`Type into ${title}`}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onKeyDown={onKeyDown}
          onKeyUp={onKeyUp}
          onBlur={(e) => {
            if (!view.current?.contains(e.relatedTarget as Node | null)) setActive(false);
          }}
        />
      )}
      {cursor && (
        <span className="bp-cursor" aria-hidden="true" style={{ transform: `translate(${cursor.left}px, ${cursor.top}px)` }}>
          <MousePointer2 />
        </span>
      )}
      {/* A run that stopped kept this tab for you; its page offers Done and what comes next. */}
      {!driven && holder(tab).rank === 1 && !chooser && (
        <div className="bp-island bp-kept" role="note" onMouseDown={(e) => e.stopPropagation()}>
          A run stopped here and left this tab to you
          <Link to={`/runs/${encodeURIComponent(tab.owner ?? "")}`}>What the run did</Link>
        </div>
      )}
      {driven && (
        <div className="bp-island" role="status">
          <span className="bp-island-dot" aria-hidden="true" />
          Applysmith is using this tab
          {runId && <Link to={`/runs/${encodeURIComponent(runId)}`}>See the run</Link>}
        </div>
      )}
      {chooser && !driven && (
        <div className="bp-note" role="alert">
          <p>This page asks for a file. Files cannot be chosen in the panel; choose it in the Chrome window.</p>
          <div className="actions">
            <button type="button" disabled={showWindow.busy} onClick={() => void showWindow.run(() => post("/api/browser/apply-window/show", browserShown, { tabId: tab.id }))}>
              Open in Chrome window
            </button>
            <Button variant="ghost" size="sm" icon={X} className="icon-only" aria-label="Dismiss" onClick={() => setChooserSeen(live.chooser)} />
          </div>
        </div>
      )}
      {problem && (
        <p role="status" className="bp-problem">
          {problem}
        </p>
      )}
    </div>
  );
}

// Address is the toolbar's address: shortened and centred at rest, the full
// address while editing. Enter goes, Escape puts it back.
function Address({ url, onGo, field, disabled }: { url: string; onGo: (url: string) => void; field: RefObject<HTMLInputElement | null>; disabled: boolean }) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [bad, setBad] = useState(false);
  useEffect(() => {
    if (!editing) setValue(url.startsWith("about:") ? "" : url);
  }, [url, editing]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const to = toAddress(value);
    if (!to) {
      setBad(true);
      return;
    }
    setBad(false);
    setEditing(false);
    field.current?.blur();
    onGo(to);
  };
  return (
    <form className="bp-address" onSubmit={submit} role="search" aria-label="Address">
      <input
        ref={field}
        type="text"
        inputMode="url"
        aria-label="Address"
        aria-invalid={bad || undefined}
        placeholder="Enter a web address"
        title={url}
        disabled={disabled}
        value={editing ? value : shortAddress(url)}
        onFocus={(e) => {
          setEditing(true);
          setValue(url.startsWith("about:") ? "" : url);
          const el = e.currentTarget;
          requestAnimationFrame(() => el.select());
        }}
        onBlur={() => {
          setEditing(false);
          setBad(false);
        }}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            setEditing(false);
            setBad(false);
            e.currentTarget.blur();
          }
        }}
      />
    </form>
  );
}

// StartBrowsing is the panel with no tab to show: an address opens one.
function StartBrowsing({ onOpened, failure }: { onOpened: (tabId: string) => void; failure?: string }) {
  const open = useAction();
  const [value, setValue] = useState("");
  const [bad, setBad] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const url = value.trim() === "" ? "" : toAddress(value);
    setBad(url === null);
    if (url === null) return;
    const got = await open.run(() => post("/api/browser/lanes/apply/tabs", browserNewTab, url ? { url } : {}));
    if (got) onOpened(got.tabId);
  };
  return (
    <div className="bp-empty">
      <Globe aria-hidden="true" />
      <h3>Start browsing</h3>
      <p className="muted">Enter a web address to open a page in the apply window. You sign in to sites here, and runs use what you sign in to.</p>
      {failure && (
        <p role="alert" className="note error sentence">
          The browser failed: {failure}
        </p>
      )}
      <form onSubmit={(e) => void submit(e)} className="bp-start">
        <input type="text" inputMode="url" aria-label="Web address" aria-invalid={bad || undefined} placeholder="Enter a web address" value={value} onChange={(e) => setValue(e.target.value)} />
        <button type="submit" disabled={open.busy}>
          Open
        </button>
      </form>
      {bad && (
        <p role="alert" className="note error">
          Enter an http or https address, such as jobs.example.com.
        </p>
      )}
      {open.error && <ErrorNote error={open.error} />}
    </div>
  );
}

function pickTab(tabs: BrowserTab[], wanted: string | null): BrowserTab | undefined {
  return tabs.find((t) => t.id === wanted) ?? tabs.find((t) => holder(t).rank === 0) ?? tabs.find((t) => t.active) ?? tabs[0];
}

// PanelTabs is the strip of the apply window's tabs. While a run drives a tab
// its icon is a cursor and the tab is highlighted; a tab a run handed over is
// marked as waiting for you.
function PanelTabs({ tabs, current, onPick, onNew, busy }: { tabs: BrowserTab[]; current: string | undefined; onPick: (id: string) => void; onNew: () => void; busy: boolean }) {
  // One tab stop for the strip: the arrow keys, Home and End move between
  // tabs, and Enter or Space shows the focused one.
  const move = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const all = [...e.currentTarget.querySelectorAll<HTMLButtonElement>("[role=tab]")];
    const at = all.indexOf(document.activeElement as HTMLButtonElement);
    const to = { ArrowRight: at + 1, ArrowLeft: at - 1, Home: 0, End: all.length - 1 }[e.key];
    if (to === undefined || at < 0) return;
    e.preventDefault();
    all[(to + all.length) % all.length]?.focus();
  };
  const stop = tabs.some((t) => t.id === current) ? current : tabs[0]?.id;
  return (
    <div className="bp-tabs">
      <div role="tablist" aria-label="Apply window tabs" className="bp-tablist" onKeyDown={move}>
        {tabs.map((t) => {
          const h = holder(t);
          const name = tabName(t);
          const Icon = h.rank === 0 ? MousePointer2 : t.loading ? LoaderCircle : Globe;
          return (
            <button
              key={t.id}
              type="button"
              role="tab"
              aria-selected={t.id === current}
              tabIndex={t.id === stop ? 0 : -1}
              aria-controls="bp-view"
              aria-label={`${name.title}, ${h.label}`}
              title={name.url || name.title}
              className={`bp-tab holder-${h.rank}`}
              onClick={() => onPick(t.id)}
            >
              <Icon className={t.loading && h.rank !== 0 ? "spin" : undefined} aria-hidden="true" />
              <span className="bp-tab-title">{name.title}</span>
              {h.rank === 1 && <span className="bp-tab-dot" aria-hidden="true" />}
            </button>
          );
        })}
      </div>
      <Button variant="ghost" size="sm" icon={Plus} className="icon-only" aria-label="New tab" title="New tab" disabled={busy} onClick={onNew} />
    </div>
  );
}

// BrowserPanel is docked beside every page while open. Its width follows a
// drag of its left edge (or the arrow keys on that edge) and is remembered.
export function BrowserPanel() {
  const state = usePanel();
  if (!state.open) return null;
  return <PanelBody state={state} />;
}

function PanelBody({ state }: { state: PanelState }) {
  const [res, reload] = useResource(() => get("/api/browser/status", browserStatus), [], ["browser"]);
  useInterval(reload, PANEL_STATUS_MS, true);
  const img = useRef<HTMLImageElement>(null);
  const view = useRef<HTMLDivElement>(null);
  const field = useRef<HTMLInputElement>(null);
  const aside = useRef<HTMLElement>(null);
  const action = useAction();
  const tabs = res.data?.apply.tabs ?? [];
  const tab = pickTab(tabs, state.tabId);
  const live = useLive(tab?.id ?? null, img, view);
  const name = tab ? tabName(live.tab ? { title: live.tab.title, url: live.tab.url } : tab) : null;
  const driven = live.tab ? live.tab.driven : tab ? holder(tab).rank === 0 : false;
  const runId = live.tab?.runId ?? (tab && holder(tab).rank === 0 ? (tab.owner ?? "") : "");

  const navigateTo = (body: { action: string; url?: string }) => {
    if (!tab) return;
    void action.run(() => post(`/api/browser/lanes/apply/tabs/${encodeURIComponent(tab.id)}/navigate`, browserTabSent, body));
  };
  const newTab = async () => {
    const got = await action.run(() => post("/api/browser/lanes/apply/tabs", browserNewTab, {}));
    if (got) {
      setPanel({ tabId: got.tabId });
      reload();
    }
  };

  const onKeyDown = (e: ReactKeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "l" && tab && !driven) {
      e.preventDefault();
      field.current?.focus();
    }
  };

  const startDrag = (e: ReactPointerEvent) => {
    e.preventDefault();
    const move = (m: PointerEvent) => setPanel({ width: Math.max(PANEL_MIN, Math.round(window.innerWidth - m.clientX)) });
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  const keyDrag = (e: ReactKeyboardEvent) => {
    const step = e.shiftKey ? 64 : 16;
    if (e.key === "ArrowLeft") setPanel({ width: state.width + step });
    else if (e.key === "ArrowRight") setPanel({ width: Math.max(PANEL_MIN, state.width - step) });
    else return;
    e.preventDefault();
  };

  const loading = live.tab?.loading ?? tab?.loading ?? false;
  const empty = res.status !== "loading" && (!res.data || res.data.state !== "running" || tabs.length === 0);
  // A read that failed keeps the tabs last read, greyed under when that was.
  const stale = res.status === "error" && res.data !== undefined;
  return (
    <aside ref={aside} className="browser-panel" aria-label="Browser" onKeyDown={onKeyDown}>
      <div
        className="bp-resize"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize the browser panel"
        aria-valuenow={state.width}
        aria-valuemin={PANEL_MIN}
        tabIndex={0}
        onPointerDown={startDrag}
        onKeyDown={keyDrag}
      />
      <header className={stale ? "bp-head stale-data" : "bp-head"}>
        {tabs.length > 0 ? <PanelTabs tabs={tabs} current={tab?.id} onPick={(id) => setPanel({ tabId: id })} onNew={() => void newTab()} busy={action.busy} /> : <h2 className="bp-heading">Browser</h2>}
        <div className="bp-head-actions">
          <Button variant="ghost" size="sm" icon={state.full ? Minimize2 : Maximize2} className="icon-only" aria-label={state.full ? "Leave full width" : "Full width"} title={state.full ? "Leave full width" : "Full width"} aria-pressed={state.full} onClick={() => setPanel({ full: !state.full })} />
          <Button variant="ghost" size="sm" icon={X} className="icon-only" aria-label="Close the browser panel" title="Close the browser panel" onClick={() => setPanel({ open: false })} />
        </div>
      </header>
      {tab && name && !empty && (
        <div className="bp-toolbar">
          <Button variant="ghost" size="sm" icon={ArrowLeft} className="icon-only" aria-label="Back" title="Back" disabled={driven || !live.tab?.canGoBack} onClick={() => navigateTo({ action: "back" })} />
          <Button variant="ghost" size="sm" icon={ArrowRight} className="icon-only" aria-label="Forward" title="Forward" disabled={driven || !live.tab?.canGoForward} onClick={() => navigateTo({ action: "forward" })} />
          <Button variant="ghost" size="sm" icon={loading ? X : RotateCw} className="icon-only" aria-label={loading ? "Stop loading" : "Reload page"} title={loading ? "Stop loading" : "Reload page"} disabled={driven} onClick={() => navigateTo({ action: loading ? "stop" : "reload" })} />
          <Address url={live.tab?.url ?? tab.url} field={field} disabled={driven} onGo={(url) => navigateTo({ action: "go", url })} />
          <Button
            variant="ghost"
            size="sm"
            icon={ExternalLink}
            className="icon-only"
            aria-label="Open in Chrome window"
            title="Open in Chrome window"
            disabled={action.busy}
            onClick={() => void action.run(() => post("/api/browser/apply-window/show", browserShown, { tabId: tab.id }))}
          />
        </div>
      )}
      {action.error && <ErrorNote error={action.error} />}
      {stale && (
        <div className="bp-stale">
          <ErrorNote error={res.error} onRetry={reload} what="the browser's tabs" />
          <p className="muted">{lastRead(res.at)}</p>
        </div>
      )}
      <div className="bp-body">
        {res.status === "loading" && (
          <p role="status" className="bp-state">
            Loading the browser
          </p>
        )}
        {res.status === "error" && !res.data && <ErrorNote error={res.error} onRetry={reload} />}
        {empty && res.status !== "error" && <StartBrowsing failure={res.data?.state === "failed" ? res.data.error : undefined} onOpened={(id) => (setPanel({ tabId: id }), reload())} />}
        {!empty && tab && name && <Viewport key={tab.id} tab={tab} live={live} img={img} view={view} driven={driven} runId={runId} title={name.title} onAddress={() => field.current?.focus()} />}
      </div>
    </aside>
  );
}
