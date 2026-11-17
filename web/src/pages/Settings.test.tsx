import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../testing/api";
import { mount, q, qa, text, waitFor } from "../testing/dom";
import { cost, routing } from "../testing/fixtures";
import { health } from "../testing/fixtures_gmail";
import Settings from "./Settings";

function server() {
  const api = fakeApi({
    "GET /api/llm/routing": () => ok(routing),
    "GET /api/llm/cost": () => ok(cost),
    "GET /api/extension/build": () => ok({ stamp: "3f9a0c27d41e8b65", version: "0.1.0" }),
    "GET /api/extension/clients": () => ok({ since: "2026-10-12T09:00:00Z", clients: [] }),
    "PUT /api/llm/keys/anthropic": () => ok({ provider: "anthropic", keyConfigured: true }),
    "DELETE /api/llm/keys/deepseek": () => ok({ provider: "deepseek", keyConfigured: false }),
    "POST /api/llm/preset": () => ok({ ...routing, preset: "anthropic" }),
    "PATCH /api/llm/routing": () => ok(routing),
    "GET /api/agent/settings": () => ok({ compaction: true, consolidation: true, selfImprove: true, tokenBudget: 24000, tokenBudgetMin: 4000, tokenBudgetMax: 200000, tokenBudgetDefault: 24000 }),
    "GET /api/agent/harness": () => ok({ active: 0, running: false, versions: [], rounds: [], lessons: [] }),
    "GET /api/notifications/settings": () => ok({ desktop: true, telegram: true, failedScans: false, telegramPaired: false }),
    "GET /api/config/policy": () => ok({ file: "policy", exists: true, template: false, content: "", revision: "p1", issues: [], values: {} }),
    "GET /api/policy/risk": () => ok({ days: 30, baseline: [], signals: [], cooldowns: [] }),
    "GET /api/documents/preferences": () => ok({ language: "en", paper: "a4", resumePages: 2, coverWhenAsked: "always", revision: 1 }),
    "GET /api/gmail/health": () => ok(health()),
    "GET /api/gmail/settings": () => ok({ alerts: true, scheduledDigests: true }),
    "GET /api/secrets": () => ok({ registry: [], entries: [] }),
    "GET /api/telegram/status": () => ok({ tokenSet: false, paired: false, health: "stopped", unauthorizedAttempts: 0 }),
    "GET /api/computer-use": () => ok({ health: "permissions", accessibility: true, screenRecording: false, servicePaused: false, backgroundOff: false, paused: false, imagesToModel: false }),
    "GET /api/remote/status": () => ok({ mode: "off", listeners: [{ name: "primary", address: "127.0.0.1:47319" }], tunnel: { state: "off", publicUrl: "", version: "", consecutiveFailures: 0 }, allowScreenshots: false, devices: 0, connected: 0 }),
    "GET /api/remote/devices": () => ok({ devices: [] }),
    "GET /api/remote/me": () => ok({ kind: "dashboard" }),
    "GET /api/answer-bank/settings": () => ok({ llmFallbackEnabled: false, groundingMode: "strict", autoAcceptLegalDeclarations: false, allowGoogleSignIn: false, watchScans: false, knowledgeSemanticRetrieval: false }),
    "GET /api/system/info": () => ok({ workspacePath: "/Users/example/Applysmith" }),
    "GET /api/system/cli": () => ok({ available: true, state: "installed", link: "/Users/example/.local/bin/applysmith", target: "/Applications/Applysmith.app/Contents/MacOS/Applysmith", onPath: true, profileFile: "~/.zshrc", profileLine: "x" }),
    "GET /api/scan-sources": () => ok({ sources: [{ name: "linkedin", title: "LinkedIn search", kind: "browser", readiness: "ready", running: false }, { name: "exa", title: "Exa search", kind: "api", needs: "key", keyName: "api-key-exa", readiness: "needs key", running: false }] }),
    "GET /api/config/profile": () => ok({ file: "profile", exists: true, template: false, content: "version: 1\n", revision: "r1", issues: [], values: {} }),
    "GET /api/health": () => ok({ status: "ok", apiVersion: 1, version: "0.1.0", buildId: "dev", workspaceId: "0123456789ab", startedAt: "2026-10-12T09:00:00Z", schemaVersion: 36 }),
  });
  return api;
}

describe("Settings: a paired remote device", () => {
  it("sees none of the sections the server refuses it, and none of their routes is asked", async () => {
    const api = server();
    api.on("GET /api/remote/me", () => ok({ kind: "remote-device", deviceId: "dev_1", label: "Alex's phone" }));
    await mount(<Settings />);
    await waitFor(() => text().includes("Settings are changed on the computer that runs Applysmith"));
    expect(qa("section.panel")).toHaveLength(0);
    for (const heading of ["Credentials", "Runtime settings", "Remote access", "Files", "Advanced", "Models and providers"]) expect(text()).not.toContain(heading);
    expect(q("nav[aria-label='Sections on this page']")).toBeNull();
    expect(api.calls.filter((c) => c.path !== "/api/remote/me")).toEqual([]);
  });

  it("shows nothing of the page until it has heard who it is", async () => {
    const api = server();
    api.on("GET /api/remote/me", () => new Promise(() => {}));
    await mount(<Settings />);
    expect(qa("section.panel")).toHaveLength(0);
    expect(api.calls.filter((c) => c.path !== "/api/remote/me")).toEqual([]);
  });

  it("shows every section on the computer itself", async () => {
    server();
    await mount(<Settings />);
    await waitFor(() => qa("section.panel").length >= 12);
    expect(text()).toContain("Runtime settings");
  });
});
