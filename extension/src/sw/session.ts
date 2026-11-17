// Copying sign-ins to the apply window, through the extension only. Cookie
// values go from the cookie API into one request body and nowhere else.
import type {
  SessionCheckin,
  SessionCheckinReply,
  SessionCookie,
  SessionImport,
  SessionImportResult,
  SessionPush,
} from "../../../web/src/wire";
import { ApiError } from "./api";
import { on } from "./router";
import { api, isManagedBrowser } from "./state";
import * as store from "./store";
import { normalizeHost } from "../shared/messages";

const CHECKIN_ALARM = "session-checkin";
export const MAX_COOKIES = 2000;
export const MAX_BYTES = 1024 * 1024;
const MAX_SITES = 200;
const SITES_KEY = "sessionSites";

const FORBIDDEN = ["google.com", "youtube.com"];

/** google.com, youtube.com and everything below them are never read or sent. */
export function isForbidden(domain: string): boolean {
  const d = normalizeHost(domain).replace(/^\./, "");
  return FORBIDDEN.some((f) => d === f || d.endsWith(`.${f}`));
}

export type WireCookie = SessionCookie;

const unpartitioned = (c: chrome.cookies.Cookie) => c.partitionKey === undefined;

/** Cookies a site would receive plus those of its subdomains, unpartitioned only. */
async function cookiesOf(domain: string): Promise<chrome.cookies.Cookie[]> {
  const d = normalizeHost(domain).replace(/^\./, "");
  const [sent, below] = await Promise.all([chrome.cookies.getAll({ url: `https://${d}/` }), chrome.cookies.getAll({ domain: d })]);
  const seen = new Map<string, chrome.cookies.Cookie>();
  for (const c of [...sent, ...below]) if (unpartitioned(c)) seen.set(`${c.domain}\n${c.path}\n${c.name}`, c);
  return [...seen.values()];
}

async function countFor(domain: string): Promise<number> {
  return isForbidden(domain) ? 0 : (await cookiesOf(domain)).length;
}

function wire(c: chrome.cookies.Cookie): WireCookie {
  const w: WireCookie = {
    name: c.name,
    value: c.value,
    domain: c.domain,
    path: c.path,
    secure: c.secure,
    httpOnly: c.httpOnly,
    hostOnly: c.hostOnly,
    session: c.session,
    sameSite: c.sameSite,
  };
  if (c.expirationDate !== undefined) w.expirationDate = c.expirationDate;
  return w;
}

/** The cookies for some domains, within the request caps. */
export async function collect(domains: string[]): Promise<{ cookies: WireCookie[]; skipped: string[]; truncated: boolean }> {
  const cookies: WireCookie[] = [];
  const skipped: string[] = [];
  let bytes = 2;
  let truncated = false;
  for (const domain of domains) {
    if (isForbidden(domain)) {
      skipped.push(domain);
      continue;
    }
    for (const c of await cookiesOf(domain)) {
      if (isForbidden(c.domain)) continue;
      const w = wire(c);
      const size = JSON.stringify(w).length + 1;
      if (cookies.length >= MAX_COOKIES || bytes + size > MAX_BYTES) {
        truncated = true;
        break;
      }
      cookies.push(w);
      bytes += size;
    }
  }
  return { cookies, skipped, truncated };
}

async function sites(): Promise<string[]> {
  const got = await chrome.storage.session.get(SITES_KEY);
  return (got[SITES_KEY] as string[] | undefined) ?? [];
}

/** One check-in: counts per listed site go out, the site list and any request come back. */
export async function checkin(): Promise<void> {
  const prefs = await store.prefs();
  if (!prefs.answerSync || !(await store.get("token")) || (await isManagedBrowser())) return;
  const known = (await sites()).filter((d) => !isForbidden(d)).slice(0, MAX_SITES);
  const counts = await Promise.all(known.map(async (domain) => ({ domain, cookies: await countFor(domain) })));
  const reply = await api.call<SessionCheckinReply>("/api/browser/sessions/checkin", { method: "POST", body: { sites: counts } satisfies SessionCheckin });
  await chrome.storage.session.set({ [SITES_KEY]: (reply.sites ?? []).filter((d) => !isForbidden(d)).slice(0, MAX_SITES) });
  const pending = reply.pending;
  if (!pending || pending.domains.length === 0) return;
  const { cookies, skipped, truncated } = await collect(pending.domains);
  await api.call<SessionImportResult>("/api/browser/sessions/imports", {
    method: "POST",
    body: { requestId: pending.requestId, cookies, skipped, truncated } satisfies SessionImport,
  });
}

/** The user's own click: push the current site's cookies once. */
export async function pushSite(host: string): Promise<SessionImportResult> {
  if (await isManagedBrowser()) throw new ApiError("forbidden", "This is the Applysmith browser; there is nothing to copy from it.", 403);
  if (isForbidden(host)) throw new ApiError("forbidden", "Sign-ins for this site are never synced.", 403);
  const { cookies, truncated } = await collect([host]);
  const body = { site: normalizeHost(host), cookies, truncated } satisfies SessionPush;
  return api.call<SessionImportResult>("/api/browser/sessions/pushes", { method: "POST", body });
}

export function registerSession(): void {
  void chrome.alarms.create(CHECKIN_ALARM, { periodInMinutes: 0.5 });
  chrome.alarms.onAlarm.addListener((a) => {
    if (a.name === CHECKIN_ALARM) void checkin().catch(() => {});
  });
  on("sync.host", async (m) => pushSite(m.host), { popupOnly: true });
}
