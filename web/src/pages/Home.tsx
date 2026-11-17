import { useContext, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Activity, BellRing, ChevronRight, ClipboardPaste, Mail, CircleAlert, CircleCheck, CircleDashed, CircleX, ClipboardCheck, History, Info, Inbox, ListChecks, MessageSquareText, MessagesSquare, NotebookPen, PenLine, Radar, Send, Server as ServerIcon, Wallet, type LucideIcon } from "lucide-react";
import { get, newRequestId, request, writesSent } from "../api/client";
import type { BoardState } from "../api/board";
import { followupList, llmRouting, trackerSummary } from "../api/decoders";
import { applyList } from "../api/decoders_apply";
import { agentTaskList, confirmationList } from "../api/decoders_agent";
import { inboxPage } from "../api/decoders_discovery";
import { interviewsSoon } from "../api/decoders_interview";
import { gmailInboxPage } from "../api/decoders_gmail";
import type { AgentTaskList, ApplyList, ConfirmationList, DayLine, FollowupList, GmailInboxPage, InboxPage, InterviewsSoon, LLMRouting, NeedsYouCounts, NeedsYouInbox } from "../wire";
import { boardEntryList, boardSummary, dayView, needsYouCounts, needsYouInbox, runAccepted } from "../api/decoders_runs";
import { health } from "../api/decode";
import { BoardContext, useBoardState, useInterval, useLocalDay, useResource } from "../hooks";
import { useAction } from "./action";
import { EvaluationNote } from "./discovery/EvaluationNote";
import { WeekFunnel } from "./discovery/Funnel";
import { Link, useSearchParams } from "../router";
import { Button } from "../ui/Button";
import { ariaKeys, keyLabel } from "../ui/Shortcuts";
import { PageHeader } from "../ui/PageHeader";
import { CareerCover } from "../ui/CareerCover";
import { useUnsaved } from "../ui/SaveBar";
import { Chip, Empty, ErrorNote, Panel, ProgressBar, ShowMore, TRACKER_STATUSES, formatDateTime, humanize, useCapped, usd, type Tone } from "../ui/parts";
import { durationLabel, fixOf, groupWaiting, plainDetail, stageDoing } from "./runs/format";
import { SetupCard } from "./setup/HomeCard";
import { LetAgentFinish } from "./runs/Takeover";
import { LetAgentDo, open } from "./askagent/Task";
import { noAgentModel } from "./askagent/Chat";
import { ShowInPanel } from "./browser/Lanes";

const LANE_TITLES: Record<string, string> = {
  "apply-now": "Apply now",
  selective: "Selective",
  "marked-applied": "Marked applied",
  awaiting: "Awaiting receipt",
};

// readyCount is the lanes Apply Next's Next up comes from.
const readyCount = (list: ApplyList) => list.lanes.reduce((n, l) => n + (l.lane === "apply-now" || l.lane === "selective" ? l.total : 0), 0);

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

// The tracker tile lists every status that holds a row, so the parts always
// add up to the total; a status with no row is left out rather than shown as 0.
function TrackerCounts() {
  const [res, retry] = useResource(() => get("/api/tracker/summary", trackerSummary), [], ["tracker"], { keep: "home.summary" });
  return (
    <Panel title="Tracker" id="home-tracker" res={res} retry={retry} icon={ListChecks} className="stat">
      {(s) =>
        s.total === 0 ? (
          <>
            <span className="stat-value">0</span>
            <p className="stat-sub">
              No applications are tracked yet. <Link to="/tracker">Open the tracker</Link>
            </p>
          </>
        ) : (
          <>
            <Link to="/tracker" className="stat-value" aria-label={`${plural(s.total, "tracked row", "tracked rows")}`}>
              {s.total}
            </Link>
            <p className="stat-sub">{s.total === 1 ? "row" : "rows"} in the tracker</p>
            <ul className="plain mini-stats" aria-label="Rows by status">
              {TRACKER_STATUSES.filter((st) => (s.counts[st] ?? 0) > 0).map((st) => (
                <li key={st}>
                  <Link to={`/tracker?status=${st}`}>
                    <span className="dot" data-status={st} aria-hidden="true" />
                    <strong>{s.counts[st]}</strong> {humanize(st)}
                  </Link>
                </li>
              ))}
            </ul>
          </>
        )
      }
    </Panel>
  );
}

function ReadyToApply() {
  const [res, retry] = useResource(() => get("/api/apply/list", applyList, { pageSize: 1 }), [], ["tracker"], { keep: "home.ready" });
  return (
    <Panel title="Ready to apply" id="home-ready" res={res} retry={retry} icon={Send} className="stat">
      {(list) => {
        const now = readyCount(list);
        // Four lanes of 0 say less than where postings come from.
        if (list.lanes.every((l) => l.total === 0))
          return (
            <>
              <span className="stat-value">0</span>
              <p className="stat-sub">
                Nothing is ready yet. <Link to="/discovery">Find postings in Discovery</Link>
              </p>
            </>
          );
        return (
          <>
            <Link to="/apply-next?lane=apply-now,selective" className="stat-value" aria-label={`${now} ready to apply`}>
              {now}
            </Link>
            <p className="stat-sub">
              {now === 1 ? "posting is" : "postings are"} ready to apply{now === 0 && <>. <Link to="/discovery">Find postings in Discovery</Link></>}
            </p>
            <ul className="plain mini-stats" aria-label="Rows by lane">
              {list.lanes.map((lane) => (
                <li key={lane.lane}>
                  <Link to={`/apply-next?lane=${lane.lane}`}>
                    <strong>{lane.total}</strong> {LANE_TITLES[lane.lane] ?? humanize(lane.lane)}
                  </Link>
                </li>
              ))}
            </ul>
          </>
        );
      }}
    </Panel>
  );
}

function Spend() {
  // The spend the cap is held to, so the bar fills as the cap nears.
  const [res, retry] = useResource(async () => {
    const routing = await get("/api/llm/routing", llmRouting);
    return { spent: routing.capSpentUsd, cap: routing.dailyUsdCap };
  }, [], ["runs"], { keep: "home.spend" });
  return (
    <Panel title="Model spend today" id="home-spend" res={res} retry={retry} icon={Wallet} className="stat">
      {({ spent, cap }) => {
        const share = cap > 0 ? (spent / cap) * 100 : 0;
        return (
          <>
            <span className="stat-value">{usd(spent)}</span>
            <p className="stat-sub">
              of {cap > 0 ? `$${cap.toFixed(2)}` : "no"} daily cap. <Link to="/usage">Usage</Link>
            </p>
            {cap > 0 && <ProgressBar percent={share} label="Share of the daily cap spent" tone={share >= 90 ? "bad" : share >= 70 ? "warn" : undefined} />}
          </>
        );
      }}
    </Panel>
  );
}

export interface Item {
  key: string;
  to: string;
  text: string;
  detail?: string;
  tone: Tone;
  icon: LucideIcon;
  /** A control beside the link, such as Let the agent finish. */
  action?: ReactNode;
  /** The runs the item stands for: each repeat of a grouped run. */
  runs?: string[];
}

function ItemList({ items }: { items: Item[] }) {
  return (
    <ul className="plain item-list">
      {items.map((i) => {
        const Icon = i.icon;
        return (
          <li key={i.key}>
            <Link to={i.to} className="item-link">
              <span className={`status-icon ${i.tone}`}>
                <Icon />
              </span>
              <span className="item-main">
                {i.text}
                {i.detail && (
                  <span className="item-detail clamp" title={i.detail}>
                    {i.detail}
                  </span>
                )}
              </span>
              <ChevronRight />
            </Link>
            {i.action && <div className="item-actions">{i.action}</div>}
          </li>
        );
      })}
    </ul>
  );
}

const RUNS_SHOWN = 3;
const MAIL_SHOWN = 25;
// The sources of Needs you, in the order needsYouOf reads them, each with the
// board topics whose notice can change its answer. "runs" is announced for
// more than the board, so only a source with a topic of its own leaves it
// out: the apply list (as Ready to apply), the job emails, and the follow-ups,
// which a synced mail moves too. What Needs you only counts (email status
// changes, suggested answers and hand edits, wiki drafts, held signals) is one
// read of the counts.
const SOURCES: { name: string; topics: string[]; load: () => Promise<unknown> }[] = [
  { name: "model routing", topics: ["runs"], load: () => get("/api/llm/routing", llmRouting) },
  { name: "postings ready", topics: ["tracker"], load: () => get("/api/apply/list", applyList, { pageSize: 1 }) },
  { name: "waiting runs", topics: ["runs", "inbox"], load: () => get("/api/needs-you", needsYouInbox) },
  { name: "things to review", topics: ["answerbank", "gmail", "runs"], load: () => get("/api/needs-you/counts", needsYouCounts) },
  { name: "the inbox", topics: ["runs", "inbox"], load: () => get("/api/inbox", inboxPage, { status: "pending", pageSize: 1 }) },
  { name: "job emails", topics: ["gmail"], load: () => get("/api/gmail/inbox", gmailInboxPage, { lane: "do_now", limit: MAIL_SHOWN }) },
  { name: "follow-ups due", topics: ["tracker", "gmail"], load: () => get("/api/tracker/followups", followupList) },
  { name: "interviews", topics: ["tracker", "gmail", "runs"], load: () => get("/api/interviews/soon", interviewsSoon) },
  { name: "Ask Agent questions", topics: ["runs"], load: () => get("/api/agent/confirmations", confirmationList) },
  { name: "Ask Agent tasks", topics: ["runs"], load: () => get("/api/agent/tasks", agentTaskList) },
];
const NEEDS_TOPICS = [...new Set(SOURCES.flatMap((s) => s.topics))];

// A read of the sources: each one's answer, with the board revisions and the
// count of writes sent when the read began.
interface SourcesRead {
  all: PromiseSettledResult<unknown>[];
  at: Pick<BoardState, "revisions" | "epoch">;
  writes: number;
}

// readSources reads every source, or after an earlier read only those a
// notice since then can have changed: a source whose topic moved, one that
// failed, and all of them after a write, another server run, or with no
// revisions to compare. The other answers are the earlier read's.
async function readSources(prior?: SourcesRead, at: SourcesRead["at"] = { revisions: null, epoch: null }): Promise<SourcesRead> {
  const writes = writesSent();
  const [was, now] = [prior?.at.revisions, at.revisions];
  const comparable = prior && was && now && prior.at.epoch === at.epoch && prior.writes === writes;
  const all = await Promise.allSettled(
    SOURCES.map((s, i) => {
      const had = prior?.all[i];
      return comparable && had?.status === "fulfilled" && s.topics.every((t) => was[t] === now[t]) ? had.value : s.load();
    }),
  );
  return { all, at, writes };
}

// needsYouOf gathers what waits on the user across the pages that own it:
// interviews soon, runs that stopped for an answer, Ask conversations holding
// a call or a task's question, job emails to act on now, model tasks without a usable route,
// answers, wiki drafts, signals and email status changes to review, and
// postings to apply to or evaluate. A source that cannot be read reads as
// empty and is named in `unread`; only when none answers is it an error.
function needsYouOf(all: PromiseSettledResult<unknown>[]) {
  if (all.every((r) => r.status === "rejected")) throw (all[0] as PromiseRejectedResult).reason;
  const v = <T,>(i: number, empty: unknown) => (all[i].status === "fulfilled" ? all[i].value : empty) as T;
  return {
    routing: v<LLMRouting>(0, { presets: [], tasks: [], providers: [] }),
    ready: v<ApplyList>(1, { lanes: [] }),
    waiting: v<NeedsYouInbox>(2, { items: [] }),
    counts: v<NeedsYouCounts>(3, { emailConfirmations: 0, wikiDrafts: 0, heldSignals: 0, suggestedAnswers: 0, answerEdits: 0 }),
    inbox: v<InboxPage>(4, { total: 0 }),
    mail: v<GmailInboxPage>(5, { threads: [] }),
    // A follow-up and a reply check are the user's to send; the cadence
    // notes that ask nothing today are not.
    followups: v<FollowupList>(6, { suggestions: [] }).suggestions.filter((s) => s.kind === "follow_up" || s.kind === "reply_check"),
    interviews: v<InterviewsSoon>(7, { items: [] }).items,
    cards: v<ConfirmationList>(8, { cards: [] }).cards,
    tasks: v<AgentTaskList>(9, { tasks: [] }).tasks.filter(open),
    unread: SOURCES.filter((_, i) => all[i].status === "rejected").map((s) => s.name),
  };
}

// loadNeedsYou reads every source once, for a caller that does not follow the
// board (the palette, the next-item command).
export async function loadNeedsYou() {
  return needsYouOf((await readSources()).all);
}

// needsYouItems is Needs you's list in its order. The card names the first
// runs and one conversation and counts the rest in a line; `every` names each,
// for the next-item command (Mod+Shift+N), so the walk and the card agree.
export function needsYouItems({ routing, ready, waiting, counts, inbox, mail, followups, interviews, cards, tasks }: Awaited<ReturnType<typeof loadNeedsYou>>, every = false): Item[] {
  const items: Item[] = [];
  // An item the agent can carry out offers "Let the agent do it", or opens
  // the task already working on it. Without a model for the agent (none
  // routed, or the demo's offline simulation) the agent cannot run, so no
  // item offers it.
  const agentOff = noAgentModel(routing) !== "";
  const letAgent = (idea: string) => (agentOff ? undefined : <LetAgentDo idea={idea} task={tasks.find((t) => t.idea === idea)} />);
  // An interview today or tomorrow leads, its prep drawer one press away;
  // one already over today asks for the debrief until it is written.
  const today = new Date().getDate();
  for (const i of interviews) {
    const at = i.stage.at ?? "";
    const day = new Date(at).getDate() === today ? "today" : "tomorrow";
    const over = Date.parse(at) <= Date.now();
    if (over && i.debriefed) continue;
    const what = `${i.company}, ${humanize(i.stage.type)}`;
    items.push({
      key: `interview-${i.candidacyId}-${i.stage.id}`, to: `/tracker?interview=${i.candidacyId}`, tone: over || i.packed ? "info" : "warn", icon: MessagesSquare,
      text: over ? `Write the debrief of today's interview: ${what}` : `Interview ${day} at ${formatDateTime(at).slice(11)}: ${what}`,
      detail: over ? "Note what they asked while you remember it; the next stage's prep pack uses it." : i.packed ? "The prep pack is ready." : "No prep pack yet.",
    });
  }
  // Repeated runs of one posting stopped for one reason are one item,
  // as in the Runs inbox.
  const groups = groupWaiting(waiting.items);
  for (const g of every ? groups : groups.slice(0, RUNS_SHOWN)) {
    const e = g.items[0].entry;
    const why = plainDetail(e.detail, e.reason, e.kind)?.line ?? (e.state === "blocked" ? "Blocked" : "Needs you");
    const text = g.items.length > 1 ? `${e.title} (${g.items.length} runs)` : e.title;
    // An item whose fix is made on another page opens that page.
    items.push({ key: `run-${e.id}`, to: fixOf(e)?.to ?? `/runs/${e.id}`, text, detail: why, tone: e.state === "blocked" ? "bad" : "warn", icon: CircleAlert, action: <>{e.focusTab && <ShowInPanel tabId={e.focusTab} small />}{!agentOff && <LetAgentFinish entry={e} compact />}</>, runs: g.items.map((i) => i.entry.id) });
  }
  const rest = every ? [] : groups.slice(RUNS_SHOWN).flatMap((g) => g.items);
  const more = rest.length;
  // Quick-screen reviews are decided in the Runs inbox, one press each.
  const reviews = more > 0 && rest.every((i) => i.entry.kind === "evaluate" && i.entry.reason === "review");
  if (more > 0) items.push({ key: "runs-more", to: "/runs", text: reviews ? `${plural(more, "more posting waits", "more postings wait")} for your decision: evaluate in full or skip` : `${plural(more, "more run waits", "more runs wait")} for you`, tone: "warn", icon: Activity });
  // A conversation holding two calls is one item.
  const talks = cards.filter((c, i) => cards.findIndex((o) => o.sessionId === c.sessionId) === i);
  if (talks.length > 1 && !every) items.push({ key: "ask", to: "/ask", text: `${talks.length} Ask Agent conversations wait for your answer`, tone: "warn", icon: MessageSquareText });
  else for (const c of talks) items.push({ key: `ask-${c.id}`, to: c.sessionId ? `/ask?conversation=${encodeURIComponent(c.sessionId)}` : "/ask", text: `The Ask Agent waits for your answer: ${c.summary}`, tone: "warn", icon: MessageSquareText });
  // A delegated task that stopped on a question waits for the user's answer.
  for (const t of tasks.filter((t) => t.state === "waiting_you" && t.question)) {
    items.push({ key: `task-${t.id}`, to: `/ask?conversation=${encodeURIComponent(t.sessionId)}`, text: `A task the agent works on asks you: ${t.title}`, detail: t.question, tone: "warn", icon: MessageSquareText });
  }
  const [mail1, urgent] = [mail.threads[0], mail.threads.length];
  // One email opens itself; several open the filtered inbox.
  if (mail1) items.push({ key: "mail", to: `/gmail?lane=do_now${urgent === 1 ? `&thread=${encodeURIComponent(mail1.threadId)}` : ""}`, text: mail.next ? `More than ${MAIL_SHOWN} job emails need you now` : `${plural(urgent, "job email needs", "job emails need")} you now`, detail: `${mail1.employer}${mail1.role ? `: ${mail1.role}` : ""}, ${mail1.subject}`, tone: "warn", icon: Mail });
  if (followups.length > 0) items.push({ key: "followups", to: "/tracker#tracker-followups", text: followups.length === 1 ? `A follow-up to ${followups[0].company} is due` : `${followups.length} follow-ups are due`, tone: "info", icon: BellRing, action: letAgent("draft_followups") });
  // A command-line preset has no embeddings and no native tool calling, so
  // the tasks that need them are unrouted by design, not by a fault.
  const cli = routing.presets.find((p) => p.name === routing.preset)?.kind === "cli";
  const lacking = routing.tasks.filter((t) => t.unrouted || t.broken);
  const needsApi = lacking.filter((t) => cli && !t.broken && t.unrouted?.startsWith("the cli kind has no ")).length;
  const broken = lacking.length - needsApi;
  // A new workspace has no provider and no preset: that is the setup step,
  // not a routing fault. Its opt-in tasks are off rather than unrouted, so
  // the count of lacking tasks does not tell it apart.
  const unset = routing.providers.length === 0 && !routing.preset;
  if (unset && broken > 0) items.push({ key: "routing", to: "/setup?step=llm", text: "No language model is set up yet, so nothing can be evaluated or written", tone: "bad", icon: CircleAlert });
  else if (broken > 0) items.push({ key: "routing", to: "/settings#settings-models", text: `${broken} model ${broken === 1 ? "task has" : "tasks have"} no usable route`, tone: "bad", icon: CircleAlert });
  const { emailConfirmations: offers, suggestedAnswers: pending, answerEdits: edits, wikiDrafts: drafts, heldSignals: held } = counts;
  if (offers > 0) items.push({ key: "offers", to: "/gmail?tab=review", text: `${plural(offers, "email suggests a status change", "emails suggest status changes")} for you to confirm`, tone: "info", icon: Mail });
  if (pending > 0) items.push({ key: "answers", to: "/answer-bank?tab=candidates", text: `${plural(pending, "suggested answer waits", "suggested answers wait")} for review`, tone: "info", icon: ClipboardCheck });
  if (edits > 0) items.push({ key: "edits", to: "/answer-bank?tab=candidates", text: `Use ${edits === 1 ? "an answer" : `${edits} answers`} you changed by hand from now on?`, tone: "info", icon: PenLine });
  if (drafts > 0) items.push({ key: "drafts", to: "/wiki?tab=pages", text: `${plural(drafts, "wiki draft waits", "wiki drafts wait")} for review`, tone: "info", icon: NotebookPen });
  if (held > 0) items.push({ key: "signals", to: "/leads?tab=review", text: `${plural(held, "hiring signal is", "hiring signals are")} held for review`, tone: "info", icon: Radar });
  const n = readyCount(ready);
  if (n > 0) items.push({ key: "apply", to: "/apply-next?lane=apply-now,selective", text: `${plural(n, "posting is", "postings are")} ready to apply`, tone: "info", icon: Send, action: letAgent("apply_ready") });
  if (inbox.total > 0) items.push({ key: "inbox", to: "/discovery?tab=inbox", text: `${plural(inbox.total, "posting waits", "postings wait")} in the inbox to be evaluated`, tone: "info", icon: Inbox, action: letAgent("evaluate_inbox") });
  if (needsApi > 0) items.push({ key: "api", to: "/settings#settings-models", text: `${needsApi} model ${needsApi === 1 ? "task needs" : "tasks need"} an API provider (the command-line preset covers completion only)`, tone: "info", icon: Info });
  return items;
}

// NeedsYou is the card of that list. Its interviews say today or tomorrow, so
// it reads again when the local day turns while the page stays open, and an
// interview whose time comes asks for its debrief from then on: one timer to
// the next interview, no polling.
// A notice reads again only the sources of its topic (readSources); the day
// turning reads them all.
function NeedsYou() {
  const day = useLocalDay();
  const board = useContext(BoardContext);
  const last = useRef<{ day: string; read: SourcesRead }>(undefined);
  const [res, retry] = useResource(async () => {
    const read = await readSources(last.current?.day === day ? last.current.read : undefined, board.getState());
    last.current = { day, read };
    return needsYouOf(read.all);
  }, [day], NEEDS_TOPICS, { keep: "home.needs" });
  const [, passed] = useState(0);
  const next = Math.min(...(res.data?.interviews ?? []).map((i) => Date.parse(i.stage.at ?? "")).filter((t) => t > Date.now()));
  useEffect(() => {
    if (!Number.isFinite(next)) return;
    const timer = setTimeout(() => passed((n) => n + 1), next - Date.now() + 100);
    return () => clearTimeout(timer);
  }, [next]);
  return (
    <Panel title="Needs you" id="home-needs" res={res} retry={retry} icon={Inbox}>
      {(data) => {
        const items = needsYouItems(data);
        return (
          <>
            {items.length === 0 ? <Empty>Nothing {data.unread.length > 0 ? "else " : ""}needs you right now.</Empty> : <ItemList items={items} />}
            {data.unread.length > 0 && (
              <p className="more-line muted">
                Couldn't read {data.unread.join(", ")}.{" "}
                <Button variant="ghost" size="sm" onClick={retry}>
                  Try again
                </Button>
              </p>
            )}
          </>
        );
      }}
    </Panel>
  );
}

// StartFromPost evaluates a pasted posting: one web address is its URL,
// anything else its text. The command line takes the text in --text or --file.
// An address /?post=... (the palette's "Evaluate this posting") fills the box
// and leaves the press to the user.
function StartFromPost() {
  const [paste, setPaste] = useState("");
  const [params, setParams] = useSearchParams();
  const offered = params.get("post");
  useEffect(() => {
    if (offered == null) return;
    setPaste(offered);
    setParams({ post: undefined }, { replace: true });
    // After the shell has moved focus to a newly opened page.
    setTimeout(() => document.getElementById("home-post")?.focus());
  }, [offered, setParams]);
  const held = useUnsaved(paste.trim() !== "");
  const [started, setStarted] = useState<{ name: string; jobId: string } | null>(null);
  const start = useAction();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const v = paste.trim();
    const url = /^https?:\/\/\S+$/i.test(v);
    const body = url ? { url: v } : { text: v };
    setStarted(null);
    const got = await start.run(() => request("POST", "/api/evaluations", runAccepted, { body, headers: { "Idempotency-Key": newRequestId() } }));
    if (!got) return;
    setStarted({ name: url ? `the posting at ${new URL(v).host}` : "the pasted posting", jobId: got.runId });
    setPaste("");
  };
  return (
    <section aria-labelledby="home-paste" className="panel">
      <header className="panel-head">
        <h2 id="home-paste">
          <ClipboardPaste className="panel-icon" />
          Start from a job post
        </h2>
      </header>
      <form onSubmit={submit} className="form-grid" aria-label="Evaluate a posting">
        <label>
          Posting URL or text
          <textarea id="home-post" className="editor short" rows={3} aria-keyshortcuts={ariaKeys("Mod+Enter")} value={paste} onChange={(e) => setPaste(e.target.value)} onKeyDown={(e) => e.key === "Enter" && (e.metaKey || e.ctrlKey) && e.currentTarget.form?.requestSubmit()} placeholder="https://jobs.example.com/123, or the posting's text" />
        </label>
        <p className="help">The evaluation scores the posting against your profile and writes a report. {keyLabel("Mod+Enter")} starts it.</p>
        <Button type="submit" variant="primary" size="sm" disabled={start.busy || paste.trim() === ""}>
          Evaluate
        </Button>
        {start.error && <ErrorNote error={start.error} />}
        {started && <EvaluationNote name={started.name} jobId={started.jobId} />}
      </form>
      {held}
    </section>
  );
}

const RUNNING_SHOWN = 5;
// A run's progress inside its state is silent on the board stream, so the
// card asks again while one works, as the Runs page does.
const RUNNING_POLL_MS = 5000;

// The list is cut at what the card shows; the board's summary counts them all.
function RunningNow() {
  const [res, retry] = useResource(async () => {
    const [list, summary] = await Promise.all([get("/api/runs", boardEntryList, { state: "working", limit: RUNNING_SHOWN }), get("/api/runs/summary", boardSummary)]);
    return { entries: list.entries, total: Math.max(summary.working, list.entries.length) };
  }, [], ["runs"], { keep: "home.working" });
  useInterval(retry, RUNNING_POLL_MS, (res.data?.entries.length ?? 0) > 0);
  return (
    <Panel title="Running now" id="home-running" res={res} retry={retry} icon={Activity} actions={res.data && res.data.total > 0 && <Chip>{res.data.total}</Chip>}>
      {({ entries, total }) =>
        entries.length === 0 ? (
          <Empty>Nothing is running.</Empty>
        ) : (
          <>
            <ItemList
              items={entries.map((e) => ({
                key: e.id,
                to: `/runs/${e.id}`,
                text: e.title,
                detail: `${humanize(e.kind)}${stageDoing(e.stage, true) ? `, ${stageDoing(e.stage, true)}` : ""}, running for ${durationLabel(e.startedAt, undefined)}`,
                tone: "info",
                icon: Activity,
              }))}
            />
            {total > RUNNING_SHOWN && (
              <p className="more-line">
                <Link to="/runs?status=working">All {total} running</Link>
              </p>
            )}
          </>
        )
      }
    </Panel>
  );
}


// DayPart is one part of the day, each line a link to the page it is about. A
// busy day shows its first lines and the rest behind Show more, so the card
// does not push the rest of Home a screen down.
const DAY_SHOWN = 6;
function DayPart({ head, lines }: { head: string; lines: DayLine[] }) {
  const { shown, hidden, more } = useCapped(lines, DAY_SHOWN);
  if (lines.length === 0) return null;
  return (
    <section className="day-part" aria-label={head}>
      <h3>{head}</h3>
      <ul className="plain change-list">
        {shown.map((l, i) => (
          <li key={i}>
            <Link to={l.link}>{l.text}</Link>
          </li>
        ))}
      </ul>
      <ShowMore hidden={hidden} onMore={more} />
    </section>
  );
}

// Today is what was done for the user today and what waits on employers, in
// the words the day summary, Telegram's /today and the Ask Agent use, from one
// read. What is left for the user (the day's "left" lines) is Needs you's list
// above, so it is not told twice here; the channels without that list keep it.
function Today() {
  const day = useLocalDay();
  const [res, retry] = useResource(() => get("/api/day", dayView), [day], ["tracker", "runs", "scans", "gmail"], { keep: "home.day" });
  return (
    <Panel title="Today" id="home-today" res={res} retry={retry} icon={History}>
      {(day) => {
        const c = day.counts;
        return (
          <>
            {day.did.length === 0 ? <Empty>Nothing was done for you today.</Empty> : <DayPart head="Done for you" lines={day.did} />}
            <DayPart head="Waiting on employers" lines={day.employers} />
            <ul className="plain mini-stats" aria-label="Today's counts">
              <li>
                <Link to={`/discovery?tab=inbox&since=${day.date}`}>
                  <strong>{c.found}</strong> new
                </Link>
              </li>
              <li>
                <Link to={`/reports?since=${day.date}`}>
                  <strong>{c.evaluated}</strong> evaluated
                </Link>
              </li>
              <li>
                <Link to="/apply-next?lane=apply-now,selective">
                  <strong>{c.ready}</strong> ready to apply
                </Link>
              </li>
              <li>
                <Link to={`/tracker?reached=applied&since=${day.date}`}>
                  <strong>{c.appliedByUser + c.appliedByAgent}</strong> applied{c.appliedByAgent > 0 && `, ${c.appliedByAgent} by the agent`}
                </Link>
              </li>
            </ul>
          </>
        );
      }}
    </Panel>
  );
}

// The server's components, by the names the health route uses, and where a
// component that is not working is set up or looked at.
const COMPONENTS: Record<string, { name: string; to: string }> = {
  database: { name: "Database", to: "/settings#settings-advanced" },
  scheduler: { name: "Scheduler", to: "/discovery?tab=schedules" },
  browser: { name: "Browser windows", to: "/browser" },
  llm: { name: "Language model", to: "/settings#settings-models" },
  gmail: { name: "Gmail", to: "/settings#settings-gmail" },
  remote: { name: "Remote access", to: "/settings#settings-remote" },
  "computer-use": { name: "Computer use", to: "/agent-control" },
};

// Health states in words; "ok" would read as a bare token.
const HEALTH_WORDS: Record<string, string> = { ok: "Working", degraded: "Degraded", down: "Down", unconfigured: "Not set up", starting: "Starting" };

function healthLook(status: string): { tone: Tone; icon: LucideIcon } {
  if (status === "ok") return { tone: "good", icon: CircleCheck };
  if (status === "unconfigured") return { tone: "neutral", icon: CircleDashed };
  if (status === "down") return { tone: "bad", icon: CircleX };
  return { tone: "warn", icon: CircleAlert };
}

function Server() {
  const [res, retry] = useResource(() => get("/api/health", health), [], ["runs"], { keep: "home.health" });
  // While the server does not answer, its last answer is history, not its state.
  const { reconnecting } = useBoardState();
  const overall = reconnecting ? "Not answering" : res.data && (res.data.status === "ok" ? "Running" : (HEALTH_WORDS[res.data.status] ?? humanize(res.data.status)));
  return (
    <Panel title="Server" id="home-health" res={res} retry={retry} icon={ServerIcon} actions={res.data && <Chip tone={reconnecting ? "bad" : res.data.status === "ok" ? "good" : "warn"}>{overall}</Chip>}>
      {(h) => (
        <>
          <p className="muted">Version {h.version}{reconnecting && ". Last known state of its parts:"}</p>
          {h.components && h.components.length > 0 && (
            <ul className={`plain item-list health-list${reconnecting ? " stale-data" : ""}`}>
              {h.components.map((c) => {
                const look = healthLook(c.status);
                const Icon = look.icon;
                // Remote access that is off is a choice, not a missing setup;
                // a detail that only repeats the state is left out.
                const word = c.detail === "off" ? "Off" : (HEALTH_WORDS[c.status] ?? humanize(c.status));
                const fix = c.status !== "ok" && word !== "Off" ? COMPONENTS[c.name]?.to : undefined;
                const body = (
                  <>
                    <span className={`status-icon ${look.tone}`}>
                      <Icon />
                    </span>
                    <span className="item-main">{COMPONENTS[c.name]?.name ?? humanize(c.name)}</span>
                    <Chip tone={look.tone}>{word}</Chip>
                    {fix && <ChevronRight />}
                    {c.detail && !word.toLowerCase().includes(c.detail.toLowerCase()) && <span className="item-detail">{c.detail}</span>}
                  </>
                );
                return <li key={c.name}>{fix ? <Link to={fix} className="item-link">{body}</Link> : body}</li>;
              })}
            </ul>
          )}
        </>
      )}
    </Panel>
  );
}

export default function Home() {
  return (
    <div className="home-page">
      <PageHeader path="/">Your job search, one good step at a time.</PageHeader>
      <CareerCover />
      <SetupCard />
      {/* The DOM runs in the order a narrow page shows: what needs you, then
          the counts. Two columns stack independently, so a long list in one
          never leaves a gap in the other; on a narrow page they merge into
          one, led by Needs you. A wide page lifts the counts above them. */}
      <div className="home-grid">
        <div className="home-col">
          <NeedsYou />
          <Today />
        </div>
        <div className="home-col">
          <RunningNow />
          <StartFromPost />
        </div>
      </div>
      <div className="stats">
        <ReadyToApply />
        <TrackerCounts />
        <Spend />
      </div>
      <WeekFunnel className="week" />
      <Server />
    </div>
  );
}
