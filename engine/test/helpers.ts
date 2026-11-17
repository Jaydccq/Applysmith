// Clean-room: docs/provenance.md#CR-ENGINE
import { afterEach, vi } from "vitest";
import { AutofillEngine } from "../src/engine";
import { Run } from "../src/run";
import { HOLDER } from "../src/mainworld/helper";
import type { Win } from "../src/dom";
import type { Timing } from "../src/env";
import type { Host } from "../src/host";
import type {
  ApplicantSnapshot,
  MatchRequest,
  MatchResponse,
  Resolution,
  ResumePayload,
  ResumeRequest,
  StartArgs,
  StatusReport,
  Summary,
  TerminalNotice,
} from "../src/protocol";

export const FAST: Partial<Timing> = {
  settleMs: 1,
  emptyRetryMs: 3,
  selectRecommitMs: 2,
  quietMs: 5,
  quietCapMs: 30,
  passSettleMs: 1,
  parseQuietMs: 20,
  parseNoChangeMs: 20,
  parsePollMs: 5,
  dayforceQuietMs: 40,
  dayforceParseCapMs: 300,
  tapMs: 1,
  pollMs: 2,
  openWaitMs: 60,
  retryWaitMs: 60,
  suggestWaitMs: 80,
  staleDrainMs: 20,
  activateMs: 2,
  seedSettleMs: 5,
  typeGapMs: 0,
  dependentWaitMs: 80,
  cascadeGapMs: 1,
  toggleGapMs: 2,
  searchWaitMs: 100,
  commitWaitMs: 40,
  picklistOpenMs: 60,
  picklistFilterMs: 80,
  prescanReadMs: 40,
  prescanDrainMs: 20,
  restorePollMs: 20,
  restoreIdleMs: 80,
  restoreCapMs: 250,
  refillSettleMs: 5,
  tailCapMs: 150,
  watchdogTickMs: 25,
  revealWaitMs: 150,
  revealPollMs: 10,
  promptWaitMs: 150,
  promptPollMs: 5,
  pillRecheckMs: 5,
  doneMs: 2,
  scrollStepMs: 1,
  pickerWaitMs: 8,
  pickerPollMs: 2,
  hydrateRetryMs: 3,
  legendPollMs: 3,
  oracleRowPollMs: 4,
  oracleLateMs: 60,
  oraclePollMs: 10,
  oracleAddressWaitMs: 400,
  oracleCascadeWaitMs: 150,
  oracleGateWaitMs: 60,
  oracleCountryWaitMs: 400,
  ashbyGraceMs: 5,
  ashbyDrainMs: 400,
  ashbyDebounceMs: 200,
  icimsPollMs: 4,
  icimsAjaxWaitMs: 80,
  icimsSearchWaitMs: 90,
  sfPollMs: 4,
  sfExpandWaitMs: 60,
  leverKeyMs: 0,
  leverSuggestWaitMs: 100,
  leverSearchCapMs: 2000,
  srAwaitMs: 40,
  srEditorWaitMs: 100,
  srLateMs: 40,
  sectionAddWaitMs: 80,
  sectionRemoveWaitMs: 80,
  sectionConfirmMs: 3,
  rowCountryMs: 5,
  sweepPollMs: 10,
  sweepIdleMs: 120,
  sweepCapMs: 400,
  rwOpenMs: 4,
  pctyVerifyMs: 6,
  pctyMenuMs: 120,
  correctionDelayMs: 40,
  listboxPollMs: 30,
  trustedWindowMs: 120,
  // A loaded machine must not read as a stalled page in an unrelated test.
  stallLateMs: 60_000,
  stallLongTotalMs: 60_000,
};

export const ADA: ApplicantSnapshot = {
  candidate: {
    fullName: "Ada Example",
    firstName: "Ada",
    lastName: "Example",
    email: "ada@example.com",
    phone: "+1 555 0100",
    phoneCountryCode: "+1",
    address: { line1: "1 Example Street", city: "Durham", state: "NC", postalCode: "27701", country: "United States" },
  },
  links: { linkedin: "https://www.linkedin.com/in/ada-example", github: "https://github.com/ada-example" },
};

/** An unrelated second field, so a one-question fixture still counts as an application form. */
export const PAD = `<label for="pad">Referral code</label><input id="pad" value="REF-1">`;

export function mount(html: string, title = "Careers"): void {
  document.title = title;
  document.body.innerHTML = html;
}

export interface FakeHost extends Host {
  matchRequests: MatchRequest[];
  resumeRequests: ResumeRequest[];
  terminals: TerminalNotice[];
  /** Answers by question text (normalized lower case) or by selector. */
  table: Record<string, Partial<Resolution>>;
  failMatch?: Error;
  failTerminal?: boolean;
  resume: (req: ResumeRequest) => Promise<ResumePayload | null>;
}

export const PDF: ResumePayload = { fileName: "ada-example.pdf", mediaType: "application/pdf", contentBase64: btoa("%PDF-1.4 example") };

export function fakeHost(table: FakeHost["table"] = {}): FakeHost {
  const host: FakeHost = {
    matchRequests: [],
    resumeRequests: [],
    terminals: [],
    table,
    async match(req: MatchRequest): Promise<MatchResponse> {
      host.matchRequests.push(req);
      if (host.failMatch) throw host.failMatch;
      return {
        results: req.questions.map((q) => {
          const hit = host.table[q.text.toLowerCase()] ?? host.table[q.selector];
          return { selector: q.selector, outcome: "abstained", ...(hit ? {} : { reason: "no-entry", clientMayOverwrite: true }), ...hit } as Resolution;
        }),
      };
    },
    async resume(req: ResumeRequest) {
      host.resumeRequests.push(req);
      return req.document === "resume" ? PDF : null;
    },
    async terminal(n: TerminalNotice) {
      host.terminals.push(n);
      if (host.failTerminal) throw new Error("terminal binding down");
    },
  };
  return host;
}

/** An event marked as the browser marks a person's own. */
export function trusted<T extends Event>(ev: T): T {
  Object.defineProperty(ev, "isTrusted", { value: true });
  return ev;
}

/** Dispatches a trusted, bubbling and composed event of this type at the element. */
export function fireTrusted(el: Element, type: string): void {
  el.dispatchEvent(trusted(new Event(type, { bubbles: true, composed: true })));
}

/** Disposes the main-world helper a test installed, so the next test installs its own. */
export const uninstall = () => (window as unknown as Record<symbol, { dispose(): void } | undefined>)[HOLDER]?.dispose();

/** Sets the page address, as a site's own form would carry it. */
export const setUrl = (u: string) => (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL(u);
const START_URL = window.location.href;

/** Engines made in a test; the restore pass of one must not keep working on the next test's page. A site address a test set ends with it, pass or fail. */
const made: AutofillEngine[] = [];
afterEach(() => {
  for (const e of made.splice(0)) e.dispose();
  if (window.location.href !== START_URL) setUrl(START_URL);
});

export function makeEngine(host: Host, timing: Partial<Timing> = FAST): AutofillEngine {
  const engine = new AutofillEngine({ host, win: window as unknown as Win, timing: { ...FAST, ...timing } });
  made.push(engine);
  return engine;
}

export const tick = (ms = 5) => new Promise<void>((r) => setTimeout(r, ms));

/** Waits on a condition. The cap only ends a wait that can never succeed, so it outlasts a loaded machine. */
export async function until(cond: () => boolean, ms = 3000): Promise<void> {
  const t0 = Date.now();
  const cap = Math.max(ms * 5, 15_000);
  while (!cond()) {
    if (Date.now() - t0 > cap) throw new Error("timed out waiting for condition");
    await tick(2);
  }
}

export async function runFill(engine: AutofillEngine, args: Partial<StartArgs> = {}, waitMs = 15_000): Promise<StatusReport> {
  const r = engine.start({ applicant: ADA, ...args });
  if (!r.started) throw new Error(`not started: ${r.reason}`);
  await until(() => !["running", "paused"].includes(engine.status().state), waitMs);
  return engine.status();
}

/** A bare run over the current document with the test timings, for a stage called on its own. */
export function newRun(): Run {
  return new Run(
    { id: "t", applicant: ADA, posting: {}, acceptLegalDeclarations: false, edits: noEdits(), pageDeadlineMs: 60_000, prepassDeadlineMs: 60_000, profileApplyBudgetMs: 60_000 },
    { ...DEFAULT_TIMING, ...FAST },
    document,
  );
}

/** Advances vi's fake clock in small steps until the work settles: what a test measures in time is the engine's, not the machine's. */
export async function onFakeClock<T>(work: Promise<T>): Promise<T> {
  let settled = false;
  const out = work.finally(() => (settled = true));
  for (let i = 0; i < 20_000 && !settled; i++) await vi.advanceTimersByTimeAsync(5);
  if (!settled) throw new Error("the work did not settle in 100 s of the fake clock");
  return out;
}

/** A fill on vi's fake clock, advanced until the run ends. */
export async function runFillFaked(engine: AutofillEngine, args: Partial<StartArgs> = {}): Promise<StatusReport> {
  const r = engine.start({ applicant: ADA, ...args });
  if (!r.started) throw new Error(`not started: ${r.reason}`);
  await onFakeClock(until(() => !["running", "paused"].includes(engine.status().state), 100_000));
  return engine.status();
}

export function summaryOf(s: StatusReport): Summary {
  if (s.state !== "done" && s.state !== "stopped") throw new Error(`no summary in state ${s.state}`);
  return s.summary;
}

export function rowFor(s: Summary, labelPart: string) {
  const row = s.rows.find((r) => r.label.toLowerCase().includes(labelPart.toLowerCase()));
  if (!row) throw new Error(`no row for ${labelPart}: ${s.rows.map((r) => r.label).join(" | ")}`);
  return row;
}

export const byId = <T extends HTMLElement = HTMLInputElement>(id: string): T => document.getElementById(id) as T;

import { applyValue, type ApplyResult, type Desired } from "../src/apply";
import { DEFAULT_TIMING, type WriteCtx } from "../src/env";
import { UserEdits } from "../src/restore";
import { scanControls, type ControlInfo } from "../src/scan";

export function ctx(timing: Partial<Timing> = {}): WriteCtx {
  return {
    doc: document,
    timing: { ...DEFAULT_TIMING, ...FAST, ...timing },
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    checkpoint: async () => {},
    now: () => new Date(2026, 9, 11, 12, 0, 0),
    log: () => {},
  };
}

/** A tracker that never started, so no control counts as edited. */
export const noEdits = (): UserEdits => new UserEdits(document);

export function infoOf(id: string): ControlInfo {
  const target = document.getElementById(id);
  const info = scanControls(document).find((i) => i.el.id === id || i.members.some((m) => m.id === id) || (!!target && i.el.contains(target)));
  if (!info) throw new Error(`no control ${id}`);
  return info;
}

export async function write(
  id: string,
  value: string,
  extra: Partial<Desired> = {},
  mode: "keep" | "replace" = "replace",
  written = new Map<string, string>(),
  acceptLegalDeclarations = false,
  timing: Partial<Timing> = {},
): Promise<ApplyResult> {
  return applyValue(infoOf(id), { value, replaceDefault: false, ...extra }, { ctx: ctx(timing), written, mode, acceptLegalDeclarations, edits: noEdits() });
}
