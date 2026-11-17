import { describe, expect, it, vi } from "vitest";
import { shellInfoDecoder } from "./desktop";
import { fakeApi, ok } from "./testing/api";
import { button, click, mount, qa, text, waitFor } from "./testing/dom";
import { FilesSection } from "./pages/settings/Files";

const info = (over: Record<string, unknown> = {}) => ({
  version: "0.0.1", buildId: "abc", mode: "in-process", pid: 4242, packaged: true,
  notificationsPermitted: true, logDir: "/Users/example/Applysmith/.applysmith/logs", startAtLogin: false,
  extensionDir: "/Users/example/Library/Application Support/Applysmith/extension", ...over,
});

// shell installs a stand-in for the bridge the desktop app injects.
function shell(handlers: Record<string, (...args: unknown[]) => unknown> = {}) {
  const calls: [string, unknown[]][] = [];
  let login = false;
  const all: Record<string, (...args: unknown[]) => unknown> = {
    "Shell.GetShellInfo": () => info({ startAtLogin: login }),
    "Shell.SetStartAtLogin": (on) => void (login = on as boolean),
    "Shell.RevealInFinder": () => undefined,
    ...handlers,
  };
  vi.stubGlobal("mygo", {
    call: async (method: string, ...args: unknown[]) => {
      calls.push([method, args]);
      const h = all[method];
      if (!h) throw new Error("unknown method");
      return h(...args);
    },
  });
  return { calls, setLogin: (v: boolean) => void (login = v) };
}

const filesApi = () => fakeApi({ "GET /api/system/info": () => ok({ workspacePath: "/Users/example/Applysmith" }), "GET /api/config/profile": () => ok({ file: "profile", exists: true, template: false, content: "version: 1\n", revision: "r1", issues: [] }) });

describe("desktop shell presence", () => {
  it("decodes the shell info and refuses a field the dashboard does not know", () => {
    expect(shellInfoDecoder(info(), "$").startAtLogin).toBe(false);
    expect(() => shellInfoDecoder({ ...info(), extra: 1 }, "$")).toThrow();
  });
});

describe("Settings Files in the desktop app", () => {
  it("offers no shell controls in a browser and makes no native call", async () => {
    filesApi();
    await mount(<FilesSection />);
    await waitFor(() => text().includes("/Users/example/Applysmith"));
    expect(text()).not.toContain("Reveal in Finder");
    expect(text()).not.toContain("log in");
    expect(qa("input[type=checkbox]")).toHaveLength(0);
  });

  it("reveals the workspace folder", async () => {
    filesApi();
    const { calls } = shell();
    await mount(<FilesSection />);
    await click(await waitFor(() => button("Reveal in Finder")));
    expect(calls.filter(([m]) => m === "Shell.RevealInFinder")).toEqual([["Shell.RevealInFinder", ["/Users/example/Applysmith"]]]);
  });

  it("shows the start-at-login state the system reports and changes it", async () => {
    filesApi();
    const { calls } = shell();
    await mount(<FilesSection />);
    const box = await waitFor(() => qa<HTMLInputElement>("input[type=checkbox]")[0]);
    await waitFor(() => !box.disabled);
    expect(box.checked).toBe(false);
    await click(box);
    expect(calls.filter(([m]) => m === "Shell.SetStartAtLogin")).toEqual([["Shell.SetStartAtLogin", [true]]]);
    await waitFor(() => qa<HTMLInputElement>("input[type=checkbox]")[0].checked);
  });

  it("reports a refusal from the system and shows the real state", async () => {
    filesApi();
    shell({ "Shell.SetStartAtLogin": () => { throw new Error("login item not allowed"); } });
    await mount(<FilesSection />);
    const box = await waitFor(() => qa<HTMLInputElement>("input[type=checkbox]")[0]);
    await waitFor(() => !box.disabled);
    await click(box);
    await waitFor(() => text().includes("login item not allowed"));
    expect(qa<HTMLInputElement>("input[type=checkbox]")[0].checked).toBe(false);
  });
});
