import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { byLabel, click, mount, q, submit, text, type, waitFor } from "../../testing/dom";
import type { ProviderAccount } from "../../wire";
import { UsageSetup, UsageWindows } from "./PlanUsage";

const account: ProviderAccount = {
  id: "minimax-global", name: "MiniMax (Global)", mode: "subscription", installed: false, authFilePresent: false,
  credentialStatus: "missing", keyConfigured: false, cookieConfigured: false, workspaceConfigured: false,
  cookieSupported: true, consoleURL: "https://platform.minimax.io/subscribe/token-plan", note: "Key preferred; console cookie optional.",
  usage: { status: "unknown", message: "Refresh to retrieve quota.", windows: [] },
};

describe("plan usage", () => {
  it("contacts no usage endpoint until Refresh, and draws no bar for usage nobody has read", async () => {
    const api = fakeApi();
    await mount(
      <>
        <UsageSetup account={account} changed={() => {}} />
        <UsageWindows account={account} />
      </>,
    );
    expect(api.calls).toHaveLength(0);
    expect(q("progress")).toBeNull();
    expect(text()).not.toContain("0% used");
  });

  it("stores a secret once, clears the field after a failure and never puts it into an attribute", async () => {
    const api = fakeApi({ "PUT /api/llm/accounts/minimax-global/credentials": () => fail("unavailable", "Unlock the keychain") });
    await mount(<UsageSetup account={account} changed={() => {}} />);
    const field = q<HTMLInputElement>('input[type="password"]')!;
    await type(field, "synthetic-plan-key");
    await submit(field.closest("form"));
    await waitFor(() => text().includes("Unlock the keychain"));
    expect(field.value).toBe("");
    expect(document.body.innerHTML).not.toContain("synthetic-plan-key");
    expect(api.callsTo("PUT /api/llm/accounts/minimax-global/credentials").map((c) => c.body)).toEqual([{ kind: "key", value: "synthetic-plan-key" }]);
    expect(api.calls.filter((c) => c.path.endsWith("/refresh"))).toHaveLength(0);
  });

  it("shows valid zero usage, then none after an answer that could not be read", async () => {
    let changed = 0;
    const api = fakeApi({
      "POST /api/llm/accounts/minimax-global/refresh": () => ok({ ...account, usage: { status: "ok", message: "Provider quota", checkedAt: "2026-10-24T12:00:00Z", windows: [{ name: "5 hours", usedPercent: 0 }] } }),
    });
    await mount(<UsageSetup account={account} changed={() => changed++} />);
    await click(byLabel(/Refresh usage for MiniMax/));
    expect(api.callsTo("POST /api/llm/accounts/minimax-global/refresh")).toHaveLength(1);
    expect(changed).toBe(1);
    const zero = { ...account, usage: { status: "ok", message: "Provider quota", checkedAt: "2026-10-24T12:00:00Z", windows: [{ name: "5 hours", usedPercent: 0 }] } };
    await mount(<UsageWindows account={zero} />);
    expect(text()).toContain("0.0% used");
    expect(q<HTMLProgressElement>("progress")?.value).toBe(0);
    await mount(<UsageWindows account={{ ...zero, usage: { status: "unknown", message: "Unrecognized response", windows: [] } }} />);
    expect(q("progress")).toBeNull();
  });
});
