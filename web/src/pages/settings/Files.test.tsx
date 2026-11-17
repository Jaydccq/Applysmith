import { act } from "react";
import { describe, expect, it } from "vitest";
import type { Board, BoardState } from "../../api/board";
import { BoardContext } from "../../hooks";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, click, mount, q, settle, text, type, waitFor } from "../../testing/dom";
import { FilesSection } from "./Files";

const doc = (file: string, content: string, revision: string, issues: unknown[] = []) => ({ file, exists: true, template: false, content, revision, issues });

function server() {
  const files: Record<string, { content: string; rev: number }> = {
    profile: { content: "version: 1\nname: Alex Example\n", rev: 1 },
    portals: { content: "version: 1\n", rev: 1 },
    policy: { content: "version: 1\n", rev: 1 },
    cv: { content: "# Alex Example\n", rev: 1 },
  };
  const api = fakeApi({ "GET /api/system/info": () => ok({ workspacePath: "/Users/example/Applysmith" }) });
  for (const f of Object.keys(files)) {
    api.on(`GET /api/config/${f}`, () => ok(doc(f, files[f].content, `r${files[f].rev}`)));
    api.on(`PUT /api/config/${f}`, (c) => {
      const b = c.body as { content: string; expectedRevision: string };
      if (b.expectedRevision !== `r${files[f].rev}`) return fail("revision_conflict", `${f} changed on disk; reload it and apply the change again`);
      files[f] = { content: b.content, rev: files[f].rev + 1 };
      return ok({ revision: `r${files[f].rev}`, changed: true });
    });
  }
  return { api, files };
}

const editor = () => q<HTMLTextAreaElement>("textarea")!;

describe("Settings: files", () => {
  it("saves an edit with the revision it read and then shows it saved", async () => {
    const { api, files } = server();
    await mount(<FilesSection />);
    await waitFor(() => editor().value.includes("Alex Example"));
    await type(editor(), "version: 1\nname: Sam Example\n");
    expect(text()).toContain("Unsaved changes.");
    await click(button("Save profile.yml"));
    expect(api.callsTo("PUT /api/config/profile")[0].body).toEqual({ content: "version: 1\nname: Sam Example\n", expectedRevision: "r1" });
    await waitFor(() => text().includes("Saved at"));
    expect(files.profile.content).toContain("Sam Example");
    expect(button("Save profile.yml")!.disabled).toBe(true);
  });

  it("shows the findings the server names when it refuses a file", async () => {
    const { api } = server();
    api.on("PUT /api/config/profile", () => fail("validation_failed", "profile.yml:2:1 bogus: unknown_key: unknown key"));
    await mount(<FilesSection />);
    await waitFor(() => editor().value.includes("Alex Example"));
    await type(editor(), "version: 1\nbogus: 1\n");
    await click(button("Save profile.yml"));
    await waitFor(() => text().includes("bogus: unknown_key"));
    expect(editor().value).toBe("version: 1\nbogus: 1\n");
  });

  it("does not overwrite a file that changed on disk; keeps the text or reloads, as the user picks", async () => {
    const { files } = server();
    await mount(<FilesSection />);
    await waitFor(() => editor().value.includes("Alex Example"));
    await type(editor(), "version: 1\nname: Sam Example\n");
    const conflict = async (rev: number) => {
      files.profile = { content: "version: 1\nname: Changed Elsewhere\n", rev };
      await click(button("Save profile.yml"));
      await waitFor(() => button("Keep my text"));
      expect(files.profile.content).toContain("Changed Elsewhere");
    };
    await conflict(2);
    await click(button("Keep my text"));
    await waitFor(() => !button("Keep my text"));
    await click(button("Save profile.yml"));
    await waitFor(() => text().includes("Saved at"));
    expect(files.profile.content).toContain("Sam Example");
    await type(editor(), "version: 1\nname: Kim Example\n");
    await conflict(9);
    await click(button("Load the new version"));
    await waitFor(() => editor().value.includes("Changed Elsewhere"));
  });

  it("follows a change made elsewhere while the text holds no edit, and keeps an edit whose save is then refused", async () => {
    const { api, files } = server();
    let state: BoardState = { revisions: null, epoch: null, reconnecting: false, stale: false, links: 0 };
    const listeners = new Set<() => void>();
    const board: Board = { getState: () => state, subscribe: (l) => (listeners.add(l), () => listeners.delete(l)), start() {}, stop() {}, retryNow() {} };
    const announce = (configuration: number) => act(async () => ((state = { ...state, revisions: { configuration }, epoch: "aaaa" }), listeners.forEach((l) => l())));
    await mount(
      <BoardContext.Provider value={board}>
        <FilesSection />
      </BoardContext.Provider>,
    );
    await waitFor(() => editor().value.includes("Alex Example"));
    await announce(1);
    await settle();
    files.profile = { content: "version: 1\nname: Changed Elsewhere\n", rev: 2 };
    await announce(2);
    await waitFor(() => editor().value.includes("Changed Elsewhere"));
    await type(editor(), "version: 1\nname: Sam Example\n");
    files.profile = { content: "version: 1\nname: Changed Again\n", rev: 3 };
    await announce(3);
    await waitFor(() => api.callsTo("GET /api/config/profile").length === 3);
    await settle();
    expect(editor().value).toContain("Sam Example");
    await click(button("Save profile.yml"));
    await waitFor(() => button("Keep my text"));
    expect((api.callsTo("PUT /api/config/profile")[0].body as { expectedRevision: string }).expectedRevision).toBe("r2");
    expect(files.profile.content).toContain("Changed Again");
  });

  it("asks before leaving a file with unsaved edits, and opens policy.yml, portals.yml and cv.md", async () => {
    const { api } = server();
    await mount(<FilesSection />);
    await waitFor(() => editor().value.includes("Alex Example"));
    await type(editor(), "version: 1\nname: Sam Example\n");
    await click(button("cv.md"));
    expect(q("dialog")!.textContent).toContain("Leave without saving?");
    await click(Array.from(q("dialog")!.querySelectorAll("button")).find((b) => b.textContent === "Cancel")!);
    expect(editor().value).toContain("Sam Example");
    await click(button("cv.md"));
    await click(Array.from(q("dialog")!.querySelectorAll("button")).find((b) => b.textContent === "Leave")!);
    await waitFor(() => editor().value.startsWith("# Alex Example"));
    for (const [label, file] of [["policy.yml", "policy"], ["portals.yml", "portals"]]) {
      await click(button(label));
      await waitFor(() => api.callsTo(`GET /api/config/${file}`).length > 0);
    }
  });
});
