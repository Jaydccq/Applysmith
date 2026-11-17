import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { act } from "react";
import { fakeApi, fail, ok, type Reply } from "./testing/api";
import { button, click, mount, q, text, waitFor } from "./testing/dom";
import { expectAccessible } from "./testing/axe";
import { applyList } from "./testing/fixtures";

const refused = () => fail("unauthenticated", "a valid credential is required");
// held is a board stream the server keeps open without an event.
const held = () => new Promise<Reply>(() => {});

describe("App without a session", () => {
  it("shows the open-from-the-app screen when the board stream is refused, and nothing else", async () => {
    fakeApi({ "GET /api/board/events": refused, "GET /api/apply/list": () => ok(applyList) });
    await mount(<App />);
    await waitFor(() => text().includes("Open the dashboard from the Applysmith app"), "the signed-out screen");
    expect(text()).toContain("applysmith open");
    expect(q("nav[aria-label='Main']")).toBeNull();
  });

  it("shows it when an ordinary request is refused while the stream is not", async () => {
    fakeApi({ "GET /api/apply/list": refused, "GET /api/board/events": held });
    await mount(<App />);
    await waitFor(() => text().includes("Open the dashboard from the Applysmith app"), "the signed-out screen");
  });
});

describe("App without a session inside the desktop app", () => {
  it("offers to reopen itself through the shell, and says so when the shell refuses", async () => {
    const call = vi.fn().mockRejectedValueOnce(new Error("server_not_ready")).mockResolvedValue(undefined);
    vi.stubGlobal("mygo", { call });
    fakeApi({ "GET /api/board/events": refused, "GET /api/apply/list": () => ok(applyList) });
    await mount(<App />);
    await waitFor(() => button("Open the dashboard again"), "the reopen button");
    await expectAccessible();
    await click(button("Open the dashboard again"));
    await waitFor(() => q("[role='alert']")?.textContent === "The server is not running yet. Try again in a moment.", "the refusal");
    await click(button("Open the dashboard again"));
    expect(call).toHaveBeenNthCalledWith(2, "Shell.ReopenDashboard");
    vi.unstubAllGlobals();
  });
});

describe("App after the session ended", () => {
  it("comes back by itself when a new link opened in another tab stores its key", async () => {
    let valid = false;
    fakeApi({ "GET /api/apply/list": () => (valid ? ok(applyList) : refused()), "GET /api/board/events": () => (valid ? held() : refused()) });
    await mount(<App />);
    await waitFor(() => text().includes("comes back by itself"), "the signed-out screen");
    valid = true;
    const key = "k".repeat(32);
    window.localStorage.setItem("applysmith.sessionKey", key);
    await act(async () => window.dispatchEvent(new StorageEvent("storage", { key: "applysmith.sessionKey", newValue: key })));
    await waitFor(() => q("nav[aria-label='Main']"), "the dashboard again");
  });
});
