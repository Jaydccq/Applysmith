import { useEffect, useState } from "react";
import { get, post } from "../../api/client";
import { sessionPending, sessionUndoResult, sessionsView } from "../../api/decoders_browser";
import { useInterval } from "../../hooks";
import type { Resource } from "../../hooks";
import { Link, useSearchParams } from "../../router";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import type { SessionsView } from "../../wire";
import { useAction, when } from "../action";

export const loadSessions = () => get("/api/browser/sessions", sessionsView);

const POLL_MS = 1500;

// SessionSync copies the sign-in of a site from the user's everyday Chrome into
// the apply window, through the extension. The page shows host names and cookie
// counts and never a cookie name or value; the server holds none to send.
export function SessionSync({ res, reload }: { res: Resource<SessionsView>; reload: () => void }) {
  const ask = useAction();
  const undo = useAction();
  const [asking, setAsking] = useState(false);
  const [undone, setUndone] = useState<string | null>(null);
  // A needs-you item links here with the site whose sign-in a scan lacked.
  const [params] = useSearchParams();
  const asked = params.get("sync") ?? "";
  useInterval(reload, POLL_MS, !!res.data?.pending);
  useEffect(() => {
    if (asked) document.getElementById("browser-sessions")?.scrollIntoView?.({ block: "start" });
  }, [asked]);

  const sync = async (domains: string[]) => {
    setUndone(null);
    if (await ask.run(() => post("/api/browser/sessions/sync-requests", sessionPending, { sites: domains }))) reload();
  };
  const doUndo = async () => {
    setAsking(false);
    const done = await undo.run(() => post("/api/browser/sessions/undo", sessionUndoResult));
    if (done) {
      setUndone(`Restored ${done.restored} cookies${done.failed ? `, ${done.failed} failed` : ""}.`);
      reload();
    }
  };

  return (
    <Panel title="Session sync" id="browser-sessions" res={res} retry={reload}>
      {(v) => {
        const sites = v.sites.filter((s) => s.sourceCookies != null && s.sourceCookies > 0);
        const waiting = !!v.pending;
        const wanted = asked ? sites.find((s) => s.domain === asked || asked.endsWith(`.${s.domain}`)) : undefined;
        return (
          <>
            {asked && (
              <div className="note" role="note">
                {wanted ? (
                  <p>
                    A scan stopped at the sign-in page of {asked}. Sync its sign-in from your everyday Chrome, then run the scan again from Needs you.{" "}
                    <button type="button" disabled={ask.busy || waiting} onClick={() => void sync([wanted.domain])}>
                      Sync {wanted.label} now
                    </button>
                  </p>
                ) : (
                  <p>
                    A scan stopped at the sign-in page of {asked}, and your everyday Chrome holds no sign-in for it that the extension has counted. Sign in
                    to {asked} in your Chrome and sync it here, or sign in in the app browser.
                  </p>
                )}
              </div>
            )}
            <p className="muted">
              Copies a site's sign-in from your everyday Chrome into the apply window, through the Applysmith extension. Only host names and cookie counts
              are shown here. A sync keeps the cookies it replaces so Undo can put them back; google.com and youtube.com are never synced.
            </p>
            <p className="muted">
              {v.lastCheckin ? (
                `The extension last reported ${when(v.lastCheckin)}.`
              ) : (
                <>
                  The extension has not reported yet. <Link to="/settings#settings-extension">Pair it in Settings</Link> and keep Chrome open.
                </>
              )}
            </p>
            {sites.length === 0 ? (
              <Empty>No site has a sign-in in your everyday Chrome that the extension has counted.</Empty>
            ) : (
              <ul className="plain stack">
                {sites.map((s) => (
                  <li key={s.domain} className="row">
                    <span>
                      <strong>{s.label}</strong> <span className="muted">{s.domain}</span>
                      <br />
                      <span className="muted">
                        {s.sourceCookies} cookies in your Chrome, {s.cookies} in the apply window
                      </span>
                    </span>
                    <button type="button" disabled={ask.busy || waiting} aria-label={`Sync ${s.label}`} onClick={() => void sync([s.domain])}>
                      Sync
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {sites.length > 1 && (
              <div className="actions">
                <button type="button" disabled={ask.busy || waiting} onClick={() => void sync(sites.map((s) => s.domain))}>
                  Sync all {sites.length} sites
                </button>
              </div>
            )}
            {ask.error && <ErrorNote error={ask.error} />}
            {/* One region that stays in the page, so what appears in it is announced; a region added with its text is not. */}
            <div role="status">
              {waiting && <p>Waiting for Chrome to answer for {v.pending?.domains.join(", ")}.</p>}
              {v.lastOutcome && !waiting && (
                <p className={`note ${v.lastOutcome.state === "failed" ? "error" : "ok"}`}>
                  <Chip tone={v.lastOutcome.state === "failed" ? "bad" : "good"}>{v.lastOutcome.state}</Chip> {v.lastOutcome.message}{" "}
                  <span className="muted">{when(v.lastOutcome.at)}</span>
                  {v.lastImport && v.lastOutcome.state === "succeeded" && (
                    <>
                      <br />
                      Imported {v.lastImport.result.imported}, replaced {v.lastImport.result.replaced}, failed {v.lastImport.result.failed}, not in your
                      Chrome {v.lastImport.result.notInSource}.
                    </>
                  )}
                </p>
              )}
              {undone && <p className="note ok">{undone}</p>}
            </div>
            {v.undoAvailable && (
              <div className="actions">
                <button type="button" disabled={undo.busy || waiting} onClick={() => setAsking(true)}>
                  Undo last sync{v.lastImport?.domains.length ? ` (${v.lastImport.domains.join(", ")})` : ""}
                </button>
              </div>
            )}
            {undo.error && <ErrorNote error={undo.error} />}
            {asking && (
              <ConfirmDialog
                title="Undo the last sync"
                consequence="The apply window's cookies for the synced sites return to what they were before the sync."
                verb="Undo sync"
                onCancel={() => setAsking(false)}
                onConfirm={() => void doUndo()}
              />
            )}
          </>
        );
      }}
    </Panel>
  );
}
