// Messages between the extension's contexts. Every one goes to the service
// worker, which alone holds the token and calls the API.
import type { PriorSubmission, ResumeEntry, ResumeRank } from "../../../web/src/wire";
import type { JobContext, PageReading } from "../../../engine/src/capture";

export type { JobContext, PageReading };

export interface Failure {
  code: string;
  message: string;
  /** Seconds, for a rate-limited answer. */
  retryAfter?: number;
  /** The server's machine-readable reason, when it gave one. */
  reason?: string;
}

export type Reply<T> = { ok: true; data: T } | { ok: false; error: Failure };

export type Connection = "connected" | "unpaired" | "offline";

export interface Prefs {
  /** The "Edit with AI" master switch. */
  editWithAI: boolean;
  /** The "Answer sign-in sync requests" switch. */
  answerSync: boolean;
  /** Open the panel by itself once per tab and host on a supported site. */
  autoOpen: boolean;
}

export const DEFAULT_PREFS: Prefs = { editWithAI: true, answerSync: true, autoOpen: true };

export interface BuildInfo {
  stamp: string;
  version: string;
}

export interface StateView {
  connection: Connection;
  address: string;
  extension: BuildInfo;
  /** The build the application expects, when it answered. */
  application?: BuildInfo;
  /** Seconds until a throttled pairing may try again. */
  claimWait?: number;
  prefs: Prefs;
  /** True when this browser carries the managed browser's marker cookie. */
  managed: boolean;
}

/** Content script to worker. */
export type Request =
  | { kind: "state" }
  | { kind: "health" }
  | { kind: "pair.claim"; code: string }
  | { kind: "pair.token"; token: string }
  | { kind: "pair.forget" }
  | { kind: "settings.address"; address: string }
  | { kind: "settings.prefs"; prefs: Partial<Prefs> }
  | { kind: "site.get"; host: string }
  | { kind: "site.set"; host: string; off: boolean }
  | { kind: "loader.inject" }
  | { kind: "panel.show"; tabId: number; toggle?: boolean }
  | { kind: "ui.get"; host: string }
  | ({ kind: "ui.set"; host: string } & UiState)
  | { kind: "fill.start"; reading: PageReading; doc: string }
  | { kind: "fill.pause" }
  | { kind: "fill.resume" }
  | { kind: "fill.stop" }
  | { kind: "fill.status" }
  | { kind: "fill.dismiss" }
  | { kind: "step.advance" }
  | { kind: "engine.match"; payload: string }
  | { kind: "engine.resume"; payload: string }
  | { kind: "engine.terminal"; payload: string }
  | { kind: "engine.edit"; edit: UserEdit }
  | { kind: "sync.host"; host: string; tabId?: number }
  | { kind: "flags.get"; host: string }
  | { kind: "analyze.lookup"; url: string }
  | { kind: "analyze.start"; capture: Capture; force?: boolean }
  | { kind: "analyze.status"; runId: string }
  | { kind: "analyze.track"; capture: Capture }
  | { kind: "compose.run"; key: string; question: string; maxLength?: number; posting: ComposePosting; current?: string; instruction?: string }
  | { kind: "compose.cancel"; key: string }
  | { kind: "context.resolve"; reading: PageReading }
  | { kind: "context.pin"; reading: PageReading; pin: boolean }
  | { kind: "resume.list" }
  | { kind: "resume.rank"; posting: PostingHint }
  | { kind: "resume.pin"; page: string; id: string | null }
  | { kind: "report.field"; report: FieldReport }
  | { kind: "banner.get"; url: string }
  | { kind: "docs.get"; url: string }
  | { kind: "docs.start"; candidacyId: number; document: "resume" | "cover-letter" }
  | { kind: "docs.dismiss" }
  | { kind: "docs.run"; runId: string }
  | { kind: "banner.dismiss"; key: string }
  | { kind: "hook.install" }
  | { kind: "signin.resolve" }
  | { kind: "signin.settings" }
  | { kind: "otp.latest"; since: string }
  | { kind: "signin.link"; since: string }
  | { kind: "submission.record"; entry: SubmissionEntry }
  | { kind: "press.claim"; company?: string; role?: string }
  | { kind: "press.report"; claimId: string; entry: SubmissionEntry };

/** Where the panel keeps the token of its document, in the page's isolated world. */
export const DOC_KEY = "__applysmithDoc";

export type FillPhase = "idle" | "starting" | "running" | "paused" | "done" | "error" | "stopped";

export interface FrameView {
  frameId: string;
  /** Origin and path only. */
  url: string;
  outcome: string;
  controls?: number;
  reason?: string;
}

export interface RowView {
  label: string;
  /** Which part of the engine wrote it: the answer bank, the profile pass or an attestation. */
  lane?: string;
  outcome: string;
  reason?: string;
  required: boolean;
  review: boolean;
  /** The control, only when it is in the top frame, where the panel can show it. */
  selector?: string;
}

export interface FillView {
  phase: FillPhase;
  runId?: string;
  message?: string;
  elapsedMs?: number;
  frames: FrameView[];
  counts?: { filled: number; prefilled: number; review: number; abstained: number; failed: number; skipped: number; requiredUnfilled: number };
  rows?: RowView[];
  drafts?: number;
  stall?: string;
  /** Frames the fill could not reach. */
  unreachable?: number;
  notAnApplication?: boolean;
  matchUnavailable?: boolean;
}

export interface PostingHint {
  company?: string;
  title?: string;
  url?: string;
  jdText?: string;
  location?: string;
}

/** A value the user committed to a control themselves, seen by the bridge. */
export interface UserEdit {
  selector: string;
  label: string;
  /** The engine's row kind. */
  kind: string;
  value: string;
  inputType?: string;
  autocomplete?: string;
  /** The captions of the form sections around the control, outermost first. */
  section?: string[];
  /** The control's option labels, as the engine's inspection samples them for an event. */
  options?: string[];
}

export interface UiState {
  pos?: { x: number; y: number };
  open?: boolean;
  tab?: string;
  /** The panel already opened by itself in this tab on this host. */
  auto?: boolean;
}

/** Worker to a tab's panel. */
export type PanelCommand = { kind: "panel.toggle" } | { kind: "panel.open" };

export function normalizeHost(host: string): string {
  return host.trim().toLowerCase().replace(/^www\./, "");
}

/** What the page-side widgets may know about the switches, without a network call. */
export interface Flags {
  off: boolean;
  editWithAI: boolean;
}

export type DetectionLabel = "job_posting" | "likely_job_posting" | "not_job_posting";

/** The page as the Analyze tab read it. */
export interface Capture {
  /** Origin, path and query as the person sees it; the server reduces it. */
  url: string;
  title: string;
  /** At most 200,000 characters. */
  text: string;
  company?: string;
  role?: string;
  location?: string;
  /** True when the page carries structured job-posting data. */
  structured: boolean;
  detection: { label: DetectionLabel; confidence: number; signals: string[] };
  /** Wording on the page that says the posting is closed. */
  closed?: string;
}

export interface ReportView {
  number: number;
  company: string;
  role: string;
  score?: number;
  recommendation: string;
}

export interface LookupView {
  found: boolean;
  identityKey?: string;
  status?: string;
  /** The date the tracker holds as the application date. */
  appliedAt?: string;
  /** The posting's submitted ledger row: a confirmed one over an unconfirmed one, then the most recent. */
  submission?: PriorSubmission;
  report?: ReportView;
  /** The tracker row, for re-evaluation and the add button. */
  candidacyId?: number;
  /** The tracker row's evaluated report. */
  reportNumber?: number;
}

/** The report the Documents section offers documents for. */
export interface DocsView {
  show: boolean;
  reportNumber?: number;
  candidacyId?: number;
  /** The tab carried the record here from another page (the posting's Apply hop): it shows only on an application form. */
  inherited?: boolean;
}

/** A document run docs.start answers with; already marks one that was working before the press. */
export interface DocsStarted {
  runId: string;
  already?: boolean;
}

export interface EvaluationView {
  runId: string;
  /** The server's run phase, or "done" once the stream ended. */
  phase: string;
  done: boolean;
  outcome?: string;
  reason?: string;
  explanation?: string;
  review?: boolean;
  report?: ReportView;
}

export interface ComposePosting {
  company?: string;
  role?: string;
  description?: string;
  applicationUrl?: string;
}

export interface ComposeView {
  outcome: "filled-model" | "abstained";
  answer?: string;
  reason?: string;
}

/** A roster entry as the application lists it; never the file's bytes. */
export type ResumeVariant = ResumeEntry;

/** Best first; each reason names the matched words, or "default" for the chosen entry when none matched. */
export type ResumeRankView = ResumeRank;

export interface ResumeState {
  resumes: ResumeVariant[];
  /** The id the page's pin names, when one is set. */
  pinned?: string;
}

/** What the picker sends for one control the person pointed at. It never holds the control's value. */
export interface FieldReport {
  question: string;
  controlKind: string;
  /** At most 25 sample options. */
  options: string[];
  /** At most 500 characters. */
  note: string;
  tag: string;
  role: string;
  accessibleName: string;
  path: string;
  nearbyText: string;
  /** Origin and path only. */
  url: string;
  frameUrl: string;
  shadowDepth: number;
  snippet: string;
  inputType?: string;
}

/** One observed submission. It carries the posting, never anything typed into the form. */
export interface SubmissionEntry {
  entryId: string;
  /** Origin and path only. */
  url: string;
  host: string;
  company?: string;
  role?: string;
  title: string;
  /** ISO time of the click. */
  at: string;
  confirmed: boolean;
  /** The press claim the step walker held when it pressed the final control. */
  claimId?: string;
}

/**
 * The server's answer to a request to press the final submit control. Only
 * `granted: true` with a claim id lets the walker press; everything else
 * carries the reason to show the person.
 */
export type PressDecision = { granted: true; claimId: string } | { granted: false; message: string };

export interface BannerView {
  show: boolean;
  /** The application date: the tracker's YYYY-MM-DD, or the quoted submission's ISO time. */
  date?: string;
  key?: string;
  /** Set when the banner quotes a submission of the ledger rather than the tracker's status. */
  submission?: {
    confirmed: boolean;
    /** The application chain submitted it. */
    agent: boolean;
    /** A record this extension did not see being made: it came from the ledger alone. */
    fromLedger: boolean;
  };
}
