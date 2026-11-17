// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Controls the engine never writes or presses (the page's own handlers are called through the
// guard); Injection into an isolated world and re-injection (the nonce stays out of the page; the observer
// never delays a submission); Drivers for application boards (the Ashby queue is scoped to the fill).
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { applyValue } from "../src/apply";
import type { Win } from "../src/dom";
import { MainWorld } from "../src/mainworld-client";
import { HOLDER, installHelper } from "../src/mainworld/helper";
import { UserEdits } from "../src/restore";
import { mountPctySelect, mountRwDropdown, mountAshby, sleep } from "./ats-fixtures";
import { ADA, ctx, fakeHost, infoOf, makeEngine, mount, PAD, runFill, setUrl, uninstall, until } from "./helpers";

const win = window as unknown as Win;
const NONCE = "safety-nonce-4c1f9e";
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

async function writeWithHelper(id: string, value: string) {
  installHelper(win, NONCE, "h1");
  const main = new MainWorld(win, NONCE);
  const c = { ...ctx(), main, site: "paylocity" };
  const r = await applyValue(infoOf(id), { value, replaceDefault: false }, { ctx: c, written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: new UserEdits(document) });
  main.dispose();
  return r;
}

describe("The page's own handlers are called only for controls the guard allows", () => {
  test("a react-widgets option labelled Submit application is refused and its handler never runs", async () => {
    mount(PAD);
    const commits: string[] = [];
    const rw = mountRwDropdown({ id: "trap.dd", label: "Trap", options: ["Submit application", "No"], onCommit: (l) => commits.push(l) });
    const r = await writeWithHelper("trap.dd", "Submit application");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(commits).toEqual([]);
    expect(rw.shown()).toBe("");
  });

  test("a Paylocity select whose leaf option is labelled Next is refused at the leaf handler", async () => {
    mount(PAD);
    const d = mountPctySelect({ id: "workHistory.0.state", label: "State", options: ["Next", "Texas"] });
    const r = await writeWithHelper("workHistory.0.state", "Next");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(d.shown()).toBe("");
    // The same widget still takes an ordinary option.
    uninstall();
    expect(await writeWithHelper("workHistory.0.state", "Texas")).toMatchObject({ outcome: "filled" });
  });

  test("the helper itself refuses react.call and component.call at a control named Submit, Save or Sign in, and calls an ordinary one", async () => {
    mount(`<button id="a" aria-label="Submit application">Go</button><button id="b">Save</button><a id="c" role="button">Sign in</a><button id="d">Add another</button>
      <input id="e" value="Review notes typed by the person"><x-comp id="f" label="Continue"></x-comp>`);
    const called: string[] = [];
    for (const id of ["a", "b", "c", "d", "e"]) (document.getElementById(id) as unknown as Record<string, unknown>)["__reactProps$t"] = { onClick: () => called.push(id) };
    (document.getElementById("f") as unknown as Record<string, unknown>).press = () => called.push("f");
    installHelper(win, NONCE, "h1");
    const main = new MainWorld(win, NONCE);
    const outs = [];
    for (const id of ["a", "b", "c", "d", "e"]) outs.push(await main.request<{ called: boolean; reason?: string }>("react.call", { selector: `#${id}`, prop: "onClick", event: {} }));
    expect(outs.map((o) => o.called)).toEqual([false, false, false, true, true]);
    expect(outs.slice(0, 3).map((o) => o.reason)).toEqual(["unsafe-click", "unsafe-click", "unsafe-click"]);
    await expect(main.request("component.call", { selector: "#f", method: "press", args: [] })).rejects.toThrow("unsafe-click");
    expect(called).toEqual(["d", "e"]);
    main.dispose();
  });

  test("the helper reads a name given through aria-labelledby or a nested aria-label", async () => {
    mount(`<span id="l">Submit application</span><div id="g" role="button" aria-labelledby="l"></div>
      <button id="h"><span aria-label="Next"></span></button><x-comp id="i" aria-labelledby="l"></x-comp><x-comp id="j"><i aria-label="Continue"></i></x-comp>
      <div id="k" role="combobox">Country<div role="listbox"><div role="option" aria-label="Next">Next</div></div></div>`);
    const called: string[] = [];
    for (const id of ["g", "h", "k"]) (document.getElementById(id) as unknown as Record<string, unknown>)["__reactProps$t"] = { onClick: () => called.push(id) };
    for (const id of ["i", "j"]) (document.getElementById(id) as unknown as Record<string, unknown>).press = () => called.push(id);
    installHelper(win, NONCE, "h1");
    const main = new MainWorld(win, NONCE);
    const outs = [];
    for (const id of ["g", "h", "k"]) outs.push(await main.request<{ called: boolean; reason?: string }>("react.call", { selector: `#${id}`, prop: "onClick", event: {} }));
    expect(outs.map((o) => o.reason ?? "called")).toEqual(["unsafe-click", "unsafe-click", "called"]);
    for (const id of ["i", "j"]) await expect(main.request("component.call", { selector: `#${id}`, method: "press", args: [] })).rejects.toThrow("unsafe-click");
    expect(called).toEqual(["k"]);
    main.dispose();
  });
});

describe("The request nonce never reaches the page", () => {
  test("it is in no storage key or value, no attribute or text of the DOM, no global and no event detail, after requests, replies, fetches and a submission", async () => {
    mount(`<form id="f" action="/x"><button type="submit">Go</button></form>`);
    window.fetch = (async () => new Response("{}", { status: 200 })) as typeof fetch;
    const details: string[] = [];
    const orig = document.dispatchEvent.bind(document);
    document.dispatchEvent = (ev: Event) => {
      details.push(JSON.stringify((ev as CustomEvent).detail ?? null));
      return orig(ev);
    };
    installHelper(win, NONCE, "h9");
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    const main = new MainWorld(win, NONCE);
    await main.request("queue.enable", { ops: "ApiSetFormValue" });
    await main.request("queue.disable");
    await window.fetch("https://apply.example.com/submit", { method: "POST" });
    document.getElementById("f")!.dispatchEvent(new Event("submit", { cancelable: true, bubbles: true }));
    expect(engine.submitObservations().length).toBeGreaterThan(0);

    const seen: string[] = [...details, document.documentElement.outerHTML, document.cookie];
    for (const store of [window.sessionStorage, window.localStorage]) {
      for (let i = 0; i < store.length; i++) {
        const k = store.key(i) as string;
        seen.push(k, store.getItem(k) ?? "");
      }
    }
    const scan = (host: object) => {
      for (const k of [...Object.getOwnPropertyNames(host), ...Object.getOwnPropertySymbols(host)]) {
        seen.push(String(k));
        try {
          const v = (host as Record<string | symbol, unknown>)[k];
          if (typeof v === "string") seen.push(v);
          else if (k === HOLDER) seen.push(JSON.stringify(v));
        } catch {
          /* an accessor may throw */
        }
      }
    };
    scan(window);
    scan(document);
    expect(details.length).toBeGreaterThan(0);
    for (const s of seen) expect(s).not.toContain(NONCE);
    expect(Object.keys(window.sessionStorage)).toEqual(["aj.obs"]);
    document.dispatchEvent = orig;
    main.dispose();
    engine.dispose();
  });
});

describe("The Ashby queue is the fill's tool and never holds a submission", () => {
  function hungFetch() {
    const calls: string[] = [];
    window.fetch = ((input: unknown) => {
      calls.push(String(input));
      return String(input).includes("ApiSetFormValue") ? new Promise<Response>(() => {}) : Promise.resolve(new Response("{}", { status: 200 }));
    }) as typeof fetch;
    return calls;
  }

  test("a submit POST goes out at once while a save is hung, even when the host asks the queue to own the submit operation", async () => {
    const calls = hungFetch();
    installHelper(win, NONCE, "h1");
    const main = new MainWorld(win, NONCE);
    await main.request("queue.enable", { ops: "ApiSetFormValue|ApiSubmitSingleApplicationFormAction" });
    void window.fetch("/api/ApiSetFormValue", { method: "POST" });
    const res = await Promise.race([window.fetch("/api/ApiSubmitSingleApplicationFormAction", { method: "POST" }), sleep(100).then(() => null)]);
    expect(res).toBeInstanceOf(Response);
    expect(calls).toContain("/api/ApiSubmitSingleApplicationFormAction");
    main.dispose();
  });

  test("a second save waits behind the first only while the queue is on, and the queue is off after disable", async () => {
    const calls = hungFetch();
    installHelper(win, NONCE, "h1");
    const main = new MainWorld(win, NONCE);
    await main.request("queue.enable", { ops: "ApiSetFormValue" });
    void window.fetch("/api/ApiSetFormValue", { method: "POST" });
    void window.fetch("/api/ApiSetFormValue?second", { method: "POST" });
    await sleep(30);
    expect(calls).toHaveLength(1);
    await main.request("queue.disable");
    void window.fetch("/api/ApiSetFormValue?third", { method: "POST" });
    await sleep(30);
    expect(calls).toHaveLength(2);
    main.dispose();
  });

  test("the queue is on during an Ashby fill and off once it is done: two saves after the fill both leave at once", async () => {
    mount("");
    mountAshby({ questions: [{ path: "q1", text: "Are you authorized to work in the US?" }], latency: () => 5 });
    let hang = false;
    const started: string[] = [];
    const page = window.fetch;
    window.fetch = ((input: unknown, init?: RequestInit) => {
      if (!hang) return page(input as string, init);
      started.push(String(input));
      return new Promise<Response>(() => {});
    }) as typeof fetch;
    installHelper(win, NONCE, "h1");
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const engine = makeEngine(fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" } }));
    engine.useHelper(NONCE);
    await runFill(engine);
    await sleep(20);
    hang = true;
    void window.fetch("/api/ApiSetFormValue", { method: "POST" });
    void window.fetch("/api/ApiSetFormValue?again", { method: "POST" });
    await sleep(30);
    expect(started).toHaveLength(2);
    engine.dispose();
  });

  test("the engine turns the queue off at the end of a fill, a stop and a dispose", async () => {
    setUrl("https://jobs.ashbyhq.com/example/abc/application");
    const sent: string[] = [];
    const real = { request: MainWorld.prototype.request, send: MainWorld.prototype.send };
    MainWorld.prototype.request = function (this: MainWorld, op: string, ...rest: unknown[]) {
      sent.push(op);
      return (real.request as (...a: unknown[]) => Promise<unknown>).call(this, op, ...rest) as never;
    } as typeof real.request;
    MainWorld.prototype.send = function (this: MainWorld, op: string, args?: unknown) {
      sent.push(op);
      real.send.call(this, op, args);
    };
    const QUESTION = { "are you authorized to work in the us?": { outcome: "filled" as const, value: "Yes" } };
    const page = () => {
      mount("");
      mountAshby({ questions: [{ path: "q1", text: "Are you authorized to work in the US?" }], latency: () => 5 });
      installHelper(win, NONCE, "h1");
    };
    try {
      page();
      const engine = makeEngine(fakeHost(QUESTION));
      engine.useHelper(NONCE);
      await runFill(engine);
      await sleep(20);
      expect(sent.indexOf("queue.enable")).toBeGreaterThanOrEqual(0);
      expect(sent.lastIndexOf("queue.disable")).toBeGreaterThan(sent.indexOf("queue.enable"));
      engine.dispose();

      // A stop and a dispose while the run waits on the matcher, the queue on.
      for (const end of ["stop", "dispose"] as const) {
        uninstall();
        page();
        sent.length = 0;
        const host = fakeHost(QUESTION);
        const match = host.match.bind(host);
        let release: (() => void) | undefined;
        host.match = async (req) => (await new Promise<void>((r) => (release = r)), match(req));
        const running = makeEngine(host);
        running.useHelper(NONCE);
        const { runId } = running.start({ applicant: ADA });
        await until(() => release !== undefined);
        expect(sent).toContain("queue.enable");
        if (end === "stop") running.stop(runId as string);
        else running.dispose();
        await until(() => sent.lastIndexOf("queue.disable") > sent.indexOf("queue.enable"));
        release?.();
      }
    } finally {
      Object.assign(MainWorld.prototype, real);
    }
  });
});
