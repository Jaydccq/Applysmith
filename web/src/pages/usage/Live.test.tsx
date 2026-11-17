import { act, StrictMode } from "react";
import { describe, expect, it, vi } from "vitest";
import { fakeApi, ok } from "../../testing/api";
import { button, mount, qa, text, waitFor } from "../../testing/dom";
import type { UsageCall } from "../../wire";
import { Live } from "./Live";

const attempt = (id: number, callId: string, n: number, extra: Partial<UsageCall> = {}): UsageCall => ({
  id, callId, startedAt: "2026-10-23T10:00:00Z", task: "scan.rank", provider: "deepseek", model: "flash", attempt: n, outcome: "ok",
  inputTokens: 100, outputTokens: 10, cachedTokens: 0, latencyMs: 500, downgraded: false, ...extra,
});

describe("Live", () => {
  it("reads the newest calls once, then asks only for what follows the last id, and stops when paused", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    let rows: UsageCall[] = [attempt(2, "b", 1, { runId: "run-1" }), attempt(1, "a", 1, { outcome: "overloaded" })];
    const api = fakeApi({
      "GET /api/llm/calls": (c) => ok({ calls: c.query.has("after") ? rows.filter((r) => r.id > Number(c.query.get("after"))) : rows, total: 2 }),
    });
    await mount(<Live onTrace={() => {}} />);
    await waitFor(() => qa("tbody tr").length === 2);
    expect(api.callsTo("GET /api/llm/calls").map((c) => [c.query.get("after"), c.query.get("pageSize")])).toEqual([[null, "100"]]);

    rows = [attempt(3, "a", 2, { provider: "openrouter", model: "x", reason: "fallback", ttftMs: 250 }), ...rows];
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    await waitFor(() => text().includes("openrouter/x"));
    expect(api.callsTo("GET /api/llm/calls")[1].query.get("after")).toBe("2");
    expect(qa("tbody tr")).toHaveLength(2);
    expect(qa("tbody tr")[0].textContent).toContain("deepseek/flash overloaded → openrouter/x okfallback after an error");

    await act(async () => {
      button("Pause")?.click();
    });
    const asked = api.callsTo("GET /api/llm/calls").length;
    await act(async () => {
      vi.advanceTimersByTime(6000);
    });
    expect(api.callsTo("GET /api/llm/calls")).toHaveLength(asked);
  });

  // Strict mode mounts, unmounts and mounts again in development.
  it("shows the calls after a second mount of the same panel", async () => {
    fakeApi({ "GET /api/llm/calls": () => ok({ calls: [attempt(1, "a", 1)], total: 1 }) });
    await mount(<StrictMode><Live onTrace={() => {}} /></StrictMode>);
    await waitFor(() => qa("tbody tr").length === 1);
  });
});
