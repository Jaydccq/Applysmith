import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { byText, button, click, mount, q, text, waitFor } from "../../testing/dom";
import { setupApi, setupState } from "../../testing/fixtures_setup";
import { DemoBanner } from "../../ui/DemoBanner";
import Setup from "../Setup";

describe("Demo workspace", () => {
  it("offers Try the demo on Setup, explains it is a separate folder, and says how to open the result", async () => {
    const api = setupApi(setupState({ identity: { status: "partial" } }), {
      "POST /api/setup/demo": () =>
        ok({ workspace: "/tmp/demo/Applysmith-Demo", created: ["profile.yml"], postings: 3, notes: [], start: "applysmith serve --workspace /tmp/demo/Applysmith-Demo" }),
    });
    await mount(<Setup />);
    const panel = await waitFor(() => q("section[aria-labelledby='try-demo']"));
    expect(panel.textContent).toContain("separate folder");
    expect(panel.textContent).toContain("never touches your own");
    await click(button("Create the demo workspace"));
    await waitFor(() => byText("code", "applysmith serve --workspace /tmp/demo/Applysmith-Demo"));
    expect(api.callsTo("POST /api/setup/demo")).toHaveLength(1);
    expect(panel.textContent).toContain("3 new sample posting(s)");
    expect(panel.textContent).toContain("Your own workspace is unchanged");
    expect(panel.textContent).toContain("stop this server");
    expect(panel.querySelector("button[aria-label='Copy the command that starts the demo']")).not.toBeNull();
    expect(button("Open the demo workspace")).toBeNull();
    expect(button("Create the demo workspace")).toBeNull();
  });

  describe("inside the desktop app", () => {
    const inApp = (call: ReturnType<typeof vi.fn>) => vi.stubGlobal("mygo", { call });

    it("opens the created demo in this window through the app, and stays busy while the app replaces the page", async () => {
      const call = vi.fn().mockResolvedValue("started");
      inApp(call);
      setupApi(setupState({ identity: { status: "partial" } }), {
        "POST /api/setup/demo": () => ok({ workspace: "/tmp/demo/Applysmith-Demo", created: [], postings: 3, notes: [], start: "applysmith serve --workspace /tmp/demo/Applysmith-Demo" }),
      });
      await mount(<Setup />);
      await click(await waitFor(() => button("Create the demo workspace")));
      await waitFor(() => button("Open the demo workspace"));
      expect(text()).not.toContain("stop this server");
      await click(button("Open the demo workspace"));
      expect(call).toHaveBeenCalledWith("Shell.SwitchWorkspace", "demo");
      await waitFor(() => button("Switching…")?.disabled === true);
    });

    it("gives the button back when the user kept running work, and says why a refusal happened", async () => {
      const call = vi.fn().mockResolvedValueOnce("cancelled").mockRejectedValueOnce(new Error("demo_not_found"));
      inApp(call);
      fakeApi({ "GET /api/setup/status": () => ok({ demo: true }) });
      await mount(<DemoBanner />);
      await click(await waitFor(() => button("Back to my workspace")));
      expect(call).toHaveBeenCalledWith("Shell.SwitchWorkspace", "home");
      await waitFor(() => button("Back to my workspace")?.disabled === false);
      await click(button("Back to my workspace"));
      await waitFor(() => q("[role='alert']")?.textContent?.includes("Create it again from Setup") === true);
      expect(button("Back to my workspace")?.disabled).toBe(false);
    });
  });

  describe("Try autofill on the sample form", () => {
    const demo = () => setupApi(setupState({}), { "GET /api/setup/status": () => ok({ demo: true }), "POST /api/apply/runs": () => ({ data: { runId: "run_77" }, status: 202 }) });

    it("starts a dry run on this server's sample form and links the run", async () => {
      const api = demo();
      await mount(<Setup />);
      await click(await waitFor(() => button("Try autofill on the sample form")));
      const call = await waitFor(() => api.callsTo("POST /api/apply/runs")[0]);
      // Named after the sample posting, so Runs and Home do not read "Apply to 127.0.0.1".
      expect(call.body).toEqual({ url: `${window.location.origin}/demo/sample-application`, company: "Harborlight Analytics", role: "Senior Platform Engineer", submit: false, confirmed: false });
      const link = await waitFor(() => byText("a", "Follow the run"));
      expect(link.getAttribute("href")).toBe("/runs/run_77");
      expect(q("#demo-tutorial p[role='status']")?.textContent).toContain("never submitted");
    });

    it("shows why the run did not start", async () => {
      const api = demo();
      api.on("POST /api/apply/runs", () => fail("conflict", "an application run is driving the apply window; wait for it to finish", { details: { reason: "lane-busy" } }));
      await mount(<Setup />);
      await click(await waitFor(() => button("Try autofill on the sample form")));
      await waitFor(() => text().includes("wait for it to finish"));
      expect(byText("a", "Follow the run")).toBeNull();
    });
  });
});
