// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Run lifecycle, polling and deadlines (engine side).
import { afterEach, describe, expect, test, vi } from "vitest";
import { ADA, PAD, fakeHost, makeEngine, mount, runFill, summaryOf, tick, until } from "./helpers";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const FORM = `<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em">`;

describe("Run lifecycle, polling and deadlines", () => {
  test("Normal completion: running, then done with a summary, and the terminal binding was notified once", async () => {
    mount(FORM + PAD);
    const host = fakeHost();
    const engine = makeEngine(host, { settleMs: 5 });
    const r = engine.start({ applicant: ADA });
    expect(engine.status()).toMatchObject({ state: "running", runId: r.runId });
    await until(() => engine.status().state === "done");
    await tick(5);
    expect(summaryOf(engine.status()).counts.filled).toBe(2);
    expect(host.terminals).toHaveLength(1);
    expect(host.terminals[0]).toMatchObject({ runId: r.runId, state: "done" });
  });

  test("Deadline: at the page deadline the state becomes stopped with reason deadline and the summary lists what was written", async () => {
    vi.useFakeTimers();
    mount(FORM + `<label for="ln">Last name</label><input id="ln">` + PAD);
    const host = fakeHost();
    const engine = makeEngine(host, { settleMs: 400 });
    engine.start({ applicant: ADA, pageDeadlineMs: 500 });
    await vi.advanceTimersByTimeAsync(2000);
    const st = engine.status();
    expect(st).toMatchObject({ state: "stopped", reason: "deadline" });
    if (st.state !== "stopped") throw new Error("unreachable");
    expect(st.summary.rows.some((r) => r.outcome === "filled")).toBe(true);
    expect(st.summary.partial).toBe(true);
    expect(host.terminals.at(-1)).toMatchObject({ state: "stopped", reason: "deadline" });
  });

  test("Time spent paused does not count toward the deadline", async () => {
    vi.useFakeTimers();
    mount(FORM + PAD);
    const engine = makeEngine(fakeHost(), { settleMs: 100 });
    engine.start({ applicant: ADA, pageDeadlineMs: 1000 });
    await vi.advanceTimersByTimeAsync(50);
    engine.pause();
    expect(engine.status().state).toBe("paused");
    await vi.advanceTimersByTimeAsync(10_000);
    expect(engine.status().state).toBe("paused");
    engine.resume();
    await vi.advanceTimersByTimeAsync(2000);
    expect(engine.status().state).toBe("done");
  });

  test("Time spent paused counts toward neither the question pass's budget nor one control's write budget", async () => {
    vi.useFakeTimers();
    mount(`<label for="y">Years of experience with Go</label><input id="y"><label for="z">Notice period in weeks</label><input id="z">${PAD}`);
    const host = fakeHost({ "years of experience with go": { outcome: "filled", value: "6" }, "notice period in weeks": { outcome: "filled", value: "4" } });
    const engine = makeEngine(host);
    const pauseFor = (ms: number) => {
      engine.pause();
      setTimeout(() => engine.resume(), ms);
    };
    // The person pauses as the matcher answers, for longer than the pass budget; and again as the first box is typed, for longer than a write's budget.
    const match = host.match.bind(host);
    host.match = async (req) => {
      pauseFor(120_000);
      return match(req);
    };
    document.getElementById("y")?.addEventListener("input", () => pauseFor(120_000), { once: true });
    engine.start({ applicant: ADA });
    await vi.advanceTimersByTimeAsync(400_000);
    const s = summaryOf(engine.status());
    expect(s.rows.filter((r) => r.lane === "bank").map((r) => [r.outcome, r.reason, r.value])).toEqual([
      ["filled", undefined, "6"],
      ["filled", undefined, "4"],
    ]);
  });

  test("Binding down: the terminal binding throws on every call, the fill still reaches done", async () => {
    mount(FORM + PAD);
    const host = fakeHost();
    host.failTerminal = true;
    const engine = makeEngine(host);
    const status = await runFill(engine);
    await tick(5);
    expect(status.state).toBe("done");
    expect(engine.status().state).toBe("done");
    expect(host.terminals).toHaveLength(1);
  });

  test("Applicant data not retained: the fictional email appears only inside the controls that were filled", async () => {
    mount(FORM + `<label for="why">Why us?</label><textarea id="why"></textarea>`);
    const host = fakeHost();
    const engine = makeEngine(host);
    const spy = vi.spyOn(console, "log");
    await runFill(engine);
    expect(spy).not.toHaveBeenCalled();
    const html = document.body.innerHTML;
    // The value lives in the control's property, never in an attribute or text node.
    expect(html).not.toContain("ada@example.com");
    expect(engine.log().join("\n")).not.toContain("ada@example.com");
    expect(JSON.stringify(window.sessionStorage) + JSON.stringify(window.localStorage) + window.name).not.toContain("ada@example.com");
    expect((document.getElementById("em") as HTMLInputElement).value).toBe("ada@example.com");
  });

  test("The diagnostic log scrubs applicant strings even when a page label contains one", async () => {
    mount(`<label for="a">Contact ada@example.com about this?</label><input id="a">${PAD}`);
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    expect(engine.log().join("\n")).not.toContain("ada@example.com");
  });

  test("A start without an applicant snapshot is refused", () => {
    const engine = makeEngine(fakeHost());
    expect(engine.start({} as never).started).toBe(false);
  });

  test("A second start on the same document resets the per-run state", async () => {
    mount(FORM + PAD);
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    (document.getElementById("fn") as HTMLInputElement).value = "";
    const s = summaryOf(await runFill(engine));
    expect((document.getElementById("fn") as HTMLInputElement).value).toBe("Ada");
    expect(s.rows).toHaveLength(3);
  });
});
