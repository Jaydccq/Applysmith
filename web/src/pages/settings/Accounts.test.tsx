import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../../testing/api";
import { button, byLabel, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { cost, routing } from "../../testing/fixtures";
import type { LLMRouting, ProviderAccount } from "../../wire";
import { ModelsBody } from "./Models";
import { ProviderEditor } from "./ProviderEditor";
import { ProviderPrice } from "./ProviderPrice";

const base = { ...routing };

const account = (over: Partial<ProviderAccount> = {}): ProviderAccount => ({
  id: "codex", name: "Codex", mode: "native", installed: true, authFilePresent: false, credentialStatus: "missing", keyConfigured: false, cookieConfigured: false, cookieSupported: false, workspaceConfigured: false,
  note: "", usage: { status: "unsupported", message: "Use the native program", windows: [] }, ...over,
});

// The section is mounted with a live routing view the test can change, and a
// server that answers the account list and the cost report.
function run(view: { current: LLMRouting }, accounts: ProviderAccount[] = []) {
  const reload = vi.fn();
  const api = fakeApi({ "GET /api/llm/accounts": () => ok({ accounts }), "GET /api/llm/cost": () => ok(cost) });
  return { api, reload, show: () => mount(<ModelsBody routing={view.current} reload={reload} onNames={() => {}} />) };
}
const card = (name: string) => qa("li.svc").find((c) => c.querySelector(".svc-title strong")?.textContent === name)!;

describe("Models: testing an account", () => {
  it("starts the test on the press, follows it while it runs and shows connected with the time and latency", async () => {
    const { api, reload, show } = run({ current: { ...base, lastTest: null } });
    api.on("POST /api/llm/providers/deepseek/test", () => ({ data: { provider: "deepseek" }, status: 202 }));
    await show();
    await click(byLabel(/Test DeepSeek/));
    expect(api.callsTo("POST /api/llm/providers/deepseek/test").map((c) => c.body)).toEqual([{}]);
    expect(reload).toHaveBeenCalled();
    // While the server says the test runs, the section reads the routing again each second.
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const testing: LLMRouting = { ...base, lastTest: null, testing: "deepseek" };
    await mount(<ModelsBody routing={testing} reload={reload} onNames={() => {}} />);
    expect(card("DeepSeek").textContent).toContain("Testing");
    expect(byLabel(/Test DeepSeek/)!.disabled).toBe(true);
    reload.mockClear();
    await act(async () => void vi.advanceTimersByTime(2_000));
    expect(reload).toHaveBeenCalledTimes(2);
    const done: LLMRouting = { ...base, lastTest: { provider: "deepseek", model: "deepseek-chat", outcome: "ok", latencyMs: 640, testedAt: "2026-10-12T09:00:00.000Z" } };
    await mount(<ModelsBody routing={done} reload={reload} onNames={() => {}} />);
    expect(card("DeepSeek").textContent).toContain("Connected");
    expect(card("DeepSeek").textContent).toContain("640 ms");
    reload.mockClear();
    await act(async () => void vi.advanceTimersByTime(3_000));
    expect(reload).not.toHaveBeenCalled();
  });

  it("shows failing with the error class and the instruction to replace the key", async () => {
    const view = { current: { ...base, lastTest: { provider: "deepseek", model: "deepseek-chat", outcome: "failed", errorClass: "auth", fix: "paste a new key with `applysmith llm keys set deepseek`", latencyMs: 120, testedAt: "2026-10-12T09:00:00.000Z" } } };
    await run(view).show();
    expect(card("DeepSeek").textContent).toContain("Failing: auth");
    expect(card("DeepSeek").textContent).toContain("paste a new key");
    expect(text()).not.toMatch(/sk-/);
  });
});

describe("Models: Sign in with ChatGPT", () => {
  const plan: LLMRouting = {
    ...base,
    providers: [...base.providers, { id: "chatgpt", kind: "chatgpt", baseUrl: "https://api.openai.com/v1", preset: "chatgpt", structuredOutput: true, embeddings: false, keyConfigured: false }],
    presets: [...base.presets, { name: "chatgpt", label: "ChatGPT plan (sign in)", kind: "chatgpt", inSetup: false, subscription: true, keyConfigured: false }],
  };
  const out = { signedIn: false, planUsage: false, pending: false };

  async function chatgpt(view: LLMRouting, state: () => Record<string, unknown>, models = [{ slug: "gpt-6.1-sol" }]): Promise<FakeApi> {
    const { api, show } = run({ current: view });
    api.on("GET /api/llm/chatgpt", () => ok(state()));
    api.on("GET /api/llm/chatgpt/models", () => ok({ models }));
    await show();
    return api;
  }

  it("signs in through the browser, follows the attempt, shows the account and its models without a token", async () => {
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    let state: Record<string, unknown> = out;
    const { api, show } = run({ current: plan });
    api.on("GET /api/llm/chatgpt", () => ok(state));
    api.on("GET /api/llm/chatgpt/models", () => ok({ models: [{ slug: "gpt-6.1-sol" }] }));
    api.on("POST /api/llm/chatgpt/signin", () => {
      state = { ...out, pending: true };
      return ok({ url: "https://auth.openai.com/api/accounts/authorize?state=s" });
    });
    await show();
    await waitFor(() => card("ChatGPT plan").textContent?.includes("Not signed in"));
    await click(button("Sign in with ChatGPT"));
    expect(open).toHaveBeenCalledWith("https://auth.openai.com/api/accounts/authorize?state=s", "_blank", "noopener,noreferrer");
    await waitFor(() => text().includes("Waiting for the browser"));
    state = { signedIn: true, planUsage: true, pending: false, account: "me@example.test" };
    await waitFor(() => card("ChatGPT plan").textContent?.includes("me@example.test"));
    expect(card("ChatGPT plan").textContent).not.toContain("Not signed in");
    await waitFor(() => card("ChatGPT plan").textContent?.includes("Models on your plan: gpt-6.1-sol"));
    expect(text()).not.toMatch(/eyJ|refresh_token|access_token/i);
    open.mockRestore();
  });

  it("adds the account before signing in when there is none, and changes no job", async () => {
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const none: LLMRouting = { ...plan, providers: base.providers };
    const { api, show } = run({ current: none });
    api.on("GET /api/llm/chatgpt", () => ok(out));
    api.on("PUT /api/llm/providers/chatgpt", () => ok(plan));
    api.on("POST /api/llm/chatgpt/signin", () => ok({ url: "https://auth.openai.com/x" }));
    await show();
    await click(await waitFor(() => button("Sign in with ChatGPT")));
    expect(api.callsTo("PUT /api/llm/providers/chatgpt").map((c) => c.body)).toEqual([{ preset: "chatgpt" }]);
    expect(api.callsTo("POST /api/llm/preset")).toHaveLength(0);
    expect(api.callsTo("PATCH /api/llm/routing")).toHaveLength(0);
    open.mockRestore();
  });

  it("signs out and says when OpenAI did not confirm the revocation", async () => {
    const api = await chatgpt(plan, () => ({ signedIn: true, planUsage: true, pending: false, account: "me@example.test" }), []);
    api.on("POST /api/llm/chatgpt/signout", () => ok({ revoked: false }));
    await click(await waitFor(() => button("Sign out")));
    expect(api.callsTo("POST /api/llm/chatgpt/signout")).toHaveLength(1);
    await waitFor(() => text().includes("did not confirm the revocation"));
  });
});

describe("Models: programs on this Mac", () => {
  const withClaude: LLMRouting = { ...base, presets: [...base.presets, { name: "claude", label: "Claude Code CLI (subscription)", kind: "cli", program: "claude", inSetup: true, subscription: true, keyConfigured: false }] };
  const claude = (over: Partial<ProviderAccount> = {}) => account({ id: "claude", name: "Claude", loginCommand: "claude auth login", ...over });

  it("says plainly that Claude works by running the installed program, and adds it without touching a job", async () => {
    const { api, reload, show } = run({ current: withClaude }, [claude()]);
    api.on("PUT /api/llm/providers/claude", () => ok(withClaude));
    await show();
    const c = card("Claude Pro/Max");
    expect(c.textContent).toContain("only by running the Claude Code program installed on this Mac");
    expect(c.textContent).toContain("slower");
    expect(c.textContent).toContain("claude auth login");
    await click(byLabel(/Connect Claude Pro\/Max/));
    expect(api.callsTo("PUT /api/llm/providers/claude").map((x) => x.body)).toEqual([{ preset: "claude" }]);
    expect(api.callsTo("POST /api/llm/preset")).toHaveLength(0);
    expect(api.callsTo("PATCH /api/llm/routing")).toHaveLength(0);
    expect(reload).toHaveBeenCalled();
  });
});

describe("Models: another account or an endpoint of your own", () => {
  it("adds a separately named account without changing the default or any job", async () => {
    const reload = vi.fn();
    const api = fakeApi({ "PUT /api/llm/providers/second": () => ok(routing) });
    await mount(<ProviderEditor routing={routing} reload={reload} />);
    await click(button("Add another account"));
    await type(q("select"), routing.presets[0].name);
    await type(q('input[placeholder="minimax-personal"]'), "second");
    await submit(q("form"));
    expect(api.callsTo("PUT /api/llm/providers/second").map((c) => c.body)).toEqual([{ preset: routing.presets[0].name }]);
    expect(api.calls).toHaveLength(1);
    expect(reload).toHaveBeenCalledOnce();
  });

  it("reports the refusal to remove an account a job still uses, and keeps it", async () => {
    const { api, show } = run({ current: base });
    api.on("DELETE /api/llm/providers/deepseek", () => fail("conflict", "provider is in use"));
    await show();
    await click(byLabel(/Remove DeepSeek account/));
    await click(button("Remove account") && qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Remove account")!);
    await waitFor(() => text().includes("provider is in use"));
    expect(api.callsTo("DELETE /api/llm/providers/deepseek")).toHaveLength(1);
    expect(byLabel(/Test DeepSeek/)).not.toBeNull();
  });

  it("saves USD price values for the named account and model", async () => {
    const api = fakeApi({ "PUT /api/llm/prices/china": () => ok(routing) });
    await mount(<ProviderPrice provider="china" models={["model-1"]} reload={() => {}} />);
    const fields = qa<HTMLInputElement>("input");
    for (const [i, value] of ["model-1", "1", "0", "2"].entries()) await type(fields[i], value);
    await submit(q("form"));
    expect(api.callsTo("PUT /api/llm/prices/china").map((c) => c.body)).toEqual([{ model: "model-1", inputPerM: 1, cachedInputPerM: 0, outputPerM: 2 }]);
    expect(text()).toContain("Saved the USD price");
  });
});
