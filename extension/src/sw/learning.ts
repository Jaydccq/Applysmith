// What a fill tells the server afterwards: the values the extension's own
// profile pass wrote (so a user fill equal to one is not counted as ground
// truth), and the values the person committed themselves.
import type { LearningEvent, LearningEventsResult, SystemWriteRequest } from "../../../web/src/wire";
import type { LearningEvent as EngineEvent, Row } from "../../../engine/src/protocol";
import type { UserEdit } from "../shared/messages";
import { api } from "./state";

const BATCH = 50;
export const QUIET_MS = 2000;

const WRITE_LANES = new Set(["profile", "attestation"]);

/** Rows the profile pass (or an attestation) filled with a value the page now holds. */
export function systemWrites(rows: Row[]): { selector: string; value: string }[] {
  return rows
    .filter((r) => WRITE_LANES.has(r.lane) && r.outcome === "filled" && r.value)
    .map((r) => ({ selector: r.selector, value: r.value as string }));
}

/** Reports each profile-lane write. A failure of one never stops the others. */
export async function reportSystemWrites(runId: string, rows: Row[]): Promise<number> {
  let sent = 0;
  for (const w of systemWrites(rows)) {
    const body: SystemWriteRequest = { runId, selector: w.selector, value: w.value };
    try {
      await api.call("/api/answer-bank/system-writes", { method: "POST", body });
      sent++;
    } catch {
      /* the value is then counted as the user's if they leave it; the server still cross-checks the match writes */
    }
  }
  return sent;
}

/** The server's control kinds; the engine's file kind has none. */
const CONTROL_KIND: Record<string, string | undefined> = {
  text: "text",
  textarea: "text",
  contenteditable: "text",
  select: "select",
  "radio-group": "radio-group",
  checkbox: "checkbox",
  "custom-listbox": "custom-listbox",
};

export interface EditContext {
  runId: string;
  host: string;
  /** What the system wrote into the control, by selector. */
  before: Map<string, string>;
  /** The bank entry that produced the system's value, by selector. */
  entry: Map<string, string>;
}

/**
 * A manual-fill event for a value the person committed. `trustedInput` is
 * true because the bridge forwards only trusted change events; `selector`
 * lets the server compare the value with the run's own writes.
 */
export function manualFill(edit: UserEdit, ctx: EditContext, now = new Date()): LearningEvent | undefined {
  const controlKind = CONTROL_KIND[edit.kind];
  if (!controlKind || !edit.value || !edit.label.trim()) return undefined;
  const before = ctx.before.get(edit.selector) ?? "";
  if (before && before === edit.value) return undefined;
  const choice = controlKind === "select" || controlKind === "radio-group" || controlKind === "custom-listbox";
  const event: LearningEvent = {
    eventId: crypto.randomUUID(),
    kind: "manual-fill",
    host: ctx.host,
    question: edit.label,
    controlKind,
    at: now.toISOString(),
    after: edit.value,
    runId: ctx.runId,
    selector: edit.selector,
    trustedInput: true,
  };
  if (choice) event.optionLabel = edit.value;
  if (edit.inputType) event.inputType = edit.inputType;
  if (edit.autocomplete) event.autocomplete = edit.autocomplete;
  if (edit.section?.length) event.section = edit.section;
  if (edit.options?.length) event.options = edit.options;
  if (before) {
    event.before = before;
    const entryId = ctx.entry.get(edit.selector);
    if (entryId) event.entryId = entryId;
  }
  return event;
}

/** A frame the fill could not enter, as the top frame's engine reported it, in the upload's shape and tied to the run. */
export function frameEvent(ev: EngineEvent, runId: string): LearningEvent {
  return {
    eventId: ev.eventId,
    kind: ev.kind,
    host: ev.host,
    question: ev.question,
    controlKind: ev.controlKind,
    at: new Date(ev.time).toISOString(),
    reason: ev.reason,
    runId,
  };
}

/**
 * Collects events and uploads them in batches of at most 50. A failed upload is
 * retried once and then dropped; the fill is never affected.
 */
export class Uploader {
  private events = new Map<string, LearningEvent>();
  private timer: ReturnType<typeof setTimeout> | undefined;
  private flushing: Promise<void> = Promise.resolve();
  dropped = 0;
  uploaded = 0;

  add(e: LearningEvent): void {
    // A later edit of the same control replaces the earlier one.
    this.events.set(e.selector ?? e.eventId, e);
    clearTimeout(this.timer);
    if (this.events.size >= BATCH) void this.flush();
    else this.timer = setTimeout(() => void this.flush(), QUIET_MS);
  }

  flush(): Promise<void> {
    clearTimeout(this.timer);
    this.flushing = this.flushing.then(async () => {
      const all = [...this.events.values()];
      this.events.clear();
      for (let i = 0; i < all.length; i += BATCH) await this.send(all.slice(i, i + BATCH));
    });
    return this.flushing;
  }

  private async send(batch: LearningEvent[]): Promise<void> {
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        await api.call<LearningEventsResult>("/api/answer-bank/events", { method: "POST", body: { events: batch } });
        this.uploaded += batch.length;
        return;
      } catch {
        /* retried once */
      }
    }
    this.dropped += batch.length;
  }
}
