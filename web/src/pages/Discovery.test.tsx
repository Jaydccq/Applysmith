import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, sseResponse, type FakeApi } from "../testing/api";
import { button, byLabel, byText, click, mount, q, qa, text, type, waitFor } from "../testing/dom";
import { inboxRow, scan, source, sources } from "../testing/fixtures_discovery";
import Discovery from "./Discovery";

const open = (search = "") => window.history.replaceState(null, "", "/discovery" + search);

function server(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}): FakeApi {
  return fakeApi({ "GET /api/scan-sources": () => ok({ sources }), "GET /api/scans": () => ok({ scans: [], page: 1, limit: 20, total: 0 }), ...extra });
}

const progress = (id: number, line: string) => `id: e-${id}\nevent: progress\ndata: ${JSON.stringify({ runId: "run_1", line })}\n\n`;

describe("Discovery: Run", () => {
  // A registry scan with no board to read ends skipped (registry-empty). Its
  // row says so before a run and downloads the lists itself, the seed of the
  // Sources and portals tab; the row is ready once the lists are in.
  it("says the registry is empty on its row and downloads the board lists from there", async () => {
    open();
    let seeded = false;
    const empty = source({ title: "Company job boards", readiness: "needs boards", reason: "The registry is empty: download the lists of company boards for it to read." });
    const api = server({
      "GET /api/scan-sources": () => ok({ sources: [seeded ? source({ title: "Company job boards" }) : empty] }),
      "POST /api/registry/seed": () => {
        seeded = true;
        return ok({ runId: "run_seed" });
      },
      "GET /api/runs/run_seed/events": () => sseResponse([`id: e-1\nevent: final\ndata: ${JSON.stringify({ runId: "run_seed", status: "succeeded", outcome: "120 new boards from 3 lists" })}\n\n`]),
    });
    await mount(<Discovery />);
    await waitFor(() => q('li[aria-label="Company job boards"]'));
    expect(q('li[aria-label="Company job boards"]')!.textContent).toContain("The registry is empty");
    expect(byLabel(/^Start Company job boards$/)!.disabled).toBe(true);
    await click(button("Download the board lists"));
    expect(api.callsTo("POST /api/registry/seed")[0].headers.get("Idempotency-Key")).toMatch(/^web-/);
    await waitFor(() => byLabel(/^Start Company job boards$/)?.disabled === false);
  });

  it("starts a scan with an idempotency key and follows it on the Monitor tab", async () => {
    open();
    const api = server({ "POST /api/scans": () => ok({ runId: "run_1" }) });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Start registry$/));
    await click(byLabel(/^Start registry$/));
    const [call] = api.callsTo("POST /api/scans");
    expect(call.body).toEqual({ source: "registry" });
    expect(call.headers.get("Idempotency-Key")).toMatch(/^web-/);
    await waitFor(() => window.location.search === "?tab=monitor&run=run_1");
  });

  it("sends only the options the user set, and names a field that is out of range without sending anything", async () => {
    open("?source=registry");
    const api = server({ "POST /api/scans": () => ok({ runId: "run_1" }) });
    await mount(<Discovery />);
    await waitFor(() => q("dialog form"));
    await type(q<HTMLInputElement>('dialog input[type="number"][max="1000"]'), "5000");
    await click(button("Start scan"));
    expect(q('dialog [role="alert"]')?.textContent).toContain("Results per search must be a whole number from 0 to 1000.");
    expect(api.callsTo("POST /api/scans")).toHaveLength(0);

    await type(q<HTMLInputElement>('dialog input[type="number"][max="1000"]'), "10");
    await type(byText("label", /^Search text/)!.querySelector("input"), "platform engineer");
    const scope = byText("label", /^Location scope/)!.querySelector("select")!;
    // The scopes of internal/discovery/scope.go, each shown by its name and sent as its id.
    expect([...scope.options].map((o) => o.textContent)).toEqual(["Default", "United States", "Canada", "United Kingdom", "Europe", "Asia-Pacific", "Worldwide"]);
    await type(scope, "us");
    await type(byText("label", /^Time budget/)!.querySelector("input"), "2");
    await click(button("Start scan"));
    expect(api.callsTo("POST /api/scans")[0].body).toEqual({ source: "registry", search: "platform engineer", scope: "us", limit: 10, budgetSeconds: 120 });
    await waitFor(() => window.location.search.includes("run=run_1"));
  });

  it("shows the server's refusal when a source is already running", async () => {
    open();
    server({ "POST /api/scans": () => fail("conflict", "a registry scan is already running (run scan-9)") });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Start registry$/));
    await click(byLabel(/^Start registry$/));
    expect(q('[role="alert"]')?.textContent).toContain("a registry scan is already running");
    expect(window.location.search).toBe("");
  });

  it("shows an error with a retry when the sources cannot be loaded", async () => {
    open();
    let calls = 0;
    server({ "GET /api/scan-sources": () => (++calls === 1 ? fail("internal", "boom") : ok({ sources })) });
    await mount(<Discovery />);
    await waitFor(() => q('[role="alert"]'));
    await click(button("Try again"));
    await waitFor(() => q('li[aria-label="registry"]'));
  });
});

describe("Discovery: Monitor", () => {
  const running = scan({ id: "scan-9", source: "registry", status: "running", finishedAt: undefined, runId: "run_1", startedAt: new Date(Date.now() - 65_000).toISOString() });

  function monitor(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}) {
    return server({
      "GET /api/scans": (c) => (c.query.get("status") === "running" ? ok({ scans: [running], page: 1, limit: 20, total: 1 }) : ok({ scans: [running, scan({ id: "scan-8", source: "github" })], page: 1, limit: 10, total: 2 })),
      "GET /api/runs/run_1/events": () => sseResponse([progress(1, "[registry] fetched 10 postings"), progress(2, "[registry] 4 new")]),
      ...extra,
    });
  }

  it("asks before cancelling and cancels the run", async () => {
    open("?tab=monitor");
    const api = monitor({ "POST /api/runs/run_1/cancel": () => ok({ runId: "run_1", kind: "scan", status: "cancelling", createdAt: running.startedAt, updatedAt: running.startedAt }) });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Cancel the registry scan$/));
    await click(byLabel(/^Cancel the registry scan$/));
    expect(api.callsTo("POST /api/runs/run_1/cancel")).toHaveLength(0);
    expect(q("dialog")?.textContent).toContain("Cancel the registry scan");
    await click(button("Stop scan"));
    expect(api.callsTo("POST /api/runs/run_1/cancel")).toHaveLength(1);
  });

  it("keeps the cancel dialog's first focus on Cancel and leaves the scan alone when it is dismissed", async () => {
    open("?tab=monitor");
    const api = monitor();
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Cancel the registry scan$/));
    await click(byLabel(/^Cancel the registry scan$/));
    await click(qa("dialog button").find((b) => b.textContent === "Cancel")!);
    expect(api.callsTo("POST /api/runs/run_1/cancel")).toHaveLength(0);
  });

  it("opens the new items of a finished scan and evaluates one", async () => {
    open("?tab=monitor");
    const row = {
      id: 5, url: "https://jobs.example.com/p/1", originalUrl: "https://jobs.example.com/p/1", aliases: [], identityKey: "k", company: "Fabrikam Example", role: "Platform Engineer", location: "Remote",
      source: "github", detail: "", score: 7, reasons: [], earlyCareer: false, clearanceRequired: false, flags: [], notes: "", status: "pending", attempts: 0, firstSeenAt: running.startedAt, updatedAt: running.startedAt,
    };
    const api = server({
      "GET /api/scans": (c) => ok({ scans: c.query.get("status") === "running" ? [] : [scan({ id: "scan-8", source: "github" })], page: 1, limit: 10, total: 1 }),
      "GET /api/inbox": () => ok({ rows: [row], page: 1, pageSize: 50, total: 1, counts: { pending: 1 } }),
      "POST /api/inbox/promote": () => ok({ queued: [5], rejected: {} }),
    });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^New items from the github scan$/));
    await click(byLabel(/^New items from the github scan$/));
    await waitFor(() => text().includes("Fabrikam Example"));
    expect(api.callsTo("GET /api/inbox").some((c) => c.query.get("runId") === "scan-8")).toBe(true);
    expect(window.location.search).toBe("?tab=monitor&items=scan-8");
    await click(byLabel(/^Evaluate Fabrikam Example, Platform Engineer$/));
    expect(api.callsTo("POST /api/inbox/promote")[0].body).toEqual({ ids: [5] });
  });

  it("evaluates every waiting item of a scan at once and names rows in the inbox's words", async () => {
    open("?tab=monitor&items=scan-8");
    const rows = [inboxRow({ id: 5 }), inboxRow({ id: 6, notes: "eligibility: clearance required" }), inboxRow({ id: 7, company: "Litware Example", status: "queued" })];
    const api = server({
      "GET /api/scans": () => ok({ scans: [], page: 1, limit: 10, total: 0 }),
      "GET /api/inbox": () => ok({ rows, page: 1, pageSize: 50, total: 3, counts: { pending: 2 } }),
      "POST /api/inbox/promote": () => ok({ queued: [5], rejected: { "6": "clearance" } }),
    });
    await mount(<Discovery />);
    await waitFor(() => text().includes("Litware Example"));
    expect(text()).toContain("Evaluating");
    expect(text()).toContain("rank 7");
    expect(text()).toContain("eligibility: clearance required");
    await click(button("Evaluate the 2 waiting"));
    expect(api.callsTo("POST /api/inbox/promote")[0].body).toEqual({ ids: [5, 6] });
    await waitFor(() => text().includes("Started 1 evaluation. Not started: Northwind Example, Staff Engineer (clearance)."));
  });
});
