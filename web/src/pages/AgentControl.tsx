import { CircleStop, MonitorCog, MousePointerClick, ScanEye, SlidersHorizontal } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { openChatStream, type ChatStream } from "../api/chatstream";
import { asApiError, useInterval, useResource, type Resource } from "../hooks";
import { fetchBlob, get, post } from "../api/client";
import { chatCancelResult, computerUseStatus, desktopActivity } from "../api/decoders_agent";
import { Link } from "../router";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/EmptyState";
import { Modal } from "../ui/Modal";
import { PageHeader } from "../ui/PageHeader";
import { Chip, ErrorNote, Panel, formatDateTime, humanize, type Tone } from "../ui/parts";
import type { ComputerUseStatus, DesktopEvent, DesktopTarget } from "../wire";
import { useAction } from "./action";

// The activity is the server's in-memory record of the running and recent
// turns, so it is read often while the page shows; the service's state moves
// only when the user opens PersonalCU, grants it something or locks the Mac.
const ACTIVITY_MS = 1500;
const STATUS_MS = 5000;

const HEALTH: Record<string, { label: string; tone: Tone }> = {
  ready: { label: "Ready", tone: "good" },
  "not-running": { label: "Not running", tone: "bad" },
  permissions: { label: "Needs permissions", tone: "warn" },
  locked: { label: "Screen locked", tone: "warn" },
  paused: { label: "Paused", tone: "warn" },
  unsupported: { label: "Clicks only", tone: "warn" },
};

// pausedBy tells the three things the paused health stands for apart, in the
// order the service reports them: its own pause, its background control
// switched off, and otherwise the Pause of this page.
function pausedBy(s: ComputerUseStatus): "service" | "background" | "page" {
  return s.servicePaused ? "service" : s.backgroundOff ? "background" : "page";
}

const PAUSED: Record<ReturnType<typeof pausedBy>, string> = { service: "Paused in PersonalCU", background: "Background control off", page: "Paused in Agent control" };

// HealthHelp says what to do about each health. PersonalCU is a separate app
// that holds the macOS permissions itself; Applysmith never starts it and asks
// for neither permission (owner decision AC-1).
function HealthHelp({ s }: { s: ComputerUseStatus }) {
  switch (s.health) {
    case "ready":
      return <>PersonalCU is running with both permissions, so the agent can look at and act in the windows it is allowed to use.</>;
    case "not-running":
      return (
        <>
          PersonalCU is a separate app, and Applysmith never starts it. Open PersonalCU from your Applications folder; this page notices within a few seconds once it runs.
        </>
      );
    case "permissions": {
      const missing = [!s.accessibility && "Accessibility", !s.screenRecording && "Screen Recording"].filter((p): p is string => Boolean(p));
      const where = missing.length === 1 ? `then ${missing[0]}` : missing.length === 2 ? "then each of the two" : "then each list that names it";
      return (
        <>
          PersonalCU needs {missing.length > 0 ? missing.join(" and ") : "a permission macOS grants"}. In System Settings, open Privacy &amp; Security, {where}, and turn PersonalCU
          on. Quit and reopen PersonalCU afterwards.
        </>
      );
    }
    case "locked":
      return <>This Mac is locked. PersonalCU cannot read or act in a window until you unlock it.</>;
    case "paused":
      switch (pausedBy(s)) {
        case "service":
          return <>PersonalCU&rsquo;s own pause is on, so it refuses every input. Resume control from its menu bar icon.</>;
        case "background":
          // Every action the agent sends is a background one.
          return <>Background control is off in PersonalCU, so it refuses every action the agent sends. Turn on Allow background control in its window.</>;
        default:
          return <>Pause is on in Agent control, so every desktop action of the agent is refused. PersonalCU itself is ready; turn Pause off to let the agent act again.</>;
      }
    case "unsupported":
      return <>PersonalCU can type, press keys and scroll in the background only on macOS 26 on Apple silicon. On this Mac the agent can click, and nothing more.</>;
    default:
      return null;
  }
}

function Permission({ granted, known }: { granted: boolean; known: boolean }) {
  if (!known) return <span className="muted">Not known</span>;
  return <Chip tone={granted ? "good" : "warn"}>{granted ? "Granted" : "Missing"}</Chip>;
}

// ServiceHealth is PersonalCU's state with what to do about it, the two
// permissions it holds, and whether a look's screenshot reaches the model
// (owner decision AC-3). Settings shows it too.
export function ServiceHealth({ status: s }: { status: ComputerUseStatus }) {
  const h = HEALTH[s.health] ?? { label: humanize(s.health), tone: "neutral" };
  const label = s.health === "paused" ? PAUSED[pausedBy(s)] : h.label;
  // A service that does not answer reports no permission; the flags are false
  // because nothing was read, not because a grant is missing.
  const known = s.health !== "not-running";
  return (
    <>
      <p>
        Health: <Chip tone={h.tone}>{label}</Chip>
      </p>
      <p className={s.health === "ready" ? "help" : "note warn"}>
        <HealthHelp s={s} />
      </p>
      {/* The server's reason restates the help above for every other health;
          a service that does not answer gives its error there instead. */}
      {s.reason && s.health === "not-running" && <p className="help sentence">{s.reason}</p>}
      <dl className="facts">
        <dt>Accessibility</dt>
        <dd>
          <Permission granted={s.accessibility} known={known} />
        </dd>
        <dt>Screen Recording</dt>
        <dd>
          <Permission granted={s.screenRecording} known={known} />
        </dd>
        <dt>Screenshots</dt>
        <dd>{s.imagesToModel ? "Sent to the agent's model with each look." : "Shown on this page only; the agent's model reads each window's controls as text."}</dd>
      </dl>
    </>
  );
}

// PauseSwitch is the Pause of desktop control (owner decision AC-5). The
// server answers with the state it keeps, and the switch shows that answer,
// never the press.
function PauseSwitch({ status, onReply }: { status?: ComputerUseStatus; onReply: (s: ComputerUseStatus) => void }) {
  const act = useAction();
  const [said, setSaid] = useState("");
  const set = async (paused: boolean) => {
    setSaid("");
    const out = await act.run(() => post("/api/computer-use/pause", computerUseStatus, { paused }));
    if (!out) return;
    onReply(out);
    setSaid(out.paused ? "Pause is on: every desktop action is refused." : "Pause is off: the agent can act again when you ask it to.");
  };
  return (
    <div className="ac-pause">
      {status ? (
        <label className="check">
          <input type="checkbox" role="switch" checked={status.paused} disabled={act.busy} onChange={(e) => void set(e.target.checked)} />
          Pause desktop control
        </label>
      ) : (
        <p className="muted">Pause shows once the state of desktop control is read.</p>
      )}
      <span className="help">While Pause is on, every desktop action of the agent is refused at once. It stays on after a restart.</span>
      <div role="status">{said && <p className="note ok">{said}</p>}</div>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// useRunningTurn follows the conversation of the newest desktop event. The
// first event of its chat stream says whether a turn runs there, and the
// stream ends with that turn; it is asked again only once a newer desktop
// event arrives while no turn is known to run.
function useRunningTurn(sessionId: string | undefined, newest: string | undefined): boolean {
  const [running, setRunning] = useState(false);
  const following = useRef<{ session: string; stream?: ChatStream } | null>(null);
  useEffect(() => {
    if (!sessionId || following.current?.session === sessionId) return;
    following.current?.stream?.close();
    setRunning(false);
    const mine: { session: string; stream?: ChatStream } = { session: sessionId };
    following.current = mine;
    mine.stream = openChatStream(
      sessionId,
      (ev) => {
        if (following.current === mine && ev.type === "state") setRunning(true);
      },
      () => {
        if (following.current !== mine) return;
        following.current = null;
        setRunning(false);
      },
    );
  }, [sessionId, newest]);
  useEffect(
    () => () => {
      following.current?.stream?.close();
      following.current = null;
    },
    [],
  );
  return running;
}

// StopTurn cancels the running turn of the newest desktop event's
// conversation, as the Ask Agent's own cancel does. It asks nothing first:
// stopping is the safe direction.
function StopTurn({ sessionId, running }: { sessionId?: string; running: boolean }) {
  const act = useAction();
  const [said, setSaid] = useState("");
  const stop = async (id: string) => {
    setSaid("");
    const out = await act.run(() => post(`/api/chat/sessions/${encodeURIComponent(id)}/cancel`, chatCancelResult));
    if (out) setSaid(out.note);
  };
  return (
    <div className="ac-stop">
      {running && sessionId ? (
        <>
          <p>A turn of the agent is using the desktop now.</p>
          <div className="actions">
            <Button variant="danger" icon={CircleStop} disabled={act.busy} onClick={() => void stop(sessionId)}>
              Stop the turn
            </Button>
            <Link to={`/ask?conversation=${encodeURIComponent(sessionId)}`}>Open the conversation</Link>
          </div>
        </>
      ) : (
        <p className="muted">No turn is using the desktop now. Stop shows here while one runs.</p>
      )}
      <div role="status">{said && <p className="note ok">{said}</p>}</div>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// shots keeps one object URL per screenshot while a row shows it, so a look
// and the action decided on it read the picture once. It is read with the
// session key (an image element cannot send it).
interface HeldShot {
  url: Promise<string>;
  rows: number;
}
const shots = new Map<string, HeldShot>();

function holdShot(id: string): HeldShot {
  let held = shots.get(id);
  if (!held) {
    const mine: HeldShot = { url: fetchBlob(`/api/computer-use/shots/${encodeURIComponent(id)}`).then((blob) => URL.createObjectURL(blob)), rows: 0 };
    // A read that failed is not kept, so Retry reads again.
    mine.url.catch(() => shots.get(id) === mine && shots.delete(id));
    shots.set(id, (held = mine));
  }
  held.rows++;
  return held;
}

function dropShot(id: string, held: HeldShot) {
  if (--held.rows > 0) return;
  if (shots.get(id) === held) shots.delete(id);
  held.url.then(
    (url) => URL.revokeObjectURL(url),
    () => {},
  );
}

// Picture is a screenshot with, on an action's step, an outline around the
// control the agent acted on. The outline is placed by fractions of the
// picture, so it holds at any size.
function Picture({ src, alt, target, verdict }: { src: string; alt: string; target?: DesktopTarget; verdict: string }) {
  return (
    <span className="desk-pic">
      <img src={src} alt={alt} />
      {target && (
        <span
          className="desk-mark"
          data-verdict={verdict}
          aria-hidden="true"
          style={{ left: `${target.x * 100}%`, top: `${target.y * 100}%`, width: `${target.width * 100}%`, height: `${target.height * 100}%` }}
        />
      )}
    </span>
  );
}

// Shot is the screenshot of a look or, with a target, of the look an action
// was decided on. The server keeps only the latest few in memory, so an older
// one is gone.
function Shot({ id, title, target, verdict }: { id: string; title: string; target?: DesktopTarget; verdict: string }) {
  const [src, setSrc] = useState<string | null>(null);
  const [missing, setMissing] = useState("");
  const [open, setOpen] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let live = true;
    const held = holdShot(id);
    held.url.then(
      (url) => live && setSrc(url),
      (e) => live && setMissing(asApiError(e).kind === "not_found" ? "No longer kept" : "Not loaded"),
    );
    return () => {
      live = false;
      dropShot(id, held);
    };
  }, [id, attempt]);
  // A screenshot that is gone stays gone; one that failed to load can be asked again.
  if (missing === "Not loaded") {
    return (
      <button type="button" className="desk-shot gone" aria-label={`Not loaded. Retry the screenshot of ${title}`} onClick={() => (setMissing(""), setAttempt((n) => n + 1))}>
        Not loaded. Retry
      </button>
    );
  }
  if (missing) return <span className="desk-shot gone">{missing}</span>;
  if (!src) return <span className="desk-shot" aria-hidden="true" />;
  const what = target ? `where the agent acted in ${title}` : `the screenshot of ${title}`;
  return (
    <>
      <button type="button" className="desk-shot" aria-label={`Open ${what}`} onClick={() => setOpen(true)}>
        <Picture src={src} alt="" target={target} verdict={verdict} />
      </button>
      {open && (
        <Modal title={`Screenshot of ${title}`} variant="wide" onClose={() => setOpen(false)}>
          {target && <p className="help">The outline marks the control this step named, in the look it was decided on.</p>}
          {/* Focusable, so the keyboard scrolls a picture wider than the dialog. */}
          <div className="shot-scroll" tabIndex={0} role="group" aria-label="Full-size screenshot">
            <Picture src={src} alt={`Screenshot of ${title}`} target={target} verdict={verdict} />
          </div>
          <div className="actions">
            <button type="button" onClick={() => setOpen(false)}>
              Close
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}

const TOOLS: Record<string, string> = {
  desktop_windows: "List windows",
  desktop_look: "Look",
  desktop_click: "Click",
  desktop_type: "Type",
  desktop_keys: "Keys",
  desktop_scroll: "Scroll",
};

const VERDICTS: Record<string, { label: string; tone: Tone }> = {
  allow: { label: "Allowed", tone: "good" },
  deny: { label: "Refused", tone: "bad" },
  failed: { label: "Failed", tone: "warn" },
};

// roleWord reads an accessibility role ("AXPopUpButton") as words.
const roleWord = (role: string) => humanize(role.replace(/^AX/, "").replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase());

const pad = (n: number) => String(n).padStart(2, "0");

// clock is an event's time to the second on this computer's clock, since the
// steps of a turn are seconds apart; the turn's heading carries the day.
function clock(iso: string): string {
  const d = new Date(iso);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

// StepDetail is the key a Keys step pressed, each key of the chord on its own
// cap, or a scroll's direction and pages.
function StepDetail({ tool, detail }: { tool: string; detail: string }) {
  if (tool !== "desktop_keys") return <> {detail}</>;
  return (
    <>
      {detail.split("+").map((key) => (
        <span key={key}>
          {" "}
          <kbd>{humanize(key)}</kbd>
        </span>
      ))}
    </>
  );
}

// EventRow is one desktop decision. An event never holds what was typed, so
// a Type step names the field alone.
function EventRow({ e }: { e: DesktopEvent }) {
  const v = VERDICTS[e.verdict] ?? { label: humanize(e.verdict), tone: "neutral" };
  return (
    <li className="desk-event" data-verdict={e.verdict}>
      <time className="desk-time" dateTime={e.at}>
        {clock(e.at)}
      </time>
      <div className="desk-main">
        <p>
          <strong>{TOOLS[e.tool] ?? e.tool}</strong>
          {e.detail && <StepDetail tool={e.tool} detail={e.detail} />}
          {e.app && <> in {e.app}</>}
          {e.window && <span className="muted"> &middot; {e.window}</span>}
        </p>
        {(e.role || e.control) && (
          <p className="desk-control">
            {e.role ? roleWord(e.role) : "Control"}
            {e.control && <> &ldquo;{e.control}&rdquo;</>}
          </p>
        )}
        <p className="desk-verdict">
          <Chip tone={v.tone}>{v.label}</Chip>
          {e.reason && <span className="sentence">{e.reason}</span>}
        </p>
      </div>
      {e.shotId && <Shot id={e.shotId} title={e.window || e.app || "the window"} target={e.target} verdict={e.verdict} />}
    </li>
  );
}

interface Turn {
  key: string;
  sessionId?: string;
  events: DesktopEvent[];
}

// turns groups consecutive events of one turn, newest first as listed.
function turns(events: DesktopEvent[]): Turn[] {
  const out: Turn[] = [];
  for (const e of events) {
    const key = e.turnId || e.sessionId || e.id;
    const last = out[out.length - 1];
    if (last?.key === key) last.events.push(e);
    else out.push({ key, sessionId: e.sessionId, events: [e] });
  }
  return out;
}

const steps = (n: number) => (n === 1 ? "1 step" : `${n} steps`);

// SHOWN are the steps the activity can be narrowed to: all of them, or those
// the gate refused or the service failed, which are what a user comes to find.
const SHOWN = [
  { verdict: "", label: "All" },
  { verdict: "deny", label: "Refused" },
  { verdict: "failed", label: "Failed" },
];

function Activity({ events: all, running }: { events: DesktopEvent[]; running?: string }) {
  const [only, setOnly] = useState("");
  const count = (verdict: string) => (verdict ? all.filter((e) => e.verdict === verdict).length : all.length);
  // A filter whose steps are gone from the kept activity shows all again.
  const verdict = only && count(only) > 0 ? only : "";
  const events = verdict ? all.filter((e) => e.verdict === verdict) : all;
  if (all.length === 0) {
    return (
      <EmptyState icon={MousePointerClick} title="The agent has not used a window yet." action={<Link to="/ask">Open Ask Agent</Link>}>
        When you ask in Ask Agent, it can look at and act in the apply browser and in apps your message names. It never presses Submit, Send or Delete,
        and Pause on this page refuses every action at once.
      </EmptyState>
    );
  }
  return (
    <>
      <div className="desk-bar">
        <p className="count-line">{steps(events.length)}, newest first. Screenshots are kept in memory only.</p>
        {events.length < all.length || all.some((e) => e.verdict !== "allow") ? (
          <div className="desk-filter" role="group" aria-label="Steps shown">
            {SHOWN.filter((f) => !f.verdict || count(f.verdict) > 0).map((f) => (
              <button type="button" key={f.label} aria-pressed={verdict === f.verdict} onClick={() => setOnly(f.verdict)}>
                {f.label} <span className="chip">{count(f.verdict)}</span>
              </button>
            ))}
          </div>
        ) : null}
      </div>
      <ol className="plain desk-turns">
        {turns(events).map((t, i) => {
          const first = t.events[t.events.length - 1];
          return (
            <li key={`${t.key}:${first.id}`}>
              <div className="desk-turn-head">
                <h3>
                  Turn at <time dateTime={first.at}>{formatDateTime(first.at)}</time>
                </h3>
                <span className="muted">{steps(t.events.length)}</span>
                {i === 0 && running !== undefined && t.sessionId === running && <Chip tone="info">Running</Chip>}
                {t.sessionId && <Link to={`/ask?conversation=${encodeURIComponent(t.sessionId)}`}>Open the conversation</Link>}
              </div>
              <ol className="plain desk-events">
                {t.events.map((e) => (
                  <EventRow key={e.id} e={e} />
                ))}
              </ol>
            </li>
          );
        })}
      </ol>
    </>
  );
}

export default function AgentControl() {
  const [statusRes, reloadStatus] = useResource(() => get("/api/computer-use", computerUseStatus), []);
  // A Pause press answers with the state the server now keeps; that answer
  // stands until the read the press starts comes back.
  const [reply, setReply] = useState<ComputerUseStatus | null>(null);
  useEffect(() => {
    setReply(null);
  }, [statusRes.data]);
  const status: Resource<ComputerUseStatus> = reply ? { status: "ready", data: reply } : statusRes;
  const [activity, reloadActivity] = useResource(() => get("/api/computer-use/activity", desktopActivity), []);
  useInterval(reloadStatus, STATUS_MS, true);
  useInterval(reloadActivity, ACTIVITY_MS, true);
  const events = activity.data?.events ?? [];
  const session = events.find((e) => e.sessionId)?.sessionId;
  const running = useRunningTurn(session, events[0]?.id);
  return (
    <>
      <PageHeader path="/agent-control" />
      <div className="agent-control">
        <div className="agent-control-side">
          <Panel title="PersonalCU" id="ac-service" res={status} retry={reloadStatus} what="the state of PersonalCU" icon={MonitorCog}>
            {(s) => <ServiceHealth status={s} />}
          </Panel>
          <section aria-labelledby="ac-controls" className="panel">
            <header className="panel-head">
              <h2 id="ac-controls">
                <SlidersHorizontal className="panel-icon" />
                Controls
              </h2>
            </header>
            <PauseSwitch
              status={status.data}
              onReply={(s) => {
                setReply(s);
                reloadStatus();
              }}
            />
            <StopTurn sessionId={session} running={running} />
          </section>
        </div>
        <Panel title="Activity" id="ac-activity" res={activity} retry={reloadActivity} what="the agent's desktop activity" icon={ScanEye}>
          {(a) => <Activity events={a.events} running={running ? session : undefined} />}
        </Panel>
      </div>
    </>
  );
}
