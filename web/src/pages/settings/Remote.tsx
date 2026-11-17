import { useEffect, useRef, useState } from "react";
import { del, get, patch, post } from "../../api/client";
import { configDocument, configSaved } from "../../api/decoders_pages";
import { remoteDeviceList, remoteEventList, remoteOffer, remoteRevoked, remoteStatus } from "../../api/decoders_remote";
import { useExpiring, useInterval, useResource } from "../../hooks";
import { CopyButton } from "../../ui/CopyButton";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Loading, Panel, QrCode } from "../../ui/parts";
import type { RemoteDevice, RemoteOffer, RemoteStatus } from "../../wire";
import { useAction, when } from "../action";

const TUNNEL_TONE: Record<string, "good" | "warn" | "bad" | "neutral"> = { running: "good", starting: "warn", error: "bad", failed: "bad", stopped: "neutral", off: "neutral" };


// ModeControl turns remote access on or off by editing policy.yml through the
// dashboard session. Turning it on states what becomes reachable first.
function ModeControl({ status, onChanged }: { status: RemoteStatus; onChanged: () => void }) {
  const [asking, setAsking] = useState(false);
  const act = useAction();
  const on = status.mode !== "off";
  const write = async (mode: "tunnel" | "off") => {
    const done = await act.run(async () => {
      const doc = await get("/api/config/policy", configDocument);
      await patch("/api/config/policy", configSaved, { expectedRevision: doc.revision, ops: [{ op: "set", path: "remote_access.mode", value: mode }] });
      return true;
    });
    setAsking(false);
    if (done) onChanged();
  };
  return (
    <div>
      <p>
        Mode: <Chip tone={on ? "warn" : "good"}>{on ? status.mode : "off"}</Chip>{" "}
        <span className="muted">{on ? "A running server applies a change within 5 seconds." : "Off: only this computer can reach the dashboard."}</span>
      </p>
      {on && (
        <p role="note" className="note">
          The dashboard is reachable from the internet through an HTTPS tunnel and protected by device credentials. Each paired device can read your job search and chat in read and draft mode; it cannot change settings, read secrets or apply.
        </p>
      )}
      <div className="actions">
        {on ? (
          <button type="button" disabled={act.busy} onClick={() => void write("off")}>
            Turn remote access off
          </button>
        ) : (
          <button type="button" disabled={act.busy} onClick={() => setAsking(true)}>
            Turn remote access on
          </button>
        )}
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {asking && (
        <ConfirmDialog
          title="Turn on remote access"
          consequence="This makes the dashboard reachable from the internet through an HTTPS tunnel. Anyone who holds a paired device can read your tracker, reports and runs and chat with the agent in read and draft mode."
          details={
            <ul>
              <li>Pairing needs a one-time code that you create here, valid for 5 minutes.</li>
              <li>A device cannot change settings, read secrets, confirm a submission or apply.</li>
              <li>It needs cloudflared, which you install yourself, and a configured named Cloudflare Tunnel with an HTTPS public hostname.</li>
              <li>You can revoke any device, or turn this off, at any time.</li>
            </ul>
          }
          verb="Turn on"
          busy={act.busy}
          onCancel={() => setAsking(false)}
          onConfirm={() => void write("tunnel")}
        />
      )}
    </div>
  );
}

function TunnelState({ status, onChanged }: { status: RemoteStatus; onChanged: () => void }) {
  const act = useAction();
  const t = status.tunnel;
  const retry = async () => {
    if (await act.run(() => post("/api/remote/retry", remoteStatus))) onChanged();
  };
  return (
    <div>
      <h3>Tunnel</h3>
      <p>
        State: <Chip tone={TUNNEL_TONE[t.state] ?? "neutral"}>{t.state}</Chip>
        {t.publicUrl && <span> {t.publicUrl}</span>}
        {t.version && <span className="muted"> cloudflared {t.version}</span>}
      </p>
      {status.error && <p role="alert" className="note error">{status.error}</p>}
      {t.error && <p role="alert" className="note error">{t.error}</p>}
      <p className="muted">
        Listening on {status.listeners.map((l) => `${l.name} ${l.address}`).join(", ")}. Every listener is on this computer only; the tunnel reaches the tunnel listener.
      </p>
      <p className="muted">
        Remote access requires a named Cloudflare Tunnel for live updates and chat. Configure its token and HTTPS public URL before turning it on. Quick Tunnels do not support live updates. A named tunnel is set up in the Cloudflare dashboard: point its public hostname at <code>http://{status.listeners.find((l) => l.name === "tunnel")?.address ?? "127.0.0.1:<tunnel_port>"}</code>, never at the dashboard port, which refuses what a tunnel forwards.
      </p>
      {(t.state === "error" || t.state === "failed") && (
        <div className="actions">
          <button type="button" disabled={act.busy} onClick={() => void retry()}>
            Retry the tunnel
          </button>
        </div>
      )}
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// Pairing shows one offer at a time, with its countdown; it is held in memory
// and dropped when it ends.
function Pairing({ status }: { status: RemoteStatus }) {
  const { item: offer, seconds, expired, hold, asking } = useExpiring<RemoteOffer>();
  const act = useAction();
  const ready = status.tunnel.state === "running" && status.tunnel.publicUrl.startsWith("https://");

  // A device that paired has used the offer.
  useEffect(() => hold(null), [status.devices]);

  const create = async () => {
    asking();
    const next = await act.run(() => post("/api/remote/offers", remoteOffer));
    if (next) hold(next);
  };
  return (
    <div>
      <h3>Pairing</h3>
      <div className="actions">
        <button type="button" disabled={!ready || act.busy} onClick={() => void create()}>
          Pair a device
        </button>
        {!ready && <span className="muted">Available while the tunnel is running.</span>}
      </div>
      {offer && (
        <div>
          <QrCode rows={offer.qr.rows} size={offer.qr.size} label="QR code of the pairing link" />
          <p>
            Scan the code with the phone, or open this link on it. It works once.
          </p>
          <p>
            <code className="pairing-link">{offer.link}</code> <CopyButton text={offer.link} label="Copy the pairing link" />
          </p>
          <p role="timer">Expires in {seconds} s</p>
        </div>
      )}
      {expired && (
        <p role="status" className="muted">
          The offer expired. Create a new one.
        </p>
      )}
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// usePoll polls while the section is shown and reads once as it shows again.
function usePoll(reload: () => void, ms: number, shown: boolean) {
  const hidden = useRef(false);
  useEffect(() => {
    if (!shown) hidden.current = true;
    else if (hidden.current) {
      hidden.current = false;
      reload();
    }
  }, [shown]);
  useInterval(reload, ms, shown);
}

function Devices({ on, onChanged, shown }: { on: boolean; onChanged: () => void; shown: boolean }) {
  const [list, reload] = useResource(() => get("/api/remote/devices", remoteDeviceList), [], ["notifications"], { keep: "settings.remote-devices" });
  const [revoking, setRevoking] = useState<RemoteDevice | "all" | null>(null);
  const act = useAction();
  usePoll(reload, 15000, shown);
  const revoke = async () => {
    const target = revoking;
    setRevoking(null);
    if (!target) return;
    const done = await act.run(() => (target === "all" ? post("/api/remote/devices/revoke-all", remoteRevoked) : del(`/api/remote/devices/${encodeURIComponent(target.id)}`, remoteRevoked)));
    if (done) {
      reload();
      onChanged();
    }
  };
  return (
    <div>
      <h3>Paired devices</h3>
      {list.status === "loading" && <Loading label="Loading the paired devices" lines={2} />}
      {list.status === "error" && <ErrorNote error={list.error} onRetry={reload} what="the paired devices" />}
      {list.data && list.data.devices.length === 0 && <Empty>No devices are paired. {on ? "Pair a device above once the tunnel is running." : "Turn remote access on above, then pair a device."}</Empty>}
      {list.data && list.data.devices.length > 0 && (
        <>
          <table className="cards">
            <caption className="sr-only">Paired devices</caption>
            <thead>
              <tr>
                <th scope="col">Device</th>
                <th scope="col">Paired</th>
                <th scope="col">Last seen</th>
                <th scope="col">Status</th>
                <th scope="col">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.data.devices.map((d) => (
                <tr key={d.id}>
                  <td data-label="Device">{d.label}</td>
                  <td data-label="Paired">{when(d.createdAt)}</td>
                  <td data-label="Last seen">{d.lastSeenAt ? when(d.lastSeenAt) : "never"}</td>
                  <td data-label="Status">
                    <Chip tone={d.connected ? "good" : "neutral"}>{d.connected ? "connected" : "idle"}</Chip>
                  </td>
                  <td>
                    <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRevoking(d)} aria-label={`Revoke ${d.label}`}>
                      Revoke
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="actions">
            <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRevoking("all")}>
              Revoke all devices
            </button>
          </div>
        </>
      )}
      {act.error && <ErrorNote error={act.error} />}
      {revoking && (
        <ConfirmDialog
          title={revoking === "all" ? "Revoke every device" : `Revoke ${revoking.label}`}
          consequence={revoking === "all" ? "Every paired device is signed out at once and its open streams end. They must be paired again." : "This device is signed out at once and its open streams end. It must be paired again."}
          verb="Revoke"
          onCancel={() => setRevoking(null)}
          onConfirm={() => void revoke()}
        />
      )}
    </div>
  );
}

function Events() {
  const [events, reload] = useResource(() => get("/api/remote/events", remoteEventList), [], ["notifications"], { keep: "settings.remote-events" });
  return (
    <details>
      <summary>Recent remote events</summary>
      {events.status === "error" && <ErrorNote error={events.error} onRetry={reload} what="the remote events" />}
      {events.data && events.data.events.length === 0 && <Empty>Nothing has happened yet.</Empty>}
      {events.data && events.data.events.length > 0 && (
        <ul className="plain">
          {events.data.events.map((e, i) => (
            <li key={`${e.at}-${i}`}>
              <span className="muted">{when(e.at)}</span> {e.kind} {e.source && <span className="muted">from {e.source}</span>} {e.detail}
            </li>
          ))}
        </ul>
      )}
    </details>
  );
}

export function RemoteSection({ shown = true }: { shown?: boolean }) {
  const [status, reload] = useResource(() => get("/api/remote/status", remoteStatus), [], ["notifications"], { keep: "settings.remote-status" });
  // The tunnel's state changes without a board notice, and a mode written to
  // the file is applied a moment later.
  usePoll(reload, 3000, shown);
  return (
    <Panel title="Remote access" id="settings-remote" res={status} retry={reload} what="the remote access state">
      {(s) => (
        <>
          <ModeControl status={s} onChanged={reload} />
          {s.mode !== "off" && (
            <>
              <TunnelState status={s} onChanged={reload} />
              <Pairing status={s} />
              <Devices on onChanged={reload} shown={shown} />
              <Events />
            </>
          )}
          {s.mode === "off" && <Devices on={false} onChanged={reload} shown={shown} />}
        </>
      )}
    </Panel>
  );
}
