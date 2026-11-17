import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mountAnalyze } from "../src/panel/analyze";
import type { Capture } from "../src/shared/messages";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({
  send: (r: unknown) => send(r),
  failureOf: (e: { failure?: unknown }) => e.failure ?? { code: "internal", message: "Something went wrong." },
}));

const POSTING = `Backend Engineer. Responsibilities: build services. Qualifications: Go. Salary and benefits. Full-time, remote. Apply now. ${"Details. ".repeat(50)}`;
const report = { number: 12, company: "Example Corp", role: "Backend Engineer", score: 4.2, recommendation: "apply" };
const STATE = { connection: "connected", address: "http://127.0.0.1:47319" };

let el: HTMLElement;
const text = () => el.textContent ?? "";
const click = (label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === label)!.click();
const labels = () => [...el.querySelectorAll("button")].map((b) => b.textContent);
const kinds = () => send.mock.calls.map((c) => (c[0] as { kind: string }).kind);

function setPage(body: string, url = "https://example.com/careers/backend") {
  (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL(url);
  document.title = "Backend Engineer - Example Corp";
  document.body.innerHTML = `<div id="t"></div><main>${body}</main>`;
  el = document.getElementById("t")!;
}

/** Routes the tab's messages; `lookup` and `status` answer per test. */
function wire(over: { lookup?: unknown; status?: unknown[] } = {}) {
  const statuses = [...(over.status ?? [])];
  send.mockImplementation(async (req: { kind: string }) => {
    switch (req.kind) {
      case "state": return STATE;
      case "analyze.lookup": return over.lookup ?? { found: false };
      case "analyze.start": return { runId: "run_x" };
      case "analyze.status": return statuses.length > 1 ? statuses.shift() : statuses[0];
      case "analyze.track": return { found: true };
    }
    throw new Error(`unexpected ${req.kind}`);
  });
}

async function open(): Promise<ReturnType<typeof mountAnalyze>> {
  const tab = mountAnalyze(el, { pollMs: 1000 });
  tab.setConnection("connected");
  await vi.advanceTimersByTimeAsync(10);
  return tab;
}

beforeEach(() => {
  vi.useFakeTimers();
  send.mockReset();
  setPage(POSTING);
});
afterEach(() => vi.useRealTimers());

describe("Analyze tab", () => {
  it("shows the score and report of a posting that already has one and starts no evaluation", async () => {
    wire({ lookup: { found: true, report, candidacyId: 7, status: "evaluated" } });
    await open();
    expect(el.dataset.state).toBe("scored");
    expect(text()).toContain("Score 4.2");
    expect(text()).toContain("recommendation: apply");
    expect(el.querySelector("a")!.textContent).toBe("Open report 12");
    // The dashboard opens one report from its id parameter (Reports.tsx, the Tracker's and the palette's links).
    expect(new URL(el.querySelector("a")!.href).search).toBe("?id=12");
    expect(kinds()).not.toContain("analyze.start");
  });

  it("evaluates a fresh posting, advances the stepper with each phase and ends on the score", async () => {
    wire({ status: [{ runId: "run_x", phase: "reading", done: false }, { runId: "run_x", phase: "evaluating", done: false }, { runId: "run_x", phase: "writing", done: true, outcome: "reported", report }] });
    await open();
    expect(el.dataset.state).toBe("captured");
    click("Evaluate");
    await vi.advanceTimersByTimeAsync(10);
    expect(el.dataset.state).toBe("progress");
    const step = () => el.querySelector("[aria-current=step]")?.textContent;
    expect(step()).toBe("Reading the posting");
    await vi.advanceTimersByTimeAsync(1000);
    expect(step()).toBe("Evaluating");
    await vi.advanceTimersByTimeAsync(1000);
    expect(el.dataset.state).toBe("scored");
    expect(text()).toContain("Score 4.2");
  });

  it("asks before evaluating a posting that looks closed, and evaluates only after the person agrees", async () => {
    setPage(`This job is no longer accepting applications. ${POSTING}`);
    wire({ status: [{ runId: "run_x", phase: "reading", done: false }] });
    await open();
    expect(el.dataset.state).toBe("awaiting-expiry");
    expect(text()).toMatch(/looks closed/);
    expect(kinds()).not.toContain("analyze.start");
    click("Evaluate anyway");
    await vi.advanceTimersByTimeAsync(10);
    expect(kinds()).toContain("analyze.start");
  });

  it("lets a page that is not a posting be evaluated only after a confirmation", async () => {
    setPage("Cats sleep a lot and enjoy sunny windows. ".repeat(20), "https://example.com/blog/cats");
    wire({ status: [{ runId: "run_x", phase: "reading", done: false }] });
    await open();
    expect(text()).toMatch(/something other than a job posting/);
    click("Evaluate anyway");
    expect(text()).toMatch(/Evaluate it anyway\?/);
    expect(kinds()).not.toContain("analyze.start");
    click("Evaluate anyway");
    await vi.advanceTimersByTimeAsync(10);
    expect(kinds()).toContain("analyze.start");
  });

  it("adds a scored posting to the tracker", async () => {
    wire({ lookup: { found: false }, status: [{ runId: "run_x", phase: "writing", done: true, outcome: "reported", report }] });
    await open();
    click("Evaluate");
    await vi.advanceTimersByTimeAsync(10);
    click("Add to tracker");
    await vi.advanceTimersByTimeAsync(10);
    expect(el.dataset.state).toBe("actioned");
    const track = send.mock.calls.map((c) => c[0] as { kind: string; capture?: Capture }).find((r) => r.kind === "analyze.track")!;
    expect(track.capture).toMatchObject({ company: "Example Corp", role: "Backend Engineer" });
  });

  it("sends one tracker request however often Add to tracker is pressed", async () => {
    wire({ lookup: { found: false }, status: [{ runId: "run_x", phase: "writing", done: true, outcome: "reported", report }] });
    await open();
    click("Evaluate");
    await vi.advanceTimersByTimeAsync(10);
    click("Add to tracker");
    click("Add to tracker");
    await vi.advanceTimersByTimeAsync(10);
    expect(kinds().filter((k) => k === "analyze.track")).toHaveLength(1);
    expect(el.dataset.state).toBe("actioned");
  });

  it("re-evaluates only after a confirmation, forcing a new run", async () => {
    wire({ lookup: { found: true, report, candidacyId: 7 }, status: [{ runId: "run_x", phase: "reading", done: false }] });
    await open();
    click("Re-evaluate");
    expect(labels()).toContain("Cancel");
    expect(kinds()).not.toContain("analyze.start");
    click("Re-evaluate");
    await vi.advanceTimersByTimeAsync(10);
    expect(send.mock.calls.map((c) => c[0] as { kind: string; force?: boolean }).find((r) => r.kind === "analyze.start")!.force).toBe(true);
  });

  it("explains a failed evaluation", async () => {
    wire({ status: [{ runId: "run_x", phase: "reading", done: true, outcome: "failed", reason: "posting-unreadable", explanation: "The page could not be read." }] });
    await open();
    click("Evaluate");
    await vi.advanceTimersByTimeAsync(10);
    expect(el.dataset.state).toBe("error");
    expect(text()).toContain("The page could not be read.");
  });
});
