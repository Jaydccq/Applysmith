import { useEffect, useId, useState, type FormEvent } from "react";
import { get, patch, post } from "../../api/client";
import { notificationSettings, notificationTest } from "../../api/decoders_agent";
import { configDocument } from "../../api/decoders_pages";
import { toYaml } from "../../api/yaml";
import { useResource } from "../../hooks";
import { Modal } from "../../ui/Modal";
import { Chip, ErrorNote, Panel } from "../../ui/parts";
import { getShellInfo, hasShell } from "../../desktop";
import { Link } from "../../router";
import type { ConfigDocument, NotificationSettings, NotificationSettingsPatch, NotificationTest } from "../../wire";
import { useAction } from "../action";
import { saveProfile, text, useProfileSaves, useRevisedDraft, valueAt } from "../profile/shared";
import { ZONES } from "../discovery/schedule";
import { SaveBar } from "../../ui/SaveBar";

const SWITCHES: { key: keyof NotificationSettingsPatch; label: string; hint: string }[] = [
  { key: "desktop", label: "Desktop notifications", hint: "On by default. Announcements of runs that need you or have finished. While the app is closed they wait under Notices in the top bar." },
  {
    key: "telegram",
    label: "Telegram notifications",
    hint: "On by default. Sent only while a Telegram chat is paired. The phone gets what you can act on away from the desk: employer mail and interviews, the assistant's questions, and the daily briefs. Runs you start in the dashboard are told on the desktop only.",
  },
  { key: "failedScans", label: "Announce failed scheduled scans", hint: "Off by default; a failed scan still shows on the Runs page." },
];

function Switches({ settings, reload }: { settings: NotificationSettings; reload: () => void }) {
  const act = useAction();
  // The app reports a refusal only after macOS refused a notification.
  const [denied, setDenied] = useState(false);
  useEffect(() => {
    if (hasShell()) void getShellInfo().then((i) => setDenied(!i.notificationsPermitted), () => undefined);
  }, []);
  const flip = async (key: keyof NotificationSettingsPatch) => {
    const body: NotificationSettingsPatch = { [key]: !settings[key] };
    if (await act.run(() => patch("/api/notifications/settings", notificationSettings, body))) reload();
  };
  return (
    <>
      <ul className="plain stack">
        {SWITCHES.map((s) => (
          <li key={s.key}>
            <label className="check">
              <input type="checkbox" checked={settings[s.key]} disabled={act.busy} onChange={() => void flip(s.key)} />
              {s.label}
            </label>
            <span className="help">{s.hint}</span>
            {s.key === "telegram" && !settings.telegramPaired && (
              <span>
                <Chip tone="warn">No chat is paired</Chip> <Link to="/settings#settings-telegram">Pair a chat</Link>
              </span>
            )}
          </li>
        ))}
      </ul>
      {act.error && <ErrorNote error={act.error} />}
      {denied && <p className="note error">macOS is not showing Applysmith's notifications. Allow them in System Settings, Notifications, Applysmith.</p>}
      <TestSend />
    </>
  );
}

const TEST_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { sent: "good", off: "neutral", unavailable: "warn", failed: "bad" };

// TestSend sends one test notification to every channel at once, past quiet
// hours, and says per channel what happened and what to do.
function TestSend() {
  const [result, setResult] = useState<NotificationTest | null>(null);
  const act = useAction();
  const send = async () => {
    setResult(null);
    const got = await act.run(() => post("/api/notifications/test", notificationTest));
    if (got) setResult(got);
  };
  return (
    <div>
      <button type="button" disabled={act.busy} onClick={() => void send()}>
        Send a test notification
      </button>
      <ul className="plain" aria-live="polite">
        {result?.channels.map((c) => (
          <li key={c.channel}>
            {c.channel === "desktop" ? "Desktop" : "Telegram"} <Chip tone={TEST_TONE[c.state] ?? "neutral"}>{c.state}</Chip> <span className="muted">{c.detail}</span>
          </li>
        ))}
      </ul>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

interface Quiet {
  enabled: boolean;
  start: string;
  end: string;
  timezone: string;
}

const quietOf = (doc: ConfigDocument): Quiet => ({
  enabled: valueAt(doc.values, "notifications.quiet_hours.enabled") === true,
  start: text(valueAt(doc.values, "notifications.quiet_hours.start")),
  end: text(valueAt(doc.values, "notifications.quiet_hours.end")),
  timezone: text(valueAt(doc.values, "notifications.quiet_hours.timezone")),
});

const describe = (q: Quiet) => (q.enabled ? `on, ${q.start || "?"} to ${q.end || "?"}, ${q.timezone || "this machine's time zone"}` : "off");
const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;

// Quiet hours are a key group of profile.yml, so a save quotes the revision the
// edit was made at (useRevisedDraft). A conflict opens a dialog with both
// versions.
function QuietForm({ doc, reload }: { doc: ConfigDocument; reload: () => void }) {
  const form = useRevisedDraft(doc.revision, quietOf(doc));
  const { draft, setDraft, dirty } = form;
  const [conflict, setConflict] = useState<ConfigDocument | null>(null);
  const [savedAt, setSavedAt] = useState("");
  const act = useAction();
  const ids = [useId(), useId(), useId(), useId()];
  // The server holds nothing when start equals end, and a start after the end
  // crosses midnight.
  const equal = draft.start === draft.end;
  const timesOk = !draft.enabled || (HHMM.test(draft.start) && HHMM.test(draft.end) && !equal);

  const write = async (revision: string) => {
    const value = { enabled: draft.enabled, start: draft.start, end: draft.end, timezone: draft.timezone };
    await saveProfile(revision, [{ op: "set", path: "notifications.quiet_hours", value: toYaml(value) }]);
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setSavedAt("");
    const ok = await act.run(async () => {
      await write(form.revision);
      return true;
    });
    if (ok) saved();
  };
  const saved = () => {
    form.sent(draft);
    setSavedAt(new Date().toLocaleTimeString());
    reload();
  };

  const openConflict = async () => {
    const current = await act.run(() => get("/api/config/profile", configDocument));
    if (current) setConflict(current);
  };

  const failure = act.error;
  const conflicted = failure?.kind === "revision_conflict";
  // The zone is the one field the form cannot check itself, so a refusal of it
  // shows the server's words for the field (details.fields) beside it.
  const zoneIssue = failure?.kind === "validation_failed" ? failure.details?.fields?.["notifications.quiet_hours.timezone"] : undefined;

  return (
    <form onSubmit={save} className="form-grid">
      <div>
        <label className="check">
          <input type="checkbox" checked={draft.enabled} onChange={(e) => setDraft(e.target.checked && !draft.start && !draft.end ? { ...draft, enabled: true, start: "22:00", end: "08:00" } : { ...draft, enabled: e.target.checked })} />
          Hold normal notifications overnight
        </label>
        <p className="help">Off by default. A saved change applies to the next notification, without a restart.</p>
      </div>
      <div>
        <label htmlFor={ids[0]}>Start</label>
        <input id={ids[0]} type="time" value={draft.start} onChange={(e) => setDraft({ ...draft, start: e.target.value })} />
      </div>
      <div>
        <label htmlFor={ids[1]}>End</label>
        <input id={ids[1]} type="time" value={draft.end} onChange={(e) => setDraft({ ...draft, end: e.target.value })} />
      </div>
      <div>
        <label htmlFor={ids[2]}>Time zone</label>
        <input id={ids[2]} list={ids[3]} value={draft.timezone} placeholder="Blank means this machine's zone" aria-invalid={zoneIssue ? true : undefined} aria-describedby={zoneIssue ? `${ids[2]}-issue` : undefined} onChange={(e) => (setDraft({ ...draft, timezone: e.target.value }), zoneIssue && act.clear())} />
        {zoneIssue && <p id={`${ids[2]}-issue`} className="note error compact">{zoneIssue}</p>}
        <datalist id={ids[3]}>
          {ZONES.map((z) => (
            <option key={z} value={z} />
          ))}
        </datalist>
        <span className="muted">An IANA name such as Europe/London.</span>
      </div>
      {!timesOk && <p className="note error">{equal && draft.start ? "Start and end are the same time, so nothing would be held. Pick an end such as 08:00." : "Start and end must be times such as 22:00 and 08:00."}</p>}
      {draft.enabled && timesOk && (
        <p className="muted">
          Held from {draft.start} to {draft.end} {draft.start > draft.end ? "the next morning" : "the same day"}, every day.
        </p>
      )}
      <SaveBar dirty={dirty} savedAt={savedAt}>
        <button type="submit" disabled={act.busy || !dirty || !timesOk}>
          Save quiet hours
        </button>
      </SaveBar>
      {failure && !conflicted && !zoneIssue && <ErrorNote error={failure} />}
      {conflicted && !conflict && (
        <div role="alert" className="note error">
          <p>The profile changed since this page loaded, so nothing was saved.</p>
          <button type="button" onClick={() => void openConflict()}>
            Compare with the current version
          </button>
        </div>
      )}
      {conflict && (
        <Modal title="The profile changed" onClose={() => setConflict(null)}>
          <p>Quiet hours in the saved profile: {describe(quietOf(conflict))}.</p>
          <p>Your edit: {describe(draft)}.</p>
          <div className="actions">
            <button type="button" onClick={() => setConflict(null)}>
              Keep editing
            </button>
            <button
              type="button"
              onClick={() => {
                setConflict(null);
                form.reset();
                act.clear();
                reload();
              }}
            >
              Reload the current version
            </button>
            <button
              type="button"
              disabled={act.busy}
              onClick={() => {
                const rev = conflict.revision;
                setConflict(null);
                void act
                  .run(async () => {
                    await write(rev);
                    return true;
                  })
                  .then((ok) => ok && saved());
              }}
            >
              Apply my edit again
            </button>
          </div>
        </Modal>
      )}
    </form>
  );
}

function QuietHours() {
  const [res, reload] = useResource(() => get("/api/config/profile", configDocument), [], ["configuration"], { keep: "settings.profile" });
  useProfileSaves(reload);
  return (
    <Panel title="Quiet hours" id="settings-quiet" res={res} retry={reload} what="the quiet hours">
      {(doc) =>
        doc.issues.length > 0 ? (
          <div role="alert" className="note error">
            <p>The profile file has errors, so quiet hours cannot be edited here yet:</p>
            <ul>
              {doc.issues.map((i) => (
                <li key={`${i.path}${i.message}`}>{i.path ? `${i.path}: ` : ""}{i.message}</li>
              ))}
            </ul>
          </div>
        ) : (
          <>
            <QuietForm doc={doc} reload={reload} />
            <h3>What is held and what is not</h3>
            <p>Urgent ones pass at once: a run that needs you or is stopped, and an email about an interview within 24 hours.</p>
            <p>Normal ones are held: finished runs, Gmail briefs and other alerts, the spend cap, hiring signals. They are listed under Notices in the top bar at once, without a sound or a banner. When the window ends they arrive as one summary instead of one by one.</p>
            <p>Never held: results of scheduled Ask Agent prompts, replies in a Telegram conversation to a message you just sent, verification codes you asked for, and test notifications. The Runs page always shows the current state.</p>
          </>
        )
      }
    </Panel>
  );
}

export function NotificationsSection() {
  const [res, reload] = useResource(() => get("/api/notifications/settings", notificationSettings), [], ["notifications"], { keep: "settings.notifications" });
  return (
    <>
      <Panel title="Notifications" id="settings-notifications" res={res} retry={reload} what="the notification settings">
        {(s) => <Switches settings={s} reload={reload} />}
      </Panel>
      <QuietHours />
    </>
  );
}
