// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Page API, and Run lifecycle, polling and deadlines.
import { afterEach, describe, expect, test, vi } from "vitest";
import { DEFAULT_TIMING } from "../src/env";
import { Run } from "../src/run";
import { ADA, FAST, PAD, byId, fakeHost, makeEngine, mount, noEdits, runFill, setUrl, summaryOf, tick, until } from "./helpers";

afterEach(() => {
  vi.useRealTimers();
});

const FORM = `<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln">`;

function slowMatchHost() {
  const host = fakeHost();
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => (release = r));
  const match = host.match.bind(host);
  const state = { asked: 0 };
  host.match = async (req) => {
    state.asked++;
    await gate;
    return match(req);
  };
  return { host, release, state };
}

describe("Page API", () => {
  test("Start while running", async () => {
    mount(FORM + `<label for="q">Anything else</label><input id="q">`);
    const { host, release } = slowMatchHost();
    const engine = makeEngine(host);
    const first = engine.start({ applicant: ADA });
    expect(first.started).toBe(true);
    const second = engine.start({ applicant: ADA });
    expect(second.started).toBe(false);
    expect(second.reason).toBeTruthy();
    expect(engine.status().state).toBe("running");
    release();
    await until(() => engine.status().state === "done");
  });

  test("Scoped stop", async () => {
    mount(FORM + `<label for="q">Anything else</label><input id="q">`);
    const { host, release } = slowMatchHost();
    const engine = makeEngine(host);
    const run = engine.start({ applicant: ADA, runId: "run-2" });
    expect(engine.stop("run-1", "cancelled")).toBe(false);
    expect(engine.status().state).toBe("running");
    release();
    await until(() => engine.status().state === "done");
    expect(run.runId).toBe("run-2");
  });

  test("Start during stop unwind is refused and the late completion is not published", async () => {
    mount(FORM + `<label for="q">Anything else</label><input id="q">`);
    const { host, release, state } = slowMatchHost();
    const engine = makeEngine(host);
    const r = engine.start({ applicant: ADA });
    await until(() => state.asked === 1);
    expect(engine.stop(r.runId as string, "cancelled")).toBe(true);
    expect(engine.start({ applicant: ADA }).started).toBe(false);
    release();
    await tick(30);
    const st = engine.status();
    expect(st.state).toBe("stopped");
    expect(host.terminals.map((t) => t.state)).not.toContain("done");
    expect(engine.start({ applicant: ADA }).started).toBe(true);
  });

  test("Answer refused during a fill", async () => {
    mount(FORM + `<label for="q">Anything else</label><input id="q">`);
    const { host, release } = slowMatchHost();
    const engine = makeEngine(host);
    engine.start({ applicant: ADA });
    await expect(engine.answer([{ selector: "#q", outcome: "filled", value: "x" }])).rejects.toThrow(/owns the page/);
    expect(byId("q").value).toBe("");
    release();
    await until(() => engine.status().state === "done");
  });

  test("Answer rejects a stale run id and rejects after a stop", async () => {
    mount(FORM + PAD);
    const engine = makeEngine(fakeHost());
    const r = engine.start({ applicant: ADA, runId: "run-a" });
    await until(() => engine.status().state === "done");
    await expect(engine.answer([], "run-b")).rejects.toThrow(/stale/);
    expect(r.started).toBe(true);
    const { host, release, state } = slowMatchHost();
    mount(FORM + `<label for="q">Anything else</label><input id="q">`);
    const e2 = makeEngine(host);
    const s = e2.start({ applicant: ADA });
    await until(() => state.asked === 1);
    e2.stop(s.runId as string);
    release();
    await tick(30);
    await expect(e2.answer([])).rejects.toThrow(/stopped/);
  });

  test("Partial answer batch: only the resolutions that fit the 19 s window are handled", async () => {
    vi.useFakeTimers();
    mount(Array.from({ length: 12 }, (_, i) => `<label for="t${i}">Question ${i}</label><input id="t${i}">`).join(""));
    const engine = makeEngine(fakeHost(), { settleMs: 500, emptyRetryMs: 600 });
    const resolutions = Array.from({ length: 12 }, (_, i) => ({ selector: `[id="t${i}"]`, outcome: "filled" as const, value: `v${i}` }));
    const promise = engine.answer(resolutions);
    await vi.advanceTimersByTimeAsync(20_000);
    const reply = await promise;
    expect(reply.handled).toBe(7);
    expect(reply.results).toHaveLength(7);
    expect(reply.results.every((r) => r.outcome === "filled")).toBe(true);
    expect(byId("t6").value).toBe("v6");
    expect(byId("t7").value).toBe("");
    // The host sends the other five in a following call.
    const next = engine.answer(resolutions.slice(7));
    await vi.advanceTimersByTimeAsync(20_000);
    expect((await next).handled).toBe(5);
  });

  test("One resolution never outlives the call's window: its write is cut where the window ends, not at the field budget", async () => {
    vi.useFakeTimers();
    mount(`<label id="l">Team</label><button id="t" type="button" role="combobox" aria-haspopup="listbox" aria-labelledby="l">Select one</button>${PAD}`);
    const engine = makeEngine(fakeHost(), { answerWindowMs: 3000, fieldBudgetMs: 60_000, openWaitMs: 20_000 });
    let outcomes: (string | undefined)[][] | null = null;
    void engine.answer([{ selector: '[id="t"]', outcome: "filled", value: "Platform" }]).then((r) => (outcomes = r.results.map((x) => [x.outcome, x.reason])));
    await vi.advanceTimersByTimeAsync(3000);
    expect(outcomes).toEqual([["failed", "timeout"]]);
  });

  test("An answer written after the fill passes the fill's own guard: the applicant's name never goes into a reference's box", async () => {
    mount(`${FORM}<label for="rn">Reference name</label><input id="rn"><label for="nick">Nickname</label><input id="nick">`);
    const engine = makeEngine(fakeHost());
    await runFill(engine, { applicant: ADA });
    const reply = await engine.answer([
      { selector: '[id="rn"]', outcome: "filled", value: "Ada Example" },
      { selector: '[id="nick"]', outcome: "filled", value: "Ada Example" },
    ]);
    expect(reply.results.map((r) => [r.outcome, r.reason])).toEqual([["abstained", "section-policy"], ["filled", undefined]]);
    expect(byId("rn").value).toBe("");
  });

  test("A disposed run holds nothing of the applicant snapshot, in any of its fields", () => {
    mount(FORM);
    const run = new Run({ id: "t", applicant: ADA, posting: {}, acceptLegalDeclarations: false, edits: noEdits(), pageDeadlineMs: 60_000, prepassDeadlineMs: 0, profileApplyBudgetMs: 0 }, { ...DEFAULT_TIMING, ...FAST }, document);
    run.dispose();
    const holds = (v: unknown) => v === ADA || (typeof v === "object" && v !== null && Object.values(v).includes(ADA));
    expect(Object.values(run).filter(holds)).toEqual([]);
  });

  test("Host helpers: probe counts without writing, locate resolves a selector, decideFrame applies the fan-out rules", () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln">`);
    const before = document.body.innerHTML;
    const engine = makeEngine(fakeHost());
    expect(engine.probe()).toEqual({ controls: 2, required: 0 });
    expect(engine.locate('[id="ln"]')).toBe(byId("ln"));
    expect(engine.locate('[id="nope"]')).toBeNull();
    expect(engine.decideFrame({ frameId: "1", url: "https://www.googletagmanager.com/ns.html?id=1" })?.outcome).toBe("skipped-denylist");
    expect(engine.decideFrame({ frameId: "2", url: "https://forms.example.test/x" })).toBeNull();
    expect(engine.decideFrame({ frameId: "2", url: "https://forms.example.test/x?a=1", probe: { controls: 3, required: 0 } })).toMatchObject({
      outcome: "admitted-probe",
      url: "https://forms.example.test/x",
    });
    expect(engine.decideFrame({ frameId: "3", url: "about:blank", failure: { outcome: "failed-inject", reason: "context lost" } })).toMatchObject({
      outcome: "failed-inject",
      reason: "context lost",
    });
    expect(document.body.innerHTML).toBe(before);
  });

  test("A summary carries host, URL, a row per control and the counts", async () => {
    setUrl("https://jobs.example.com/apply?token=secret#x");
    mount(FORM + PAD);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(s.host).toBe("jobs.example.com");
    expect(s.url).toBe("https://jobs.example.com/apply");
    expect(s.rows.map((r) => r.label)).toEqual(["First name", "Last name", "Referral code"]);
    expect(s.counts).toMatchObject({ filled: 2, prefilled: 1, abstained: 0, failed: 0 });
  });

  test("Password inputs are never read and a file input reports only its name", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="up">Resume</label><input id="up" type="file"><input type="password" id="pw" value="hunter2" aria-label="Password">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(JSON.stringify(s)).not.toContain("hunter2");
    const file = s.rows.find((r) => r.kind === "file");
    expect(file?.fileName).toBe("ada-example.pdf");
    expect(file?.value).toBeUndefined();
  });
});
