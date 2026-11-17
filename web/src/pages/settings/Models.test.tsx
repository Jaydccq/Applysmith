import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, byText, click, mount, press, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { routing } from "../../testing/fixtures";
import type { LLMRouting } from "../../wire";
import { ModelsBody } from "./Models";

const CANARY = ["sk", "FAKE", "canary", "0123456789"].join("-");

// The section is mounted on its own, with the routing it is given and a server
// that answers only what the section asks.
async function open(over: Partial<LLMRouting> = {}, byTask?: Parameters<typeof ModelsBody>[0]["byTask"]) {
  const reload = vi.fn();
  const api = fakeApi({
    "GET /api/llm/accounts": () => ok({ accounts: [] }),
    "PATCH /api/llm/routing": () => ok(routing),
    "POST /api/llm/preset": () => ok(routing),
    "PUT /api/llm/keys/anthropic": () => ok({ provider: "anthropic", keyConfigured: true }),
    "PUT /api/llm/providers/anthropic": () => ok(routing),
    "DELETE /api/llm/keys/deepseek": () => ok({ provider: "deepseek", keyConfigured: false }),
  });
  await mount(<ModelsBody routing={{ ...routing, ...over }} byTask={byTask} reload={reload} onNames={() => {}} />);
  return { api, reload };
}

const named = (label: string) => qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === label)!;
// The control inside the open drawer whose label starts with the words given.
function field<T extends HTMLElement = HTMLInputElement>(label: string): T {
  const l = qa<HTMLLabelElement>("dialog label").find((x) => x.textContent?.startsWith(label));
  return (l?.querySelector("select, input") ?? document.getElementById(l?.htmlFor ?? "")) as T;
}

describe("Models: what each job uses", () => {
  it("puts a job back on the default with an emptied row", async () => {
    const { api, reload } = await open();
    await click(button("Use default"));
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 1);
    expect(api.callsTo("PATCH /api/llm/routing")[0].body).toEqual({ routes: { "scan.rank": null } });
    expect(reload).toHaveBeenCalled();
    await waitFor(() => text().includes("scan.rank follows the default again."));
  });
});

describe("Models: change a job", () => {
  it("sends the model, a backup and the wait for a first word as one full row, and nothing else", async () => {
    const { api, reload } = await open();
    await click(named("Change scan.rank"));
    await waitFor(() => q("dialog form[aria-label='Change scan.rank']"));
    expect(field<HTMLSelectElement>("Model").value).toBe("deepseek|deepseek-chat");
    expect(qa("dialog optgroup").map((g) => g.getAttribute("label"))).toEqual(["DeepSeek"]);
    expect(qa("dialog option").map((o) => o.getAttribute("value"))).toContain("deepseek|deepseek-reasoner");
    await type(field("Model"), "deepseek|deepseek-reasoner");
    await type(field("Try the backup if no reply starts"), "8");
    await click(button("Add a backup"));
    await type(field("Backup 1"), "deepseek|deepseek-chat");
    await submit(q("dialog form"));
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 1);
    expect(api.callsTo("PATCH /api/llm/routing")[0].body).toEqual({
      routes: { "scan.rank": { provider: "deepseek", model: "deepseek-reasoner", firstTokenSeconds: 8, fallbacks: [{ provider: "deepseek", model: "deepseek-chat" }] } },
    });
    await waitFor(() => text().includes("scan.rank saved."));
    expect(q("dialog")).toBeNull();
    expect(reload).toHaveBeenCalled();
  });

  it("changes only the model of a job on the default: its thinking and limits stay the job's own and are shown as placeholders", async () => {
    const jobs = routing.tasks.map((t, i) => (i === 0 ? { ...t, maxOutputTokens: 24000, timeoutSeconds: 600 } : t));
    const { api } = await open({ tasks: jobs });
    await click(named("Change evaluation.deep"));
    expect(field("Longest answer").value).toBe("");
    expect(field("Longest answer").placeholder).toBe("24000 (job default)");
    expect(field("Give up after").placeholder).toBe("600 (job default)");
    expect(field<HTMLSelectElement>("How hard it thinks").selectedOptions[0].textContent).toBe("Job default (medium)");
    await type(field("Model"), "deepseek|deepseek-reasoner");
    await submit(q("dialog form"));
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 1);
    expect(api.callsTo("PATCH /api/llm/routing")[0].body).toEqual({
      routes: { "evaluation.deep": { provider: "deepseek", model: "deepseek-reasoner", fallbacks: [{ provider: "anthropic", model: "claude-example" }] } },
    });
  });

  it("keeps the limits a Custom job already has when only its model changes", async () => {
    const jobs = routing.tasks.map((t) => (t.task === "scan.rank" ? { ...t, thinking: "low", maxOutputTokens: 5000, timeoutSeconds: 90 } : t));
    const { api } = await open({ tasks: jobs });
    await click(named("Change scan.rank"));
    expect(field("Longest answer").value).toBe("5000");
    await type(field("Model"), "deepseek|deepseek-reasoner");
    await submit(q("dialog form"));
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 1);
    expect(api.callsTo("PATCH /api/llm/routing")[0].body).toEqual({ routes: { "scan.rank": { provider: "deepseek", model: "deepseek-reasoner", thinking: "low", maxOutputTokens: 5000, timeoutSeconds: 90 } } });
  });

  it("changes only the budget of a job on the default without making it Custom", async () => {
    const { api } = await open({ tasks: routing.tasks.map((t, i) => (i === 0 ? { ...t, budgetable: true, budgetUsd: 0.5 } : t)) });
    await click(named("Change evaluation.deep"));
    expect(field("Daily budget").value).toBe("0.5");
    await type(field("Daily budget"), "1.25");
    await submit(q("dialog form"));
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 1);
    expect(api.callsTo("PATCH /api/llm/routing")[0].body).toEqual({ budgets: { "evaluation.deep": 1.25 } });
  });

  it("shows the server's message inside the editor, keeps what was typed, and checks whole numbers itself", async () => {
    const { api } = await open();
    api.on("PATCH /api/llm/routing", () => fail("validation_failed", "routes.scan.rank: first_token_seconds: must be shorter than timeout_seconds"));
    await click(named("Change scan.rank"));
    await type(field("Try the backup if no reply starts"), "90");
    await submit(q("dialog form"));
    await waitFor(() => q("dialog [role=alert]")?.textContent?.includes("must be shorter than timeout_seconds") === true);
    expect(field("Try the backup if no reply starts").value).toBe("90");
    await type(field("Try the backup if no reply starts"), "x");
    await submit(q("dialog form"));
    await waitFor(() => q("dialog [role=alert]")?.textContent?.includes("must be a whole number") === true);
    expect(api.callsTo("PATCH /api/llm/routing")).toHaveLength(1);
  });
});

describe("Models: the default", () => {
  it("changes the default only after a dialog that says what it does, whose first focus is Cancel", async () => {
    const { api } = await open({ providers: [...routing.providers, { id: "anthropic", kind: "anthropic", structuredOutput: true, embeddings: false, keyConfigured: true }], presets: routing.presets.map((p) => (p.name === "anthropic" ? { ...p, keyConfigured: true } : p)) });
    await click(button("Change default"));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("POST /api/llm/preset")).toHaveLength(0);
    expect(document.activeElement?.textContent).toBe("Cancel");
    expect(q("dialog")!.textContent).toContain("Every job marked Default switches");
    const apply = qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Change default")!;
    expect(apply.disabled).toBe(true);
    await type(q("dialog select"), "anthropic");
    await type(qa<HTMLSelectElement>("dialog select")[1], "deepseek");
    await click(apply);
    await waitFor(() => api.callsTo("POST /api/llm/preset").length === 1);
    expect(api.callsTo("POST /api/llm/preset")[0].body).toEqual({ name: "anthropic", fallback: "deepseek" });
    await waitFor(() => text().includes("The default is now Anthropic."));
  });

  it("does nothing when the dialog is closed with Escape", async () => {
    const { api } = await open();
    await click(button("Change default"));
    await waitFor(() => q("dialog"));
    await press("Escape", {}, q("dialog")!);
    await waitFor(() => !q("dialog"));
    expect(api.callsTo("POST /api/llm/preset")).toHaveLength(0);
  });
});

describe("Models: pools", () => {
  const pool = { mode: "rotate", members: [{ provider: "deepseek", model: "deepseek-chat" }, { provider: "deepseek", model: "deepseek-reasoner" }] };

  it("lists the pools, sends a new or deleted pool in the routing patch, and offers a pool as a job's model but not as a backup", async () => {
    const { api } = await open({ groups: { pool } });
    expect(text()).toContain("DeepSeek · deepseek-chat, DeepSeek · deepseek-reasoner");
    await click(named("Change scan.rank"));
    expect(qa("dialog optgroup").map((g) => g.getAttribute("label"))).toEqual(["DeepSeek", "Model pools"]);
    await click(button("Add a backup"));
    expect(qa("dialog select").filter((s) => s.querySelector("optgroup[label='Model pools']"))).toHaveLength(1);
    await click(byText("dialog button", "Cancel"));
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Leave")!);

    await click(button("Add a pool"));
    const form = await waitFor(() => q<HTMLFormElement>("form[aria-label='New pool']"));
    const input = (label: string) => qa<HTMLInputElement>("form[aria-label='New pool'] input").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;
    await type(input("Name"), "fast");
    await type(form.querySelector("select:nth-of-type(2), .model-pick select") as HTMLSelectElement, "deepseek|deepseek-chat");
    await submit(form);
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 1);
    expect(api.callsTo("PATCH /api/llm/routing")[0].body).toEqual({ groups: { fast: { mode: "order", members: [{ provider: "deepseek", model: "deepseek-chat" }] } } });
    await waitFor(() => text().includes("Pool fast saved."));

    await click(named("Delete pool pool"));
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 2);
    expect(api.callsTo("PATCH /api/llm/routing")[1].body).toEqual({ groups: { pool: null } });
  });
});

describe("Models: spending", () => {
  it("shows the spend the cap counts beside the cap and saves a new cap, holding the page while it is edited", async () => {
    const { api } = await open({ capSpentUsd: 0.42 });
    expect(text()).toContain("Spent today: $0.42");
    const cap = qa<HTMLInputElement>("input[type=number]").find((i) => i.labels?.[0]?.textContent === "Daily cap, in US dollars")!;
    expect(cap.value).toBe("5");
    await type(cap, "8.5");
    await click(cap.closest("form")!.querySelector("a[href='/usage']"));
    expect(q("dialog")!.textContent).toContain("Leave without saving?");
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Cancel")!);
    expect(window.location.pathname).not.toBe("/usage");
    await submit(cap.closest("form"));
    await waitFor(() => api.callsTo("PATCH /api/llm/routing").length === 1);
    expect(api.callsTo("PATCH /api/llm/routing")[0].body).toEqual({ dailyUsdCap: 8.5 });
    await waitFor(() => text().includes("Saved at"));
  });

  it("shows no request line for a client-side validation error, and one for every server error", async () => {
    const { api } = await open();
    const cap = qa<HTMLInputElement>("input[type=number]").find((i) => i.labels?.[0]?.textContent === "Daily cap, in US dollars")!;
    await type(cap, "-1");
    await submit(cap.closest("form"));
    await waitFor(() => text().includes("The daily cap must be zero or more."));
    expect(text()).not.toContain("Request ");
    api.on("PATCH /api/llm/routing", () => fail("internal", "The cap could not be saved."));
    await type(cap, "3");
    await submit(cap.closest("form"));
    await waitFor(() => text().includes("The cap could not be saved."));
    expect(text()).toMatch(/Request req-test|Request web-/);
  });
});

describe("Models: API keys", () => {
  const keyField = (name: string) => qa<HTMLInputElement>("input[type=password]").find((f) => f.labels?.[0]?.textContent?.includes(name))!;

  it("saves a key, adds the account without touching the default or any job, empties the field and never renders the key", async () => {
    const { api, reload } = await open();
    const field = keyField("Anthropic");
    await type(field, CANARY);
    expect(field.getAttribute("value")).toBeNull();
    await submit(field.closest("form"));
    await waitFor(() => text().includes("Saved at"));
    expect(api.callsTo("PUT /api/llm/keys/anthropic")[0].body).toEqual({ key: CANARY });
    expect(api.callsTo("PUT /api/llm/providers/anthropic").map((c) => c.body)).toEqual([{ preset: "anthropic" }]);
    expect(api.callsTo("POST /api/llm/preset")).toHaveLength(0);
    expect(api.callsTo("PATCH /api/llm/routing")).toHaveLength(0);
    expect(reload).toHaveBeenCalled();
    expect(field.value).toBe("");
    expect(document.body.innerHTML).not.toContain(CANARY);
  });

  it("keeps a rejected key out of the message, empties the field and adds no account", async () => {
    const { api } = await open();
    api.on("PUT /api/llm/keys/anthropic", () => fail("validation_failed", "The key is not accepted.", { details: { field: "key" } }));
    const field = keyField("Anthropic");
    await type(field, CANARY);
    await submit(field.closest("form"));
    await waitFor(() => text().includes("The key is not accepted."));
    expect(field.value).toBe("");
    expect(field.getAttribute("value")).toBeNull();
    expect(document.body.innerHTML).not.toContain(CANARY);
    expect(api.callsTo("PUT /api/llm/providers/anthropic")).toHaveLength(0);
  });

  it("asks before clearing a key and clears it only when confirmed", async () => {
    const { api } = await open();
    await click(button("Clear key"));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/llm/keys/deepseek")).toHaveLength(0);
    expect(document.activeElement?.textContent).toBe("Cancel");
    await click(byText("dialog button", "Cancel"));
    expect(api.callsTo("DELETE /api/llm/keys/deepseek")).toHaveLength(0);
    await click(button("Clear key"));
    await click(byText("dialog button", "Clear key"));
    await waitFor(() => api.callsTo("DELETE /api/llm/keys/deepseek").length === 1);
    await waitFor(() => text().includes("Key cleared."));
  });
});
