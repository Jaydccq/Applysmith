// Extension storage, owned by the service worker. The area is restricted to
// trusted contexts at start, so content scripts and pages can read none of it;
// they ask the worker.
import { DEFAULT_ADDRESS } from "./api";
import { DEFAULT_PREFS, normalizeHost, type Prefs, type SubmissionEntry, type UiState } from "../shared/messages";

interface Shape {
  token?: string;
  address: string;
  prefs: Prefs;
  offHosts: string[];
  ui: Record<string, UiState>;
  /** Posting identity keys whose applied banner the person dismissed, newest last. */
  dismissed: string[];
  submissionQueue: QueuedSubmission[];
  /** A random id that tells this install apart in the dashboard's list. */
  install?: string;
}

/** A submission record waiting for the application to come back. It holds no form value. */
export interface QueuedSubmission {
  queuedAt: number;
  entry: SubmissionEntry;
}

/** What one browser tab remembers about the panel. */
interface TabUi {
  open?: boolean;
  tab?: string;
  autoHosts?: string[];
}

const DEFAULTS: Shape = { address: DEFAULT_ADDRESS, prefs: DEFAULT_PREFS, offHosts: [], ui: {}, dismissed: [], submissionQueue: [] };

let restricted: Promise<void> | undefined;

/** Run once per worker; every session write waits for it, so nothing is stored before content scripts are locked out. */
export function restrictToTrusted(): Promise<void> {
  restricted ??= (async () => {
    await chrome.storage.local.setAccessLevel({ accessLevel: "TRUSTED_CONTEXTS" });
    await chrome.storage.session.setAccessLevel({ accessLevel: "TRUSTED_CONTEXTS" });
  })();
  return restricted;
}

/** Per-session records of the worker (a fill's state, a pinned résumé); gone when the browser closes. */
export async function sessionGet<T>(key: string): Promise<T | undefined> {
  await restrictToTrusted();
  return (await chrome.storage.session.get(key))[key] as T | undefined;
}

export async function sessionPut(key: string, value: unknown): Promise<void> {
  await restrictToTrusted();
  await chrome.storage.session.set({ [key]: value });
}

export async function sessionRemove(key: string): Promise<void> {
  await restrictToTrusted();
  await chrome.storage.session.remove(key);
}

export async function get<K extends keyof Shape>(key: K): Promise<Shape[K]> {
  const got = await chrome.storage.local.get(key);
  return (got[key] ?? DEFAULTS[key]) as Shape[K];
}

export async function put<K extends keyof Shape>(key: K, value: Shape[K] | undefined): Promise<void> {
  if (value === undefined) await chrome.storage.local.remove(key);
  else await chrome.storage.local.set({ [key]: value });
}

export async function prefs(): Promise<Prefs> {
  return { ...DEFAULT_PREFS, ...(await get("prefs")) };
}

/** The switched-off hosts as stored; a value that is missing, hand-edited or not a list reads as none. */
async function offHosts(): Promise<string[]> {
  const stored: unknown = await get("offHosts");
  return Array.isArray(stored) ? stored.filter((x): x is string => typeof x === "string").map(normalizeHost) : [];
}

export async function isOff(host: string): Promise<boolean> {
  return (await offHosts()).includes(normalizeHost(host));
}

export async function setOff(host: string, off: boolean): Promise<void> {
  const h = normalizeHost(host);
  const rest = (await offHosts()).filter((x) => x !== h);
  await put("offHosts", off ? [...rest, h].sort() : rest);
}

/** Where the panel sits on a host is remembered; whether it is open, and its tab, belong to one browser tab. */
export async function uiOf(host: string, tabId?: number): Promise<UiState> {
  const pos = (await get("ui"))[normalizeHost(host)]?.pos;
  const key = `tab:${tabId}`;
  const tab = tabId === undefined ? {} : ((await chrome.storage.session.get(key))[key] as TabUi | undefined) ?? {};
  return { ...(pos ? { pos } : {}), open: tab.open, tab: tab.tab, auto: tab.autoHosts?.includes(normalizeHost(host)) ?? false };
}

// Each write reads the record and writes it back, and the panel sends several at
// once (open, the automatic-open mark, the tab), so writes run one at a time or a
// later one would put back what an earlier one had just replaced.
let uiWrites: Promise<void> = Promise.resolve();

export function setUi(host: string, patch: UiState, tabId?: number): Promise<void> {
  const write = uiWrites.then(() => writeUi(host, patch, tabId));
  uiWrites = write.catch(() => {});
  return write;
}

async function writeUi(host: string, patch: UiState, tabId?: number): Promise<void> {
  if (patch.pos) {
    const all = await get("ui");
    all[normalizeHost(host)] = { pos: patch.pos };
    // Hosts the user visited long ago fall away so the record cannot grow without bound.
    const keys = Object.keys(all);
    if (keys.length > 200) for (const k of keys.slice(0, keys.length - 200)) delete all[k];
    await put("ui", all);
  }
  if (tabId !== undefined && (patch.open !== undefined || patch.tab !== undefined || patch.auto !== undefined)) {
    const key = `tab:${tabId}`;
    const now = ((await chrome.storage.session.get(key))[key] as TabUi | undefined) ?? {};
    const autoHosts = new Set(now.autoHosts);
    if (patch.auto) autoHosts.add(normalizeHost(host));
    await chrome.storage.session.set({
      [key]: { open: patch.open ?? now.open, tab: patch.tab ?? now.tab, autoHosts: [...autoHosts] },
    });
  }
}
