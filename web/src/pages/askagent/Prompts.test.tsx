import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, byLabel, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { prompt, trigger } from "../../testing/fixtures_agent";
import type { AgentPrompt } from "../../wire";
import { PromptsPanel } from "./Prompts";

function server(initial: AgentPrompt[] = [prompt(), trigger()]) {
  let prompts = initial;
  const api = fakeApi({ "GET /api/agent/prompts": () => ok({ prompts, limit: 10 }) });
  return { api, set: (p: AgentPrompt[]) => void (prompts = p), get: () => prompts };
}

const confirmButton = (verb: string) => qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === verb)!;

describe("Ask Agent: triggers list", () => {
  it("creates a cadence prompt from text, days and a time", async () => {
    const s = server([]);
    s.api.on("POST /api/agent/prompts", () => {
      s.set([prompt({ id: "p3", text: "Weekly review" })]);
      return { data: prompt({ id: "p3" }), status: 201 };
    });
    await mount(<PromptsPanel />);
    await waitFor(() => text().includes("No scheduled prompt yet."));
    expect(button("Add prompt")?.disabled).toBe(true);
    await type(q("textarea"), "Weekly review");
    await click(qa<HTMLInputElement>("input[type=checkbox]").find((c) => c.parentElement?.textContent?.trim() === "fri")!);
    await click(qa<HTMLInputElement>("input[type=checkbox]").find((c) => c.parentElement?.textContent?.trim() === "mon")!);
    await type(q("input[type=time]"), "07:15");
    await submit(q("form"));
    await waitFor(() => s.api.callsTo("POST /api/agent/prompts").length === 1);
    expect(s.api.callsTo("POST /api/agent/prompts")[0].body).toEqual({ text: "Weekly review", cadence: { days: ["mon", "fri"], time: "07:15" } });
    await waitFor(() => text().includes("Weekly review"));
  });

  it("shows the refusal when the server rejects a prompt that asks the agent to act", async () => {
    const s = server([]);
    s.api.on("POST /api/agent/prompts", () => fail("validation_failed", "A scheduled prompt cannot apply, submit or write data."));
    await mount(<PromptsPanel />);
    await waitFor(() => text().includes("No scheduled prompt yet."));
    await type(q("textarea"), "Apply to every posting");
    await click(qa<HTMLInputElement>("input[type=checkbox]")[0]);
    await type(q("input[type=time]"), "07:15");
    await submit(q("form"));
    await waitFor(() => q('[role="alert"]'));
    expect(text()).toContain("cannot apply, submit or write data");
  });

  it("pauses and resumes a prompt", async () => {
    const s = server([prompt()]);
    s.api.on("PATCH /api/agent/prompts/p1", (c) => {
      const state = (c.body as { state: string }).state;
      s.set([prompt({ state })]);
      return ok(prompt({ state }));
    });
    await mount(<PromptsPanel />);
    await click(await waitFor(() => byLabel(/^Pause /)));
    await waitFor(() => byLabel(/^Resume /));
    expect(s.api.callsTo("PATCH /api/agent/prompts/p1")[0].body).toEqual({ state: "paused" });
    await click(byLabel(/^Resume /));
    await waitFor(() => byLabel(/^Pause /));
    expect(s.api.callsTo("PATCH /api/agent/prompts/p1")[1].body).toEqual({ state: "active" });
  });

  it("confirms a prompt the agent proposed, which does nothing until then", async () => {
    const s = server([prompt({ state: "proposed", origin: "agent" })]);
    s.api.on("POST /api/agent/prompts/p1/confirm", () => {
      s.set([prompt()]);
      return ok(prompt());
    });
    await mount(<PromptsPanel />);
    await waitFor(() => text().includes("made by the agent"));
    expect(byLabel(/^Pause /)).toBeNull();
    await click(byLabel(/^Confirm /));
    await waitFor(() => byLabel(/^Pause /));
  });

  it("deletes only after a confirmation", async () => {
    const s = server([prompt()]);
    s.api.on("DELETE /api/agent/prompts/p1", () => {
      s.set([]);
      return ok({ id: "p1" });
    });
    await mount(<PromptsPanel />);
    await click(await waitFor(() => byLabel(/^Delete /)));
    expect(s.api.callsTo("DELETE /api/agent/prompts/p1")).toHaveLength(0);
    await click(confirmButton("Cancel"));
    expect(s.api.callsTo("DELETE /api/agent/prompts/p1")).toHaveLength(0);
    await click(byLabel(/^Delete /));
    await click(confirmButton("Delete prompt"));
    await waitFor(() => text().includes("No scheduled prompt yet."));
    expect(s.api.callsTo("DELETE /api/agent/prompts/p1")).toHaveLength(1);
  });
});
