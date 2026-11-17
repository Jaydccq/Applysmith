import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { assessment, signalDetail, signalPage, signalRow, statusOk } from "../testing/fixtures_discovery";
import { button, byLabel, byText, click, mount, q, qa, text, waitFor } from "../testing/dom";
import LeadsAndSignals from "./Leads";

const open = (search = "?tab=signals") => window.history.replaceState(null, "", "/leads" + search);

const fabrikam = signalRow({ companyId: 8, company: "Fabrikam Example", tier: "warm", state: "watching", blocked: true, sources: [], newestEvidence: undefined });

function server(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}): FakeApi {
  return fakeApi({
    "GET /api/signals": () => ok(signalPage([signalRow(), fabrikam])),
    "GET /api/signals/status": () => ok(statusOk),
    "GET /api/signals/review": () => ok({ items: [] }),
    ...extra,
  });
}

const held = {
  id: 11, companyId: 7, company: "Contoso Example", reason: "quote_failed", state: "open", createdAt: "2026-10-12T09:00:00.000Z",
  assessment: assessment({ id: 2, status: "quote_failed", quotes: [{ documentId: 3, quote: "we will triple the team", verified: false }] }),
};

describe("Leads and Signals: hiring signals", () => {
  it("lists companies with their tier, state, sources and the date of the newest evidence", async () => {
    open();
    server();
    await mount(<LeadsAndSignals />);
    await waitFor(() => text().includes("Contoso Example"));
    const list = q("#signals-list")!.closest("section")!.textContent!;
    expect(list).toContain("hot");
    expect(list).toContain("SEC EDGAR filings, Hacker News");
    expect(list).toContain("Newest evidence");
    expect(list).toContain("Fabrikam Example");
    expect(list).toContain("blocked");
    expect(list).toContain("no source");
  });

  it("switches a source on in portals.yml and shows what its next collection said", async () => {
    open();
    let on = false;
    const api = server({
      "GET /api/signals/status": () => ok({ ...statusOk, lanes: [statusOk.lanes[0], { source: "signals-hn", title: "Hacker News", enabled: on, ...(on && { lastStatus: "skipped: no queries" }) }] }),
      "GET /api/config/portals": () => ok({ file: "portals", exists: true, template: false, content: "", revision: "r4", issues: [], values: { sources: { "signals-hn": { queries: ["rust"] } } } }),
      "PATCH /api/config/portals": () => ((on = true), ok({ revision: "r5", changed: true })),
    });
    await mount(<LeadsAndSignals />);
    const hn = await waitFor(() => qa<HTMLInputElement>('input[role="switch"]').find((i) => i.closest("label")!.textContent === "Hacker News"));
    await click(hn);
    await waitFor(() => text().includes("last collection skipped: no queries"));
    expect(api.callsTo("PATCH /api/config/portals").map((c) => c.body)).toEqual([{ expectedRevision: "r4", ops: [{ op: "set", path: "sources.signals-hn.enabled", value: "true" }] }]);
  });

  it("shows what the SEC source sends before it switches it on, and writes nothing on Cancel", async () => {
    open();
    const api = server({
      "GET /api/signals/status": () => ok({ ...statusOk, lanes: [{ ...statusOk.lanes[0], enabled: false }, statusOk.lanes[1]] }),
      "GET /api/config/portals": () => ok({ file: "portals", exists: true, template: false, content: "", revision: "r4", issues: [], values: { sources: {} } }),
      "PATCH /api/config/portals": () => ok({ revision: "r5", changed: true }),
    });
    await mount(<LeadsAndSignals />);
    const edgar = () => qa<HTMLInputElement>('input[role="switch"]').find((i) => i.closest("label")!.textContent === "SEC EDGAR filings")!;
    await click(await waitFor(edgar));
    expect(q("dialog")?.textContent).toContain("Every request to the SEC carries your contact email");
    await click(button("Cancel"));
    expect(api.callsTo("PATCH /api/config/portals")).toHaveLength(0);
    await click(edgar());
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Turn on")!);
    await waitFor(() => api.callsTo("PATCH /api/config/portals").length === 1);
    expect(api.callsTo("PATCH /api/config/portals")[0].body).toEqual({ expectedRevision: "r4", ops: [{ op: "set", path: "sources", value: "signals-edgar:\n  enabled: true" }] });
  });

  it("shows an error with retry for the list", async () => {
    open();
    let calls = 0;
    server({ "GET /api/signals": () => (++calls === 1 ? fail("internal", "boom") : ok(signalPage([signalRow()]))) });
    await mount(<LeadsAndSignals />);
    await waitFor(() => q('[role="alert"]'));
    await click(button("Try again"));
    await waitFor(() => text().includes("Contoso Example"));
  });

  it("watches and dismisses a company from its row, and undoes the dismissal", async () => {
    open();
    let contoso = signalRow();
    const api = server({
      "GET /api/signals": () => ok(signalPage([contoso, fabrikam])),
      "POST /api/signals/7/dismiss": () => {
        contoso = signalRow({ state: "dismissed" });
        return ok(contoso);
      },
      "POST /api/signals/7/restore": () => {
        contoso = signalRow();
        return ok(contoso);
      },
      "POST /api/signals/8/watch": () => ok(signalRow()),
    });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Dismiss Contoso Example$/));
    await click(byLabel(/^Dismiss Contoso Example$/));
    expect(api.callsTo("POST /api/signals/7/dismiss")).toHaveLength(1);
    expect(byLabel(/^Watch Fabrikam Example$/)).toBeNull();
    expect(byLabel(/^Watch Contoso Example$/)).not.toBeNull();
    await waitFor(() => !byLabel(/^Dismiss Contoso Example$/));
    await click(button("Undo"));
    await waitFor(() => byLabel(/^Dismiss Contoso Example$/));
  });

  it("starts a collection with an idempotency key and rescoring without a model call", async () => {
    open();
    const api = server({
      "POST /api/signals/ticks": () => ok({ runId: "run_3" }),
      "POST /api/signals/rescore": () => ok({ companies: 8, tiers: { hot: 1, warm: 2, cold: 5 } }),
    });
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Collect now"));
    await click(button("Collect now"));
    expect(api.callsTo("POST /api/signals/ticks")[0].headers.get("Idempotency-Key")).toMatch(/^web-/);
    expect(text()).toContain("Collection started.");
    expect(byText("a", "Follow the run")?.getAttribute("href")).toBe("/runs/run_3");
    await click(button("Rescore"));
    expect(api.callsTo("POST /api/signals/rescore")).toHaveLength(1);
    expect(text()).toContain("Rescored 8 companies under the current policy.");
  });

  it("shows a refusal to rescore", async () => {
    open();
    server({ "POST /api/signals/rescore": () => fail("internal", "scoring failed") });
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Rescore"));
    await click(button("Rescore"));
    expect(q('[role="alert"]')?.textContent).toContain("scoring failed");
  });
});

describe("Leads and Signals: evidence chain", () => {
  function withDetail(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}, detail = signalDetail()) {
    return server({ "GET /api/signals/7": () => ok(detail), "GET /api/config/portals": () => ok({ file: "portals", exists: true, template: false, content: "", revision: "r1", issues: [], values: { companies: [] } }), ...extra });
  }

  it("evaluates the matching postings and reports how many were queued", async () => {
    open("?tab=signals&signal=7");
    const api = withDetail({ "POST /api/signals/7/evaluate-postings": () => ok({ queued: [4, 5] }) });
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Evaluate its matching postings"));
    await click(button("Evaluate its matching postings"));
    expect(api.callsTo("POST /api/signals/7/evaluate-postings")).toHaveLength(1);
    expect(text()).toContain("2 postings queued for evaluation.");
    expect(byText("a", "See them evaluating")?.getAttribute("href")).toBe("/discovery?tab=inbox&status=queued");
  });

  it("adds the company to portals with the board its signal found", async () => {
    open("?tab=signals&signal=7");
    const api = withDetail({ "PATCH /api/config/portals": () => ok({ revision: "r2", changed: true }) });
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Add to portals"));
    await click(button("Add to portals"));
    const [call] = api.callsTo("PATCH /api/config/portals");
    expect(call.body).toEqual({ expectedRevision: "r1", ops: [{ op: "set", path: "companies", value: expect.stringContaining("contoso-example") }] });
    expect((call.body as { ops: { value: string }[] }).ops[0].value).toContain("Contoso Example");
    expect(text()).toContain("Contoso Example was added to portals.yml.");
  });

  it("does not add a company that portals already lists", async () => {
    open("?tab=signals&signal=7");
    const api = withDetail({ "GET /api/config/portals": () => ok({ file: "portals", exists: true, template: false, content: "", revision: "r1", issues: [], values: { companies: [{ name: "contoso example", ats: "lever", slug: "x" }] } }) });
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Add to portals"));
    await click(button("Add to portals"));
    expect(text()).toContain("Contoso Example is already in portals.yml.");
    expect(api.callsTo("PATCH /api/config/portals")).toHaveLength(0);
  });

  it("lifts the block of a blocked company", async () => {
    open("?tab=signals&signal=7");
    const api = withDetail({ "POST /api/signals/7/lift-block": () => ok(signalRow()) }, signalDetail({ row: signalRow({ blocked: true }), blockExpires: "2026-11-01T00:00:00.000Z" }));
    await mount(<LeadsAndSignals />);
    await waitFor(() => button("Lift the block"));
    expect(q("dialog")!.textContent).toContain("blocked until");
    await click(button("Lift the block"));
    expect(api.callsTo("POST /api/signals/7/lift-block")).toHaveLength(1);
  });
});

describe("Leads and Signals: review queue", () => {
  it("approves and rejects an item, reloads the queue and undoes the rejection", async () => {
    open("?tab=review");
    let items = [held, { ...held, id: 12, company: "Fabrikam Example" }];
    const api = server({
      "GET /api/signals/review": () => ok({ items }),
      "POST /api/signals/review/11/approve": () => {
        items = items.filter((i) => i.id !== 11);
        return ok({ ...held, state: "approved" });
      },
      "POST /api/signals/review/12/reject": () => {
        items = [];
        return ok({ ...held, id: 12, state: "rejected" });
      },
      "POST /api/signals/review/12/restore": () => {
        items = [{ ...held, id: 12, company: "Fabrikam Example" }];
        return ok(items[0]);
      },
    });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Approve the assessment for Contoso Example$/));
    await click(byLabel(/^Approve the assessment for Contoso Example$/));
    expect(api.callsTo("POST /api/signals/review/11/approve")).toHaveLength(1);
    await waitFor(() => byLabel(/^Reject the assessment for Fabrikam Example$/) && !byLabel(/^Approve the assessment for Contoso Example$/));
    // An approval has no undo, so the page says it was taken.
    expect(q('[role="status"]')?.textContent).toContain("Approved the assessment for Contoso Example.");
    await click(byLabel(/^Reject the assessment for Fabrikam Example$/));
    expect(api.callsTo("POST /api/signals/review/12/reject")).toHaveLength(1);
    await waitFor(() => text().includes("Nothing is waiting for review."));
    await click(button("Undo"));
    await waitFor(() => byLabel(/^Reject the assessment for Fabrikam Example$/));
  });

  it("shows a conflict when the item was decided elsewhere", async () => {
    open("?tab=review");
    server({ "GET /api/signals/review": () => ok({ items: [held] }), "POST /api/signals/review/11/approve": () => fail("conflict", "this review item was decided already") });
    await mount(<LeadsAndSignals />);
    await waitFor(() => byLabel(/^Approve the assessment for Contoso Example$/));
    await click(byLabel(/^Approve the assessment for Contoso Example$/));
    expect(q('[role="alert"]')?.textContent).toContain("decided already");
  });
});
