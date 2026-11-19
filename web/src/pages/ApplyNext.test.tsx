import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { button, byLabel, byText, click, mount, openMore, press, q, qa, text, type, waitFor } from "../testing/dom";
import { applyList, applyListWithSkips, candidacy } from "../testing/fixtures";
import ApplyNext from "./ApplyNext";

const accepted = { runId: "run_20261012T010203.000_abcdef" };
const pick = (who: string) => q<HTMLInputElement>(`input[aria-label^="Choose ${who}"]`);
const stamp = "2026-10-12T09:00:00.000Z";

function entry(over: Record<string, unknown> = {}) {
  return { id: "run_1", kind: "apply", origin: "user", title: "Apply to Northwind Example", state: "working", link: "/runs/run_1", revision: 1, startedAt: stamp, updatedAt: stamp, stateAt: stamp, ...over };
}

function server(extra: Record<string, () => ReturnType<typeof ok>> = {}) {
  return fakeApi({
    "GET /api/apply/list": () => ok(applyListWithSkips),
    "GET /api/runs": () => ok({ entries: [] }),
    ...extra,
  });
}

describe("Apply Next", () => {

  it("never offers a withdraw action", async () => {
    server();
    await mount(<ApplyNext />);
    await waitFor(() => text().includes("Northwind Example"));
    expect(text().toLowerCase()).not.toContain("withdraw");
  });

  it("reads an idle row whose last run filled the form as filled earlier, not as not started", async () => {
    const filled = { ...applyList.lanes[0].items[0], runId: "run_f", attempt: { runId: "run_f", at: stamp, mode: "dry-run", outcome: { kind: "filled" } } };
    server({ "GET /api/apply/list": () => ok({ ...applyList, lanes: applyList.lanes.map((l) => (l.lane === "apply-now" ? { ...l, items: [filled] } : l)) }) });
    await mount(<ApplyNext />);
    const state = await waitFor(() => q("#lane-apply-now-section td.state-col .chip"));
    expect(state.textContent).toBe("Filled earlier");
  });

  it("pages each lane on its own, and a new order starts every lane on its first page", async () => {
    const row = (lane: string, id: number) => ({ ...applyList.lanes[0].items[0], id, company: `Company ${id}`, lane });
    const lanes = [
      { lane: "apply-now", total: 12, items: [row("apply-now", 1)] },
      { lane: "selective", total: 12, items: [row("selective", 2)] },
      { lane: "marked-applied", total: 0, items: [] },
      { lane: "awaiting", total: 0, items: [] },
    ];
    window.history.replaceState(null, "", "/apply-next?page.apply-now=2");
    const api = server({ "GET /api/apply/list": () => ok({ ...applyList, lanes }) });
    const last = () => api.callsTo("GET /api/apply/list").at(-1)!.query;
    const pager = (lane: string) => q(`#lane-${lane}-section nav[aria-label="Pages"]`);
    await mount(<ApplyNext />);
    await click(await waitFor(() => [...(pager("selective")?.querySelectorAll("button") ?? [])].find((b) => b.textContent === "Next")));
    await waitFor(() => last().get("page.selective") === "2");
    expect(last().get("page.apply-now")).toBe("2");
    expect(pager("apply-now")?.textContent).toContain("Page 2 of 2");
    await type(q<HTMLSelectElement>(".page-actions select"), "newest");
    await waitFor(() => last().get("sort") === "newest");
    expect([...last().keys()].filter((k) => k.startsWith("page"))).toEqual([]);
  });

});

describe("Apply Next starts runs", () => {
  it("fills one posting without asking, and never as a submit", async () => {
    const api = server({ "POST /api/apply/runs": () => ok(accepted) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => byLabel(/^Autofill only: Northwind Example/)));
    await waitFor(() => api.callsTo("POST /api/apply/runs").length === 1);
    expect(api.callsTo("POST /api/apply/runs")[0].body).toEqual({
      url: "https://jobs.example.com/northwind/1", company: "Northwind Example", role: "Staff Engineer", submit: false, confirmed: false,
    });
    expect(q("dialog")).toBeNull();
    await waitFor(() => text().includes("Filling the application to Northwind Example; it stops before Submit."));
    expect(qa<HTMLAnchorElement>("a").find((a) => a.textContent === "Follow the run")?.getAttribute("href")).toBe(`/runs/${accepted.runId}`);
  });

  it("says so when the run it started has already failed, instead of still filling", async () => {
    const failed = entry({ id: accepted.runId, state: "done", outcome: "failed", detail: "The site isn't answering right now." });
    server({ "POST /api/apply/runs": () => ok(accepted), [`GET /api/runs/${accepted.runId}`]: () => ok({ runId: accepted.runId, kind: "apply", status: "failed", createdAt: stamp, updatedAt: stamp, entry: failed }) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => byLabel(/^Autofill only: Northwind Example/)));
    await waitFor(() => text().includes("Apply to Northwind Example: Failed. The site isn't answering right now."));
    expect(text()).not.toContain("it stops before Submit.");
    expect(q(".started-note")?.className).toContain("error");
    expect(byText("a", "Open the run")?.getAttribute("href")).toBe(`/runs/${accepted.runId}`);
  });

  it("asks before submitting, names the company, role and host, and focuses Cancel", async () => {
    const api = server({ "POST /api/apply/runs": () => ok(accepted) });
    await mount(<ApplyNext />);
    await openMore("Northwind Example");
    await click(await waitFor(() => byLabel(/^Autofill and submit: Northwind Example/)));
    const dialog = await waitFor(() => q("dialog"));
    expect(dialog.querySelector("h2")?.textContent).toBe("Submit the application to Northwind Example");
    expect(dialog.textContent).toContain("Staff Engineer at Northwind Example on jobs.example.com");
    expect(dialog.textContent).toContain("press its final Submit button");
    expect(dialog.textContent).toContain("Northwind Example is not on your batch skip list.");
    expect(document.activeElement?.textContent).toBe("Cancel");
    expect(api.callsTo("POST /api/apply/runs")).toHaveLength(0);
    await click(button("Submit application"));
    await waitFor(() => api.callsTo("POST /api/apply/runs").length === 1);
    expect(api.callsTo("POST /api/apply/runs")[0].body).toMatchObject({ submit: true, confirmed: true });
    await waitFor(() => !q("dialog"));
    expect(text()).toContain("Submitting the application to Northwind Example.");
  });

  it("offers a row below the document floor the cover-letter override, unticked, and records a tick before the run starts", async () => {
    const below = (offer?: boolean) => ({ ...applyList, lanes: applyList.lanes.map((l) => ({ ...l, items: l.items.map((i) => ({ ...i, letterOffer: offer })) })) });
    const order: string[] = [];
    const api = server({
      "GET /api/apply/list": () => ok(below(true)),
      "POST /api/candidacies/1/documents-override": () => (order.push("override"), ok(candidacy({ reportNumber: 7 }))),
      "POST /api/apply/runs": () => (order.push("run"), ok(accepted)),
    });
    const line = "Allow a tailored cover letter if a form asks for one (recorded as your override for this report)";
    const ask = async () => {
      await openMore("Northwind Example");
      await click(await waitFor(() => byLabel(/^Autofill and submit: Northwind Example/)));
      return waitFor(() => q("dialog"));
    };
    await mount(<ApplyNext />);
    let dialog = await ask();
    expect(dialog.textContent).toContain("Its report scores 4.3, below the 4.5 floor for tailored documents.");
    const box = () => qa<HTMLInputElement>("dialog input[type=checkbox]").find((i) => i.closest("label")?.textContent?.trim() === line);
    expect(box()?.checked).toBe(false);
    // Left unticked, the run starts with no override.
    await click(button("Submit application"));
    await waitFor(() => order.length === 1);
    expect(order).toEqual(["run"]);
    await waitFor(() => !q("dialog"));
    dialog = await ask();
    await click(box()!);
    await click(button("Submit application"));
    await waitFor(() => order.length === 3);
    expect(order).toEqual(["run", "override", "run"]);
    expect(api.callsTo("POST /api/candidacies/1/documents-override")).toHaveLength(1);
    // A row the server offers nothing is offered nothing.
    for (const offer of [false, undefined]) {
      api.on("GET /api/apply/list", () => ok(below(offer)));
      await mount(<ApplyNext />);
      dialog = await ask();
      expect(dialog.textContent).not.toContain("tailored cover letter");
      await press("Escape", {}, dialog);
      await waitFor(() => !q("dialog"));
    }
  });

  it.each([
    { start: "a lane", open: async () => click(await waitFor(() => button("Submit all\u2026"))), verb: "Submit up to 12" },
    {
      start: "the chosen rows",
      open: async () => {
        await click(await waitFor(() => pick("Northwind Example")));
        await click(pick("Microsoft"));
        await click(button("Submit selected\u2026"));
      },
      verb: "Submit 2",
    },
  ])("offers the override per row in the confirmation of $start and records the ticked ones before the batch starts", async ({ open, verb }) => {
    const lanes = applyListWithSkips.lanes.map((l) => ({ ...l, items: l.items.map((i) => ({ ...i, letterOffer: true })) }));
    const order: string[] = [];
    server({
      "GET /api/apply/list": () => ok({ ...applyListWithSkips, lanes }),
      "POST /api/candidacies/1/documents-override": () => (order.push("override 1"), ok(candidacy({ reportNumber: 7 }))),
      "POST /api/apply/batches": () => (order.push("batch"), ok({ runId: accepted.runId, rows: 2, skipped: [] })),
    });
    await mount(<ApplyNext />);
    await open();
    const dialog = await waitFor(() => q("dialog"));
    // The skip-listed row and the row without an address are not run, so not offered.
    const boxes = qa<HTMLInputElement>("dialog fieldset input[type=checkbox]");
    expect(boxes.map((b) => b.closest("label")?.textContent?.trim())).toEqual(["Northwind Example, Staff Engineer (4.3)"]);
    expect(dialog.textContent).toContain("Allow a tailored cover letter if a form asks for one (recorded as your override for each report you tick)");
    expect(boxes[0].checked).toBe(false);
    await click(boxes[0]);
    await click(button(verb));
    await waitFor(() => order.length === 2);
    expect(order).toEqual(["override 1", "batch"]);
  });

  it("submits nothing when the dialog is closed with Escape", async () => {
    const api = server({ "POST /api/apply/runs": () => ok(accepted) });
    await mount(<ApplyNext />);
    await openMore("Northwind Example");
    await click(await waitFor(() => byLabel(/^Autofill and submit: Northwind Example/)));
    await press("Escape", {}, await waitFor(() => q("dialog")));
    await waitFor(() => !q("dialog"));
    expect(api.callsTo("POST /api/apply/runs")).toHaveLength(0);
  });

  it("applies to a whole lane only after a dialog that states the count and what the skip list leaves out", async () => {
    const batch = { runId: accepted.runId, batchId: "list_1", rows: 10, skipped: [{ company: "Microsoft", role: "Platform Engineer", reason: 'Microsoft is on your batch skip list (as "Microsoft"); apply there yourself' }] };
    const api = server({ "POST /api/apply/batches": () => ok(batch) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => button("Submit all\u2026")));
    const dialog = await waitFor(() => q("dialog"));
    expect(dialog.querySelector("h2")?.textContent).toBe("Apply to up to 12 postings in Apply now");
    expect(dialog.textContent).toContain("sends up to 12 real applications");
    expect(dialog.querySelector("li")?.textContent).toBe('Microsoft: Microsoft is on your batch skip list (as "Microsoft"); apply there yourself');
    expect(dialog.textContent).toContain("Rows beyond this page are checked when the run starts.");
    expect(api.callsTo("POST /api/apply/batches")).toHaveLength(0);
    await click(button("Submit up to 12"));
    await waitFor(() => api.callsTo("POST /api/apply/batches").length === 1);
    expect(api.callsTo("POST /api/apply/batches")[0].body).toEqual({ lanes: ["apply-now"], limit: 12, submit: true, confirmed: true });
    await waitFor(() => text().includes("Applying to 10 postings from Apply now."));
    expect(text()).toContain("Left out (1):Microsoft: Microsoft is on your batch skip list");
  });

  it("retries today's stops only when the lane dialog's box is ticked", async () => {
    const api = server({ "POST /api/apply/batches": () => ok({ runId: accepted.runId, batchId: "list_1", rows: 3, skipped: [] }) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => button("Submit all\u2026")));
    await click(await waitFor(() => q<HTMLInputElement>("dialog input[type=checkbox]")));
    await click(button("Submit up to 12"));
    await waitFor(() => api.callsTo("POST /api/apply/batches").length === 1);
    expect(api.callsTo("POST /api/apply/batches")[0].body).toEqual({ lanes: ["apply-now"], limit: 12, submit: true, confirmed: true, retryToday: true });
  });

  it("fills a whole lane without a dialog and never as a submit", async () => {
    const api = server({ "POST /api/apply/batches": () => ok({ runId: accepted.runId, batchId: "list_1", rows: 2, skipped: [] }) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => button("Autofill all")));
    await waitFor(() => api.callsTo("POST /api/apply/batches").length === 1);
    expect(api.callsTo("POST /api/apply/batches")[0].body).toMatchObject({ submit: false, confirmed: false });
    expect(q("dialog")).toBeNull();
  });

  it("says the place of a start that waits for the apply window, and cancels it", async () => {
    const queued = entry({ id: accepted.runId, stage: "queued", detail: "Queued (2 ahead): it starts when the apply window is free." });
    const api = server({
      "POST /api/apply/runs": () => ok({ ...accepted, ahead: 2 }),
      [`GET /api/runs/${accepted.runId}`]: () => ok({ runId: accepted.runId, kind: "apply", status: "running", stage: "queued", createdAt: stamp, updatedAt: stamp, entry: queued }),
      [`POST /api/runs/${accepted.runId}/cancel`]: () => ok({ runId: accepted.runId, kind: "apply", status: "cancelled", createdAt: stamp, updatedAt: stamp }),
    });
    await mount(<ApplyNext />);
    await click(await waitFor(() => byLabel(/^Autofill only: Northwind Example/)));
    await waitFor(() => text().includes("Queued (2 ahead): it starts when the apply window is free."));
    await click(byText("button", "Cancel")!);
    await waitFor(() => api.callsTo(`POST /api/runs/${accepted.runId}/cancel`).length === 1);
  });

  it("fills the rows the user ticked, in that order, without a dialog and never as a submit", async () => {
    const api = server({ "POST /api/apply/batches": () => ok({ runId: accepted.runId, batchId: "list_1", rows: 2, skipped: [] }) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => pick("Microsoft")));
    await click(pick("Northwind Example"));
    expect(pick("Tailspin Example")?.disabled).toBe(true);
    await click(button("Autofill selected"));
    await waitFor(() => api.callsTo("POST /api/apply/batches").length === 1);
    expect(api.callsTo("POST /api/apply/batches")[0].body).toEqual({ candidacies: [2, 1], submit: false, confirmed: false });
    expect(q("dialog")).toBeNull();
    // The ticks clear with the press, so the keyboard goes to the run it started.
    await waitFor(() => document.activeElement?.textContent === "Follow the run");
  });

  it("submits the ticked rows only after a dialog that names the role, company and host of each", async () => {
    const api = server({ "POST /api/apply/batches": () => ok({ runId: accepted.runId, batchId: "list_1", rows: 1, skipped: [] }) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => pick("Northwind Example")));
    await click(pick("Microsoft"));
    await click(button("Submit selected\u2026"));
    const dialog = await waitFor(() => q("dialog"));
    expect(qa("dialog li").map((li) => li.textContent)).toEqual([
      "Staff Engineer at Northwind Example on jobs.example.com",
      'Platform Engineer at Microsoft on jobs.example.org (left out: Microsoft is on your batch skip list (as "Microsoft"); apply there yourself)',
    ]);
    expect(dialog.textContent).toContain("sends up to 2 real applications");
    expect(api.callsTo("POST /api/apply/batches")).toHaveLength(0);
    await click(button("Submit 2"));
    await waitFor(() => api.callsTo("POST /api/apply/batches").length === 1);
    expect(api.callsTo("POST /api/apply/batches")[0].body).toEqual({ candidacies: [1, 2], submit: true, confirmed: true });
  });

  it("drops a ticked row from the selection once it is skipped or has left the runnable lanes", async () => {
    const api = server({ "POST /api/apply/batches": () => ok({ runId: accepted.runId, batchId: "list_1", rows: 1, skipped: [] }) });
    api.on("PATCH /api/candidacies/1", () => ok({ entry: candidacy({ id: 1, status: "skipped", version: 2 }), changed: true, correction: false, inferred: false }));
    await mount(<ApplyNext />);
    await click(await waitFor(() => pick("Northwind Example")));
    await click(pick("Microsoft"));
    await openMore("Northwind Example");
    await click(await waitFor(() => byLabel(/^Skip this posting: Northwind Example/)));
    await waitFor(() => text().includes("1 row chosen."));
    // A run elsewhere takes Microsoft to Awaiting receipt; the next read of the list says so.
    const moved = { ...applyListWithSkips.lanes[0].items[1], lane: "awaiting", state: "submitted" };
    api.on("GET /api/apply/list", () => ok({ ...applyListWithSkips, lanes: applyListWithSkips.lanes.map((l) => (l.lane === "awaiting" ? { ...l, total: 1, items: [moved] } : l.lane === "apply-now" ? { ...l, items: [l.items[2]] } : l)) }));
    await click(button("Undo"));
    await waitFor(() => !text().includes("row chosen.") && !text().includes("rows chosen."));
  });

  it("records an Awaiting receipt row as submitted only after a confirmation", async () => {
    const waiting = { id: 9, company: "Contoso Example", role: "Platform Engineer", score: 4.2, lane: "awaiting", postingHost: "jobs.example.net", postingUrl: "https://jobs.example.net/contoso/9", date: "2026-09-27", version: 3, state: "submitted" };
    const list = { ...applyList, lanes: applyList.lanes.map((l) => (l.lane === "awaiting" ? { ...l, total: 1, items: [waiting] } : l)) };
    const api = server({ "GET /api/apply/list": () => ok(list), "POST /api/candidacies/9/confirm-submitted": () => ok(candidacy({ id: 9, status: "applied", appliedBy: "agent" })) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => byLabel(/^Confirm submitted: Contoso Example/)));
    const dialog = await waitFor(() => q("dialog"));
    expect(dialog.textContent).toContain("applied by Applysmith");
    expect(api.callsTo("POST /api/candidacies/9/confirm-submitted")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Confirm submitted")!);
    await waitFor(() => api.callsTo("POST /api/candidacies/9/confirm-submitted").length === 1);
    await waitFor(() => api.callsTo("GET /api/apply/list").length >= 2);
  });

  it("marks a row applied only after a confirmation, with the row version the list was read at", async () => {
    const row = candidacy({ id: 1, version: 5 });
    const api = server({ "PATCH /api/candidacies/1": () => ok({ entry: { ...row, status: "applied", version: 6 }, changed: true, correction: false, inferred: false }) });
    await mount(<ApplyNext />);
    await openMore("Northwind Example");
    await click(await waitFor(() => byLabel(/^Mark applied: Northwind Example/)));
    const dialog = await waitFor(() => q("dialog"));
    expect(dialog.textContent).toContain("Nothing is sent to the employer");
    expect(api.callsTo("PATCH /api/candidacies/1")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Mark applied")!);
    await waitFor(() => api.callsTo("PATCH /api/candidacies/1").length === 1);
    expect(api.callsTo("PATCH /api/candidacies/1")[0].body).toEqual({ status: "applied", expectedVersion: 1 });
    expect(api.callsTo("GET /api/candidacies/1")).toHaveLength(0);
    await waitFor(() => api.callsTo("GET /api/apply/list").length >= 2);
  });

  it("refuses to mark a row applied that moved since the list was drawn, says why, and reads the list again", async () => {
    const api = server({ "PATCH /api/candidacies/1": () => fail("revision_conflict", "the row changed since it was loaded; reload and try again") });
    await mount(<ApplyNext />);
    await openMore("Northwind Example");
    await click(await waitFor(() => byLabel(/^Mark applied: Northwind Example/)));
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Mark applied")!);
    await waitFor(() => text().includes("the row changed since it was loaded"));
    await waitFor(() => api.callsTo("GET /api/apply/list").length >= 2);
  });

  // Skip quotes the version the list was read at; Undo quotes the version the skip returned.
  it("skips a posting from its More menu or from Next up, keeps the keyboard on Undo, and Undo makes it evaluated again", async () => {
    const patchRow = "PATCH /api/candidacies/1";
    const api = server();
    let version = 1;
    api.on(patchRow, (c) => ok({ entry: candidacy({ id: 1, status: (c.body as { status: string }).status, version: ++version + 5 }), changed: true, correction: false, inferred: false }));
    await mount(<ApplyNext />);
    await openMore("Northwind Example");
    await click(await waitFor(() => byLabel(/^Skip this posting: Northwind Example/)));
    await waitFor(() => text().includes("Northwind Example is skipped and has left the list."));
    expect(api.callsTo(patchRow)[0].body).toEqual({ status: "skipped", expectedVersion: 1 });
    await waitFor(() => api.callsTo("GET /api/apply/list").length >= 2);
    expect(document.activeElement).toBe(button("Undo"));
    await click(button("Undo"));
    await waitFor(() => text().includes("Northwind Example is back on the list."));
    expect(api.callsTo(patchRow)[1].body).toEqual({ status: "evaluated", expectedVersion: 7 });
    await click(byLabel(/^Skip this posting for next up: Northwind Example/));
    await waitFor(() => api.callsTo(patchRow).length === 3);
    expect(api.callsTo(patchRow)[2].body).toEqual({ status: "skipped", expectedVersion: 1 });
  });

  it("does not skip a posting a run is working on", async () => {
    const lanes = applyListWithSkips.lanes.map((l) => (l.lane === "apply-now" ? { ...l, items: l.items.map((i) => (i.id === 1 ? { ...i, state: "running" } : i)) } : l));
    server({ "GET /api/apply/list": () => ok({ ...applyListWithSkips, lanes }) });
    await mount(<ApplyNext />);
    await openMore("Northwind Example");
    expect(byLabel(/^Skip this posting: Northwind Example/)?.disabled).toBe(true);
  });

  it("shows a filled form waiting at Submit in its kept tab, and submits it after the Autofill and submit dialog", async () => {
    const lanes = applyList.lanes.map((l) =>
      l.lane === "apply-now"
        ? { ...l, items: [{ ...l.items[0], state: "needs-user", runId: "run_f", keptTab: true, formWaiting: true, attempt: { runId: "run_f", at: stamp, mode: "dry-run", outcome: { kind: "filled" } } }] }
        : l,
    );
    const api = fakeApi({
      "GET /api/apply/list": () => ok({ ...applyList, lanes }),
      "GET /api/runs": () => ok({ entries: [] }),
      "GET /api/runs/run_f": () => ok({ runId: "run_f", kind: "apply", status: "running", createdAt: stamp, updatedAt: stamp, entry: entry({ id: "run_f" }) }),
      "POST /api/apply/runs/run_f/submit": () => ok({ runId: "run_f" }),
    });
    await mount(<ApplyNext />);
    await waitFor(() => text().includes("The form is filled and waits at Submit in its kept tab."));
    expect(byLabel(/^Jump to tab: Northwind Example/)?.disabled).toBe(false);
    await click(byLabel(/^Submit this form: Northwind Example/)!);
    const dialog = await waitFor(() => q("dialog"));
    expect(dialog.querySelector("h2")?.textContent).toBe("Submit the application to Northwind Example");
    expect(dialog.textContent).toContain("Staff Engineer at Northwind Example on jobs.example.com and press its final Submit button");
    expect(api.callsTo("POST /api/apply/runs/run_f/submit")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Submit application")!);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_f/submit").length === 1);
    expect(api.callsTo("POST /api/apply/runs/run_f/submit")[0].body).toEqual({ confirmed: true });
    expect(api.callsTo("POST /api/apply/runs")).toHaveLength(0);
  });

});

describe("Apply Next suggests the next row", () => {

  it("fills the next row without asking and asks before submitting it", async () => {
    const api = server({ "POST /api/apply/runs": () => ok(accepted) });
    await mount(<ApplyNext />);
    await click(await waitFor(() => byLabel(/^Next up, autofill only: Northwind Example/)));
    await waitFor(() => api.callsTo("POST /api/apply/runs").length === 1);
    expect(api.callsTo("POST /api/apply/runs")[0].body).toMatchObject({ submit: false, confirmed: false });
    expect(q("dialog")).toBeNull();
    await click(byLabel(/^Next up, autofill and submit: Northwind Example/)!);
    const dialog = await waitFor(() => q("dialog"));
    expect(dialog.querySelector("h2")?.textContent).toBe("Submit the application to Northwind Example");
    expect(api.callsTo("POST /api/apply/runs")).toHaveLength(1);
    await press("Escape", {}, dialog);
    await waitFor(() => !q("dialog"));
    expect(api.callsTo("POST /api/apply/runs")).toHaveLength(1);
  });

  it("offers no autofill on a sample posting whose site cannot exist, and leads with a real row when there is one", async () => {
    const sample = (i: (typeof applyList.lanes)[number]["items"][number]) => ({ ...i, postingHost: "careers.harborlight.example", postingUrl: "https://careers.harborlight.example/jobs/1001" });
    const only = { ...applyList, lanes: applyList.lanes.map((l) => (l.lane === "apply-now" ? { ...l, items: l.items.map(sample) } : l)) };
    const api = server({ "GET /api/apply/list": () => ok(only) });
    await mount(<ApplyNext />);
    const card = await waitFor(() => q("section[aria-labelledby='next-up']"));
    expect(card.querySelector(".next-up-help a")?.getAttribute("href")).toBe("/setup?step=orientation#demo-tutorial");
    expect(byLabel(/^Next up, autofill only: /)!.disabled).toBe(true);
    expect(byLabel(/^Autofill only: Northwind Example/)!.disabled).toBe(true);
    expect(text()).toContain("Sample postings offer no autofill");
    expect(button("Autofill all")).toBeNull();
    expect(api.callsTo("POST /api/apply/runs")).toHaveLength(0);

    const real = { ...applyList.lanes[0].items[0], id: 9, company: "Contoso Example", score: 3.6 };
    const mixed = { ...only, lanes: only.lanes.map((l) => (l.lane === "apply-now" ? { ...l, total: 2, items: [...l.items, real] } : l)) };
    server({ "GET /api/apply/list": () => ok(mixed) });
    await mount(<ApplyNext />);
    const next = await waitFor(() => q("section[aria-labelledby='next-up'] .next-up-name"));
    expect(next.textContent).toContain("Contoso Example");
  });
});

