// Decoders for the runs board, the day, the application starts and the fill of
// the active tab. Each mirrors a struct of internal/api/wire; the object decoder
// rejects unknown fields.
import type {
  ApplyBatchAccepted, ApplySkippedRow, AutofillControl, AutofillTabResult, BoardEntry, BoardEntryList, BoardSummary, DayCounts, DayLine, DayView, EntryFix, NeedsYouCounts, NeedsYouInbox, NeedsYouItem,
  Notice, NoticeList, RunAccepted, RunView, SignInCheck, SignInChecks,
} from "../wire";
import { bool, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

export const runAccepted: Decoder<RunAccepted> = object<RunAccepted>({ runId: str }, { ahead: safeInt });

const entryFix = object<EntryFix>({ to: str, label: str }, { scan: str });

export const boardEntry: Decoder<BoardEntry> = object<BoardEntry>(
  { id: str, kind: str, origin: str, title: str, state: str, link: str, revision: safeInt, startedAt: time, updatedAt: time, stateAt: time },
  { stage: str, detail: str, reason: str, outcome: str, batchId: str, ref: str, canFocusTab: bool, focusTab: str, canDone: bool, canSubmit: bool, finishedAt: time, seenAt: time, acknowledgedAt: time, fix: entryFix },
);

export const boardEntryList: Decoder<BoardEntryList> = object<BoardEntryList>({ entries: orEmpty(boardEntry) });

export const boardSummary: Decoder<BoardSummary> = object<BoardSummary>({
  working: safeInt, needsYou: safeInt, blocked: safeInt, doneUnacknowledged: safeInt, unseen: safeInt,
});

const needsYouItem: Decoder<NeedsYouItem> = object<NeedsYouItem>({ entry: boardEntry, waitingSeconds: safeInt, action: str });

export const needsYouInbox: Decoder<NeedsYouInbox> = object<NeedsYouInbox>({ items: orEmpty(needsYouItem), unseen: safeInt });
export const needsYouCounts: Decoder<NeedsYouCounts> = object<NeedsYouCounts>({ emailConfirmations: safeInt, wikiDrafts: safeInt, heldSignals: safeInt, suggestedAnswers: safeInt, answerEdits: safeInt });

const dayLine: Decoder<DayLine> = object<DayLine>({ text: str, link: str });

const dayCounts: Decoder<DayCounts> = object<DayCounts>({ found: safeInt, evaluated: safeInt, ready: safeInt, appliedByUser: safeInt, appliedByAgent: safeInt, waiting: safeInt });

export const dayView: Decoder<DayView> = object<DayView>({ date: str, did: orEmpty(dayLine), left: orEmpty(dayLine), employers: orEmpty(dayLine), counts: dayCounts });

const notice: Decoder<Notice> = object<Notice>({ id: safeInt, title: str, body: str, createdAt: time, read: bool }, { link: str });

export const noticeList: Decoder<NoticeList> = object<NoticeList>({ notices: orEmpty(notice), unread: safeInt });

export const runView: Decoder<RunView> = object<RunView>(
  { runId: str, kind: str, status: str, createdAt: time, updatedAt: time },
  { stage: str, failureReason: str, finishedAt: time, entry: boardEntry, conversation: str },
);

const skippedRow: Decoder<ApplySkippedRow> = object<ApplySkippedRow>({ company: str, role: str, reason: str });

export const applyBatchAccepted: Decoder<ApplyBatchAccepted> = object<ApplyBatchAccepted>({ runId: str, batchId: str, rows: safeInt, skipped: orEmpty(skippedRow) }, { ahead: safeInt });

const control: Decoder<AutofillControl> = object<AutofillControl>({ label: str }, { reason: str });

export const autofillTabResult: Decoder<AutofillTabResult> = object<AutofillTabResult>(
  {
    runId: str, status: str, filled: orEmpty(control), review: orEmpty(control), missing: orEmpty(control), skipped: orEmpty(control),
    drafts: safeInt, flagged: safeInt, events: safeInt,
  },
  { reason: str, url: str, site: str },
);

const signInCheck: Decoder<SignInCheck> = object<SignInCheck>(
  { domain: str, label: str, state: str, next: str, cached: bool, error: str },
  { checkedAt: time },
  ["checkedAt"],
);

export const signInChecks: Decoder<SignInChecks> = object<SignInChecks>({ checks: orEmpty(signInCheck) });
