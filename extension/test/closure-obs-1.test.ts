import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { postingFacts } from "../src/shared/capture";
import { HOOK_TAG, installHook } from "../src/mainworld/hook";
import { startRecorder, type Recorder } from "../src/widgets/recorder";
import { startSignIn, type SignIn } from "../src/widgets/signin";
import { startWidgets, watchPage, type Widgets } from "../src/widgets/main";
import type { SubmissionEntry } from "../src/shared/messages";
import { fakeHookPage } from "./chrome-mock";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({ send: (r: unknown) => send(r), failureOf: () => ({ code: "internal", message: "x" }) }));

const setUrl = (u: string) => (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL(u);

/** A click the way a person makes it: a trusted event cannot be built in a test, so the flag is forced. */
function userClick(el: Element): void {
  const e = new MouseEvent("click", { bubbles: true, cancelable: true, composed: true });
  Object.defineProperty(e, "isTrusted", { value: true });
  el.dispatchEvent(e);
}

describe("page widgets: the context gate", () => {
  let widgets: Widgets | undefined;
  const kinds = () => send.mock.calls.map((c) => (c[0] as { kind: string }).kind);
  const records = () => send.mock.calls.map((c) => c[0] as { kind: string; entry?: SubmissionEntry }).filter((m) => m.kind === "submission.record");

  beforeEach(() => {
    send.mockReset();
    send.mockImplementation(async (req: { kind: string }) => (req.kind === "flags.get" ? { off: false, editWithAI: true } : req.kind === "banner.get" ? { show: false } : undefined));
    document.head.innerHTML = "";
  });
  afterEach(() => {
    widgets?.destroy();
    widgets = undefined;
    delete (globalThis as { __applysmithEngine?: unknown }).__applysmithEngine;
  });

  async function openPage(url: string, body: string) {
    setUrl(url);
    document.body.innerHTML = body;
    widgets?.destroy();
    widgets = await startWidgets();
  }

  it("a submit on a contact-us form and on a login page record nothing; a manual submit on an employer-hosted application form records", async () => {
    await openPage("https://www.example.org/contact", `<form><label for="n">Your name</label><input id="n"><label for="e">Your email</label><input id="e" type="email"><textarea aria-label="Message"></textarea><button id="s" type="submit">Submit</button></form>`);
    userClick(document.getElementById("s")!);
    await openPage("https://www.example.org/account/login", `<form><label for="e">Email</label><input id="e" type="email"><input type="password" aria-label="Password"><button id="s" type="submit">Submit</button></form>`);
    userClick(document.getElementById("s")!);
    expect(records()).toHaveLength(0);

    // The same plain host, but the form asks what only an application asks.
    await openPage("https://www.example.org/openings/7", `<form><label for="n">Full name</label><input id="n"><label for="e">Email</label><input id="e" type="email"><label for="cv">Upload your résumé</label><input id="cv" type="file"><button id="s" type="submit">Submit application</button></form>`);
    userClick(document.getElementById("s")!);
    expect(records()).toHaveLength(1);
    expect(records()[0]!.entry).toMatchObject({ host: "www.example.org", url: "https://www.example.org/openings/7", confirmed: false });
    expect(kinds()).not.toContain("compose.run");

    // A run of the fill on the page is the strongest signal, with no vocabulary needed.
    send.mockClear();
    (globalThis as { __applysmithEngine?: unknown }).__applysmithEngine = { status: () => ({ state: "done" }) };
    await openPage("https://www.example.org/contact", `<form><label for="n">Your name</label><input id="n"><label for="e">Your email</label><input id="e" type="email"><textarea aria-label="Message"></textarea><button id="s" type="submit">Submit</button></form>`);
    userClick(document.getElementById("s")!);
    expect(records()).toHaveLength(1);
  });
});

describe("employer and role of a recorded entry", () => {
  const facts = (title: string, og?: string, ld?: object) => {
    document.head.innerHTML = (og ? `<meta property="og:site_name" content="${og}">` : "") + (ld ? `<script type="application/ld+json">${JSON.stringify(ld)}</script>` : "");
    document.title = title;
    return postingFacts(document);
  };

  it("an EEO-questions step title and a vendor-branded title yield no employer", () => {
    expect(facts("Backend Engineer - EEO Questions").company).toBeUndefined();
    expect(facts("Apply - Voluntary Self-Identification").company).toBeUndefined();
    expect(facts("Application - Candidate Profile").company).toBeUndefined();
    expect(facts("Backend Engineer - Greenhouse").company).toBeUndefined();
    expect(facts("Manual - Workday Careers").company).toBeUndefined();
    expect(facts("Backend Engineer - jobs.example.org").company).toBeUndefined();
    // The system's own name in the page's metadata does not count either.
    expect(facts("Backend Engineer", "Lever").company).toBeUndefined();
    // A real employer survives, from the metadata first, then from the title, then from the structured data.
    expect(facts("Apply - Backend Engineer - Example Corp")).toMatchObject({ company: "Example Corp", role: "Apply" });
    expect(facts("Backend Engineer at Example Corp", "Greenhouse").company).toBe("Example Corp");
    expect(facts("Backend Engineer", "Example Corp Careers").company).toBe("Example Corp");
    expect(facts("Login - Greenhouse", undefined, { "@type": "JobPosting", title: "Backend Engineer", hiringOrganization: { name: "Example Corp" } })).toMatchObject({ company: "Example Corp", role: "Backend Engineer" });
  });
});

describe("network confirmation", () => {
  it("a 200 POST reports once, a GET and a 500 report nothing, a throwing postMessage leaves the original promise intact", async () => {
    const env = fakeHookPage();
    installHook(env.page);
    env.fetchMock.mockResolvedValue({ status: 200 } as Response);
    await env.page.fetch("/api/apply", { method: "POST" });
    await env.page.fetch("/api/apply");
    env.fetchMock.mockResolvedValue({ status: 500 } as Response);
    await env.page.fetch("/api/apply", { method: "POST" });
    await Promise.resolve();
    expect(env.posted).toEqual([{ origin: "https://example.com", msg: { tag: HOOK_TAG, method: "POST", status: 200, path: "https://example.com/api/apply" } }]);
    // A request made with XMLHttpRequest follows the same rule.
    const xhr = new env.FakeXhr();
    xhr.open("GET", "/api/status");
    xhr.send();
    xhr.listeners.loadend!();
    expect(env.posted).toHaveLength(1);

    const broken = fakeHookPage();
    broken.page.postMessage = () => {
      throw new Error("observer broke");
    };
    installHook(broken.page);
    const pending = Promise.resolve({ status: 200 } as Response);
    broken.fetchMock.mockReturnValue(pending);
    const got = broken.page.fetch("/api/apply", { method: "POST" });
    expect(got).toBe(pending);
    await expect(got).resolves.toMatchObject({ status: 200 });
  });
});

describe("confirmation by the page's own signal", () => {
  let recorder: Recorder | undefined;
  let entries: SubmissionEntry[];
  let now: number;

  beforeEach(() => {
    vi.useFakeTimers();
    entries = [];
    now = 1_000_000;
    document.title = "Backend Engineer - Example Corp";
    document.body.innerHTML = `<main><form id="f"><label for="n">Name</label><input id="n"><button id="submit" type="submit">Submit application</button></form></main>`;
    document.getElementById("f")!.addEventListener("submit", (e) => e.preventDefault());
  });
  afterEach(() => {
    recorder?.destroy();
    recorder = undefined;
    vi.useRealTimers();
  });

  const start = () => (recorder = startRecorder({ doc: document, win: window, record: async (e) => void entries.push(e), now: () => now }));
  const net = (data: object) => window.dispatchEvent(new MessageEvent("message", { data: { tag: HOOK_TAG, method: "POST", status: 200, ...data }, origin: window.location.origin, source: window }));

  it("a 200 from an Ashby host does not confirm; the success phrase does", () => {
    setUrl("https://jobs.ashbyhq.com/example/00000000/application");
    start();
    userClick(document.getElementById("submit")!);
    // The API answers a bounced submit exactly as it answers an accepted one.
    net({ path: "https://jobs.ashbyhq.com/api/application/submit" });
    expect(entries.map((e) => e.confirmed)).toEqual([false]);
    document.querySelector("main")!.insertAdjacentText("beforeend", "Thank you for applying!");
    vi.advanceTimersByTime(600);
    expect(entries.map((e) => e.confirmed)).toEqual([false, true]);
  });

  it("analytics hosts and static assets never confirm, and an application endpoint does", () => {
    setUrl("https://careers.example.org/jobs/1/apply");
    start();
    userClick(document.getElementById("submit")!);
    net({ path: "https://api.segment.io/v1/job" });
    net({ path: "https://o1.ingest.sentry.io/api/job/envelope" });
    net({ path: "https://careers.example.org/static/application.js" });
    // The form is gone, so only the kind of call decides.
    document.getElementById("f")!.remove();
    expect(entries.map((e) => e.confirmed)).toEqual([false]);
    net({ path: "https://careers.example.org/api/apply" });
    expect(entries.map((e) => e.confirmed)).toEqual([false, true]);
  });
});

describe("the legal-declarations setting fails closed", () => {
  const roots: ShadowRoot[] = [];
  const realAttach = Element.prototype.attachShadow;
  let widget: SignIn | undefined;
  const outcomes: string[] = [];

  beforeEach(() => {
    vi.useFakeTimers();
    roots.length = 0;
    outcomes.length = 0;
    Element.prototype.attachShadow = function (init: ShadowRootInit) {
      const r = realAttach.call(this, { ...init, mode: "open" });
      roots.push(r);
      return r;
    };
  });
  afterEach(() => {
    widget?.destroy();
    widget = undefined;
    Element.prototype.attachShadow = realAttach;
    vi.useRealTimers();
  });

  it("a failing settings read leaves every agreement box unticked and types nothing", async () => {
    for (const reading of [async () => { throw new Error("bridge down"); }, async () => ({}) as { acceptLegalDeclarations: boolean }, async () => ({ acceptLegalDeclarations: "yes" as unknown as boolean })]) {
      outcomes.length = 0;
      document.body.innerHTML = `<form id="f"><input id="email" type="email"><input id="pw" type="password"><input id="pw2" type="password"><label><input id="terms" type="checkbox" required> I agree to the terms and privacy policy</label><button id="go" type="submit">Create account</button></form>`;
      const resolve = vi.fn(async () => ({ email: "alex.example@example.com", password: "Example-Pass-123", source: "settings-default" }));
      widget = startSignIn({ doc: document, win: window, changes: watchPage(document), resolve, settings: reading, onOutcome: (o) => outcomes.push(o), verdictMs: 1000, pollMs: 100 });
      roots.filter((r) => r.host.isConnected).map((r) => r.querySelector<HTMLButtonElement>(".btn")).find(Boolean)!.click();
      await vi.advanceTimersByTimeAsync(4000);
      // Nothing is typed or ticked, and the person is told why.
      expect(outcomes).toEqual(["consent-required"]);
      expect((document.getElementById("terms") as HTMLInputElement).checked).toBe(false);
      expect((document.getElementById("email") as HTMLInputElement).value).toBe("");
      expect(resolve).not.toHaveBeenCalled();
      widget.destroy();
    }
  });
});
