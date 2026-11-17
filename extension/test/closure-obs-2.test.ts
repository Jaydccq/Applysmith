import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { registerFill } from "../src/sw/fill";
import { dispatch } from "../src/sw/router";
import { registerSubmissions } from "../src/sw/submissions";
import { registerWidgets } from "../src/sw/widgets";
import * as store from "../src/sw/store";
import { fail, installChrome, installFetch, ok, type Call } from "./chrome-mock";

const TOKEN = "T".repeat(43);
const PAGE = "https://careers.example.com/jobs/1/apply";
const TAB = { id: "ext-id", frameId: 0, tab: { id: 9, url: PAGE } as chrome.tabs.Tab, url: PAGE } as chrome.runtime.MessageSender;
const ask = (kind: string, extra: Record<string, unknown> = {}, sender: chrome.runtime.MessageSender = TAB) => dispatch({ kind, ...extra }, sender);

describe("confirming a submission by the page the next document lands on", () => {
  let calls: Call[];
  let committed: ((d: { tabId: number; frameId: number; url: string }) => void) | undefined;
  const entry = (n: number) => ({ entryId: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`, url: "https://careers.example.com/jobs/1/apply", host: "careers.example.com", title: "Apply", at: "2026-10-11T09:00:00.000Z", confirmed: false });
  const posts = () => calls.filter((c) => c.method === "POST" && c.url.endsWith("/api/submissions"));

  beforeEach(async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-10-11T09:00:00Z"));
    installChrome({
      alarms: { create: vi.fn(), onAlarm: { addListener: vi.fn() } },
      webNavigation: { onCommitted: { addListener: vi.fn((f) => (committed = f)) } },
    });
    await store.put("token", TOKEN);
    calls = installFetch((c) => (new URL(c.url).pathname === "/api/submissions" ? ok({ ledgerId: 1, result: "created", tracker: "created" }) : fail("not_found", "no", 404)));
    registerWidgets();
    registerSubmissions();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("a thank-you path reached by full reload within 20 s confirms the pending entry and one after 25 s does not", async () => {
    await ask("submission.record", { entry: entry(1) });
    vi.setSystemTime(new Date("2026-10-11T09:00:12Z"));
    calls.length = 0;
    committed!({ tabId: 9, frameId: 0, url: "https://careers.example.com/jobs/1/thank-you?ref=abc" });
    await vi.waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0]!.body).toMatchObject({ entryId: entry(1).entryId, confirmed: true });

    await ask("submission.record", { entry: entry(2) });
    calls.length = 0;
    vi.setSystemTime(new Date("2026-10-11T09:00:37Z"));
    committed!({ tabId: 9, frameId: 0, url: "https://careers.example.com/jobs/1/thank-you" });
    await new Promise((r) => setTimeout(r, 30));
    expect(posts()).toHaveLength(0);
  });
});

describe("the per-site switch", () => {
  beforeEach(() => {
    installChrome();
  });
  afterEach(() => vi.unstubAllGlobals());

  it("www.Jobs.example.test and jobs.example.test are the same host for the switch", async () => {
    registerWidgets();
    await store.setOff("www.Jobs.example.test", true);
    for (const host of ["jobs.example.test", "WWW.JOBS.EXAMPLE.TEST", "www.jobs.example.test", "Jobs.Example.Test"]) expect(await store.isOff(host), host).toBe(true);
    expect(await store.isOff("careers.example.test")).toBe(false);
    expect(await store.isOff("notjobs.example.test")).toBe(false);
    expect(await ask("flags.get", { host: "JOBS.example.test" })).toMatchObject({ data: { off: true } });
    await store.setOff("jobs.EXAMPLE.test", false);
    expect(await store.isOff("www.jobs.example.test")).toBe(false);

    // A stored value that is missing, hand-edited or junk reads as no site disabled; a good entry among junk still counts.
    for (const junk of [undefined, "jobs.example.test", { "jobs.example.test": true }, 7, null]) {
      await store.put("offHosts", junk as never);
      expect(await store.isOff("jobs.example.test"), JSON.stringify(junk)).toBe(false);
    }
    await store.put("offHosts", [1, null, "WWW.Jobs.example.test"] as never);
    expect(await store.isOff("jobs.example.test")).toBe(true);
  });
});

describe("the legal-declarations setting at the start of a fill", () => {
  const PANEL_PAGE = "https://example.com/careers/apply";
  let seq = 500;
  let calls: Call[];
  let settingsAnswer: () => Response;
  let startArgs: { acceptLegalDeclarations?: boolean }[];

  beforeEach(async () => {
    startArgs = [];
    installChrome({
      scripting: {
        executeScript: vi.fn(async (inj: { target: { frameIds: number[] }; files?: string[]; func?: { name: string }; args?: unknown[] }) => {
          if (inj.files) return [{ result: undefined }];
          switch (inj.func?.name) {
            case "probeFn":
            case "lightProbeFn": return [{ result: { controls: 6, required: 2 } }];
            case "docFn": return [{ result: "doc-1" }];
            case "startFn":
              startArgs.push(inj.args![0] as { acceptLegalDeclarations?: boolean });
              return [{ result: { started: true, runId: (inj.args![0] as { runId: string }).runId } }];
            case "statusFn": return [{ result: { state: "running", runId: "run_0123456789abcdef01234567", stamp: "x" } }];
          }
          return [{ result: undefined }];
        }),
      },
      webNavigation: { getAllFrames: vi.fn(async () => [{ frameId: 0, url: PANEL_PAGE, documentLifecycle: "active" }]), onCommitted: { addListener: vi.fn() } },
      tabs: { onRemoved: { addListener: vi.fn() } },
    });
    await store.put("token", TOKEN);
    calls = installFetch((c) => {
      const path = new URL(c.url).pathname;
      if (path === "/api/autofill/applicant") return ok({ candidate: { fullName: "Alex Example", email: "alex.example@example.com" } });
      if (path === "/api/answer-bank/settings") return settingsAnswer();
      if (path === "/api/autofill/runs") return ok({ runId: "run_0123456789abcdef01234567" }, 202);
      return fail("not_found", "no", 404);
    });
    registerFill();
  });
  afterEach(() => vi.unstubAllGlobals());

  async function start(): Promise<unknown> {
    const tabId = ++seq;
    const sender = { id: "ext-id", frameId: 0, doc: "doc-1", tab: { id: tabId, url: PANEL_PAGE } as chrome.tabs.Tab, url: PANEL_PAGE } as unknown as chrome.runtime.MessageSender;
    return dispatch({ kind: "fill.start", doc: "doc-1", posting: {} }, sender);
  }

  it("a failing settings read passes acceptLegalDeclarations false", async () => {
    settingsAnswer = () => fail("internal", "down", 500);
    expect(await start()).toMatchObject({ ok: true });
    settingsAnswer = () => ok({});
    expect(await start()).toMatchObject({ ok: true });
    settingsAnswer = () => ok({ autoAcceptLegalDeclarations: "yes" });
    expect(await start()).toMatchObject({ ok: true });
    // Only an explicit true turns the setting on.
    settingsAnswer = () => ok({ autoAcceptLegalDeclarations: true });
    expect(await start()).toMatchObject({ ok: true });
    expect(startArgs.map((a) => a.acceptLegalDeclarations)).toEqual([false, false, false, true]);
    expect(calls.filter((c) => c.url.endsWith("/api/autofill/runs"))).toHaveLength(4);
  });
});
