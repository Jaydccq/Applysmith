// Clean-room: docs/provenance.md#CR-ENGINE
// The page API: start, status, stop, pause, resume, answer, inspect, the hold
// of the host's own input and the read-only reports. One instance serves one
// document.

import { answerControl, makeRow } from "./answers";
import { withPagePosting } from "./capture";
import { probeControls } from "./detect";
import { DEFAULT_TIMING, StopSignal, type Timing } from "./env";
import type { Win } from "./dom";
import type { Host } from "./host";
import { failedDecision, frameRollup, probeDecision, reduceUrl, staticDecision, unreachableFrameEvents } from "./frames";
import { runPipeline } from "./pipeline";
import { UserEdits, type Restorer } from "./restore";
import type {
  AnswerReply,
  AnswerResult,
  ApplicantSnapshot,
  BlockedSubmit,
  FrameDecision,
  FrameDecisionRequest,
  InspectRow,
  LearningEvent,
  ProbeResult,
  Resolution,
  RunState,
  StartArgs,
  StartReply,
  StatusReport,
  StopReason,
  Summary,
  SubmitObservation,
} from "./protocol";
import { disableAshbyQueue, enableAshbyQueue, gapsFrom, repairYesNo } from "./ashby";
import { CorrectionWatcher } from "./corrections";
import { MainWorld } from "./mainworld-client";
import { eventId, uncertaintyEvents } from "./learning";
import { Run } from "./run";
import { siteOf } from "./sites";
import { scanControls, type ControlInfo } from "./scan";
import { resolveSelector } from "./selector";
import { SubmitHold } from "./submit-hold";
import { Watchdog } from "./watchdog";
import { buildSummary, inspectPage } from "./summary";

export interface EngineOptions {
  host: Host;
  win: Win;
  timing?: Partial<Timing>;
}

const DEFAULTS = { pageDeadlineMs: 300_000, prepassDeadlineMs: 90_000, profileApplyBudgetMs: 60_000 };

/** A hold of the host's own input outlives the 30 s the host gives one control, and no more. */
const INPUT_HOLD_MS = 35_000;

function newRunId(win: Win): string {
  const c = win.crypto as Crypto | undefined;
  if (c?.randomUUID) return c.randomUUID();
  return `run-${Math.random().toString(16).slice(2)}${Date.now().toString(16)}`;
}

export class AutofillEngine {
  stamp = "dev";
  private readonly host: Host;
  private readonly win: Win;
  private readonly timing: Timing;
  private readonly hold: SubmitHold;
  private readonly edits: UserEdits;
  /** The restore pass of the last run: it outlives the summary and ends with a new run, a stop or a dispose. */
  private restorer: Restorer | null = null;
  private readonly attached = { nodes: new WeakSet<Element>(), ids: new Set<string>() };
  private state: RunState = { state: "idle" };
  private watchdog: Watchdog | null = null;
  private current: Run | null = null;
  private last: Run | null = null;
  /**
   * The last fill's applicant, kept until the next fill: an answer the host writes after the fill is checked against
   * it, so the applicant's own name, phone or email never goes into a reference's or an employer's box.
   */
  private applicant: ApplicantSnapshot | null = null;
  private lastStopped = false;
  /** The pipeline of a stopped run may still be unwinding after its status was published. */
  private task: Promise<void> | null = null;
  private busy = false;
  private written = new Map<string, string>();
  /** The channel to the main-world helper, once the host has told the engine the helper's nonce. */
  private main: MainWorld | null = null;
  private gaps: string[] = [];
  /** After a fill, what the person changes is watched; the events wait here for the host to take them. */
  private watcher: CorrectionWatcher | null = null;
  private pending: LearningEvent[] = [];
  /** While the host drives trusted input itself (holdInput): the timer that ends a hold it never released. */
  private inputHold: ReturnType<typeof setTimeout> | null = null;

  constructor(opts: EngineOptions) {
    this.host = opts.host;
    this.win = opts.win;
    this.timing = { ...DEFAULT_TIMING, ...opts.timing };
    this.hold = new SubmitHold(opts.win);
    this.edits = new UserEdits(opts.win.document);
  }

  get doc(): Document {
    return this.win.document;
  }

  setStamp(stamp: string): void {
    this.stamp = stamp;
  }

  start(args: StartArgs): StartReply {
    if (this.current) return { started: false, reason: "a fill is already running" };
    if (this.task) return { started: false, reason: "a stopped fill is still unwinding" };
    if (this.busy) return { started: false, reason: "an answer call is in progress" };
    if (!args?.applicant?.candidate) return { started: false, reason: "the applicant snapshot is missing" };
    const id = args.runId ?? newRunId(this.win);
    const run: Run = new Run(
      {
        id,
        applicant: args.applicant,
        posting: withPagePosting(args.posting ?? {}, this.doc, this.win),
        acceptLegalDeclarations: args.acceptLegalDeclarations === true,
        edits: this.edits,
        pageDeadlineMs: args.pageDeadlineMs ?? DEFAULTS.pageDeadlineMs,
        prepassDeadlineMs: args.prepassDeadlineMs ?? DEFAULTS.prepassDeadlineMs,
        profileApplyBudgetMs: args.profileApplyBudgetMs ?? DEFAULTS.profileApplyBudgetMs,
        main: this.main,
        onDeadline: () => {
          this.stop(id, "deadline");
        },
      },
      this.timing,
      this.doc,
    );
    this.releaseInput([]);
    this.endWatch();
    this.hold.reset();
    this.hold.acquire();
    this.restorer?.stop();
    this.restorer = null;
    this.watchdog?.stop();
    this.watchdog = new Watchdog({
      timing: this.timing,
      log: run.log,
      now: () => this.win.performance?.now() ?? Date.now(),
      observer: (this.win as unknown as { PerformanceObserver?: ConstructorParameters<typeof Watchdog>[0]["observer"] }).PerformanceObserver,
    });
    this.watchdog.start();
    this.edits.start();
    this.edits.mark(args.edited ?? []);
    this.written = new Map();
    this.current = run;
    this.last = run;
    this.applicant = args.applicant;
    this.lastStopped = false;
    this.state = { state: "running", runId: id, startedAt: run.startedAt };
    const task = this.execute(run, args.watchCorrections !== false).finally(() => {
      if (this.task === task) this.task = null;
    });
    this.task = task;
    return { started: true, runId: id };
  }

  private async execute(run: Run, watch: boolean): Promise<void> {
    try {
      const summary = await runPipeline(run, {
        host: this.host,
        win: this.win,
        attached: this.attached,
        written: this.written,
        edits: this.edits,
        afterSummary: async () => {
          await this.refreshSavedFormGaps();
        },
        onRestorer: (r) => {
          this.restorer = r;
        },
      });
      // The late completion of a stopped run never replaces the current status.
      if (this.current !== run || run.stopped) return;
      summary.events = uncertaintyEvents(run, this.written);
      this.settle(run, { state: "done", runId: run.id, summary });
      if (watch) this.watch(run, summary);
    } catch (e) {
      if (this.current !== run || run.stopped) return;
      const message = e instanceof StopSignal ? e.message : e instanceof Error ? e.message : String(e);
      this.settle(run, { state: "error", runId: run.id, message });
    }
  }

  private settle(run: Run, state: RunState): void {
    this.state = state;
    this.current = null;
    this.watchdog?.stop();
    this.watchdog = null;
    this.hold.release();
    void this.queueOff();
    run.dispose();
    if (state.state === "idle" || state.state === "running" || state.state === "paused") return;
    const notice =
      state.state === "done"
        ? { runId: run.id, state: "done" as const, summary: state.summary }
        : state.state === "stopped"
          ? { runId: run.id, state: "stopped" as const, reason: state.reason, summary: state.summary }
          : { runId: run.id, state: "error" as const, message: state.message };
    // A missing or failing binding never changes the state; status stays the source of truth.
    void Promise.resolve()
      .then(() => this.host.terminal(notice))
      .catch(() => {});
  }

  status(): StatusReport {
    return { ...this.state, stamp: this.stamp };
  }

  stop(runId: string, reason: StopReason = "cancelled"): boolean {
    const run = this.current;
    // A finished run's restore pass and tail guard end with its stop as well.
    if (!run && runId === this.last?.id) this.restorer?.stop();
    if (!run || run.id !== runId || run.stopped) return false;
    run.halt(reason);
    this.restorer?.stop();
    this.lastStopped = true;
    this.settle(run, { state: "stopped", runId, reason, summary: buildSummary(run, this.win, { partial: true }) });
    return true;
  }

  pause(): void {
    const run = this.current;
    if (!run || run.paused) return;
    run.pause();
    this.state = { state: "paused", runId: run.id, startedAt: run.startedAt };
  }

  resume(): void {
    const run = this.current;
    if (!run || !run.paused) return;
    run.resume();
    this.state = { state: "running", runId: run.id, startedAt: run.startedAt };
  }

  /**
   * Write match results the host obtained after the fill. A resolution starts
   * only when its whole write budget still fits in the call's window, and the
   * first one's write ends with the window; the host sends the remainder in
   * another call.
   */
  async answer(resolutions: Resolution[], runId?: string): Promise<AnswerReply> {
    if (this.current) throw new Error("a fill is running: the fill owns the page");
    if (this.task) throw new Error("a stopped fill is still unwinding");
    if (this.busy) throw new Error("another answer call is in progress");
    if (this.lastStopped) throw new Error("the run was stopped");
    if (runId !== undefined && runId !== this.last?.id) throw new Error("stale run id");
    this.busy = true;
    this.hold.acquire();
    // The engine's own writes are no person's edit, whatever trusted input reached the page just before; a hold the host
    // took for its own input stays the host's to release.
    const watched = !this.inputHold;
    if (watched) this.watcher?.hold(true);
    const queued = await this.queueOn();
    try {
      const run = new Run(
        {
          id: this.last?.id ?? "answer",
          applicant: this.applicant ?? { candidate: {} },
          posting: {},
          acceptLegalDeclarations: this.last?.acceptLegalDeclarations === true,
          edits: this.edits,
          pageDeadlineMs: 0,
          prepassDeadlineMs: 0,
          profileApplyBudgetMs: 0,
          main: this.main,
          // What the writers try and see goes where the host reads it: the last fill's log.
          log: this.last?.log,
        },
        this.timing,
        this.doc,
      );
      const began = Date.now();
      const infos = scanControls(this.doc);
      const results: AnswerResult[] = [];
      for (const [i, res] of resolutions.entries()) {
        if (i > 0 && Date.now() - began + this.timing.fieldBudgetMs + 1000 > this.timing.answerWindowMs) break;
        const info = this.infoFor(infos, res.selector);
        if (!info) {
          results.push({ selector: res.selector, outcome: "failed", reason: "control-detached" });
          continue;
        }
        // The host stops waiting a second after the window: the write in hand is cut there, whatever its own budget.
        const row = await answerControl(run, info, res, this.written, began + this.timing.answerWindowMs - 1000);
        // The watch of the fill measures a later edit against this write too.
        if (row.outcome === "filled") this.watcher?.wrote(info, row);
        results.push({ selector: res.selector, outcome: row.outcome, reason: row.reason, value: row.value, optionLabel: row.optionLabel });
      }
      run.dispose();
      return { results, handled: results.length };
    } finally {
      if (queued) await this.queueOff();
      if (watched) this.watcher?.hold(false);
      this.hold.release();
      this.busy = false;
    }
  }

  /**
   * The host drives trusted input itself, as the apply chain's computer-use rung does (owner decision F-9): until
   * releaseInput, native submissions are held and no event, trusted or not, is the person's edit. A hold the host never
   * releases ends after INPUT_HOLD_MS. Refused while a fill or an answer call owns the page.
   */
  holdInput(): boolean {
    if (this.current || this.task || this.busy) return false;
    if (this.inputHold) clearTimeout(this.inputHold);
    else {
      this.hold.acquire();
      this.edits.held = true;
      this.watcher?.hold(true);
    }
    this.inputHold = setTimeout(() => this.releaseInput([]), INPUT_HOLD_MS);
    return true;
  }

  /** Ends the hold. `writes` are the values the host's input left in their controls: a later edit by the person measures against them, as against the fill's own. */
  releaseInput(writes: Resolution[]): void {
    if (!this.inputHold) return;
    clearTimeout(this.inputHold);
    this.inputHold = null;
    this.edits.held = false;
    this.watcher?.hold(false);
    this.hold.release();
    const infos = writes.length && this.watcher ? scanControls(this.doc) : [];
    for (const w of writes) {
      const info = this.infoFor(infos, w.selector);
      if (info) this.watcher?.wrote(info, makeRow(info, "bank", "filled", { value: w.value, optionLabel: w.optionLabel }));
    }
  }

  private infoFor(infos: ControlInfo[], selector: string): ControlInfo | undefined {
    const direct = infos.find((i) => i.selector === selector);
    if (direct) return direct;
    const el = resolveSelector(this.doc, selector);
    return el ? infos.find((i) => i.el === el || i.members.includes(el as HTMLInputElement) || i.toggles.includes(el as HTMLElement)) : undefined;
  }

  inspect(): InspectRow[] {
    return inspectPage(this.doc);
  }

  /** Side-effect-free count of the data-entry controls this document holds. */
  probe(): ProbeResult {
    return probeControls(this.doc);
  }

  /**
   * The fan-out decision for one frame of this page: from the registry and
   * deny-list alone, from a probe the host ran in the frame, or a failure the
   * host reports. Null means the frame needs a probe first.
   */
  decideFrame(req: FrameDecisionRequest): FrameDecision | null {
    if (req.failure) return failedDecision(req.frameId, req.url, req.failure.outcome, req.failure.reason);
    const fixed = staticDecision(req.frameId, req.url);
    if (fixed) return fixed;
    return req.probe ? probeDecision(req.frameId, req.url, req.probe) : null;
  }

  /**
   * The host's account of the page's frames once the walk is over: the
   * roll-up and one line per frame not reached go to the last fill's log, and
   * each frame that failed becomes one learning event for the host to upload.
   */
  frameReport(decisions: FrameDecision[]): LearningEvent[] {
    const log = this.last?.log;
    log?.add("frames", "rollup", frameRollup(decisions));
    for (const d of decisions) if (d.outcome.startsWith("failed-")) log?.add("frames", "not-reached", { url: reduceUrl(d.url), outcome: d.outcome, reason: d.reason ?? "" });
    const loc = this.win.location;
    return unreachableFrameEvents(decisions, { host: loc?.hostname ?? "", url: loc?.href ?? "", site: this.site() }, () => eventId(this.win as unknown as Window), Date.now());
  }

  /** The control a selector names, for a host that acts on it natively (setting files). */
  locate(selector: string): Element | null {
    return resolveSelector(this.doc, selector);
  }

  private watch(run: Run, summary: Summary): void {
    this.endWatch();
    this.watcher = new CorrectionWatcher({
      doc: this.doc,
      win: this.win,
      timing: this.timing,
      site: run.site,
      rows: summary.rows,
    });
    this.watcher.start();
  }

  private endWatch(): void {
    if (!this.watcher) return;
    this.watcher.stop();
    this.pending.push(...this.watcher.drain());
    this.watcher = null;
  }

  /** What the person has changed since the fill, as events for the host to upload. They are handed over once. */
  drainEvents(): LearningEvent[] {
    const out = [...this.pending, ...(this.watcher?.drain() ?? [])];
    this.pending = [];
    return out;
  }

  /** The site table's id for this document, or null. */
  site(): string | null {
    return siteOf(this.win.location?.href ?? "");
  }

  blockedSubmits(): BlockedSubmit[] {
    return this.hold.list();
  }

  log(): string[] {
    return this.last?.log.get() ?? [];
  }

  /** The host installed the main-world helper under this nonce; the engine talks to it from now on. */
  useHelper(nonce: string): void {
    if (this.main?.nonce === nonce) return;
    this.main?.dispose();
    this.main = new MainWorld(this.win, nonce);
  }

  /** Required questions the site's own saved copy lacks, as of the last time the saved copy was read; only a site driver can say. */
  savedFormGaps(): string[] {
    return this.gaps.slice();
  }

  /** Reads the saved copy again, then reports its gaps. */
  async refreshSavedFormGaps(): Promise<string[]> {
    if (this.main && siteOf(this.win.location?.href ?? "") === "ashby") {
      try {
        await this.main.request("queue.drain", { timeoutMs: this.timing.ashbyDrainMs }, this.timing.ashbyDrainMs + 1500);
        this.gaps = gapsFrom(this.doc, await this.main.request("queue.echo"));
      } catch {
        /* no helper, no claim */
      }
    }
    return this.savedFormGaps();
  }

  /**
   * The submit-repair round for Ashby: a Yes/No the saved copy lacks is cleared and pressed again.
   * It runs only when the host asks (after a refused submit), never as part of a fill; returns the gaps that remain.
   */
  async repairSavedForm(): Promise<string[]> {
    if (this.current || this.task || this.busy || !this.main || siteOf(this.win.location?.href ?? "") !== "ashby") return this.savedFormGaps();
    this.busy = true;
    this.hold.acquire();
    const queued = await this.queueOn();
    try {
      const ctx = {
        doc: this.doc,
        timing: this.timing,
        sleep: (ms: number) => new Promise<void>((r) => setTimeout(r, ms)),
        checkpoint: async () => {},
        now: () => new Date(),
        log: () => {},
        site: "ashby" as const,
        main: this.main,
      };
      await repairYesNo(ctx);
    } finally {
      if (queued) await this.queueOff();
      this.hold.release();
      this.busy = false;
    }
    return this.refreshSavedFormGaps();
  }

  /** Ashby's save queue runs only while the engine writes: this turns it on for a call that writes after the fill. */
  private async queueOn(): Promise<boolean> {
    return !!this.main && siteOf(this.win.location?.href ?? "") === "ashby" && (await enableAshbyQueue(this.main));
  }

  /** Ends the queue; a request the page makes afterwards, a submission above all, is never held. */
  private async queueOff(): Promise<void> {
    if (this.main && siteOf(this.win.location?.href ?? "") === "ashby") await disableAshbyQueue(this.main);
  }

  /** What the submit observer saw in the last `sinceMs` milliseconds (default two minutes): evidence that the page's submit path fired, never a confirmation. */
  submitObservations(sinceMs = 120_000): SubmitObservation[] {
    return this.main?.observations(sinceMs) ?? [];
  }

  /** Releases listeners, observers and timers; a newer instance calls this on the one it replaces. */
  dispose(): void {
    const run = this.current;
    if (run) {
      run.halt("cancelled");
      run.dispose();
    }
    this.current = null;
    this.applicant = null;
    this.state = { state: "idle" };
    this.releaseInput([]);
    this.restorer?.stop();
    this.restorer = null;
    this.watchdog?.stop();
    this.watchdog = null;
    this.edits.stop();
    this.hold.dispose();
    this.endWatch();
    if (this.main && siteOf(this.win.location?.href ?? "") === "ashby") this.main.send("queue.disable");
    this.main?.dispose();
    this.main = null;
  }
}
