import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { button, byLabel, byText, click, mount, openMore, q, qa, settle, text, type as typeInto, waitFor } from "../testing/dom";
import { applyList } from "../testing/fixtures";
import ApplyNext from "./ApplyNext";

const stamp = "2026-10-12T09:00:00.000Z";

function list(items: Record<string, unknown>[], counts: Record<string, number> = { idle: items.length }) {
  return {
    ...applyList,
    counts: { states: counts, lanes: { "apply-now": items.length } },
    lanes: applyList.lanes.map((l) => (l.lane === "apply-now" ? { ...l, total: items.length, items } : { ...l, total: 0, items: [] })),
  };
}

const base = { id: 1, company: "Northwind Example", role: "Staff Engineer", score: 4.3, lane: "apply-now", reportNumber: 7, postingHost: "jobs.example.com", postingUrl: "https://jobs.example.com/northwind/1", date: "2026-09-28", version: 1 };
const stopped = {
  ...base, state: "needs-user", runId: "run_a", keptTab: true,
  attempt: { runId: "run_a", at: stamp, mode: "submit", outcome: { kind: "stopped", stage: "account", class: "credential", reason: "the saved password was refused", owner: "user" } },
};

function server(items: Record<string, unknown>[] = [{ ...base, state: "idle" }], extra: Record<string, () => ReturnType<typeof ok>> = {}) {
  return fakeApi({ "GET /api/apply/list": () => ok(list(items)), "GET /api/runs": () => ok({ entries: [] }), ...extra });
}

describe("Apply Next row state and attempt", () => {
  it("carries no form value on a row", async () => {
    server([stopped]);
    await mount(<ApplyNext />);
    await waitFor(() => text().includes("Northwind Example"));
    // A row's choose box picks the row; it holds no value from the form.
    expect(qa("input:not([type=checkbox]), textarea")).toHaveLength(0);
  });
});

describe("Apply Next jump to tab", () => {
  it("leads a row that keeps a tab, is disabled elsewhere, and brings that run's tab forward", async () => {
    const api = server([stopped, { ...base, id: 2, company: "Contoso Example", state: "idle" }], { "POST /api/apply/runs/run_a/show-tab": () => ok({ runId: "run_a" }) });
    await mount(<ApplyNext />);
    // The kept tab is the row's one action in view; Autofill only moves into More.
    const kept = await waitFor(() => byLabel(/^Jump to tab: Northwind Example/));
    expect(qa("a").find((a) => a.textContent === "Open the run")?.getAttribute("href")).toBe("/runs/run_a");
    expect(byLabel(/^Autofill only: Northwind Example/)).toBeNull();
    await openMore("Northwind Example");
    expect(byLabel(/^Autofill only: Northwind Example/)).not.toBeNull();
    await openMore("Contoso Example");
    const none = byLabel(/^Jump to tab: Contoso Example/)!;
    expect(none.disabled).toBe(true);
    expect(none.title).toBe("No tab is kept for this row.");
    expect(kept.disabled).toBe(false);
    await click(kept);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_a/show-tab").length === 1);
    await waitFor(() => text().includes("The tab kept for Northwind Example is in front."));
  });

  it("shows the server's refusal when the tab is gone", async () => {
    server([stopped], { "POST /api/apply/runs/run_a/show-tab": () => fail("conflict", "the run no longer keeps a tab") });
    await mount(<ApplyNext />);
    await click(await waitFor(() => byLabel(/^Jump to tab: Northwind Example/)));
    await waitFor(() => text().includes("the run no longer keeps a tab"));
  });
});

describe("Apply Next skip-list editor", () => {
  const stored = { entries: ["Apple", "Microsoft"], defaulted: false, differs: true, defaults: ["Apple", "Microsoft", "Alphabet"], revision: "r1" };

  // A save gives the list a new revision, which the next read returns.
  function skipServer() {
    let list = stored;
    const api = server(undefined, { "GET /api/apply/skip-list": () => ok(list) });
    api.on("PUT /api/apply/skip-list", (c) => ok((list = { ...list, entries: (c.body as { entries: string[] }).entries, revision: "r2" })));
    return api;
  }

  async function open() {
    await mount(<ApplyNext />);
    await click(await waitFor(() => button("Edit the batch skip list")));
    await waitFor(() => q("dialog")?.textContent?.includes("Microsoft"));
  }

  it("adds an employer, refuses a duplicate and saves the whole list with the revision it read", async () => {
    const api = skipServer();
    await open();
    expect(button("Save the list")!.disabled).toBe(true);
    await typeInto(q<HTMLInputElement>("dialog input"), "microsoft");
    await click(button("Add"));
    expect(q("dialog")?.textContent).toContain("microsoft is already on the list.");
    await typeInto(q<HTMLInputElement>("dialog input"), "Contoso Example");
    await click(button("Add"));
    expect(q("dialog")?.textContent).toContain("You have unsaved changes.");
    await click(button("Save the list"));
    await waitFor(() => api.callsTo("PUT /api/apply/skip-list").length === 1);
    expect(api.callsTo("PUT /api/apply/skip-list")[0].body).toEqual({ entries: ["Apple", "Microsoft", "Contoso Example"], revision: "r1" });
    await waitFor(() => api.callsTo("GET /api/apply/list").length >= 2);
    // The list read again at its new revision fills the same form, so the saved note stays and keeps focus.
    await waitFor(() => api.callsTo("GET /api/apply/skip-list").length >= 2);
    await settle();
    expect(document.activeElement).toBe(byText("dialog [role=status]", "Your changes are saved."));
  });

  it("removes an employer and restores the default list", async () => {
    const api = skipServer();
    await open();
    await click(byLabel(/^Remove Apple$/));
    expect(qa("dialog ul li span").map((s) => s.textContent)).toEqual(["Microsoft"]);
    await click(button("Restore the default list"));
    expect(qa("dialog ul li span").map((s) => s.textContent)).toEqual(["Apple", "Microsoft", "Alphabet"]);
    await click(button("Save the list"));
    await waitFor(() => api.callsTo("PUT /api/apply/skip-list").length === 1);
    expect(api.callsTo("PUT /api/apply/skip-list")[0].body).toMatchObject({ entries: ["Apple", "Microsoft", "Alphabet"] });
  });

  it("shows a revision conflict and offers a reload that starts again from the server's list", async () => {
    let read = 0;
    server(undefined, {
      "GET /api/apply/skip-list": () => ok(read++ === 0 ? stored : { ...stored, entries: ["Apple"], revision: "r9" }),
      "PUT /api/apply/skip-list": () => fail("revision_conflict", "the profile changed since the list was read"),
    });
    await open();
    await click(byLabel(/^Remove Apple$/));
    await click(button("Save the list"));
    await waitFor(() => q("dialog [role='alert']")?.textContent?.includes("nothing was saved"));
    await click(button("Reload the list"));
    await waitFor(() => qa("dialog ul li span").map((s) => s.textContent).join() === "Apple");
    expect(q("dialog [role='alert']")).toBeNull();
  });
});
