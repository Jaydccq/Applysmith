import { useState, type ReactNode } from "react";
import { get, patch, post } from "../../api/client";
import { candidacyDetail, candidacyPage, statusChangeResult } from "../../api/decoders";
import { gmailConfirmationDecided, gmailConfirmationList, gmailLinkConfirmed, gmailLinkList, gmailLinkRejected, gmailLinkRestored, gmailMoveList } from "../../api/decoders_gmail";
import { useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { Chip, Empty, ErrorNote, Panel, Loading, ShowMore, UndoNote, humanize, useCapped } from "../../ui/parts";
import type { GmailConfirmation, GmailLinkConfirm, GmailLinkItem, GmailOutcome } from "../../wire";
import { useAction, when } from "../action";
import { KINDS, reasons } from "./Inbox";

const outcomeText = (o: GmailOutcome) =>
  o.result === "changed" ? `Tracker status moved from ${o.fromStatus ?? "none"} to ${o.toStatus}.` : o.result === "confirmation" ? "The status moves to offer only when you confirm it under Review." : `The tracker was not changed${o.reason ? `: ${o.reason}` : ""}.`;

// moveBack puts a row back to the status a mail moved it from, as the user's
// correction, only while it still has the status the mail gave it. A row that
// moved on since is left alone.
async function moveBack(id: number, from: string, to: string) {
  const row = (await get(`/api/candidacies/${id}`, candidacyDetail)).entry;
  if (row.status !== to) throw new Error(`The application changed since (it is ${row.status} now), so it was left as it is. Change it on the Tracker.`);
  await patch(`/api/candidacies/${id}`, statusChangeResult, { status: from, expectedVersion: row.version });
  return true;
}

// Moved says what a mail did to its application and, when it moved the
// status, offers to put it back: a misread rejection is one press from undone.
// now is the row's status when known, with the server's word on whether the
// move is still the row's latest status change; one that is not is not offered back.
export function Moved({ o, lead = "", className, now }: { o: GmailOutcome; lead?: ReactNode; className?: string; now?: { status?: string; putBack: boolean } }) {
  const act = useAction();
  const [back, setBack] = useState(false);
  const { candidacyId: id, fromStatus: from, toStatus: to } = o;
  const on = now !== undefined && !now.putBack;
  return (
    <div aria-live="polite" className={className}>
      <p>
        {lead}
        {back ? `The application is back to ${from}.` : outcomeText(o)}
        {!back && on && now.status && ` It changed since and is ${now.status} now.`}{" "}
        {!back && !on && o.result === "changed" && id !== undefined && from && to && (
          <button type="button" className="small" disabled={act.busy} onClick={async () => setBack(!!(await act.run(() => moveBack(id, from, to))))}>
            Put it back to {from}
          </button>
        )}
      </p>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

type Said = { subject: string; outcome?: GmailOutcome };

// matchWords says how close the best application matched, in the server's
// word (link.Strength, link.NearlyAsClose).
const STRENGTH: Record<string, string> = { close: "Close match", weak: "Weak match" };
function matchWords({ strength, nearlyAsClose: near, reasons: why }: GmailLinkItem) {
  const rest = why.filter((r) => !(near && r === "ambiguous_runner_up"));
  return `${STRENGTH[strength] ?? "No close match"}${near ? ", another application is nearly as close" : ""}${rest.length > 0 ? `: ${reasons(rest)}` : ""}.`;
}

function LinkRow({ item, reload, said, rejected }: { item: GmailLinkItem; reload: () => void; said: (m: Said) => void; rejected: () => void }) {
  const [candidacy, setCandidacy] = useState(item.candidacyId === undefined ? "" : String(item.candidacyId));
  // The employer's applications are offered by name; an id is typed only for
  // one the tracker files under another name.
  const [apps] = useResource(() => get("/api/candidacies", candidacyPage, { company: item.employer, pageSize: 20 }), [item.employer], ["tracker"]);
  const [byId, setById] = useState(false);
  const found = apps.data?.entries ?? [];
  const pick = !byId && found.length > 0 && (candidacy === "" || found.some((c) => String(c.id) === candidacy));
  const act = useAction();
  const base = `/api/gmail/links/${encodeURIComponent(item.signalId)}`;
  const id = Number(candidacy);
  const confirm = async () => {
    const body: GmailLinkConfirm = { candidacyId: id };
    const out = await act.run(() => post(`${base}/confirm`, gmailLinkConfirmed, body));
    if (out) {
      said({ subject: item.subject, outcome: out.outcome });
      reload();
    }
  };
  const reject = async () => {
    if (await act.run(() => post(`${base}/reject`, gmailLinkRejected))) {
      rejected();
      reload();
    }
  };
  return (
    <li className="panel">
      <header className="row">
        <h3>
          {item.employer}
          {item.role ? `: ${item.role}` : ""}
        </h3>
        <Chip tone={item.decision === "review" ? "warn" : "neutral"}>{item.decision === "review" ? "Needs review" : "No match"}</Chip>
      </header>
      <p>{item.subject}</p>
      <p className="muted" title={`${item.type}, tier ${item.tier}, match score ${item.score.toFixed(2)}, runner-up ${item.runnerUp.toFixed(2)}`}>
        {KINDS[item.type] ?? humanize(item.type)}, received {when(item.receivedAt)}. {matchWords(item)}
      </p>
      {item.outcome && <p className="note">{outcomeText(item.outcome)}</p>}
      <div className="actions">
        {pick ? (
          <label>
            Application
            <select value={candidacy} onChange={(e) => (e.target.value === "id" ? setById(true) : setCandidacy(e.target.value))}>
              <option value="">Choose the application</option>
              {found.map((c) => (
                <option key={c.id} value={c.id}>{`${c.company}: ${c.role} (${humanize(c.status)}, ${c.date})`}</option>
              ))}
              <option value="id">Another application, by its id</option>
            </select>
          </label>
        ) : (
          <label>
            Application id
            <input type="number" min={1} value={candidacy} autoFocus={byId} onChange={(e) => setCandidacy(e.target.value)} />
          </label>
        )}
        <button type="button" disabled={act.busy || !Number.isInteger(id) || id < 1} onClick={() => void confirm()} aria-label={`Confirm the link for ${item.subject}`}>
          Confirm link
        </button>
        <button type="button" disabled={act.busy} onClick={() => void reject()} aria-label={`Not job related: ${item.subject}`}>
          Not job related
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
    </li>
  );
}

// useLinks reads both halves of the link review queue.
function useLinks() {
  const [review, reloadReview] = useResource(() => get("/api/gmail/links", gmailLinkList, { decision: "review" }), [], ["gmail"]);
  const [unlinked, reloadUnlinked] = useResource(() => get("/api/gmail/links", gmailLinkList, { decision: "unlinked" }), [], ["gmail"]);
  return { review, unlinked, reload: () => (reloadReview(), reloadUnlinked()) };
}

// The queue shows this many rows until Show more: each row reads the tracker
// for its employer's applications, and the server answers up to 200 of each
// half of the queue, 400 in all.
const LINKS_SHOWN = 10;

// LinkQueue lists the signals whose application link needs the user, all of
// them or only those of one thread. A confirmation is final and covers the whole
// thread; a rejection marks the signal as not job related, and Undo takes only
// that mark off again.
export function LinkQueue({ threadId }: { threadId?: string }) {
  const { review, unlinked, reload } = useLinks();
  const items = [...(review.data?.items ?? []), ...(unlinked.data?.items ?? [])].filter((i) => !threadId || i.threadId === threadId);
  const { shown, hidden, more } = useCapped(items, LINKS_SHOWN);
  const [said, setSaid] = useState<Said[]>([]);
  const [notJob, setNotJob] = useState<{ signalId: string; subject: string }[]>([]);
  const undo = useAction();
  const restore = async (signalId: string) => {
    if (await undo.run(() => post(`/api/gmail/links/${encodeURIComponent(signalId)}/restore`, gmailLinkRestored))) {
      setNotJob((all) => all.filter((r) => r.signalId !== signalId));
      reload();
    }
  };
  const failed = review.status === "error" ? review : unlinked.status === "error" ? unlinked : null;
  const loading = review.status === "loading" || unlinked.status === "loading";
  return (
    <section aria-labelledby={`gmail-links${threadId ? "-thread" : ""}`} className="panel">
      <header className="panel-head">
        <h2 id={`gmail-links${threadId ? "-thread" : ""}`}>Link review</h2>
      </header>
      <p className="muted">A confirmed link is final and covers the whole thread.</p>
      <div aria-live="polite">
        {said.map((m, i) => (m.outcome ? <Moved key={i} o={m.outcome} lead={`${m.subject}: `} className="note ok" /> : <p key={i} className="note ok">{m.subject}: Linked.</p>))}
        {notJob.map((r) => (
          <p key={r.signalId} className="note ok">
            Marked as not job related: {r.subject}.{" "}
            <button type="button" disabled={undo.busy} onClick={() => void restore(r.signalId)}>
              Undo
            </button>
          </p>
        ))}
      </div>
      {undo.error && <ErrorNote error={undo.error} />}
      {loading && <Loading />}
      {failed?.error && <ErrorNote error={failed.error} onRetry={reload} />}
      {!loading && !failed && (items.length === 0 ? <Empty>No link needs your decision.</Empty> : (
        <>
          <ul className="plain stack">
            {shown.map((i) => (
              <LinkRow key={i.signalId} item={i} reload={reload} said={(m) => setSaid((all) => [...all, m])} rejected={() => setNotJob((all) => [...all.filter((r) => r.signalId !== i.signalId), { signalId: i.signalId, subject: i.subject }])} />
            ))}
          </ul>
          <ShowMore hidden={hidden} onMore={more} />
        </>
      ))}
    </section>
  );
}

// Moves lists the statuses the mail moved on its own in the last two weeks,
// each one press from put back while it is still the row's latest status
// change (the server's putBack).
export function Moves() {
  const [res, reload] = useResource(() => get("/api/gmail/moves", gmailMoveList), [], ["gmail", "tracker"]);
  return (
    <Panel title="Moved by your mail" id="gmail-moves" res={res} retry={reload}>
      {(data) =>
        data.moves.length === 0 ? (
          <Empty>Your mail moved no application in the last two weeks.</Empty>
        ) : (
          <ul className="plain stack">
            {data.moves.map((m) => (
              <li key={`${m.threadId}:${m.at}:${m.outcome.toStatus}`}>
                <Moved o={m.outcome} now={m} lead={<><Link to={`/tracker?row=${m.outcome.candidacyId}`}>{[m.company, m.role].filter(Boolean).join(" \u2014 ")}</Link>, {when(m.at)}: </>} />
              </li>
            ))}
          </ul>
        )
      }
    </Panel>
  );
}

// Confirmations lists offers waiting for the user. The tracker status moves to
// offer only when the user confirms here. Undo puts the request back; the undo
// of a confirmation also puts the row back to the status it had, in the same
// server call, or refuses with nothing changed once the row moved on.
export function Confirmations() {
  const [res, reload] = useResource(() => get("/api/gmail/confirmations", gmailConfirmationList), [], ["gmail"]);
  const [, setParams] = useSearchParams();
  const act = useAction();
  const [last, setLast] = useState<{ n: number; id: number; row: string; confirmed: boolean; back?: string }>();
  const decide = async (c: GmailConfirmation, verb: "confirm" | "dismiss", row: string) => {
    if (!(await act.run(() => post(`/api/gmail/confirmations/${c.id}/${verb}`, gmailConfirmationDecided)))) return;
    setLast({ n: (last?.n ?? 0) + 1, id: c.id, row, confirmed: verb === "confirm" });
    reload();
  };
  const undo = async () => {
    const out = last && (await act.run(() => post(`/api/gmail/confirmations/${last.id}/restore`, gmailConfirmationDecided)));
    if (out) setLast({ ...last, back: out.rowStatus });
    reload();
    return !!out;
  };
  return (
    <Panel title="Confirmations" id="gmail-confirmations" res={res} retry={reload}>
      {(data) => (
        <>
          <div role="status">
            {last && (
              <UndoNote
                key={last.n}
                done={last.confirmed ? `Moved ${last.row} to offer.` : `Dismissed the offer for ${last.row}.`}
                undone={last.confirmed ? `${last.row} is back to ${last.back}, and the offer waits for you again.` : `The offer for ${last.row} waits again.`}
                onUndo={undo}
              />
            )}
          </div>
          {data.confirmations.length === 0 ? (
            <Empty>Nothing waits for your confirmation.</Empty>
          ) : (
            <ul className="plain stack">
              {data.confirmations.map((c) => {
                // The row is named by its company and role; a row without them by its number.
                const row = c.company ? [c.company, c.role].filter(Boolean).join(" \u2014 ") : `application ${c.candidacyId}`;
                return (
                  <li key={c.id} className="row">
                    <span>
                      An email suggests the status <strong>{c.status}</strong> for <Link to={`/tracker?row=${c.candidacyId}`}>{row}</Link>. <span className="muted">{when(c.createdAt)}. The status changes only if you confirm.</span>
                    </span>
                    <span className="actions">
                      <button type="button" className="ghost" onClick={() => setParams({ thread: c.threadId })} aria-label={`Read the email behind ${c.status} for ${row}`}>
                        Read the email
                      </button>
                      <button type="button" disabled={act.busy} onClick={() => void decide(c, "confirm", row)} aria-label={`Confirm ${c.status} for ${row}`}>
                        Confirm
                      </button>
                      <button type="button" disabled={act.busy} onClick={() => void decide(c, "dismiss", row)} aria-label={`Dismiss ${c.status} for ${row}`}>
                        Dismiss
                      </button>
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
          {act.error && <ErrorNote error={act.error} />}
        </>
      )}
    </Panel>
  );
}
