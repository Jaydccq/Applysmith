import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WINDOW_MS, findTargets, openAuthorizationWindow, startOtp, type CodeMail, type Otp } from "../src/widgets/otp";
import { watchPage } from "../src/widgets/main";

const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
let widget: Otp | undefined;
let latest: ReturnType<typeof vi.fn>;
let t: number;

/** Waits on a condition; the cap only ends a wait that can never succeed, so it outlasts a loaded machine. */
const settled = <T>(check: () => T) => vi.waitFor(check, { timeout: 15_000 });
const layers = () => roots.filter((r) => r.host.isConnected);
const fillButton = () => layers().map((r) => r.querySelector<HTMLButtonElement>(".btn")).find(Boolean)!;
const pop = () => layers().map((r) => r.querySelector<HTMLElement>(".pop")).find(Boolean);
const mailAt = (code: string): CodeMail => ({ code, receivedAt: "2026-10-11T09:30:00.000Z", from: "no-reply@mail.example.com" });

function start() {
  widget = startOtp({ doc: document, win: window, changes: watchPage(document), latest: latest as never, now: () => t, sleep: async (ms) => void (t += ms) });
}

beforeEach(() => {
  roots.length = 0;
  t = 1_000_000;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  latest = vi.fn(async () => null);
  document.body.innerHTML = `<form><label for="code">Verification code</label><input id="code" type="text" autocomplete="one-time-code" value="old"></form>`;
});
afterEach(() => {
  widget?.destroy();
  widget = undefined;
  Element.prototype.attachShadow = realAttach;
  openAuthorizationWindow(-WINDOW_MS - 1);
});

describe("one-time-code button", () => {
  it("never reads mail on load: the button waits for a click", async () => {
    start();
    await new Promise((r) => setTimeout(r, 30));
    expect(fillButton().textContent).toBe("Fill code from Gmail");
    expect(latest).not.toHaveBeenCalled();
  });

  it("writes a code that arrives later, shows the sender domain and arrival time, and clicks nothing on the page", async () => {
    let calls = 0;
    latest = vi.fn(async () => (++calls < 14 ? null : mailAt("482913")));
    const pageClicks = vi.fn();
    document.addEventListener("click", pageClicks);
    start();
    pageClicks.mockClear();
    fillButton().click();
    await settled(() => expect((document.getElementById("code") as HTMLInputElement).value).toBe("482913"));
    await settled(() => expect(pop()!.textContent).toMatch(/mail\.example\.com/));
    expect(pop()!.textContent).toMatch(/received/);
    expect(pageClicks.mock.calls.filter((c) => (c[0] as Event).target !== document.body && !(c[0] as Event).composedPath().some((n) => (n as Element).hasAttribute?.("data-applysmith"))).length).toBe(0);
    document.removeEventListener("click", pageClicks);
  });

  it("stops reading mail and writes nothing once its field is gone or the widget is destroyed", async () => {
    for (const end of ["field-gone", "destroyed"]) {
      let answer: (m: CodeMail | null) => void = () => {};
      latest = vi.fn(() => new Promise<CodeMail | null>((r) => (answer = r)));
      document.body.innerHTML = `<form><label for="code">Verification code</label><input id="code" type="text" autocomplete="one-time-code" value="old"></form>`;
      const input = document.getElementById("code") as HTMLInputElement;
      start();
      fillButton().click();
      await settled(() => expect(latest).toHaveBeenCalledTimes(1));
      if (end === "field-gone") {
        input.remove();
        widget!.refresh();
      } else widget!.destroy();
      answer(null);
      await new Promise((r) => setTimeout(r, 20));
      answer(mailAt("999000"));
      await new Promise((r) => setTimeout(r, 20));
      expect(latest, end).toHaveBeenCalledTimes(1);
      expect(input.value, end).toBe("old");
      widget?.destroy();
    }
  });

  it("writes one digit per box for a split code, in order", async () => {
    document.body.innerHTML = `<div id="row">${Array.from({ length: 6 }, (_, i) => `<input id="d${i}" maxlength="1" inputmode="numeric">`).join("")}</div>`;
    expect(findTargets(document)).toHaveLength(1);
    expect(findTargets(document)[0]!.inputs).toHaveLength(6);
    latest = vi.fn(async () => mailAt("135790"));
    start();
    fillButton().click();
    await settled(() => expect(pop()!.textContent).toMatch(/Code written/));
    expect(Array.from({ length: 6 }, (_, i) => (document.getElementById(`d${i}`) as HTMLInputElement).value).join("")).toBe("135790");
  });

  it("reads mail without a second click only inside the window a sign-in run opened", async () => {
    latest = vi.fn(async () => mailAt("111222"));
    openAuthorizationWindow(t);
    start();
    await settled(() => expect((document.getElementById("code") as HTMLInputElement).value).toBe("111222"));
    expect(latest).toHaveBeenCalledTimes(1);
  });

  it("does not read mail on its own once the window has closed", async () => {
    openAuthorizationWindow(t - WINDOW_MS - 1);
    start();
    await new Promise((r) => setTimeout(r, 30));
    expect(latest).not.toHaveBeenCalled();
  });

  it("ignores an ordinary text field", () => {
    document.body.innerHTML = `<input id="name" type="text" name="fullname">`;
    expect(findTargets(document)).toHaveLength(0);
  });
});
