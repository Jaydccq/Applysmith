import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { registerPress } from "../src/sw/press";
import { dispatch } from "../src/sw/router";
import { registerSignIn } from "../src/sw/signin";
import { registerSubmissions } from "../src/sw/submissions";
import * as store from "../src/sw/store";
import { fail, installChrome, installFetch, ok, type Call } from "./chrome-mock";

const TOKEN = "T".repeat(43);
const PAGE = "https://careers.example.com/jobs/1/apply?utm=x#step";
const TAB = { id: "ext-id", frameId: 0, tab: { id: 9, url: PAGE } as chrome.tabs.Tab, url: PAGE } as chrome.runtime.MessageSender;

let calls: Call[];
let handler: (c: Call) => Response | Promise<Response>;

const ask = (kind: string, extra: Record<string, unknown> = {}, sender = TAB) => dispatch({ kind, ...extra }, sender);
const claims = () => calls.filter((c) => new URL(c.url).pathname === "/api/submissions/claims");
const entry = { entryId: "00000000-0000-4000-8000-000000000001", url: "https://careers.example.com/jobs/1/apply", host: "careers.example.com", title: "Apply", at: "2026-10-11T09:00:00.000Z", confirmed: false };

beforeEach(async () => {
  installChrome({
    alarms: { create: vi.fn(), onAlarm: { addListener: vi.fn() } },
    webNavigation: { onCommitted: { addListener: vi.fn() } },
  });
  await store.put("token", TOKEN);
  calls = installFetch((c) => handler(c));
  handler = () => fail("not_found", "no", 404);
  registerSubmissions();
  registerPress();
  registerSignIn();
});
afterEach(() => vi.unstubAllGlobals());

describe("press claim", () => {
  it("sends the page without its query or fragment and returns the grant", async () => {
    handler = () => ok({ claimId: "claim-1", expiresAt: "2026-10-11T09:15:00.000Z" });
    expect(await ask("press.claim", { company: "Example Corp", role: "Engineer" })).toEqual({ ok: true, data: { granted: true, claimId: "claim-1" } });
    expect(claims()).toHaveLength(1);
    expect(claims()[0]!.method).toBe("POST");
    expect(claims()[0]!.body).toEqual({ url: "https://careers.example.com/jobs/1/apply", host: "careers.example.com", company: "Example Corp", role: "Engineer" });
  });

  it.each([
    ["a duplicate", () => fail("conflict", "duplicate submit blocked: already submitted", 409, { details: { reason: "duplicate_submit_blocked" } }), /already submitted/],
    ["an unavailable guard", () => fail("unavailable", "the duplicate check and press claim are not available, so nothing was pressed", 503, { details: { reason: "claim_unavailable" } }), /not available/],
    ["an unknown route", () => fail("not_found", "not found", 404), /not pressed/i],
    ["a server error", () => fail("internal", "internal error", 500), /not pressed/i],
    ["a grant with no claim id", () => ok({ expiresAt: "2026-10-11T09:15:00.000Z" }), /not pressed/i],
    ["an empty claim id", () => ok({ claimId: "" }), /not pressed/i],
    ["an empty body", () => ok(undefined), /not pressed/i],
  ])("refuses on %s and says Submit was not pressed", async (_name, answer, text) => {
    handler = answer;
    const r = (await ask("press.claim")) as { ok: true; data: { granted: boolean; message?: string } };
    expect(r.ok).toBe(true);
    expect(r.data.granted).toBe(false);
    expect(r.data.message).toMatch(text);
    expect(r.data.message).toMatch(/not pressed/i);
  });

  it("refuses when the application does not answer", async () => {
    handler = () => {
      throw new TypeError("network down");
    };
    const r = (await ask("press.claim")) as { data: { granted: boolean; message: string } };
    expect(r.data.granted).toBe(false);
    expect(r.data.message).toMatch(/did not answer.*not pressed/i);
    expect(claims()).toHaveLength(1);
  });

  it("refuses when the extension is not paired, without calling the application", async () => {
    await store.put("token", "");
    const r = (await ask("press.claim")) as { data: { granted: boolean; message: string } };
    expect(r.data.granted).toBe(false);
    expect(calls).toHaveLength(0);
  });

  it("refuses a sender that is not this extension", async () => {
    const r = await dispatch({ kind: "press.claim" }, { id: "other", tab: TAB.tab } as chrome.runtime.MessageSender);
    expect(r).toMatchObject({ ok: false, error: { code: "unauthorized" } });
    expect(calls).toHaveLength(0);
  });

  it("makes one request per ask and never retries a refusal", async () => {
    handler = () => fail("unavailable", "busy", 503);
    await ask("press.claim");
    expect(claims()).toHaveLength(1);
  });
});

describe("press report", () => {
  it("records the press with its claim id", async () => {
    handler = (c) => (new URL(c.url).pathname === "/api/submissions" ? ok({ ledgerId: 5, result: "settled", tracker: "created" }) : fail("not_found", "no", 404));
    expect(await ask("press.report", { claimId: "claim-1", entry })).toMatchObject({ ok: true, data: { queued: false } });
    const post = calls.find((c) => c.method === "POST" && new URL(c.url).pathname === "/api/submissions")!;
    expect(post.body).toMatchObject({ entryId: entry.entryId, claimId: "claim-1", confirmed: false, host: "careers.example.com" });
  });

  it("queues the report when the application cannot take it yet", async () => {
    handler = () => fail("unavailable", "busy", 503);
    expect(await ask("press.report", { claimId: "claim-1", entry })).toMatchObject({ ok: true, data: { queued: true } });
    const queue = await store.get("submissionQueue");
    expect(queue).toHaveLength(1);
    expect(queue[0]!.entry).toMatchObject({ claimId: "claim-1" });
  });
});

describe("routes that answer a refusal", () => {
  it("passes the sign-in route's own reason on and returns no credential", async () => {
    handler = () => fail("conflict", "Signing in here is on hold for today", 409, { details: { reason: "signin_held" } });
    expect(await ask("signin.resolve")).toMatchObject({ ok: false, error: { code: "conflict", reason: "signin_held" } });
  });
});
