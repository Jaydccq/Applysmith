import { beforeEach, describe, expect, it, vi } from "vitest";
import { dispatch, on } from "../src/sw/router";
import { installChrome } from "./chrome-mock";

beforeEach(() => installChrome());

describe("message trust", () => {
  it("does not run a handler for a foreign sender", async () => {
    const run = vi.fn(async () => "secret");
    on("fill.status", run);
    const r = await dispatch({ kind: "fill.status" }, { id: "other-extension" });
    expect(r).toMatchObject({ ok: false, error: { code: "unauthorized" } });
    expect(run).not.toHaveBeenCalled();
  });

  it("answers the extension's own contexts", async () => {
    on("health", async () => "fine");
    await expect(dispatch({ kind: "health" }, { id: "ext-id" })).resolves.toEqual({ ok: true, data: "fine" });
  });

  it("lets only the popup change pairing and settings", async () => {
    const run = vi.fn(async () => "done");
    on("pair.forget", run, { popupOnly: true });
    const fromPage = await dispatch({ kind: "pair.forget" }, { id: "ext-id", tab: { id: 3 } as chrome.tabs.Tab, url: "https://example.com/apply" });
    expect(fromPage).toMatchObject({ ok: false, error: { code: "unauthorized" } });
    const fromPopup = await dispatch({ kind: "pair.forget" }, { id: "ext-id", url: "chrome-extension://ext-id/popup.html" });
    expect(fromPopup.ok).toBe(true);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("answers an unknown message with an error, not a crash", async () => {
    await expect(dispatch({ kind: "nope" }, { id: "ext-id" })).resolves.toMatchObject({ ok: false });
    await expect(dispatch(null, { id: "ext-id" })).resolves.toMatchObject({ ok: false });
  });
});
