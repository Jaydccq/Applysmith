import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { agentSettings, harness } from "../../testing/fixtures_agent";
import { button, click, mount, q, qa, text, waitFor } from "../../testing/dom";
import { AskAgentSection } from "./AskAgent";

function server(current = agentSettings(), view = harness()) {
  let now = current;
  return fakeApi({
    "GET /api/agent/settings": () => ok(now),
    "PATCH /api/agent/settings": (c) => ((now = { ...now, ...(c.body as object) }), ok(now)),
    "GET /api/agent/harness": () => ok(view),
  });
}

const restoreBtn = (id: number) => q<HTMLButtonElement>(`button[aria-label="Restore version ${id}"]`);
const box = (label: string) => qa<HTMLInputElement>("input[type=checkbox]").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;

describe("Settings: Ask Agent", () => {
  it("patches only the switch that was flipped", async () => {
    const api = server();
    await mount(<AskAgentSection />);
    await click(await waitFor(() => box("Nightly")));
    await waitFor(() => api.callsTo("PATCH /api/agent/settings").length === 1);
    expect(api.callsTo("PATCH /api/agent/settings")[0].body).toEqual({ consolidation: false });
    await waitFor(() => box("Nightly").checked === false);
  });

  it("shows the server's refusal and leaves the switch as it was stored", async () => {
    const api = server();
    api.on("PATCH /api/agent/settings", () => fail("validation_failed", "tokenBudget must be between 4000 and 200000"));
    await mount(<AskAgentSection />);
    await click(await waitFor(() => box("Summarise")));
    await waitFor(() => text().includes("tokenBudget must be between"));
    expect(box("Summarise").checked).toBe(true);
  });

  it("restores a version and draws the answer", async () => {
    const api = server();
    const restored = harness({ active: 1, versions: harness().versions.map((v) => ({ ...v, source: v.id === 1 ? "manual" : v.source, active: v.id === 1 })) });
    api.on("POST /api/agent/harness/restore", () => ok(restored));
    await mount(<AskAgentSection />);
    await click(await waitFor(() => restoreBtn(1)));
    await waitFor(() => api.callsTo("POST /api/agent/harness/restore").length === 1);
    expect(api.callsTo("POST /api/agent/harness/restore")[0].body).toEqual({ id: 1 });
    await waitFor(() => text().includes("Answer in three sentences.") && restoreBtn(2) !== null);
    expect(text()).toContain("restored by you");
  });

  it("starts a round, shows that it runs and polls until it ends", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const api = server();
      api.on("POST /api/agent/harness/round", () => ok(harness({ running: true })));
      await mount(<AskAgentSection />);
      await click(await waitFor(() => button("Run a round now")));
      await waitFor(() => api.callsTo("POST /api/agent/harness/round").length === 1);
      await waitFor(() => text().includes("A round is running"));
      expect(button("Run a round now")!.disabled).toBe(true);
      const before = api.callsTo("GET /api/agent/harness").length;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      await waitFor(() => api.callsTo("GET /api/agent/harness").length > before);
      await waitFor(() => !text().includes("A round is running"));
      expect(button("Run a round now")!.disabled).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});
