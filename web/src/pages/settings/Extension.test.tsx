import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, click, mount, qa, text, waitFor } from "../../testing/dom";
import { ExtensionSection } from "./Extension";

const BUILD = { stamp: "3f9a0c27d41e8b65", version: "0.1.0" };
const NOW = Date.parse("2030-01-01T00:00:00Z");
const codeBody = (seconds: number) => ({ code: "ABCDEFGHJK", expiresAt: new Date(NOW + seconds * 1000).toISOString() });

// Only the clock is faked: the test helpers poll with setTimeout.
const clock = () => vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"], now: NOW });
const tick = (ms: number) => act(async () => void vi.advanceTimersByTime(ms));

describe("Settings: Browser extension", () => {
  it("requests a code, shows it with a countdown and keeps it out of storage, and drops it when it expires", async () => {
    clock();
    const api = fakeApi({ "GET /api/extension/build": () => ok(BUILD), "POST /api/pairing/codes": () => ok(codeBody(120)) });
    await mount(<ExtensionSection />);
    expect(text()).not.toContain("ABCDEFGHJK");
    await click(button("Pair the extension"));
    await waitFor(() => text().includes("ABCDEFGHJK"));
    expect(api.callsTo("POST /api/pairing/codes")).toHaveLength(1);
    // The code carries the port the extension must reach.
    expect(document.querySelector(".pairing-code")?.textContent).toBe(`${window.location.port}-ABCDEFGHJK`);
    expect(text()).toContain("Expires in 120 s");
    expect(localStorage.length + sessionStorage.length).toBe(0);
    await tick(30_000);
    expect(text()).toContain("Expires in 90 s");
    await tick(90_000);
    expect(text()).not.toContain("ABCDEFGHJK");
    expect(text()).toContain("The code expired. Request a new one.");
    expect(document.body.innerHTML).not.toContain("ABCDEFGHJK");
  });

  // Every paired extension shares one token, so unpairing signs all of them
  // out: it happens only after the dialog is confirmed.
  it("lists each paired install with when it was last seen, and unpairs only after the dialog is confirmed", async () => {
    clock();
    const at = (s: number) => new Date(NOW - s * 1000).toISOString();
    let clients = [{ id: "install-1", version: "0.1.0", build: BUILD.stamp, lastSeen: at(180) }];
    const api = fakeApi({
      "GET /api/extension/build": () => ok(BUILD),
      "GET /api/extension/clients": () => ok({ since: at(7200), clients }),
      "DELETE /api/extension/clients": () => ((clients = []), ok({ since: at(7200), clients })),
    });
    await mount(<ExtensionSection />);
    await waitFor(() => text().includes("last seen 3 min ago (version 0.1.0)"));
    await click(button("Unpair extensions"));
    expect(api.callsTo("DELETE /api/extension/clients")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Unpair")!);
    await waitFor(() => text().includes("Not paired"));
    expect(api.callsTo("DELETE /api/extension/clients")).toHaveLength(1);
  });

  it("shows a refusal with its request id", async () => {
    const api = fakeApi({ "GET /api/extension/build": () => ok(BUILD), "POST /api/pairing/codes": () => fail("forbidden", "Pairing codes are shown only on this computer.") });
    await mount(<ExtensionSection />);
    await click(button("Pair the extension"));
    await waitFor(() => text().includes("Pairing codes are shown only on this computer."));
    expect(text()).toContain("Request ");
    expect(api.callsTo("POST /api/pairing/codes")).toHaveLength(1);
  });
});
