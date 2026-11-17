import { useEffect, useState } from "react";
import { get, newRequestId, post, request } from "../../api/client";
import { gmailDigestList, gmailDigestRated, gmailDigestVerdicts, gmailSettings } from "../../api/decoders_gmail";
import { runAccepted } from "../../api/decoders_runs";
import { useResource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { CopyButton } from "../../ui/CopyButton";
import { Chip, Empty, ErrorNote, ExternalLink, Loading, Panel } from "../../ui/parts";
import type { GmailDigest, GmailDigestFeedback, GmailDigestItem } from "../../wire";
import { useAction, when } from "../action";
import { useRunLines } from "../discovery/runLines";
import { gmailUrl } from "./Thread";

const HISTORY = 20;
const SECTIONS: Record<string, string> = { waiting_on_you: "Waiting on you", follow_up: "Follow up" };
const CHANNELS: Record<string, string> = { telegram: "Telegram", desktop: "Desktop", agent: "Ask Agent" };

// delivered says where a brief went, one sentence per outcome with the
// channels that share it named together; a state is "sent", "skipped: <why>"
// or "failed: <why>" (digest_run.go).
export function delivered(delivery: Record<string, string>): string {
  const byState = new Map<string, string[]>();
  for (const [channel, state] of Object.entries(delivery)) byState.set(state, [...(byState.get(state) ?? []), CHANNELS[channel] ?? channel]);
  if (byState.size === 0) return "Sent nowhere.";
  const names = (channels: string[]) => new Intl.ListFormat("en").format(channels);
  return [...byState]
    .map(([state, channels]) => {
      if (state === "sent") return `Sent to ${names(channels)}.`;
      const [head, why] = state.split(/: (.*)/s);
      if (head === "skipped") return `Not sent to ${names(channels)}${why ? `: ${why}` : ""}.`;
      if (head === "failed") return `Sending to ${names(channels)} failed${why ? `: ${why}` : ""}.`;
      return `${names(channels)}: ${state}.`;
    })
    .join(" ");
}

// kindOf tells the morning, evening and weekly briefs apart: the digest has a
// kind of daily or weekly, and a daily one is named for the half of the day it
// was made in.
function kindOf(d: GmailDigest): string {
  if (d.kind === "weekly") return "Weekly review";
  return new Date(d.createdAt).getHours() < 12 ? "Morning brief" : "Evening brief";
}

function Rating({ digestId, item, verdict, onRated }: { digestId: number; item: GmailDigestItem; verdict: string | undefined; onRated: () => void }) {
  const act = useAction();
  const rate = async (next: string) => {
    const body: GmailDigestFeedback = { threadKey: item.key, verdict: next };
    if (await act.run(() => post(`/api/gmail/digests/${digestId}/feedback`, gmailDigestRated, body))) onRated();
  };
  const name = `${item.company}${item.role ? `, ${item.role}` : ""}`;
  return (
    <span className="actions">
      <button type="button" aria-pressed={verdict === "up"} disabled={act.busy} onClick={() => void rate(verdict === "up" ? "clear" : "up")} aria-label={`Thumbs up for ${name}`}>
        Useful
      </button>
      <button type="button" aria-pressed={verdict === "down"} disabled={act.busy} onClick={() => void rate(verdict === "down" ? "clear" : "down")} aria-label={`Thumbs down for ${name}`}>
        Not useful
      </button>
      {act.error && <ErrorNote error={act.error} />}
    </span>
  );
}

function Item({ digestId, item, verdict, onRated, rate }: { digestId: number; item: GmailDigestItem; verdict: string | undefined; onRated: () => void; rate: boolean }) {
  const [, setParams] = useSearchParams();
  return (
    <li>
      <p>{item.line}</p>
      <p className="muted">
        {item.lane.replaceAll("_", " ")}. Owner: {item.owner}.{item.due ? ` Due ${when(item.due)}.` : ""}
      </p>
      {item.draft && (
        <div className="note" role="group" aria-label={`Reply draft for ${item.company}`}>
          <pre className="pre">{item.draft}</pre>
          <p className="muted">A draft for you to review and send yourself; the product never sends mail.</p>
          <CopyButton text={item.draft} label={`Copy the reply draft for ${item.company}`} />
        </div>
      )}
      <div className="actions">
        <button type="button" className="small" onClick={() => setParams({ thread: item.key })} aria-label={`Open the thread of ${item.company}`}>
          Open thread
        </button>
        <ExternalLink href={gmailUrl(item.key)}>Open in Gmail</ExternalLink>
      </div>
      {rate && <Rating digestId={digestId} item={item} verdict={verdict} onRated={onRated} />}
    </li>
  );
}

function Brief({ d, verdicts, onRated }: { d: GmailDigest; verdicts: Record<string, string>; onRated: () => void }) {
  const bySection = new Map<string, GmailDigestItem[]>();
  for (const i of d.items) bySection.set(i.section, [...(bySection.get(i.section) ?? []), i]);
  return (
    <article aria-label={kindOf(d)}>
      <h3>{d.headline || kindOf(d)}</h3>
      <p className="muted">
        {kindOf(d)}, {when(d.createdAt)}. {d.method === "model" ? "Written by a model." : "Built from the stored signals."} {delivered(d.delivery)}
      </p>
      {d.status === "failed" && d.error && <ErrorNote error={new Error(d.error)} />}
      {d.text && <p className="pre">{d.text}</p>}
      {d.narrative && <p>{d.narrative}</p>}
      {d.items.length === 0 && d.muted.length === 0 && <p className="muted">Nothing in this window needed you.</p>}
      {d.items.length > 0 && <p className="muted">Not useful hides a thread from later briefs until its lane or state changes.</p>}
      {[...bySection.entries()].map(([section, items]) => (
        <section key={section} aria-label={SECTIONS[section] ?? section.replaceAll("_", " ")}>
          <h4>{SECTIONS[section] ?? section.replaceAll("_", " ")}</h4>
          <ul className="plain stack">
            {items.map((i) => (
              <Item key={i.key} digestId={d.id} item={i} verdict={verdicts[i.key]} onRated={onRated} rate />
            ))}
          </ul>
        </section>
      ))}
      {d.muted.length > 0 && (
        <details>
          <summary>{d.muted.length} muted by your rating</summary>
          <ul className="plain stack">
            {d.muted.map((i) => (
              <Item key={i.key} digestId={d.id} item={i} verdict={verdicts[i.key]} onRated={onRated} rate />
            ))}
          </ul>
        </details>
      )}
      {d.omitted > 0 && (
        <p className="muted">
          {d.omitted} more threads were left out of this brief; <Link to="/gmail">the Inbox</Link> lists them all.
        </p>
      )}
      {d.receipts > 0 && <p className="muted">{d.receipts} receipts were counted and not listed.</p>}
    </article>
  );
}

// Briefs shows one brief by section and the history of briefs, newest first.
// The brief in the address (a notification link) opens first; otherwise the
// latest does.
export function Briefs() {
  const [params, setParams] = useSearchParams();
  const [res, reload] = useResource(() => get("/api/gmail/digests", gmailDigestList, { limit: HISTORY }), [], ["gmail"]);
  const [verdicts, reloadVerdicts] = useResource(() => get("/api/gmail/digests/feedback", gmailDigestVerdicts), [], ["gmail"]);
  // The times are fixed (08:00, 18:00, Sunday); only the switch changes them.
  const [settings] = useResource(() => get("/api/gmail/settings", gmailSettings), [], ["gmail"]);
  const act = useAction();

  const wanted = Number(params.get("brief")) || 0;
  const all = res.data?.digests ?? [];
  const shown = all.find((d) => d.id === wanted) ?? all[0];
  // Making a brief is a model call of up to a minute, so it is a background
  // run: the brief it made is the one the run ends with.
  const [runId, setRunId] = useState<string>();
  const { final } = useRunLines(runId, () => reload());
  const making = act.busy || (runId !== undefined && !final);
  useEffect(() => {
    if (final?.status === "succeeded" && final.outcome) setParams({ brief: final.outcome }, { replace: true });
  }, [final]);
  const run = async () => {
    setRunId(undefined);
    const out = await act.run(() => request("POST", "/api/gmail/digests/run", runAccepted, { headers: { "Idempotency-Key": newRequestId() } }));
    if (out) setRunId(out.runId);
  };

  return (
    <Panel
      title="Briefs"
      id="gmail-briefs"
      res={res}
      retry={reload}
      actions={
        <button type="button" disabled={making} onClick={() => void run()}>
          {making ? "Making a brief" : "Make a brief now"}
        </button>
      }
    >
      {() => (
        <>
          {act.error && <ErrorNote error={act.error} />}
          {final?.status === "failed" && <p className="note">{final.reason}</p>}
          {wanted > 0 && shown && shown.id !== wanted && <p role="note" className="note">That brief is not among the latest {HISTORY}; the newest is shown.</p>}
          {!shown ? <Empty>
              No brief yet.{" "}
              {settings.data?.scheduledDigests === false ? (
                <>Scheduled briefs are off; turn them on in <Link to="/settings#settings-gmail">Settings, Gmail</Link>, or make one now.</>
              ) : (
                "Briefs are made at 08:00 and 18:00 local time, and a weekly review on Sunday."
              )}
            </Empty> : verdicts.status === "loading" ? <Loading label="Loading the brief" /> : <Brief d={shown} verdicts={verdicts.data?.verdicts ?? {}} onRated={reloadVerdicts} />}
          {all.length > 0 && (
            <nav aria-label="Brief history">
              <h3>History</h3>
              <ul className="plain history">
                {all.map((d) => (
                  <li key={d.id}>
                    <button type="button" aria-current={d.id === shown?.id ? "true" : undefined} onClick={() => setParams({ brief: String(d.id) }, { replace: true })}>
                      {kindOf(d)}, {when(d.createdAt)}
                    </button>{" "}
                    {d.status === "failed" && <Chip tone="bad">failed</Chip>}
                  </li>
                ))}
              </ul>
            </nav>
          )}
        </>
      )}
    </Panel>
  );
}
