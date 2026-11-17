import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, click, mount, q, text, waitFor } from "../../testing/dom";
import { AdvancedSection } from "./Advanced";

const health = { status: "ok", apiVersion: 1, version: "0.1.0", buildId: "abc1234", workspaceId: "0123456789ab", startedAt: "2026-10-12T09:00:00Z", schemaVersion: 36 };

function server() {
  return fakeApi({
    "GET /api/health": () => ok(health),
    "POST /api/setup/doctor": () =>
      ok({
        platform: "Applysmith supports macOS 13 or later. Other operating systems are unverified.",
        results: [
          { id: "runtime.port", status: "pass", required: true, summary: "port 47319 is held by this workspace's server" },
          { id: "browser.chrome", status: "fail", required: false, summary: "Google Chrome is not installed", fix: "install Google Chrome" },
          { id: "llm.provider", status: "fail", required: true, summary: "no provider is configured", fix: "run `applysmith llm configure`", step: "llm" },
        ],
      }),
    "GET /api/system/cli": () => ok({ available: true, state: "not-installed", link: "/Users/alex/.local/bin/applysmith", target: "/Applications/Applysmith.app/Contents/MacOS/Applysmith", onPath: false, profileFile: "~/.zshrc", profileLine: "x" }),
    "GET /api/system/logs": () => ok({ lines: ["level=INFO msg=started", "level=WARN msg=leak value=[redacted]"] }),
    "GET /api/system/diagnostics": () => new Response(new Blob(["{}"], { type: "application/json" }), { status: 200 }),
  });
}

describe("Settings: advanced", () => {
  it("runs the doctor on the press and lists each result with its fix", async () => {
    const api = server();
    await mount(<AdvancedSection />);
    expect(api.callsTo("POST /api/setup/doctor")).toHaveLength(0);
    await click(button("Run doctor"));
    await waitFor(() => text().includes("Google Chrome is not installed"));
    expect(text()).toContain("pass");
    expect(text()).toContain("Fix: install Google Chrome");
    expect(text()).not.toContain("applysmith llm configure");
    expect(q<HTMLAnchorElement>("a.doctor-fix")?.getAttribute("href")).toBe("/setup?step=llm");
  });

  it("says why the doctor cannot run", async () => {
    const api = server();
    api.on("POST /api/setup/doctor", () => fail("unavailable", "the doctor is not available in this build"));
    await mount(<AdvancedSection />);
    await click(button("Run doctor"));
    await waitFor(() => text().includes("the doctor is not available in this build"));
  });
});
