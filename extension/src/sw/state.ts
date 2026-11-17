// The connection and what the popup and panel show about it.
import { Api, ApiError, OFFLINE, UNAUTHENTICATED, type Connection as ApiConnection } from "./api";
import { claimWait } from "./pairing";
import * as store from "./store";
import type { BuildInfo, Connection, StateView } from "../shared/messages";

let clientLine: Promise<string> | undefined;

/** Names this install, its build and its browser to the application, which lists them in Settings, Browser extension. */
function client(): Promise<string> {
  clientLine ??= (async () => {
    let id = await store.get("install");
    if (!id) await store.put("install", (id = crypto.randomUUID()));
    const b = extensionBuild();
    return `id=${id}; version=${b.version}; build=${b.stamp}; managed=${(await isManagedBrowser()) ? 1 : 0}`;
  })();
  return clientLine;
}

const conn: ApiConnection = {
  address: () => store.get("address"),
  token: () => store.get("token"),
  rejected: () => store.put("token", undefined),
  client,
};

export const api = new Api(conn);

const HEALTH_TIMEOUT_MS = 3_000;

const MANAGED_COOKIE = { domain: "managed.applysmith.invalid", name: "applysmith_managed" };

/** The managed browser carries a marker cookie; the everyday browser never does. */
export async function isManagedBrowser(): Promise<boolean> {
  const found = await chrome.cookies.getAll(MANAGED_COOKIE);
  return found.length > 0;
}

/** The version shown is the version name's, before the stamp: the manifest version also carries the build number. */
function extensionBuild(): BuildInfo {
  const m = chrome.runtime.getManifest();
  return { stamp: __BUILD_STAMP__, version: (m.version_name ?? m.version).split("+")[0] };
}

/**
 * Health first (public), then one authenticated request when a token is held:
 * a rejection drops the token, so the popup shows the unpaired state.
 */
export async function connection(): Promise<Connection> {
  // The application answers health at once on loopback; an address that holds
  // the connection open without answering is another program, and waiting the
  // full call timeout would leave the popup and the panel loading for a minute.
  const probe = new AbortController();
  const timer = setTimeout(() => probe.abort(), HEALTH_TIMEOUT_MS);
  try {
    await api.call("/api/health", { anonymous: true, signal: probe.signal });
  } catch (e) {
    if (e instanceof ApiError && e.code === OFFLINE) return "offline";
  } finally {
    clearTimeout(timer);
  }
  if (!(await store.get("token"))) return "unpaired";
  try {
    await api.call("/api/answer-bank/settings");
    return "connected";
  } catch (e) {
    if (e instanceof ApiError && e.code === OFFLINE) return "offline";
    if (e instanceof ApiError && e.code === UNAUTHENTICATED) return "unpaired";
    return "connected";
  }
}

export async function stateView(): Promise<StateView> {
  const [connectionState, address, prefs, wait, managed] = await Promise.all([
    connection(),
    store.get("address"),
    store.prefs(),
    claimWait(),
    isManagedBrowser(),
  ]);
  const view: StateView = { connection: connectionState, address, extension: extensionBuild(), prefs, managed };
  if (wait > 0) view.claimWait = wait;
  if (connectionState === "connected") {
    try {
      view.application = await api.call<BuildInfo>("/api/extension/build");
    } catch {
      /* an application without the route simply reports no build */
    }
  }
  return view;
}
