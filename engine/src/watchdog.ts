// Clean-room: docs/provenance.md#CR-ENGINE
// Main-thread watchdog. It observes only: a tick that arrives far later than
// scheduled, or long tasks that add up inside a short window, are written to
// the diagnostic log as one stall event, then it stays quiet for a cooldown.
// A fill that slows the page down is told apart from a page that is slow.

import type { Timing } from "./env";
import type { RunLog } from "./run";

type LongTaskSource = { observe(opts: { type: string; buffered?: boolean }): void; disconnect(): void };
type LongTaskCtor = new (cb: (list: { getEntries(): { startTime: number; duration: number }[] }) => void) => LongTaskSource;

export interface WatchdogEnv {
  timing: Timing;
  log: RunLog;
  /** Milliseconds on a monotonic clock; injected so a test needs no real stall. */
  now: () => number;
  /** The page's PerformanceObserver, when it has one that reports long tasks. */
  observer?: LongTaskCtor;
}

export class Watchdog {
  private timer: ReturnType<typeof setTimeout> | undefined;
  private source: LongTaskSource | null = null;
  private running = false;
  private expected = 0;
  private lastReport = Number.NEGATIVE_INFINITY;
  /** Long tasks inside the window, oldest first. */
  private tasks: { at: number; ms: number }[] = [];
  /** Stall events recorded so far. */
  reports = 0;

  constructor(private readonly env: WatchdogEnv) {}

  start(): void {
    if (this.running) return;
    this.running = true;
    this.expected = this.env.now() + this.env.timing.watchdogTickMs;
    this.watchLongTasks();
    this.schedule();
  }

  stop(): void {
    this.running = false;
    clearTimeout(this.timer);
    this.source?.disconnect();
    this.source = null;
  }

  private schedule(): void {
    this.timer = setTimeout(() => {
      this.tick();
      if (this.running) this.schedule();
    }, this.env.timing.watchdogTickMs);
  }

  private watchLongTasks(): void {
    const Ctor = this.env.observer;
    if (!Ctor) return;
    try {
      this.source = new Ctor((list) => {
        for (const e of list.getEntries()) this.longTask(e.startTime, e.duration);
      });
      this.source.observe({ type: "longtask" });
    } catch {
      this.source = null;
    }
  }

  /** One scheduled tick: reports when it came more than stallLateMs after it was due. */
  tick(): void {
    try {
      const now = this.env.now();
      const late = now - this.expected;
      this.expected = now + this.env.timing.watchdogTickMs;
      if (late > this.env.timing.stallLateMs) this.report("late-tick", late);
    } catch {
      /* the watchdog never throws into the run */
    }
  }

  /** A long task of the page, for the window rule: over 1,500 ms in 5 s with at least 3 in a row. */
  longTask(at: number, ms: number): void {
    try {
      const t = this.env.timing;
      this.tasks.push({ at, ms });
      this.tasks = this.tasks.filter((x) => at - x.at <= t.stallWindowMs);
      const total = this.tasks.reduce((sum, x) => sum + x.ms, 0);
      if (this.tasks.length >= t.stallLongRun && total > t.stallLongTotalMs) this.report("long-tasks", total);
    } catch {
      /* observe only */
    }
  }

  private report(kind: string, ms: number): void {
    const now = this.env.now();
    if (now - this.lastReport < this.env.timing.stallCooldownMs) return;
    this.lastReport = now;
    this.reports++;
    this.env.log.add("watchdog", "stall", { kind, ms: Math.round(ms) });
  }
}
