import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { dispatch } from "../src/sw/router";
import { MAX_BYTES, MAX_COOKIES, checkin, collect, isForbidden, pushSite, registerSession } from "../src/sw/session";
import * as store from "../src/sw/store";
import type { SessionCheckinReply, SessionImportResult } from "../../web/src/wire";
import { installChrome, installFetch, ok, type Call } from "./chrome-mock";

const TOKEN = "S".repeat(43);
const SECRET = "SECRETCOOKIEVALUE";

function cookie(domain: string, name: string, extra: Partial<chrome.cookies.Cookie> = {}): chrome.cookies.Cookie {
  return { name, value: SECRET, domain, path: "/", secure: true, httpOnly: true, hostOnly: false, session: false, sameSite: "lax", storeId: "0", expirationDate: 2e9, ...extra };
}

let jar: chrome.cookies.Cookie[];
let lookups: string[];
let env: ReturnType<typeof installChrome>;
let calls: Call[];
let reply: SessionCheckinReply;
const pending = (requestId: string, domains: string[]): SessionCheckinReply["pending"] => ({ requestId, domains, expiresAt: "2026-01-02T03:06:05.000Z" });
const result: SessionImportResult = { imported: 2, failed: 0, replaced: 1, notInSource: 0, truncated: false };

function match(c: chrome.cookies.Cookie, d: string): boolean {
  const dom = c.domain.replace(/^\./, "");
  return dom === d || dom.endsWith(`.${d}`) || d.endsWith(`.${dom}`);
}

beforeEach(async () => {
  jar = [];
  lookups = [];
  reply = { sites: ["jobs.example.com", "accounts.google.com"], pending: null };
  env = installChrome({
    cookies: {
      getAll: vi.fn(async (q: { url?: string; domain?: string; name?: string }) => {
        const d = q.domain ?? new URL(q.url!).hostname;
        lookups.push(d);
        if (q.name) return jar.filter((c) => c.name === q.name && match(c, d));
        return jar.filter((c) => match(c, d));
      }),
    },
    alarms: { create: vi.fn(), onAlarm: { addListener: vi.fn() } },
  });
  await store.put("token", TOKEN);
  calls = installFetch((c) => {
    const path = new URL(c.url).pathname;
    if (path.endsWith("/checkin")) return ok(reply);
    return ok(result);
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("forbidden domains", () => {
  it("never reads google.com, youtube.com or anything below them", () => {
    for (const d of ["google.com", "accounts.google.com", ".google.com", "www.youtube.com", "studio.youtube.com"]) expect(isForbidden(d), d).toBe(true);
    for (const d of ["notgoogle.com", "google.com.example.org", "example.com"]) expect(isForbidden(d), d).toBe(false);
  });

  it("a request naming accounts.google.com reads no cookie for it and says so", async () => {
    jar.push(cookie(".google.com", "SID"));
    reply = { sites: [], pending: pending("req_1", ["accounts.google.com", "jobs.example.com"]) };
    jar.push(cookie("jobs.example.com", "sess"));
    await checkin();
    expect(lookups.some((d) => d.includes("google"))).toBe(false);
    const imp = calls.find((c) => c.url.endsWith("/imports"))!.body as { skipped: string[]; cookies: { domain: string }[]; requestId: string };
    expect(imp.skipped).toEqual(["accounts.google.com"]);
    expect(imp.requestId).toBe("req_1");
    expect(imp.cookies.every((c) => !c.domain.includes("google"))).toBe(true);
  });
});

describe("check-in", () => {
  it("sends counts only: domains and integers, no cookie name or value", async () => {
    await chrome.storage.session.set({ sessionSites: ["jobs.example.com"] });
    jar.push(cookie(".example.com", "session_id"), cookie("jobs.example.com", "csrf"));
    await checkin();
    const body = calls[0]!.body as { sites: { domain: string; cookies: number }[] };
    expect(body.sites).toEqual([{ domain: "jobs.example.com", cookies: 2 }]);
    const text = JSON.stringify(body);
    expect(text).not.toContain(SECRET);
    expect(text).not.toContain("session_id");
    expect(text).not.toContain("csrf");
  });

  it("sends no counts on the first check-in after a restart, and keeps the site list it gets", async () => {
    await checkin();
    expect((calls[0]!.body as { sites: unknown[] }).sites).toEqual([]);
    const stored = (await chrome.storage.session.get("sessionSites")) as { sessionSites: string[] };
    expect(stored.sessionSites).toEqual(["jobs.example.com"]);
  });

  it("answers a pending request with the unpartitioned cookies of the domain and its subdomains, in one request", async () => {
    jar.push(cookie(".example.com", "a"), cookie("auth.jobs.example.com", "b", { httpOnly: true }), cookie("jobs.example.com", "p", { partitionKey: { topLevelSite: "https://other.example.org" } }));
    reply = { sites: [], pending: pending("req_9", ["jobs.example.com"]) };
    await checkin();
    const imports = calls.filter((c) => c.url.endsWith("/imports"));
    expect(imports).toHaveLength(1);
    const names = (imports[0]!.body as { cookies: { name: string }[] }).cookies.map((c) => c.name).sort();
    expect(names).toEqual(["a", "b"]);
  });

  it("sends nothing when the switch is off, when unpaired, or in the managed browser", async () => {
    await store.put("prefs", { editWithAI: true, answerSync: false, autoOpen: true });
    await checkin();
    expect(calls).toHaveLength(0);
    await store.put("prefs", { editWithAI: true, answerSync: true, autoOpen: true });
    await store.put("token", undefined);
    await checkin();
    expect(calls).toHaveLength(0);
    await store.put("token", TOKEN);
    jar.push(cookie("managed.applysmith.invalid", "applysmith_managed"));
    await checkin();
    expect(calls).toHaveLength(0);
  });
});

describe("request caps", () => {
  it("sends at most 2000 cookies", async () => {
    for (let i = 0; i < MAX_COOKIES + 500; i++) jar.push(cookie("jobs.example.com", `c${i}`, { value: "v" }));
    const out = await collect(["jobs.example.com"]);
    expect(out.cookies).toHaveLength(MAX_COOKIES);
    expect(out.truncated).toBe(true);
  });

  it("sends at most 1 MiB", async () => {
    for (let i = 0; i < 400; i++) jar.push(cookie("jobs.example.com", `c${i}`, { value: "x".repeat(4000) }));
    const out = await collect(["jobs.example.com"]);
    expect(JSON.stringify(out.cookies).length).toBeLessThanOrEqual(MAX_BYTES);
    expect(out.truncated).toBe(true);
  });
});

describe("user push", () => {
  it("sends the current site's cookies once and returns only counts", async () => {
    jar.push(cookie("jobs.example.com", "sess"));
    const r = await pushSite("www.jobs.example.com");
    expect(r).toEqual(result);
    const pushes = calls.filter((c) => c.url.endsWith("/pushes"));
    expect(pushes).toHaveLength(1);
    expect((pushes[0]!.body as { site: string }).site).toBe("jobs.example.com");
  });

  it("refuses google and the managed browser", async () => {
    await expect(pushSite("accounts.google.com")).rejects.toMatchObject({ code: "forbidden" });
    jar.push(cookie("managed.applysmith.invalid", "applysmith_managed"));
    await expect(pushSite("jobs.example.com")).rejects.toMatchObject({ code: "forbidden" });
    expect(calls).toHaveLength(0);
  });

  it("is a popup action only", async () => {
    registerSession();
    const fromPage = await dispatch({ kind: "sync.host", host: "jobs.example.com" }, { id: "ext-id", tab: { id: 1 } as chrome.tabs.Tab, url: "https://example.com/" });
    expect(fromPage).toMatchObject({ ok: false, error: { code: "unauthorized" } });
    const fromPopup = await dispatch({ kind: "sync.host", host: "jobs.example.com" }, { id: "ext-id", url: "chrome-extension://ext-id/popup.html" });
    expect(fromPopup).toMatchObject({ ok: true, data: { imported: 2 } });
  });
});

describe("cookie values stay in one request body", () => {
  it("do not reach the console, storage or the reply to the popup", async () => {
    const spies = (["log", "info", "warn", "error", "debug"] as const).map((m) => vi.spyOn(console, m));
    jar.push(cookie("jobs.example.com", "sess"));
    reply = { sites: [], pending: pending("req_2", ["jobs.example.com"]) };
    await checkin();
    registerSession();
    const popup = await dispatch({ kind: "sync.host", host: "jobs.example.com" }, { id: "ext-id", url: "chrome-extension://ext-id/popup.html" });
    for (const s of spies) expect(JSON.stringify(s.mock.calls)).not.toContain(SECRET);
    expect(JSON.stringify(env.local.data)).not.toContain(SECRET);
    expect(JSON.stringify(env.session.data)).not.toContain(SECRET);
    expect(JSON.stringify(popup)).not.toContain(SECRET);
    // The value did travel, once per request, in the body.
    expect(calls.filter((c) => JSON.stringify(c.body).includes(SECRET)).length).toBe(2);
    spies.forEach((s) => s.mockRestore());
  });
});
