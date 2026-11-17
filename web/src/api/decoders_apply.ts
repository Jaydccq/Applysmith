// Decoders for the apply list and the application run, batch and skip-list
// records. Each mirrors a struct of internal/api/wire/apply.go and tracker.go.
// The only form values any carries are the user's own (a question's answer is
// their Answer Bank entry's, an edit the value they typed over one an entry
// wrote), and the object decoder rejects unknown fields, so a field the server
// adds fails here until the page learns about it.
import type {
  ApplyAttempt, ApplyBatchRow, ApplyCorrection, ApplyBatchView, ApplyCounts, ApplyDefectShape, ApplyItem, ApplyLane, ApplyList, ApplyOutcome, ApplyOwnerGroup,
  ApplyAnswerSaved, ApplyQuestion, ApplyRunView, ApplySkipList, ApplySkipStatus, ApplyStage, ApplyTabShown,
} from "../wire";
import { bool, dict, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";
import { answerEdit } from "./decoders_bank";

// Go encodes a nil map as null.
const dictOrEmpty =
  <T>(item: Decoder<T>): Decoder<Record<string, T>> =>
  (v, path) =>
    v === null ? {} : dict(item)(v, path);

const applyOutcome: Decoder<ApplyOutcome> = object<ApplyOutcome>({ kind: str }, { stage: str, class: str, reason: str, confirmed: bool, owner: str });

const applyAttempt: Decoder<ApplyAttempt> = object<ApplyAttempt>({ runId: str, at: time, mode: str, outcome: applyOutcome });

const applyItem: Decoder<ApplyItem> = object<ApplyItem>(
  { id: safeInt, company: str, role: str, lane: str, date: str, version: safeInt, state: str },
  { score: num, reportNumber: safeInt, postingHost: str, postingUrl: str, letterOffer: bool, skipEntry: str, attempt: applyAttempt, runId: str, keptTab: bool, formWaiting: bool, leftOut: str, sourceCheck: str },
);

const applyLane: Decoder<ApplyLane> = object<ApplyLane>({ lane: str, total: safeInt, items: orEmpty(applyItem) });

const applyCounts: Decoder<ApplyCounts> = object<ApplyCounts>({ states: dictOrEmpty(safeInt), lanes: dictOrEmpty(safeInt) });

export const applyList: Decoder<ApplyList> = object<ApplyList>({ lanes: orEmpty(applyLane), page: safeInt, pageSize: safeInt, counts: applyCounts });

const applyStage: Decoder<ApplyStage> = object<ApplyStage>({ stage: str, status: str, detail: str, startedAt: time, ms: safeInt });

const applyCorrection: Decoder<ApplyCorrection> = object<ApplyCorrection>({ message: str });

const applyQuestion: Decoder<ApplyQuestion> = object<ApplyQuestion>({ question: str, kind: str, everywhere: bool }, { options: orEmpty(str), answeredBy: str, answer: str, draftable: bool });

export const applyRunView: Decoder<ApplyRunView> = object<ApplyRunView>(
  { runId: str, mode: str, caller: str, host: str, startedAt: time, outcome: applyOutcome, stages: orEmpty(applyStage), pages: safeInt, keptTab: bool },
  { batchId: str, company: str, role: str, ats: str, finishedAt: time, reportNumber: safeInt, candidacyId: safeInt, uploads: orEmpty(str), letterOverride: bool, correction: applyCorrection, questions: orEmpty(applyQuestion), edits: orEmpty(answerEdit) },
);

export const applyAnswerSaved: Decoder<ApplyAnswerSaved> = object<ApplyAnswerSaved>({ entryId: str }, { continued: bool });

const batchRow: Decoder<ApplyBatchRow> = object<ApplyBatchRow>(
  { index: safeInt, company: str, role: str, host: str, state: str },
  { mirrors: safeInt, runId: str, note: str, outcome: applyOutcome },
);

const ownerGroup: Decoder<ApplyOwnerGroup> = object<ApplyOwnerGroup>({ owner: str, companies: orEmpty(str) });

const defectShape: Decoder<ApplyDefectShape> = object<ApplyDefectShape>({ stage: str, class: str, reason: str, companies: orEmpty(str) }, { snapshot: str });

export const applyBatchView: Decoder<ApplyBatchView> = object<ApplyBatchView>(
  {
    batchId: str, state: str, mode: str, caller: str, createdAt: time,
    rows: orEmpty(batchRow), owners: orEmpty(ownerGroup), defects: orEmpty(defectShape),
  },
);

export const applySkipList: Decoder<ApplySkipList> = object<ApplySkipList>({
  entries: orEmpty(str), defaulted: bool, differs: bool, defaults: orEmpty(str), revision: str,
});

export const applyTabShown: Decoder<ApplyTabShown> = object<ApplyTabShown>({ runId: str });

export const applySkipStatus: Decoder<ApplySkipStatus> = object<ApplySkipStatus>({ company: str, listed: bool }, { entry: str });
