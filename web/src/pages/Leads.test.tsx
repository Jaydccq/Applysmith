import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { lead, signalPage, signalRow, statusOk } from "../testing/fixtures_discovery";
import { button, byLabel, byText, click, mount, q, qa, text, waitFor } from "../testing/dom";
import LeadsAndSignals from "./Leads";

const open = (search = "") => window.history.replaceState(null, "", "/leads" + search);
const page = (rows: unknown[], total = rows.length, counts: Record<string, number> = { posting: rows.length }) => ({ rows, page: 1, pageSize: 25, total, counts });

function server(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}): FakeApi {
  return fakeApi({
    "GET /api/leads": () => ok(page([lead()], 1, { posting: 1, "careers-page": 2 })),
    "GET /api/signals": () => ok(signalPage([signalRow()])),
    "GET /api/signals/status": () => ok(statusOk),
    "GET /api/signals/review": () => ok({ items: [] }),
    ...extra,
  });
}

describe("Leads and Signals: leads", () => {
  it("evaluates one lead as a run with an idempotency key", async () => {
    open();
    const api = server({ "POST /api/leads/evaluate": () => ok({ runId: "run_1" }) });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Evaluate Northwind Example$/));
    await click(byLabel(/^Evaluate Northwind Example$/));
    const [call] = api.callsTo("POST /api/leads/evaluate");
    expect(call.body).toEqual({ ids: [1] });
    expect(call.headers.get("Idempotency-Key")).toMatch(/^web-/);
    expect(text()).toContain("Evaluation of Northwind Example started.");
    expect(byText("a", "Follow the run")?.getAttribute("href")).toBe("/runs/run_1");
  });

  it("holds Evaluate while the evaluation pass runs, which the server refuses to start twice", async () => {
    open();
    let running = false;
    const api = server({
      "GET /api/leads": () => ok({ ...page([lead()]), evaluating: running || undefined }),
      "POST /api/leads/evaluate": () => ((running = true), ok({ runId: "run_1" })),
    });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Evaluate Northwind Example$/));
    await click(byLabel(/^Evaluate Northwind Example$/));
    await waitFor(() => (byLabel(/^Evaluate Northwind Example$/) as HTMLButtonElement).disabled);
    expect((button("Evaluate new leads") as HTMLButtonElement).disabled).toBe(true);
    await click(byLabel(/^Evaluate Northwind Example$/));
    expect(api.callsTo("POST /api/leads/evaluate")).toHaveLength(1);
  });

  it("dismisses a lead and reloads the list", async () => {
    open();
    let rows = [lead()];
    const api = server({
      "GET /api/leads": () => ok(page(rows)),
      "POST /api/leads/1/dismiss": () => {
        rows = [];
        return ok(lead({ state: "dismissed" }));
      },
      "POST /api/leads/1/restore": () => {
        rows = [lead()];
        return ok(lead());
      },
    });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Dismiss Northwind Example$/));
    await click(byLabel(/^Dismiss Northwind Example$/));
    expect(api.callsTo("POST /api/leads/1/dismiss")).toHaveLength(1);
    await waitFor(() => text().includes("No new lead."));
    expect(text()).toContain("Dismissed Northwind Example.");
    // Nothing is new, so a pass over the new leads has nothing to do.
    expect(button("Evaluate new leads")!.disabled).toBe(true);
    // Undo puts the lead back and clears the note.
    await click(button("Undo")!);
    expect(api.callsTo("POST /api/leads/1/restore")).toHaveLength(1);
    await waitFor(() => Boolean(byLabel(/^Dismiss Northwind Example$/)));
    expect(text()).not.toContain("Dismissed Northwind Example.");
  });

  it("asks before blocking the company and blocks it by name", async () => {
    open();
    const api = server({ "POST /api/blocked-companies": () => ok({ id: 1, name: "Northwind Example", normalized: "northwind", reason: "", addedBy: "dashboard", addedAt: "2026-10-12T09:00:00.000Z" }) });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Block company: Northwind Example$/));
    await click(byLabel(/^Block company: Northwind Example$/));
    expect(api.callsTo("POST /api/blocked-companies")).toHaveLength(0);
    await click(qa("dialog button").find((b) => b.textContent === "Block")!);
    expect((api.callsTo("POST /api/blocked-companies")[0].body as { name: string }).name).toBe("Northwind Example");
    await waitFor(() => text().includes("Blocked Northwind Example."));
    // The company was blocked before this press, so Undo would lift that block.
    expect(button("Undo")).toBeNull();
  });

  it("undoes a block this press added", async () => {
    open();
    const api = server({
      "POST /api/blocked-companies": () => ok({ id: 1, name: "Northwind Example", normalized: "northwind", reason: "", addedBy: "dashboard", addedAt: new Date().toISOString() }),
      "DELETE /api/blocked-companies": () => ok({ companies: [] }),
    });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Block company: Northwind Example$/));
    await click(byLabel(/^Block company: Northwind Example$/));
    await click(qa("dialog button").find((b) => b.textContent === "Block")!);
    await waitFor(() => Boolean(button("Undo")));
    await click(button("Undo")!);
    expect(api.callsTo("DELETE /api/blocked-companies")[0].query.get("name")).toBe("Northwind Example");
  });

  it("starts a pass over all the new leads", async () => {
    open();
    const api = server({ "POST /api/leads/evaluate": () => ok({ runId: "run_1" }), "POST /api/leads/expand": () => ok({ runId: "run_2" }) });
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Evaluate new leads"));
    await click(button("Evaluate new leads"));
    expect(api.callsTo("POST /api/leads/evaluate")[0].body).toEqual({});
    await click(button("Read new careers pages"));
    expect(api.callsTo("POST /api/leads/expand")).toHaveLength(1);
    expect(text()).toContain("Reading the new careers pages started.");
  });

  it("shows a refusal from the server for a pass that is already running", async () => {
    open();
    server({ "POST /api/leads/evaluate": () => fail("conflict", "a evaluate pass over the leads is already running") });
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Evaluate new leads"));
    await click(button("Evaluate new leads"));
    expect(q('[role="alert"]')?.textContent).toContain("already running");
  });
});
