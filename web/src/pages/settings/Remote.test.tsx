import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { fakeApi, ok } from "../../testing/api";
import { button, byLabel, click, mount, q, qa, text, waitFor } from "../../testing/dom";
import { RemoteSection } from "./Remote";

const NOW = Date.parse("2026-10-12T10:00:00Z");

const off = { mode: "off", listeners: [{ name: "primary", address: "127.0.0.1:47319" }], tunnel: { state: "off", publicUrl: "", version: "", consecutiveFailures: 0 }, allowScreenshots: false, devices: 0, connected: 0 };
const running = {
  mode: "tunnel",
  listeners: [{ name: "primary", address: "127.0.0.1:47319" }, { name: "tunnel", address: "127.0.0.1:47321" }],
  tunnel: { state: "running", publicUrl: "https://applysmith.example.com", version: "2026.1.0", consecutiveFailures: 0 },
  allowScreenshots: false,
  devices: 1,
  connected: 1,
};
const phone = { id: "dev_1", label: "Alex's phone", createdAt: "2026-10-10T09:00:00.000Z", lastSeenAt: "2026-10-12T09:59:30.000Z", network: "tunnel", connected: true };
const offer = {
  link: "https://applysmith.example.com/pair#Zm9vYmFyYmF6cXV4MTIzNA",
  expiresAt: new Date(NOW + 300_000).toISOString(),
  qr: { size: 5, rows: ["10101", "01010", "10101", "01010", "10101"] },
};

function server(status: Record<string, unknown> = off, devices: unknown[] = []) {
  let current = status;
  let list = devices;
  const api = fakeApi({
    "GET /api/remote/status": () => ok(current),
    "GET /api/remote/devices": () => ok({ devices: list }),
    "GET /api/remote/events": () => ok({ events: [{ at: "2026-10-12T09:00:00.000Z", kind: "pair_claim", deviceId: "dev_1", source: "198.51.100.1", detail: "paired Alex's phone" }] }),
    "GET /api/config/policy": () => ok({ file: "policy", exists: true, template: false, content: "", revision: "rev1", issues: [] }),
    "PATCH /api/config/policy": (call) => {
      const mode = (call.body as { ops: { value: string }[] }).ops[0].value;
      current = mode === "tunnel" ? running : off;
      return ok({ revision: "rev2", changed: true });
    },
    "POST /api/remote/offers": () => ok(offer),
    "POST /api/remote/retry": () => ok(current),
    "DELETE /api/remote/devices/dev_1": () => {
      list = [];
      return ok({ revoked: 1 });
    },
    "POST /api/remote/devices/revoke-all": () => {
      list = [];
      return ok({ revoked: 1 });
    },
  });
  return api;
}

describe("Settings: Remote access", () => {
  it("states what becomes reachable and writes the mode only after the confirmation", async () => {
    const api = server();
    await mount(<RemoteSection />);
    await click(await waitFor(() => button("Turn remote access on")));
    await waitFor(() => q("dialog"));
    const dialog = q("dialog")!.textContent ?? "";
    expect(dialog).toContain("reachable from the internet");
    expect(dialog).toContain("cannot change settings, read secrets, confirm a submission or apply");
    expect(api.callsTo("PATCH /api/config/policy")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Turn on")!);
    await waitFor(() => api.callsTo("PATCH /api/config/policy").length === 1);
    expect(api.callsTo("PATCH /api/config/policy")[0].body).toEqual({ expectedRevision: "rev1", ops: [{ op: "set", path: "remote_access.mode", value: "tunnel" }] });
    await waitFor(() => text().includes("reachable from the internet through an HTTPS tunnel and protected by device credentials"));
  });

  it("turns remote access off without a dialog", async () => {
    const api = server(running, [phone]);
    await mount(<RemoteSection />);
    await click(await waitFor(() => button("Turn remote access off")));
    await waitFor(() => api.callsTo("PATCH /api/config/policy").length === 1);
    expect(api.callsTo("PATCH /api/config/policy")[0].body).toEqual({ expectedRevision: "rev1", ops: [{ op: "set", path: "remote_access.mode", value: "off" }] });
    expect(q("dialog")).toBeNull();
  });

  it("draws the QR code locally with a countdown and the link text, and drops it when it expires", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"], now: NOW });
    const api = server(running);
    await mount(<RemoteSection />);
    await click(await waitFor(() => button("Pair a device")));
    await waitFor(() => api.callsTo("POST /api/remote/offers").length === 1);
    const svg = await waitFor(() => q("svg[role=img]"));
    expect(svg.getAttribute("aria-label")).toBe("QR code of the pairing link");
    expect(svg.getAttribute("viewBox")).toBe("0 0 13 13");
    expect(svg.querySelector("path")?.getAttribute("d")).toContain("M4 4h1v1h-1z");
    expect(q(".pairing-link")?.textContent).toBe(offer.link);
    expect(q("[role=timer]")?.textContent).toMatch(/Expires in 300 s/);
    expect(document.querySelector("img")).toBeNull(); // no external image or service
    await act(async () => void vi.advanceTimersByTime(301_000));
    expect(q("svg[role=img]")).toBeNull();
    expect(text()).toContain("The offer expired.");
  });

  it("lists devices with their connected flag and revokes one after a confirmation", async () => {
    const api = server(running, [phone]);
    await mount(<RemoteSection />);
    await waitFor(() => text().includes("Alex's phone"));
    expect(text()).toContain("connected");
    await click(byLabel(/Revoke Alex's phone/));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/remote/devices/dev_1")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Revoke")!);
    await waitFor(() => api.callsTo("DELETE /api/remote/devices/dev_1").length === 1);
    await waitFor(() => text().includes("No devices are paired."));
  });

  it("revokes every device through its own route", async () => {
    const api = server(running, [phone]);
    await mount(<RemoteSection />);
    await click(await waitFor(() => button("Revoke all devices")));
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Revoke")!);
    await waitFor(() => api.callsTo("POST /api/remote/devices/revoke-all").length === 1);
  });
});
