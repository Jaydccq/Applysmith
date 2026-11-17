import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mountAutofill } from "../src/panel/autofill";
import type { FillView } from "../src/shared/messages";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({
  send: (r: unknown) => send(r),
  failureOf: (e: { failure?: unknown }) => e.failure ?? { code: "internal", message: "Something went wrong." },
}));

let el: HTMLElement;
const counts = { filled: 8, prefilled: 1, review: 1, abstained: 1, failed: 0, skipped: 1, requiredUnfilled: 1 };
const done: FillView = {
  phase: "done",
  runId: "run_x",
  frames: [
    { frameId: "0", url: "https://example.com/apply", outcome: "admitted-probe", controls: 6 },
    { frameId: "1", url: "https://job-boards.greenhouse.io/embed/job_app", outcome: "admitted-registry" },
  ],
  counts,
  rows: [
    { label: "Visa sponsorship", outcome: "abstained", reason: "ask-me", required: true, review: false },
    { label: "Notes", outcome: "filled", required: false, review: true },
  ],
  drafts: 2,
  stall: "The page slowed down while the form was filled. Check the fields below.",
};

beforeEach(() => {
  vi.useFakeTimers();
  send.mockReset();
  send.mockResolvedValue({ phase: "idle", frames: [] });
  document.body.innerHTML = '<div id="t"></div>';
  el = document.getElementById("t")!;
});
afterEach(() => vi.useRealTimers());

const buttons = () => [...el.querySelectorAll("button")].map((b) => b.textContent);
const click = (label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === label)!.click();

describe("Autofill tab", () => {
  it("starts a fill of this page, polls while it runs and shows the result", async () => {
    const tab = mountAutofill(el);
    tab.setConnection("connected");
    await vi.advanceTimersByTimeAsync(0);
    send.mockClear();
    send.mockResolvedValueOnce({ phase: "running", runId: "run_x", frames: [], elapsedMs: 1000 });
    click("Start");
    await vi.advanceTimersByTimeAsync(0);
    expect(send.mock.calls[0]![0]).toMatchObject({ kind: "fill.start", reading: { url: expect.stringContaining("http") } });
    expect(buttons()).toEqual(expect.arrayContaining(["Pause", "Stop"]));
    send.mockResolvedValueOnce(done);
    await vi.advanceTimersByTimeAsync(800);
    expect(send.mock.calls[1]![0]).toEqual({ kind: "fill.status" });
    expect(el.textContent).toContain("1 required field is still empty. Fill it before you submit.");
    expect(el.textContent).toContain("8 filled, 1 already filled, 1 to review, 1 skipped.");
    expect(el.textContent).toContain("Visa sponsorship: your answer bank says to ask you");
    expect(el.textContent).toContain("2 free-text questions were left for you: nothing was written.");
    expect(el.textContent).toContain("slowed down");
    // The poll stops once the fill ended.
    send.mockClear();
    await vi.advanceTimersByTimeAsync(3000);
    expect(send).not.toHaveBeenCalled();
  });

  it("pauses, resumes and stops", async () => {
    const tab = mountAutofill(el);
    tab.setConnection("connected");
    await vi.advanceTimersByTimeAsync(0);
    send.mockClear();
    send.mockResolvedValueOnce({ phase: "running", frames: [] });
    click("Start");
    await vi.advanceTimersByTimeAsync(0);
    send.mockResolvedValueOnce({ phase: "paused", frames: [] });
    click("Pause");
    await vi.advanceTimersByTimeAsync(0);
    expect(buttons()).toContain("Resume");
    send.mockResolvedValueOnce({ phase: "running", frames: [] });
    click("Resume");
    await vi.advanceTimersByTimeAsync(0);
    send.mockResolvedValueOnce({ phase: "stopped", frames: [], counts });
    click("Stop");
    await vi.advanceTimersByTimeAsync(0);
    expect(send.mock.calls.map((c) => (c[0] as { kind: string }).kind)).toEqual(["fill.start", "fill.pause", "fill.resume", "fill.stop"]);
    expect(el.textContent).toContain("Stopped.");
  });

  it("lists every question the fill could not answer under Needs you, marking the ones the page requires and counting only those", async () => {
    const view: FillView = {
      ...done,
      counts: { ...counts, requiredUnfilled: 1 },
      rows: [
        { label: "Why do you want to work here?", outcome: "abstained", reason: "no-match", required: false, review: false },
        { label: "Visa sponsorship", outcome: "abstained", reason: "ask-me", required: true, review: false },
        { label: "First name", outcome: "filled", required: true, review: false },
      ],
    };
    send.mockResolvedValue(view);
    const tab = mountAutofill(el);
    tab.setConnection("connected");
    await vi.advanceTimersByTimeAsync(0);
    const items = [...el.querySelectorAll("ul.list")[0]!.querySelectorAll("li")].map((li) => li.textContent);
    expect(items).toEqual(["Visa sponsorship: your answer bank says to ask you (required)", "Why do you want to work here?: nothing in your answer bank answers it"]);
    expect(el.textContent).toContain("1 required field is still empty");
  });

  it("starts a fill against the token its document carries", async () => {
    (window as unknown as Record<string, string>).__applysmithDoc = "token-1";
    const tab = mountAutofill(el);
    tab.setConnection("connected");
    await vi.advanceTimersByTimeAsync(0);
    send.mockClear();
    send.mockResolvedValue({ phase: "running", frames: [] });
    click("Start");
    await vi.advanceTimersByTimeAsync(0);
    expect(send.mock.calls[0]![0]).toMatchObject({ kind: "fill.start", doc: "token-1" });
    tab.dispose();
    delete (window as unknown as Record<string, string>).__applysmithDoc;
  });

  it("starts the step walk with the Submit option off, and does not remember the option", async () => {
    const tab = mountAutofill(el);
    tab.setConnection("connected");
    await vi.advanceTimersByTimeAsync(0);
    const box = el.querySelector<HTMLInputElement>("#walk-submit")!;
    expect(box.checked).toBe(false);
    tab.dispose();
  });

  it("confirms the walker's own Submit press by the page's thank-you words, under the same entry and claim", async () => {
    document.body.innerHTML = '<div data-applysmith><div id="t"></div></div><main><form><input id="n"><button type="submit">Submit application</button></form></main>';
    el = document.getElementById("t")!;
    send.mockImplementation(async (req: { kind: string }) =>
      req.kind === "press.claim" ? { granted: true, claimId: "c1" } : req.kind.startsWith("fill.") ? { phase: "done", frames: [] } : undefined,
    );
    const tab = mountAutofill(el);
    tab.setConnection("connected");
    await vi.advanceTimersByTimeAsync(0);
    el.querySelector<HTMLInputElement>("#walk-submit")!.checked = true;
    el.querySelector<HTMLInputElement>("#walk-submit")!.dispatchEvent(new Event("change"));
    click("Walk the steps");
    await vi.advanceTimersByTimeAsync(2_000);
    const reports = () => send.mock.calls.map((c) => c[0] as { kind: string; claimId?: string; entry?: { entryId: string; confirmed: boolean } }).filter((r) => r.kind === "press.report");
    expect(reports().map((r) => r.entry?.confirmed)).toEqual([false]);
    document.querySelector("main")!.insertAdjacentText("beforeend", "Thank you for applying!");
    await vi.advanceTimersByTimeAsync(600);
    expect(reports().map((r) => [r.claimId, r.entry?.entryId === reports()[0]!.entry?.entryId, r.entry?.confirmed])).toEqual([
      ["c1", true, false],
      ["c1", true, true],
    ]);
    tab.dispose();
  });
});
