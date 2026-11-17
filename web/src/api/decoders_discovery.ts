// Decoders for the Discovery page and the schedule proposals. Each mirrors a
// struct of internal/api/wire; the object decoder rejects unknown fields.
import type {
  ConfigOp, EvaluationView, InboxPage, InboxPromoteResult, PortalCandidate, PortalCompany, PortalCompanyList, PortalDetection, PortalRegistry, PortalTargetRoles, InboxRow, BlockedCompany, BlockedCompanyList, Lead, LeadPage, RunFinal, RunProgress, ScanCounts, ScanList, ScanRecord, ScanSource, ScanSourceList, Schedule, ScheduleHistory,
  ScheduleHistoryRow, ScheduleKind, ScheduleKindList, ScheduleList, SchedulePauseResult, SchedulePreset, SchedulePreview, ScheduleProposal, ScheduleProposalDecided,
  ScheduleProposalList, ScheduleTimer, ScheduleField, FunnelLane, FunnelStages, FunnelWeek, SourceFunnel,
} from "../wire";
import { bool, dict, list, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

// A map of strings: Go writes a nil one as null.
const countStrings: Decoder<Record<string, string>> = (v, path) => (v === null ? {} : dict(str)(v, path));

// Go writes an empty map as an object, but a nil one as null.
const countMap: Decoder<Record<string, number>> = (v, path) => (v === null ? {} : dict(safeInt)(v, path));

const scanCounts = object<ScanCounts>({
  discovered: safeInt, filtered: countMap, undated: safeInt, duplicates: countMap, inserted: safeInt, queued: safeInt, enriched: safeInt, enrichmentDegraded: safeInt,
  enrichmentFailed: safeInt, enrichmentSkipped: safeInt, gateRejected: safeInt, outcomes: countMap, truncatedPostings: safeInt, boardErrors: safeInt,
});

const scanRecord: Decoder<ScanRecord> = object<ScanRecord>(
  { id: str, source: str, status: str, startedAt: time, updatedAt: time, durationMs: safeInt, counts: scanCounts, boardsVisited: safeInt },
  { runId: str, reason: str, error: str, finishedAt: time, truncated: orEmpty(str), deferred: countMap, suspended: orEmpty(str), errors: orEmpty(str) },
);

const scanSource = object<ScanSource>(
  { name: str, title: str, kind: str, readiness: str, running: bool },
  { needs: str, keyName: str, reason: str, cooldownUntil: time, lastScan: scanRecord },
);

export const scanSourceList: Decoder<ScanSourceList> = object<ScanSourceList>({ sources: orEmpty(scanSource) });

export const scanList: Decoder<ScanList> = object<ScanList>({ scans: orEmpty(scanRecord), page: safeInt, limit: safeInt, total: safeInt });

export const lead: Decoder<Lead> = object<Lead>(
  {
    id: safeInt, source: str, category: str, state: str, company: str, roles: orEmpty(str), locations: orEmpty(str), applyLinks: orEmpty(str), contactEmails: orEmpty(str),
    confidence: num, createdAt: time, updatedAt: time, evaluable: bool,
  },
  {
    reason: str, workModel: str, compensation: str, contactMethod: str, sponsorship: str, posterType: str, expanded: str, evidenceUrl: str, evidenceTitle: str, evidenceText: str,
    evidenceAdapter: str, evidenceAt: time, evidenceAbsentReason: str, inboxId: safeInt, author: str, inboxStatus: str, reportNumber: safeInt, trackerId: safeInt,
  },
);

export const leadPage: Decoder<LeadPage> = object<LeadPage>(
  { rows: orEmpty(lead), page: safeInt, pageSize: safeInt, total: safeInt, counts: countMap },
  { evaluating: bool, expanding: bool },
);

const blocked = object<BlockedCompany>({ id: safeInt, name: str, normalized: str, reason: str, addedBy: str, addedAt: time });
export const blockedCompany: Decoder<BlockedCompany> = blocked;
export const blockedCompanyList: Decoder<BlockedCompanyList> = object<BlockedCompanyList>({ companies: orEmpty(blocked) });

export const runProgress: Decoder<RunProgress> = object<RunProgress>({ runId: str, line: str });
export const runFinal: Decoder<RunFinal> = object<RunFinal>(
  { runId: str, status: str },
  { outcome: str, reportNumber: safeInt, reason: str, decision: str, review: bool, scanId: str, documents: orEmpty(safeInt) },
);

const preset: Decoder<SchedulePreset> = object<SchedulePreset>({ kind: str, timezone: str }, { time: str, weekdays: orEmpty(str), rrule: str });

// Inputs are free-form by job kind.
const anyValue: Decoder<unknown> = (v) => v;
const inputs: Decoder<Record<string, unknown>> = (v, path) => (v === null ? {} : dict(anyValue)(v, path));

export const schedule: Decoder<Schedule> = object<Schedule>(
  {
    id: safeInt, name: str, kind: str, inputs, preset, enabled: bool, missedPolicy: str, dtstart: time, createdAt: time, updatedAt: time, next: orEmpty(time),
  },
  { packId: str, packKey: str, lastFiredAt: time, lastEvaluatedAt: time, activeRunId: str, lastRunId: str, lastResult: str, lastSkipReason: str, lastResultAt: time, lastReason: str, lastDetail: str, unknownKind: bool, problem: str, pausedBy: str, attention: list(str) },
);

const timer = object<ScheduleTimer>({ name: str, description: str, active: bool }, { reason: str });
export const scheduleList: Decoder<ScheduleList> = object<ScheduleList>({ schedules: orEmpty(schedule), timers: orEmpty(timer) }, { pausedAll: safeInt });
export const schedulePauseResult: Decoder<SchedulePauseResult> = object<SchedulePauseResult>({ changed: orEmpty(str) });

const field = object<ScheduleField>({ name: str, type: str }, { enum: orEmpty(str), min: safeInt, max: safeInt, doc: str });
const kind = object<ScheduleKind>({ name: str, title: str, doc: str, leases: orEmpty(str), network: bool, inputs: orEmpty(field) }, { artifact: str });
export const scheduleKindList: Decoder<ScheduleKindList> = object<ScheduleKindList>({ kinds: orEmpty(kind) });

const historyRow = object<ScheduleHistoryRow>(
  { id: safeInt, scheduledFor: time, outcome: str, attempts: safeInt, createdAt: time },
  { reason: str, detail: str, runId: str, startedAt: time, finishedAt: time, summary: str },
);
export const scheduleHistory: Decoder<ScheduleHistory> = object<ScheduleHistory>({ history: orEmpty(historyRow) });

export const schedulePreview: Decoder<SchedulePreview> = object<SchedulePreview>({ next: orEmpty(time) });

export const scheduleProposal: Decoder<ScheduleProposal> = object<ScheduleProposal>(
  { id: str, scheduleId: safeInt, reason: str, change: object<ScheduleProposal["change"]>({}, { enabled: bool, preset }), createdAt: time },
  { scheduleName: str },
);
export const scheduleProposalList: Decoder<ScheduleProposalList> = object<ScheduleProposalList>({ proposals: orEmpty(scheduleProposal) });
export const scheduleProposalDecided: Decoder<ScheduleProposalDecided> = object<ScheduleProposalDecided>({ id: str, accepted: bool }, { schedule });

const inboxRow = object<InboxRow>(
  {
    id: safeInt, url: str, originalUrl: str, aliases: orEmpty(str), identityKey: str, company: str, role: str, location: str, source: str, detail: str, score: safeInt,
    reasons: orEmpty(str), earlyCareer: bool, clearanceRequired: bool, flags: orEmpty(str), notes: str, status: str, attempts: safeInt, firstSeenAt: time, updatedAt: time,
  },
  { requiredYears: safeInt, jobId: str, runId: str, outcome: str, reportNumber: safeInt, reportScore: num },
);

export const inboxPage: Decoder<InboxPage> = object<InboxPage>({ rows: orEmpty(inboxRow), page: safeInt, pageSize: safeInt, total: safeInt, counts: countMap }, { unrouted: str });
export const inboxRowDecoder: Decoder<InboxRow> = inboxRow;
export const evaluationView: Decoder<EvaluationView> = object<EvaluationView>(
  { runId: str, status: str, review: bool },
  { phase: str, outcome: str, reportNumber: safeInt, score: num, recommendation: str, reason: str, explanation: str, decision: str, reviewReason: str, postingUrl: str, inboxRowId: safeInt },
);
export const inboxPromoteResult: Decoder<InboxPromoteResult> = object<InboxPromoteResult>({ queued: orEmpty(safeInt), rejected: countStrings });

const portalCompany = object<PortalCompany>(
  { name: str, enabled: bool, ats: str, slug: str, careersUrl: str, board: str, status: str, ownTitleFilter: bool },
  { lastScannedAt: time, jobCount: safeInt },
  ["lastScannedAt", "jobCount"],
);
export const portalCompanyList: Decoder<PortalCompanyList> = object<PortalCompanyList>({ companies: orEmpty(portalCompany) });

export const portalRegistry: Decoder<PortalRegistry> = object<PortalRegistry>(
  { active: safeInt, dead: safeInt, blocked: safeInt, total: safeInt, enabled: bool },
  { lastSeededAt: time },
  ["lastSeededAt"],
);

const portalCandidate = object<PortalCandidate>({ ats: str, slug: str, company: str, jobs: safeInt, live: bool, reason: str, inRegistry: bool, inPortals: bool });
export const portalDetection: Decoder<PortalDetection> = object<PortalDetection>({
  input: str, kind: str, name: str, careersUrl: str, candidates: orEmpty(portalCandidate), note: str,
});

const configOp = object<ConfigOp>({ op: str, path: str }, { value: str, index: safeInt, match: str });
export const portalTargetRoles: Decoder<PortalTargetRoles> = object<PortalTargetRoles>({ revision: str, lines: orEmpty(str), ops: orEmpty(configOp) });

const funnelLane = object<FunnelLane>({ lane: str, discovered: safeInt, inserted: safeInt, evaluated: safeInt, failed: safeInt, gateRejected: safeInt, high: safeInt, applied: safeInt, responded: safeInt, interview: safeInt });

const funnelStages = object<FunnelStages>({ found: safeInt, evaluated: safeInt, applied: safeInt, responded: safeInt, interview: safeInt, offer: safeInt });
export const funnelWeek: Decoder<FunnelWeek> = object<FunnelWeek>({ since: str, current: funnelStages, previous: funnelStages });

export const sourceFunnel: Decoder<SourceFunnel> = object<SourceFunnel>({ days: safeInt, lanes: orEmpty(funnelLane), total: funnelLane });
