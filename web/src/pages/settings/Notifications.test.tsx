import { act } from "react";
import { describe, expect, it } from "vitest";
import { BoardContext } from "../../hooks";
import { fail, fakeApi, manualBoard, ok } from "../../testing/api";
import { button, click, mount, q, qa, settle, submit, text, type, waitFor } from "../../testing/dom";
import type { ConfigDocument } from "../../wire";
import { NotificationsSection } from "./Notifications";

const settings = { desktop: true, telegram: true, failedScans: false, telegramPaired: true };

function profile(revision: string, quiet?: Record<string, unknown>): ConfigDocument {
  return { file: "profile", exists: true, template: false, content: "", revision, issues: [], values: quiet ? { notifications: { quiet_hours: quiet } } : {} };
}

function server(doc = profile("r1")) {
  return fakeApi({
    "GET /api/notifications/settings": () => ok(settings),
    "PATCH /api/notifications/settings": (c) => ok({ ...settings, ...(c.body as object) }),
    "GET /api/config/profile": () => ok(doc),
    "PATCH /api/config/profile": () => ok({ revision: "r2", changed: true }),
    "POST /api/notifications/test": () =>
      ok({ channels: [{ channel: "desktop", state: "sent", detail: "Sent to the Applysmith app." }, { channel: "telegram", state: "failed", detail: "Telegram refused the bot token. Save a new token from BotFather in the Telegram section." }] }),
  });
}

// changedElsewhere mounts the section, lets the test edit, then serves doc as
// the profile another writer left and announces it.
async function changedElsewhere(api: ReturnType<typeof server>, doc: ConfigDocument, edit: () => Promise<void>) {
  const board = manualBoard();
  await mount(
    <BoardContext.Provider value={board}>
      <NotificationsSection />
    </BoardContext.Provider>,
  );
  await waitFor(() => field("Start"));
  await act(async () => board.set({ configuration: 1 }));
  await settle();
  await edit();
  api.on("GET /api/config/profile", () => ok(doc));
  await act(async () => board.set({ configuration: 2 }));
  await waitFor(() => api.callsTo("GET /api/config/profile").length === 2);
  await settle();
}

const field = (label: string) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;

describe("Settings: notifications", () => {
  it("patches only the switch that was flipped", async () => {
    const api = server();
    await mount(<NotificationsSection />);
    await click(await waitFor(() => field("Announce failed")));
    await waitFor(() => api.callsTo("PATCH /api/notifications/settings").length === 1);
    expect(api.callsTo("PATCH /api/notifications/settings")[0].body).toEqual({ failedScans: true });
  });

  it("sends a test and says per channel what happened and what to do", async () => {
    const api = server();
    await mount(<NotificationsSection />);
    await click(await waitFor(() => button("Send a test notification")));
    await waitFor(() => text().includes("Telegram refused the bot token"));
    expect(api.callsTo("POST /api/notifications/test")).toHaveLength(1);
    expect(text()).toContain("Sent to the Applysmith app.");
    expect(qa(".chip").map((c) => c.textContent)).toEqual(expect.arrayContaining(["sent", "failed"]));
  });
});

describe("Settings: quiet hours", () => {
  it("fills the form from the profile and saves the group with the revision it was loaded at", async () => {
    const api = server(profile("r1", { enabled: false, start: "22:00", end: "08:00", timezone: "" }));
    await mount(<NotificationsSection />);
    await waitFor(() => field("Start"));
    expect(field("Start").value).toBe("22:00");
    expect(field("End").value).toBe("08:00");
    await click(field("Hold normal"));
    // The zone is picked from the zones this browser knows, as on a schedule.
    const zones = Array.from(field("Time zone").list!.options).map((o) => o.value);
    expect(zones).toEqual(Intl.supportedValuesOf("timeZone"));
    await type(field("Time zone"), "Europe/London");
    await submit(q("form.form-grid"));
    await waitFor(() => api.callsTo("PATCH /api/config/profile").length === 1);
    const body = api.callsTo("PATCH /api/config/profile")[0].body as { expectedRevision: string; ops: { op: string; path: string; value: string }[] };
    expect(body.expectedRevision).toBe("r1");
    expect(body.ops).toHaveLength(1);
    expect(body.ops[0]).toMatchObject({ op: "set", path: "notifications.quiet_hours" });
    expect(body.ops[0].value).toContain("enabled: true");
    expect(body.ops[0].value).toContain('start: "22:00"');
    expect(body.ops[0].value).toContain('timezone: "Europe/London"');
    await waitFor(() => text().includes("Saved at"));
  });

  it("opens a dialog with both versions on a revision conflict and applies the edit again on the new revision", async () => {
    const api = server(profile("r1", { enabled: false, start: "22:00", end: "08:00" }));
    let first = true;
    api.on("PATCH /api/config/profile", () => {
      if (first) {
        first = false;
        return fail("revision_conflict", "the profile changed since it was loaded");
      }
      return ok({ revision: "r4", changed: true });
    });
    await mount(<NotificationsSection />);
    await waitFor(() => field("Start"));
    await click(field("Hold normal"));
    api.on("GET /api/config/profile", () => ok(profile("r3", { enabled: true, start: "23:00", end: "07:00" })));
    await submit(q("form.form-grid"));
    await click(await waitFor(() => button("Compare with the current version")));
    await waitFor(() => q("dialog"));
    expect(q("dialog")?.textContent).toContain("on, 23:00 to 07:00");
    expect(q("dialog")?.textContent).toContain("Your edit: on, 22:00 to 08:00");
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Apply my edit again")!);
    await waitFor(() => api.callsTo("PATCH /api/config/profile").length === 2);
    expect((api.callsTo("PATCH /api/config/profile")[1].body as { expectedRevision: string }).expectedRevision).toBe("r3");
    await waitFor(() => text().includes("Saved at"));
  });

  it("keeps an edit over a change made elsewhere to quiet hours, and its save quotes the revision the edit was made at", async () => {
    const api = server(profile("r1", { enabled: true, start: "22:00", end: "08:00" }));
    await changedElsewhere(api, profile("r2", { enabled: true, start: "23:00", end: "07:00" }), () => type(field("Start"), "21:00"));
    expect(field("Start").value).toBe("21:00");
    await submit(q("form.form-grid"));
    await waitFor(() => api.callsTo("PATCH /api/config/profile").length === 1);
    expect((api.callsTo("PATCH /api/config/profile")[0].body as { expectedRevision: string }).expectedRevision).toBe("r1");
  });

  it("saves an edit at the new revision when another part of the profile changed under it", async () => {
    const api = server(profile("r1", { enabled: true, start: "22:00", end: "08:00" }));
    await changedElsewhere(api, profile("r2", { enabled: true, start: "22:00", end: "08:00" }), () => type(field("Start"), "21:00"));
    expect(field("Start").value).toBe("21:00");
    await submit(q("form.form-grid"));
    await waitFor(() => api.callsTo("PATCH /api/config/profile").length === 1);
    expect((api.callsTo("PATCH /api/config/profile")[0].body as { expectedRevision: string }).expectedRevision).toBe("r2");
  });
});
