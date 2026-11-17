// Clean-room: docs/provenance.md#CR-ENGINE
// Holds native form submission while a start or answer call is in flight.

import type { BlockedSubmit } from "./protocol";
import { collapse, flatText, type Win } from "./dom";

export class SubmitHold {
  private depth = 0;
  private held: BlockedSubmit[] = [];
  private readonly listener = (ev: Event) => this.onSubmit(ev as SubmitEvent);

  constructor(private readonly win: Win) {}

  acquire(): void {
    if (this.depth++ === 0) this.win.addEventListener("submit", this.listener, true);
  }

  release(): void {
    if (this.depth > 0 && --this.depth === 0) this.win.removeEventListener("submit", this.listener, true);
  }

  get active(): boolean {
    return this.depth > 0;
  }

  /** Forget everything held so far; called by every start. */
  reset(): void {
    this.held = [];
  }

  list(): BlockedSubmit[] {
    return this.held.map((h) => ({ ...h }));
  }

  dispose(): void {
    this.depth = 0;
    this.win.removeEventListener("submit", this.listener, true);
  }

  private onSubmit(ev: SubmitEvent): void {
    // Cancelling comes first: a failure while recording must never let the submission through.
    ev.preventDefault();
    ev.stopImmediatePropagation();
    try {
      const form = ev.target as HTMLFormElement | null;
      const id = form?.getAttribute?.("id") || form?.getAttribute?.("name") || form?.getAttribute?.("action") || "form";
      const sub = ev.submitter ? collapse(flatText(ev.submitter) || (ev.submitter as HTMLInputElement).value || "") : "";
      this.held.push({ form: id, submitter: sub ? sub.slice(0, 80) : "no submitter" });
    } catch {
      this.held.push({ form: "form", submitter: "no submitter" });
    }
  }
}
