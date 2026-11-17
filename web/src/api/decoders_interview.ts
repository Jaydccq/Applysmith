// Decoders for the interview preparation views and the document run that
// builds a prep pack.
import type {
  DocumentRetrieval, DocumentRetrievalItem, DocumentRunView, InterviewFeedback, InterviewCoachRun, InterviewHook, InterviewMockTurn, InterviewPack, InterviewPackInfo,
  InterviewPackRef, InterviewPackRun, InterviewStep, InterviewQuestion, InterviewSourceRef, InterviewStage, InterviewWorkspace, InterviewSoon, InterviewsSoon,
} from "../wire";
import { bool, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

export const interviewStage: Decoder<InterviewStage> = object<InterviewStage>(
  { id: str, type: str, format: str, interviewers: orEmpty(str), userConfirmed: bool, revision: safeInt, recordedAt: time },
  { at: str, cancelled: bool },
);

export const interviewsSoon: Decoder<InterviewsSoon> = object<InterviewsSoon>({
  items: orEmpty(object<InterviewSoon>({ candidacyId: safeInt, company: str, role: str, stage: interviewStage, packed: bool, debriefed: bool })),
});

const feedback: Decoder<InterviewFeedback> = object<InterviewFeedback>({ id: safeInt, stage: str, from: str, text: str, recordedAt: time });

export const interviewFeedback = feedback;

const interviewMockTurn: Decoder<InterviewMockTurn> = object<InterviewMockTurn>({
  id: safeInt, stage: str, question: str, answer: str, coaching: str, recordedAt: time,
});

const packInfo: Decoder<InterviewPackInfo> = object<InterviewPackInfo>({ stage: str, revision: safeInt, generation: str, createdAt: time });

const step: Decoder<InterviewStep> = object<InterviewStep>({ text: str }, { kind: str, target: str });

export const interviewWorkspace: Decoder<InterviewWorkspace> = object<InterviewWorkspace>(
  {
    candidacyId: safeInt, stages: orEmpty(interviewStage), cancelled: orEmpty(interviewStage), feedback: orEmpty(feedback), mockTurns: orEmpty(interviewMockTurn), packs: orEmpty(packInfo),
    degraded: bool, nextActions: orEmpty(step),
  },
  { repair: str, packRun: object<InterviewPackRun>({ runId: str, stage: str }), coachRun: object<InterviewCoachRun>({ runId: str, stage: str, question: str, answer: str }) },
);

const hook: Decoder<InterviewHook> = object<InterviewHook>({ text: str, sources: orEmpty(str) });
const question: Decoder<InterviewQuestion> = object<InterviewQuestion>({ category: str, prompt: str, storyIds: orEmpty(str), sources: orEmpty(str) });
const source: Decoder<InterviewSourceRef> = object<InterviewSourceRef>({ kind: str, ref: str, label: str, state: str });

export const interviewPack: Decoder<InterviewPack> = object<InterviewPack>(
  {
    candidacyId: safeInt, stage: str, revision: safeInt, generation: str, createdAt: time, roleBrief: str, companyBrief: str, hooks: orEmpty(hook),
    claims: orEmpty(str), questions: orEmpty(question), priorFeedback: orEmpty(str), gaps: orEmpty(step), nextActions: orEmpty(step),
    sources: orEmpty(source), warnings: orEmpty(str),
  },
  { reportNumber: safeInt },
);

const retrievalItem: Decoder<DocumentRetrievalItem> = object<DocumentRetrievalItem>({ source: str, score: num }, { reasons: orEmpty(str) });
const retrieval: Decoder<DocumentRetrieval> = object<DocumentRetrieval>({ query: str, retrieved: safeInt, items: orEmpty(retrievalItem) });
const packRef: Decoder<InterviewPackRef> = object<InterviewPackRef>({ candidacyId: safeInt, stage: str, revision: safeInt });

export const documentRun: Decoder<DocumentRunView> = object<DocumentRunView>(
  { runId: str, status: str, documents: orEmpty(safeInt), warnings: orEmpty(str), retrieval },
  { stage: str, remainingSeconds: safeInt, reason: str, generation: str, pack: packRef },
);
