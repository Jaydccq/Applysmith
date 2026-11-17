import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Row, Summary, TerminalNotice } from "../../engine/src/protocol";
import { dispatch } from "../src/sw/router";
import { registerFill } from "../src/sw/fill";
import * as store from "../src/sw/store";
import { looksLikeApplicationForm } from "../src/shared/site";
import type { SubmissionEntry } from "../src/shared/messages";
import { startRecorder, type Recorder } from "../src/widgets/recorder";
import { submitControlOf } from "../src/widgets/submit";
import { fail, installChrome, installFetch, ok, type Call } from "./chrome-mock";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({
  send: (r: unknown) => send(r),
  failureOf: (e: { failure?: unknown }) => e.failure ?? { code: "internal", message: "Something went wrong." },
  WorkerDisconnected: class extends Error {
    constructor(readonly failure: unknown, readonly invalidated: boolean) {
      super("disconnected");
    }
  },
}));

const setUrl = (u: string) => (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL(u);

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("strict reading of an application form", () => {
  it("webshop with newsletter email and upload is not an application", () => {
    // An email box and a spreadsheet upload: a shop's newsletter, with one signal.
    document.body.innerHTML = `<form><label for="e">Email</label><input id="e" type="email"><label for="f">Price list</label><input id="f" type="file"><button>Send</button></form>`;
    expect(looksLikeApplicationForm(document)).toBe(false);
    // Three fields, an email and an upload, but only one kind of evidence.
    document.body.innerHTML = `<form><label for="e">Email</label><input id="e" type="email"><label for="n">Note</label><input id="n"><label for="f">Spreadsheet</label><input id="f" type="file"></form>`;
    expect(looksLikeApplicationForm(document)).toBe(false);
    // A sign-in page: any password field rules the page out, in the light tree or in a shadow root.
    document.body.innerHTML = `<form><label for="e">Email</label><input id="e" type="email"><label for="p">Password</label><input id="p" type="password"><label for="n">First name</label><input id="n"><label for="t">Phone</label><input id="t" type="tel"></form>`;
    expect(looksLikeApplicationForm(document)).toBe(false);
    document.body.innerHTML = `<form><label for="e">Email</label><input id="e" type="email"><label for="n">First name</label><input id="n"><label for="t">Phone</label><input id="t" type="tel"></form><div id="host"></div>`;
    expect(looksLikeApplicationForm(document)).toBe(true);
    (document.getElementById("host") as HTMLElement).attachShadow({ mode: "open" }).innerHTML = `<input type="password" aria-label="Password">`;
    expect(looksLikeApplicationForm(document)).toBe(false);
  });

  it("an email field with two independent kinds of evidence among at least three fields is an application", () => {
    document.body.innerHTML = `<form><label for="e">Email address</label><input id="e"><label for="n">Full name</label><input id="n"><label for="f">Resume</label><input id="f" type="file"></form>`;
    expect(looksLikeApplicationForm(document)).toBe(true);
    // Wording counts as evidence, but alone it is one kind.
    document.body.innerHTML = `<form><input type="email" aria-label="Contact"><input aria-label="Reference"><input aria-label="Notes"></form><p>Attach your cover letter and tell us about your work authorization.</p>`;
    expect(looksLikeApplicationForm(document)).toBe(false);
    document.body.innerHTML = `<form><input type="email" aria-label="Contact"><input type="tel" aria-label="Number"><input aria-label="Notes"></form><p>Tell us about your work authorization.</p>`;
    expect(looksLikeApplicationForm(document)).toBe(true);
    // A label is looked up inside the root its control lives in: the id "x" here names two different labels.
    document.body.innerHTML = `<div id="host"></div><label for="x">Price</label><input id="x">`;
    const root = (document.getElementById("host") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<label for="x">First name</label><input id="x"><label for="y">Mobile phone</label><input id="y"><input type="email" aria-label="Email">`;
    expect(looksLikeApplicationForm(document)).toBe(true);
  });
});

describe("what counts as the submit control", () => {
  it("untyped helper button is not a submit", () => {
    document.body.innerHTML = `<form>
      <button id="gear"><svg></svg></button>
      <button id="icon" type="submit"><svg></svg></button>
      <button id="upload">Upload resume</button>
      <button id="soft1">Apply</button><button id="soft2">Confirm</button>
      <button id="final1" type="button">Submit</button><button id="final2" type="button">Send application</button>
      <button id="now" type="submit">Apply now</button>
      <div id="rb" role="button">Confirm</div><input id="nxt" type="submit" value="Next"></form>`;
    const found = (id: string) => submitControlOf(document.getElementById(id)) !== null;
    // An untyped button reads as a submit by default; that alone does not make it one, and an empty label counts only when typed.
    expect(found("gear")).toBe(false);
    expect(found("icon")).toBe(true);
    expect(found("upload")).toBe(false);
    // A bare Apply or Confirm is a soft match, and the plain submit wordings are final.
    expect([found("soft1"), found("soft2"), found("final1"), found("final2"), found("rb")]).toEqual([true, true, true, true, true]);
    expect(found("nxt")).toBe(false);
    // Step and account words are never final, typed or not.
    const never = ["Next", "Continue", "Save", "Back", "Register", "Sign in", "Create account", "Add", "Upload", "Edit", "Cancel", "Skip", "Start", "Login", "Apply now", "Search"];
    for (const label of never) {
      document.body.innerHTML = `<form><button id="b" type="submit">${label}</button></form>`;
      expect(found("b"), label).toBe(false);
    }
    // A form that asks for a password signs in, whatever its button says.
    document.body.innerHTML = `<form><input type="password"><button id="b" type="submit">Submit</button></form>`;
    expect(found("b")).toBe(false);
  });
});

describe("in-document address changes", () => {
  let entries: SubmissionEntry[];
  let recorder: Recorder | undefined;

  function userClick(el: Element): void {
    const e = new MouseEvent("click", { bubbles: true, cancelable: true, composed: true });
    Object.defineProperty(e, "isTrusted", { value: true });
    el.dispatchEvent(e);
  }

  beforeEach(() => {
    vi.useFakeTimers();
    entries = [];
    setUrl("https://example.com/careers/backend/apply/step-1");
    document.title = "Apply - Backend Engineer - Example Corp";
    document.body.innerHTML = `<main><form id="f"><button id="go" type="submit">Submit application</button></form></main>`;
    document.getElementById("f")!.addEventListener("submit", (e) => e.preventDefault());
  });
  afterEach(() => {
    recorder?.destroy();
    // The MutationObserver spy below must not outlive its test.
    vi.restoreAllMocks();
  });

  it("pushState navigation re-arms recorder without patching history", async () => {
    const push = history.pushState;
    const replace = history.replaceState;
    const observe = vi.spyOn(MutationObserver.prototype, "observe");
    let now = 1_000_000;
    recorder = startRecorder({ doc: document, win: window, record: async (e) => void entries.push(e), now: () => now });
    expect(history.pushState).toBe(push);
    expect(history.replaceState).toBe(replace);
    expect(observe).not.toHaveBeenCalled();

    userClick(document.getElementById("go")!);
    expect(entries.map((e) => e.url)).toEqual(["https://example.com/careers/backend/apply/step-1"]);
    // The single-page application moves on without loading a document; the address is read when it matters.
    history.pushState({}, "", "/careers/backend/apply/step-2");
    now += 1_000;
    userClick(document.getElementById("go")!);
    expect(entries.filter((e) => !e.confirmed).map((e) => e.url)).toEqual([
      "https://example.com/careers/backend/apply/step-1",
      "https://example.com/careers/backend/apply/step-2",
    ]);
    // And the move to a thank-you address confirms the entry made on the page it left.
    history.pushState({}, "", "/careers/backend/apply/thank-you");
    await vi.advanceTimersByTimeAsync(600);
    expect(entries.at(-1)).toMatchObject({ confirmed: true });
    expect(history.pushState).toBe(push);
    expect(observe).not.toHaveBeenCalled();
  });
});

describe("a panel the page took out", () => {
  it("soft navigation that removes the panel root re-mounts it", async () => {
    setUrl("https://example.org/careers");
    const env = installChrome();
    send.mockReset();
    send.mockImplementation(async (r: { kind: string }) => {
      if (r.kind === "site.get") return { off: false };
      if (r.kind === "state") return { prefs: { autoOpen: false } };
      if (r.kind === "health") return { connection: "connected" };
      if (r.kind === "flags.get") return { off: true };
      return {};
    });
    const roots = () => document.querySelectorAll("[data-applysmith]").length;
    document.body.innerHTML = "<main>first route</main>";
    vi.resetModules();
    await import("../src/panel/main");
    await vi.waitFor(() => expect(roots()).toBe(1));

    // A second injection while the panel is there only reuses it.
    vi.resetModules();
    await import("../src/panel/main");
    await new Promise((r) => setTimeout(r, 20));
    expect(roots()).toBe(1);

    // The page's router replaces the body; the script's context, and so its record of the panel, survives.
    document.body.innerHTML = "<main>second route</main>";
    expect(roots()).toBe(0);
    vi.resetModules();
    await import("../src/panel/main");
    await vi.waitFor(() => expect(roots()).toBe(1));
    expect((env.chrome.runtime.onMessage.addListener as unknown as { mock: { calls: unknown[] } }).mock.calls).toHaveLength(1);
  });
});

describe("the automatic open", () => {
  // Every application system of the engine's site table is a supported site, not only the hosts whose frames the fill admits.
  for (const [url, opens] of [
    ["https://example.wd5.myworkdayjobs.com/en-US/External/job/Remote/Engineer_JR1", true],
    ["https://example.org/careers", false],
  ] as const) {
    it(`${opens ? "opens" : "does not open"} by itself on ${new URL(url).hostname}`, async () => {
      setUrl(url);
      installChrome();
      send.mockReset();
      send.mockImplementation(async (r: { kind: string }) => {
        if (r.kind === "site.get") return { off: false };
        if (r.kind === "ui.get") return {};
        if (r.kind === "state") return { prefs: { autoOpen: true } };
        if (r.kind === "health") return { connection: "connected" };
        if (r.kind === "flags.get") return { off: true };
        if (r.kind === "fill.status") return { phase: "idle", frames: [] };
        return {};
      });
      document.body.innerHTML = "<main>posting</main>";
      vi.resetModules();
      await import("../src/panel/main");
      await vi.waitFor(() => expect(document.querySelectorAll("[data-applysmith]").length).toBe(1));
      await new Promise((r) => setTimeout(r, 20));
      const sets = send.mock.calls.map((c) => c[0] as { kind: string; open?: boolean; auto?: boolean }).filter((r) => r.kind === "ui.set");
      expect(sets.some((r) => r.auto === true), url).toBe(opens);
      expect(sets.some((r) => r.open === true), url).toBe(opens);
      document.querySelector("[data-applysmith]")?.remove();
    });
  }
});

describe("the manifest", () => {
  it("never declares a form builder or the engine as a content script", async () => {
    const { readFileSync } = await import("node:fs");
    const manifest = JSON.parse(readFileSync(`${process.cwd()}/manifest.json`, "utf8")) as { content_scripts: { js: string[]; matches: string[] }[] };
    expect(manifest.content_scripts.flatMap((c) => c.js)).toEqual(["loader.js"]);
    for (const c of manifest.content_scripts) for (const m of c.matches) expect(m).toMatch(/^https?:\/\/\*\/\*$|^http:\/\/\*\/\*$|^https:\/\/\*\/\*$/);
  });
});

// --- the fill: one snapshot per fill, one terminal result per frame -------------------------------------------------

const TOKEN = "T".repeat(43);
const RUN = "run_0123456789abcdef01234567";
const PAGE = "https://example.com/careers/apply?ref=secret";
let tabSeq = 700;
const PANEL = { id: "ext-id", frameId: 0, doc: "doc-7", tab: { id: 7, url: PAGE } as chrome.tabs.Tab, url: PAGE };

interface Frame {
  frameId: number;
  url: string;
  state?: string;
  summary?: Summary;
}

const row = (over: Partial<Row>): Row => ({
  selector: "#a", label: "First name", section: [], lane: "profile", kind: "text", required: true, outcome: "filled", review: false, value: "Alex", ...over,
});
const summary = (rows: Row[]): Summary => ({
  host: "example.com", url: "https://example.com/careers/apply", rows, drafts: [],
  counts: { filled: rows.filter((r) => r.outcome === "filled").length, prefilled: 0, review: 0, abstained: 0, failed: 0, skipped: 0, requiredUnfilled: 0 },
});

describe("the fill's frames", () => {
  let frames: Frame[];
  let calls: Call[];
  let startArgs: { frameId: number; applicant: unknown }[];
  let applicantFails: boolean;

  beforeEach(async () => {
    PANEL.tab = { id: ++tabSeq, url: PAGE } as chrome.tabs.Tab;
    PANEL.doc = `doc-${tabSeq}`;
    startArgs = [];
    applicantFails = false;
    frames = [
      { frameId: 0, url: PAGE },
      { frameId: 1, url: "https://job-boards.greenhouse.io/embed/job_app" },
      { frameId: 2, url: "https://jobs.lever.co/example/abc/apply" },
    ];
    installChrome({
      scripting: {
        executeScript: vi.fn(async (inj: { target: { frameIds: number[] }; files?: string[]; func?: { name: string }; args?: unknown[] }) => {
          const id = inj.target.frameIds[0]!;
          const f = frames.find((x) => x.frameId === id)!;
          if (inj.files) return [{ result: undefined }];
          switch (inj.func?.name) {
            case "probeFn":
            case "lightProbeFn": return [{ result: { controls: 6, required: 2 } }];
            case "docFn": return [{ result: id === 0 ? PANEL.doc : null }];
            case "startFn":
              startArgs.push({ frameId: id, applicant: (inj.args![0] as { applicant: unknown }).applicant });
              return [{ result: { started: true, runId: (inj.args![0] as { runId: string }).runId } }];
            case "statusFn": return [{ result: f.state === "done" ? { state: "done", runId: RUN, stamp: "x", summary: f.summary } : { state: "running", runId: RUN, stamp: "x" } }];
            case "logFn": return [{ result: [] }];
          }
          return [{ result: true }];
        }),
      },
      webNavigation: { getAllFrames: vi.fn(async () => frames.map((f) => ({ frameId: f.frameId, url: f.url, documentLifecycle: "active" }))), onCommitted: { addListener: vi.fn() } },
      tabs: { onRemoved: { addListener: vi.fn() } },
    });
    await store.put("token", TOKEN);
    calls = installFetch((c) => {
      const path = new URL(c.url).pathname;
      if (path === "/api/autofill/applicant") return applicantFails ? fail("unavailable", "down", 503) : ok({ candidate: { fullName: "Alex Example", email: "alex.example@example.com" } });
      if (path === "/api/answer-bank/settings") return ok({ autoAcceptLegalDeclarations: false });
      if (path === "/api/autofill/runs") return ok({ runId: RUN }, 202);
      if (path === `/api/autofill/runs/${RUN}/end`) return ok({ runId: RUN, status: "succeeded" });
      if (path === "/api/answer-bank/system-writes") return ok({ recorded: true });
      return fail("not_found", "no", 404);
    });
    registerFill();
  });

  const ask = (kind: string, extra: Record<string, unknown> = {}, sender: Record<string, unknown> = PANEL) =>
    dispatch({ kind, ...(kind === "fill.start" ? { doc: PANEL.doc } : {}), ...extra }, sender as chrome.runtime.MessageSender);
  const applicantCalls = () => calls.filter((c) => new URL(c.url).pathname === "/api/autofill/applicant").length;

  it("concurrent frames share one snapshot request", async () => {
    await ask("fill.start", { posting: {} });
    expect(startArgs.map((s) => s.frameId).sort()).toEqual([0, 1, 2]);
    expect(applicantCalls()).toBe(1);
    expect(new Set(startArgs.map((s) => JSON.stringify(s.applicant))).size).toBe(1);

    for (const f of frames) {
      f.state = "done";
      f.summary = summary([row({})]);
    }
    await ask("fill.status");
    // The next fill asks again: a frame never answers from the first fill's data.
    await ask("fill.start", { posting: {} });
    expect(applicantCalls()).toBe(2);
    expect(startArgs).toHaveLength(6);
  });

  it("later relay for a frame replaces its earlier counts", async () => {
    await ask("fill.start", { posting: {} });
    const notice = (rows: Row[]): { payload: string } => ({ payload: JSON.stringify({ runId: RUN, state: "done", summary: summary(rows) } satisfies TerminalNotice) });
    const embed = { ...PANEL, frameId: 1 };
    // A page that posts back re-dispatches the fill in the embedded frame: its result is relayed again, with fewer rows.
    await ask("engine.terminal", notice([row({ selector: "#a" }), row({ selector: "#b" }), row({ selector: "#c" })]), embed);
    await ask("engine.terminal", notice([row({ selector: "#a" })]), embed);
    frames[0]!.state = "done";
    frames[0]!.summary = summary([row({ selector: "#t1" }), row({ selector: "#t2" })]);
    frames[2]!.state = "done";
    frames[2]!.summary = summary([]);
    const view = (await ask("fill.status")) as { data: { counts: { filled: number }; frames: unknown[]; phase: string } };
    expect(view.data.phase).toBe("done");
    // The run total is 2 from the top frame and 1 from the embed, not 6; each frame's own decision stays beneath it.
    expect(view.data.counts.filled).toBe(3);
    expect(view.data.frames).toHaveLength(3);
  });
});
