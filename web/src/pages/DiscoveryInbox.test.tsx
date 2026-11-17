import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, byText, click, mount, q, qa, text, waitFor } from "../testing/dom";
import { inboxRow, sources } from "../testing/fixtures_discovery";
import Discovery from "./Discovery";

const open = (search = "?tab=inbox") => window.history.replaceState(null, "", "/discovery" + search);

const counts = { pending: 2, queued: 1, evaluated: 1, dismissed: 0, expired: 0 };
const page = (rows: unknown[], over: Record<string, unknown> = {}) => ({ rows, page: 1, pageSize: 25, total: rows.length, counts, ...over });
const rows = [inboxRow(), inboxRow({ id: 2, company: "Contoso Example", role: "Backend Engineer", location: "Austin, TX", source: "github", score: 4, url: "https://jobs.example.com/contoso/2" })];

function server(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}): FakeApi {
  return fakeApi({ "GET /api/scan-sources": () => ok({ sources }), "GET /api/inbox": () => ok(page(rows)), ...extra });
}

describe("Discovery: Inbox", () => {
  it("opens from the address and lists the waiting postings with company, role, location, source, rank and first seen", async () => {
    open();
    const api = server();
    await mount(<Discovery />);
    await waitFor(() => qa("tbody tr").length === 2);
    // The tab counts what waits (the fixture's two).
    expect(qa('[role="tab"]').find((t) => t.textContent === "Inbox2")?.getAttribute("aria-selected")).toBe("true");
    const first = qa("tbody tr")[0].textContent ?? "";
    for (const part of ["Northwind Example", "Staff Engineer", "Remote", "registry", "7", "2026-10-12"]) expect(first).toContain(part);
    expect(api.callsTo("GET /api/inbox")[0].query.get("status")).toBe("pending");
    expect(qa("a").some((a) => a.getAttribute("href") === "https://jobs.example.com/northwind/1" && a.textContent === "Open posting: Northwind Example, Staff Engineer")).toBe(true);
    expect(text()).toContain("2 waiting");
  });

  it("starts an evaluation with Evaluate, says it started, and reloads the list", async () => {
    open();
    let waiting = rows;
    const api = server({
      "GET /api/inbox": () => ok(page(waiting)),
      "POST /api/inbox/promote": () => {
        waiting = rows.slice(1);
        return ok({ queued: [1], rejected: {} });
      },
    });
    await mount(<Discovery />);
    await waitFor(() => qa("tbody tr").length === 2);
    await click(byLabel(/^Evaluate Northwind Example, Staff Engineer$/));
    expect(api.callsTo("POST /api/inbox/promote")[0].body).toEqual({ ids: [1] });
    await waitFor(() => qa("tbody tr").length === 1);
    expect(q('[role="status"].note')?.textContent).toContain("Evaluation of Northwind Example, Staff Engineer started.");
    expect(qa("a").some((a) => a.getAttribute("href") === "/discovery?tab=inbox&status=queued")).toBe(true);
    // The keyboard goes on to the row that took its place, not the page's top.
    await waitFor(() => document.activeElement === byLabel(/^Evaluate Contoso Example/));
  });

  it("evaluates every waiting row of the page at once and names what the gate kept, with its rule", async () => {
    open();
    const api = server({ "POST /api/inbox/promote": () => ok({ queued: [1], rejected: { "2": "clearance" } }) });
    await mount(<Discovery />);
    await waitFor(() => qa("tbody tr").length === 2);
    expect(text()).toContain("A scan evaluates only its top rows");
    await click(button("Evaluate the 2 on this page"));
    expect(api.callsTo("POST /api/inbox/promote")[0].body).toEqual({ ids: [1, 2] });
    await waitFor(() => (q('[role="status"] > .note')?.textContent ?? "").includes("Started 1 evaluation."));
    // The gate's rule is in the answer only: the note names the kept row with it.
    expect(q('[role="status"] > .note')?.textContent).toContain("Contoso Example, Backend Engineer (clearance)");
  });

  it("evaluates or dismisses only the ticked rows, and one Undo restores every dismissal", async () => {
    open();
    let gone: number[] = [];
    const api = server({
      "GET /api/inbox": () => ok(page(rows.filter((r) => !gone.includes(r.id)))),
      "POST /api/inbox/promote": () => ok({ queued: [2], rejected: {} }),
      ...Object.fromEntries(rows.flatMap((r) => [
        [`POST /api/inbox/${r.id}/dismiss`, () => ((gone = [...gone, r.id]), ok(r))],
        [`POST /api/inbox/${r.id}/restore`, () => ((gone = gone.filter((x) => x !== r.id)), ok(r))],
      ])),
    });
    await mount(<Discovery />);
    await waitFor(() => qa("tbody tr").length === 2);
    // A live region announces a change to its content, not its own arrival, so
    // the count's region is on the page before the first tick.
    const live = qa("[aria-live=polite]");
    await click(q('input[aria-label="Select Contoso Example, Backend Engineer"]'));
    // The actions on ticked rows sit in a bar after the list, with the count announced.
    const count = q('[role=region][aria-label="Selected rows"] [aria-live=polite]');
    expect(count?.textContent).toBe("1 row chosen.");
    expect(live).toContain(count);
    await click(button("Evaluate 1 selected"));
    expect(api.callsTo("POST /api/inbox/promote")[0].body).toEqual({ ids: [2] });
    // The batch clears the ticks; Select all ticks the page.
    await waitFor(() => button("Evaluate the 2 on this page"));
    await click(byText("label", /Select all 2/)?.querySelector("input") ?? null);
    await click(button("Dismiss 2 selected"));
    await waitFor(() => text().includes("Dismissed 2 postings."));
    await waitFor(() => qa("tbody tr").length === 0);
    await click(button("Undo"));
    expect(api.callsTo("POST /api/inbox/1/restore")).toHaveLength(1);
    expect(api.callsTo("POST /api/inbox/2/restore")).toHaveLength(1);
    await waitFor(() => qa("tbody tr").length === 2);
  });

  it("shows a failed start and keeps the row", async () => {
    open();
    server({ "POST /api/inbox/promote": () => fail("unavailable", "The evaluation queue is full; retry shortly.") });
    await mount(<Discovery />);
    await waitFor(() => qa("tbody tr").length === 2);
    await click(byLabel(/^Evaluate Northwind Example/));
    expect(q('[role="alert"]')?.textContent).toContain("The evaluation queue is full");
    expect(qa("tbody tr")).toHaveLength(2);
  });
});
