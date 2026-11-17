import { Check, ChevronRight, CircleAlert, GitFork, Hand, LoaderCircle, RotateCcw } from "lucide-react";
import { createContext, memo, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, type ReactNode } from "react";
import { get } from "../../api/client";
import { runView } from "../../api/decoders_runs";
import { useResource } from "../../hooks";
import { CopyButton } from "../../ui/CopyButton";
import { Markdown, ModelText } from "../../ui/Markdown";
import { Chip } from "../../ui/parts";
import { Link } from "../../router";
import { ReasonNoModelRoute, type ChatMessage, type ChatTurn } from "../../wire";
import { durationLabel } from "../runs/format";
import { LetAgentFinish } from "../runs/Takeover";
import type { Live, LiveTool } from "./live";

interface ToolEntry {
  call?: ChatMessage;
  result?: ChatMessage;
}

type Entry = { kind: "message"; m: ChatMessage } | { kind: "tool"; t: ToolEntry; key: number };

// entriesOf pairs each tool call with its result so one tool shows as one line.
function entriesOf(messages: ChatMessage[]): Entry[] {
  const out: Entry[] = [];
  const byCall = new Map<string, ToolEntry>();
  for (const m of messages) {
    if (m.role === "tool_call") {
      const t: ToolEntry = { call: m };
      if (m.toolCallId) byCall.set(m.toolCallId, t);
      out.push({ kind: "tool", t, key: m.id });
    } else if (m.role === "tool_result") {
      const t = m.toolCallId ? byCall.get(m.toolCallId) : undefined;
      if (t) t.result = m;
      else out.push({ kind: "tool", t: { result: m }, key: m.id });
    } else {
      out.push({ kind: "message", m });
    }
  }
  return out;
}

// A turn is the rows from one user message up to the next one.
interface Turn {
  user?: ChatMessage;
  rows: Entry[];
}

function turnsOf(entries: Entry[]): Turn[] {
  const out: Turn[] = [];
  for (const e of entries) {
    if (e.kind === "message" && e.m.role === "user") out.push({ user: e.m, rows: [] });
    else if (out.length === 0) out.push({ rows: [e] });
    else out[out.length - 1].rows.push(e);
  }
  return out;
}

const timeOf = (e: Entry) => (e.kind === "message" ? e.m.createdAt : (e.t.result ?? e.t.call)!.createdAt);

const IRREGULAR: Record<string, string> = {
  building: "built", finding: "found", forgetting: "forgot", getting: "got", holding: "held", keeping: "kept", making: "made",
  putting: "put", reading: "read", running: "ran", seeking: "sought", sending: "sent", setting: "set", taking: "took", thinking: "thought", writing: "wrote",
};

// pastTense turns a tool's live phrase ("Reading the profile", "Starting an
// evaluation") into what it did ("Read the profile", "Started an
// evaluation"). A phrase that does not start with a verb in -ing is kept.
function pastTense(phrase: string): string {
  const [first = "", ...rest] = phrase.split(" ");
  const word = first.toLowerCase();
  if (word.length < 5 || !word.endsWith("ing")) return phrase;
  const stem = word.slice(0, -3);
  const past = IRREGULAR[word] ?? (/[^aeiou]y$/.test(stem) ? `${stem.slice(0, -1)}ied` : `${stem}ed`);
  return [first[0] === first[0].toUpperCase() ? past[0].toUpperCase() + past.slice(1) : past, ...rest].join(" ");
}

// liveWords says what a tool of the running turn is doing or did: the live
// phrase while it runs or waits, its past tense once done, "... failed" when
// it failed; the tool's name stands in when the event had no phrase.
function liveWords(t: LiveTool): string {
  if (t.state === "ok") return t.phrase ? pastTense(t.phrase) : `Ran ${t.tool}`;
  if (t.state === "failed") return `${t.phrase || t.tool} failed`;
  return t.phrase || `Running ${t.tool}`;
}

// gist sums up a finished turn's tools in one sentence; repeated calls of a
// tool are counted rather than listed.
function gist(tools: ToolEntry[]): string {
  const counts = new Map<string, number>();
  for (const t of tools) {
    const name = t.call?.toolName ?? t.result?.toolName ?? "tool";
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  const parts = Array.from(counts, ([name, n]) => (n > 1 ? `${name} (${n} calls)` : name));
  const list = parts.length < 2 ? parts.join("") : `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
  return `Used ${list}.`;
}

// ClassChip shows a tool's permission class; the two classes that change
// something outside the conversation read as a warning.
function ClassChip({ name }: { name?: string }) {
  if (!name) return null;
  return <Chip tone={name === "write-local" || name === "external-action" ? "warn" : "neutral"} raw>{name}</Chip>;
}

// Running is whether the conversation runs a turn now; a run's note offers no
// takeover then.
const Running = createContext(false);

// RunNote is a line the server wrote about a run this conversation started.
// While the run waits for answers in its kept tab and no turn runs, it offers
// "Let the agent finish" here, in the conversation the takeover continues.
function RunNote({ text, runId }: { text: string; runId: string }) {
  const [run] = useResource(() => get(`/api/runs/${encodeURIComponent(runId)}`, runView), [runId], ["runs"]);
  const running = useContext(Running);
  const entry = run.data?.entry;
  return (
    <div className="run-note">
      <p className="muted">{text}</p>
      {entry && !running && <LetAgentFinish entry={entry} compact />}
    </div>
  );
}

// RunLink opens the run a tool call started in the Runs page's drawer.
const RunLink = ({ id }: { id: string }) => <Link to={`/runs/${encodeURIComponent(id)}`}>Open the run</Link>;

// Tool and Message are memoised: a token of the streaming answer draws the
// answer alone, not every finished row above it.
const Tool = memo(function Tool({ t }: { t: ToolEntry }) {
  const name = t.call?.toolName ?? t.result?.toolName ?? "tool";
  const failed = t.result !== undefined && !t.result.ok;
  const words = !t.result ? `${name} gave no result` : failed ? `${name} failed` : `Ran ${name}`;
  // The run link sits beside the row: a link inside a summary is a control
  // inside a control.
  return (
    <>
      <details className="tool">
        <summary>
          <span className={failed ? "step-failed" : undefined}>{words}</span> <ClassChip name={t.call?.class ?? t.result?.class} />
          {t.result?.systemMade && <span className="muted"> made by the system after a restart</span>}
        </summary>
        {t.call && (
          <>
            <p className="label">Input (redacted)</p>
            <pre className="pre">{t.call.text}</pre>
          </>
        )}
        {t.result && (
          <>
            <p className="label">Output (redacted)</p>
            <ModelText source={t.result.text} />
          </>
        )}
      </details>
      {t.result?.runId && <RunLink id={t.result.runId} />}
    </>
  );
});

// pageUrl returns the address of a web source when it is a plain http(s) link;
// anything else is shown as text, whatever a tool or a stored record says.
function pageUrl(raw?: string): string | null {
  if (!raw) return null;
  try {
    const u = new URL(raw);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : null;
  } catch {
    return null;
  }
}

// Sources lists what an answer rests on, what it could not establish and what
// the user can do next. The envelope reaches the page as data, so it is shown
// as text and never as markup.
function Sources({ m }: { m: ChatMessage }) {
  const cites = m.citations ?? [];
  const limits = m.limitations ?? [];
  if (cites.length === 0 && limits.length === 0 && !m.nextAction) return null;
  return (
    <div className="sources">
      {cites.length > 0 && (
        <>
          <p className="label">Sources</p>
          <ul className="plain">
            {cites.map((c) => {
              const href = pageUrl(c.url);
              return (
                <li key={href ?? c.label}>
                  {href ? (
                    <>
                      <a href={href} target="_blank" rel="noopener noreferrer">
                        {c.label}
                      </a>{" "}
                      <span className="source-url">{href}</span>
                    </>
                  ) : (
                    <code>{c.label}</code>
                  )}
                  {c.snippet && <span className="muted"> {c.snippet}</span>}
                </li>
              );
            })}
          </ul>
        </>
      )}
      {limits.length > 0 && (
        <>
          <p className="label">Limits</p>
          <ul>
            {limits.map((l, i) => (
              <li key={i}>{l}</li>
            ))}
          </ul>
        </>
      )}
      {m.nextAction && (
        <p>
          <span className="label">Next </span>
          {m.nextAction}
        </p>
      )}
    </div>
  );
}

// STOP_WORDS says in words why a turn ended without an answer, by the reason
// code of its error event (internal/agent/pirpc). The detail, when there is
// one, is what happened; runtime_unavailable's detail says it on its own, and
// no_model_route has no entry because its detail says where to choose a model.
const STOP_WORDS: Record<string, string> = {
  cancelled: "you cancelled it",
  "time-limit": "it ran past the time limit for a turn (10 minutes, 30 when it finishes a run)",
  "cost-cap": "the day's model spend reached its cap",
  stuck: "the agent kept repeating the same step",
  truncated: "the model's answer was cut off",
  provider_error: "the model provider answered with an error",
  runtime_unavailable: "the agent could not run",
};

export function stopWords(reason?: string, detail?: string): string {
  const words = reason ? STOP_WORDS[reason] : undefined;
  if (!words) return detail || reason || "it ended without an answer";
  if (!detail || reason === "runtime_unavailable") return detail || words;
  return `${words}: ${detail}`;
}

// Stopped says why a turn ended without an answer. The provider, the cap and a
// missing model are fixed in the model settings; the user's own message can be
// sent again.
export function Stopped({ reason, why, onRetry }: { reason?: string; why: string; onRetry?: () => void }) {
  return (
    <div role="status" className="note error stopped">
      <span>The turn stopped: {why}</span>
      {(reason === "provider_error" || reason === "cost-cap" || reason === ReasonNoModelRoute) && <Link to="/settings#settings-models">Models and providers</Link>}
      {onRetry && (
        <button type="button" className="small" onClick={onRetry}>
          <RotateCcw aria-hidden="true" />
          Send again
        </button>
      )}
    </div>
  );
}

const Message = memo(function Message({ m, busy, onFork }: { m: ChatMessage; busy: boolean; onFork: (id: number) => void }) {
  if (m.role === "user" || m.role === "steer") {
    return (
      <div className={m.role === "steer" ? "turn user steer" : "turn user"}>
        <div className="bubble">
          <p className="label">{m.role === "steer" ? "You, steering" : "You"}</p>
          <p className="pre">{m.text}</p>
        </div>
        {m.role === "user" && (
          <button type="button" className="ghost small fork" disabled={busy} onClick={() => onFork(m.id)} title="Start a new conversation from this message">
            <GitFork />
            Fork from here
          </button>
        )}
      </div>
    );
  }
  if (m.role === "assistant") {
    return (
      <div className="turn assistant">
        <p className="label">Agent</p>
        <Markdown source={m.text} />
        <Sources m={m} />
        <span className="answer-copy">
          <CopyButton text={m.text} label="Copy the answer" />
        </span>
      </div>
    );
  }
  if (m.role === "note" && m.runId) return <RunNote text={m.text} runId={m.runId} />;
  return <p className="muted">{m.text}</p>;
});

// FOLLOW_SLACK_PX is how near the end of the page counts as being at it.
const FOLLOW_SLACK_PX = 48;

// useFollowEnd keeps the end of the page in view while a turn streams in, as
// long as the reader was at the end: scrolling up to read stops the following
// until they scroll back down. The page itself scrolls, not the transcript.
function useFollowEnd(content: string | null) {
  const atEnd = useRef(true);
  useEffect(() => {
    const check = () => {
      const page = document.documentElement;
      atEnd.current = page.scrollHeight - window.scrollY - window.innerHeight <= FOLLOW_SLACK_PX;
    };
    check();
    window.addEventListener("scroll", check, { passive: true });
    return () => window.removeEventListener("scroll", check);
  }, []);
  useLayoutEffect(() => {
    if (content !== null && atEnd.current) window.scrollTo({ top: document.documentElement.scrollHeight });
  }, [content]);
}

const STEP_ICON = { running: LoaderCircle, ok: Check, failed: CircleAlert, held: Hand } as const;

// Worked folds a finished turn's steps behind one line: how long the turn
// took (its user message to its last row, both stamped on the wire), how many
// tools failed and a sentence of what it used. It opens to the steps.
function Worked({ rows, from, to, busy, onFork }: { rows: Entry[]; from: string; to: string; busy: boolean; onFork: (id: number) => void }) {
  const tools = rows.flatMap((e) => (e.kind === "tool" ? [e.t] : []));
  const failed = tools.filter((t) => t.result && !t.result.ok).length;
  return (
    <details className="worked">
      <summary>
        <span className="worked-time">Worked for {durationLabel(from, to)}</span>
        <ChevronRight aria-hidden="true" />
        {failed > 0 && <span className="step-failed">{failed} failed</span>}
        <span className="worked-gist">{gist(tools)}</span>
      </summary>
      <div className="worked-steps">{rows.map((e) => row(e, busy, onFork))}</div>
    </details>
  );
}

const isAnswer = (e: Entry) => e.kind === "message" && e.m.role === "assistant";

function row(e: Entry, busy: boolean, onFork: (id: number) => void) {
  return e.kind === "message" ? <Message key={e.m.id} m={e.m} busy={busy} onFork={onFork} /> : <Tool key={e.key} t={e.t} />;
}

// TurnRows draws one turn. A finished turn that used tools shows its user
// message, the Worked line, a divider and its answer (the turn's last agent
// message, with whatever came after it); the running turn stays open.
function TurnRows({ turn, open, busy, onFork, stop }: { turn: Turn; open: boolean; busy: boolean; onFork: (id: number) => void; stop: ReactNode }) {
  const user = turn.user && <Message m={turn.user} busy={busy} onFork={onFork} />;
  let answer = turn.rows.length - 1;
  while (answer >= 0 && !isAnswer(turn.rows[answer])) answer--;
  const work = answer < 0 ? turn.rows : turn.rows.slice(0, answer);
  if (open || !work.some((e) => e.kind === "tool")) {
    return (
      <>
        {user}
        {turn.rows.map((e) => row(e, busy, onFork))}
        {!open && stop}
      </>
    );
  }
  const tail = answer < 0 ? [] : turn.rows.slice(answer);
  const from = turn.user?.createdAt ?? timeOf(work[0]);
  return (
    <>
      {user}
      <Worked rows={work} from={from} to={timeOf(tail[0] ?? work[work.length - 1])} busy={busy} onFork={onFork} />
      {tail.length > 0 && <hr className="worked-rule" />}
      {tail.map((e) => row(e, busy, onFork))}
      {stop}
    </>
  );
}

// latestRunNotes keeps the run of only the latest note about each run, so one
// note offers to finish it.
function latestRunNotes(messages: ChatMessage[]): ChatMessage[] {
  const latest = new Map<string, number>();
  for (const m of messages) if (m.role === "note" && m.runId) latest.set(m.runId, m.id);
  return messages.map((m) => (m.role === "note" && m.runId && latest.get(m.runId) !== m.id ? { ...m, runId: undefined } : m));
}

// A turn the server stored as ended in error shows why under its rows, so a
// reload or another visit still says what happened; the last one offers to
// send its message again. stopped is the live reason of a turn whose stored
// record could not be read (the server went away).
export function Transcript({ messages, turns: records = [], live, stopped, running, busy, onFork, onRetry }: { messages: ChatMessage[]; turns?: ChatTurn[]; live: Live | null; stopped: string | null; running: boolean; busy: boolean; onFork: (id: number) => void; onRetry?: (text: string) => void }) {
  const turns = useMemo(() => turnsOf(entriesOf(latestRunNotes(messages))), [messages]);
  // One function for the life of the transcript, so a finished row keeps its props.
  const latestFork = useRef(onFork);
  latestFork.current = onFork;
  const fork = useCallback((id: number) => latestFork.current(id), []);
  const failed = new Map(records.filter((r) => r.status === "error").map((r) => [r.id, r]));
  const stopOf = (t: Turn, last: boolean) => {
    const r = t.user?.turnId ? failed.get(t.user.turnId) : undefined;
    if (!r) return null;
    const text = t.user!.text;
    return <Stopped reason={r.reason} why={stopWords(r.reason, r.detail)} onRetry={last && !running && onRetry ? () => onRetry(text) : undefined} />;
  };
  useFollowEnd(live ? `${live.tools.length}|${live.tools.map((t) => t.state).join()}|${live.text}` : null);
  return (
    <div className="transcript" role="log" aria-label="Conversation">
      <Running.Provider value={running}>
        {turns.map((t, i) => (
          <TurnRows key={t.user?.id ?? "lead"} turn={t} open={running && i === turns.length - 1} busy={busy} onFork={fork} stop={stopOf(t, i === turns.length - 1)} />
        ))}
      </Running.Provider>
      {live && (
        <div className="turn assistant" aria-live="polite" aria-busy="true">
          <p className="label working">
            Agent is working
            <span className="dots" aria-hidden="true">
              <span />
              <span />
              <span />
            </span>
          </p>
          <ul className="plain steps">
            {live.tools.map((t) => {
              const Icon = STEP_ICON[t.state];
              return (
                <li key={t.id} data-state={t.state}>
                  <Icon aria-hidden="true" /> <span className={t.state === "failed" ? "step-failed" : undefined}>{liveWords(t)}</span> <ClassChip name={t.class} />
                  {t.state === "held" && <Chip tone="warn">waiting for you</Chip>}
                  {t.state === "held" && t.summary && <span className="muted"> {t.summary}</span>}
                  {t.runId && <RunLink id={t.runId} />}
                </li>
              );
            })}
          </ul>
          {live.text && <Markdown source={live.text} />}
        </div>
      )}
      {stopped && <Stopped why={stopped} />}
    </div>
  );
}
