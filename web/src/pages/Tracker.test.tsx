import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, byText, click, mount, openMore, q, qa, text, type, submit, unmount, waitFor, press } from "../testing/dom";
import { candidacy, detail, page, rows, summary, transitions } from "../testing/fixtures";
import Tracker from "./Tracker";
import { expectAccessible } from "../testing/axe";

// exported presses an export and answers it with a file. A plain link could not
// send the session key, so the export is a fetch (FileLink): the request it
// makes says what it exports.
async function exported(api: FakeApi, label: string): Promise<URL> {
  api.on("GET /api/tracker/export", () => new Response("id,company\n", { status: 200, headers: { "Content-Type": "text/csv" } }));
  // The saving click would navigate the test page to the blob address.
  const saved: string[] = [];
  const save = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    saved.push(this.download);
  });
  const key = "session-key-of-this-test-0123";
  window.localStorage.setItem("applysmith.sessionKey", key);
  const before = api.callsTo("GET /api/tracker/export").length;
  await click(button(label));
  await waitFor(() => saved.length === 1);
  save.mockRestore();
  window.localStorage.removeItem("applysmith.sessionKey");
  expect(saved).toEqual([label === "CSV" ? "tracker.csv" : "tracker.md"]);
  expect(api.callsTo("GET /api/tracker/export")).toHaveLength(before + 1);
  const c = api.callsTo("GET /api/tracker/export").at(-1)!;
  expect(c.headers.get("X-Applysmith-Session-Key")).toBe(key);
  return new URL(`${c.path}?${c.query}`, "http://x");
}

function server(list = rows) {
  return fakeApi({
    "GET /api/candidacies": (c) => {
      // The server reads a comma list of statuses as any of them.
      const status = c.query.get("status")?.split(",");
      const filtered = status ? list.filter((r) => status.includes(r.status)) : list;
      return ok(page(filtered));
    },
    "GET /api/tracker/summary": () => ok(summary),
    "GET /api/tracker/followups": () => ok({ suggestions: [] }),
    "GET /api/discovery/funnel/week": () => ok({ since: "2026-10-16", current: stages, previous: stages }),
  });
}

const stages = { found: 0, evaluated: 0, applied: 0, responded: 0, interview: 0, offer: 0 };

describe("Tracker", () => {

  // DuplicateMergeRequest / DuplicateDismissal (internal/api/wire): a merge
  // answers the token its Undo sends back; Not duplicates is undone by the same
  // set with undo. After a merge the keyboard is on the kept row, not the page.
  it("merges a group of duplicates after the confirm, Undo puts the rows back, and Not duplicates keeps a group apart until undone", async () => {
    window.history.replaceState(null, "", "/tracker");
    const api = server();
    const group = (ids: number[], reason?: string) => ({ survivor: ids[0], status: "applied", rule: "x", needsUser: !!reason, reason, rows: ids.map((id) => candidacy({ id, company: `Co ${ids[0]}` })) });
    let groups = [group([3, 7]), group([4, 9], "different applied histories")];
    api.on("GET /api/tracker/duplicates", () => ok({ groups }));
    api.on("POST /api/tracker/duplicates/merge", (c) => ((groups = (c.body as { undo?: string }).undo ? [group([3, 7]), ...groups] : groups.slice(1)), ok({ kept: candidacy({ id: 3, company: "Co 3" }), undo: "merge-1" })));
    api.on("POST /api/tracker/duplicates/dismiss", (c) => ((groups = (c.body as { undo?: boolean }).undo ? [...groups, group([4, 9], "different applied histories")] : groups.filter((g) => g.survivor !== 4)), ok({ groups })));
    await mount(<Tracker />);
    await click(await waitFor(() => button("Merge into row 3")));
    expect(api.callsTo("POST /api/tracker/duplicates/merge")).toHaveLength(0);
    await click(button("Merge rows"));
    await waitFor(() => text().includes("Merged 2 rows of Co 3 into row 3."));
    expect(api.callsTo("POST /api/tracker/duplicates/merge")[0].body).toEqual({ members: [3, 7] });
    await waitFor(() => !button("Merge into row 3"));
    expect(document.activeElement).toBe(q("button[aria-label='Open Fabrikam Example, Backend Engineer']"));
    await click(button("Undo"));
    await waitFor(() => button("Merge into row 3"));
    expect(api.callsTo("POST /api/tracker/duplicates/merge")[1].body).toEqual({ undo: "merge-1" });

    expect(button("Merge into row 4")).toBeNull();
    await click(q("button[aria-label='Not duplicates: rows 4, 9 of Co 4']"));
    await waitFor(() => !text().includes("Left to you"));
    expect(api.callsTo("POST /api/tracker/duplicates/dismiss")[0].body).toEqual({ members: [4, 9] });
    await click(button("Undo"));
    await waitFor(() => text().includes("Left to you: different applied histories"));
    expect(api.callsTo("POST /api/tracker/duplicates/dismiss")[1].body).toEqual({ members: [4, 9], undo: true });
  });

  // The export is ordered by id unless it names a sort (api.md), so it names
  // the list's: the one chosen, else the list's default, recently updated.
  it("exports exactly the filtered rows in the list's order", async () => {
    window.history.replaceState(null, "", "/tracker?status=applied&company=Contoso");
    const api = server();
    await mount(<Tracker />);
    await waitFor(() => button("CSV"));
    expect([...(await exported(api, "CSV")).searchParams]).toEqual([["status", "applied"], ["company", "Contoso"], ["sort", "updated_desc"], ["format", "csv"]]);
    expect((await exported(api, "Markdown")).searchParams.get("format")).toBe("md");
    await type(qa("form[aria-label='Filters'] label").find((l) => l.textContent?.startsWith("Sort"))!.querySelector("select"), "score_desc");
    await waitFor(() => api.callsTo("GET /api/candidacies").at(-1)!.query.get("sort") === "score_desc");
    expect((await exported(api, "CSV")).searchParams.get("sort")).toBe("score_desc");
  });

  // FollowupRequest (internal/api/wire): each press quotes the version it read,
  // the card's own, then for Undo the one the snooze made.
  it("lists the follow-ups due, snoozes one, and Undo brings it back", async () => {
    window.history.replaceState(null, "", "/tracker");
    const api = server();
    const post = "POST /api/candidacies/1/followup";
    const due = [{ id: 1, company: "Northwind Example", role: "Staff Engineer", version: 1, kind: "follow_up", number: 2, due: "2026-10-01" }, { id: 3, company: "Contoso Example", role: "SRE", version: 1, kind: "reply_check", due: "2026-10-02" }];
    api.on("GET /api/tracker/followups", () => ok({ suggestions: due.slice(api.callsTo(post).length % 2) }));
    api.on(post, () => ok(candidacy({ version: 3 })));
    await mount(<Tracker />);
    await waitFor(() => text().includes("Follow-up 2 has been due since 2026-10-01."));
    expect(text()).toContain("They replied; check that you answered (since 2026-10-02).");
    await openMore("Northwind Example");
    expect(qa("[role=menuitem]").map((m) => m.textContent)).toEqual(["Snooze 3 days", "Snooze 7 days", "Snooze 14 days"]);
    await click(byText("[role=menuitem]", "Snooze 14 days"));
    await waitFor(() => !text().includes("Follow-up 2"));
    expect(api.callsTo(post)[0].body).toEqual({ snoozeDays: 14, expectedVersion: 1 });
    expect(document.activeElement).toBe(button("Undo"));
    await click(button("Undo"));
    await waitFor(() => text().includes("Follow-up 2 has been due since 2026-10-01."));
    expect(api.callsTo(post)[1].body).toEqual({ undo: true, expectedVersion: 3 });
  });

  // Both presses quote a row version (StatusChangeRequest, internal/api/wire): the
  // card's own, then the one the close made. A row that moved is refused, and
  // the list reloads instead of closing or reopening it blindly.
  it("closes a row nobody answered in one press, as discarded, and Undo reopens it as applied", async () => {
    window.history.replaceState(null, "", "/tracker");
    const patchRow = "PATCH /api/candidacies/1";
    const api = server();
    const due = [{ id: 1, company: "Northwind Example", role: "Staff Engineer", version: 3, kind: "consider_closing", due: "2026-10-01" }];
    let open = true;
    api.on("GET /api/tracker/followups", () => ok({ suggestions: open ? due : [] }));
    api.on(patchRow, (c) => {
      const { status, expectedVersion } = c.body as { status: string; expectedVersion: number };
      open = status === "applied";
      return ok({ entry: candidacy({ status, version: expectedVersion + 1 }), changed: true, correction: false, inferred: false });
    });
    await mount(<Tracker />);
    await click(await waitFor(() => byLabel(/^Close as no reply: Northwind Example/)));
    await waitFor(() => text().includes("Northwind Example is closed as no reply."));
    expect(api.callsTo(patchRow)[0].body).toEqual({ status: "discarded", expectedVersion: 3 });
    // The list is empty now, and the keyboard is on Undo, not lost with the row.
    await waitFor(() => !text().includes("Follow-ups due"));
    expect(document.activeElement).toBe(button("Undo"));
    await click(button("Undo"));
    await waitFor(() => text().includes("Northwind Example is applied again."));
    expect(api.callsTo(patchRow)[1].body).toEqual({ status: "applied", expectedVersion: 4 });
    await waitFor(() => text().includes("Follow-ups due"));
    // Closing the same row again is a new move: Undo is offered again.
    await click(await waitFor(() => byLabel(/^Close as no reply: Northwind Example/)));
    await waitFor(() => button("Undo"));
    expect(text()).toContain("Northwind Example is closed as no reply.");
  });

  it("refuses to close a row that moved since the card was drawn and reloads the list", async () => {
    window.history.replaceState(null, "", "/tracker");
    const api = server();
    api.on("GET /api/tracker/followups", () => ok({ suggestions: [{ id: 1, company: "Northwind Example", role: "Staff Engineer", version: 3, kind: "consider_closing", due: "2026-10-01" }] }));
    api.on("PATCH /api/candidacies/1", () => fail("revision_conflict", "the row changed since it was loaded; reload and try again"));
    await mount(<Tracker />);
    await click(await waitFor(() => byLabel(/^Close as no reply: Northwind Example/)));
    await waitFor(() => text().includes("the row changed since it was loaded"));
    expect(text()).not.toContain("is closed as no reply");
    await waitFor(() => api.callsTo("GET /api/tracker/followups").length >= 2);
  });

  // FollowupDraftRequest (internal/api/wire): the kind, name and last exchange
  // the user gives reach the draft; nothing is recorded.
  it("drafts a due follow-up to copy, redrafts it with the user's choices, records it only on I sent this, and keeps both drafts on reopening", async () => {
    window.history.replaceState(null, "", "/tracker");
    const api = server();
    api.on("GET /api/tracker/followups", () => ok({ suggestions: [{ id: 1, company: "Northwind Example", role: "Staff Engineer", version: 1, kind: "follow_up", number: 1, due: "2026-10-01" }] }));
    const draft = (message: string) => ({ kind: "recruiter_ping", primary: { angle: "", message }, alternatives: [{ angle: "shorter", message: "Any news?" }], maxChars: 600, confirmed: [], toConfirm: ["The name."], dropped: [], notes: ["Nothing was sent."] });
    api.on("POST /api/candidacies/1/followup-draft", (c) => ok(draft((c.body as { kind: string }).kind ? "Checking in on my status." : "Hi, any update?")));
    await mount(<Tracker />);
    await click(await waitFor(() => byLabel(/^Draft a follow-up to Northwind Example$/)));
    const dialog = await waitFor(() => qa("dialog").find((d) => d.textContent?.includes("Hi, any update?")));
    expect([...dialog.querySelectorAll("pre")].map((p) => p.textContent)).toEqual(["Hi, any update?", "Any news?"]);
    await type(dialog.querySelector("select"), "status_check");
    await type(dialog.querySelector("textarea"), "Spoke to Sam on Monday");
    await submit(dialog.querySelector("form"));
    await waitFor(() => dialog.textContent?.includes("Checking in on my status."));
    expect(api.callsTo("POST /api/candidacies/1/followup-draft").map((c) => c.body)).toEqual([
      { kind: "", contact: "", context: "" },
      { kind: "status_check", contact: "", context: "Spoke to Sam on Monday" },
    ]);
    expect(api.callsTo("POST /api/candidacies/1/followup")).toHaveLength(0);
    // Each write quotes the version the one before it made (expectedVersion).
    api.on("POST /api/candidacies/1/notes", (c) => ok({ ...rows[0], version: "undo" in (c.body as object) ? 5 : 2 }));
    api.on("POST /api/candidacies/1/followup", (c) => ok({ ...rows[0], version: "undo" in (c.body as object) ? 4 : 3 }));
    await click(byLabel(/^I sent the suggested$/));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/followup").length === 1);
    expect(api.callsTo("POST /api/candidacies/1/notes")[0].body).toMatchObject({ text: expect.stringContaining("Checking in on my status."), expectedVersion: 1 });
    expect(api.callsTo("POST /api/candidacies/1/followup")[0].body).toEqual({ expectedVersion: 2 });
    // Undo takes back the follow-up, then the note (NoteRequest.undo).
    await click(await waitFor(() => button("Undo")));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/notes").length === 2);
    expect(api.callsTo("POST /api/candidacies/1/followup")[1].body).toEqual({ undo: true, expectedVersion: 3 });
    expect(api.callsTo("POST /api/candidacies/1/notes")[1].body).toEqual({ undo: true, expectedVersion: 4 });
    await click(byText("button", "Close"));
    await click(await waitFor(() => byLabel(/^Draft a follow-up to Northwind Example$/)));
    const again = await waitFor(() => qa("dialog").find((d) => d.textContent?.includes("Checking in on my status.")));
    expect(again.querySelector("details")?.textContent).toContain("Hi, any update?");
    expect(api.callsTo("POST /api/candidacies/1/followup-draft")).toHaveLength(2);
  });

  it("sets one status on the ticked rows, asks first above 5 rows, and Undo puts each row back to its own status, leaving a row that changed since", async () => {
    window.history.replaceState(null, "", "/tracker");
    const bulk = "POST /api/candidacies/bulk-status";
    const answer = (list: typeof rows) => (c: { body: unknown }) => {
      const b = c.body as { ids: number[]; status: string };
      return ok({ results: list.filter((r) => b.ids.includes(r.id)).map((entry) => ({ entry: { ...entry, version: entry.version + 1 }, changed: entry.status !== b.status, correction: false, inferred: false })) });
    };
    const api = server();
    api.on(bulk, answer(rows));
    await mount(<Tracker />);
    await waitFor(() => text().includes("Contoso Example"));
    await click(q("input[aria-label='Select Northwind Example, Staff Engineer']"));
    await click(q("input[aria-label='Select Contoso Example, Platform Engineer']"));
    await type(q<HTMLSelectElement>("select[aria-label='New status for the 2 selected']"), "rejected");
    await click(button("Set status of 2 selected"));
    await waitFor(() => text().includes("2 rows moved to Rejected."));
    expect(api.callsTo(bulk)[0].body).toEqual({ ids: [1, 2], status: "rejected" });
    expect(q("tbody input:checked")).toBeNull();
    // The Set status button left with the ticks; the keyboard is on Undo.
    expect(document.activeElement).toBe(button("Undo"));
    api.on("PATCH /api/candidacies/1", () => ok({ entry: rows[0], changed: true, correction: false, inferred: false }));
    api.on("PATCH /api/candidacies/2", () => fail("revision_conflict", "the row changed since it was loaded; reload and try again"));
    await click(button("Undo"));
    await waitFor(() => !button("Undo"));
    expect([api.callsTo("PATCH /api/candidacies/1")[0].body, api.callsTo("PATCH /api/candidacies/2")[0].body]).toEqual([{ status: "evaluated", expectedVersion: rows[0].version + 1 }, { status: "applied", expectedVersion: rows[1].version + 1 }]);
    expect(api.callsTo(bulk).length).toBe(1);
    expect(text()).toContain("1 row changed since and was left as it is.");

    const six = Array.from({ length: 6 }, (_, i) => candidacy({ id: i + 1, company: `Company ${i + 1}` }));
    const api2 = server(six);
    api2.on(bulk, answer(six));
    await mount(<Tracker />);
    await waitFor(() => text().includes("Company 6"));
    await click(byText("label", "Select all 6")?.querySelector("input") ?? null);
    await type(q<HTMLSelectElement>("select[aria-label='New status for the 6 selected']"), "skipped");
    await click(button("Set status of 6 selected"));
    await waitFor(() => q("dialog"));
    expect([text().includes("All 6 selected rows move to Skipped."), api2.callsTo(bulk).length]).toEqual([true, 0]);
    await click(button("Change status"));
    await waitFor(() => api2.callsTo(bulk).length === 1);
  });

  // BulkArchiveRequest (internal/api/wire): the ids of every row the filters
  // match, from the server's own list, and Undo sends back the ones that moved.
  it("selects every matching row across pages, archives them, leaves an open one, and Undo brings them back", async () => {
    window.history.replaceState(null, "", "/tracker?status=applied,rejected");
    const all = [candidacy({ id: 1, status: "rejected" }), candidacy({ id: 2, company: "Contoso Example", status: "rejected" }), candidacy({ id: 3, company: "Fabrikam Example", status: "applied" })];
    const api = server();
    api.on("GET /api/candidacies", (c) => ok(c.query.get("pageSize") === "100" ? page(all, 3, 1, 100) : page(all.slice(0, 2), 3)));
    api.on("POST /api/candidacies/bulk-archive", (c) => ok((c.body as { archived: boolean }).archived ? { changed: [1, 2], open: [3] } : { changed: [1, 2], open: [] }));
    await mount(<Tracker />);
    await click(await waitFor(() => byText("label", "Select all 2")?.querySelector("input") ?? null));
    await click(button("Select all 3 matching"));
    await waitFor(() => text().includes("All 3 matching rows are selected."));
    expect(api.callsTo("GET /api/candidacies").at(-1)!.query.get("status")).toBe("applied,rejected");
    await click(button("Archive 3 selected"));
    await waitFor(() => text().includes("2 rows archived, out of the default list. 1 row still in flight stays in the list until closed."));
    expect(api.callsTo("POST /api/candidacies/bulk-archive")[0].body).toEqual({ ids: [1, 2, 3], archived: true });
    expect(document.activeElement).toBe(button("Undo"));
    await click(button("Undo"));
    await waitFor(() => text().includes("2 rows back in the list."));
    expect(api.callsTo("POST /api/candidacies/bulk-archive")[1].body).toEqual({ ids: [1, 2], archived: false });
  });

  describe("row drawer", () => {
    const row = rows[0];
    function withRow() {
      const api = server();
      api.on("GET /api/candidacies/1", () => ok(detail(row)));
      api.on("GET /api/candidacies/1/transitions", () => ok(transitions("evaluated", ["applied", "skipped"], ["offer"])));
      return api;
    }

    it("sends the loaded version with a status change, and Undo puts the old status back", async () => {
      window.history.replaceState(null, "", "/tracker?row=1");
      const api = withRow();
      api.on("PATCH /api/candidacies/1", () => ok({ entry: candidacy({ status: "applied", version: 3 }), changed: true, correction: false, inferred: false }));
      await mount(<Tracker />);
      await waitFor(() => q("dialog select"));
      await type(q<HTMLSelectElement>("dialog select"), "applied");
      await submit(q("dialog form[aria-label='Change status']"));
      await waitFor(() => api.callsTo("PATCH /api/candidacies/1").length === 1);
      expect(api.callsTo("PATCH /api/candidacies/1")[0].body).toEqual({ status: "applied", expectedVersion: 2 });
      await waitFor(() => text().includes("Status is now Applied."));
      expect(document.activeElement).toBe(q("dialog select")); // the button is disabled again, so focus waits on the picker
      // Undo puts the old status back against the version the change made.
      await click(button("Undo"));
      await waitFor(() => text().includes("Status is back to Evaluated."));
      expect(api.callsTo("PATCH /api/candidacies/1")[1].body).toEqual({ status: "evaluated", expectedVersion: 3 });
    });

    // A status Gmail moved is put back from the history as the user's change,
    // only while the row still has the status the mail gave it (round-58 audit).
    it("puts a status Gmail moved back from the history, and not once the row moved on", async () => {
      for (const later of [false, true]) {
        window.history.replaceState(null, "", "/tracker?row=1");
        const api = withRow();
        const ev = (id: number, actor: string, from: string, to: string) => ({ id, at: "2026-10-12T09:00:00Z", actor, kind: "status_changed", previousStatus: from, newStatus: to });
        const events = [ev(2, "user", "evaluated", "applied"), ev(3, "gmail", "applied", "rejected"), ...(later ? [ev(4, "user", "rejected", "interview")] : [])];
        const entry = candidacy({ status: later ? "interview" : "rejected", version: 5 });
        api.on("GET /api/candidacies/1", () => ok({ ...detail(entry), events }));
        api.on("PATCH /api/candidacies/1", () => ok({ entry: candidacy({ status: "applied", version: 6 }), changed: true, correction: true, inferred: false }));
        await mount(<Tracker />);
        await waitFor(() => qa("dialog ol.timeline li").length === events.length);
        const back = qa<HTMLButtonElement>("dialog ol.timeline button");
        if (later) expect(back).toHaveLength(0);
        else {
          await click(back[0]);
          await waitFor(() => api.callsTo("PATCH /api/candidacies/1").length === 1);
          expect(api.callsTo("PATCH /api/candidacies/1")[0].body).toEqual({ status: "applied", expectedVersion: 5 });
        }
        unmount();
      }
    });

    it("sends the day applied and a note with a change to applied, and neither when left alone", async () => {
      window.history.replaceState(null, "", "/tracker?row=1");
      const api = withRow();
      api.on("PATCH /api/candidacies/1", () => ok({ entry: candidacy({ status: "applied", version: 3 }), changed: true, correction: false, inferred: false }));
      await mount(<Tracker />);
      await waitFor(() => q("dialog select"));
      const form = "dialog form[aria-label='Change status']";
      await type(q<HTMLSelectElement>("dialog select"), "skipped");
      // The day applied belongs to a change to applied only: a date typed for
      // applied and then left behind for another status is not sent.
      expect(q("dialog input[type=date]")).toBeNull();
      await type(q<HTMLSelectElement>("dialog select"), "applied");
      await type(q<HTMLInputElement>("dialog input[type=date]"), "2026-10-01");
      await type(q<HTMLSelectElement>("dialog select"), "skipped");
      await submit(q(form));
      await waitFor(() => api.callsTo("PATCH /api/candidacies/1").length === 1);
      expect(api.callsTo("PATCH /api/candidacies/1")[0].body).toEqual({ status: "skipped", expectedVersion: 2 });
      await type(q<HTMLSelectElement>("dialog select"), "applied");
      await type(q<HTMLInputElement>("dialog input[type=date]"), "2026-10-02");
      await type(q<HTMLTextAreaElement>(`${form} textarea`), " Applied through the referral ");
      await submit(q(form));
      await waitFor(() => api.callsTo("PATCH /api/candidacies/1").length === 2);
      expect(api.callsTo("PATCH /api/candidacies/1")[1].body).toEqual({ status: "applied", expectedVersion: 2, on: "2026-10-02", note: "Applied through the referral" });
    });

    it("archives a closed row from the drawer and offers Undo", async () => {
      window.history.replaceState(null, "", "/tracker?row=1");
      const api = withRow();
      let current = candidacy({ status: "rejected" });
      api.on("GET /api/candidacies/1", () => ok(detail(current)));
      api.on("POST /api/candidacies/1/archive", () => ok((current = candidacy({ status: "rejected", archivedAt: "2026-10-20T10:00:00Z" }))));
      api.on("POST /api/candidacies/1/unarchive", () => ok((current = candidacy({ status: "rejected" }))));
      await mount(<Tracker />);
      await click(await waitFor(() => button("Archive")));
      await waitFor(() => text().includes("Archived: the row leaves the default list."));
      expect(api.callsTo("POST /api/candidacies/1/archive")).toHaveLength(1);
      await waitFor(() => button("Unarchive"));
      expect(document.activeElement).toBe(button("Undo"));
      await expectAccessible();
      await click(button("Undo"));
      await waitFor(() => api.callsTo("POST /api/candidacies/1/unarchive").length === 1);
      await waitFor(() => button("Archive"));
    });

    it("tells the user the row changed when the version is stale, and does not retry", async () => {
      window.history.replaceState(null, "", "/tracker?row=1");
      const api = withRow();
      api.on("PATCH /api/candidacies/1", () => fail("revision_conflict", "the row changed since it was loaded; reload and try again"));
      await mount(<Tracker />);
      await waitFor(() => q("dialog select"));
      await type(q<HTMLSelectElement>("dialog select"), "applied");
      await submit(q("dialog form[aria-label='Change status']"));
      await waitFor(() => text().includes("This row changed since you loaded it"));
      expect(text()).toContain("Request ");
      expect(api.callsTo("PATCH /api/candidacies/1")).toHaveLength(1);
      expect((q("dialog select") as HTMLSelectElement).value).toBe("");
      const before = api.callsTo("GET /api/candidacies/1").length;
      await click(button("Reload row"));
      await waitFor(() => api.callsTo("GET /api/candidacies/1").length > before);
    });

  });

  describe("notes", () => {
    function withRow() {
      const api = server();
      api.on("GET /api/candidacies/1", () => ok(detail(rows[0])));
      api.on("GET /api/candidacies/1/transitions", () => ok(transitions("evaluated", ["applied"])));
      return api;
    }

    // Escape over a note not added yet asks; once it is added, Escape closes.
    // Focus stays in the box for the next note; Undo quotes the version the
    // note made (NoteRequest.undo).
    it("shows the character cap, sends the note with the loaded version, and Undo takes it back", async () => {
      window.history.replaceState(null, "", "/tracker?row=1");
      const api = withRow();
      api.on("POST /api/candidacies/1/notes", (c) => ({ status: 201, data: candidacy({ version: "undo" in (c.body as object) ? 4 : 3 }) }));
      await mount(<Tracker />);
      const area = await waitFor(() => q<HTMLTextAreaElement>("dialog form[aria-label='Add a note'] textarea"));
      expect(text()).toContain("0 of 1000 characters");
      expect(area.maxLength).toBe(1000);
      await type(area, "Called the recruiter");
      expect(text()).toContain("20 of 1000 characters");
      await press("Escape", {}, area);
      await click(await waitFor(() => button("Cancel")));
      expect(window.location.search).toBe("?row=1");
      await submit(q("dialog form[aria-label='Add a note']"));
      await waitFor(() => api.callsTo("POST /api/candidacies/1/notes").length === 1);
      expect(api.callsTo("POST /api/candidacies/1/notes")[0].body).toEqual({ text: "Called the recruiter", expectedVersion: 2 });
      await waitFor(() => text().includes("Note added."));
      expect(q<HTMLTextAreaElement>("dialog form[aria-label='Add a note'] textarea")!.value).toBe("");
      expect(document.activeElement).toBe(q("dialog form[aria-label='Add a note'] textarea"));
      await click(button("Undo"));
      await waitFor(() => text().includes("The note is off the row."));
      expect(api.callsTo("POST /api/candidacies/1/notes")[1].body).toEqual({ undo: true, expectedVersion: 3 });
      await press("Escape", {}, q("dialog")!);
      await waitFor(() => q("dialog") === null);
      expect(window.location.search).toBe("");
    });

    it("keeps the note and offers a reload when the row changed", async () => {
      window.history.replaceState(null, "", "/tracker?row=1");
      const api = withRow();
      api.on("POST /api/candidacies/1/notes", () => fail("revision_conflict", "stale"));
      await mount(<Tracker />);
      const area = await waitFor(() => q<HTMLTextAreaElement>("dialog form[aria-label='Add a note'] textarea"));
      await type(area, "Keep me");
      await submit(q("dialog form[aria-label='Add a note']"));
      await waitFor(() => text().includes("This row changed since you loaded it"));
      expect(q<HTMLTextAreaElement>("dialog form[aria-label='Add a note'] textarea")!.value).toBe("Keep me");
      expect(api.callsTo("POST /api/candidacies/1/notes")).toHaveLength(1);
    });
  });

  describe("add application", () => {
    async function fill(values: Record<string, string>) {
      for (const [name, value] of Object.entries(values)) await type(q<HTMLInputElement>(`dialog [name=${name}]`), value);
    }

    it("records a job applied for elsewhere and opens its row", async () => {
      const api = server();
      api.on("POST /api/candidacies", () => ({ status: 201, data: candidacy({ id: 9, company: "Adatum Example", role: "SRE", status: "applied" }) }));
      api.on("GET /api/candidacies/9", () => ok(detail(candidacy({ id: 9 }))));
      api.on("GET /api/candidacies/9/transitions", () => ok(transitions("applied", [])));
      await mount(<Tracker />);
      await click(button("Add application"));
      await waitFor(() => q("dialog form"));
      await fill({ company: "Adatum Example", role: "SRE", url: "https://jobs.example.com/adatum/3", appliedAt: "2026-10-01", source: "Referral" });
      await submit(q("dialog form[aria-label='Add application']"));
      await waitFor(() => api.callsTo("POST /api/candidacies").length === 1);
      expect(api.callsTo("POST /api/candidacies")[0].body).toEqual({ company: "Adatum Example", role: "SRE", status: "applied", url: "https://jobs.example.com/adatum/3", appliedAt: "2026-10-01", source: "Referral" });
      await waitFor(() => window.location.search === "?row=9");
    });

    it("warns about a duplicate and links the matching row", async () => {
      const api = server();
      api.on("POST /api/candidacies", () => fail("conflict", "this job is already row 2 of the tracker (the same posting)", { details: { reason: "duplicate_candidacy", matchId: 2 } }));
      await mount(<Tracker />);
      await click(button("Add application"));
      await waitFor(() => q("dialog form"));
      await fill({ company: "Contoso Example", role: "Platform Engineer" });
      await submit(q("dialog form[aria-label='Add application']"));
      const link = await waitFor(() => byText("dialog a", "Open the matching row"));
      expect(link.getAttribute("href")).toBe("/tracker?row=2");
      expect(text()).toContain("this job is already row 2 of the tracker");
      expect(text()).toContain("Request ");
    });

  });
});
