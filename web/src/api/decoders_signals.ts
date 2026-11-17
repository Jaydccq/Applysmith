// Decoders for the hiring-signals half of the Leads and Signals page. Each
// mirrors a struct of internal/api/wire; the object decoder rejects unknown
// fields.
import type {
  SignalAlarm, SignalAssessment, SignalDetail, SignalDocument, SignalEvaluateResult, SignalEvent, SignalFactor, SignalIdentity, SignalLane, SignalList, SignalOutcome,
  SignalOutcomes, SignalPosting, SignalQuote, SignalRescoreResult, SignalReviewItem, SignalReviewList, SignalRow, SignalSourceResult, SignalStatus, SignalTickSummary,
  SignalTypeHit,
} from "../wire";
import { bool, dict, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const countMap: Decoder<Record<string, number>> = (v, path) => (v === null ? {} : dict(safeInt)(v, path));

export const signalRow: Decoder<SignalRow> = object<SignalRow>(
  { companyId: safeInt, company: str, tier: str, momentum: num, scanPriority: num, state: str, blocked: bool, sources: orEmpty(str) },
  { newestEvidence: time },
);

export const signalList: Decoder<SignalList> = object<SignalList>({ rows: orEmpty(signalRow), page: safeInt, pageSize: safeInt, total: safeInt });

const document = object<SignalDocument>({ id: safeInt, adapter: str, url: str, title: str, date: time });
const quote = object<SignalQuote>({ documentId: safeInt, quote: str, verified: bool });

const assessment: Decoder<SignalAssessment> = object<SignalAssessment>(
  {
    id: safeInt, status: str, eventType: str, hiringIntent: str, affectedTeams: orEmpty(str), scale: str, strength: safeInt, confidence: num, reasoning: str, quotes: orEmpty(quote),
  },
);

const event = object<SignalEvent>(
  { id: safeInt, type: str, time, documents: orEmpty(document), held: bool },
  { amount: num, assessment },
);

const factor = object<SignalFactor>({
  eventId: safeInt, eventType: str, eventTime: str, hiringIntent: str, typeWeight: num, strengthFactor: num, confidence: num, sourceFactor: num, timeDecay: num, contribution: num, weak: bool,
});

const identity = object<SignalIdentity>({ status: str }, { ats: str, slug: str, careersUrl: str, lane: str });

const posting = object<SignalPosting>(
  { id: safeInt, title: str, location: str, url: str, verdict: str, reasons: orEmpty(str), firstSeen: time },
  { handedOff: time, handoff: str },
);

const outcome = object<SignalOutcome>({ eventId: safeInt, type: str, status: str, windowEnd: time }, { leadDays: safeInt });

export const signalDetail: Decoder<SignalDetail> = object<SignalDetail>(
  { row: signalRow, events: orEmpty(event), factors: orEmpty(factor), postings: orEmpty(posting), outcomes: orEmpty(outcome), searchLinks: orEmpty(str) },
  { blockExpires: time, identity },
);

const reviewItem = object<SignalReviewItem>({ id: safeInt, companyId: safeInt, company: str, reason: str, state: str, assessment, createdAt: time });
export const signalReviewItem: Decoder<SignalReviewItem> = reviewItem;
export const signalReviewList: Decoder<SignalReviewList> = object<SignalReviewList>({ items: orEmpty(reviewItem) });

export const signalEvaluateResult: Decoder<SignalEvaluateResult> = object<SignalEvaluateResult>({ queued: orEmpty(safeInt) });
export const signalRescoreResult: Decoder<SignalRescoreResult> = object<SignalRescoreResult>({ companies: safeInt, tiers: countMap });

const lane = object<SignalLane>({ source: str, title: str, enabled: bool }, { lastProduced: time, lastStatus: str, disclosure: str });
const typeHit = object<SignalTypeHit>({ type: str, hits: safeInt, misses: safeInt, pending: safeInt, hitRate: num });
const outcomes = object<SignalOutcomes>({ hits: safeInt, misses: safeInt, pending: safeInt, hitRate: num, medianLeadDays: num, byType: orEmpty(typeHit) });
const sourceResult = object<SignalSourceResult>({ id: str, status: str, docs: safeInt, new: safeInt }, { reason: str, deferred: orEmpty(str) });
const alarm = object<SignalAlarm>({ source: str, title: str, status: str, lastProduced: time, retryable: bool }, { requiredAction: str });
const tick = object<SignalTickSummary>(
  {
    tickId: safeInt, status: str, startedAt: time, limited: bool, sources: orEmpty(sourceResult), ingested: safeInt, events: safeInt, assessed: safeInt, held: safeInt, tierChanges: safeInt,
    handoffs: safeInt, alarms: orEmpty(alarm), budgetReached: bool,
  },
  { notes: orEmpty(str), error: str },
);

export const signalStatus: Decoder<SignalStatus> = object<SignalStatus>(
  { outcomes, tiers: countMap, reviewOpen: safeInt, lanes: orEmpty(lane) },
  { lastTick: tick, activeTick: safeInt },
);
