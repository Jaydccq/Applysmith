import { useEffect, useId, useState, type FormEvent } from "react";
import { del, get, post, put } from "../../api/client";
import { telegramPairingCode, telegramStatus } from "../../api/decoders_agent";
import { useExpiring, useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote, ExternalLink, Panel, QrCode } from "../../ui/parts";
import type { TelegramPairingCode, TelegramStatus } from "../../wire";
import { Link } from "../../router";
import { useAction } from "../action";
import { usePasswordField } from "./passwordField";

const HEALTH_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { healthy: "good", configured: "neutral", stale: "warn", conflicted: "bad", stopped: "warn", refused: "bad" };

// What each health value means and what to do about it.
const HEALTH_HINT: Record<string, string> = {
  configured: "Waiting for the first answer from Telegram.",
  stale: "Telegram has not answered for a while. Check this computer's connection.",
  conflicted: "Another program is reading this bot's messages. Stop it, or create a new bot with BotFather and save its token.",
  refused: "Telegram refused the saved token: it was revoked or replaced. Get the current token from BotFather and save it below.",
};


// TokenForm takes the bot token write-only (usePasswordField).
function TokenForm({ status, onChanged }: { status: TelegramStatus; onChanged: () => void }) {
  const field = useId();
  const input = usePasswordField();
  const [clearing, setClearing] = useState(false);
  const [saved, setSaved] = useState("");
  const act = useAction();

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const token = input.take();
    if (!token) return;
    setSaved("");
    if (await act.run(() => put("/api/telegram/token", telegramStatus, { token }))) {
      setSaved(`Saved at ${new Date().toLocaleTimeString()}.`);
      onChanged();
    }
  };
  const clear = async () => {
    setClearing(false);
    setSaved("");
    if (await act.run(() => del("/api/telegram/token", telegramStatus))) onChanged();
  };

  return (
    <form onSubmit={save} className="key-row">
      {!status.tokenSet && (
        <ol className="help">
          <li>
            In Telegram, open <ExternalLink href="https://t.me/BotFather">@BotFather</ExternalLink> and send <code>/newbot</code>.
          </li>
          <li>Give the bot a name, then a username that ends in &ldquo;bot&rdquo;.</li>
          <li>Paste the token BotFather replies with below and save it.</li>
        </ol>
      )}
      <label htmlFor={field}>
        Bot token <Chip tone={status.tokenSet ? "good" : "warn"}>{status.tokenSet ? "saved" : "not set"}</Chip>
      </label>
      <input id={field} ref={input.ref} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => input.onInput(e.target.value)} placeholder={status.tokenSet ? "Enter a new token to replace it" : "Paste the token from BotFather"} />
      <div className="actions">
        <button type="submit" disabled={input.text === "" || act.busy}>
          Save token
        </button>
        {status.tokenSet && (
          <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setClearing(true)}>
            Remove token
          </button>
        )}
        <span aria-live="polite" className="muted">
          {saved}
        </span>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {clearing && (
        <ConfirmDialog title="Remove the Telegram bot token" consequence="The bot stops answering and no Telegram notification is sent until a new token is saved. The paired chat is kept." verb="Remove token" onCancel={() => setClearing(false)} onConfirm={() => void clear()} />
      )}
    </form>
  );
}

// Pairing shows the one-time code only while it is valid; it is held in memory
// and dropped the moment it expires.
function Pairing({ status, onChanged }: { status: TelegramStatus; onChanged: () => void }) {
  const { item: code, seconds, expired, hold, asking } = useExpiring<TelegramPairingCode>();
  const [unpairing, setUnpairing] = useState(false);
  const act = useAction();

  // Once the chat is paired the code has done its work.
  useEffect(() => {
    if (status.paired) hold(null);
  }, [status.paired]);

  const request = async () => {
    asking();
    const next = await act.run(() => post("/api/telegram/pairing-code", telegramPairingCode));
    if (next) hold(next);
  };
  const unpair = async () => {
    setUnpairing(false);
    if (await act.run(() => del("/api/telegram/pairing", telegramStatus))) onChanged();
  };

  return (
    <div>
      <p>
        Chat: <Chip tone={status.paired ? "good" : "neutral"}>{status.paired ? "paired" : "not paired"}</Chip>
      </p>
      <div className="actions">
        {!status.paired && (
          <button type="button" disabled={act.busy || !status.tokenSet} onClick={() => void request()}>
            Get a pairing code
          </button>
        )}
        {status.paired && (
          <button type="button" disabled={act.busy} onClick={() => setUnpairing(true)}>
            Unpair this chat
          </button>
        )}
        {status.paired && <Link to="/settings#settings-notifications">Send a test notification</Link>}
      </div>
      {code && (
        <div>
          {code.link ? (
            <>
              <p>
                <ExternalLink href={code.link}>Open @{code.botUsername} in Telegram</ExternalLink> and press Start: that pairs your chat. On your phone, scan the code with the camera.
              </p>
              {code.qr && <QrCode rows={code.qr.rows} size={code.qr.size} label="QR code of the link that pairs Telegram" />}
              <p className="muted">
                Or send this code to @{code.botUsername} yourself. It works once: <strong className="pairing-code">{code.code}</strong>
              </p>
            </>
          ) : (
            <>
              <p>
                Send this code to {code.botUsername ? `@${code.botUsername}` : "the bot"} in Telegram. It works once.
              </p>
              <p>
                <strong className="pairing-code">{code.code}</strong>
              </p>
            </>
          )}
          <p role="timer">Expires in {seconds} s</p>
        </div>
      )}
      {expired && <p role="status" className="muted">The code expired. Get a new one.</p>}
      {act.error && <ErrorNote error={act.error} />}
      {unpairing && (
        <ConfirmDialog title="Unpair the Telegram chat" consequence="The bot stops answering this chat and held actions can no longer be approved from it. Pair again with a new code." verb="Unpair" onCancel={() => setUnpairing(false)} onConfirm={() => void unpair()} />
      )}
    </div>
  );
}

export function TelegramSection() {
  const [res, reload] = useResource(() => get("/api/telegram/status", telegramStatus), [], ["notifications"], { keep: "settings.telegram" });
  return (
    <Panel title="Telegram" id="settings-telegram" res={res} retry={reload} what="the Telegram state">
      {(s) => (
        <>
          <p>
            Health: <Chip tone={HEALTH_TONE[s.health] ?? "neutral"}>{s.health}</Chip>
            {s.botUsername && <span className="muted"> bot @{s.botUsername}</span>}
            {s.unauthorizedAttempts > 0 && <span className="muted"> · {s.unauthorizedAttempts} messages from other chats were ignored</span>}
          </p>
          {s.health === "stopped" && <p className="help">{s.tokenSet ? "The bot is not polling: another Applysmith process holds it, or this is the demo workspace. It retries on its own." : "Save a bot token to start the bot."}</p>}
          {HEALTH_HINT[s.health] && <p className={s.health === "stale" || s.health === "configured" ? "help" : "note error"}>{HEALTH_HINT[s.health]}</p>}
          <TokenForm status={s} onChanged={reload} />
          <h3>Pairing</h3>
          <Pairing status={s} onChanged={reload} />
        </>
      )}
    </Panel>
  );
}
