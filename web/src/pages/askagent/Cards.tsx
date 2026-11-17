import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { get, post, type ApiError } from "../../api/client";
import { applySkipStatus, confirmationOutcome } from "../../api/decoders_agent";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import { asApiError, useResource, type Resource } from "../../hooks";
import { MaxChatMessage, type ConfirmationCard, type ConfirmationList } from "../../wire";
import { when } from "../action";

const SCOPE_LABEL: Record<string, string> = { conversation: "Approve for this conversation", today: "Approve for today" };
const SUBMIT_TIER = 4;

// A submit is approved once, in SubmitConfirm, or not at all: both the list's
// card and the conversation's request panel read the rule here. isSubmit says
// which cards it holds for, and widerScopes offers them no standing scope.
const isSubmit = (card: ConfirmationCard) => card.tier >= SUBMIT_TIER;
const widerScopes = (card: ConfirmationCard) => (isSubmit(card) ? [] : card.scopes.filter((s) => s !== "once"));

function Target({ card }: { card: ConfirmationCard }) {
  const parts = [card.company, card.role, card.host].filter(Boolean);
  return parts.length === 0 ? null : <p>{parts.join(" · ")}</p>;
}

// SkipListLine states whether the employer is on the batch skip list, read
// from the same list the batch gate uses.
function SkipListLine({ company }: { company: string }) {
  const [res] = useResource(() => get("/api/apply/skip-status", applySkipStatus, { company }), [company]);
  if (res.status === "loading") return <span role="status">Checking the batch skip list.</span>;
  if (res.status === "error") return <span role="status">The batch skip list could not be read.</span>;
  const { listed, entry } = res.data;
  return <span role="status">{listed ? `${company} is on your batch skip list (as ${entry}).` : `${company} is not on your batch skip list.`}</span>;
}

export type Said = { say: string } | { fail: ApiError } | null;

// SaidNote shows how the last decision on a card went.
export function SaidNote({ said }: { said: Said }) {
  if (said && "say" in said) return <p className="note ok">{said.say}</p>;
  if (said && "fail" in said) return <ErrorNote error={said.fail} />;
  return null;
}

// useDecision posts one decision on a card. An expired or handled card answers
// conflict; either way onDecided reloads the list so the card leaves, and the
// refusal stays visible. decide answers whether the server took the decision.
function useDecision(card: ConfirmationCard, onDecided: (said: Said) => void) {
  const [busy, setBusy] = useState(false);
  const decide = async (path: string, body?: unknown): Promise<boolean> => {
    setBusy(true);
    try {
      const out = await post(`/api/agent/confirmations/${encodeURIComponent(card.id)}/${path}`, confirmationOutcome, body);
      onDecided({ say: `${card.tool}: ${out.card.state}.${out.result ? ` ${out.result}` : ""}` });
      return true;
    } catch (e) {
      onDecided({ fail: asApiError(e) });
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { busy, decide };
}

// SubmitConfirm is the one way a submit is approved: a dialog that names the
// employer, role and host, checks the batch skip list and opens on Cancel.
function SubmitConfirm({ card, busy, onCancel, onConfirm }: { card: ConfirmationCard; busy: boolean; onCancel: () => void; onConfirm: () => void }) {
  return (
    <ConfirmDialog
      title="Approve a submit"
      consequence={
        <>
          {`This submits the application${card.company ? ` to ${card.company}` : ""}${card.role ? ` for ${card.role}` : ""}${card.host ? ` on ${card.host}` : ""}. It cannot be undone from here.`}{" "}
          {card.company ? <SkipListLine company={card.company} /> : <span>The employer is not named, so the skip list was not checked.</span>}
        </>
      }
      verb="Approve and submit"
      busy={busy}
      onCancel={onCancel}
      onConfirm={onConfirm}
    />
  );
}

function Card({ card, onDecided, onOpen }: { card: ConfirmationCard; onDecided: (said: Said) => void; onOpen: (sessionId: string) => void }) {
  const { busy, decide } = useDecision(card, onDecided);
  const [confirm, setConfirm] = useState(false);
  const wider = widerScopes(card);

  return (
    <li className="panel card-held">
      <h3>{card.summary || card.tool}</h3>
      <p className="chips">
        <Chip tone="neutral" raw>{card.tool}</Chip>
        <Chip tone={card.tier >= 3 ? "warn" : "neutral"}>tier {card.tier}</Chip>
        <Chip tone="warn">{card.code}</Chip>
      </p>
      <Target card={card} />
      <p>{card.reason}</p>
      <p>
        <strong>Why you are asked:</strong> <span className="pre">{card.purpose}</span>
      </p>
      <details>
        <summary>Arguments (redacted)</summary>
        <pre className="pre">{card.args}</pre>
      </details>
      <p className="muted">Expires {when(card.expiresAt)}</p>
      <div className="actions">
        <button type="button" disabled={busy} onClick={() => (isSubmit(card) ? setConfirm(true) : void decide("approve"))}>
          Approve once
        </button>
        {wider.map((s) => (
          <button key={s} type="button" disabled={busy} onClick={() => void decide("approve-standing", { scope: s })}>
            {SCOPE_LABEL[s] ?? `Approve for ${s}`}
          </button>
        ))}
        <button type="button" className="danger" disabled={busy} onClick={() => void decide("decline")}>
          Decline
        </button>
        {card.sessionId && (
          <button type="button" className="ghost" onClick={() => onOpen(card.sessionId)}>
            Open its conversation
          </button>
        )}
      </div>
      {confirm && (
        <SubmitConfirm
          card={card}
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={() => {
            setConfirm(false);
            void decide("approve");
          }}
        />
      )}
    </li>
  );
}

// PendingCards lists the held calls that wait for the user, oldest first,
// except those of the open conversation (inConversation), which wait in its
// request panel instead. An approval or a decline is a write; the list is
// reloaded after it so the card leaves, and a refusal (an expired card answers
// conflict) is shown on the card.
export function PendingCards({ res, reload, inConversation, onOpen }: { res: Resource<ConfirmationList>; reload: () => void; inConversation?: string; onOpen: (sessionId: string) => void }) {
  const [said, setSaid] = useState<Said>(null);
  // With nothing waiting (outside the open conversation) the panel would only
  // say so; it is left out, as lists hide what is empty.
  if (!said && res.data && res.data.cards.every((c) => c.sessionId === inConversation)) return null;
  return (
    <Panel title="Waiting for your decision" id="ask-cards" res={res} retry={reload}>
      {(list) => {
        const shown = list.cards.filter((c) => !inConversation || c.sessionId !== inConversation);
        return (
          <>
            <div aria-live="polite">
              <SaidNote said={said} />
            </div>
            {shown.length === 0 ? (
              <Empty>Nothing is waiting for you.</Empty>
            ) : (
              <ul className="plain stack">
                {shown.map((c) => (
                  <Card
                    key={c.id}
                    card={c}
                    onOpen={onOpen}
                    onDecided={(next) => {
                      setSaid(next);
                      reload();
                    }}
                  />
                ))}
              </ul>
            )}
          </>
        );
      }}
    </Panel>
  );
}

interface Choice {
  label: string;
  hint: string;
  path: "approve" | "approve-standing" | "decline";
  scope?: string;
}

const SCOPE_RANK: Record<string, number> = { conversation: 1, today: 2 };
const SCOPE_CHOICE: Record<string, { label: string; end: string }> = {
  conversation: { label: "Yes, and for the rest of this conversation", end: "this conversation ends" },
  today: { label: "Yes, and for today", end: "midnight" },
};

// choicesOf orders a card's answers from the narrowest scope to the widest,
// then No.
function choicesOf(card: ConfirmationCard): Choice[] {
  const submit = isSubmit(card);
  const wider = widerScopes(card).sort((a, b) => (SCOPE_RANK[a] ?? 9) - (SCOPE_RANK[b] ?? 9));
  return [
    submit
      ? { label: "Yes, review the submit", hint: "Opens the confirmation that names the employer. No key press alone submits.", path: "approve" }
      : { label: "Yes, once", hint: "Runs this call one time.", path: "approve" },
    ...wider.map((s): Choice => ({
      label: SCOPE_CHOICE[s]?.label ?? `Yes, and for ${s}`,
      hint: `Runs it now; later ${card.tool} calls on the same target run without asking until ${SCOPE_CHOICE[s]?.end ?? s}, 24 hours at most.`,
      path: "approve-standing",
      scope: s,
    })),
    { label: "No", hint: "Declines the call. Nothing runs.", path: "decline" },
  ];
}

const isTextField = (el: Element | null) =>
  el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement || (el instanceof HTMLInputElement && !["button", "checkbox", "radio", "submit"].includes(el.type)) || (el instanceof HTMLElement && el.isContentEditable);

// RequestPanel asks for the decision on the oldest held call of a conversation
// in place of its composer. Keys: 1 to 9 choose an answer (the number after
// the last one goes to the free-text field), Up and Down move, Enter chooses
// the highlighted answer and Esc minimises the panel without deciding. An
// approval of a submit only opens SubmitConfirm, whatever pressed it. "No, and
// tell the agent" declines, then hands the text to onTell (the conversation's
// own send: steering while a turn runs, else a new message).
// The panel takes the keyboard when a card arrives only when autoFocus says
// the user was not typing a message, and never from another text field, so a
// card that appears mid-sentence cannot be answered by the next key.
export function RequestPanel({ cards, autoFocus, onDecided, onTell, onMinimise }: {
  cards: ConfirmationCard[];
  autoFocus: boolean;
  onDecided: (said: Said) => void;
  onTell: (text: string) => Promise<boolean>;
  onMinimise: () => void;
}) {
  const card = cards[0];
  const choices = choicesOf(card);
  const submit = isSubmit(card);
  const tellAt = choices.length;
  const [at, setAt] = useState(0);
  const [tell, setTell] = useState("");
  const [confirm, setConfirm] = useState(false);
  const { busy, decide } = useDecision(card, onDecided);
  const root = useRef<HTMLElement>(null);
  const rows = useRef<(HTMLElement | null)[]>([]);
  const title = useId();
  const field = useId();

  useEffect(() => {
    setAt(0);
    setTell("");
    const active = document.activeElement;
    if (root.current?.contains(active) || (autoFocus && !isTextField(active))) rows.current[0]?.focus();
  }, [card.id]);

  const move = (i: number) => {
    const next = Math.min(Math.max(i, 0), tellAt);
    setAt(next);
    rows.current[next]?.focus();
  };
  const choose = (i: number) => {
    const c = choices[i];
    if (busy || !c) return;
    setAt(i);
    if (c.path === "approve" && submit) setConfirm(true);
    else void decide(c.path, c.scope ? { scope: c.scope } : undefined);
  };
  const tellAgent = async () => {
    const text = tell.trim();
    if (text === "" || busy) return;
    if (await decide("decline")) await onTell(text);
  };

  const keys = (e: KeyboardEvent<HTMLElement>) => {
    const target = e.target as HTMLElement;
    // The submit dialog sits inside the panel and answers its own keys.
    if (target.closest("dialog")) return;
    if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229 || e.metaKey || e.ctrlKey || e.altKey) return;
    const inField = target.id === field;
    if (e.key === "Escape") {
      e.preventDefault();
      onMinimise();
    } else if (e.key === "ArrowUp" || (e.key === "ArrowDown" && !inField)) {
      e.preventDefault();
      move(at + (e.key === "ArrowUp" ? -1 : 1));
    } else if (inField) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        void tellAgent();
      }
    } else if (e.key === "Enter") {
      // Enter on an answer chooses it; on any other button it is that button's own press.
      const row = rows.current.indexOf(target);
      if (row < 0 || row >= tellAt) return;
      e.preventDefault();
      choose(row);
    } else if (/^[1-9]$/.test(e.key) && Number(e.key) - 1 <= tellAt) {
      e.preventDefault();
      const i = Number(e.key) - 1;
      move(i);
      if (i < tellAt) choose(i);
    }
  };

  return (
    <section ref={root} className="request" aria-labelledby={title} onKeyDown={keys}>
      <header className="request-head">
        <h3 id={title}>{submit ? "Allow the agent to submit this application?" : `Allow the agent to run ${card.tool}?`}</h3>
        {cards.length > 1 && <span className="muted">1 of {cards.length}</span>}
      </header>
      <p className="muted">{card.summary || [card.company, card.role, card.host].filter(Boolean).join(" · ")}</p>
      <p className="label">The call, arguments redacted</p>
      <pre className="request-code">
        {card.tool} {card.args}
      </pre>
      <p>
        {card.reason} <span className="muted">Why you are asked:</span> <span className="pre">{card.purpose}</span>
      </p>
      <p className="chips">
        <Chip tone={card.tier >= 3 ? "warn" : "neutral"}>tier {card.tier}</Chip>
        <Chip tone="warn">{card.code}</Chip>
        <span className="muted">Expires {when(card.expiresAt)}</span>
      </p>
      <ol className="plain request-options" aria-label="Answers">
        {choices.map((c, i) => (
          <li key={c.label}>
            <button type="button" className="request-option" ref={(el) => void (rows.current[i] = el)} tabIndex={i === at ? 0 : -1} data-active={i === at ? "" : undefined} aria-disabled={busy || undefined} onClick={() => choose(i)} onFocus={() => setAt(i)}>
              <kbd aria-hidden="true">{i + 1}</kbd>
              <span className="request-label">{c.label}</span>
              <span className="request-hint">{c.hint}</span>
            </button>
          </li>
        ))}
        <li className="request-tell" data-active={at === tellAt ? "" : undefined}>
          <kbd aria-hidden="true">{tellAt + 1}</kbd>
          <label htmlFor={field} className="sr-only">
            No, and tell the agent what to do differently
          </label>
          <input id={field} ref={(el) => void (rows.current[tellAt] = el)} placeholder="No, and tell the agent what to do differently" value={tell} maxLength={MaxChatMessage} onChange={(e) => setTell(e.target.value)} onFocus={() => setAt(tellAt)} />
          <button type="button" className="small" disabled={busy || tell.trim() === ""} onClick={() => void tellAgent()}>
            Decline and send
          </button>
        </li>
      </ol>
      <p className="request-keys muted">
        <kbd>1</kbd> to <kbd>{tellAt + 1}</kbd> choose, <kbd>Up</kbd> <kbd>Down</kbd> move, <kbd>Enter</kbd> chooses, <kbd>Esc</kbd> minimises.
        {submit && " A submit is approved only in its confirmation dialog."}
      </p>
      {confirm && (
        <SubmitConfirm
          card={card}
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={() => {
            setConfirm(false);
            void decide("approve");
          }}
        />
      )}
    </section>
  );
}
