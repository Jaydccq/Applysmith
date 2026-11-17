import { beforeEach, describe, expect, it } from "vitest";
import { HOOK_TAG, installHook } from "../src/mainworld/hook";
import { fakeHookPage } from "./chrome-mock";

let env: ReturnType<typeof fakeHookPage>;
beforeEach(() => {
  env = fakeHookPage();
});

describe("page-world network hook", () => {
  it("returns the very promise the page's own fetch returned, with the same arguments", async () => {
    const pending = Promise.resolve({ status: 201 } as Response);
    env.fetchMock.mockReturnValue(pending);
    const before = env.page.fetch;
    installHook(env.page);
    expect(env.page.fetch).not.toBe(before);
    const got = env.page.fetch("/api/apply?secret=1", { method: "POST", body: "form-data" });
    expect(got).toBe(pending);
    expect(before).toHaveBeenCalledWith("/api/apply?secret=1", { method: "POST", body: "form-data" });
  });

  it("tells the page's own script the method, status and path only, to its own origin", async () => {
    env.fetchMock.mockResolvedValue({ status: 200 } as Response);
    installHook(env.page);
    await env.page.fetch("https://example.com/api/apply?secret=1#x", { method: "post", body: "name=Alex" });
    await Promise.resolve();
    expect(env.posted).toEqual([{ origin: "https://example.com", msg: { tag: HOOK_TAG, method: "POST", status: 200, path: "https://example.com/api/apply" } }]);
    expect(JSON.stringify(env.posted)).not.toMatch(/secret|Alex/);
  });

  it("never throws into page code, never changes a rejection and does not repeat a request", async () => {
    const failure = new Error("network down");
    env.fetchMock.mockRejectedValue(failure);
    env.page.postMessage = () => {
      throw new Error("observer broke");
    };
    installHook(env.page);
    await expect(env.page.fetch("/x", { method: "POST" })).rejects.toBe(failure);
    expect(env.fetchMock).toHaveBeenCalledTimes(1);
    env.fetchMock.mockResolvedValue({ status: 200 } as Response);
    await expect(env.page.fetch("/y")).resolves.toMatchObject({ status: 200 });
  });

  it("reports an XMLHttpRequest after it ends, and passes the original return values through", () => {
    installHook(env.page);
    const xhr = new env.FakeXhr();
    expect(xhr.open("PUT", "/api/applications/9?x=1")).toBe("opened");
    expect(xhr.send("body")).toBe("sent");
    xhr.listeners.loadend!();
    expect(env.posted[0]).toMatchObject({ origin: "https://example.com", msg: { method: "PUT", status: 200, path: "https://example.com/api/applications/9" } });
  });

  it("installs once", () => {
    installHook(env.page);
    const wrapped = env.page.fetch;
    installHook(env.page);
    expect(env.page.fetch).toBe(wrapped);
  });
});
