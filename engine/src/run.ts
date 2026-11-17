// Clean-room: docs/provenance.md#CR-ENGINE
// One fill run: cancellation points, pause, the page deadline, the diagnostic
// log, and the rows the run has produced so far.

import { StopSignal, type Timing, type WriteCtx } from "./env";
import type { MainWorld } from "./mainworld-client";
import { siteOf } from "./sites";
import type { UserEdits } from "./restore";
import type { ApplicantSnapshot, Draft, PostingContext, Resolution, Row, StopReason } from "./protocol";
import type { Option } from "./scan";

const MAX_LOG_LINES = 2000;
const MAX_FIELD_CHARS = 4000;

function leafStrings(value: unknown, out: string[]): void {
  if (typeof value === "string") {
    if (value.trim().length >= 4) out.push(value.trim());
  } else if (Array.isArray(value)) value.forEach((v) => leafStrings(v, out));
  else if (value && typeof value === "object") Object.values(value).forEach((v) => leafStrings(v, out));
}

/** Bounded diagnostic log. Applicant strings never survive into a line. */
export class RunLog {
  private lines: string[] = [];
  private secrets: string[] = [];

  constructor(private readonly now: () => number) {}

  protect(applicant: ApplicantSnapshot): void {
    const found: string[] = [];
    leafStrings(applicant, found);
    this.secrets = Array.from(new Set(found)).sort((a, b) => b.length - a.length);
  }

  private scrub(text: string): string {
    let out = text;
    for (const s of this.secrets) {
      out = out.split(s).join("[applicant]");
      const lower = s.toLowerCase();
      if (lower !== s) out = out.split(lower).join("[applicant]");
    }
    return out;
  }

  add(stage: string, event: string, data?: Record<string, unknown>): void {
    let tail = "";
    if (data) {
      const parts: string[] = [];
      for (const [k, v] of Object.entries(data)) {
        let s: string;
        try {
          s = typeof v === "string" ? v : JSON.stringify(v);
        } catch {
          s = String(v);
        }
        parts.push(`${k}=${(s ?? "").slice(0, MAX_FIELD_CHARS)}`);
      }
      tail = ` ${parts.join(" ")}`;
    }
    this.lines.push(this.scrub(`${Math.round(this.now())} ${stage} ${event}${tail}`));
    if (this.lines.length > MAX_LOG_LINES) this.lines.splice(0, this.lines.length - MAX_LOG_LINES);
  }

  get(): string[] {
    return this.lines.slice();
  }
}

export interface RunOptions {
  id: string;
  applicant: ApplicantSnapshot;
  posting: PostingContext;
  acceptLegalDeclarations: boolean;
  /** The user-edit tracker of the page: no pass of this run writes a control it has marked. */
  edits: UserEdits;
  pageDeadlineMs: number;
  prepassDeadlineMs: number;
  profileApplyBudgetMs: number;
  /** The channel to the main-world helper, when the host installed one. */
  main?: MainWorld | null;
  /** Fired when the page deadline passes; the engine stops the run with reason deadline. */
  onDeadline?: () => void;
  /** The log an answer run continues: the last fill's, which already scrubs its applicant's strings. */
  log?: RunLog;
}

export class Run {
  readonly id: string;
  /** The site table's id for the document this run fills, or null. */
  readonly site: string | null;
  readonly startedAt: number;
  applicant: ApplicantSnapshot | null;
  readonly posting: PostingContext;
  readonly acceptLegalDeclarations: boolean;
  readonly edits: UserEdits;
  readonly prepassDeadlineMs: number;
  readonly profileApplyBudgetMs: number;
  readonly log: RunLog;
  readonly rows = new Map<string, Row>();
  readonly drafts: Draft[] = [];
  /** Canonical keys of the controls the question pass owns, unioned across passes. */
  readonly claimed = new Set<string>();
  /** Controls that held no value when the run began; a value found later was authored by the page. */
  readonly emptyAtStart = new Set<string>();
  /** Text boxes the question pass already left, with the value they held; the blur pass touches them again only once that value changed. */
  readonly committed = new Map<Element, string>();
  stopReason: StopReason | null = null;
  paused = false;
  matchUnavailable = false;
  coverLetterAttached = false;
  resumeAttached = false;
  /** This run handed the page a document to parse. */
  parseTriggered = false;
  /** The parse was seen to land and the page went quiet after it: no late parse is waited for. */
  parseSettled = false;
  /** The run has a document to attach, so an acknowledgement that depends on one may be ticked. */
  hasDocument = false;
  /** Answers whose control opened with no options: retried once by the cascade pass. */
  readonly unsatisfied = new Map<string, Resolution>();
  /** The options of each closed listbox this run opened and read, by selector: a later scan finds it closed again. */
  readonly listOptions = new Map<string, Option[]>();

  private readonly haltedPromise: Promise<never>;
  private haltNow: (e: StopSignal) => void = () => {};
  private gate: Promise<void> | null = null;
  private openGate: (() => void) | null = null;
  private wakers = new Set<() => void>();
  private deadlineTimer: ReturnType<typeof setTimeout> | undefined;
  private deadlineLeft: number;
  private deadlineSetAt = 0;
  /** The time spent paused before the pause in progress, and when that one began. */
  private pausedMs = 0;
  private pausedAt = 0;

  /** The channel to the main-world helper, when the host installed one. */
  readonly main: MainWorld | null;
  private readonly onDeadline: (() => void) | undefined;

  // The options are read here and not kept: once the run is disposed nothing of it holds the applicant snapshot.
  constructor(opts: RunOptions, readonly timing: Timing, readonly doc: Document) {
    this.id = opts.id;
    this.main = opts.main ?? null;
    this.onDeadline = opts.onDeadline;
    this.site = siteOf(doc.defaultView?.location?.href ?? "");
    this.applicant = opts.applicant;
    this.posting = opts.posting;
    this.acceptLegalDeclarations = opts.acceptLegalDeclarations;
    this.edits = opts.edits;
    this.prepassDeadlineMs = opts.prepassDeadlineMs;
    this.profileApplyBudgetMs = opts.profileApplyBudgetMs;
    this.startedAt = Date.now();
    this.log = opts.log ?? new RunLog(() => Date.now() - this.startedAt);
    if (!opts.log) this.log.protect(opts.applicant);
    this.deadlineLeft = opts.pageDeadlineMs;
    this.haltedPromise = new Promise<never>((_, reject) => {
      this.haltNow = reject;
    });
    this.haltedPromise.catch(() => {});
    this.armDeadline();
  }

  /** Awaits work that is outside the engine's control (a binding), giving up when the run is stopped. */
  until<T>(work: Promise<T>): Promise<T> {
    return Promise.race([work, this.haltedPromise]);
  }

  get stopped(): boolean {
    return this.stopReason !== null;
  }

  private armDeadline(): void {
    clearTimeout(this.deadlineTimer);
    if (!this.onDeadline) return;
    this.deadlineSetAt = Date.now();
    this.deadlineTimer = setTimeout(() => this.onDeadline?.(), Math.max(0, this.deadlineLeft));
  }

  /** Ends the run's loops at their next cancellation point. */
  halt(reason: StopReason): void {
    if (this.stopReason) return;
    this.stopReason = reason;
    clearTimeout(this.deadlineTimer);
    this.haltNow(new StopSignal(reason));
    this.openGate?.();
    for (const wake of Array.from(this.wakers)) wake();
  }

  /** The clock every stage and write budget is measured on: the time less the time spent paused, so it stands still during a pause. */
  active(): number {
    return (this.paused ? this.pausedAt : Date.now()) - this.pausedMs;
  }

  pause(): void {
    if (this.paused || this.stopped) return;
    this.paused = true;
    this.pausedAt = Date.now();
    // Paused time does not count toward the page deadline.
    clearTimeout(this.deadlineTimer);
    this.deadlineLeft -= Date.now() - this.deadlineSetAt;
    this.gate = new Promise((resolve) => {
      this.openGate = resolve;
    });
  }

  resume(): void {
    if (!this.paused) return;
    this.paused = false;
    this.pausedMs += Date.now() - this.pausedAt;
    this.openGate?.();
    this.gate = null;
    this.openGate = null;
    if (!this.stopped) this.armDeadline();
  }

  async checkpoint(): Promise<void> {
    if (this.gate) await this.gate;
    if (this.stopReason) throw new StopSignal(this.stopReason);
  }

  /** Sleeps, ending early when the run is stopped. */
  sleep(ms: number): Promise<void> {
    if (this.stopped || ms <= 0) return Promise.resolve();
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

  dispose(): void {
    clearTimeout(this.deadlineTimer);
    this.applicant = null;
    this.openGate?.();
  }

  ctx(): WriteCtx {
    return {
      site: this.site,
      main: this.main,
      doc: this.doc,
      timing: this.timing,
      sleep: (ms) => this.sleep(ms),
      checkpoint: () => this.checkpoint(),
      active: () => this.active(),
      now: () => new Date(),
      log: (event, data) => this.log.add("write", event, data),
    };
  }

  async stage<T>(name: string, work: () => Promise<T>): Promise<T> {
    await this.checkpoint();
    const t0 = Date.now();
    this.log.add(name, "start");
    try {
      return await work();
    } finally {
      this.log.add(name, "end", { ms: Date.now() - t0 });
    }
  }

  setRow(row: Row): void {
    this.rows.set(row.selector, row);
  }
}
