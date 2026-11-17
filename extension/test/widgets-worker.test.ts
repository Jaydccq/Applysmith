import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { dispatch } from "../src/sw/router";
import { registerAnalyze } from "../src/sw/analyze";
import { registerSignIn } from "../src/sw/signin";
import { MAX_AGE_MS, MAX_QUEUE, flush, registerSubmissions } from "../src/sw/submissions";
import { fieldEvent, registerWidgets } from "../src/sw/widgets";
import * as store from "../src/sw/store";
import { fail, installChrome, installFetch, ok, type Call } from "./chrome-mock";

const TOKEN = "T".repeat(43);
const PAGE = "https://careers.example.com/jobs/1/apply?utm=x";
const TAB = { id: "ext-id", frameId: 0, tab: { id: 9, url: PAGE } as chrome.tabs.Tab, url: PAGE } as chrome.runtime.MessageSender;

let calls: Call[];
let handler: (c: Call) => Response | Promise<Response>;
let executed: unknown[];

const ask = (kind: string, extra: Record<string, unknown> = {}, sender = TAB) => dispatch({ kind, ...extra }, sender);
const entry = (n: number, over: Record<string, unknown> = {}) => ({ entryId: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`, url: "https://careers.example.com/jobs/1/apply", host: "careers.example.com", title: "Apply", at: "2026-10-11T09:00:00.000Z", confirmed: false, ...over });
const posts = () => calls.filter((c) => c.method === "POST" && c.url.endsWith("/api/submissions"));

beforeEach(async () => {
  executed = [];
  const env = installChrome({
    alarms: { create: vi.fn(), onAlarm: { addListener: vi.fn() } },
    webNavigation: { onCommitted: { addListener: vi.fn() } },
    scripting: { executeScript: vi.fn(async (inj: unknown) => void executed.push(inj)) },
  });
  void env;
  await store.put("token", TOKEN);
  calls = installFetch((c) => handler(c));
  handler = () => fail("not_found", "no", 404);
  registerAnalyze();
  registerWidgets();
  registerSignIn();
  registerSubmissions();
});
afterEach(() => vi.unstubAllGlobals());

describe("field report", () => {
  const report = { question: "City", controlKind: "text", options: Array.from({ length: 40 }, (_, i) => `o${i}`), note: "n".repeat(900), tag: "input", role: "", accessibleName: "City", path: "#city", nearbyText: "City", url: "https://careers.example.com/apply?token=abc", frameUrl: "https://careers.example.com/apply#x", shadowDepth: 0, snippet: '<input id="city">' };

  it("is one user-reported event with no value, a reduced address, at most 25 options and a note of at most 500", () => {
    const ev = fieldEvent(report, "careers.example.com", new Date("2026-10-11T09:00:00Z"));
    expect(ev).toMatchObject({ kind: "user-reported", host: "careers.example.com", question: "City", controlKind: "text" });
    expect(ev.options).toHaveLength(25);
    expect(ev.report!.note).toHaveLength(500);
    expect(ev.report!.url).toBe("https://careers.example.com/apply");
    expect(ev.report!.frameUrl).toBe("https://careers.example.com/apply");
    expect(ev).not.toHaveProperty("after");
    expect(ev).not.toHaveProperty("before");
    expect(ev.eventId).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("is posted once and a rejection is shown to the person", async () => {
    handler = () => ok({ accepted: 1, duplicates: [], rejected: [] });
    expect(await ask("report.field", { report })).toMatchObject({ ok: true });
    expect(calls.filter((c) => c.url.endsWith("/api/answer-bank/events"))).toHaveLength(1);
    handler = () => ok({ accepted: 0, duplicates: [], rejected: [{ eventId: "x", reason: "report-rejected" }] });
    expect(await ask("report.field", { report })).toMatchObject({ ok: false, error: { message: expect.stringContaining("report-rejected") } });
    expect(calls.filter((c) => c.url.endsWith("/api/answer-bank/events"))).toHaveLength(2);
  });
});

describe("applied banner", () => {
  const lookup = (status: string, key = "k1") => (c: Call) => (new URL(c.url).pathname === "/api/applications/lookup" ? ok({ found: true, identityKey: key, entry: { id: 1, status, appliedAt: "2026-09-30" } }) : fail("not_found", "no", 404));

  it.each(["applied"])("shows for status %s with the tracker's date", async (status) => {
    handler = lookup(status);
    expect(await ask("banner.get", { url: PAGE })).toMatchObject({ ok: true, data: { show: true, key: "k1", date: "2026-09-30" } });
  });

  it.each(["evaluated"])("stays away for status %s", async (status) => {
    handler = lookup(status);
    expect(await ask("banner.get", { url: PAGE })).toMatchObject({ data: { show: false } });
  });

  it("quotes the ledger's submission over the tracker's status: whether it was confirmed, made by the agent, or seen only in the ledger", async () => {
    const at = "2026-10-01T09:00:00.000Z";
    const withSubmission = (entry: Record<string, unknown> | undefined, submission: Record<string, unknown>) => (c: Call) =>
      new URL(c.url).pathname === "/api/applications/lookup" ? ok({ found: entry !== undefined, identityKey: "k1", entry, submission }) : fail("not_found", "no", 404);
    handler = withSubmission({ id: 1, status: "applied", appliedAt: "2026-09-30" }, { confirmed: false, source: "user-observed", at });
    expect(await ask("banner.get", { url: PAGE })).toMatchObject({ data: { show: true, key: "k1", date: at, submission: { confirmed: false, agent: false, fromLedger: false } } });
    handler = withSubmission(undefined, { confirmed: true, source: "apply-chain", at });
    expect(await ask("banner.get", { url: PAGE })).toMatchObject({ data: { show: true, submission: { confirmed: true, agent: true, fromLedger: true } } });
  });

  it("does not show when the application cannot be reached or the host is switched off", async () => {
    handler = () => {
      throw new Error("down");
    };
    expect(await ask("banner.get", { url: PAGE })).toMatchObject({ data: { show: false } });
    handler = lookup("applied");
    await store.setOff("careers.example.com", true);
    expect(await ask("banner.get", { url: PAGE })).toMatchObject({ data: { show: false } });
  });
});

describe("the page-world hook", () => {
  it("is injected into the page's own world of the top frame only, and not on a host switched off", async () => {
    expect(await ask("hook.install")).toMatchObject({ data: { installed: true } });
    expect(executed[0]).toMatchObject({ target: { tabId: 9 }, files: ["hook.js"], world: "MAIN" });
    expect(await ask("hook.install", {}, { ...TAB, frameId: 4 })).toMatchObject({ data: { installed: false } });
    await store.setOff("careers.example.com", true);
    expect(await ask("hook.install")).toMatchObject({ data: { installed: false } });
    expect(executed).toHaveLength(1);
  });
});

describe("credentials and codes", () => {
  it("asks the vault route for the page's address and passes the answer through unstored", async () => {
    handler = () => ok({ email: "alex.example@example.com", password: "Example-Pass-123", source: "settings-default" });
    const r = await ask("signin.resolve");
    expect(r).toMatchObject({ ok: true, data: { email: "alex.example@example.com" } });
    expect(calls[0]!.body).toEqual({ url: PAGE });
    const g = (globalThis as unknown as { chrome: { storage: { local: { data: object }; session: { data: object } } } }).chrome.storage;
    expect(JSON.stringify([g.local.data, g.session.data])).not.toContain("Example-Pass-123");
  });

  it("passes the vault's reason on so the widget can name the outcome", async () => {
    handler = () => fail("not_found", "none", 404, { details: { reason: "secret_not_found" } });
    expect(await ask("signin.resolve")).toMatchObject({ ok: false, error: { code: "not_found", reason: "secret_not_found" } });
  });

  it("asks for the latest code since an instant and reports no mail yet as null", async () => {
    handler = () => fail("not_found", "no code", 404);
    expect(await ask("otp.latest", { since: "2026-10-11T09:00:00.000Z" })).toMatchObject({ ok: true, data: null });
    expect(new URL(calls[0]!.url).searchParams.get("since")).toBe("2026-10-11T09:00:00.000Z");
    handler = () => ok({ code: "482913", receivedAt: "2026-10-11T09:01:00.000Z", from: "no-reply@mail.example.com" });
    expect(await ask("otp.latest", { since: "2026-10-11T09:00:00.000Z" })).toMatchObject({ data: { code: "482913" } });
  });
});

describe("code lookup sender", () => {
  const on = (url: string) => ({ ...TAB, tab: { id: 9, url } as chrome.tabs.Tab, url }) as chrome.runtime.MessageSender;
  const from = () => new URL(calls.at(-1)!.url).searchParams.get("fromDomain");

  it("bounds the lookup to a site's known code sender, by host or subdomain, and takes any sender elsewhere", async () => {
    handler = () => fail("not_found", "no code", 404);
    await ask("otp.latest", { since: "2026-10-11T09:00:00.000Z" }, on("https://job-boards.greenhouse.io/acme/jobs/1"));
    expect(from()).toBe("greenhouse-mail.io");
    await ask("otp.latest", { since: "2026-10-11T09:00:00.000Z" }, on("https://acme.wd5.myworkdayjobs.com/en-US/careers/login"));
    expect(from()).toBe("otp.workday.com");
    await ask("otp.latest", { since: "2026-10-11T09:00:00.000Z" }, on("https://notgreenhouse.io/apply"));
    expect(from()).toBeNull();
  });
});

describe("code lookup failures", () => {
  it("rejects on a failure of the lookup itself instead of answering no code", async () => {
    handler = () => fail("unavailable", "Gmail needs to be reconnected.", 503);
    expect(await ask("otp.latest", { since: "2026-10-11T09:00:00.000Z" })).toMatchObject({ ok: false, error: { code: "unavailable" } });
  });
});

describe("sign-in link", () => {
  it("asks for the latest link since an instant and reports no mail yet as null", async () => {
    handler = () => fail("not_found", "no link", 404);
    expect(await ask("signin.link", { since: "2026-10-11T09:00:00.000Z" })).toMatchObject({ ok: true, data: null });
    expect(new URL(calls[0]!.url).pathname).toBe("/api/gmail/signin-link/latest");
    handler = () => ok({ url: "https://auth.example.com/magic?token=FAKE_Zx9Yw8Vv7Uu6", host: "auth.example.com", receivedAt: "2026-10-11T09:01:00.000Z", from: "no-reply@auth.example.com" });
    expect(await ask("signin.link", { since: "2026-10-11T09:00:00.000Z" })).toMatchObject({ data: { host: "auth.example.com" } });
  });
});

describe("submission records", () => {
  it("sends the entry with the score and report the tracker knows, and carries no form value", async () => {
    handler = (c) => {
      const p = new URL(c.url).pathname;
      if (p === "/api/applications/lookup") return ok({ found: true, identityKey: "k", entry: { id: 1, status: "evaluated", reportNumber: 12 } });
      if (p === "/api/reports/12") return ok({ report: { number: 12, company: "Example Corp", role: "Engineer", score: 4.2, recommendation: "apply" }, header: {}, content: "" });
      if (p === "/api/submissions") return ok({ ledgerId: 1, result: "created", tracker: "applied" });
      return fail("not_found", "no", 404);
    };
    await ask("submission.record", { entry: entry(1) });
    expect(posts()[0]!.body).toMatchObject({ entryId: entry(1).entryId, url: "https://careers.example.com/jobs/1/apply", score: 4.2, reportNumber: 12, confirmed: false });
  });

  it("queues an entry when the application is closed, keeps at most 50, and flushes when it returns", async () => {
    handler = () => {
      throw new Error("down");
    };
    for (let i = 0; i < 55; i++) await ask("submission.record", { entry: entry(i) });
    const queue = await store.get("submissionQueue");
    expect(queue).toHaveLength(MAX_QUEUE);
    expect(queue[0]!.entry.entryId).toBe(entry(5).entryId);
    calls.length = 0;
    handler = (c) => (new URL(c.url).pathname === "/api/submissions" ? ok({ ledgerId: 1, result: "created", tracker: "created" }) : fail("not_found", "no", 404));
    await flush();
    expect(posts()).toHaveLength(50);
    expect(await store.get("submissionQueue")).toHaveLength(0);
  });

  it("does not resend a failing queue in a loop: one attempt per flush, stopping at the first failure", async () => {
    handler = () => {
      throw new Error("down");
    };
    await ask("submission.record", { entry: entry(1) });
    await ask("submission.record", { entry: entry(2) });
    calls.length = 0;
    await flush();
    expect(posts()).toHaveLength(1);
    expect(await store.get("submissionQueue")).toHaveLength(2);
  });

  it("replaces a queued unconfirmed entry with its confirmed version", async () => {
    handler = () => {
      throw new Error("down");
    };
    await ask("submission.record", { entry: entry(1) });
    await ask("submission.record", { entry: entry(1, { confirmed: true }) });
    const queue = await store.get("submissionQueue");
    expect(queue).toHaveLength(1);
    expect(queue[0]!.entry.confirmed).toBe(true);
  });

  it("drops entries older than 30 days and ones the application can never accept", async () => {
    await store.put("submissionQueue", [{ queuedAt: Date.now() - MAX_AGE_MS - 1, entry: entry(1) as never }]);
    handler = () => ok({});
    await flush();
    expect(posts()).toHaveLength(0);
    expect(await store.get("submissionQueue")).toHaveLength(0);
    handler = () => fail("validation_failed", "bad", 422);
    await ask("submission.record", { entry: entry(2) });
    expect(await store.get("submissionQueue")).toHaveLength(0);
  });

  it("records nothing on a host the person switched off", async () => {
    await store.setOff("careers.example.com", true);
    await ask("submission.record", { entry: entry(1) });
    expect(calls).toHaveLength(0);
    expect(await store.get("submissionQueue")).toHaveLength(0);
  });
});
