// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Resilience to re-renders and user edits (the main-thread watchdog).
import { describe, expect, test } from "vitest";
import { DEFAULT_TIMING } from "../src/env";
import { RunLog } from "../src/run";
import { Watchdog } from "../src/watchdog";

/** A watchdog on a clock the test moves by hand. */
function clocked(observer?: ConstructorParameters<typeof Watchdog>[0]["observer"]) {
  let t = 10_000;
  const log = new RunLog(() => t);
  const dog = new Watchdog({ timing: DEFAULT_TIMING, log, now: () => t, observer });
  return {
    dog,
    log,
    advance: (ms: number) => (t += ms),
    stalls: () => log.get().filter((l) => l.includes("watchdog stall")),
  };
}

describe("Main-thread watchdog", () => {
  test("Stall report: a tick 2.5 s late records one stall event and none again within 5 s", () => {
    const c = clocked();
    c.dog.start();
    c.advance(500);
    c.dog.tick();
    expect(c.stalls()).toHaveLength(0);
    c.advance(500 + 2500);
    c.dog.tick();
    expect(c.stalls()).toHaveLength(1);
    expect(c.stalls()[0]).toContain("kind=late-tick");
    c.advance(500 + 2500);
    c.dog.tick();
    expect(c.stalls()).toHaveLength(1);
    // Five seconds after the report another one is allowed.
    c.advance(500 + 2500);
    c.dog.tick();
    expect(c.stalls()).toHaveLength(2);
    c.dog.stop();
  });

  test("The page's own long-task observer feeds the window, and a failing observer never reaches the run", () => {
    let deliver: (entries: { startTime: number; duration: number }[]) => void = () => {};
    class Fake {
      constructor(cb: (list: { getEntries(): { startTime: number; duration: number }[] }) => void) {
        deliver = (entries) => cb({ getEntries: () => entries });
      }
      observe(): void {}
      disconnect(): void {}
    }
    const c = clocked(Fake);
    c.dog.start();
    deliver([
      { startTime: 100, duration: 600 },
      { startTime: 800, duration: 600 },
      { startTime: 1500, duration: 600 },
    ]);
    expect(c.stalls()).toHaveLength(1);
    c.dog.stop();
    class Broken {
      constructor() {
        throw new Error("unsupported entry type");
      }
    }
    const b = clocked(Broken as never);
    expect(() => b.dog.start()).not.toThrow();
    b.dog.stop();
  });

});
