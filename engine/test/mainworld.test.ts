// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Injection into an isolated world and re-injection (main-world helper, submit observer);
// Drivers for application boards (Ashby save queue).
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { HOLDER, installHelper } from "../src/mainworld/helper";
import { readEcho, type EchoState } from "../src/mainworld/ashby-queue";
import { callComponent, callReact } from "../src/mainworld/components";
import { MainWorld } from "../src/mainworld-client";
import type { Win } from "../src/dom";
import { ashbyHolds } from "../src/ashby";
import { StopSignal } from "../src/env";
import { SHADOW_SEP, selectorFor } from "../src/selector";
import { ADA, ctx, fakeHost, makeEngine, mount, runFill, setUrl, summaryOf, until, uninstall } from "./helpers";
import { mountAshby, sleep } from "./ats-fixtures";

const win = window as unknown as Win;
const NONCE = "n0nce-test";
const realFetch = window.fetch;

beforeEach(() => {
  uninstall();
  window.sessionStorage.clear();
  window.fetch = realFetch;
});
afterEach(() => {
  uninstall();
  window.fetch = realFetch;
});

function endpoint(status = 200): { calls: { url: string; init?: RequestInit }[] } {
  const calls: { url: string; init?: RequestInit }[] = [];
  window.fetch = (async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    return new Response("{}", { status });
  }) as typeof fetch;
  return { calls };
}

describe("Main-world helper", () => {
  test("Submit observed across a navigation: a POST that returned 200 is one request entry with the endpoint's origin and path, and the request itself was neither delayed nor altered", async () => {
    const server = endpoint(200);
    uninstall();
    installHelper(win, NONCE, "s1");
    const init = { method: "POST", body: "a=1", headers: { "X-Test": "1" } };
    const pending = window.fetch("https://apply.example.com/submit?token=secret#frag", init);
    // Not delayed: the page's own fetch ran before the call returned.
    expect(server.calls).toHaveLength(1);
    const res = await pending;
    expect(res.status).toBe(200);
    expect(server.calls[0]?.init).toBe(init);
    // The next document: a new engine reads what the first document's helper stored.
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    expect(engine.submitObservations()).toMatchObject([{ kind: "request", method: "POST", status: 200, url: "https://apply.example.com/submit" }]);
    expect(JSON.stringify(engine.submitObservations())).not.toContain("secret");
  });

  test("A GET, a failed POST and a redirect-free PUT are told apart: only POST, PUT and PATCH with a 2xx count", async () => {
    for (const [method, status] of [["GET", 200], ["POST", 500], ["PUT", 204], ["PATCH", 201]] as const) {
      installWith((async () => new Response("", { status })) as typeof fetch);
      await window.fetch("https://apply.example.com/x", { method });
    }
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    expect(engine.submitObservations().map((e) => `${e.method}:${e.status}`)).toEqual(["PUT:204", "PATCH:201"]);
  });

  test("A native submission is a submit-event entry; the observer keeps at most 50 entries for 10 minutes", async () => {
    installHelper(win, NONCE, "s1");
    mount(`<form id="f" action="https://apply.example.com/post?x=1"></form>`);
    document.getElementById("f")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    expect(engine.submitObservations()).toMatchObject([{ kind: "submit-event", url: "https://apply.example.com/post" }]);
    const old = Date.now() - 11 * 60 * 1000;
    window.sessionStorage.setItem(
      "aj.obs",
      JSON.stringify(Array.from({ length: 60 }, (_, i) => ({ time: i < 5 ? old : Date.now(), kind: "request", method: "POST", status: 200, url: `https://a.example.com/${i}` }))),
    );
    expect(engine.submitObservations(20 * 60 * 1000).length).toBe(55);
    installWith((async () => new Response("", { status: 200 })) as typeof fetch);
    await window.fetch("https://apply.example.com/y", { method: "POST" });
    const kept = JSON.parse(window.sessionStorage.getItem("aj.obs") as string) as unknown[];
    expect(kept.length).toBeLessThanOrEqual(50);
  });

  test("The observer never throws into page code: a fetch that rejects still rejects for the page, and storage failures are swallowed", async () => {
    installWith((async () => {
      throw new TypeError("network down");
    }) as typeof fetch);
    await expect(window.fetch("https://apply.example.com/x", { method: "POST" })).rejects.toThrow("network down");
    const store = window.sessionStorage;
    const orig = store.setItem.bind(store);
    store.setItem = () => {
      throw new Error("quota");
    };
    installWith((async () => new Response("", { status: 200 })) as typeof fetch);
    await expect(window.fetch("https://apply.example.com/x", { method: "POST" })).resolves.toBeInstanceOf(Response);
    store.setItem = orig;
  });

  test("A request without the nonce in its event name is never answered, and an older instance is replaced by a newer stamp", async () => {
    installHelper(win, NONCE, "s1");
    const seen: string[] = [];
    document.addEventListener("aj:wrong:res", (e) => seen.push(String((e as CustomEvent).detail)));
    document.dispatchEvent(new CustomEvent("aj:wrong:req", { detail: JSON.stringify({ id: 1, op: "queue.echo" }) }));
    await sleep(5);
    expect(seen).toEqual([]);
    const mw = new MainWorld(win, NONCE);
    const stampOf = () => ((win as unknown as Record<symbol, unknown>)[HOLDER] as { stamp: string }).stamp;
    expect(await mw.request("queue.echo")).toMatchObject({ seen: false });
    expect(stampOf()).toBe("s1");
    installHelper(win, NONCE, "s2");
    expect(await mw.request("queue.echo")).toMatchObject({ seen: false });
    expect(stampOf()).toBe("s2");
    mw.dispose();
  });

  test("A second host on the same document gets a helper that listens on its own nonce, and the first nonce goes quiet", async () => {
    installHelper(win, "first-nonce", "s1");
    installHelper(win, "second-nonce", "s1");
    const second = new MainWorld(win, "second-nonce");
    await expect(second.request("queue.echo", undefined, 50)).resolves.toMatchObject({ seen: false });
    const first = new MainWorld(win, "first-nonce");
    await expect(first.request("queue.echo", undefined, 20)).rejects.toThrow("binding-unavailable");
    // The same nonce and stamp again is the helper already there: nothing is installed twice.
    const holder = (win as unknown as Record<symbol, unknown>)[HOLDER];
    installHelper(win, "second-nonce", "s1");
    expect((win as unknown as Record<symbol, unknown>)[HOLDER]).toBe(holder);
    first.dispose();
    second.dispose();
  });

});

/** The page's fetch is whatever it is when the helper installs; a test swaps it before each install. */
function installWith(fetchImpl: typeof fetch): void {
  uninstall();
  window.fetch = fetchImpl;
  installHelper(win, NONCE, "s1");
}

describe("Ashby save queue", () => {
  test("Ashby save race: with an unsynchronised whole-form write the server's copy holds all three answers and the saves were sent one at a time", async () => {
    mount("");
    const page = mountAshby({
      questions: [
        { path: "q1", text: "Are you authorized to work in the US?" },
        { path: "q2", text: "Do you require visa sponsorship?" },
        { path: "q3", text: "Are you at least 18 years old?" },
      ],
      latency: (n) => 60 - n * 20,
    });
    installHelper(win, NONCE, "s1");
    const host = fakeHost({
      "are you authorized to work in the us?": { outcome: "filled", value: "Yes" },
      "do you require visa sponsorship?": { outcome: "filled", value: "No" },
      "are you at least 18 years old?": { outcome: "filled", value: "Yes" },
    });
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(host);
    engine.useHelper(NONCE);
    const s = summaryOf(await runFill(engine));
    expect(page.server).toEqual({ q1: "Yes", q2: "No", q3: "Yes" });
    expect(page.maxInflight()).toBe(1);
    expect(s.rows.filter((r) => r.outcome === "filled")).toHaveLength(3);
  });

  test("The wait for a text save the echo lacks ends with the debounce window, however long each drain takes, and at once when the run is stopped", async () => {
    mount(`<div data-field-path="q1"><label for="t">Why us?</label><input id="t" value="Because"></div>`);
    let drains = 0;
    const main = {
      request: async (op: string) => {
        if (op === "queue.echo") return { seen: true, held: {}, required: {} };
        drains++;
        await sleep(40);
        return {};
      },
    } as unknown as MainWorld;
    const base = { ...ctx({ ashbyDebounceMs: 100, pollMs: 2, ashbyGraceMs: 1 }), site: "ashby", main };
    expect(await ashbyHolds(base, document.getElementById("t") as Element)).toBe(false);
    // One drain after the write, then as many as fit in the window: a stuck save holds each for its whole length.
    expect(drains).toBeLessThanOrEqual(4);
    const halting = { ...base, checkpoint: async () => { throw new StopSignal("cancelled"); } };
    await expect(ashbyHolds(halting, document.getElementById("t") as Element)).rejects.toBeInstanceOf(StopSignal);
  });

  test("Lost Ashby answer: an echo that still lacks a written Yes after one repeat fails the row with ashby-save-lost", async () => {
    mount("");
    mountAshby({
      questions: [
        { path: "q1", text: "Are you authorized to work in the US?" },
        { path: "q2", text: "Do you require visa sponsorship?" },
      ],
      dropValue: "Yes",
    });
    installHelper(win, NONCE, "s1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" }, "do you require visa sponsorship?": { outcome: "filled", value: "No" } }));
    engine.useHelper(NONCE);
    const s = summaryOf(await runFill(engine));
    expect(s.rows.find((r) => r.label.startsWith("Are you authorized"))).toMatchObject({ outcome: "failed", reason: "ashby-save-lost" });
    expect(s.rows.find((r) => r.label.startsWith("Do you require"))?.outcome).toBe("filled");
  });

  // UiPath (Ashby, live): the SMS and WhatsApp consent radios under the phone box failed as ashby-save-lost while the saved phone
  // was empty; Abridge's single text-message consent is named communicationConsent.
  test.each([
    ["smsConsent", "SMS"],
    ["communicationConsent", "Text messages"],
  ])("Ashby texting consent (%s): the radios under the phone box are saved by a request of their own, so a written consent is no lost save of the phone's copy", async (name, label) => {
    mount("");
    mountAshby({ questions: [{ path: "q1", text: "Are you authorized to work in the US?" }, { path: "phone", text: "Phone number", textbox: true }] });
    document.querySelector('[data-field-path="phone"]')?.insertAdjacentHTML(
      "beforeend",
      `<ul><li><p>${label}</p><div><label><input type="radio" name="${name}" value="given"><div><p><strong>Yes</strong> - I consent to receiving text messages</p></div></label>` +
        `<label><input type="radio" name="${name}" value="notGiven"><div><p><strong>No</strong> - I do not consent to receiving text messages</p></div></label></div></li></ul>`,
    );
    installHelper(win, NONCE, "s1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(
      fakeHost({
        "are you authorized to work in the us?": { outcome: "filled", value: "Yes" },
        [label.toLowerCase()]: { outcome: "filled", value: "Yes - I consent to receiving text messages", ownAnswer: true },
      }),
    );
    engine.useHelper(NONCE);
    const s = summaryOf(await runFill(engine));
    expect(s.rows.find((r) => r.label === label)).toMatchObject({ outcome: "filled" });
    expect((document.querySelector(`input[name="${name}"][value="given"]`) as HTMLInputElement).checked).toBe(true);
  });

  test.each([
    ["a blur sends the debounced save at once", { textDebounceMs: 5000, blurFlushes: true }],
    ["the page's own debounce is waited for when a blur does not send it", { textDebounceMs: 60, blurFlushes: false }],
  ])("Debounced text answer: %s, so a written text box is not a lost save", async (_name, page) => {
    mount("");
    const ashby = mountAshby({ questions: [{ path: "q1", text: "Are you authorized to work in the US?" }, { path: "title", text: "Current job title?", required: true, textbox: true }], ...page });
    installHelper(win, NONCE, "s1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" }, "current job title?": { outcome: "filled", value: "Senior Platform Engineer" } }));
    engine.useHelper(NONCE);
    const s = summaryOf(await runFill(engine));
    expect(s.rows.find((r) => r.label.startsWith("Current job title"))).toMatchObject({ outcome: "filled" });
    expect(ashby.server).toEqual({ q1: "Yes", title: "Senior Platform Engineer" });
  });

  test("Ordinary fill: a pressed Yes the save echo lacks is cleared and pressed again, never the other option", async () => {
    mount("");
    const page = mountAshby({ questions: [{ path: "q1", text: "Are you authorized to work in the US?", required: true }], dropOnce: ["q1"], dropTimes: 1 });
    let pressedNo = 0;
    (document.querySelector('[data-field-path="q1"] button:last-child') as HTMLElement).addEventListener("click", () => pressedNo++);
    installHelper(win, NONCE, "s1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" } }));
    engine.useHelper(NONCE);
    const s = summaryOf(await runFill(engine));
    expect(s.rows.find((r) => r.label.startsWith("Are you authorized"))?.outcome).toBe("filled");
    expect(page.server.q1).toBe("Yes");
    expect(pressedNo).toBe(0);
  });

  test("Submit repair: a Yes the server still lacks after the fill is cleared and pressed again on the repair round, and the other option is never pressed", async () => {
    mount("");
    // The server loses the fill's Yes, the fill's own re-press and the Yes that q2's whole-form save carries.
    const page = mountAshby({ questions: [{ path: "q1", text: "Are you authorized to work in the US?", required: true }, { path: "q2", text: "Do you require visa sponsorship?", required: true }], dropOnce: ["q1"], dropTimes: 3 });
    installHelper(win, NONCE, "s1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" }, "do you require visa sponsorship?": { outcome: "filled", value: "No" } }));
    engine.useHelper(NONCE);
    const s = summaryOf(await runFill(engine));
    // The ordinary fill pressed the Yes again, and the server still lacks it.
    expect(page.server.q1).toBeUndefined();
    expect(s.rows.find((r) => r.label.startsWith("Are you authorized"))?.reason).toBe("ashby-save-lost");
    const noButton = document.querySelector('[data-field-path="q1"] button:last-child') as HTMLElement;
    let pressedNo = 0;
    noButton.addEventListener("click", () => pressedNo++);
    const gaps = await engine.repairSavedForm();
    expect(page.server).toMatchObject({ q1: "Yes", q2: "No" });
    expect(pressedNo).toBe(0);
    expect(gaps).toEqual([]);
  });

  test("The repair round holds a submission its press provokes, and presses nothing while a stopped fill unwinds", async () => {
    mount("");
    const page = mountAshby({ questions: [{ path: "q1", text: "Are you authorized to work in the US?", required: true }], dropOnce: ["q1"], dropTimes: 5 });
    installHelper(win, NONCE, "s1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const host = fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" } });
    const engine = makeEngine(host);
    engine.useHelper(NONCE);
    await runFill(engine);
    expect(page.server.q1).toBeUndefined();
    // The page answers a press of Yes by submitting its form.
    const form = document.body.appendChild(document.createElement("form"));
    let presses = 0;
    let submitted = 0;
    form.addEventListener("submit", () => submitted++);
    (document.querySelector('[data-field-path="q1"] button') as HTMLElement).addEventListener("click", () => {
      presses++;
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    await engine.repairSavedForm();
    expect(presses).toBeGreaterThan(0);
    expect(submitted).toBe(0);
    // A second fill is stopped while its matcher call is still open: its task has not unwound.
    // The page gains an empty question, so this fill has something to ask the matcher.
    document.body.insertAdjacentHTML("beforeend", '<label for="city">City of residence</label><input id="city" />');
    let release = () => {};
    let asked = false;
    const match = host.match.bind(host);
    host.match = async (req) => {
      asked = true;
      await new Promise<void>((r) => (release = r));
      return match(req);
    };
    const started = engine.start({ applicant: ADA });
    await until(() => asked);
    expect(engine.stop(started.runId ?? "")).toBe(true);
    presses = 0;
    await engine.repairSavedForm();
    expect(presses).toBe(0);
    release();
  });

  test("savedFormGaps lists the required questions the saved copy lacks", async () => {
    mount("");
    mountAshby({
      questions: [
        { path: "q1", text: "Are you authorized to work in the US?", required: true },
        { path: "q2", text: "Do you require visa sponsorship?", required: true },
      ],
    });
    installHelper(win, NONCE, "s1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" } }));
    engine.useHelper(NONCE);
    await runFill(engine);
    await until(() => engine.savedFormGaps().length > 0);
    expect(engine.savedFormGaps()).toEqual(["Do you require visa sponsorship?"]);
  });

  test("Echo parsing: sections at any depth up to four, held means a value or an object without a value key, nothing is claimed before an echo", () => {
    const state: EchoState = { seen: false, held: {}, required: {} };
    readEcho({ data: { a: { b: { c: { d: { sections: [{ fieldEntries: [{ path: "x", value: "" }] }] } } } } } }, state);
    expect(state.seen).toBe(false);
    readEcho(
      {
        data: {
          form: {
            sections: [
              { fieldEntries: [{ path: "text", value: "hello" }, { path: "empty", value: "" }, { path: "none", value: null }, { path: "list", value: [] }, { path: "file", value: { name: "cv.pdf" } }, { path: "req", value: null, required: true }, { path: "hiddenReq", value: null, required: true, hidden: true }] },
              { hidden: true, fieldEntries: [{ path: "gone", value: null, required: true }] },
            ],
          },
        },
      },
      state,
    );
    expect(state.seen).toBe(true);
    expect(state.held).toMatchObject({ text: true, empty: false, none: false, list: false, file: true, req: false });
    expect(state.required).toEqual({ req: true });
  });
  test("A required field a later echo hides is not required: a conditional question an answer took away leaves no gap", () => {
    const state: EchoState = { seen: false, held: {}, required: {} };
    const echo = (sections: unknown[]) => readEcho({ data: { form: { sections } } }, state);
    echo([{ fieldEntries: [{ path: "cond", value: null, required: true }, { path: "other", value: null, required: true }] }]);
    expect(state.required).toEqual({ cond: true, other: true });
    echo([{ fieldEntries: [{ path: "cond", value: null, required: true, hidden: true }] }, { hidden: true, fieldEntries: [{ path: "other", value: null, required: true }] }]);
    expect(state.required).toEqual({});
    // Shown again, it is required again.
    echo([{ fieldEntries: [{ path: "cond", value: null, required: true }] }]);
    expect(state.required).toEqual({ cond: true });
  });
});

describe("SuccessFactors uploader hook", () => {
  const b64 = btoa("%PDF-1.4 example");

  test("An uploader input inside an open shadow root is reached by the selector the engine sends for it", async () => {
    mount(`<div id="host"></div>`);
    const root = (document.getElementById("host") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<form id="form_x"><input id="u" type="file" name="fileData"></form>`;
    const input = root.getElementById("u") as HTMLInputElement;
    installHelper(win, NONCE, "h1");
    const mw = new MainWorld(win, NONCE);
    await mw.request("sf.stage", { name: "cv.pdf", type: "application/pdf", base64: b64, ttlMs: 60_000 });
    expect(selectorFor(input)).toContain(SHADOW_SEP);
    expect(await mw.request("sf.attach", { selector: selectorFor(input) })).toEqual({ attached: true });
    expect(input.files?.[0]?.name).toBe("cv.pdf");
    mw.dispose();
  });

  test("The hook is inert unless a document is staged, touches only the system's own uploader inputs, and a stale stage expires", async () => {
    mount(`<form id="form_x"><input id="u" type="file" name="fileData"></form><input id="other" type="file">`);
    installHelper(win, NONCE, "h1");
    const mw = new MainWorld(win, NONCE);
    expect(await mw.request("sf.attach", { selector: "#u" })).toMatchObject({ attached: false, reason: "nothing-staged" });
    await mw.request("sf.stage", { name: "cv.pdf", type: "application/pdf", base64: b64, ttlMs: 60_000 });
    expect(await mw.request("sf.attach", { selector: "#other" })).toMatchObject({ attached: false, reason: "not-an-uploader" });
    expect((document.getElementById("other") as HTMLInputElement).files?.length ?? 0).toBe(0);
    expect(await mw.request("sf.attach", { selector: "#u" })).toEqual({ attached: true });
    expect((document.getElementById("u") as HTMLInputElement).files?.[0]?.name).toBe("cv.pdf");
    await mw.request("sf.stage", { name: "cv.pdf", type: "application/pdf", base64: b64, ttlMs: 5 });
    await sleep(20);
    expect(await mw.request("sf.attach", { selector: "#u" })).toMatchObject({ attached: false, reason: "nothing-staged" });
    mw.dispose();
  });

  test("An application form that is merely named form_something is not the uploader: its file input gets no staged document by a click or by a request", async () => {
    mount(`<form id="form_apply"><input name="first"><input id="v" type="file"></form><form id="form_upload"><input type="hidden" name="token" value="t"><input id="u" type="file"></form>`);
    installHelper(win, NONCE, "h1");
    const mw = new MainWorld(win, NONCE);
    await mw.request("sf.stage", { name: "cv.pdf", type: "application/pdf", base64: b64, ttlMs: 60_000 });
    expect(await mw.request("sf.attach", { selector: "#v" })).toMatchObject({ attached: false, reason: "not-an-uploader" });
    (document.getElementById("v") as HTMLInputElement).click();
    expect((document.getElementById("v") as HTMLInputElement).files?.length ?? 0).toBe(0);
    expect(await mw.request("sf.attach", { selector: "#u" })).toEqual({ attached: true });
    mw.dispose();
  });

  test("The staged document is delivered once: after a click took it, a later click on another uploader input is left to the page", async () => {
    mount(`<input id="u" type="file" name="fileData"><input id="c" type="file" name="fileData2">`);
    installHelper(win, NONCE, "h1");
    const mw = new MainWorld(win, NONCE);
    await mw.request("sf.stage", { name: "cv.pdf", type: "application/pdf", base64: b64, ttlMs: 60_000 });
    (document.getElementById("u") as HTMLInputElement).click();
    expect((document.getElementById("u") as HTMLInputElement).files?.[0]?.name).toBe("cv.pdf");
    (document.getElementById("c") as HTMLInputElement).click();
    expect((document.getElementById("c") as HTMLInputElement).files?.length ?? 0).toBe(0);
    mw.dispose();
  });
});

describe("Page-object calls", () => {
  test("A React element's own handler is called with a synthetic event that has persist()", () => {
    mount(`<input id="pc">`);
    const el = document.getElementById("pc")!;
    let got: { persist?: unknown; target?: unknown } = {};
    (el as unknown as Record<string, unknown>)["__reactProps$abc"] = { onChange: (e: typeof got) => (got = e), onBlur: () => {} };
    expect(callReact(win, "#pc", "onChange", {})).toEqual({ called: true });
    expect(typeof got.persist).toBe("function");
    expect(got.target).toBe(el);
    expect(callReact(win, "#pc", "onKeyDown", {})).toMatchObject({ called: false, reason: "no-handler" });
    expect(callReact(win, "#nope", "onChange", {})).toMatchObject({ called: false });
  });

  test("A component method is called and bounded: a validator that never settles does not hang the write", async () => {
    mount(`<div id="cmp"></div>`);
    const el = document.getElementById("cmp") as unknown as Record<string, unknown>;
    const calls: unknown[] = [];
    el.handleChange = (v: unknown) => {
      calls.push(v);
      return new Promise(() => {});
    };
    const out = await callComponent(win, "#cmp", "handleChange", ["Alex"], 20);
    expect(out).toEqual({ called: true, settled: false });
    expect(calls).toEqual(["Alex"]);
    await expect(callComponent(win, "#cmp", "missing", [], 20)).rejects.toThrow("no-method");
  });
});
