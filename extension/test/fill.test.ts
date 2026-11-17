import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { unreachableFrameEvents } from "../../engine/src/frames";
import type { FrameDecision, Row, Summary } from "../../engine/src/protocol";
import { dispatch } from "../src/sw/router";
import { registerFill, toMatchRequest, toResolutions, formOnlyBoardPage, registerJobContext } from "../src/sw/fill";
import { registerResumes } from "../src/sw/resumes";
import * as store from "../src/sw/store";
import { fail, installChrome, installFetch, ok, type Call } from "./chrome-mock";
import type { JobContext, PageReading } from "../../engine/src/capture";
import { readPosting } from "../src/shared/capture";

const TOKEN = "T".repeat(43);
const RUN = "run_0123456789abcdef01234567";
const PAGE = "https://example.com/careers/apply?ref=secret";
// Each test gets its own tab, because the worker keeps one fill per tab.
let tabSeq = 100;
const PANEL = { id: "ext-id", frameId: 0, doc: "doc-7", tab: { id: 7, url: PAGE } as chrome.tabs.Tab, url: PAGE };

interface Frame {
  frameId: number;
  url: string;
  probe?: { controls: number; required: number };
  summary?: Summary;
  state?: string;
}

const row = (over: Partial<Row>): Row => ({
  selector: "#a", label: "First name", section: [], lane: "profile", kind: "text", required: true, outcome: "filled", review: false, value: "Alex", ...over,
});

function summary(rows: Row[], extra: Partial<Summary> = {}): Summary {
  return {
    host: "example.com", url: "https://example.com/careers/apply", rows, drafts: [],
    counts: { filled: rows.filter((r) => r.outcome === "filled").length, prefilled: 0, review: 0, abstained: 0, failed: 0, skipped: 0, requiredUnfilled: 0 },
    ...extra,
  };
}

let frames: Frame[];
/** The token of the document the tab's top frame holds now: what the panel stamped it with. */
let topDoc: string;
/** The frames an engine was really started in, that is, whose start passed the page's own check. */
let started: number[];
/** What the browser does on a commit: the tab holds a new document and the worker hears of it. */
function navigate(tabId = PANEL.tab.id!): string {
  topDoc = `${topDoc}-next`;
  const committed = (chrome.webNavigation.onCommitted.addListener as unknown as { mock: { calls: [(d: { frameId: number; tabId: number }) => void][] } }).mock.calls.at(-1)![0];
  committed({ frameId: 0, tabId });
  return topDoc;
}
let injected: { frameId: number; files?: string[] }[];
let touched: number[];
let calls: Call[];
const ends = () => calls.filter((c) => c.url.endsWith(`/api/autofill/runs/${RUN}/end`));
let uploads: Call[];
let eventsFail: boolean;
let endFails: boolean;
type Exec = (inj: any) => Promise<unknown>
let baseExec: Exec;

/** Lets one test change how the browser answers script injections, keeping the rest. */
function wrapExec(wrap: (inj: any, orig: Exec) => Promise<unknown>): void {
  (globalThis as unknown as { chrome: { scripting: { executeScript: Exec } } }).chrome.scripting.executeScript = vi.fn((inj: any) => wrap(inj, baseExec));
}

let resumeMode: "two" | "none" | "down" | "renamed";
const PDF = "%PDF-1.4 synthetic";

function resumeRoute(_c: Call, path: string): Response | undefined {
  if (!path.startsWith("/api/resumes")) return undefined;
  if (resumeMode === "down") return fail("unavailable", "down", 503);
  if (path === "/api/resumes") {
    const general = resumeMode === "renamed" ? "generalist" : "general";
    return ok({ resumes: resumeMode === "none" ? [] : [{ id: "backend", label: "Backend", tags: ["backend"], default: false, fileName: "backend.pdf" }, { id: general, label: "General", tags: [], default: true, fileName: "general.pdf" }] });
  }
  if (path === "/api/resumes/rank") return ok({ chosen: "backend", ranking: [{ id: "backend", score: 1, reason: "backend" }, { id: "general", score: 0, reason: "default" }] });
  const file = /^\/api\/resumes\/([a-z]+)\/file$/.exec(path);
  if (file) return new Response(`${PDF} ${file[1]}`, { status: 200, headers: { "Content-Type": "application/pdf" } });
  return fail("not_found", "no", 404);
}

let wrapRoute: ((c: Call) => Response | undefined) | undefined;

function route(c: Call): Response {
  const path = new URL(c.url).pathname;
  const early = wrapRoute?.(c);
  if (early) return early;
  const resume = resumeRoute(c, path);
  if (resume) return resume;
  if (path === "/api/autofill/applicant") return ok({ candidate: { fullName: "Alex Example", email: "alex.example@example.com" } });
  if (path === "/api/answer-bank/settings") return ok({ autoAcceptLegalDeclarations: false });
  if (path === "/api/autofill/runs") return ok({ runId: RUN }, 202);
  if (path === `/api/autofill/runs/${RUN}/end`) return endFails ? fail("internal", "down", 500) : ok({ runId: RUN, status: (c.body as { status: string }).status });
  if (path === "/api/answer-bank/system-writes") return ok({ recorded: true });
  if (path === "/api/answer-bank/events") {
    uploads.push(c);
    return eventsFail ? fail("internal", "down", 500) : ok({ accepted: 1, duplicates: [], rejected: [] });
  }
  if (path === "/api/answer-bank/match") {
    const q = (c.body as { questions: { selector: string }[] }).questions;
    return ok({ results: q.map((x) => ({ selector: x.selector, outcome: "filled", answer: "Yes", chosenLabel: "Yes", entryId: "work-auth", confidence: 0.9, terminal: false, trace: [], clientMayOverwrite: false })) });
  }
  return fail("not_found", "no", 404);
}

beforeEach(async () => {
  PANEL.tab = { id: ++tabSeq, url: PAGE } as chrome.tabs.Tab;
  topDoc = PANEL.doc = `doc-${tabSeq}`;
  started = [];
  frames = [{ frameId: 0, url: PAGE, probe: { controls: 6, required: 2 } }];
  injected = []; wrapRoute = undefined; resumeMode = "two"; touched = []; uploads = []; eventsFail = false; endFails = false;
  const env = installChrome({
    scripting: {
      executeScript: vi.fn(async (inj: { target: { frameIds: number[] }; files?: string[]; func?: { name: string }; args?: unknown[] }) => {
        const id = inj.target.frameIds[0]!;
        touched.push(id);
        const f = frames.find((x) => x.frameId === id)!;
        if (inj.files) {
          injected.push({ frameId: id, files: inj.files });
          return [{ result: undefined }];
        }
        switch (inj.func?.name) {
          case "probeFn":
          case "lightProbeFn": return [{ result: f.probe ?? null }];
          case "docFn": return [{ result: id === 0 ? topDoc : null }];
          case "startFn":
            // The page's own check: a top-frame start names the token it was given.
            if (inj.args![1] !== undefined && inj.args![1] !== topDoc) return [{ result: { started: false, reason: "page-changed" } }];
            started.push(id);
            return [{ result: { started: true, runId: (inj.args![0] as { runId: string }).runId } }];
          case "statusFn": return [{ result: f.state === "done" ? { state: "done", runId: RUN, stamp: "x", summary: f.summary } : { state: "running", runId: RUN, stamp: "x" } }];
          case "controlFn":
            if (inj.args![0] === "stop") f.state = "done";
            return [{ result: true }];
          case "logFn": return [{ result: [] }];
          case "frameReportFn": return [{ result: unreachableFrameEvents(inj.args![0] as FrameDecision[], { host: "example.com", url: PAGE }, () => crypto.randomUUID(), Date.now()) }];
        }
        return [{ result: undefined }];
      }),
    },
    webNavigation: { getAllFrames: vi.fn(async () => frames.map((f) => ({ frameId: f.frameId, url: f.url, documentLifecycle: "active" }))), onCommitted: { addListener: vi.fn() } },
    tabs: { onRemoved: { addListener: vi.fn() } },
  });
  baseExec = (env.chrome as unknown as { scripting: { executeScript: Exec } }).scripting.executeScript;
  await store.put("token", TOKEN);
  calls = installFetch((c) => route(c));
  registerFill();
  registerResumes();
});
afterEach(() => vi.unstubAllGlobals());

const ask = (kind: string, extra: Record<string, unknown> = {}, sender = PANEL) => dispatch({ kind, ...(kind === "fill.start" ? { doc: PANEL.doc } : {}), ...extra }, sender as chrome.runtime.MessageSender);

async function finish(rows: Row[], extra: Partial<Summary> = {}) {
  for (const f of frames) {
    f.state = "done";
    f.summary = summary(f.frameId === 0 ? rows : [], extra);
  }
  return (await ask("fill.status")) as { ok: true; data: Record<string, unknown> };
}

describe("matcher mapping", () => {
  it("sends options as value and label pairs and the observations as flags", () => {
    const body = toMatchRequest({
      runId: RUN, host: "example.com", posting: { company: "Example Corp", jdText: "Build things" },
      questions: [{ selector: "#q", text: "Work authorization", kind: "select", inputType: "single_select", options: ["Yes", "No"], required: true }],
      resumeAttached: true,
    });
    expect(body.runId).toBe(RUN);
    expect(body.posting.description).toBe("Build things");
    expect(body.questions[0]!.options).toEqual([{ value: "Yes", label: "Yes" }, { value: "No", label: "No" }]);
    expect(body.resumeAttached).toBe(true);
    expect(body.coverLetterAttached).toBeUndefined();
  });

  it("maps answers back into resolutions and keeps the abstain hint", () => {
    const out = toResolutions({
      results: [{ selector: "#q", outcome: "abstained", answer: "", reason: "no-entry", terminal: false, trace: [], confidence: 0, clientMayOverwrite: true, ownAnswer: false }],
    });
    expect(out[0]).toMatchObject({ selector: "#q", outcome: "abstained", reason: "no-entry", clientMayOverwrite: true });
  });

  it("carries the mark of the user's own bank answer, which the consent rules apply as written", () => {
    const out = toResolutions({
      results: [
        { selector: "#own", outcome: "filled", answer: "Yes", trace: [], confidence: 1, terminal: false, clientMayOverwrite: false, ownAnswer: true },
        { selector: "#rule", outcome: "filled", answer: "Yes", trace: [], confidence: 1, terminal: false, clientMayOverwrite: false, ownAnswer: false },
      ],
    });
    expect(out[0].ownAnswer).toBe(true);
    expect(out[1].ownAnswer).toBe(false);
  });
});

describe("starting a fill", () => {
  it("only starts from the panel of the page the user is on, and never from a sub-frame", async () => {
    const r = await ask("fill.start", { posting: {} }, { ...PANEL, frameId: 3 });
    expect(r).toMatchObject({ ok: false, error: { code: "forbidden" } });
    expect(calls).toHaveLength(0);
    expect(touched).toHaveLength(0);
  });

  it("reads the applicant and the legal setting, gets an issued run, and starts the engine with them", async () => {
    const r = (await ask("fill.start", { posting: { title: "Engineer" } })) as { ok: true; data: { phase: string; runId: string } };
    expect(r.data).toMatchObject({ phase: "running", runId: RUN });
    const paths = calls.map((c) => new URL(c.url).pathname).sort();
    expect(paths).toEqual(["/api/answer-bank/settings", "/api/autofill/applicant", "/api/autofill/runs"]);
    const issue = calls.find((c) => c.url.endsWith("/api/autofill/runs"))!;
    expect(issue.body).toEqual({ host: "example.com", url: "https://example.com/careers/apply" });
    expect(injected.map((i) => i.files)).toEqual([["engine.js", "bridge.js"]]);
  });

  it("asks for the run after the reads, with a fresh Idempotency-Key, and sends no other header of its own", async () => {
    await ask("fill.start", { posting: {} });
    const paths = calls.map((c) => new URL(c.url).pathname);
    expect(paths.indexOf("/api/autofill/runs")).toBe(2);
    const issue = calls.find((c) => c.url.endsWith("/api/autofill/runs"))!;
    expect(issue.method).toBe("POST");
    expect(issue.headers["Idempotency-Key"]).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
    expect(calls.filter((c) => c !== issue).some((c) => "Idempotency-Key" in c.headers)).toBe(false);
  });

  it("issues no run when a read fails", async () => {
    calls.length = 0;
    vi.unstubAllGlobals();
    calls = installFetch((c) => (c.url.endsWith("/api/autofill/applicant") ? fail("internal", "down", 500) : route(c)));
    const r = await ask("fill.start", { posting: {} });
    expect(r).toMatchObject({ ok: false });
    expect(calls.some((c) => c.url.endsWith("/api/autofill/runs"))).toBe(false);
  });

  it("is unauthenticated when the extension is not paired", async () => {
    await store.put("token", undefined);
    const r = await ask("fill.start", { posting: {} });
    expect(r).toMatchObject({ ok: false, error: { code: "unauthenticated" } });
  });

  it("reduces the posting URL to origin and path before the engine sees it", async () => {
    const args: unknown[] = [];
    wrapExec(async (inj, orig) => {
      if (inj.func?.name === "startFn") args.push(inj.args[0]);
      return orig(inj);
    });
    await ask("fill.start", { reading: { url: "https://example.com/apply?token=abc#frag", jdText: "", posting: false, form: true } });
    expect(JSON.stringify(args)).not.toContain("token=abc");
    expect((args[0] as { posting: { url: string } }).posting.url).toBe("https://example.com/apply");
    expect(args[0]).toMatchObject({ watchCorrections: false });
  });
});

describe("frame decisions", () => {
  it("admits a registry iframe without a probe and counts an unknown one before it gets the engine", async () => {
    frames.push({ frameId: 1, url: "https://job-boards.greenhouse.io/embed/job_app?for=example", probe: undefined });
    frames.push({ frameId: 2, url: "https://forms.example.org/f/1", probe: { controls: 5, required: 1 } });
    const r = (await ask("fill.start", { posting: {} })) as { data: { frames: { frameId: string; outcome: string; url: string }[] } };
    const by = Object.fromEntries(r.data.frames.map((f) => [f.frameId, f]));
    expect(by["1"]!.outcome).toBe("admitted-registry");
    expect(by["1"]!.url).toBe("https://job-boards.greenhouse.io/embed/job_app");
    expect(by["2"]!.outcome).toBe("admitted-probe");
    expect(injected.filter((i) => i.frameId === 1).map((i) => i.files)).toEqual([["engine.js", "bridge.js"]]);
    expect(injected.filter((i) => i.frameId === 2).map((i) => i.files)).toEqual([["probe.js"], ["engine.js", "bridge.js"]]);
  });

  it("injects the engine only into a frame that qualifies, and never touches a deny-listed frame", async () => {
    frames.push({ frameId: 1, url: "https://www.google.com/recaptcha/api2/anchor" });
    frames.push({ frameId: 2, url: "https://widgets.example.org/chat", probe: { controls: 0, required: 0 } });
    frames.push({ frameId: 3, url: "https://forms.example.org/one-field", probe: { controls: 1, required: 1 } });
    const r = (await ask("fill.start", { posting: {} })) as { data: { frames: { frameId: string; outcome: string }[] } };
    const by = Object.fromEntries(r.data.frames.map((f) => [f.frameId, f.outcome]));
    expect(by["1"]).toBe("skipped-denylist");
    expect(by["2"]).toBe("skipped-zero-controls");
    expect(by["3"]).toBe("admitted-probe");
    expect(touched).not.toContain(1);
    expect(injected.filter((i) => i.frameId === 2).flatMap((i) => i.files!)).toEqual(["probe.js"]);
    expect(injected.some((i) => i.frameId === 3 && i.files!.includes("engine.js"))).toBe(true);
  });

  it("records a frame whose probe the browser refused as failed-probe and leaves it without the engine", async () => {
    frames.push({ frameId: 5, url: "https://forms.example.org/f/9", probe: { controls: 4, required: 0 } });
    wrapExec(async (inj, orig) => {
      if (inj.target.frameIds[0] === 5 && inj.files?.includes("probe.js")) throw new Error("no access");
      return orig(inj);
    });
    const r = (await ask("fill.start", { posting: {} })) as { data: { frames: { frameId: string; outcome: string }[] } };
    expect(r.data.frames.find((f) => f.frameId === "5")!.outcome).toBe("failed-probe");
    expect(injected.some((i) => i.frameId === 5)).toBe(false);
  });

  it("reports a frame it could not inject into", async () => {
    frames.push({ frameId: 4, url: "https://job-boards.greenhouse.io/embed/job_app" });
    wrapExec(async (inj, orig) => {
      if (inj.target.frameIds[0] === 4) throw new Error("no access");
      return orig(inj);
    });
    const r = (await ask("fill.start", { posting: {} })) as { data: { unreachable?: number; frames: { outcome: string }[] } };
    expect(r.data.frames.map((f) => f.outcome)).toContain("failed-inject");
    const done = await finish([row({})]);
    expect(done.data.unreachable).toBe(1);
  });

  it("uploads one iframe event for each frame it could not enter, and none for a frame skipped on purpose", async () => {
    vi.useFakeTimers();
    try {
      frames.push({ frameId: 4, url: "https://job-boards.greenhouse.io/embed/job_app?for=example" });
      frames.push({ frameId: 5, url: "https://www.google.com/recaptcha/api2/anchor" });
      wrapExec(async (inj, orig) => {
        if (inj.target.frameIds[0] === 4) throw new Error("no access");
        return orig(inj);
      });
      await ask("fill.start", { posting: {} });
      await finish([row({})]);
      await vi.advanceTimersByTimeAsync(2500);
      const events = uploads.flatMap((u) => (u.body as { events: Record<string, unknown>[] }).events);
      expect(events).toHaveLength(1);
      expect(events[0]).toMatchObject({
        kind: "frame-unreachable", controlKind: "iframe", reason: "failed-inject", runId: RUN, host: "example.com",
        question: "Application form frame could not be reached: https://job-boards.greenhouse.io/embed/job_app",
      });
      expect(JSON.stringify(events)).not.toContain("for=example");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("the match binding", () => {
  it("serves the engine's request through the matcher with the issued run", async () => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    const payload = JSON.stringify({ runId: RUN, host: "example.com", posting: {}, questions: [{ selector: "#w", text: "Authorized to work?", kind: "select", inputType: "yes_no", options: ["Yes", "No"], required: true }] });
    const r = (await ask("engine.match", { payload })) as { ok: true; data: string };
    const reply = JSON.parse(r.data) as { ok: true; result: { results: { selector: string; value: string; optionLabel: string }[] } };
    expect(reply.result.results[0]).toMatchObject({ selector: "#w", value: "Yes", optionLabel: "Yes" });
    expect((calls[0]!.body as { runId: string }).runId).toBe(RUN);
  });

  it("refuses a run the worker did not start", async () => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    const payload = JSON.stringify({ runId: "run_ffffffffffffffffffffffff", host: "example.com", posting: {}, questions: [] });
    const r = (await ask("engine.match", { payload })) as { data: string };
    expect(JSON.parse(r.data)).toMatchObject({ ok: false });
    expect(calls).toHaveLength(0);
  });
});

describe("system writes", () => {
  it("reports the profile pass's values with the run and selector, and nothing else", async () => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    await finish([
      row({ selector: "#first", lane: "profile", value: "Alex" }),
      row({ selector: "#city", lane: "profile", outcome: "prefilled", value: "Springfield" }),
      row({ selector: "#auth", lane: "bank", value: "Yes" }),
      row({ selector: "#pw", lane: "profile", value: undefined }),
      row({ selector: "#legal", lane: "attestation", kind: "checkbox", value: "checked" }),
    ]);
    const writes = calls.filter((c) => c.url.endsWith("/api/answer-bank/system-writes")).map((c) => c.body);
    expect(writes).toEqual([
      { runId: RUN, selector: "#first", value: "Alex" },
      { runId: RUN, selector: "#legal", value: "checked" },
    ]);
  });

  it("reports once even when the status is read twice", async () => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    await finish([row({})]);
    await ask("fill.status");
    expect(calls.filter((c) => c.url.endsWith("/api/answer-bank/system-writes"))).toHaveLength(1);
  });
});

describe("what the person commits", () => {
  const edit = (selector: string, value: string, over: Record<string, unknown> = {}) => ({ selector, label: "Preferred name", kind: "text", value, ...over });

  it("is ignored before the fill has ended", async () => {
    vi.useFakeTimers();
    try {
      await ask("fill.start", { posting: {} });
      await ask("engine.edit", { edit: edit("#n", "Lex") });
      await vi.advanceTimersByTimeAsync(2500);
      expect(uploads).toHaveLength(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("becomes a trusted manual-fill event carrying the run, the selector, the system's value, the section and the options", async () => {
    vi.useFakeTimers();
    try {
      await ask("fill.start", { posting: {} });
      await finish([row({ selector: "#n", label: "Preferred name", value: "Alex" })]);
      await ask("engine.edit", { edit: edit("#n", "Lex", { section: ["Personal information"] }) });
      await ask("engine.edit", { edit: edit("#o", "Berlin", { label: "Preferred office", kind: "select", options: ["Lisbon", "Berlin"] }) });
      await vi.advanceTimersByTimeAsync(2500);
      expect(uploads).toHaveLength(1);
      const [ev, pick] = (uploads[0]!.body as { events: Record<string, unknown>[] }).events;
      expect(ev).toMatchObject({ kind: "manual-fill", runId: RUN, selector: "#n", trustedInput: true, before: "Alex", after: "Lex", host: "example.com", controlKind: "text",
        section: ["Personal information"] });
      expect(pick).toMatchObject({ selector: "#o", question: "Preferred office", controlKind: "select", after: "Berlin", optionLabel: "Berlin", options: ["Lisbon", "Berlin"] });
      expect(ev!.eventId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("names the bank entry that answered a control, and none for a control the matcher abstained on that the profile pass then wrote", async () => {
    vi.useFakeTimers();
    try {
      const result = (selector: string, outcome: string, entryId: string) => ({ selector, outcome, answer: outcome === "abstained" ? "" : "Yes", entryId, confidence: 0.6,
        terminal: false, trace: [], clientMayOverwrite: outcome === "abstained", ...(outcome === "abstained" ? { reason: "low-confidence" } : {}) });
      wrapRoute = (c) =>
        c.url.endsWith("/api/answer-bank/match") ? ok({ results: [result("#auth", "filled", "work-auth"), result("#city", "abstained", "address-city")] }) : undefined;
      await ask("fill.start", { posting: {} });
      const question = (selector: string, text: string) => ({ selector, text, kind: "text", inputType: "text", options: [], required: true });
      await ask("engine.match", { payload: JSON.stringify({ runId: RUN, host: "example.com", posting: {}, questions: [question("#auth", "Authorized to work?"), question("#city", "City")] }) });
      await finish([row({ selector: "#auth", label: "Authorized to work?", lane: "bank", value: "Yes" }), row({ selector: "#city", label: "City", lane: "profile", value: "Springfield" })]);
      await ask("engine.edit", { edit: edit("#auth", "No", { label: "Authorized to work?" }) });
      await ask("engine.edit", { edit: edit("#city", "Shelbyville", { label: "City" }) });
      await vi.advanceTimersByTimeAsync(2500);
      const events = uploads.flatMap((u) => (u.body as { events: Record<string, unknown>[] }).events);
      expect(events.map((e) => [e.selector, e.before, e.entryId])).toEqual([["#auth", "Yes", "work-auth"], ["#city", "Springfield", undefined]]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("sends a value equal to the system's, a file and an unlabeled control nowhere", async () => {
    vi.useFakeTimers();
    try {
      await ask("fill.start", { posting: {} });
      await finish([row({ selector: "#n", value: "Alex" })]);
      await ask("engine.edit", { edit: edit("#n", "Alex") });
      await ask("engine.edit", { edit: edit("#f", "cv.pdf", { kind: "file" }) });
      await ask("engine.edit", { edit: edit("#u", "x", { label: " " }) });
      await vi.advanceTimersByTimeAsync(2500);
      expect(uploads).toHaveLength(0);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("the end of a fill", () => {
  it("sums the frames and lists nothing with a value", async () => {
    frames.push({ frameId: 1, url: "https://job-boards.greenhouse.io/embed/job_app" });
    await ask("fill.start", { posting: {} });
    const done = await finish([row({ value: "Alex Example" }), row({ selector: "#x", label: "Why us", outcome: "abstained", value: undefined, reason: "terminal-abstain" })]);
    const text = JSON.stringify(done.data);
    expect(text).not.toContain("Alex Example");
    expect(done.data).toMatchObject({ phase: "done", counts: { filled: 1 } });
  });

  it("an engine error in every frame is an error", async () => {
    await ask("fill.start", { posting: {} });
    frames[0]!.state = "done";
    wrapExec(async (inj, orig) => (inj.func?.name === "statusFn" ? [{ result: { state: "error", runId: RUN, stamp: "x", message: "boom" } }] : orig(inj)));
    const r = (await ask("fill.status")) as { data: { phase: string } };
    expect(r.data.phase).toBe("error");
  });

  it("stop ends the run and the result is final", async () => {
    await ask("fill.start", { posting: {} });
    frames[0]!.summary = summary([row({})], { partial: true });
    const r = (await ask("fill.stop")) as { data: { phase: string } };
    expect(r.data.phase).toBe("done");
  });
});



/** A restarted worker: every module is loaded again, the browser's storage and the engines in the pages stay. */
async function restart() {
  vi.resetModules();
  const [router, fill] = await Promise.all([import("../src/sw/router"), import("../src/sw/fill")]);
  fill.registerFill();
  return (kind: string, extra: Record<string, unknown> = {}) => router.dispatch({ kind, ...extra }, PANEL as chrome.runtime.MessageSender);
}

describe("a fill survives a worker restart", () => {
  const stored = () => (globalThis as unknown as { chrome: { storage: { session: { get(k: string): Promise<Record<string, unknown>> } } } }).chrome.storage.session.get(`fill:${PANEL.tab.id}`);

  it("cancels a journaled run when reload cleared session state, and retries a failed ending", async () => {
    await ask("fill.start", { posting: {} });
    const areas = chrome.storage;
    expect(await areas.local.get(`pending-fill:${RUN}`)).toEqual({ [`pending-fill:${RUN}`]: PANEL.tab.id });
    await areas.session.remove(`fill:${PANEL.tab.id}`);
    endFails = true;
    await restart();
    await vi.waitFor(() => expect(ends()).toHaveLength(1));
    expect(await areas.local.get(`pending-fill:${RUN}`)).toEqual({ [`pending-fill:${RUN}`]: PANEL.tab.id });
    endFails = false;
    await restart();
    await vi.waitFor(async () => expect(await areas.local.get(`pending-fill:${RUN}`)).toEqual({}));
    expect(ends().at(-1)!.body).toEqual({ status: "cancelled" });
  });

  it("keeps the panel's view of a running fill, and the engines' own status finishes it", async () => {
    await ask("fill.start", { reading: { url: "https://example.com/careers/apply", title: "Engineer", company: "Example Corp", jdText: "Build reliable things for Example Corp", jdSource: "structured", posting: true, form: true } });
    const before = (await ask("fill.status")) as { data: { runId: string; phase: string } };
    expect(before.data.phase).toBe("running");

    const askAgain = await restart();
    const after = (await askAgain("fill.status")) as { ok: true; data: { runId: string; phase: string; frames: { url: string }[] } };
    expect(after.data).toMatchObject({ runId: RUN, phase: "running" });
    expect(after.data.frames[0]!.url).toBe("https://example.com/careers/apply");

    for (const f of frames) {
      f.state = "done";
      f.summary = summary(f.frameId === 0 ? [row({ value: "Alex" })] : []);
    }
    calls.length = 0;
    const done = (await askAgain("fill.status")) as { data: { phase: string; counts: { filled: number } } };
    expect(done.data).toMatchObject({ phase: "done", counts: { filled: 1 } });
    expect(calls.filter((c) => c.url.endsWith("/api/answer-bank/system-writes"))).toHaveLength(1);
  });

  it("keeps serving the engine's bindings for the run after the restart", async () => {
    await ask("fill.start", { posting: {} });
    const askAgain = await restart();
    const payload = JSON.stringify({ runId: RUN, host: "example.com", posting: {}, questions: [{ selector: "#w", text: "Authorized to work?", kind: "select", inputType: "yes_no", options: ["Yes", "No"], required: true }] });
    const r = (await askAgain("engine.match", { payload })) as { data: string };
    expect(JSON.parse(r.data)).toMatchObject({ ok: true });
    const other = JSON.stringify({ runId: "run_ffffffffffffffffffffffff", host: "example.com", posting: {}, questions: [] });
    expect(JSON.parse(((await askAgain("engine.match", { payload: other })) as { data: string }).data)).toMatchObject({ ok: false });
  });

  it("stores no applicant data, job text or written value, only after the area was locked to trusted contexts", async () => {
    vi.resetModules();
    const [router, fill] = await Promise.all([import("../src/sw/router"), import("../src/sw/fill")]);
    fill.registerFill();
    const env = (globalThis as unknown as { chrome: { storage: { session: { set: { mock: { invocationCallOrder: number[] } }; setAccessLevel: { mock: { invocationCallOrder: number[] } } } } } }).chrome.storage.session;
    await router.dispatch({ kind: "fill.start", doc: PANEL.doc, reading: { url: "https://example.com/careers/apply", title: "Engineer", company: "Example Corp", jdText: "Build reliable things for Example Corp", jdSource: "structured", posting: true, form: true } }, PANEL as chrome.runtime.MessageSender);
    await router.dispatch({ kind: "fill.status" }, PANEL as chrome.runtime.MessageSender);
    const text = JSON.stringify(await stored());
    expect(text).toContain(RUN);
    for (const secret of ["Alex Example", "alex.example@example.com", "Build reliable things"]) expect(text).not.toContain(secret);
    expect(env.setAccessLevel.mock.invocationCallOrder[0]!).toBeLessThan(env.set.mock.invocationCallOrder[0]!);
  });

  it("ends a running fill when the page navigates, keeps the reason for the new page's panel, and drops it on dismiss", async () => {
    await ask("fill.start", { posting: {} });
    expect(Object.keys(await stored())).toHaveLength(1);
    const committed = (chrome.webNavigation.onCommitted.addListener as unknown as { mock: { calls: [(d: { tabId: number; frameId: number }) => void][] } }).mock.calls.at(-1)![0];
    committed({ tabId: PANEL.tab.id!, frameId: 3 });
    await new Promise((r) => setTimeout(r, 20));
    expect(((await stored())[`fill:${PANEL.tab.id}`] as { phase: string }).phase).toBe("running");
    navigate();
    PANEL.doc = topDoc;
    await vi.waitFor(async () => expect(((await stored())[`fill:${PANEL.tab.id}`] as { phase: string }).phase).toBe("stopped"));
    const askAgain = await restart();
    expect(((await askAgain("fill.status")) as { data: { phase: string; message: string } }).data).toMatchObject({ phase: "stopped", message: "The page changed, so the fill ended." });
    expect(await askAgain("fill.dismiss")).toMatchObject({ ok: true });
    expect(Object.keys(await stored())).toHaveLength(0);
  });

  it("ends a fill whose page was replaced while the worker was stopped, once, when it reads the record back", async () => {
    await ask("fill.start", { posting: {} });
    topDoc = `${topDoc}-reloaded`;
    PANEL.doc = topDoc;
    calls.length = 0;
    const askAgain = await restart();
    const first = (await askAgain("fill.status")) as { data: { phase: string; message: string } };
    const second = (await askAgain("fill.status")) as { data: { phase: string } };
    expect(first.data).toMatchObject({ phase: "stopped", message: "The page changed, so the fill ended." });
    expect(second.data.phase).toBe("stopped");
    expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]);
  });
});

describe("the résumé binding", () => {
  const payload = JSON.stringify({ runId: RUN, document: "resume" });
  const resumeReply = async () => JSON.parse(((await ask("engine.resume", { payload })) as { data: string }).data) as { ok: boolean; result: { fileName: string; mediaType: string; contentBase64: string } | null };
  const decode = (b64: string) => atob(b64);

  it("hands over the variant the API ranks first, fetched when the engine asks", async () => {
    await ask("fill.start", { posting: { title: "Backend Engineer", company: "Example Corp" } });
    calls.length = 0;
    const r = await resumeReply();
    expect(r.ok).toBe(true);
    expect(r.result).toMatchObject({ fileName: "backend.pdf", mediaType: "application/pdf" });
    expect(decode(r.result!.contentBase64)).toBe(`${PDF} backend`);
    expect(calls.filter((c) => c.url.includes("/api/resumes/backend/file?for="))).toHaveLength(1);
  });

  it("hands over the pinned variant instead, until the pin is removed", async () => {
    await ask("fill.start", { posting: { title: "Backend Engineer" } });
    await ask("resume.pin", { page: "https://example.com/careers/apply", id: "general" });
    expect(decode((await resumeReply()).result!.contentBase64)).toBe(`${PDF} general`);
    await ask("resume.pin", { page: "https://example.com/careers/apply", id: null });
    expect(decode((await resumeReply()).result!.contentBase64)).toBe(`${PDF} backend`);
  });

  it("resolves a pin by id, then by its file once the id was renamed, and a stale pin falls back to the ranking", async () => {
    await ask("fill.start", { posting: {} });
    await ask("resume.pin", { page: "https://example.com/careers/apply", id: "general" });
    resumeMode = "renamed";
    expect(decode((await resumeReply()).result!.contentBase64)).toBe(`${PDF} generalist`);
    expect(await ask("resume.list")).toMatchObject({ data: { pinned: "generalist" } });
    await ask("resume.pin", { page: "https://example.com/careers/apply", id: "gone" });
    expect(decode((await resumeReply()).result!.contentBase64)).toBe(`${PDF} backend`);
  });

  it("keeps no document bytes in extension storage", async () => {
    await ask("fill.start", { posting: {} });
    await resumeReply();
    const g = (globalThis as unknown as { chrome: { storage: { local: { data: object }; session: { data: object } } } }).chrome.storage;
    const stored = JSON.stringify([g.local.data, g.session.data]);
    expect(stored).not.toContain(btoa(`${PDF} backend`));
    expect(stored).not.toContain("%PDF");
  });

  it("answers nothing when there is no variant or the cover letter is asked for, and refuses when the roster cannot be read", async () => {
    await ask("fill.start", { posting: {} });
    resumeMode = "none";
    expect((await resumeReply()).result).toBeNull();
    resumeMode = "down";
    expect(await resumeReply()).toMatchObject({ ok: false });
    resumeMode = "two";
    const cover = JSON.parse(((await ask("engine.resume", { payload: JSON.stringify({ runId: RUN, document: "cover-letter" }) })) as { data: string }).data) as { result: unknown };
    expect(cover.result).toBeNull();
  });

  it("uses the default variant when the ranking route fails", async () => {
    await ask("fill.start", { posting: {} });
    wrapRoute = (c) => (c.url.endsWith("/api/resumes/rank") ? fail("unavailable", "down", 503) : undefined);
    expect(decode((await resumeReply()).result!.contentBase64)).toBe(`${PDF} general`);
  });

  it("refuses a run the worker did not start", async () => {
    await ask("fill.start", { posting: {} });
    const other = JSON.stringify({ runId: "run_ffffffffffffffffffffffff", document: "resume" });
    expect(JSON.parse(((await ask("engine.resume", { payload: other })) as { data: string }).data)).toMatchObject({ ok: false });
  });
});
describe("ending the issued run", () => {

  it("reports succeeded once, after the system writes, when every frame has ended", async () => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    await finish([row({})]);
    await ask("fill.status");
    expect(ends().map((c) => c.body)).toEqual([{ status: "succeeded" }]);
    expect(ends()[0]!.method).toBe("POST");
    const paths = calls.map((c) => new URL(c.url).pathname);
    expect(paths.indexOf("/api/answer-bank/system-writes")).toBeLessThan(paths.indexOf(`/api/autofill/runs/${RUN}/end`));
  });

  it("reports the end once when two status reads overlap as the frames end", async () => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    for (const f of frames) {
      f.state = "done";
      f.summary = summary(f.frameId === 0 ? [row({})] : []);
    }
    await Promise.all([ask("fill.status"), ask("fill.status")]);
    expect(ends().map((c) => c.body)).toEqual([{ status: "succeeded" }]);
    expect(calls.filter((c) => new URL(c.url).pathname === "/api/answer-bank/system-writes")).toHaveLength(1);
  });

  it.each(["stopped", "error"])("reports cancelled when the engine ends %s", async (state) => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    wrapExec(async (inj, orig) => (inj.func?.name === "statusFn" ? [{ result: { state, runId: RUN, stamp: "x", message: "boom" } }] : orig(inj)));
    await ask("fill.status");
    expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]);
  });

  it("reports cancelled when no form was found", async () => {
    frames[0]!.probe = { controls: 0, required: 0 };
    wrapExec(async (inj, orig) => (inj.func?.name === "startFn" ? [{ result: { started: false, reason: "no form" } }] : orig(inj)));
    await ask("fill.start", { posting: {} });
    await vi.waitFor(() => expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]));
  });

  it("reports cancelled when the page navigates away mid-fill, and not after the fill ended", async () => {
    await ask("fill.start", { posting: {} });
    calls.length = 0;
    navigate();
    await vi.waitFor(() => expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]));

    PANEL.tab = { id: ++tabSeq, url: PAGE } as chrome.tabs.Tab;
    topDoc = PANEL.doc = `doc-${tabSeq}`;
    await ask("fill.start", { posting: {} });
    await finish([row({})]);
    calls.length = 0;
    navigate();
    // The listener only starts pageChanged; any end it sent would be several awaits later.
    await new Promise((r) => setTimeout(r, 30));
    expect(ends()).toHaveLength(0);
  });

  describe("a navigation while the fill is starting", () => {
    it("writes nothing into the new document when the page is replaced while the frames are admitted", async () => {
      let release!: () => void;
      const gate = new Promise<void>((r) => (release = r));
      const getAll = chrome.webNavigation.getAllFrames as unknown as ReturnType<typeof vi.fn>;
      const orig = getAll.getMockImplementation()! as (...x: unknown[]) => unknown;
      getAll.mockImplementation(async (...a: unknown[]) => {
        await gate;
        return orig(...a);
      });
      const pending = ask("fill.start", { posting: {} });
      await vi.waitFor(() => expect(getAll).toHaveBeenCalled());
      navigate();
      release();
      const r = (await pending) as { ok: true; data: { phase: string; message: string } };
      expect(r.data).toMatchObject({ phase: "stopped", message: "The page changed, so the fill ended." });
      expect(started).toEqual([]);
      await vi.waitFor(() => expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]));
      PANEL.doc = topDoc;
      const status = (await ask("fill.status")) as { data: { phase: string; message: string } };
      expect(status.data).toMatchObject({ phase: "stopped", message: "The page changed, so the fill ended." });
      await new Promise((r2) => setTimeout(r2, 30));
      expect(ends()).toHaveLength(1);
    });

    it("starts nothing when the page is replaced before the run is issued", async () => {
      wrapRoute = (c) => {
        if (c.url.endsWith("/api/autofill/runs")) navigate();
        return undefined;
      };
      const r = (await ask("fill.start", { posting: {} })) as { data: { phase: string } };
      expect(r.data.phase).toBe("stopped");
      expect(started).toEqual([]);
      expect(injected).toEqual([]);
      await vi.waitFor(() => expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]));
    });

    // The browser may still call the old document current for a moment after a navigation
    // committed, and an injection aimed at it lands in the new one. The refusal is the page's own.
    it("refuses in the page when the document was replaced behind the worker's back", async () => {
      wrapExec(async (inj, orig) => {
        if (inj.files?.includes("engine.js")) topDoc = `${topDoc}-replaced`;
        return orig(inj);
      });
      const r = (await ask("fill.start", { posting: {} })) as { data: { phase: string; message: string } };
      expect(r.data).toMatchObject({ phase: "stopped", message: "The page changed, so the fill ended." });
      expect(started).toEqual([]);
      await vi.waitFor(() => expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]));
    });

    it("stops the engines it started when the page is replaced while the frames start", async () => {
      frames.push({ frameId: 5, url: "https://job-boards.greenhouse.io/embed/job_app", probe: { controls: 4, required: 0 } });
      const stops: unknown[] = [];
      wrapExec(async (inj, orig) => {
        if (inj.func?.name === "startFn" && inj.target.frameIds[0] === 0) {
          const out = await orig(inj);
          topDoc = `${topDoc}-replaced`;
          return out;
        }
        if (inj.func?.name === "controlFn") stops.push(inj.args);
        return orig(inj);
      });
      const r = (await ask("fill.start", { posting: {} })) as { data: { phase: string } };
      expect(r.data.phase).toBe("stopped");
      expect(started).toEqual([0]);
      expect(stops).toEqual([["stop", RUN]]);
      await vi.waitFor(() => expect(ends().map((c) => c.body)).toEqual([{ status: "cancelled" }]));
    });

    it("issues no run when the page was replaced during the reads", async () => {
      wrapRoute = (c) => {
        if (c.url.endsWith("/api/autofill/applicant")) navigate();
        return undefined;
      };
      const r = await ask("fill.start", { posting: {} });
      expect(r).toMatchObject({ ok: false, error: { code: "conflict" } });
      expect(calls.some((c) => c.url.endsWith("/api/autofill/runs"))).toBe(false);
    });
  });
});


// The worker's per-tab job context (QST-86): the anchor a posting page leaves,
// the form page that reads it, the pin and the tab an Apply button opens.
describe("job context worker", () => {
  const JD = "Build payment services in Go for a growing platform team. ".repeat(6);
  const POSTING_URL = "https://jobs.example.test/acme/123";
  const FORM_URL = "https://jobs.example.test/acme/123/apply";

  const posting: PageReading = { url: POSTING_URL, company: "Acme", role: "Backend Engineer", location: "Remote", jdText: JD, jdSource: "rendered", posting: true, form: false };
  const formPage: PageReading = { url: FORM_URL, jdText: "First name Email Phone Resume Submit application ".repeat(8), jdSource: "rendered", posting: true, form: true };

  let created: ((tab: { id?: number; openerTabId?: number }) => void) | undefined;
  const sender = (tabId: number, url = FORM_URL) => ({ id: "ext-id", tab: { id: tabId, url }, frameId: 0 }) as unknown as chrome.runtime.MessageSender;
  const ask = async (kind: string, extra: Record<string, unknown>, tabId = 7) => {
    const r = await dispatch({ kind, ...extra }, sender(tabId));
    if (!r.ok) throw new Error(r.error.message);
    return r.data as JobContext;
  };

  beforeEach(() => {
    installChrome({ tabs: { onCreated: { addListener: vi.fn((f) => (created = f)) }, onRemoved: { addListener: vi.fn() } } });
    registerJobContext();
  });

  describe("the tab's job", () => {
    it("a posting page leaves its anchor and the form page after it uses the anchored description", async () => {
      expect(await ask("context.resolve", { reading: posting })).toMatchObject({ source: "page", url: POSTING_URL });
      const c = await ask("context.resolve", { reading: formPage });
      expect(c).toMatchObject({ source: "anchor", url: POSTING_URL, jdText: JD.trim(), company: "Acme", location: "Remote" });
    });

    it("a form page never replaces the anchor with its own text", async () => {
      await ask("context.resolve", { reading: posting });
      await ask("context.resolve", { reading: { ...formPage, posting: false } });
      await ask("context.resolve", { reading: formPage });
      expect((await ask("context.resolve", { reading: formPage })).jdText).toBe(JD.trim());
    });

    it("the pin beats the page until it is taken off", async () => {
      await ask("context.resolve", { reading: posting });
      expect(await ask("context.pin", { reading: posting, pin: true })).toMatchObject({ source: "pinned", url: POSTING_URL });
      const other: PageReading = { ...posting, url: "https://jobs.example.test/globex/9", company: "Globex", jdText: JD.replace(/Go/g, "Rust") };
      expect(await ask("context.resolve", { reading: other })).toMatchObject({ source: "pinned", company: "Acme" });
      expect(await ask("context.pin", { reading: other, pin: false })).toMatchObject({ source: "page", company: "Globex" });
    });

    it("a tab opened from the posting starts with its anchor", async () => {
      await ask("context.resolve", { reading: posting }, 7);
      created!({ id: 8, openerTabId: 7 });
      await vi.waitFor(async () => expect(await ask("context.resolve", { reading: formPage }, 8)).toMatchObject({ source: "anchor", url: POSTING_URL }));
    });

    describe("a form-only board page", () => {
      const LEVER = "https://jobs.lever.co/acme/5f3c2a1b-0000-4000-8000-000000000001/apply";
      const leverForm: PageReading = { url: `${LEVER}?lever-source=x`, company: "Acme", role: "Acme", jdText: "", posting: true, form: true };
      const BOARD_TEXT = "Build the platform in Go.\nRequirements\n- Go";
      let board: Call[];
      let answer: () => Response | Promise<Response>;
      beforeEach(async () => {
        await store.put("token", TOKEN);
        answer = () => ok({ title: "Platform Engineer", location: "Durham, NC", text: BOARD_TEXT });
        board = installFetch((c) => (c.url.includes("/api/autofill/posting") ? answer() : fail("not_found", "no", 404)));
      });

      it("knows the Lever /apply and Ashby /application pages and nothing else", () => {
        expect(formOnlyBoardPage(LEVER)).toBe(true);
        expect(formOnlyBoardPage("https://jobs.eu.ashbyhq.com/acme/9a8b7c6d-0000-4000-8000-000000000002/application")).toBe(true);
        for (const u of ["https://jobs.lever.co/acme/5f3c2a1b-0000-4000-8000-000000000001", "http://jobs.lever.co/acme/x/apply", "https://jobs.lever.co.evil.test/acme/x/apply", "https://boards.greenhouse.io/acme/jobs/1", FORM_URL, "not a url"]) {
          expect(formOnlyBoardPage(u), u).toBe(false);
        }
      });

      it("reads the posting once from the server and keeps it as the tab's anchor", async () => {
        const c = await ask("context.resolve", { reading: leverForm }, 31);
        expect(c).toMatchObject({ source: "board", url: LEVER, jdText: BOARD_TEXT, title: "Platform Engineer", location: "Durham, NC", company: "Acme" });
        expect(board).toHaveLength(1);
        expect(new URL(board[0].url).searchParams.get("url")).toBe(LEVER);
        expect(board[0].headers.Authorization).toBe(`Bearer ${TOKEN}`);
        expect(await ask("context.resolve", { reading: leverForm }, 31)).toMatchObject({ source: "board", jdText: BOARD_TEXT });
        expect(board).toHaveLength(1);
      });
    });

    it("a message's reading is checked, never trusted for its shape", async () => {
      const c = await ask("context.resolve", { reading: { url: 5, jdText: { evil: true }, posting: "yes" } });
      expect(c).toMatchObject({ source: "none", url: FORM_URL });
    });
  });

  describe("reading the page", () => {
    it("a form page reads no rendered text as its description, and a posting page reads its own", () => {
      (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL("https://example.com/careers/acme/apply");
      document.body.innerHTML = `<main><label for="f">First name</label><input id="f"><label for="e">Email</label><input id="e" type="email"><label for="p">Phone</label><input id="p"><input type="file" aria-label="Resume"></main>`;
      expect(readPosting(document)).toMatchObject({ form: true, jdText: "" });
      document.body.innerHTML = `<main>Backend Engineer. Responsibilities: build services. Qualifications: Go. Full-time. ${"Details. ".repeat(40)}</main>`;
      expect(readPosting(document)).toMatchObject({ form: false, posting: true, jdSource: "rendered" });
    });
  });
});

describe("review journal write failure", () => {
  for (const area of ["local", "session"] as const) {
    it(`${area} write refusal must not strand a fill in starting`, async () => {
      vi.mocked(chrome.storage[area].set).mockRejectedValueOnce(new Error("QUOTA_BYTES quota exceeded"));
      const first = await ask("fill.start", { posting: {} });
      expect(first.ok).toBe(false);
      expect(started).toEqual([]);
      const retry = await ask("fill.start", { posting: {} });
      expect(retry).toMatchObject({ ok: true });
    });
  }
});


describe("review recovery read failure", () => {
  it("allows a new start retry after a transient startup journal read refusal", async () => {
    // Settle this worker's empty recovery first.
    await ask("fill.status");
    vi.mocked(chrome.storage.local.get).mockRejectedValueOnce(new Error("temporary local read failure"));
    const again = await restart();
    const first = await again("fill.start", { doc: PANEL.doc, posting: {} });
    expect(first.ok).toBe(false);
    const retry = await again("fill.start", { doc: PANEL.doc, posting: {} });
    expect(retry).toMatchObject({ ok: true });
  });
});
