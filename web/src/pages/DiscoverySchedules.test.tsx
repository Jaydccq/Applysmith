import { describe, expect, it, vi } from "vitest";
import { writesSent } from "../api/client";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, byText, click, mount, openMore, q, qa, text, type, waitFor } from "../testing/dom";
import { schedule, sources } from "../testing/fixtures_discovery";
import Discovery from "./Discovery";
import { SECTIONS } from "./Settings";

const open = (search = "?tab=schedules") => window.history.replaceState(null, "", "/discovery" + search);

const kinds = {
  kinds: [
    { name: "discover.registry", title: "Scan Company job boards", doc: "Scan the registry.", leases: [], network: true, inputs: [{ name: "limit", type: "int", min: 1, max: 100 }, { name: "mode", type: "string", enum: ["auto", "screen", "deep"] }, { name: "retry", type: "bool" }] },
    { name: "cost.report", title: "Model cost report", doc: "Write the cost report.", leases: [], network: false, inputs: [] },
  ],
};

const proposal = { id: "prop-1", scheduleId: 1, scheduleName: "Morning scan", reason: "Mornings are busy.", change: { enabled: false }, createdAt: "2026-10-12T08:00:00.000Z" };

function server(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}, items = [schedule()]): FakeApi {
  return fakeApi({
    "GET /api/scan-sources": () => ok({ sources }),
    "GET /api/schedules": () => ok({ schedules: items, timers: [{ name: "gmail-digest", description: "Gmail digests", active: true }] }),
    "GET /api/schedule-kinds": () => ok(kinds),
    "GET /api/agent/schedule-proposals": () => ok({ proposals: [] }),
    ...extra,
  });
}

describe("Discovery: Schedules", () => {
  it("lists schedules with their recurrence, next fire and the notice that none can submit", async () => {
    open();
    vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-10-12T21:30:00.000Z"));
    server();
    await mount(<Discovery />);
    await waitFor(() => text().includes("Morning scan"));
    const row = q("table.schedules tbody tr")!;
    // How it repeats reads under the schedule's name.
    expect(row.querySelector('[data-label="Schedule"] .sub')!.textContent).toBe("Every day at 07:00 (UTC)");
    expect(row.querySelector('[data-label="Next run"]')!.textContent).toBe("2026-10-13 00:00in 9 h 30 min");
    expect(row.querySelector('[data-label="Last run"]')!.textContent).toBe("Never");
    expect(text()).toContain("1 schedule.");
    expect(text()).toContain("No schedule can submit an application.");
    expect(text()).toContain("Built-in timers (1)");
    // Settings search finds this tab by these words.
    for (const t of SECTIONS.find((s) => s.to === "/discovery?tab=schedules")!.terms) expect(text()).toContain(t);
  });

  it("pauses and resumes a schedule", async () => {
    open();
    const api = server({ "PATCH /api/schedules/1": () => ok(schedule({ enabled: false })) });
    await mount(<Discovery />);
    await waitFor(() => q('tbody input[type="checkbox"]'));
    await click(q('tbody input[type="checkbox"]'));
    expect(api.callsTo("PATCH /api/schedules/1")[0].body).toEqual({ enabled: false });
  });

  it("pauses every schedule at once and resumes only what that paused", async () => {
    open();
    let paused = 0;
    const api = server({
      "GET /api/schedules": () => ok({ schedules: [schedule({ enabled: paused === 0, pausedBy: paused ? "all" : undefined }), schedule({ id: 2, name: "Night scan", enabled: false })], timers: [], pausedAll: paused }),
      "POST /api/schedules/pause-all": () => { paused = 1; return ok({ changed: ["Morning scan"] }); },
      "POST /api/schedules/resume-all": () => { paused = 0; return ok({ changed: ["Morning scan"] }); },
    });
    await mount(<Discovery />);
    await waitFor(() => button("Pause all"));
    expect(button("Resume all")!.disabled).toBe(true);
    await click(button("Pause all"));
    expect(api.callsTo("POST /api/schedules/pause-all")).toHaveLength(1);
    // It names what it paused, apart from the schedule that was off already.
    await waitFor(() => text().includes("Paused 1 schedule: Morning scan."));
    await waitFor(() => q("table.schedules tbody tr")!.textContent!.includes("By Pause all"));
    expect(qa("table.schedules tbody tr")[1].textContent).not.toContain("By Pause all");
    await waitFor(() => button("Resume all (1)") && !button("Resume all (1)")!.disabled);
    expect(button("Pause all")!.disabled).toBe(true);
    await click(button("Resume all (1)"));
    expect(api.callsTo("POST /api/schedules/resume-all")).toHaveLength(1);
    await waitFor(() => text().includes("Resumed 1 schedule: Morning scan."));
  });

  it("runs a schedule now and says it started", async () => {
    open();
    const api = server({ "POST /api/schedules/1/run": () => ok({ runId: "run_9" }) });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Run Morning scan now$/));
    await click(byLabel(/^Run Morning scan now$/));
    expect(api.callsTo("POST /api/schedules/1/run")).toHaveLength(1);
    expect(text()).toContain("Morning scan started.");
    expect(byText("a", "Follow the run")?.getAttribute("href")).toBe("/runs/run_9");
  });

  it("shows a refusal to run now", async () => {
    open();
    server({ "POST /api/schedules/1/run": () => fail("conflict", "the schedule is already running") });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Run Morning scan now$/));
    await click(byLabel(/^Run Morning scan now$/));
    expect(q('[role="alert"]')?.textContent).toContain("already running");
  });

  it("asks before deleting a schedule", async () => {
    open();
    const api = server({ "DELETE /api/schedules/1": () => ok({ schedules: [], timers: [] }) });
    await mount(<Discovery />);
    await openMore("Morning scan");
    await click(byLabel(/^Delete Morning scan$/));
    expect(api.callsTo("DELETE /api/schedules/1")).toHaveLength(0);
    await click(qa("dialog button").find((b) => b.textContent === "Delete")!);
    expect(api.callsTo("DELETE /api/schedules/1")).toHaveLength(1);
  });
});

describe("Discovery: schedule editor", () => {
  const mondays = ["2026-10-19", "2026-10-26", "2026-11-02", "2026-11-09", "2026-11-16"].map((d) => `${d}T07:00:00.000Z`);

  const saveReady = () => waitFor(() => !button("Save schedule")!.disabled, "the preview to enable Save");

  it("previews the next five fire times of the recurrence on the form before Save is enabled", async () => {
    open("?tab=schedules&edit=new");
    // The preview answers only when released, so "Checking" holds however slow the machine is.
    let release = () => {};
    let held = new Promise<void>((r) => (release = r));
    const api = server({ "POST /api/schedules/preview": async () => (await held, ok({ next: mondays })), "POST /api/schedules": () => ok(schedule({ id: 3 })) });
    await mount(<Discovery />);
    await waitFor(() => q("dialog form"));
    const writes = writesSent();
    await type(byText("label", /^Name/)!.querySelector("input"), "Monday sweep");
    await type(byText("label", /^Repeats/)!.querySelector("select"), "weekly");
    await type(byText("label", /^Time$/)!.querySelector("input"), "07:00");
    await type(byText("label", /^Time zone/)!.querySelector("input"), "UTC");
    expect(button("Save schedule")!.disabled).toBe(true);
    expect(q("dialog")!.textContent).toContain("Checking when it runs");
    release();
    await saveReady();
    // A preview changes nothing, so no read kept by another page counts as old after it.
    expect(writesSent()).toBe(writes);
    expect(api.callsTo("POST /api/schedules/preview").at(-1)!.body).toEqual({ preset: { kind: "weekly", time: "07:00", weekdays: ["mon"], timezone: "UTC" } });
    expect(qa('ol[aria-label="Next fire times"] li')).toHaveLength(5);
    expect(q("dialog")!.textContent).toContain("Shown on this computer's clock");
    // Changing the recurrence makes the preview stale until it is asked again.
    held = new Promise<void>((r) => (release = r));
    await type(byText("label", /^Time$/)!.querySelector("input"), "08:30");
    expect(button("Save schedule")!.disabled).toBe(true);
    release();
    await saveReady();
    expect(api.callsTo("POST /api/schedules/preview").at(-1)!.body).toEqual({ preset: { kind: "weekly", time: "08:30", weekdays: ["mon"], timezone: "UTC" } });
    await click(button("Save schedule"));
    expect(api.callsTo("POST /api/schedules")[0].body).toEqual({
      name: "Monday sweep", kind: "discover.registry", inputs: {}, preset: { kind: "weekly", time: "08:30", weekdays: ["mon"], timezone: "UTC" }, enabled: true, missedPolicy: "skip",
    });
    await waitFor(() => window.location.search === "?tab=schedules");
  });

  it("sends the inputs the chosen job declares with their types", async () => {
    open("?tab=schedules&edit=new");
    const api = server({ "POST /api/schedules/preview": () => ok({ next: mondays }), "POST /api/schedules": () => ok(schedule({ id: 3 })) });
    await mount(<Discovery />);
    await waitFor(() => q("dialog form"));
    await type(byText("label", /^Name/)!.querySelector("input"), "Registry");
    await type(byText("label", /^Limit/)!.querySelector("input"), "25");
    await type(byText("label", /^Mode/)!.querySelector("select"), "deep");
    await click(byText("label", /Retry/)!.querySelector("input"));
    await type(byText("label", /^Time zone/)!.querySelector("input"), "UTC");
    await saveReady();
    await click(button("Save schedule"));
    expect((api.callsTo("POST /api/schedules")[0].body as { inputs: unknown }).inputs).toEqual({ limit: 25, mode: "deep", retry: true });
  });

  it("names a recurrence the server refuses and keeps save disabled", async () => {
    open("?tab=schedules&edit=new");
    server({ "POST /api/schedules/preview": () => fail("validation_failed", "time \"25:00\" must be HH:MM", { details: { field: "preset" } }) });
    await mount(<Discovery />);
    await waitFor(() => q('dialog [role="alert"]'));
    expect(q('dialog [role="alert"]')!.textContent).toContain("must be HH:MM");
    expect(button("Save schedule")!.disabled).toBe(true);
  });
});

describe("Discovery: suggested schedule changes", () => {
  it("lists what the Ask Agent suggested and accepts it", async () => {
    open();
    let pending = [proposal];
    const api = server({
      "GET /api/agent/schedule-proposals": () => ok({ proposals: pending }),
      "POST /api/agent/schedule-proposals/prop-1/accept": () => {
        pending = [];
        return ok({ id: "prop-1", accepted: true, schedule: schedule({ enabled: false }) });
      },
    });
    await mount(<Discovery />);
    await waitFor(() => text().includes("Mornings are busy."));
    expect(q("#schedule-proposals")!.closest("section")!.textContent).toContain("Morning scan: pause it");
    await click(byLabel(/^Accept the change to Morning scan$/));
    expect(api.callsTo("POST /api/agent/schedule-proposals/prop-1/accept")).toHaveLength(1);
    await waitFor(() => text().includes("Accepted the change to Morning scan."));
    expect(text()).toContain("no other schedule change waiting");
  });

  it("dismisses a suggestion without changing the schedule, and its Undo brings it back", async () => {
    open();
    const daily = { ...proposal, change: { preset: { kind: "daily", time: "09:00", timezone: "UTC" } } };
    let pending = [daily];
    const api = server({
      "GET /api/agent/schedule-proposals": () => ok({ proposals: pending }),
      "POST /api/agent/schedule-proposals/prop-1/dismiss": () => {
        pending = [];
        return ok({ id: "prop-1", accepted: false });
      },
      "POST /api/agent/schedule-proposals/prop-1/restore": () => {
        pending = [daily];
        return ok(daily);
      },
    });
    await mount(<Discovery />);
    await waitFor(() => text().includes("run it every day at 09:00 (UTC)"));
    await click(byLabel(/^Dismiss the change to Morning scan$/));
    expect(api.callsTo("POST /api/agent/schedule-proposals/prop-1/dismiss")).toHaveLength(1);
    expect(api.callsTo("PATCH /api/schedules/1")).toHaveLength(0);
    await waitFor(() => !byLabel(/^Dismiss the change to Morning scan$/));
    await click(button("Undo"));
    await waitFor(() => byLabel(/^Dismiss the change to Morning scan$/));
  });

  it("keeps a suggestion the scheduler refuses and shows why", async () => {
    open();
    server({
      "GET /api/agent/schedule-proposals": () => ok({ proposals: [proposal] }),
      "POST /api/agent/schedule-proposals/prop-1/accept": () => fail("validation_failed", "the rule has no occurrence within the next 366 days"),
    });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Accept the change to Morning scan$/));
    await click(byLabel(/^Accept the change to Morning scan$/));
    expect(q('[role="alert"]')?.textContent).toContain("no occurrence within the next 366 days");
    expect(text()).toContain("Mornings are busy.");
  });
});
