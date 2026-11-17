import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, click, mount, q, text, waitFor } from "../../testing/dom";
import { CommandLine } from "./CommandLine";

const link = "/Users/alex/.local/bin/applysmith";
const view = (over: Record<string, unknown>) => ({
  available: true,
  state: "not-installed",
  link,
  target: "/Applications/Applysmith.app/Contents/MacOS/Applysmith",
  onPath: false,
  profileFile: "~/.zshrc",
  profileLine: 'export PATH="$HOME/.local/bin:$PATH"',
  ...over,
});

describe("Settings: command line", () => {
  it("installs the link, then shows the one profile line when the folder is not on PATH", async () => {
    const api = fakeApi({
      "GET /api/system/cli": () => ok(view({})),
      "PUT /api/system/cli": () => ok(view({ state: "installed" })),
    });
    await mount(<CommandLine />);
    await waitFor(() => text().includes("Install the command"));
    expect(text()).not.toContain("is not on your PATH");
    await click(button("Install the command"));
    await waitFor(() => text().includes("is not on your PATH"));
    expect(api.callsTo("PUT /api/system/cli")).toHaveLength(1);
    expect(text()).toContain("~/.zshrc");
    expect(q('pre[aria-label="Line to add to the shell profile"]')!.textContent).toBe('export PATH="$HOME/.local/bin:$PATH"');
    expect(text()).toContain("does not edit your shell files");
  });

  it("says the folder is on PATH and offers removal", async () => {
    const api = fakeApi({
      "GET /api/system/cli": () => ok(view({ state: "installed", onPath: true })),
      "DELETE /api/system/cli": () => ok(view({ state: "not-installed", onPath: true })),
    });
    await mount(<CommandLine />);
    await waitFor(() => text().includes("is on your PATH"));
    expect(text()).not.toContain("Install the command");
    await click(button("Remove the command"));
    await waitFor(() => text().includes("Install the command"));
    expect(api.callsTo("DELETE /api/system/cli")).toHaveLength(1);
  });

  it("shows the server's refusal when a file appeared since the page loaded", async () => {
    fakeApi({
      "GET /api/system/cli": () => ok(view({})),
      "PUT /api/system/cli": () => fail("conflict", `${link} already exists and is not the applysmith link; it was left as it is`),
    });
    await mount(<CommandLine />);
    await waitFor(() => text().includes("Install the command"));
    await click(button("Install the command"));
    await waitFor(() => text().includes("was left as it is"));
  });
});
