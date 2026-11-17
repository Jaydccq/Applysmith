import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { mount, q, qa, text, waitFor } from "../testing/dom";
import { routing } from "../testing/fixtures";
import Usage from "./Usage";

const row = (name: string, costUsd: number, calls = 3) => ({ name, calls, successRate: 1, fallbackRate: 0.1, costUsd, unpriced: 0, p50Ms: 900, p95Ms: 2100, rateLimitedRate: 0 });

function server(todayUsd: number, cap = 5) {
  return fakeApi({
    "GET /api/llm/cost": (c) =>
      ok(
        c.query.get("days") === "30"
          ? { days: 30, byTask: [], byProvider: [row("deepseek", 9.25, 20)] }
          : { days: 7, byTask: [row("evaluation.deep", 1.5), row("scan.rank", 0.5), row("option-pick", 0.0012)], byProvider: [{ ...row("deepseek", 1.75, 4), rateLimitedRate: 0.25, cacheHitRate: 0.4 }, row("anthropic", 0.25, 1)] },
      ),
    "GET /api/llm/routing": () => ok({ ...routing, dailyUsdCap: cap, capSpentUsd: todayUsd }),
    "GET /api/llm/cost/daily": () =>
      ok({ days: [{ day: "2026-10-10", costUsd: 0, calls: 0 }, { day: "2026-10-11", costUsd: 0.75, calls: 3 }, { day: "2026-10-12", costUsd: 1.5, calls: 4 }] }),
    "GET /api/llm/calls": (c) => {
      const failedOnly = c.query.get("outcome") === "failed";
      const calls = [
        { id: 3, callId: "c1", cachedTokens: 0, startedAt: "2026-10-12T10:00:02Z", task: "scan.rank", provider: "deepseek", model: "m2", attempt: 2, outcome: "ok", inputTokens: 120, outputTokens: 30, costUsd: 0.0123, latencyMs: 900, downgraded: true, runId: "run-1" },
        { id: 2, callId: "c1", cachedTokens: 0, startedAt: "2026-10-12T10:00:00Z", task: "scan.rank", provider: "deepseek", model: "m1", attempt: 1, outcome: "timeout", inputTokens: 0, outputTokens: 0, latencyMs: 30000, downgraded: false, runId: "run-1", reason: "rested" },
      ].filter((x) => !failedOnly || x.outcome !== "ok");
      return ok({ calls, total: c.query.get("page") === "1" ? 120 : calls.length });
    },
    "GET /api/llm/traces": () =>
      ok({ traces: [{ runId: "run-1", title: "Apply to Acme Corp: Backend Engineer", kind: "apply", status: "succeeded", stage: "fill", startedAt: "2026-10-12T10:00:00Z", calls: 2, costUsd: 0.0123 }, { runId: "ses_1", title: "Ask Agent conversation", kind: "conversation", startedAt: "2026-10-12T09:00:00Z", calls: 1, costUsd: 0 }] }),
    "GET /api/llm/traces/run-1": () =>
      ok({
        trace: { runId: "run-1", title: "Apply to Acme Corp: Backend Engineer", kind: "apply", status: "succeeded", startedAt: "2026-10-12T10:00:00Z", calls: 2, costUsd: 0.0123 },
        spans: [
          { kind: "run", name: "scan", startMs: 0, durationMs: 40000, outcome: "succeeded" },
          { kind: "call", name: "scan.rank", startMs: 100, durationMs: 30000, outcome: "timeout", provider: "deepseek", model: "m1", attempt: 1, error: "deadline exceeded" },
          { kind: "call", name: "scan.rank", startMs: 30200, durationMs: 900, outcome: "ok", provider: "deepseek", model: "m2", attempt: 2, inputTokens: 120, outputTokens: 30, costUsd: 0.0123 },
        ],
      }),
  });
}

describe("Usage", () => {
  it("shows the cap's day, 7-day and 30-day cost and the share of the daily cap used", async () => {
    const api = server(1);
    await mount(<Usage />);
    await waitFor(() => text().includes("Last 7 days"));
    const tiles = qa(".tile").map((t) => t.textContent);
    expect(tiles).toEqual(["$1.00Today", "$2.00Last 7 days", "$9.25Last 30 days", "5Calls in 7 days"]);
    expect(text()).toContain("20%");
    expect(q("label strong")?.parentElement?.tagName).toBe("SPAN");
    expect(q("meter")?.getAttribute("aria-label")).toContain("20 percent");
    expect(api.callsTo("GET /api/llm/cost").map((c) => c.query.get("days")).sort()).toEqual(["30", "7"]);
  });

  it("lists the model calls with tokens, cost, latency and outcome, and no prompt text", async () => {
    server(1);
    await mount(<Usage />);
    await waitFor(() => qa("table").some((t) => t.querySelector("caption")?.textContent === "Model calls, newest first"));
    const table = qa("table").find((t) => t.querySelector("caption")?.textContent === "Model calls, newest first")!;
    const rows = Array.from(table.querySelectorAll("tbody tr")).map((r) => r.textContent);
    expect(rows[0]).toContain("scan.rank");
    expect(rows[0]).toContain("$0.0123");
    expect(rows[0]).toContain("attempt 2");
    expect(rows[0]).toContain("downgraded");
    expect(rows[1]).toContain("unpriced");
    expect(rows[1]).toContain("timeout");
    expect(rows[1]).toContain("an earlier model is resting");
    expect(text()).toContain("120 attempts");
  });

  it("shows an error for the traces without hiding the rest of the page", async () => {
    const api = server(1);
    api.on("GET /api/llm/traces", () => fail("unavailable", "the ledger is busy"));
    await mount(<Usage />);
    await waitFor(() => text().includes("The ledger is busy"));
    expect(text()).toContain("Today");
  });
});
