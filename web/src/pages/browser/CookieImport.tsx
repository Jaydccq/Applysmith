import { useEffect, useMemo, useState } from "react";
import { get, post } from "../../api/client";
import { forgetResult, importedView, importSites, importSources, sessionImportResult } from "../../api/decoders_browser";
import { useResource } from "../../hooks";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import type { ImportSource, SessionImportResult } from "../../wire";
import { useAction, when } from "../action";

// CookieImport brings the user's own sign-in cookies from an installed browser
// into the apply window, without the extension. It lists the browser profiles
// on this computer, lets the user pick a profile and its sites, and imports
// them under the browser's keychain approval. The "Brought over" list clears
// one imported site at a time. Values never reach the page: only host names
// and counts.
export function CookieImport({ onImported }: { onImported: () => void }) {
  const [sources, reloadSources] = useResource(() => get("/api/browser/import/sources", importSources), [], ["browser"]);
  const [imported, reloadImported] = useResource(() => get("/api/browser/imported", importedView), [], ["browser"]);
  const refresh = () => {
    reloadImported();
    onImported();
  };
  return (
    <>
      <Panel title="Import from your browser" id="browser-import" res={sources} retry={reloadSources}>
        {(v) =>
          v.supported ? <Wizard sources={v.sources} onImported={refresh} /> : <p className="muted">{v.reason || "Importing cookies from an installed browser is not available here."}</p>
        }
      </Panel>
      <Panel title="Brought over" id="browser-imported" res={imported} retry={reloadImported}>
        {(v) =>
          v.sites.length === 0 ? (
            <Empty>No site's sign-in has been brought into the apply window yet.</Empty>
          ) : (
            <ul className="plain stack">
              {v.sites.map((s) => (
                <ImportedRow key={s.host} host={s.host} cookies={s.cookies} at={s.at} onCleared={refresh} />
              ))}
            </ul>
          )
        }
      </Panel>
    </>
  );
}

function Wizard({ sources, onImported }: { sources: ImportSource[]; onImported: () => void }) {
  const [sourceId, setSourceId] = useState(sources[0]?.id ?? "");
  const [filter, setFilter] = useState("");
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const [result, setResult] = useState<SessionImportResult | null>(null);
  const run = useAction();

  const [sites, reloadSites] = useResource(() => (sourceId ? get(`/api/browser/import/sources/${encodeURIComponent(sourceId)}/sites`, importSites) : Promise.resolve({ sites: [] })), [sourceId], []);
  // A new source clears the choice and the last result.
  useEffect(() => {
    setChecked(new Set());
    setResult(null);
  }, [sourceId]);

  const hosts = sites.data?.sites ?? [];
  const shown = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return f ? hosts.filter((h) => h.host.includes(f)) : hosts;
  }, [hosts, filter]);

  const toggle = (host: string) =>
    setChecked((prev) => {
      const next = new Set(prev);
      if (next.has(host)) next.delete(host);
      else next.add(host);
      return next;
    });
  const allShownChecked = shown.length > 0 && shown.every((h) => checked.has(h.host));
  const toggleAll = () =>
    setChecked((prev) => {
      const next = new Set(prev);
      if (allShownChecked) shown.forEach((h) => next.delete(h.host));
      else shown.forEach((h) => next.add(h.host));
      return next;
    });

  const doImport = async () => {
    setResult(null);
    const res = await run.run(() => post("/api/browser/import", sessionImportResult, { sourceId, hosts: [...checked] }));
    if (res) {
      setResult(res);
      setChecked(new Set());
      onImported();
    }
  };

  return (
    <>
      <p className="muted">
        Brings a site's sign-in from a browser on this computer into the apply window, so the agent reaches pages you are already signed in to. macOS asks for
        your Mac login password to unlock the browser's cookies; nothing leaves this computer. Only host names and counts are shown here, never a cookie.
      </p>
      {sources.length > 1 && (
        <label className="field">
          <span>From</span>
          <select value={sourceId} onChange={(e) => setSourceId(e.target.value)}>
            {sources.map((s) => (
              <option key={s.id} value={s.id}>
                {s.browserLabel} — {s.profile}
              </option>
            ))}
          </select>
        </label>
      )}
      {sites.status === "error" && sites.error ? (
        <ErrorNote error={sites.error} onRetry={reloadSites} />
      ) : hosts.length === 0 ? (
        <Empty>This browser profile holds no cookies to bring over.</Empty>
      ) : (
        <>
          <div className="row">
            <input type="search" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter sites" aria-label="Filter sites" />
            <button type="button" onClick={toggleAll}>
              {allShownChecked ? "Clear" : "Select all"}
            </button>
          </div>
          <ul className="plain stack scroll-list" aria-label="Sites to bring over">
            {shown.map((h) => (
              <li key={h.host}>
                <label className="row">
                  <input type="checkbox" checked={checked.has(h.host)} onChange={() => toggle(h.host)} />
                  <span>
                    {h.host} <span className="muted">{h.cookies} cookies</span>
                  </span>
                </label>
              </li>
            ))}
            {shown.length === 0 && <Empty>No site matches.</Empty>}
          </ul>
          <div className="actions">
            <button type="button" disabled={run.busy || checked.size === 0} onClick={() => void doImport()}>
              {run.busy ? "Importing…" : `Import ${checked.size || ""} ${checked.size === 1 ? "site" : "sites"}`.trim()}
            </button>
          </div>
        </>
      )}
      {run.error && <ErrorNote error={run.error} />}
      {result && (
        <p className="note ok" role="status">
          Brought in sign-ins for {result.imported} {result.imported === 1 ? "cookie" : "cookies"}
          {result.replaced ? `, replacing ${result.replaced}` : ""}
          {result.notInSource ? `; ${result.notInSource} chosen ${result.notInSource === 1 ? "site had" : "sites had"} no sign-in to bring` : ""}.
        </p>
      )}
    </>
  );
}

function ImportedRow({ host, cookies, at, onCleared }: { host: string; cookies: number; at: string; onCleared: () => void }) {
  const run = useAction();
  const clear = async () => {
    if (await run.run(() => post("/api/browser/imported/forget", forgetResult, { host }))) onCleared();
  };
  return (
    <li className="row">
      <span>
        <strong>{host}</strong> <span className="muted">{cookies} cookies · brought over {when(at)}</span>
      </span>
      <span className="row">
        {run.error && <Chip tone="bad">failed</Chip>}
        <button type="button" disabled={run.busy} aria-label={`Remove ${host}`} onClick={() => void clear()}>
          {run.busy ? "Removing…" : "Remove"}
        </button>
      </span>
    </li>
  );
}
