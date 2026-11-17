import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Parity row BRD-14: the auto-open check on a hydrating page.

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({ send: (r: unknown) => send(r) }));

async function runLoader(url: string) {
  send.mockReset();
  send.mockImplementation(async (r: { kind: string }) => (r.kind === "site.get" ? { off: false } : { injected: true }));
  window.history.replaceState(null, "", url);
  vi.resetModules();
  await import("../src/loader");
  await vi.advanceTimersByTimeAsync(0);
}
const injections = () => send.mock.calls.filter((c) => (c[0] as { kind: string }).kind === "loader.inject").length;

beforeEach(() => {
  vi.useFakeTimers();
  document.body.innerHTML = "";
});
afterEach(() => vi.useRealTimers());

describe("loader on a client-rendered page", () => {
  it("re-checks on every burst of DOM change but asks for the panel once, however long the page keeps changing", async () => {
    await runLoader("/page");
    // The page is empty at load and again for a while: the check re-runs after each quiet.
    for (let i = 0; i < 5; i++) {
      document.body.append(document.createElement("div"));
      await vi.advanceTimersByTimeAsync(600);
    }
    expect(injections()).toBe(0);
    document.body.innerHTML = `<form><input name="a"><input name="b"><input name="c"></form>`;
    await vi.advanceTimersByTimeAsync(600);
    expect(injections()).toBe(1);
    // It keeps hydrating: more changes, more controls. The watch ended with the first ask.
    for (let i = 0; i < 5; i++) {
      document.body.append(document.createElement("input"));
      await vi.advanceTimersByTimeAsync(600);
    }
    expect(injections()).toBe(1);
  });
});
