import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SubmissionEntry } from "../src/shared/messages";
import { clearPending, markPending, pendingCount, settle } from "../src/widgets/drafts";
import { startGuard, type Guard } from "../src/widgets/guard";
import { CONFIRM_MS, DUPLICATE_MS, startRecorder, type Recorder } from "../src/widgets/recorder";

const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
let guard: Guard | undefined;
let recorder: Recorder | undefined;
let pageHandled: number;
let entries: SubmissionEntry[];
let now: number;

const veil = () => roots.filter((r) => r.host.isConnected).map((r) => r.querySelector(".veil")).find(Boolean) as HTMLElement | undefined;
const choose = (label: string) => [...veil()!.querySelectorAll("button")].find((b) => b.textContent === label)!.click();
const submit = () => document.getElementById("submit") as HTMLButtonElement;

/** A click the way a person makes it: a trusted event cannot be built in a test, so the flag is forced. */
function userClick(el: Element): MouseEvent {
  const e = new MouseEvent("click", { bubbles: true, cancelable: true, composed: true });
  Object.defineProperty(e, "isTrusted", { value: true });
  el.dispatchEvent(e);
  return e;
}

beforeEach(() => {
  vi.useFakeTimers();
  roots.length = 0;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL("https://example.com/careers/backend/apply?ref=secret");
  document.title = "Apply - Backend Engineer - Example Corp";
  document.body.innerHTML = `<main><form id="f"><label for="n">Name</label><input id="n" value="Alex Example"><button id="submit" type="submit">Submit application</button></form></main>`;
  pageHandled = 0;
  document.getElementById("submit")!.addEventListener("click", () => pageHandled++);
  document.getElementById("f")!.addEventListener("submit", (e) => e.preventDefault());
  entries = [];
  now = 1_000_000;
  clearPending();
});
afterEach(() => {
  guard?.destroy();
  recorder?.destroy();
  guard = recorder = undefined;
  clearPending();
  Element.prototype.attachShadow = realAttach;
  vi.useRealTimers();
});

describe("submit guard", () => {
  it("holds the person's click while a draft is pending, and the page receives nothing", () => {
    guard = startGuard({ doc: document, win: window });
    markPending("#why");
    const e = userClick(submit());
    expect(pageHandled).toBe(0);
    expect(e.defaultPrevented).toBe(true);
    expect(veil()).toBeDefined();
    expect(veil()!.textContent).toMatch(/Wait/);
    expect(veil()!.textContent).toMatch(/Submit anyway/);
    expect(veil()!.textContent).toMatch(/Skip the pending fields/);
    expect(submit().disabled).toBe(false);
  });

  it("Wait closes the overlay and leaves the page untouched", () => {
    guard = startGuard({ doc: document, win: window });
    markPending("#why");
    userClick(submit());
    choose("Wait");
    expect(veil()).toBeUndefined();
    expect(pageHandled).toBe(0);
    expect(pendingCount()).toBe(1);
  });

  it("Submit anyway delivers the held click exactly once", () => {
    guard = startGuard({ doc: document, win: window });
    markPending("#why");
    userClick(submit());
    choose("Submit anyway");
    expect(pageHandled).toBe(1);
    expect(veil()).toBeUndefined();
  });

  it("Submit anyway submits the form when the control was re-rendered away", () => {
    guard = startGuard({ doc: document, win: window });
    const submitted = vi.fn();
    document.getElementById("f")!.addEventListener("submit", submitted);
    markPending("#why");
    userClick(submit());
    submit().remove();
    (document.getElementById("f") as HTMLFormElement).requestSubmit = () => submitted();
    choose("Submit anyway");
    expect(submitted).toHaveBeenCalledTimes(1);
  });

  it("Skip clears the pending drafts, then allows the click", () => {
    guard = startGuard({ doc: document, win: window });
    markPending("#why");
    markPending("#extra");
    userClick(submit());
    choose("Skip the pending fields");
    expect(pendingCount()).toBe(0);
    expect(pageHandled).toBe(1);
  });

  it("does nothing while no draft is pending, and is removed once none remains", () => {
    guard = startGuard({ doc: document, win: window });
    userClick(submit());
    expect(pageHandled).toBe(1);
    expect(veil()).toBeUndefined();
    markPending("#why");
    expect(guard.isActive()).toBe(true);
    settle("#why");
    expect(guard.isActive()).toBe(false);
    userClick(submit());
    expect(pageHandled).toBe(2);
  });

  it("is removed while the panel is closed", () => {
    guard = startGuard({ doc: document, win: window });
    markPending("#why");
    guard.setPanelOpen(false);
    userClick(submit());
    expect(pageHandled).toBe(1);
  });

  it("ignores clicks that are not the person's own", () => {
    guard = startGuard({ doc: document, win: window });
    markPending("#why");
    submit().click();
    expect(pageHandled).toBe(1);
    expect(veil()).toBeUndefined();
  });
});

describe("submission recorder", () => {
  const start = (record = async (e: SubmissionEntry) => void entries.push(e)) => {
    recorder = startRecorder({ doc: document, win: window, record, now: () => now });
  };

  it("sends one unconfirmed entry for the person's click, with no query, no hash and no form value", async () => {
    start();
    const e = userClick(submit());
    expect(e.defaultPrevented).toBe(false);
    expect(pageHandled).toBe(1);
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({ url: "https://example.com/careers/backend/apply", host: "example.com", confirmed: false, role: "Apply", company: "Example Corp" });
    expect(JSON.stringify(entries)).not.toMatch(/secret|Alex|Name/);
    expect(entries[0]!.entryId).toMatch(/^[0-9a-f-]{36}$/);
  });

  const write = (path: string, origin = "https://example.com") =>
    window.dispatchEvent(new MessageEvent("message", { data: { tag: "applysmith-net", method: "POST", status: 200, path }, origin, source: window }));

  it("confirms the entry when the page's own call to the submit endpoint returns 2xx and the form is gone", async () => {
    start();
    userClick(submit());
    write("https://example.com/api/apply");
    expect(entries.map((e) => e.confirmed)).toEqual([false]);
    document.getElementById("f")!.remove();
    vi.advanceTimersByTime(600);
    expect(entries.map((e) => [e.entryId === entries[0]!.entryId, e.confirmed])).toEqual([[true, false], [true, true]]);
  });

  it("does not confirm a 2xx write while the submit control is still on the page: a bounced submit answers 2xx too", () => {
    start();
    userClick(submit());
    write("https://example.com/api/apply");
    vi.advanceTimersByTime(CONFIRM_MS + 600);
    expect(entries.map((e) => e.confirmed)).toEqual([false]);
  });

  it("reads the submission words in the path, never in the host name", () => {
    (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL("https://jobs.example-apply.com/acme/123");
    start();
    userClick(submit());
    write("https://jobs.example-apply.com/v0/autosave", "https://jobs.example-apply.com");
    document.getElementById("f")!.remove();
    vi.advanceTimersByTime(CONFIRM_MS + 600);
    expect(entries.map((e) => e.confirmed)).toEqual([false]);
  });

  it("ignores a failed call, a GET, another origin's message and a message after 20 s", () => {
    start();
    userClick(submit());
    const send = (data: object, origin = "https://example.com") => window.dispatchEvent(new MessageEvent("message", { data: { tag: "applysmith-net", path: "https://example.com/api/apply", ...data }, origin, source: window }));
    send({ method: "POST", status: 500 });
    send({ method: "GET", status: 200 });
    send({ method: "POST", status: 200 }, "https://evil.example.org");
    vi.advanceTimersByTime(CONFIRM_MS + 1);
    now += CONFIRM_MS + 1;
    send({ method: "POST", status: 200 });
    expect(entries).toHaveLength(1);
  });

  it("confirms on a thank-you phrase that was not there before the click", () => {
    start();
    userClick(submit());
    document.querySelector("main")!.insertAdjacentText("beforeend", "Thank you for applying!");
    vi.advanceTimersByTime(600);
    expect(entries.map((e) => e.confirmed)).toEqual([false, true]);
  });

  it("does not confirm on a phrase the page already showed", () => {
    document.querySelector("main")!.insertAdjacentText("beforeend", "Thank you for applying to other roles.");
    start();
    userClick(submit());
    vi.advanceTimersByTime(CONFIRM_MS + 600);
    expect(entries.map((e) => e.confirmed)).toEqual([false]);
  });

  it("ignores a second click on the same posting within 30 s", () => {
    start();
    userClick(submit());
    now += 5_000;
    userClick(submit());
    expect(entries).toHaveLength(1);
    now += DUPLICATE_MS;
    userClick(submit());
    expect(entries.filter((e) => !e.confirmed)).toHaveLength(2);
  });

  it("ignores a click a script on the page dispatches", () => {
    start();
    submit().click();
    submit().dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(entries).toHaveLength(0);
  });

  it("does not record the click the guard holds, and records the one it lets through, once", () => {
    start();
    guard = startGuard({ doc: document, win: window, onRelease: (el) => recorder!.noteReleased(el) });
    markPending("#why");
    userClick(submit());
    choose("Wait");
    expect(entries).toHaveLength(0);
    userClick(submit());
    choose("Submit anyway");
    expect(entries.filter((e) => !e.confirmed)).toHaveLength(1);
    expect(pageHandled).toBe(1);
  });

  it("a failing record never reaches the page", () => {
    start(async () => {
      throw new Error("offline");
    });
    expect(() => userClick(submit())).not.toThrow();
    expect(pageHandled).toBe(1);
  });
});
