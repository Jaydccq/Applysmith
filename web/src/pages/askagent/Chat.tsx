import { useEffect, useId, useRef, useState, type KeyboardEvent, type RefObject } from "react";
import { del, get, patch, post } from "../../api/client";
import { chatCancelResult, chatDeleted, chatSession, chatSessionDetail, chatSessionPage, chatSteerAccepted, chatTurnAccepted } from "../../api/decoders_agent";
import { llmRouting } from "../../api/decoders";
import { useInterval, useResource } from "../../hooks";
import { leave, useSearchParams } from "../../router";
import { ConfirmDialog } from "../../ui/Modal";
import { useUnsaved } from "../../ui/SaveBar";
import { Empty, ErrorNote, Panel, Loading, ModelsLinked, ShowMore, formatDate, formatDateTime, useCapped } from "../../ui/parts";
import { MaxChatMessage, type ChatMessage, type ChatSession, type ChatSessionPage, type ConfirmationCard, type LLMRouting } from "../../wire";
import { useAction } from "../action";
import { RequestPanel, SaidNote, type Said } from "./Cards";
import { useLiveTurn } from "./live";
import { clockIn, dayGroup } from "../runs/format";
import { TaskPanel } from "./Task";
import { Transcript } from "./Transcript";
import { EmptyState } from "../../ui/EmptyState";
import { ArrowUp, MessageSquareText, Plus } from "lucide-react";

const NEW = "new";

const PAGE = 200;
// SHOWN is how many conversations the list draws before "Show more".
const SHOWN = 20;
const SEARCH_DELAY_MS = 250;
const COUNT_FROM = MaxChatMessage * 0.9;

// CHANNEL says where a conversation began when it was not this dashboard.
const CHANNEL: Record<string, string> = { telegram: "Telegram", scheduled: "Scheduled prompt", digest: "Gmail" };

// lastMoved is when a conversation last changed, the way the runs board says
// it: "Today 14:05", "Yesterday 09:12", "Mon 14:05" within the past week, and
// the date before that.
function lastMoved(iso: string, now = Date.now()): string {
  const today = new Date(now);
  if (Date.parse(iso) < new Date(today.getFullYear(), today.getMonth(), today.getDate() - 6).getTime()) return formatDate(iso);
  const group = dayGroup(iso, now);
  const time = clockIn(iso, group);
  return group === "Earlier this week" ? time : `${group} ${time}`;
}

interface SessionsProps {
  page: ChatSessionPage;
  searching: boolean;
  query: string;
  onQuery: (q: string) => void;
  current: string | undefined;
  onOpen: (id: string | undefined) => void;
  reload: () => void;
}

// Sessions lists the conversations the server returned for the search text:
// the server matches titles and message text, so the list is not filtered here.
// The most recent ones are drawn first, the rest behind "Show more"; the open
// conversation is always drawn, however old it is.
function Sessions({ page, searching, query, onQuery, current, onOpen, reload }: SessionsProps) {
  const sessions = page.sessions;
  const capped = useCapped(sessions, SHOWN);
  const kept = capped.shown.some((s) => s.id === current) ? [] : sessions.filter((s) => s.id === current);
  const shown = [...capped.shown, ...kept];
  const [renaming, setRenaming] = useState<ChatSession | null>(null);
  const [title, setTitle] = useState("");
  const [removing, setRemoving] = useState<ChatSession | null>(null);
  const act = useAction();
  const search = useId();

  const rename = async (s: ChatSession) => {
    const done = await act.run(() => patch(`/api/chat/sessions/${encodeURIComponent(s.id)}`, chatSession, { title: title.trim() }));
    if (done) {
      setRenaming(null);
      reload();
    }
  };
  const remove = async (s: ChatSession) => {
    setRemoving(null);
    const done = await act.run(() => del(`/api/chat/sessions/${encodeURIComponent(s.id)}`, chatDeleted));
    if (done) {
      if (s.id === current) onOpen(undefined);
      reload();
    }
  };

  return (
    <nav aria-label="Conversations" className="rail">
      <button type="button" className="rail-new" onClick={() => onOpen(undefined)}>
        <Plus aria-hidden="true" />
        New conversation
      </button>
      <label htmlFor={search} className="sr-only">
        Search conversations
      </label>
      <input id={search} type="search" placeholder="Search conversations" value={query} onChange={(e) => onQuery(e.target.value)} />
      {sessions.length === 0 ? (
        <Empty>{searching ? "No conversation matches." : "No conversation yet."}</Empty>
      ) : (
        <ul className="plain">
          {shown.map((s) => (
            <li key={s.id}>
              <button type="button" className="rail-session" aria-current={s.id === current ? "true" : undefined} onClick={() => onOpen(s.id)}>
                <span className="rail-title" title={s.title}>{s.title}</span>
                <span className="muted">
                  {s.channel !== "dashboard" && `${CHANNEL[s.channel] ?? s.channel} · `}
                  <time dateTime={s.updatedAt} title={formatDateTime(s.updatedAt)}>{lastMoved(s.updatedAt)}</time>
                </span>
              </button>
              {s.id === current && (
                <span className="actions">
                  <button type="button" className="small" onClick={() => { setRenaming(s); setTitle(s.title); }}>
                    Rename
                  </button>
                  <button type="button" className="small danger-outline" disabled={act.busy} onClick={() => setRemoving(s)}>
                    Delete
                  </button>
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      <ShowMore hidden={capped.hidden - kept.length} onMore={capped.more} what="conversations" />
      {page.total > sessions.length && (
        <p className="muted">
          Showing {sessions.length} of {page.total}. {searching ? "Narrow the search to see the rest." : "Search to find an older conversation."}
        </p>
      )}
      {renaming && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void rename(renaming);
          }}
          className="inline-form"
        >
          <label htmlFor={`${search}-title`}>New title</label>
          <input id={`${search}-title`} value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} />
          <span className="actions">
            <button type="submit" disabled={act.busy || title.trim() === ""}>
              Save title
            </button>
            <button type="button" onClick={() => setRenaming(null)}>
              Cancel
            </button>
          </span>
        </form>
      )}
      {act.error && <ErrorNote error={act.error} />}
      {removing && (
        <ConfirmDialog title={`Delete the conversation "${removing.title}"`} consequence="Its messages are removed. A conversation with a running turn cannot be deleted." verb="Delete conversation" onCancel={() => setRemoving(null)} onConfirm={() => void remove(removing)} />
      )}
    </nav>
  );
}

// SUGGESTIONS start a new conversation with something the agent's read and
// draft tools answer; a choice is put in the composer to edit, not sent.
const SUGGESTIONS = [
  "What did I do today, and what is waiting for me?",
  "Which postings in my pipeline should I apply to next?",
  "Which applications need a follow-up? Draft the first one.",
  "What has Gmail brought in this week?",
];

// Composer stays mounted while a request panel takes its place (hidden), so a
// draft survives the decision. The keyboard stays in the field after a send,
// also when the send was a click on the button.
function Composer({ draft, setDraft, running, busy, hidden, box, onSend, onCancel }: { draft: string; setDraft: (text: string) => void; running: boolean; busy: boolean; hidden: boolean; box: RefObject<HTMLTextAreaElement | null>; onSend: (text: string) => Promise<boolean>; onCancel: () => void }) {
  const field = useId();
  const send = async () => {
    const text = draft.trim();
    if (text === "" || busy) return;
    if (await onSend(text)) setDraft("");
    box.current?.focus();
  };
  // Enter that confirms an input method's candidate is not a send. WebKit
  // ends the composition before that keydown, so isComposing is already false
  // there; the key code 229 still marks it.
  const keys = (e: KeyboardEvent) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing && e.nativeEvent.keyCode !== 229) {
      e.preventDefault();
      void send();
    }
  };
  return (
    <form
      className="composer"
      hidden={hidden}
      onSubmit={(e) => {
        e.preventDefault();
        void send();
      }}
    >
      <label htmlFor={field} className="sr-only">
        {running ? "Steer the running turn" : "Message"}
      </label>
      {/* A click anywhere in the box, not just on the field's own lines, puts the cursor in it. */}
      <div className="composer-box" onClick={(e) => e.target === e.currentTarget && box.current?.focus()}>
        <textarea ref={box} id={field} rows={2} placeholder={running ? "Steer the running turn" : "Message the agent"} value={draft} maxLength={MaxChatMessage} onChange={(e) => setDraft(e.target.value)} onKeyDown={keys} />
        <div className="composer-chin">
          {/* The count matters only near the limit; a touch screen has no Enter to explain. */}
          <span className="muted">
            {draft.length > COUNT_FROM && `${draft.length} of ${MaxChatMessage}. `}
            <span className="composer-keys">Enter sends, Shift+Enter adds a line.</span>
          </span>
          {running && (
            <button type="button" className="ghost small" disabled={busy} onClick={onCancel}>
              Cancel turn
            </button>
          )}
          <button type="submit" disabled={busy || draft.trim() === ""}>
            {running ? "Send guidance" : "Send"}
            <ArrowUp aria-hidden="true" />
          </button>
        </div>
      </div>
    </form>
  );
}

// SENT_ID marks the message shown for what the user just sent, until the
// stored transcript arrives and replaces it.
const SENT_ID = -1;
const ACTIVE_POLL_MS = 2000;

// Request is the held call the conversation's request panel shows: focus says
// whether the panel may take the keyboard, minimised that Esc put it away.
interface Request {
  id: string;
  focus: boolean;
  minimised: boolean;
}

function Conversation({ id, prefill, cards, reloadCards, onOpen, onCreated, reloadSessions }: { id: string | undefined; prefill: string; cards: ConfirmationCard[]; reloadCards: () => void; onOpen: (id: string | undefined) => void; onCreated: (id: string) => void; reloadSessions: () => void }) {
  const [detail, reloadDetail] = useResource(() => (id ? get(`/api/chat/sessions/${encodeURIComponent(id)}`, chatSessionDetail) : Promise.resolve(null)), [id], ["runs"]);
  const act = useAction();
  const [note, setNote] = useState("");
  const [draft, setDraft] = useState(prefill);
  // An unsent message holds the page, hidden behind a request panel too: a
  // failed send keeps it, a sent one clears it.
  const ask = useUnsaved(draft.trim() !== "");
  const [sent, setSent] = useState<ChatMessage | null>(null);
  const [said, setSaid] = useState<Said>(null);
  const box = useRef<HTMLTextAreaElement>(null);
  const settle = () => {
    reloadDetail();
    reloadSessions();
    reloadCards();
  };
  const { live, stopped: stoppedLive, ended, attach } = useLiveTurn(settle, reloadCards);
  // The live reason of a stop is shown only until the stored turn says it.
  const stopped = detail.data?.turns.some((t) => t.id === ended && t.status === "error") ? null : stoppedLive;
  const followed = useRef<string | null>(null);

  // A turn that is running when the conversation opens (one just sent, a
  // scheduled or Telegram turn, or a reload of the page) is followed from its
  // first event. Each turn is followed once.
  // A turn whose stream delivered its final or error event is over, even when
  // the stored conversation could not be read again since.
  const named = detail.data?.activeTurnId;
  const active = named && named !== ended ? named : undefined;
  useEffect(() => {
    if (id && active && followed.current !== active) {
      followed.current = active;
      attach(id);
    }
  }, [id, active]);

  // The stream can end a moment before the server drops the turn, so the read
  // that follows the end may still name it. While one is named and none is
  // followed, ask again, so the page does not stay on "running" and the stored
  // answer is read once the server has let the turn go.
  useInterval(reloadDetail, ACTIVE_POLL_MS, live === null && Boolean(named));
  const running = live !== null || Boolean(active);

  // The oldest held call of this conversation waits in place of the composer.
  // Whether the user was typing in the composer is read as the card first
  // shows, before the panel hides it, so a card that arrives mid-sentence does
  // not take the keys that follow.
  const mine = id ? cards.filter((c) => c.sessionId === id) : [];
  const [request, setRequest] = useState<Request | null>(null);
  const head = mine[0]?.id ?? null;
  if (head !== (request?.id ?? null)) setRequest(head ? { id: head, focus: document.activeElement !== box.current, minimised: false } : null);
  const asking = request !== null && !request.minimised && mine.length > 0;
  // A hidden composer lets go of the keyboard, so keys typed after the panel
  // came reach nothing. When the panel goes (minimised, or its last card
  // decided) the keyboard goes back to the composer, unless the user has
  // already put it somewhere else.
  const back = useRef(false);
  const wasAsking = useRef(false);
  useEffect(() => {
    const at = document.activeElement;
    if (asking && at === box.current) box.current?.blur();
    if (wasAsking.current && !asking && (back.current || !at || at === document.body || at.id === "main")) box.current?.focus();
    wasAsking.current = asking;
    back.current = false;
  }, [asking]);

  const send = async (text: string): Promise<boolean> => {
    setNote("");
    setSaid(null);
    if (running && id) {
      const out = await act.run(() => post(`/api/chat/sessions/${encodeURIComponent(id)}/steer`, chatSteerAccepted, { text }));
      if (out) setNote(`Guidance queued. ${out.queued} waiting for the next step.`);
      return out !== undefined;
    }
    // The first message of a new conversation is shown where it will stay, so
    // the page does not blank while the conversation is created and read back.
    if (!id) setSent({ id: SENT_ID, role: "user", text, ok: true, createdAt: new Date().toISOString() });
    const out = await act.run(() => post(`/api/chat/sessions/${id ?? NEW}/messages`, chatTurnAccepted, { text }));
    if (!out) {
      setSent(null);
      return false;
    }
    if (out.sessionId !== id) {
      followed.current = out.turnId;
      attach(out.sessionId);
      onCreated(out.sessionId);
    } else reloadDetail();
    reloadSessions();
    return true;
  };

  const cancel = async () => {
    if (!id) return;
    const out = await act.run(() => post(`/api/chat/sessions/${encodeURIComponent(id)}/cancel`, chatCancelResult));
    if (out) setNote(out.note);
  };

  const fork = async (messageId: number) => {
    if (!id) return;
    const out = await act.run(() => post(`/api/chat/sessions/${encodeURIComponent(id)}/fork`, chatSession, { messageId }));
    if (out) {
      reloadSessions();
      onOpen(out.id);
    }
  };

  return (
    <div>
      {id && !sent && detail.status === "loading" && <Loading />}
      {detail.status === "error" && <ErrorNote error={detail.error} onRetry={reloadDetail} />}
      {id && <TaskPanel sessionId={id} onMoved={reloadDetail} />}
      {detail.data ? (
        <Transcript messages={detail.data.messages} turns={detail.data.turns} live={live} stopped={stopped} running={running} busy={act.busy} onFork={(m) => void fork(m)} onRetry={(text) => void send(text)} />
      ) : sent ? (
        <Transcript messages={[sent]} live={live} stopped={stopped} running={running} busy onFork={() => {}} />
      ) : (
        !id && (
          <EmptyState
            icon={MessageSquareText}
            title="Ask the agent about your search."
            action={
              <ul className="plain suggestions" aria-label="Suggestions">
                {SUGGESTIONS.map((text) => (
                  <li key={text}>
                    <button type="button" className="small" onClick={() => { setDraft(text); box.current?.focus(); }}>
                      {text}
                    </button>
                  </li>
                ))}
              </ul>
            }
          >
            A new conversation starts with your first message. The agent reads your tracker, reports and mail, drafts text and starts runs; a call that needs your approval waits here for your decision.
          </EmptyState>
        )
      )}
      <div aria-live="polite">
        {note && <p className="note ok">{note}</p>}
        <SaidNote said={said} />
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {request && mine.length > 0 && (asking ? (
        <RequestPanel
          cards={mine}
          autoFocus={request.focus}
          onDecided={(next) => {
            setSaid(next);
            reloadCards();
            reloadDetail();
          }}
          onTell={send}
          onMinimise={() => {
            back.current = true;
            setRequest({ ...request, minimised: true });
          }}
        />
      ) : (
        <p className="request-bar">
          <span>
            {mine.length === 1 ? "A request is waiting" : `${mine.length} requests are waiting`}: {mine[0].tool}.
          </span>
          <button
            type="button"
            className="small"
            onClick={() => {
              // Asked for by the user, so the panel takes the keys even from the composer.
              box.current?.blur();
              setRequest({ ...request, minimised: false, focus: true });
            }}
          >
            Show request
          </button>
        </p>
      ))}
      <Composer draft={draft} setDraft={setDraft} running={running} busy={act.busy} hidden={asking} box={box} onSend={send} onCancel={() => void cancel()} />
      {ask}
    </div>
  );
}

// noAgentModel says why the Ask Agent cannot answer yet, or "": every turn
// without a model for agent-kernel ends at no_model_route, so the page says
// so before a message is typed (the server's words, internal/agent).
export function noAgentModel(r: LLMRouting | undefined): string {
  if (!r) return "";
  if (r.offline) return "The Ask Agent needs a real model: the offline simulation stands in only for evaluations and drafts; choose one under Settings, Models.";
  return r.tasks.find((t) => t.task === "agent-kernel")?.unrouted ? "The Ask Agent has no model it can use; choose one under Settings, Models." : "";
}

export function Chat({ cards, reloadCards }: { cards: ConfirmationCard[]; reloadCards: () => void }) {
  const [routing] = useResource(() => get("/api/llm/routing", llmRouting), [], ["runs"]);
  const noModel = noAgentModel(routing.data);
  const [params, setParams] = useSearchParams();
  const id = params.get("conversation") ?? undefined;
  // A page that hands the user to the agent (the Answer Bank's Add) names a
  // message to start a new conversation with, for the user to finish and send.
  // It is read once and taken out of the address, so Back and a reload do not
  // put it back.
  const [prefill] = useState(() => (id ? "" : (params.get("draft") ?? "")));
  useEffect(() => {
    if (params.get("draft") !== null) setParams({ draft: undefined }, { replace: true });
  }, []);
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setSearch(query.trim()), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [query]);
  const [sessions, reloadSessions] = useResource(() => get("/api/chat/sessions", chatSessionPage, { limit: PAGE, q: search || undefined }), [search], ["runs"]);
  // Another conversation remounts this one, so it asks about an unsent message.
  const open = (next: string | undefined) => leave(() => setParams({ conversation: next }));
  // A conversation is remounted whenever the address names another one, except
  // for the one it has just created itself: that keeps its composer and its
  // first message on screen while the stored transcript is read back.
  const created = useRef<string | null>(null);
  const [shown, setShown] = useState({ id, key: 0 });
  if (shown.id !== id) setShown({ id, key: created.current === id ? shown.key : shown.key + 1 });
  useEffect(() => {
    created.current = null;
  }, [id]);
  const onCreated = (next: string) => {
    created.current = next;
    setParams({ conversation: next });
  };
  return (
    <Panel title="Conversations" id="ask-chat" res={sessions} retry={reloadSessions}>
      {(page) => (
        <>
        {noModel && <p className="note" role="status"><ModelsLinked text={noModel} /></p>}
        <div className="with-rail">
          <Sessions page={page} searching={search !== ""} query={query} onQuery={setQuery} current={id} onOpen={open} reload={reloadSessions} />
          <Conversation key={shown.key} id={id} prefill={shown.key === 0 ? prefill : ""} cards={cards} reloadCards={reloadCards} onOpen={open} onCreated={onCreated} reloadSessions={reloadSessions} />
        </div>
        </>
      )}
    </Panel>
  );
}
