import { useId, useRef, useState, type FormEvent } from "react";
import { del, get, post, request } from "../../api/client";
import { gmailConnected, gmailHealth, gmailSyncStarted, gmailTest } from "../../api/decoders_gmail";
import type { Resource } from "../../hooks";
import { Info } from "lucide-react";
import { Link } from "../../router";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote, ExternalLink, Loading, humanize } from "../../ui/parts";
import type { GmailHealth } from "../../wire";
import { useAction, when } from "../action";
import { usePasswordField } from "../settings/passwordField";

export const loadHealth = () => get("/api/gmail/health", gmailHealth);
// SIGN_IN_MS is the wait for a request that signs in to Gmail: the server
// signs in, lists the folders and searches, each bounded at 30 seconds,
// within a minute.
const SIGN_IN_MS = 65_000;

const APP_PASSWORDS = "https://myaccount.google.com/apppasswords";
const TWO_STEP = "https://myaccount.google.com/signinoptions/twosv";

// Connect takes the Gmail address and a Google app password write-only: the
// server signs in to Gmail with them before it stores anything, keeps the
// password in the keychain and never shows it back. The password box is
// emptied as the request leaves. A connected mailbox can be tested, given a
// new app password and disconnected. The Gmail page, Settings and the Setup
// step draw it.
export function Connect({ h, onChanged }: { h: GmailHealth; onChanged: () => void }) {
  const addressId = useId();
  const passwordId = useId();
  const [address, setAddress] = useState(h.mailbox ?? "");
  const box = usePasswordField();
  const [said, setSaid] = useState("");
  const [leaving, setLeaving] = useState(false);
  const act = useAction();
  const connect = async (e: FormEvent) => {
    e.preventDefault();
    setSaid("");
    const password = box.take();
    const res = await act.run(() => request("PUT", "/api/gmail/account", gmailConnected, { body: { address, password }, timeoutMs: SIGN_IN_MS }));
    if (!res) return;
    const mailbox = res.health.mailbox ?? address;
    let text = `Gmail is connected as ${mailbox}.`;
    if (res.previous) text += ` This is a different account from ${res.previous}. Mail already read stays; syncing starts again for ${mailbox}.`;
    if (await post("/api/gmail/sync", gmailSyncStarted).then(() => true, () => false)) text += " A sync has started.";
    setSaid(text);
    onChanged();
  };
  const test = async () => {
    setSaid("");
    const res = await act.run(() => request("POST", "/api/gmail/account/test", gmailTest, { timeoutMs: SIGN_IN_MS }));
    if (res) setSaid(`Gmail signed in and holds ${res.messages} ${res.messages === 1 ? "message" : "messages"} from the last day.`);
    onChanged();
  };
  const disconnect = async () => {
    setLeaving(false);
    setSaid("");
    if (await act.run(() => del("/api/gmail/account", gmailHealth))) onChanged();
  };

  return (
    <form onSubmit={(e) => void connect(e)}>
      {(!h.connected || h.login === "refused") && (
        <ol className="help">
          <li>
            Turn on <ExternalLink href={TWO_STEP}>2-Step Verification</ExternalLink> for your Google Account, if it is not on yet.
          </li>
          <li>
            Create an <ExternalLink href={APP_PASSWORDS}>app password</ExternalLink> (Google Account, Security, App passwords); any name will do.
          </li>
          <li>Enter your Gmail address and the 16-letter app password below and press Connect. Gmail is signed in to before anything is stored.</li>
        </ol>
      )}
      {!h.connected && <p className="muted">Some Google Workspace administrators turn app passwords off; such an account cannot be connected.</p>}
      <label htmlFor={addressId}>Gmail address</label>
      <input id={addressId} type="email" autoComplete="email" spellCheck={false} value={address} onChange={(e) => setAddress(e.target.value)} />
      <label htmlFor={passwordId}>
        App password <Chip tone={h.connected ? "good" : "warn"}>{h.connected ? "saved" : "not set"}</Chip>
      </label>
      <input id={passwordId} ref={box.ref} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => box.onInput(e.target.value)} placeholder={h.connected ? "Enter a new app password to replace it" : "xxxx xxxx xxxx xxxx"} />
      <div className="actions">
        <button type="submit" className={h.connected ? undefined : "primary"} disabled={act.busy || box.text.trim() === "" || address.trim() === ""}>
          Connect
        </button>
        {h.connected && (
          <button type="button" disabled={act.busy} onClick={() => void test()}>
            Test the connection
          </button>
        )}
        {h.connected && (
          <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setLeaving(true)}>
            Disconnect
          </button>
        )}
      </div>
      {h.connected && h.mailbox && address.trim() !== "" && address.trim().toLowerCase() !== h.mailbox.toLowerCase() && (
        <p className="muted">Another account than {h.mailbox} syncs from its own start; the mail already read stays.</p>
      )}
      {act.error && <ErrorNote error={act.error} />}
      <p aria-live="polite" className={said ? "note ok" : undefined}>
        {said}
      </p>
      {leaving && (
        <ConfirmDialog
          title="Disconnect Gmail"
          consequence="The app password leaves the keychain. Mail is no longer read, codes are not looked up and the Gmail sync schedules are paused until you connect again. The address and the mail already read stay. To end the app password itself, remove it in your Google Account."
          verb="Disconnect"
          onCancel={() => setLeaving(false)}
          onConfirm={() => void disconnect()}
        />
      )}
    </form>
  );
}

const LOGIN_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { ok: "good", refused: "bad", unknown: "warn" };
const LOGIN_LABEL: Record<string, string> = { ok: "Connected", refused: "Password refused", unknown: "Keychain not readable" };
const SYNC_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { healthy: "good", partial: "warn", degraded: "bad" };

function age(seconds: number): string {
  if (seconds < 3600) return `${Math.max(1, Math.round(seconds / 60))} min`;
  if (seconds < 86_400) return `${Math.round(seconds / 3600)} h`;
  return `${Math.round(seconds / 86_400)} days`;
}

// Status says whether the connection is usable, when mail was last read and what
// the last run did. A running sync is shown as running, not as the failure the
// previous run left behind.
export function Status({ h }: { h: GmailHealth }) {
  const run = h.latestRun;
  return (
    <dl className="facts">
      <dt>Connection</dt>
      <dd>
        {h.connected ? <Chip tone={LOGIN_TONE[h.login] ?? "neutral"}>{LOGIN_LABEL[h.login] ?? humanize(h.login)}</Chip> : <Chip>Not connected</Chip>}
        {h.connected && h.mailbox && <span className="muted"> as {h.mailbox}</span>}
      </dd>
      {h.connected && (
        <>
          <dt>Access</dt>
          <dd>
            Read-only <span className="muted">(IMAP with an app password)</span>
          </dd>
        </>
      )}
      <dt>Sync</dt>
      <dd>{h.running ? <Chip tone="warn">Running</Chip> : <Chip tone={SYNC_TONE[h.sync] ?? "neutral"}>{humanize(h.sync)}</Chip>}</dd>
      {h.cursorAgeSeconds !== undefined && (
        <>
          <dt>Mail read up to</dt>
          <dd>{age(h.cursorAgeSeconds)} ago</dd>
        </>
      )}
      {run && (
        <>
          <dt>Last sync</dt>
          <dd>
            {run.status === "running" ? (
              `started ${when(run.startedAt)}`
            ) : (
              <>
                {when(run.startedAt)}, {run.counts.classified} messages processed, {run.counts.review > 0 ? <Link to="/gmail?tab=review">{run.counts.review} for review</Link> : "0 for review"}
              </>
            )}
          </dd>
        </>
      )}
      {!h.running && (h.failedStage || run?.error) && (
        <>
          <dt>Last error</dt>
          <dd>
            {h.failedStage ? `${h.failedStage}: ` : ""}
            {run?.error ?? "the stage failed"}
          </dd>
        </>
      )}
    </dl>
  );
}

// HealthBanner shows the connection state above every tab. A refused app
// password says so with Gmail's own answer; a mailbox that is not connected,
// or whose password Gmail refuses, is connected again right here.
export function HealthBanner({ res, reload }: { res: Resource<GmailHealth>; reload: () => void }) {
  const h = res.data;
  const act = useAction();
  const [started, setStarted] = useState(false);
  const sync = async () => {
    setStarted(false);
    if (await act.run(() => post("/api/gmail/sync", gmailSyncStarted))) {
      setStarted(true);
      reload();
    }
  };
  const refused = h?.connected === true && h.login === "refused";
  // Once offered, Connect stays for the visit, so its outcome is still read
  // after the status it fixed has turned healthy.
  const offered = useRef(false);
  if (h && (refused || !h.connected)) offered.current = true;
  return (
    <section aria-labelledby="gmail-health" className="panel">
      <header className="panel-head">
        <h2 id="gmail-health">Connection</h2>
        <button
          type="button"
          disabled={!h || act.busy || h.running || !h.connected || refused}
          title={h?.connected === false ? "Connect Gmail first." : refused ? "Enter a new app password first." : undefined}
          onClick={() => void sync()}
        >
          Sync now
        </button>
      </header>
      <div aria-live="polite">{started && <p className="note ok">A sync has started. The status below updates when it ends.</p>}</div>
      {act.error && <ErrorNote error={act.error} />}
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />}
      {h && (
        <>
          {refused && (
            <div role="alert" className="banner">
              <p>
                Gmail no longer accepts the app password{h.loginError ? ` (${h.loginError})` : ""}. It happens when the app password is removed in your Google Account or the
                account&rsquo;s password changes. Create a new <ExternalLink href={APP_PASSWORDS}>app password</ExternalLink> and enter it below. The inbox below is the last
                known data and is stale.
              </p>
            </div>
          )}
          {!h.connected && (
            <div className="note info with-icon">
              <Info />
              <p>Gmail is not connected. Connect it to see job email, actions to take and briefs here.</p>
            </div>
          )}
          {offered.current && <Connect h={h} onChanged={reload} />}
          <Status h={h} />
        </>
      )}
    </section>
  );
}
