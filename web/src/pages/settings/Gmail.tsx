import { useEffect, useRef } from "react";
import { get, patch } from "../../api/client";
import { gmailSettings } from "../../api/decoders_gmail";
import { useResource } from "../../hooks";
import { ErrorNote, Loading } from "../../ui/parts";
import type { GmailSettingsPatch } from "../../wire";
import { useAction } from "../action";
import { Connect, loadHealth, Status } from "../gmail/Health";

// Switches are the two settings of the unattended Gmail work. Reading mail is
// never switched off here; only what the product does with it.
function Switches() {
  const [res, reload] = useResource(() => get("/api/gmail/settings", gmailSettings), [], ["gmail"], { keep: "settings.gmail" });
  const act = useAction();
  const set = async (body: GmailSettingsPatch) => {
    if (await act.run(() => patch("/api/gmail/settings", gmailSettings, body))) reload();
  };
  const s = res.data;
  return (
    <div>
      <h3>Unattended work</h3>
      {res.status === "loading" && <Loading label="Loading the Gmail settings" lines={2} />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="the Gmail settings" />}
      {s && (
        <>
          <p>
            <label>
              <input type="checkbox" checked={s.scheduledDigests} disabled={act.busy} onChange={(e) => void set({ scheduledDigests: e.target.checked })} /> Make the 08:00 and 18:00 briefs and the Sunday review
            </label>
          </p>
          <p>
            <label>
              <input type="checkbox" checked={s.alerts} disabled={act.busy} onChange={(e) => void set({ alerts: e.target.checked })} /> Send an alert when an important email arrives, and a reminder before its deadline
            </label>
          </p>
        </>
      )}
      <p className="muted">The product reads mail and never sends, deletes or labels it. Reply drafts stay in the product.</p>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// GmailSection is the Settings home of the Gmail connection: its status, the
// address and app password form and the switches. The Gmail page shows the
// same status with a Sync now control.
export function GmailSection() {
  const [health, reload] = useResource(loadHealth, [], ["gmail"], { keep: "settings.gmail-health" });
  const section = useRef<HTMLElement>(null);
  useEffect(() => {
    if (window.location.hash === "#settings-gmail") section.current?.scrollIntoView?.();
  }, []);
  const h = health.data;
  return (
    <section aria-labelledby="settings-gmail" className="panel" ref={section}>
      <header className="panel-head">
        <h2 id="settings-gmail">Gmail</h2>
      </header>
      {health.status === "loading" && <Loading />}
      {health.status === "error" && <ErrorNote error={health.error} onRetry={reload} what="the Gmail connection" />}
      {h && (
        <>
          <Status h={h} />
          {h.connected && h.login === "refused" && (
            <p className="note error">
              Gmail no longer accepts the app password{h.loginError ? ` (${h.loginError})` : ""}. Create a new app password in your Google Account and enter it below.
            </p>
          )}
          <Connect h={h} onChanged={reload} />
        </>
      )}
      <Switches />
    </section>
  );
}
