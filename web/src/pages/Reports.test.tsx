import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { byText, click, mount, q, qa, text, waitFor, button } from "../testing/dom";
import { candidacy, reportDetail, reportPage } from "../testing/fixtures";
import Reports from "./Reports";

// pick is the list button of one report, found by the report number it shows.
const pick = (n: number) => qa<HTMLButtonElement>("button.row-pick").find((b) => b.textContent?.includes(`Report ${n} `)) ?? null;

function server() {
  return fakeApi({
    "GET /api/reports": () => ok(reportPage),
    "GET /api/reports/7": () => ok(reportDetail()),
    "GET /api/reports/6": () => ok(reportDetail({ number: 6, company: "Contoso Example", legitimacy: "unclear", verification: "pasted unverified 2026-09-28" })),
  });
}

describe("Reports", () => {
  it("lists the catalog and views a report with score, verification and legitimacy chips", async () => {
    server();
    await mount(<Reports />);
    await waitFor(() => text().includes("Contoso Example"));
    await click(pick(7));
    await waitFor(() => q("article"));
    const chips = qa(".viewer .chip").map((c) => c.textContent);
    expect(chips).toEqual(expect.arrayContaining(["Score 4.3", "Apply", "Posting live on 2026-09-28", "Legitimacy credible"]));
    expect(q(".viewer .chip.good span")?.getAttribute("title")).toBe("browser live 2026-09-28");
    expect(window.location.search).toBe("?id=7");
    // The catalog counts what the filters leave.
    expect(q("#catalog")?.parentElement?.textContent).toContain("2 reports");
  });

  it("starts an application from the report: Autofill only at once, a submit only after its confirmation", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const api = server();
    api.on("GET /api/applications/lookup", () => ok({ found: false }));
    api.on("POST /api/apply/runs", () => ok({ runId: "run_r" }));
    api.on("GET /api/apply/skip-status", () => ok({ company: "Northwind Example", listed: false }));
    await mount(<Reports />);
    await click(await waitFor(() => button("Autofill only")));
    await waitFor(() => byText("a", "Follow the run")?.getAttribute("href") === "/runs/run_r");
    expect(api.callsTo("POST /api/apply/runs")[0].body).toEqual({ url: "https://jobs.example.com/northwind/1", company: "Northwind Example", role: "Staff Engineer", submit: false, confirmed: false });
    await click(button("Autofill and submit…"));
    await waitFor(() => q("dialog")?.textContent?.includes("Northwind Example is not on your batch skip list."));
    expect(api.callsTo("POST /api/apply/runs")).toHaveLength(1);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Submit application")!);
    await waitFor(() => api.callsTo("POST /api/apply/runs").length === 2);
    expect(api.callsTo("POST /api/apply/runs")[1].body).toMatchObject({ submit: true, confirmed: true });
  });

  // Owner decision: a skipped or discarded posting may still be applied to, as a deliberate override that says why it
  // was skipped. The server takes an archived row out of the archive when the run records the submission.
  it("applies anyway to a posting the user discarded, saying when", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const api = server();
    api.on("GET /api/applications/lookup", () =>
      ok({ found: true, entry: candidacy({ id: 12, status: "discarded", archivedAt: "2026-09-30T00:00:00Z" }), history: [{ status: "evaluated", at: "2026-09-01T12:00:00Z", actor: "evaluation" }, { status: "discarded", at: "2026-09-05T12:00:00Z", actor: "user" }] }),
    );
    api.on("POST /api/apply/runs", () => ok({ runId: "run_r" }));
    api.on("GET /api/apply/skip-status", () => ok({ company: "Northwind Example", listed: false }));
    await mount(<Reports />);
    await waitFor(() => q(".viewer")?.textContent?.includes("discarded this posting on 2026-09-05"));
    expect(button("Autofill only")).toBeNull();
    await click(button("Autofill and submit anyway…"));
    await waitFor(() => q("dialog")?.textContent?.includes("discarded this posting on 2026-09-05"));
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Submit application")!);
    await waitFor(() => api.callsTo("POST /api/apply/runs").length === 1);
    expect(api.callsTo("POST /api/apply/runs")[0].body).toMatchObject({ url: "https://jobs.example.com/northwind/1", submit: true, confirmed: true });
  });

  // Skip quotes the version the lookup read; Undo quotes the version the skip returned. Only an evaluated row is offered.
  it("skips the report's posting from the viewer, keeps the keyboard on Undo, and Undo makes it evaluated again", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const patchRow = "PATCH /api/candidacies/12";
    const api = server();
    let status = "evaluated";
    api.on("GET /api/applications/lookup", () => ok({ found: true, entry: candidacy({ id: 12, status, version: 4 }) }));
    api.on(patchRow, (c) => {
      status = (c.body as { status: string }).status;
      return ok({ entry: candidacy({ id: 12, status, version: 9 }), changed: true, correction: false, inferred: false });
    });
    await mount(<Reports />);
    await click(await waitFor(() => button("Skip this posting")));
    await waitFor(() => text().includes("Northwind Example is skipped and no longer offered in Apply Next."));
    expect(api.callsTo(patchRow)[0].body).toEqual({ status: "skipped", expectedVersion: 4 });
    await waitFor(() => !button("Skip this posting"));
    expect(document.activeElement).toBe(button("Undo"));
    await click(button("Undo"));
    await waitFor(() => button("Skip this posting"));
    expect(api.callsTo(patchRow)[1].body).toEqual({ status: "evaluated", expectedVersion: 9 });
  });

  it("refuses to skip a posting that moved since the report was read, says why, and looks the row up again", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const api = server();
    api.on("GET /api/applications/lookup", () => ok({ found: true, entry: candidacy({ id: 12, status: "evaluated", version: 4 }) }));
    api.on("PATCH /api/candidacies/12", () => fail("revision_conflict", "the row changed since it was loaded; reload and try again"));
    await mount(<Reports />);
    await click(await waitFor(() => button("Skip this posting")));
    await waitFor(() => text().includes("the row changed since it was loaded"));
    expect(text()).not.toContain("is skipped and no longer offered");
    await waitFor(() => api.callsTo("GET /api/applications/lookup").length >= 2);
  });

  it("offers no new start for a posting the tracker has as sent", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const api = server();
    api.on("GET /api/applications/lookup", () => ok({ found: true, entry: candidacy({ id: 12, status: "applied" }) }));
    await mount(<Reports />);
    await waitFor(() => q(".report-actions")?.textContent?.includes("Tracker: applied"));
    expect(button("Autofill only")).toBeNull();
  });

  it("offers no autofill for a sample posting whose site cannot exist, and says why", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const api = server();
    api.on("GET /api/reports/7", () => ok(reportDetail({ postingUrl: "https://careers.harborlight.example/jobs/1001" })));
    api.on("GET /api/applications/lookup", () => ok({ found: false }));
    await mount(<Reports />);
    await waitFor(() => q(".report-actions")?.textContent?.includes("A sample posting: its site does not exist"));
    expect(button("Autofill only")).toBeNull();
    expect(button("Autofill and submit…")).toBeNull();
  });

  it("renders an embedded script as literal text and runs nothing", async () => {
    (window as unknown as { pwned?: boolean }).pwned = false;
    window.history.replaceState(null, "", "/reports?id=7");
    server();
    await mount(<Reports />);
    await waitFor(() => q(".markdown"));
    expect(q(".markdown script")).toBeNull();
    expect(q(".markdown")?.textContent).toContain("<script>window.pwned = true</script>");
    expect((window as unknown as { pwned?: boolean }).pwned).toBe(false);
  });

  it("opens links with noopener noreferrer", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    server();
    await mount(<Reports />);
    const link = await waitFor(() => qa<HTMLAnchorElement>(".markdown a").find((a) => a.textContent === "the posting"));
    expect(link.rel).toBe("noopener noreferrer");
    expect(link.target).toBe("_blank");
  });

  // Without force the server answers a posting that has a final report with that report, and nothing runs.
  it("evaluates the posting again from the report, forcing a new report, and links the run", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const api = server();
    api.on("GET /api/applications/lookup", () => ok({ found: false }));
    api.on("POST /api/evaluations", () => ok({ runId: "run_e" }));
    await mount(<Reports />);
    await click(await waitFor(() => button("Evaluate again")));
    await waitFor(() => byText("a", "Follow the run")?.getAttribute("href") === "/runs/run_e");
    expect(api.callsTo("POST /api/evaluations")[0].body).toMatchObject({ url: "https://jobs.example.com/northwind/1", mode: "deep", force: true });
    expect(button("Evaluate again")?.disabled).toBe(true);
  });

  it("shows a failed report without blanking the catalog", async () => {
    window.history.replaceState(null, "", "/reports?id=7");
    const api = server();
    api.on("GET /api/reports/7", () => fail("not_found", "the report file is not available"));
    await mount(<Reports />);
    await waitFor(() => text().includes("report file is not available"));
    expect(text()).toContain("Contoso Example");
    expect(byText("button", "Try again")).toBeNull();
  });
});
