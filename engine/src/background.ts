// Clean-room: docs/provenance.md#CR-ENGINE
// Work that outlives a run's summary (the restore pass, the tail guard) stops
// together: one flag, sleeps that end early, and a write context whose
// checkpoint throws once the flag is set.

import { StopSignal, type Timing, type WriteCtx } from "./env";

export class Background {
  private halted = false;
  private readonly wakers = new Set<() => void>();

  get stopped(): boolean {
    return this.halted;
  }

  sleep(ms: number): Promise<void> {
    if (this.halted || ms <= 0) return Promise.resolve();
    return new Promise((resolve) => {
      const wake = () => {
        clearTimeout(timer);
        this.wakers.delete(wake);
        resolve();
      };
      const timer = setTimeout(wake, ms);
      this.wakers.add(wake);
    });
  }

  stop(): void {
    this.halted = true;
    for (const wake of Array.from(this.wakers)) wake();
  }

  /** A write context for this work; its log lines go to `log`. */
  ctx(doc: Document, timing: Timing, log: WriteCtx["log"], extra: Pick<WriteCtx, "site" | "main"> = {}): WriteCtx {
    return {
      ...extra,
      doc,
      timing,
      sleep: (ms) => this.sleep(ms),
      checkpoint: async () => {
        if (this.halted) throw new StopSignal("cancelled");
      },
      now: () => new Date(),
      log,
    };
  }
}
