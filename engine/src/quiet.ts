// Clean-room: docs/provenance.md#CR-ENGINE
// Watches for form controls being added so the next write waits for the page
// to settle: an inserted block must not shift later answers into the wrong box.

import type { Run } from "./run";
import type { Win } from "./dom";

const CONTROL = "input, select, textarea, [contenteditable]";

function addsControls(nodes: NodeList): boolean {
  for (const n of Array.from(nodes)) {
    if (n.nodeType !== 1) continue;
    const el = n as Element;
    if (el.matches(CONTROL) || el.querySelector(CONTROL)) return true;
  }
  return false;
}

export class QuietWatcher {
  private dirty = false;
  private last = 0;
  private obs: MutationObserver | null = null;

  constructor(private readonly doc: Document, private readonly win: Win) {}

  start(): void {
    if (this.obs) return;
    this.obs = new this.win.MutationObserver((records) => {
      if (records.some((r) => addsControls(r.addedNodes))) {
        this.dirty = true;
        this.last = Date.now();
      }
    });
    this.obs.observe(this.doc, { childList: true, subtree: true });
  }

  stop(): void {
    this.obs?.disconnect();
    this.obs = null;
  }

  /** Resolves when the DOM has been free of added controls for quietMs, or after quietCapMs. */
  async settle(run: Run): Promise<void> {
    // Mutation records are delivered as microtasks; let them arrive first.
    await Promise.resolve();
    if (!this.dirty) return;
    const began = Date.now();
    for (;;) {
      await run.checkpoint();
      const now = Date.now();
      if (now - this.last >= run.timing.quietMs || now - began >= run.timing.quietCapMs) break;
      await run.sleep(Math.min(50, run.timing.quietMs));
    }
    this.dirty = false;
  }
}
