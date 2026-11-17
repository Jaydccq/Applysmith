// Clean-room: docs/provenance.md#CR-ENGINE
// Timing constants, the context writers run in, and the stop signal.

import type { MainWorld } from "./mainworld-client";

export interface Timing {
  /** Wait after a write before reading the control back. */
  settleMs: number;
  /** The apply loop's pause after a custom listbox: the next popup must not open over the one still unmounting. */
  listboxSettleMs: number;
  /** A text control still empty this long after a write gets a real insertText edit. */
  emptyRetryMs: number;
  /** A select that reverts within this window is committed once more. */
  selectRecommitMs: number;
  /** DOM must be free of added controls this long before the next write. */
  quietMs: number;
  quietCapMs: number;
  /** Ceiling for one control's write. */
  fieldBudgetMs: number;
  /** answer() returns before this much time has passed. */
  answerWindowMs: number;
  /** Pause between question-pass scans. */
  passSettleMs: number;
  /** Parse-settle: quiet window after a structural change. */
  parseQuietMs: number;
  /** Parse-settle: stop waiting this long after the upload was accepted when nothing changed. */
  parseNoChangeMs: number;
  parseCapMs: number;
  parsePollMs: number;
  /** Dayforce: the record count must be non-zero and unchanged this long, and the wait ends at the cap. */
  dayforceQuietMs: number;
  dayforceParseCapMs: number;
  /** Gap between focus and blur when a control is touched so the page's validators run. */
  tapMs: number;
  /** How often a widget driver looks again while it waits for a popup, suggestions or a repaint. */
  pollMs: number;
  /** Wait for a popup to mount after a pointer open, and after the keyboard retry. */
  openWaitMs: number;
  retryWaitMs: number;
  /** Wait for typeahead suggestions. */
  suggestWaitMs: number;
  /** Wait for a stale populated popup to drain before a trigger without aria-controls is opened. */
  staleDrainMs: number;
  /** Wait after one activation gesture before the trigger is read back. */
  activateMs: number;
  /** A tel input that detects its country is given this long for the widget's own dial-code seed before it is typed over. */
  seedSettleMs: number;
  /** Per-character gap when an address or places box is typed. */
  typeGapMs: number;
  /** Dependent selects, native cascades and the cascade-completion pass. */
  dependentWaitMs: number;
  cascadeGapMs: number;
  /** Switch and role=radio widgets: clicks and the gap between them. */
  toggleClicks: number;
  toggleGapMs: number;
  /** Search-backed widgets: how long an async search may take, and how long a commit may take to show. */
  searchWaitMs: number;
  commitWaitMs: number;
  /** A paginated picklist: wait to open and per filter. */
  picklistOpenMs: number;
  picklistFilterMs: number;
  /** Prescan of a closed listbox: read timeout and stale-node drain. */
  prescanReadMs: number;
  prescanDrainMs: number;
  /** Restore pass: poll, idle exit after the first restore, and the hard cap. */
  restorePollMs: number;
  restoreIdleMs: number;
  restoreCapMs: number;
  /** Refill stage: settle before each look. Tail guard: how long it watches for a late remount. */
  refillSettleMs: number;
  tailCapMs: number;
  /** Watchdog: tick, the lateness of one tick that is a stall, the long-task window, its total and count, and the quiet time after a report. */
  watchdogTickMs: number;
  stallLateMs: number;
  stallWindowMs: number;
  stallLongTotalMs: number;
  stallLongRun: number;
  stallCooldownMs: number;
  /** An embedded application frame that shows only an overview: how long its form may take to appear, and how often it is looked at. */
  revealWaitMs: number;
  revealPollMs: number;
  /** A search prompt whose results arrive after Enter: how long they may take and how often they are looked at. */
  promptWaitMs: number;
  promptPollMs: number;
  /** A committed pill is read again this long after the commit; the responsive dialog's Done is pressed this long after the leaf. */
  pillRecheckMs: number;
  doneMs: number;
  /** One step of the scroll through a virtualized list. */
  scrollStepMs: number;
  /** A date picker option is waited for this long, and a commit that stuck nowhere is repeated after the hydration pause. */
  pickerWaitMs: number;
  pickerPollMs: number;
  hydrateRetryMs: number;
  /** A lazily rendered question legend is looked at this often, ten times at most. */
  legendPollMs: number;
  /** Oracle: how often a select's rows are looked at, how long a re-rendering list is watched after a seed, the address list poll and its ceiling, and the cascade wait. */
  oracleRowPollMs: number;
  oracleLateMs: number;
  oraclePollMs: number;
  oracleAddressWaitMs: number;
  oracleCascadeWaitMs: number;
  /** After an Oracle country commits, the address master is waited for this long. */
  oracleGateWaitMs: number;
  /** The Oracle country's own row is waited for this long once the name is typed: the block waits on it. */
  oracleCountryWaitMs: number;
  /** Ashby: a just-triggered save is given this long to reach the queue, and the queue is waited for at most this long. */
  ashbyGraceMs: number;
  ashbyDrainMs: number;
  /** Ashby: a typed answer is saved after the page's own debounce (500 ms measured); an unsaved text box is waited on this long. */
  ashbyDebounceMs: number;
  /** iCIMS: how often a list is looked at, and how long an AJAX list and a search list may take. */
  icimsPollMs: number;
  icimsAjaxWaitMs: number;
  icimsSearchWaitMs: number;
  /** SuccessFactors: how often the page is looked at, and how long expanding or finding the upload trigger may take. */
  sfPollMs: number;
  sfExpandWaitMs: number;
  /** Lever: the gap between keys of the location search, how long its suggestions may take, and the cap while its loading row shows. */
  leverKeyMs: number;
  leverSuggestWaitMs: number;
  leverSearchCapMs: number;
  /** SmartRecruiters: every await on a component is bounded; some validators never settle. */
  srAwaitMs: number;
  /** SmartRecruiters entries: how long an editor may take to mount and a Save to show its card, and the wait for a late parse. */
  srEditorWaitMs: number;
  srLateMs: number;
  /** Repeated rows: how long an Add or a remove may take to show, the pause before a confirmation, the country morph, and the re-assert sweep. */
  sectionAddWaitMs: number;
  sectionRemoveWaitMs: number;
  sectionConfirmMs: number;
  rowCountryMs: number;
  sweepPollMs: number;
  sweepIdleMs: number;
  sweepCapMs: number;
  /** react-widgets: a fresh row's list is given this long after it was opened; Paylocity text is verified only after this long. */
  rwOpenMs: number;
  pctyVerifyMs: number;
  /** Paylocity's select shows its menu about 400 ms after ArrowDown. */
  pctyMenuMs: number;
  /** After a fill: a select or a box that ticks is captured this long after it changed, a custom listbox is read this often, and a change counts as a person's when a trusted input came within this window. */
  correctionDelayMs: number;
  /** After a fill: a control the page shows later is looked at once the page has been quiet this long. */
  revealDebounceMs: number;
  listboxPollMs: number;
  trustedWindowMs: number;
}

export const DEFAULT_TIMING: Timing = {
  settleMs: 80,
  listboxSettleMs: 280,
  emptyRetryMs: 200,
  selectRecommitMs: 250,
  quietMs: 300,
  quietCapMs: 1500,
  fieldBudgetMs: 15_000,
  answerWindowMs: 19_000,
  passSettleMs: 800,
  parseQuietMs: 2000,
  parseNoChangeMs: 3000,
  parseCapMs: 60_000,
  parsePollMs: 250,
  dayforceQuietMs: 1500,
  dayforceParseCapMs: 35_000,
  tapMs: 40,
  pollMs: 50,
  openWaitMs: 1500,
  retryWaitMs: 1800,
  suggestWaitMs: 2000,
  staleDrainMs: 400,
  activateMs: 180,
  seedSettleMs: 60,
  typeGapMs: 45,
  dependentWaitMs: 5000,
  cascadeGapMs: 120,
  toggleClicks: 5,
  toggleGapMs: 50,
  searchWaitMs: 8000,
  commitWaitMs: 2000,
  picklistOpenMs: 2500,
  picklistFilterMs: 3000,
  prescanReadMs: 600,
  prescanDrainMs: 300,
  restorePollMs: 400,
  restoreIdleMs: 40_000,
  restoreCapMs: 90_000,
  refillSettleMs: 400,
  tailCapMs: 15_000,
  watchdogTickMs: 500,
  stallLateMs: 2000,
  stallWindowMs: 5000,
  stallLongTotalMs: 1500,
  stallLongRun: 3,
  stallCooldownMs: 5000,
  revealWaitMs: 8000,
  revealPollMs: 200,
  promptWaitMs: 6000,
  promptPollMs: 150,
  pillRecheckMs: 300,
  doneMs: 150,
  scrollStepMs: 20,
  pickerWaitMs: 480,
  pickerPollMs: 80,
  hydrateRetryMs: 160,
  legendPollMs: 80,
  oracleRowPollMs: 80,
  oracleLateMs: 1000,
  oraclePollMs: 250,
  oracleAddressWaitMs: 8000,
  oracleCascadeWaitMs: 8000,
  oracleGateWaitMs: 4000,
  oracleCountryWaitMs: 6000,
  ashbyGraceMs: 150,
  ashbyDrainMs: 10_000,
  ashbyDebounceMs: 1500,
  icimsPollMs: 120,
  icimsAjaxWaitMs: 600,
  icimsSearchWaitMs: 720,
  sfPollMs: 120,
  sfExpandWaitMs: 1440,
  leverKeyMs: 150,
  leverSuggestWaitMs: 5000,
  leverSearchCapMs: 20_000,
  srAwaitMs: 2500,
  srEditorWaitMs: 2500,
  srLateMs: 6000,
  sectionAddWaitMs: 2500,
  sectionRemoveWaitMs: 1800,
  sectionConfirmMs: 150,
  rowCountryMs: 800,
  sweepPollMs: 2000,
  sweepIdleMs: 30_000,
  sweepCapMs: 90_000,
  rwOpenMs: 300,
  pctyVerifyMs: 500,
  pctyMenuMs: 1500,
  correctionDelayMs: 2000,
  revealDebounceMs: 400,
  listboxPollMs: 750,
  trustedWindowMs: 5000,
};

/** Thrown at a cancellation point once the run was stopped. */
export class StopSignal extends Error {
  constructor(readonly reason: "cancelled" | "deadline") {
    super(`stopped: ${reason}`);
    this.name = "StopSignal";
  }
}

export interface WriteCtx {
  /** The site table's id for the document, when a driver needs it. */
  site?: string | null;
  /** The channel to the main-world helper, when the host installed one. */
  main?: MainWorld | null;
  doc: Document;
  timing: Timing;
  /** Resolves early when the run is stopped. */
  sleep(ms: number): Promise<void>;
  /** Throws StopSignal when the run was stopped; waits while paused. */
  checkpoint(): Promise<void>;
  /** The run's clock, which stands still while it is paused; a context nothing pauses has none. */
  active?(): number;
  now(): Date;
  log(event: string, data?: Record<string, unknown>): void;
}

/** The time a budget is measured on: paused time is no part of it. */
export const activeNow = (ctx: WriteCtx): number => ctx.active?.() ?? Date.now();

/** How soon a timer on a clock that stood still looks again. */
const RECHECK_MS = 20;

/** Calls `done` once `now()` reaches `end`, however long the clock stood still on the way; returns its cancel. */
export function timerOn(now: () => number, end: number, done: () => void): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const arm = (floor: number) => {
    timer = setTimeout(() => (now() >= end ? done() : arm(RECHECK_MS)), Math.max(end - now(), floor));
  };
  arm(0);
  return () => clearTimeout(timer);
}

/**
 * Polls until the probe returns something, the time is up or the run stops.
 * A probe that throws counts as nothing yet.
 */
export async function waitFor<T>(ctx: WriteCtx, probe: () => T | null | undefined | false, ms: number, pollMs = ctx.timing.pollMs): Promise<T | null> {
  const end = activeNow(ctx) + ms;
  for (;;) {
    try {
      const v = probe();
      if (v) return v;
    } catch {
      /* the page is mid re-render */
    }
    const left = end - activeNow(ctx);
    if (left <= 0) return null;
    await ctx.checkpoint();
    await ctx.sleep(Math.min(pollMs, left));
  }
}
