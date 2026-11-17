import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BINDINGS, type InspectRow } from "../../engine/src/protocol";

const send = vi.fn();
vi.mock("../src/shared/rpc", async (original) => ({ ...await original<typeof import("../src/shared/rpc")>(), send: (r: unknown) => send(r) }));

let rows: InspectRow[];

async function load() {
  vi.resetModules();
  await import("../src/bridge");
}

function change(el: Element, trusted = true) {
  const ev = new Event("change", { bubbles: true });
  Object.defineProperty(ev, "isTrusted", { value: trusted });
  el.dispatchEvent(ev);
}

const edits = () => send.mock.calls.map((c) => c[0] as { kind: string; edit?: Record<string, unknown> }).filter((r) => r.kind === "engine.edit");

beforeEach(async () => {
  vi.useFakeTimers();
  vi.stubGlobal("chrome", { runtime: { id: "fixture" } });
  send.mockReset();
  send.mockResolvedValue("{}");
  rows = [];
  (globalThis as Record<string, unknown>).__applysmithEngine = { inspect: () => rows };
  document.body.innerHTML = `
    <input id="name" type="text" name="preferred">
    <input id="pw" type="password" name="password">
    <input id="otp" type="text" autocomplete="one-time-code" name="code">
    <input id="gender" type="text" name="gender">
    <select id="office"><option>Lisbon</option><option>Berlin</option></select>
    <input id="file" type="file">`;
  await load();
});
afterEach(() => {
  const g = globalThis as Record<string, unknown>;
  (g.__ajBridge as () => void)();
  delete g.__ajBridge;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  delete (globalThis as Record<string, unknown>).__applysmithEngine;
});

describe("bridge bindings", () => {
  it.each([
    ["Extension context invalidated.", true],
    ["The message port closed before a response was received.", false],
  ])("classifies the native transport failure %s", async (message, invalidated) => {
    const transport = await vi.importActual<typeof import("../src/shared/rpc")>("../src/shared/rpc");
    vi.stubGlobal("chrome", { runtime: { id: "fixture", sendMessage: vi.fn().mockRejectedValue(new Error(message)) } });
    await expect(transport.send({ kind: "health" })).rejects.toMatchObject({ invalidated, failure: { code: "unavailable" } });
  });

  it("stops before a disconnected matcher can fall through to profile writes", async () => {
    const { WorkerDisconnected } = await import("../src/shared/rpc");
    const stop = vi.fn();
    (globalThis as Record<string, unknown>).__applysmithEngine = { stop };
    send.mockRejectedValue(new WorkerDisconnected({ code: "unavailable", message: "Extension context invalidated." }, true));
    const g = globalThis as unknown as Record<string, (p: string) => Promise<string>>;
    await g[BINDINGS.match]!(JSON.stringify({ runId: "run_fixture" }));
    expect(stop).toHaveBeenCalledWith("run_fixture", "cancelled");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("retries once after worker suspension without cancelling the fill", async () => {
    const { WorkerDisconnected } = await import("../src/shared/rpc");
    const stop = vi.fn();
    (globalThis as Record<string, unknown>).__applysmithEngine = { stop };
    send.mockRejectedValueOnce(new WorkerDisconnected({ code: "unavailable", message: "port closed" }, false));
    send.mockResolvedValueOnce('{"ok":true,"result":{"results":[]}}');
    const g = globalThis as unknown as Record<string, (p: string) => Promise<string>>;
    expect(JSON.parse(await g[BINDINGS.match]!(JSON.stringify({ runId: "run_fixture" }))).ok).toBe(true);
    expect(send).toHaveBeenCalledTimes(2);
    expect(stop).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("cancels after two disconnected ports even while runtime.id is still present", async () => {
    const { WorkerDisconnected } = await import("../src/shared/rpc");
    const stop = vi.fn();
    (globalThis as Record<string, unknown>).__applysmithEngine = { stop };
    send.mockRejectedValue(new WorkerDisconnected({ code: "unavailable", message: "port closed" }, false));
    const g = globalThis as unknown as Record<string, (p: string) => Promise<string>>;
    expect(JSON.parse(await g[BINDINGS.match]!(JSON.stringify({ runId: "run_fixture" }))).ok).toBe(false);
    expect(send).toHaveBeenCalledTimes(2);
    expect(stop).toHaveBeenCalledExactlyOnceWith("run_fixture", "cancelled");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("stops an invalidated pending message without waiting for its reply", async () => {
    const stop = vi.fn();
    (globalThis as Record<string, unknown>).__applysmithEngine = { stop };
    send.mockReturnValue(new Promise(() => {}));
    const g = globalThis as unknown as Record<string, (p: string) => Promise<string>>;
    const answer = g[BINDINGS.match]!(JSON.stringify({ runId: "run_fixture" }));
    vi.stubGlobal("chrome", { runtime: {} });
    await vi.advanceTimersByTimeAsync(250);
    expect(JSON.parse(await answer).ok).toBe(false);
    expect(stop).toHaveBeenCalledWith("run_fixture", "cancelled");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("relays the three engine bindings to the worker", async () => {
    send.mockResolvedValue('{"ok":true,"result":{"results":[]}}');
    const g = globalThis as unknown as Record<string, (p: string) => Promise<string>>;
    expect(await g[BINDINGS.match]!("{}")).toBe('{"ok":true,"result":{"results":[]}}');
    expect(send).toHaveBeenCalledWith({ kind: "engine.match", payload: "{}" });
    await g[BINDINGS.resume]!("{}");
    await g[BINDINGS.terminal]!("{}");
    expect(send.mock.calls.map((c) => (c[0] as { kind: string }).kind)).toEqual(["engine.match", "engine.resume", "engine.terminal"]);
  });

  it("answers with an error reply when the worker is gone", async () => {
    send.mockRejectedValue(new Error("gone"));
    const g = globalThis as unknown as Record<string, (p: string) => Promise<string>>;
    expect(JSON.parse(await g[BINDINGS.match]!("{}"))).toEqual({ ok: false, error: "gone" });
  });
});

describe("what the user commits", () => {
  it("reports a trusted change with the engine's label, value, section and options, the section masked as the engine masks it", async () => {
    const name = document.getElementById("name") as HTMLInputElement;
    const office = document.getElementById("office") as HTMLSelectElement;
    name.value = "Lex";
    office.value = "Berlin";
    rows = [
      { selector: "#name", label: "Preferred name", section: ["Personal information", "Questions? Write to hr@example.com"], kind: "text", required: false, value: "Lex" },
      { selector: "#office", label: "Preferred office", section: [], kind: "select", required: false, value: "Berlin", options: ["Lisbon", "Berlin"] },
    ];
    change(name);
    change(office);
    await vi.advanceTimersByTimeAsync(500);
    expect(edits().map((e) => e.edit)).toEqual([
      { selector: "#name", label: "Preferred name", kind: "text", value: "Lex", inputType: "text", section: ["Personal information", "Questions? Write to [email]"] },
      { selector: "#office", label: "Preferred office", kind: "select", value: "Berlin", options: ["Lisbon", "Berlin"] },
    ]);
  });

  it("ignores a change the page or the fill made (not trusted)", async () => {
    const el = document.getElementById("name") as HTMLInputElement;
    rows = [{ selector: "#name", label: "Preferred name", section: [], kind: "text", required: false, value: "Lex" }];
    change(el, false);
    await vi.advanceTimersByTimeAsync(500);
    expect(edits()).toHaveLength(0);
  });

  it("never reports a password, a one-time code or a file", async () => {
    // The engine knows all three controls, so only the exclusion of the kind itself keeps them out.
    rows = ["pw", "otp", "file"].map((id) => ({ selector: `#${id}`, label: "Anything", section: [], kind: "text", required: false, value: "secret-value" }));
    for (const id of ["pw", "otp", "file"]) change(document.getElementById(id) as HTMLInputElement);
    await vi.advanceTimersByTimeAsync(500);
    expect(edits()).toHaveLength(0);
    expect(JSON.stringify(send.mock.calls)).not.toContain("secret-value");
  });

  it("never reports a withheld self-identification value", async () => {
    const el = document.getElementById("gender") as HTMLInputElement;
    el.value = "Woman";
    // The engine leaves the value off a withheld control.
    rows = [{ selector: "#gender", label: "Gender", section: [], kind: "text", required: false }];
    change(el);
    await vi.advanceTimersByTimeAsync(500);
    expect(edits()).toHaveLength(0);
    expect(JSON.stringify(send.mock.calls)).not.toContain("Woman");
  });

  it("reports nothing for a control the engine does not know", async () => {
    const el = document.getElementById("name") as HTMLInputElement;
    rows = [];
    change(el);
    await vi.advanceTimersByTimeAsync(500);
    expect(edits()).toHaveLength(0);
  });
});
