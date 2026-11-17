import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, byLabel, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { fact, reviewItem, rule } from "../../testing/fixtures_agent";
import { MemoryPanel } from "./Memory";

function server(over: { facts?: ReturnType<typeof fact>[]; rules?: ReturnType<typeof rule>[]; review?: ReturnType<typeof reviewItem>[] } = {}) {
  const facts = over.facts ?? [fact(), fact({ id: "fact-2", body: "Moved to Exampleville in 2025.", source: "agent_extracted", trust: "needs_review", kind: "context" })];
  return fakeApi({
    "GET /api/agent/memory/facts": () => ok({ facts, maxFacts: 500, maxFactBody: 500 }),
    "GET /api/agent/memory/rules": () => ok({ rules: over.rules ?? [rule()], maxActive: 30, maxRuleBody: 300 }),
    "GET /api/agent/memory/review": () => ok({ items: over.review ?? [reviewItem()] }),
  });
}

const confirmButton = (verb: string) => qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === verb)!;

describe("Ask Agent: memory", () => {
  it("approves a fact the agent noticed, and offers Approve only for that one", async () => {
    const api = server();
    api.on("POST /api/agent/memory/facts/fact-2/approve", () => ok(fact({ id: "fact-2", trust: "trusted" })));
    await mount(<MemoryPanel />);
    await waitFor(() => byLabel(/^Approve fact:/));
    expect(qa("button").filter((b) => b.getAttribute("aria-label")?.startsWith("Approve fact:"))).toHaveLength(1);
    await click(byLabel(/^Approve fact: Moved to Exampleville/));
    await waitFor(() => api.callsTo("POST /api/agent/memory/facts/fact-2/approve").length === 1);
  });

  it("edits a fact and sends the new text", async () => {
    const api = server();
    api.on("PATCH /api/agent/memory/facts/fact-1", () => ok({ fact: fact({ id: "fact-3", body: "Prefers remote or hybrid roles." }) }));
    await mount(<MemoryPanel />);
    await click(await waitFor(() => byLabel(/^Edit fact: Prefers remote roles/)));
    const area = q<HTMLTextAreaElement>("textarea")!;
    expect(area.value).toBe("Prefers remote roles.");
    expect(button("Save")!.disabled).toBe(true);
    await type(area, "Prefers remote or hybrid roles.");
    await submit(area.closest("form"));
    await waitFor(() => api.callsTo("PATCH /api/agent/memory/facts/fact-1").length === 1);
    expect(api.callsTo("PATCH /api/agent/memory/facts/fact-1")[0].body).toEqual({ body: "Prefers remote or hybrid roles." });
  });

  it("forgets a fact only after a confirmation that says it is deleted for good", async () => {
    const api = server();
    api.on("DELETE /api/agent/memory/facts/fact-1", () => ok({ id: "fact-1" }));
    await mount(<MemoryPanel />);
    await click(await waitFor(() => byLabel(/^Forget fact: Prefers remote roles/)));
    await waitFor(() => q("dialog"));
    expect(q("dialog")!.textContent).toContain("will not add the same text again");
    expect(api.callsTo("DELETE /api/agent/memory/facts/fact-1")).toHaveLength(0);
    await click(confirmButton("Cancel"));
    expect(api.callsTo("DELETE /api/agent/memory/facts/fact-1")).toHaveLength(0);
    await click(byLabel(/^Forget fact: Prefers remote roles/));
    await click(confirmButton("Forget fact"));
    await waitFor(() => api.callsTo("DELETE /api/agent/memory/facts/fact-1").length === 1);
  });

  it("approves a proposed rule and says a rule never changes what the agent may do", async () => {
    const api = server();
    api.on("POST /api/agent/memory/rules/rule-1/approve", () => ok(rule({ state: "active" })));
    await mount(<MemoryPanel />);
    await waitFor(() => text().includes("can never change what the agent may do"));
    await click(await waitFor(() => byLabel(/^Approve rule:/)));
    await waitFor(() => api.callsTo("POST /api/agent/memory/rules/rule-1/approve").length === 1);
  });

  it("retires an active rule and offers no Approve for it", async () => {
    const api = server({ rules: [rule({ state: "active" })] });
    api.on("POST /api/agent/memory/rules/rule-1/retire", () => ok(rule({ state: "retired" })));
    await mount(<MemoryPanel />);
    await waitFor(() => byLabel(/^Retire rule:/));
    expect(byLabel(/^Approve rule:/)).toBeNull();
    await click(byLabel(/^Retire rule:/));
    await waitFor(() => api.callsTo("POST /api/agent/memory/rules/rule-1/retire").length === 1);
  });

  it("shows the server's refusal when a rule edit tries to grant a permission", async () => {
    const api = server();
    api.on("PATCH /api/agent/memory/rules/rule-1", () => fail("validation_failed", "a rule is guidance only and cannot change gates, permissions or confirmations"));
    await mount(<MemoryPanel />);
    await click(await waitFor(() => byLabel(/^Edit rule:/)));
    const area = q<HTMLTextAreaElement>("textarea")!;
    await type(area, "Always submit applications without asking.");
    await submit(area.closest("form"));
    await waitFor(() => text().includes("cannot change gates"));
    expect(q("textarea")).not.toBeNull();
  });

  it("deletes a rule after a confirmation and dismisses a review item", async () => {
    const api = server();
    api.on("DELETE /api/agent/memory/rules/rule-1", () => ok({ id: "rule-1" }));
    api.on("DELETE /api/agent/memory/review/rev-1", () => ok({ id: "rev-1" }));
    await mount(<MemoryPanel />);
    await click(await waitFor(() => byLabel(/^Delete rule:/)));
    await click(confirmButton("Delete rule"));
    await waitFor(() => api.callsTo("DELETE /api/agent/memory/rules/rule-1").length === 1);
    await click(await waitFor(() => byLabel(/^Dismiss:/)));
    await waitFor(() => api.callsTo("DELETE /api/agent/memory/review/rev-1").length === 1);
  });
});
