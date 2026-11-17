// Clean-room: docs/provenance.md#CR-ENGINE
// Wire contract between the in-page engine and its host. The Go side (and the
// extension) implement the other end of exactly these shapes.

/** Names of the three bindings the host registers in the engine's isolated world. */
export const BINDINGS = {
  match: "__ajMatch",
  resume: "__ajResume",
  terminal: "__ajTerminal",
} as const;

/** A binding is called with one JSON string and resolves to one JSON string. */
export type BindingReply<T> = { ok: true; result: T } | { ok: false; error: string };

export const MAX_QUESTIONS_PER_REQUEST = 300;
export const MAX_OPTIONS_PER_QUESTION = 100;
export const MAX_REQUEST_BYTES = 256 * 1024;
export const MAX_RESUME_BYTES = 10 * 1024 * 1024;

export type QuestionKind = "select" | "radio-group" | "checkbox" | "text" | "custom-listbox";

export type InputType =
  | "text"
  | "long_text"
  | "number"
  | "date"
  | "email"
  | "tel"
  | "url"
  | "single_select"
  | "multi_select"
  | "yes_no";

export interface WireQuestion {
  selector: string;
  text: string;
  /** Outermost first. */
  section?: string[];
  kind: QuestionKind;
  inputType: InputType;
  /**
   * The options a person can choose, in page order. Empty for a control with
   * more than MAX_OPTIONS_PER_QUESTION options and for a search widget. A lone
   * checkbox or switch carries exactly one option: its own label, which is how
   * the matcher tells it from a group. Hidden, off-screen and decoy controls
   * never become a question: the wire has no hidden flag, so the engine
   * filters them before it sends anything.
   */
  options: string[];
  required: boolean;
  maxLength?: number;
  /**
   * The text under the question that its text does not take (Oklo: what the options A, B and C stand for), at most 4000
   * characters.
   */
  description?: string;
}

export interface PostingContext {
  company?: string;
  title?: string;
  url?: string;
  jdText?: string;
  location?: string;
}

export interface MatchRequest {
  runId: string;
  host: string;
  posting: PostingContext;
  questions: WireQuestion[];
  /** Present (and true) only when a cover letter was observed or attached. */
  coverLetterAttached?: true;
  /** Present (and true) only when a résumé was observed in its slot or attached by this run. */
  resumeAttached?: true;
}

export type MatchOutcome = "filled" | "filled-review" | "filled-model" | "abstained";

export interface Resolution {
  selector: string;
  outcome: MatchOutcome;
  value?: string;
  optionLabel?: string;
  /** For a multi-select checkbox group. */
  optionLabels?: string[];
  /** For a list the matcher was asked without options: the answer's ranked stand-ins, the first a search holds taken when it holds none of the answer. */
  alternatives?: string[];
  /** Matcher abstain reason, from the answer bank's closed set (no-entry, section-policy, context-policy, ask-me, ...). */
  reason?: string;
  /** On an abstain: true when the profile pass may still fill the control (the matcher found no entry, or was unsure). */
  clientMayOverwrite?: boolean;
  terminal?: boolean;
  /** True once the user accepted a composed draft or the apply chain's grounding checks passed. */
  reviewed?: boolean;
  /** The host marks a plural label payload as a multi-select binding; without it a single-answer control refuses one. */
  multiSelectBinding?: boolean;
  /**
   * The answer is one the user wrote into their own bank for this question, as written: not an acknowledgement
   * entry, a derived or form-text rule, or a model. The consent rules let it apply as written.
   */
  ownAnswer?: boolean;
  /**
   * The host asks for a box this run wrote to be written again, a choice included: the page now holds another value
   * than this run wrote there (a site's import that landed after the fill), or a review doubts the run's value and the
   * fix corrects it. The run's own earlier write does not keep the box.
   */
  overwrite?: boolean;
  /**
   * The user's own words name this value for a box that already holds another: the host writes it over whatever the
   * page holds, a site's prefill or this run's own write.
   */
  replace?: boolean;
}

export interface MatchResponse {
  results: Resolution[];
}

export interface ResumeRequest {
  runId: string;
  document: "resume" | "cover-letter" | "transcript";
  /** "text": the cover letter's plain text (UTF-8, text/plain), for a box the page labels as the letter. */
  format?: "text";
  /** That box's maxlength; the host refuses a longer letter rather than cut it. */
  maxLength?: number;
}

export interface ResumePayload {
  fileName: string;
  mediaType: string;
  contentBase64: string;
}

export interface TerminalNotice {
  runId: string;
  state: "done" | "error" | "stopped";
  reason?: StopReason;
  message?: string;
  summary?: Summary;
}

export type StopReason = "cancelled" | "deadline";

export interface ApplicantAddress {
  line1?: string;
  line2?: string;
  city?: string;
  county?: string;
  state?: string;
  postalCode?: string;
  country?: string;
}

/** The response of GET /api/autofill/applicant. Held in engine memory only, from one fill until the next. */
export interface ApplicantSnapshot {
  candidate: {
    fullName?: string;
    firstName?: string;
    middleName?: string;
    lastName?: string;
    preferredName?: string;
    email?: string;
    phone?: string;
    /** The phone's country calling code ("+1"), split off on the server; absent for a number written without one. */
    phoneCountryCode?: string;
    address?: ApplicantAddress;
  };
  links?: { linkedin?: string; github?: string; portfolio?: string };
  history?: { employment?: Record<string, unknown>[]; education?: Record<string, unknown>[] };
  /** Names of fields holding the ask-me marker, e.g. "address.line1". */
  askMe?: string[];
}

export interface StartArgs {
  runId?: string;
  applicant: ApplicantSnapshot;
  posting?: PostingContext;
  acceptLegalDeclarations?: boolean;
  /** Selectors of the controls the user changed by hand before this fill: they are the user's, empty or not. */
  edited?: string[];
  pageDeadlineMs?: number;
  prepassDeadlineMs?: number;
  profileApplyBudgetMs?: number;
  /** Whether the engine watches the person's changes after the fill for `drainEvents` (default true); a host that captures edits itself passes false. */
  watchCorrections?: boolean;
}

export interface StartReply {
  started: boolean;
  reason?: string;
  runId?: string;
}

export type Lane = "bank" | "profile" | "attestation";
export type RowOutcome = "filled" | "prefilled" | "skipped" | "abstained" | "failed";
export type RowKind =
  | "text"
  | "textarea"
  | "select"
  | "radio-group"
  | "checkbox"
  | "file"
  | "contenteditable"
  | "custom-listbox";

export type RowReason =
  | "option-not-found"
  | "listbox-not-opened"
  | "write-not-committed"
  | "control-detached"
  | "binding-unavailable"
  | "unsafe-click"
  | "readonly"
  | "hidden"
  | "format-not-accepted"
  | "out-of-range"
  | "terminal-abstain"
  | "awaiting-review"
  | "needs-native-upload"
  | "timeout"
  | "page-authored-value"
  | "existing-value"
  | "no-lane-claimed"
  | "driver-unavailable"
  | "ask-me"
  | "no-answer"
  | "no-match"
  | "section-policy"
  | "context-policy"
  | "duplicate-link"
  | "self-id-policy"
  | "marketing-opt-in"
  | "user-edited"
  | "legal-declaration-off"
  | "verify-mismatch"
  | "ashby-save-lost";

export interface Row {
  selector: string;
  label: string;
  section: string[];
  lane: Lane;
  kind: RowKind;
  required: boolean;
  outcome: RowOutcome;
  review: boolean;
  reason?: RowReason;
  /** The matcher's own abstain reason when the row reason is terminal-abstain. */
  matcherReason?: string;
  /** Value read back from the page; absent for password, file and withheld controls. */
  value?: string;
  /** For a file input: its file name only. */
  fileName?: string;
  optionLabel?: string;
  /** An unanswered choice's options (at most 100), so a later pass can answer it; none for a lone checkbox. */
  options?: string[];
  /** The site's own message when it refused the value written (format-not-accepted), or the bound it refused (out-of-range). */
  detail?: string;
}

export interface Draft {
  selector: string;
  label: string;
  text: string;
}

export type LearningKind = "uncertainty" | "manual-fill" | "unsafe-control" | "frame-unreachable";

/** What the engine tells the host to upload through the local API. Origin and path only in URLs; no value of a sensitive control, ever. */
export interface LearningEvent {
  /** A lowercase UUID. */
  eventId: string;
  kind: LearningKind;
  host: string;
  site?: string;
  url: string;
  selector?: string;
  /** Sanitized question text. */
  question: string;
  /** The section captions around the control, outermost first, sanitized like the question: a row's "Title" is told from a lone one. */
  section?: string[];
  /** A frame the fill could not enter is tagged `iframe`; every other event names its control's kind as a question does: a textarea, an editor or a file input is "text". */
  controlKind: QuestionKind | "iframe";
  inputType?: string;
  autocomplete?: string;
  /** At most 25 samples. */
  options?: string[];
  time: number;
  reason?: string;
  labelSource?: string;
  required?: boolean;
  /** On an uncertainty event: what the control holds now, when it is not sensitive. */
  current?: string;
  /** On a manual fill: the value the engine wrote, and the value the control holds now. */
  before?: string;
  after?: string;
  optionLabel?: string;
  /** The value read when the watch attached, when non-empty and different, never for a skipped control. */
  priorValue?: string;
  /** What the fill decided for the control. */
  fillOutcome?: RowOutcome;
  trustedInput?: boolean;
  /** The control was edited and nothing of its value is kept. */
  edited?: true;
  /** The change could not be read back. */
  incomplete?: true;
  /** A redacted copy of the markup around the control, at most 4,000 bytes. */
  fragment?: string;
}

export interface Summary {
  host: string;
  url: string;
  /** The site table's id for the page, when it is in the table. */
  site?: string;
  rows: Row[];
  counts: {
    filled: number;
    prefilled: number;
    review: number;
    abstained: number;
    failed: number;
    skipped: number;
    requiredUnfilled: number;
  };
  drafts: Draft[];
  matchUnavailable?: true;
  blockedByDialog?: true;
  notAnApplication?: true;
  /** True when the fill ended because a stage deadline cut the work short. */
  partial?: true;
  /** Selectors the restore pass wrote back after a framework blanked them. */
  restored?: string[];
  /** One event for every control the fill could not answer with confidence. */
  events?: LearningEvent[];
}

export type RunState =
  | { state: "idle" }
  | { state: "running"; runId: string; startedAt: number }
  | { state: "paused"; runId: string; startedAt: number }
  | { state: "done"; runId: string; summary: Summary }
  | { state: "error"; runId: string; message: string }
  | { state: "stopped"; runId: string; reason: StopReason; summary: Summary };

export type StatusReport = RunState & { stamp: string };

export interface AnswerResult {
  selector: string;
  outcome: RowOutcome;
  reason?: RowReason;
  value?: string;
  optionLabel?: string;
}

export interface AnswerReply {
  results: AnswerResult[];
  /** Number of leading resolutions handled; the host re-sends the rest. */
  handled: number;
}

export interface InspectRow {
  selector: string;
  label: string;
  section: string[];
  kind: RowKind;
  required: boolean;
  value?: string;
  /** The option labels a learning event about the control carries: at most 25, and none of a withheld control. */
  options?: string[];
  fileName?: string;
  /** An attachment slot or an authentication box: listed for structure, never filled or sent to the bank. */
  inspectionOnly?: boolean;
  /** An upload that only feeds the page's own parser, not an attachment of the application. */
  parserOnly?: boolean;
}

export interface BlockedSubmit {
  form: string;
  submitter: string;
}

export type FrameDecisionOutcome =
  | "admitted-registry"
  | "admitted-probe"
  | "skipped-zero-controls"
  | "skipped-denylist"
  | "failed-probe"
  | "failed-inject"
  | "failed-no-listener";

export interface FrameDecision {
  frameId: string;
  /** Origin and path only. */
  url: string;
  outcome: FrameDecisionOutcome;
  controls?: number;
  reason?: string;
}

export interface ProbeResult {
  controls: number;
  required: number;
}

/** Asks the top frame's engine for the decision about one sub-frame. */
export interface FrameDecisionRequest {
  frameId: string;
  url: string;
  /** What the host's probe counted in the frame, when the registry and deny-list did not decide. */
  probe?: ProbeResult;
  /** Set when the frame could not be probed, injected or reached. */
  failure?: { outcome: "failed-probe" | "failed-inject" | "failed-no-listener"; reason: string };
}

/** What the pass-through submit observer saw; the main-world helper records it. */
export interface SubmitObservation {
  time: number;
  kind: "request" | "submit-event";
  method?: string;
  status?: number;
  /** Origin and path only. */
  url: string;
}
