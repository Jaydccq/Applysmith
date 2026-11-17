import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { formControlCount } from "../src/shared/site";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({ send: (r: unknown) => send(r) }));

async function runLoader(url: string, off = false) {
  send.mockReset();
  send.mockImplementation(async (r: { kind: string }) => (r.kind === "site.get" ? { off } : { injected: true }));
  window.history.replaceState(null, "", url);
  vi.resetModules();
  await import("../src/loader");
  await vi.advanceTimersByTimeAsync(0);
}

const injected = () => send.mock.calls.some((c) => (c[0] as { kind: string }).kind === "loader.inject");

beforeEach(() => {
  vi.useFakeTimers();
  document.body.innerHTML = "";
});
afterEach(() => vi.useRealTimers());

describe("form probe", () => {
  it("ignores a search box and a newsletter field", () => {
    document.body.innerHTML = `<form role="search"><input type="search" name="q"></form><form><input type="email" name="newsletter-email"></form>`;
    expect(formControlCount(document)).toBeLessThan(2);
  });
});

describe("loader", () => {
  it("injects nothing and keeps quiet on an ordinary page", async () => {
    document.body.innerHTML = `<form role="search"><input type="search" name="q"></form><form><input name="newsletter"></form>`;
    await runLoader("/news/story");
    await vi.advanceTimersByTimeAsync(31_000);
    expect(injected()).toBe(false);
    expect(send.mock.calls.every((c) => (c[0] as { kind: string }).kind === "site.get")).toBe(true);
  });

  it("asks for the panel on an address that reads as an application", async () => {
    await runLoader("/");
    expect(injected()).toBe(false);
    send.mockClear();
    await runLoader("/careers/apply/123");
    expect(injected()).toBe(true);
  });

  it("does nothing on a host that is switched off", async () => {
    document.body.innerHTML = `<form><input name="a"><input name="b"></form>`;
    await runLoader("/careers/apply", true);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(injected()).toBe(false);
  });
});
