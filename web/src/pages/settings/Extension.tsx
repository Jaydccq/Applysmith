import { useEffect, useState } from "react";
import { ApiError, del, get, post } from "../../api/client";
import { extensionBuild, extensionClients, pairingCode } from "../../api/decoders";
import { getShellInfo, hasShell } from "../../desktop";
import { asApiError, useExpiring, useInterval, useResource } from "../../hooks";
import { CopyButton } from "../../ui/CopyButton";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote, Loading, formatDateTime } from "../../ui/parts";
import type { PairingCode } from "../../wire";
import { useAction } from "../action";
import { waitedLabel } from "../runs/format";


// The code is held in memory only and dropped the moment it expires: pairing
// material is shown only while it is valid.
function Pairing() {
  const { item: code, seconds, expired, hold, asking } = useExpiring<PairingCode>();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ApiError | null>(null);

  const request = async () => {
    setBusy(true);
    setFailure(null);
    asking();
    try {
      hold(await post("/api/pairing/codes", pairingCode));
    } catch (err) {
      setFailure(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <button type="button" onClick={() => void request()} disabled={busy}>
        Pair the extension
      </button>
      {code && (
        <div>
          <p>
            Type this code into the extension's toolbar popup. It works once, and its first part is this application's port, so the extension finds it without an address.
          </p>
          <p>
            <strong className="pairing-code">{[window.location.port, code.code].filter(Boolean).join("-")}</strong>
          </p>
          <p role="timer">Expires in {seconds} s</p>
        </div>
      )}
      {expired && <p role="status">The code expired. Request a new one.</p>}
      {failure && <ErrorNote error={failure} />}
    </div>
  );
}

// Paired lists the installs that used the token since the application
// started: presence, as every paired extension holds the same token. It
// reloads at the extension's 30-second check-in while its section is shown.
function Paired({ shipped, shown }: { shipped?: string; shown: boolean }) {
  const [res, reload] = useResource(() => get("/api/extension/clients", extensionClients), []);
  const act = useAction();
  const [asking, setAsking] = useState(false);
  useInterval(reload, 30_000, shown);
  if (res.status === "loading") return <Loading label="Loading the paired extensions" lines={1} />;
  if (!res.data) return <ErrorNote error={res.error} onRetry={reload} what="the paired extensions" />;
  const unpair = async () => {
    setAsking(false);
    if (await act.run(() => del("/api/extension/clients", extensionClients))) reload();
  };
  const ago = (iso: string) => {
    const w = waitedLabel(Math.max(0, (Date.now() - Date.parse(iso)) / 1000));
    return w === "just now" ? w : `${w} ago`;
  };
  return (
    <>
      {res.data.clients.length === 0 ? (
        <p>
          <Chip tone="warn">Not paired</Chip> No extension has reached this application since it started ({formatDateTime(res.data.since)}). A paired extension checks in every 30 seconds while Chrome is open; if yours does not appear, pair it again.
        </p>
      ) : (
        <ul className="plain stack">
          {res.data.clients.map((c) => (
            <li key={c.id}>
              <Chip tone="good">Paired</Chip> {c.managed ? "Applysmith browser" : "Chrome"}, last seen {ago(c.lastSeen)}
              {c.version && ` (version ${c.version})`}
              {shipped && c.build && c.build !== shipped && <> <Chip tone="warn">another build</Chip> press Reload extension in its popup</>}
            </li>
          ))}
        </ul>
      )}
      {res.data.clients.length > 0 && (
        <p className="actions">
          <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setAsking(true)}>
            Unpair extensions
          </button>
        </p>
      )}
      {act.error && <ErrorNote error={act.error} />}
      {asking && (
        <ConfirmDialog
          title="Unpair every extension"
          consequence="Every paired extension shares one token. It is replaced now, so each one stops filling until it is paired again with a new code. The command line and the MCP server read the new token themselves."
          verb="Unpair"
          onCancel={() => setAsking(false)}
          onConfirm={() => void unpair()}
        />
      )}
    </>
  );
}

// Install says how to load the extension in Chrome. In the app it names the
// folder the app keeps current with each build; in a browser it gives the
// command that writes one.
function Install() {
  const [dir, setDir] = useState<string | undefined>(hasShell() ? undefined : "");
  useEffect(() => {
    if (hasShell()) void getShellInfo().then((i) => setDir(i.extensionDir), () => setDir(""));
  }, []);
  if (dir === undefined) return null;
  if (!dir)
    return (
      <p>
        To install it, run <code>applysmith extension export &lt;dir&gt;</code> with an empty directory, open the extensions page of Chrome, switch on developer mode and load that directory.
      </p>
    );
  return (
    <>
      <p>
        The app keeps the extension in <code>{dir}</code> <CopyButton text={dir} label="Copy the extension folder's path" /> and writes each new build there when it updates. To load it:
      </p>
      <ol>
        <li>
          In Chrome, open <code>chrome://extensions</code> and switch on Developer mode.
        </li>
        <li>Press Load unpacked. In the folder dialog press Command-Shift-G, paste the path, press Return, then Select.</li>
        <li>After an app update, press Reload extension in the extension's popup.</li>
      </ol>
    </>
  );
}

export function ExtensionSection({ shown = true }: { shown?: boolean }) {
  const [build, reload] = useResource(() => get("/api/extension/build", extensionBuild), [], [], { keep: "settings.extension" });
  return (
    <section aria-labelledby="settings-extension" className="panel">
      <header className="panel-head">
        <h2 id="settings-extension">Browser extension</h2>
      </header>
      {build.status === "error" && <ErrorNote error={build.error} onRetry={reload} what="the extension build" />}
      <p className="build-line">
        {build.status === "loading" && <span role="status" className="muted">Loading</span>}
        {build.data && (
          <>
            Extension build this application ships: <strong>{build.data.version}</strong> <code>{build.data.stamp}</code>
          </>
        )}
      </p>
      <p>
        The extension reaches this application at <code className="extension-address">{window.location.origin}</code>. This address stays the same after a restart unless another program takes its port; the popup then shows Offline, and a new pairing code connects it again.
      </p>
      <Paired shipped={build.data?.stamp} shown={shown} />
      <Install />
      <Pairing />
    </section>
  );
}
