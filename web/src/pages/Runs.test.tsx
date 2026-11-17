import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { byLabel, byText, click, mount, press, q, qa, text, waitFor } from "../testing/dom";
import Runs from "./Runs";
import { resetPanel } from "./browser/Lanes";
import { plainDetail } from "./runs/format";
import { expectAccessible } from "../testing/axe";

const now = Date.now();
const ago = (ms: number) => new Date(now - ms).toISOString();
const HOUR = 3600 * 1000;

function entry(over: Record<string, unknown> = {}) {
  return {
    id: "run_1", kind: "apply", origin: "user", title: "Apply to Northwind Example", state: "working", link: "/runs/run_1", revision: 1,
    startedAt: ago(HOUR), updatedAt: ago(60000), stateAt: ago(HOUR), stage: "fill", ...over,
  };
}

const running = entry();
const waiting = entry({ id: "run_2", title: "Apply to Contoso Example", state: "needs-you", reason: "answer", stage: "check", detail: "2 required answers are missing" });
const blocked = entry({ id: "run_3", title: "Apply to Fabrikam Example", state: "blocked", reason: "credential-refused", stage: "account", seenAt: ago(1000) });
const done = entry({ id: "run_4", kind: "scan", title: "Scan builtin", state: "done", outcome: "ok", finishedAt: ago(2 * HOUR), stage: "" });
const failed = entry({ id: "run_5", kind: "evaluate", title: "Evaluate a posting on jobs.example.com", state: "done", outcome: "failed", finishedAt: ago(3 * HOUR) });
const old = entry({ id: "run_6", title: "Apply to Old Example", state: "done", outcome: "ok", finishedAt: ago(9 * 24 * HOUR) });

const inbox = (items: unknown[]) => ({ items, unseen: items.length });
const item = (e: unknown, waitingSeconds = 600, action = "open-run") => ({ entry: e, waitingSeconds, action });

function server(entries: unknown[] = [running, waiting, blocked, done, failed, old], extra: Record<string, () => ReturnType<typeof ok>> = {}) {
  return fakeApi({
    "GET /api/runs": () => ok({ entries }),
    "GET /api/needs-you": () => ok(inbox([item(waiting, 7200), item(blocked, 600)])),
    ...extra,
  });
}

describe("Runs board", () => {
  it("sorts runs into Running and Finished, leaves the waiting ones to the inbox, and leaves out what finished over 7 days ago", async () => {
    server();
    await mount(<Runs />);
    await waitFor(() => text().includes("Scan builtin"));
    const column = (id: string) => qa(`section[aria-labelledby="col-${id}"] li a`).map((a) => a.textContent);
    expect(column("working")).toEqual(["Apply to Northwind Example"]);
    // Both waiting runs are in the Needs you inbox, so no column repeats them.
    expect(q("#col-waiting")).toBeNull();
    expect(qa("a").filter((a) => a.textContent === "Apply to Contoso Example")).toHaveLength(1);
    expect(column("done")).toEqual(["Scan builtin", "Evaluate a posting on jobs.example.com"]);
    expect(text()).not.toContain("Old Example");
    const card = q("section[aria-labelledby='col-working'] li")!;
    expect(card.textContent).toContain("Apply, filling the form");
    expect(card.textContent).toContain("Started");
    expect(card.textContent).not.toContain("at fill");
    // "Scan builtin" already says it is a scan; its card adds no "Scan" line.
    const scan = q("section[aria-labelledby='col-done'] li")!;
    expect(Array.from(scan.querySelectorAll("p"), (p) => p.textContent)).not.toContain("Scan");
    await expectAccessible();
  });

  it("acknowledges every finished run not yet acknowledged and says how many went through", async () => {
    const acked = entry({ id: "run_k", kind: "scan", title: "Scan acked", state: "done", outcome: "ok", finishedAt: ago(HOUR), acknowledgedAt: ago(HOUR) });
    const api = server([done, failed, acked], {
      "POST /api/runs/run_4/ack": () => ok({ ...done, acknowledgedAt: ago(0) }),
      "POST /api/runs/run_5/ack": () => fail("internal", "no"),
    });
    await mount(<Runs />);
    await click(await waitFor(() => qa<HTMLButtonElement>("button").find((b) => b.textContent === "Acknowledge all")));
    await waitFor(() => text().includes("Acknowledged 1; 1 failed."));
    expect(api.calls.filter((c) => c.method === "POST").map((c) => c.path).sort()).toEqual(["/api/runs/run_4/ack", "/api/runs/run_5/ack"]);
  });

  it("shows a failure to read the board with a retry", async () => {
    let n = 0;
    fakeApi({ "GET /api/runs": () => (n++ === 0 ? fail("internal", "The board could not be read.") : ok({ entries: [running] })), "GET /api/needs-you": () => ok(inbox([])) });
    await mount(<Runs />);
    await waitFor(() => text().includes("The board could not be read."));
    await click(byText("button", "Try again"));
    await waitFor(() => text().includes("Apply to Northwind Example"));
  });
});

describe("Runs inbox", () => {
  it("brings a kept tab forward through the show-tab route or into the browser panel, and disables the action when none is kept", async () => {
    const api = fakeApi({
      "GET /api/runs": () => ok({ entries: [waiting] }),
      "GET /api/needs-you": () => ok(inbox([item(entry({ ...waiting, canFocusTab: true, focusTab: "tab-2" }), 60, "focus-tab"), item(entry({ id: "run_9", title: "Apply to Tailspin Example", state: "blocked", reason: "bot-check" }), 60, "focus-tab")])),
      "POST /api/apply/runs/run_2/show-tab": () => ok({ runId: "run_2" }),
    });
    await mount(<Runs />);
    const [kept, none] = await waitFor(() => {
      const b = qa<HTMLButtonElement>("button").filter((x) => x.textContent === "Show tab");
      return b.length === 2 && b;
    });
    expect(none.disabled).toBe(true);
    expect(none.title).toBe("No tab is kept for this run.");
    await click(kept);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_2/show-tab").length === 1);
    // Only the run that kept a tab offers the panel, and it opens on that tab.
    resetPanel();
    const panel = qa<HTMLButtonElement>("button").filter((x) => x.textContent === "Show it in the browser panel");
    expect(panel.map((b) => b.closest("li")?.textContent?.includes("Tailspin"))).toEqual([false]);
    await click(panel[0]);
    expect(JSON.parse(window.localStorage.getItem("applysmith-browser-panel")!).open).toBe(true);
  });

  it("offers Done on a run the assistant started and posts it, and offers none otherwise", async () => {
    const resumable = entry({ id: "run_7", origin: "agent", title: "Apply to Litware Example", state: "needs-you", reason: "sign-in", canDone: true });
    const api = fakeApi({
      "GET /api/runs": () => ok({ entries: [resumable] }),
      "GET /api/needs-you": () => ok(inbox([item(resumable, 90), item(waiting, 90)])),
      "POST /api/runs/run_7/done": () => ok({ ...resumable, state: "working", reason: undefined, canDone: undefined }),
    });
    await mount(<Runs />);
    const done = await waitFor(() => byLabel(/^Done Apply to Litware/));
    expect(qa("button").filter((b) => b.textContent === "Done")).toHaveLength(1);
    await click(done);
    await waitFor(() => api.callsTo("POST /api/runs/run_7/done").length === 1);
    await waitFor(() => api.callsTo("GET /api/needs-you").length >= 2);
  });

  it("shows the server's words when Done is refused", async () => {
    const resumable = entry({ id: "run_7", origin: "agent", title: "Apply to Litware Example", state: "needs-you", reason: "sign-in", canDone: true });
    fakeApi({
      "GET /api/runs": () => ok({ entries: [resumable] }),
      "GET /api/needs-you": () => ok(inbox([item(resumable, 90)])),
      "POST /api/runs/run_7/done": () => fail("conflict", "the run is no longer waiting for you"),
    });
    await mount(<Runs />);
    await click(await waitFor(() => byLabel(/^Done Apply to Litware/)));
    await waitFor(() => text().includes("the run is no longer waiting for you"));
  });
});

describe("Run detail", () => {
  const view = (e: Record<string, unknown>, over: Record<string, unknown> = {}) => ({
    runId: e.id, kind: e.kind, status: "running", createdAt: e.startedAt, updatedAt: e.updatedAt, entry: e, ...over,
  });

  it("asks before it cancels a running run, naming it, and does nothing on Escape", async () => {
    window.history.pushState(null, "", "/runs/run_1");
    const api = server(undefined, {
      "GET /api/runs/run_1": () => ok(view(running)),
      "POST /api/runs/run_1/cancel": () => ({ data: view(running, { status: "cancelling" }), status: 202 }),
    });
    await mount(<Runs />);
    expect((await waitFor(() => q("dialog [role=status].sr-only")))?.textContent).toBe("Running, filling the form");
    await click(await waitFor(() => byText("button", "Cancel run")));
    const confirm = await waitFor(() => qa("dialog").find((d) => d.querySelector("h2")?.textContent === "Cancel Apply to Northwind Example"));
    expect(confirm.textContent).toContain("A Submit that was already pressed cannot be taken back");
    expect(document.activeElement?.textContent).toBe("Cancel");
    await press("Escape", {}, confirm);
    await waitFor(() => qa("dialog").length === 1);
    expect(api.callsTo("POST /api/runs/run_1/cancel")).toHaveLength(0);
    await click(byText("button", "Cancel run"));
    await click(await waitFor(() => byText("button", "Stop run")));
    await waitFor(() => api.callsTo("POST /api/runs/run_1/cancel").length === 1);
  });

  it("shows the server's words when a run cannot be cancelled", async () => {
    window.history.pushState(null, "", "/runs/run_1");
    server(undefined, { "GET /api/runs/run_1": () => ok(view(running)), "POST /api/runs/run_1/cancel": () => fail("conflict", "the run has already ended") });
    await mount(<Runs />);
    await click(await waitFor(() => byText("button", "Cancel run")));
    await click(await waitFor(() => byText("button", "Stop run")));
    await waitFor(() => text().includes("the run has already ended"));
  });

  it("answers a quick screen's review: the report, Evaluate in full with the posting and its inbox row, or Skip", async () => {
    const review = entry({ id: "run_9", kind: "evaluate", title: "Evaluate Data Engineer at Tailspin Example", state: "needs-you", reason: "review", stage: "", seenAt: ago(1000),
      detail: "Open report 33: evaluate in depth or skip. The quick screen could not tell whether the posting is genuine." });
    const evaluation = { runId: "run_9", status: "review_required", review: true, outcome: "screened", reportNumber: 33, postingUrl: "https://jobs.example.com/de", inboxRowId: 85,
      reviewReason: "The quick screen could not tell whether the posting is genuine." };
    window.history.pushState(null, "", "/runs/run_9");
    const api = server(undefined, {
      "GET /api/runs/run_9": () => ok(view(review)),
      "GET /api/evaluations/run_9": () => ok(evaluation),
      "POST /api/evaluations": () => ({ data: { runId: "run_10" }, status: 202 }),
      "POST /api/inbox/85/dismiss": () => fail("conflict", "the row is dismissed already"),
      "POST /api/runs/run_9/ack": () => ok({ ...review, acknowledgedAt: ago(0) }),
    });
    await mount(<Runs />);
    await waitFor(() => q("dialog #run-review")?.textContent === "Evaluate in depth or skip");
    const dialog = q("dialog")!;
    expect(q("dialog #run-why")?.textContent).toBe("Open report 33: evaluate in depth or skip. The quick screen could not tell whether the posting is genuine.");
    expect(dialog.textContent).toContain("Open the report, then evaluate the posting in full or skip it.");
    expect(dialog.textContent).not.toContain("kept tab");
    expect(qa<HTMLAnchorElement>("dialog a").find((a) => a.textContent === "Open report 33")?.getAttribute("href")).toBe("/reports?id=33");
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Skip")!);
    await waitFor(() => api.callsTo("POST /api/runs/run_9/ack").length === 1);
    expect(api.callsTo("POST /api/inbox/85/dismiss")).toHaveLength(1);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Evaluate in full")!);
    await waitFor(() => api.callsTo("POST /api/evaluations").length === 1);
    const [call] = api.callsTo("POST /api/evaluations");
    expect(call.body).toEqual({ url: "https://jobs.example.com/de", inboxRowId: 85, mode: "deep" });
    expect(call.headers.get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => qa<HTMLAnchorElement>("dialog a").some((a) => a.textContent === "Follow it" && a.getAttribute("href") === "/runs/run_10"));
  });

  it("starts a failed evaluation again and follows the new run, and leads a finished one to its report", async () => {
    window.history.pushState(null, "", "/runs/run_5?kind=evaluate");
    const queued = { ...failed, batchId: "scan:run_4" };
    const api = server(undefined, {
      "GET /api/runs/run_5": () => ok(view(queued, { status: "failed", failureReason: "rate-limited" })),
      "GET /api/evaluations/run_5": () => ok({ runId: "run_5", status: "failed", outcome: "failed", review: false, postingUrl: "https://jobs.example.com/p/5" }),
      "POST /api/evaluations/run_5/retry": () => ({ data: { runId: "run_11" }, status: 202 }),
    });
    await mount(<Runs />);
    // A scan's evaluation names the scan's run as its batch (notify.ScanBatch).
    expect((await waitFor(() => qa<HTMLAnchorElement>("dialog dd a")[0])).getAttribute("href")).toBe("/runs/run_4?kind=evaluate");
    await click(await waitFor(() => byText("button", "Evaluate again")));
    await waitFor(() => window.location.pathname === "/runs/run_11");
    expect(api.callsTo("POST /api/evaluations/run_5/retry")[0].headers.get("Idempotency-Key")).toBeTruthy();
    expect(window.location.search).toBe("?kind=evaluate");

    const succeeded = entry({ id: "run_12", kind: "evaluate", title: "Evaluate Staff Engineer at Northwind Example", state: "done", outcome: "ok", finishedAt: ago(HOUR) });
    window.history.pushState(null, "", "/runs/run_12");
    server([succeeded], {
      "GET /api/runs/run_12": () => ok(view(succeeded, { status: "succeeded" })),
      "GET /api/evaluations/run_12": () => ok({ runId: "run_12", status: "succeeded", outcome: "reported", review: false, reportNumber: 12, score: 4.1, recommendation: "apply" }),
    });
    await mount(<Runs />);
    expect((await waitFor(() => qa<HTMLAnchorElement>("dialog a").find((a) => a.textContent === "Open report 12")))?.getAttribute("href")).toBe("/reports?id=12");
    expect(byText("button", "Evaluate again")).toBeNull();
  });
});

// Detail strings as the application chain writes them (internal/apply).
const TEAMSNAP =
  'check: page 1 "How did you hear about TeamSnap and this opportunity?": required and not answered; page 1 "Were you referred by a TeamSnap Employee?": required and not answered; page 1 "If yes, who referred you? Please enter their full name": required and not answered';

describe("Run detail in words", () => {
  it("names the questions a check stopped on, and keeps the machine text", () => {
    const p = plainDetail(TEAMSNAP, "answer")!;
    expect(p.line).toBe("Answer 3 questions: How did you hear about TeamSnap and this…, Were you referred by a TeamSnap… and 1 more");
    expect(p.groups).toEqual([
      { heading: "3 questions need your answer", items: ["How did you hear about TeamSnap and this opportunity?", "Were you referred by a TeamSnap Employee?", "If yes, who referred you? Please enter their full name"] },
    ]);
    expect(p.raw).toBe(TEAMSNAP);
  });

  it("falls back to the board reason, and to nothing", () => {
    expect(plainDetail("", "credential-refused")?.line).toBe("The site refused the saved password.");
    expect(plainDetail(undefined, "other")).toBeNull();
  });
});

describe("Runs inbox repeats", () => {
  const repeat = (n: number, hours: number) =>
    entry({ id: `ts_${n}`, title: "Apply to teamsnap", state: "needs-you", reason: "answer", stage: "check", detail: TEAMSNAP, startedAt: ago(hours * HOUR + 41000), stateAt: ago(hours * HOUR), seenAt: ago(1000) });

  it("folds runs of one posting stopped for one reason into a row with a count, the newest age and the earlier runs on request", async () => {
    const runs = [repeat(1, 18), repeat(2, 9), repeat(3, 4)];
    const api = fakeApi({
      "GET /api/runs": () => ok({ entries: runs }),
      "GET /api/needs-you": () => ok(inbox(runs.map((e, i) => item(e, [18, 9, 4][i] * 3600)))),
      "POST /api/runs/ts_1/ack": () => ok(runs[0]),
      "POST /api/runs/ts_2/ack": () => ok(runs[1]),
      "POST /api/runs/ts_3/ack": () => ok(runs[2]),
    });
    await mount(<Runs />);
    const rows = await waitFor(() => {
      const r = qa("section[aria-labelledby='runs-inbox'] .item-list > li");
      return r.length > 0 && r;
    });
    expect(rows).toHaveLength(1);
    const row = rows[0];
    expect(row.querySelector("a")?.getAttribute("href")).toBe("/runs/ts_3");
    expect(row.textContent).toContain("3 runs");
    expect(row.textContent).toContain("Answer 3 questions: How did you hear about TeamSnap");
    expect(row.textContent).toContain("Waiting 4 h");
    expect(row.textContent).not.toContain("required and not answered");
    // The board does not repeat what the inbox lists.
    expect(q("#col-waiting")).toBeNull();
    await click(byText("button", "Show 2 earlier runs"));
    expect(qa(".inbox-repeats a").map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Waiting 9 h", "/runs/ts_2"],
      ["Waiting 18 h", "/runs/ts_1"],
    ]);
    await click(byLabel(/^Acknowledge 3 runs of Apply to teamsnap/));
    await waitFor(() => api.calls.filter((c) => c.method === "POST").length === 3);
    expect(api.calls.filter((c) => c.method === "POST").map((c) => c.path).sort()).toEqual(["/api/runs/ts_1/ack", "/api/runs/ts_2/ack", "/api/runs/ts_3/ack"]);
  });
});
