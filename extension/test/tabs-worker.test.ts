import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { dispatch } from "../src/sw/router";
import { registerAnalyze } from "../src/sw/analyze";
import { registerAnswers } from "../src/sw/answers";
import { registerResumes, registerDocs } from "../src/sw/resumes";
import * as store from "../src/sw/store";
import { fail, installChrome, installFetch, ok, type Call } from "./chrome-mock";

const TOKEN = "T".repeat(43);
const RUN = "run_0123456789abcdef01234567";
const PAGE = "https://example.com/careers/backend-engineer?utm_source=news";
const TAB = { id: "ext-id", frameId: 0, tab: { id: 31, url: PAGE } as chrome.tabs.Tab, url: PAGE } as chrome.runtime.MessageSender;

const report = { number: 12, kind: "evaluation", company: "Example Corp", role: "Backend Engineer", postingUrl: PAGE, score: 4.2, scoreKind: "deep", recommendation: "apply", legitimacy: "ok", verification: "", createdAt: "2026-10-01T00:00:00.000Z", documentsEligible: true };
const entry = (over: Record<string, unknown> = {}) => ({ id: 7, status: "evaluated", company: "Example Corp", role: "Backend Engineer", reportNumber: 12, ...over });

let calls: Call[];
let handler: (c: Call) => Response | Promise<Response>;

function sse(...events: [string, unknown][]): Response {
  const text = events.map(([n, d]) => `event: ${n}\ndata: ${JSON.stringify(d)}\n\n`).join("");
  return new Response(text, { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

const ask = (kind: string, extra: Record<string, unknown> = {}) => dispatch({ kind, ...extra }, TAB);
const data = async <T>(kind: string, extra: Record<string, unknown> = {}): Promise<T> => {
  const r = (await ask(kind, extra)) as { ok: boolean; data: T; error?: unknown };
  if (!r.ok) throw new Error(JSON.stringify(r.error));
  return r.data;
};

beforeEach(async () => {
  installChrome();
  await store.put("token", TOKEN);
  calls = installFetch((c) => handler(c));
  handler = () => fail("not_found", "no", 404);
  registerAnalyze();
  registerAnswers();
  registerResumes();
});
afterEach(() => vi.unstubAllGlobals());

describe("Analyze: lookup", () => {
  it("shows an existing evaluation with its score and starts nothing", async () => {
    handler = (c) => {
      const path = new URL(c.url).pathname;
      if (path === "/api/applications/lookup") return ok({ found: true, identityKey: "k1", entry: entry() });
      if (path === "/api/reports/12") return ok({ report, header: {}, content: "" });
      return fail("not_found", "no", 404);
    };
    const v = await data<{ found: boolean; report: { score: number; recommendation: string; number: number } }>("analyze.lookup", { url: PAGE });
    expect(v).toMatchObject({ found: true, report: { score: 4.2, recommendation: "apply", number: 12 } });
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    expect(new URL(calls[0]!.url).searchParams.get("url")).toBe(PAGE);
  });
});

describe("Analyze: an evaluation", () => {
  const capture = { url: PAGE, title: "Job Application for Backend Engineer at Example Corp", role: "Backend Engineer", text: "x".repeat(250_000), company: "Example Corp", structured: false, detection: { label: "job_posting", confidence: 0.8, signals: [] } };

  it("is sent with an idempotency key, the text capped at 200,000 characters, and followed on the stream to a score", async () => {
    handler = (c) => {
      const path = new URL(c.url).pathname;
      if (path === "/api/evaluations") return ok({ runId: RUN }, 202);
      if (path === `/api/runs/${RUN}/events`) return sse(["phase", { runId: RUN, phase: "reading" }], ["phase", { runId: RUN, phase: "writing" }], ["final", { runId: RUN, status: "succeeded", outcome: "reported", reportNumber: 12 }]);
      if (path === "/api/reports/12") return ok({ report, header: {}, content: "" });
      return fail("not_found", "no", 404);
    };
    const { runId } = await data<{ runId: string }>("analyze.start", { capture });
    expect(runId).toBe(RUN);
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.headers["Idempotency-Key"]).toMatch(/^ext_[0-9a-f-]{36}$/);
    const body = post.body as { text: string; url: string; source: string };
    expect(body.text).toHaveLength(200_000);
    expect(body).toMatchObject({ url: PAGE, source: "extension", title: "Backend Engineer", company: "Example Corp" });
    await vi.waitFor(async () => expect(await data("analyze.status", { runId })).toMatchObject({ done: true, outcome: "reported", report: { score: 4.2 } }));
  });

  it("re-attaches to a running evaluation after a worker restart, seeded from the run itself", async () => {
    handler = (c) => {
      const path = new URL(c.url).pathname;
      if (path === `/api/evaluations/${RUN}`) return ok({ runId: RUN, status: "running", phase: "screening", review: false });
      if (path === `/api/runs/${RUN}/events`) return sse(["final", { runId: RUN, status: "succeeded", outcome: "screened", reason: "below_floor" }]);
      return fail("not_found", "no", 404);
    };
    const first = await data<{ phase: string; done: boolean }>("analyze.status", { runId: RUN });
    expect(first).toMatchObject({ phase: "screening", done: false });
    await vi.waitFor(async () => expect(await data("analyze.status", { runId: RUN })).toMatchObject({ done: true, outcome: "screened" }));
  });

  it("adds the posting to the tracker without choosing its status", async () => {
    handler = (c) => (new URL(c.url).pathname === "/api/candidacies" ? ok(entry(), 201) : fail("not_found", "no", 404));
    await data("analyze.track", { capture });
    expect(calls[0]!.body).toMatchObject({ company: "Example Corp", role: "Backend Engineer", url: PAGE, source: "extension" });
    expect(calls[0]!.body).not.toHaveProperty("status");
  });
});

describe("Answer: drafts", () => {
  const ask1 = (extra: Record<string, unknown> = {}) => ask("compose.run", { key: "#why", question: "Why do you want this role?", maxLength: 500, posting: { company: "Example Corp", role: "Backend Engineer", description: "Build things" }, ...extra });

  it("sends the host, the question, the limit and the posting, and returns the draft without any write", async () => {
    handler = () => ok({ outcome: "filled-model", answer: "I enjoy building reliable systems.", sources: ["profile"] });
    const r = (await ask1()) as { ok: true; data: { outcome: string; answer: string } };
    expect(r.data).toEqual({ outcome: "filled-model", answer: "I enjoy building reliable systems.", reason: undefined });
    expect(calls[0]!.body).toMatchObject({ host: "example.com", question: "Why do you want this role?", maxLength: 500, posting: { company: "Example Corp", role: "Backend Engineer", description: "Build things" } });
  });

  it("passes a refusal through as an abstain", async () => {
    handler = () => ok({ outcome: "abstained", reason: "model-abstain", sources: [] });
    expect(((await ask1()) as { data: { outcome: string } }).data.outcome).toBe("abstained");
  });

  it("allows one request in flight per field and cancels it on request", async () => {
    handler = (c) => new Promise<Response>((_res, rej) => c.signal!.addEventListener("abort", () => rej(new Error("aborted"))));
    const first = ask1();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(await ask1()).toMatchObject({ ok: false, error: { code: "conflict" } });
    await ask("compose.cancel", { key: "#why" });
    expect(await first).toMatchObject({ ok: false, error: { code: "cancelled" } });
  });

  it("does not retry a failed write", async () => {
    handler = () => fail("unavailable", "down", 503);
    await ask1();
    expect(calls).toHaveLength(1);
  });
});

describe("Résumé: variants", () => {
  const variants = [
    { id: "backend", label: "Backend", tags: ["backend", "go"], default: false, fileName: "backend.pdf" },
    { id: "general", label: "General", tags: [], default: true, fileName: "general.pdf" },
  ];

  it("lists the variants and the page's pin, and the pin follows the page", async () => {
    handler = (c) => (new URL(c.url).pathname === "/api/resumes" ? ok({ resumes: variants }) : fail("not_found", "no", 404));
    expect(await data("resume.list")).toMatchObject({ resumes: [{ id: "backend" }, { id: "general" }], pinned: undefined });
    await ask("resume.pin", { page: PAGE, id: "general" });
    expect(await data("resume.list")).toMatchObject({ pinned: "general" });
    await ask("resume.pin", { page: PAGE, id: null });
    expect(await data("resume.list")).toMatchObject({ pinned: undefined });
  });
});

describe("Documents worker", () => {
  const TOKEN = "T".repeat(43);
  const POSTING = "https://careers.example.com/jobs/1?utm=x";
  const FORM = "https://apply.example.net/form/1";
  const sender = (tabId: number, url: string) => ({ id: "ext-id", frameId: 0, tab: { id: tabId, url } as chrome.tabs.Tab, url }) as chrome.runtime.MessageSender;
  const ask = (kind: string, extra: Record<string, unknown>, from: chrome.runtime.MessageSender) => dispatch({ kind, ...extra }, from);

  let calls: Call[];
  let hop: ((d: { sourceTabId: number; tabId: number }) => void) | undefined;

  beforeEach(async () => {
    installChrome({
      webNavigation: { onCreatedNavigationTarget: { addListener: vi.fn((f) => (hop = f)) } },
      tabs: { onRemoved: { addListener: vi.fn() } },
    });
    await store.put("token", TOKEN);
    calls = installFetch((c) => {
      const u = new URL(c.url);
      if (u.pathname === "/api/applications/lookup")
        return u.searchParams.get("url") === POSTING ? ok({ found: true, identityKey: "k1", entry: { id: 7, status: "evaluated", reportNumber: 42 } }) : ok({ found: false, identityKey: "k2" });
      if (c.method === "POST" && u.pathname.startsWith("/api/documents/")) return ok({ runId: "run_doc" }, 202);
      return fail("not_found", "no", 404);
    });
    registerDocs();
  });
  afterEach(() => vi.unstubAllGlobals());

  describe("the report a tab works on", () => {
    it("binds the page's own report to its tab, hands it to a tab the posting opens as inherited, and forgets it on dismissal", async () => {
      expect(await ask("docs.get", { url: POSTING }, sender(1, POSTING))).toMatchObject({ data: { show: true, reportNumber: 42, candidacyId: 7, inherited: false } });
      hop!({ sourceTabId: 1, tabId: 2 });
      await vi.waitFor(async () => expect(await store.sessionGet("docs:2")).toBeDefined());
      expect(await ask("docs.get", { url: FORM }, sender(2, FORM))).toMatchObject({ data: { show: true, reportNumber: 42, inherited: true } });
      // The same tab moved on to another page carries the record as inherited too.
      expect(await ask("docs.get", { url: FORM }, sender(1, FORM))).toMatchObject({ data: { show: true, inherited: true } });
      await ask("docs.dismiss", {}, sender(2, FORM));
      expect(await ask("docs.get", { url: FORM }, sender(2, FORM))).toMatchObject({ data: { show: false } });
      // A tab that never saw a report has none.
      expect(await ask("docs.get", { url: FORM }, sender(3, FORM))).toMatchObject({ data: { show: false } });
    });

    it("starts a tailored résumé or a cover letter for the tracker row", async () => {
      expect(await ask("docs.start", { candidacyId: 7, document: "cover-letter" }, sender(1, POSTING))).toMatchObject({ data: { runId: "run_doc" } });
      const post = calls.find((c) => c.method === "POST")!;
      expect(new URL(post.url).pathname).toBe("/api/documents/cover-letter");
      expect(post.body).toEqual({ candidacyId: 7 });
      expect(post.headers["Idempotency-Key"]).toMatch(/^ext_/);
    });

    // The server names the run already writing a document for the job (details.runId).
    it("hands back the run already writing a document for the job, and still fails any other refusal", async () => {
      let reason = "document-run-active";
      installFetch(() => fail("conflict", "a document for this application is still being written", 409, { details: { reason, runId: "run_dash" } }));
      expect(await ask("docs.start", { candidacyId: 7, document: "resume" }, sender(1, POSTING))).toMatchObject({ data: { runId: "run_dash", already: true } });
      reason = "proposal-pending";
      expect(await ask("docs.start", { candidacyId: 7, document: "resume" }, sender(1, POSTING))).toMatchObject({ ok: false, error: { code: "conflict" } });
    });
  });
});
