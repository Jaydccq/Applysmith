// Decoders for the responses the five pages read. Each mirrors a struct of
// internal/api/wire; the object decoder rejects unknown fields, so a field the
// Go side adds fails loudly here until the page learns about it.
import type {
  ApplicationLookup, BulkArchiveResult, BulkStatusResult, Candidacy, CandidacyDetail, CandidacyMail, CandidacyEvent, CandidacyPage, CandidacyTransitions,
  DocumentCheck, DocumentList, DocumentPreferences, ExtensionBuild, ExtensionClient, ExtensionClients, DocumentPreview, DocumentReview, DocumentVerification, DocumentVerifiers, DocumentView, DocumentVisualReview,
  LLMCost, LLMCostRow, LLMFallback, LLMBench, LLMGroup, LLMBenchResult, LLMKeyStatus, LLMPreset, LLMProvider, LLMRest, LLMRouting, LLMTaskRoute, LLMTestResult, ReportDetail,
  PairingCode, PriorSubmission, ReportPage, ReportSummary, StatusChangeResult, TrackerSummary, TrackerDuplicates, DuplicateGroup, DuplicateMergeResult, MessageDraft, MessageVariant, FollowupList, FollowupSuggestion,
} from "../wire";
import { bool, dict, list, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const anything: Decoder<unknown> = (v) => v;

const candidacyMail = object<CandidacyMail>({ threadId: str, lane: str, owner: str, nextAction: str, ageDays: safeInt, open: safeInt });

export const candidacy: Decoder<Candidacy> = object<Candidacy>(
  {
    id: safeInt, date: str, company: str, role: str, status: str, resumePdf: bool, coverLetter: bool, notes: str,
    postingUrl: str, location: str, identityKey: str, version: safeInt, createdAt: time, updatedAt: time,
  },
  { score: num, scoreKind: str, appliedBy: str, appliedAt: str, reportNumber: safeInt, source: str, archivedAt: str, mail: candidacyMail },
);

export const candidacyPage: Decoder<CandidacyPage> = object<CandidacyPage>({
  entries: orEmpty(candidacy), page: safeInt, pageSize: safeInt, total: safeInt,
}, { archived: safeInt });

const candidacyEvent: Decoder<CandidacyEvent> = object<CandidacyEvent>(
  { id: safeInt, at: time, actor: str, kind: str },
  { previousStatus: str, newStatus: str, detail: anything },
);

export const candidacyDetail: Decoder<CandidacyDetail> = object<CandidacyDetail>({
  entry: candidacy, events: orEmpty(candidacyEvent), aliases: orEmpty(str),
});

export const statusChangeResult: Decoder<StatusChangeResult> = object<StatusChangeResult>({
  entry: candidacy, changed: bool, correction: bool, inferred: bool,
});

export const bulkStatusResult: Decoder<BulkStatusResult> = object<BulkStatusResult>({ results: orEmpty(statusChangeResult) });

export const bulkArchiveResult: Decoder<BulkArchiveResult> = object<BulkArchiveResult>({ changed: orEmpty(safeInt), open: orEmpty(safeInt) });

export const candidacyTransitions: Decoder<CandidacyTransitions> = object<CandidacyTransitions>({
  status: str, ordinary: orEmpty(str), correction: orEmpty(str),
});

export const applicationLookup: Decoder<ApplicationLookup> = object<ApplicationLookup>(
  { found: bool },
  { entry: candidacy, identityKey: str, history: orEmpty(object<NonNullable<ApplicationLookup["history"]>[number]>({ status: str, at: time, actor: str })), submission: object<PriorSubmission>({ confirmed: bool, source: str, at: time }) },
);

export const trackerDuplicates: Decoder<TrackerDuplicates> = object<TrackerDuplicates>({
  groups: orEmpty(object<DuplicateGroup>({ survivor: safeInt, status: str, rule: str, needsUser: bool, rows: orEmpty(candidacy) }, { reason: str })),
});

export const duplicateMergeResult: Decoder<DuplicateMergeResult> = object<DuplicateMergeResult>({ kept: candidacy }, { undo: str });

export const trackerSummary: Decoder<TrackerSummary> = object<TrackerSummary>({ counts: dict(safeInt), total: safeInt });

export const followupList: Decoder<FollowupList> = object<FollowupList>({
  suggestions: orEmpty(object<FollowupSuggestion>({ id: safeInt, company: str, role: str, version: safeInt, kind: str }, { number: safeInt, due: str })),
});

const messageVariant: Decoder<MessageVariant> = object<MessageVariant>({ angle: str, message: str });
export const messageDraft: Decoder<MessageDraft> = object<MessageDraft>({
  kind: str, primary: messageVariant, alternatives: orEmpty(messageVariant), maxChars: num,
  confirmed: orEmpty(str), toConfirm: orEmpty(str), dropped: orEmpty(str), notes: orEmpty(str),
});

const reportSummary: Decoder<ReportSummary> = object<ReportSummary>(
  {
    number: safeInt, kind: str, company: str, role: str, postingUrl: str, scoreKind: str, recommendation: str,
    legitimacy: str, verification: str, createdAt: time, documentsEligible: bool,
  },
  { score: num, trackerStatus: str },
);

export const reportPage: Decoder<ReportPage> = object<ReportPage>({
  reports: orEmpty(reportSummary), page: safeInt, pageSize: safeInt, total: safeInt,
});

export const reportDetail: Decoder<ReportDetail> = object<ReportDetail>({ report: reportSummary, header: dict(str), content: str });

const fallback: Decoder<LLMFallback> = object<LLMFallback>({ provider: str, model: str });

const provider: Decoder<LLMProvider> = object<LLMProvider>(
  { id: str, kind: str, structuredOutput: bool, embeddings: bool, keyConfigured: bool },
  { baseUrl: str, preset: str, program: str},
);

const preset: Decoder<LLMPreset> = object<LLMPreset>(
  { name: str, label: str, kind: str, inSetup: bool, keyConfigured: bool, subscription: bool },
  { keyUrl: str, program: str },
);

const rest: Decoder<LLMRest> = object<LLMRest>({ provider: str, why: str, until: time, failures: safeInt }, { model: str });

const benchResult: Decoder<LLMBenchResult> = object<LLMBenchResult>(
  { provider: str, model: str, correct: safeInt, wrong: safeInt, noAnswer: safeInt, errors: safeInt, p50Ms: safeInt, p95Ms: safeInt },
  { costUsd: num },
);

const bench: Decoder<LLMBench> = object<LLMBench>({ at: time, cases: safeInt, results: orEmpty(benchResult) });

const taskRoute: Decoder<LLMTaskRoute> = object<LLMTaskRoute>(
  { task: str, group: str, class: str },
  {
    purpose: str, provider: str, model: str, thinking: str, maxOutputTokens: safeInt, timeoutSeconds: safeInt, firstTokenSeconds: safeInt,
    fallbacks: orEmpty(fallback), source: str, bench, unrouted: str, broken: str, budgetUsd: num, spentTodayUsd: num, budgetReached: bool, budgetable: bool,
  },
);

const llmTestResult: Decoder<LLMTestResult> = object<LLMTestResult>(
  { provider: str, model: str, outcome: str, latencyMs: safeInt, testedAt: time },
  { errorClass: str, fix: str, costUsd: num, note: str },
);

const group: Decoder<LLMGroup> = object<LLMGroup>({ mode: str, members: orEmpty(fallback) });

export const llmRouting: Decoder<LLMRouting> = object<LLMRouting>(
  {
    preset: str, dailyUsdCap: num, capSpentUsd: num, providers: orEmpty(provider), presets: orEmpty(preset), tasks: orEmpty(taskRoute), groups: dict(group), rests: orEmpty(rest), knownModels: orEmpty(fallback),
  },
  { presetFallback: str, lastTest: llmTestResult, testing: str, offline: bool },
  ["lastTest"],
);

const costRow: Decoder<LLMCostRow> = object<LLMCostRow>({
  name: str, calls: safeInt, successRate: num, fallbackRate: num, costUsd: num, unpriced: safeInt, p50Ms: safeInt, p95Ms: safeInt, rateLimitedRate: num,
}, { cacheHitRate: num });

export const llmCost: Decoder<LLMCost> = object<LLMCost>({ days: safeInt, byTask: orEmpty(costRow), byProvider: orEmpty(costRow) });

export const extensionBuild: Decoder<ExtensionBuild> = object<ExtensionBuild>({ stamp: str, version: str });

const extensionClient = object<ExtensionClient>({ id: str, lastSeen: time }, { version: str, build: str, managed: bool });
export const extensionClients: Decoder<ExtensionClients> = object<ExtensionClients>({ since: time, clients: list(extensionClient) });

export const pairingCode: Decoder<PairingCode> = object<PairingCode>({ code: str, expiresAt: time });

export const llmKeyStatus: Decoder<LLMKeyStatus> = object<LLMKeyStatus>({ provider: str, keyConfigured: bool });

const documentCheck: Decoder<DocumentCheck> = object<DocumentCheck>({ tool: str, id: str, status: str }, { reason: str });

const documentPreview: Decoder<DocumentPreview> = object<DocumentPreview>({ page: safeInt, name: str, sha256: str });

const documentVerifiers: Decoder<DocumentVerifiers> = object<DocumentVerifiers>(
  { rules: str, pdfScript: str, layoutScript: str, layoutProfile: str },
  { python: str, pypdf: str, poppler: str },
);

const documentVisualReview: Decoder<DocumentVisualReview> = object<DocumentVisualReview>({ status: str }, { pdfSha256: str });

const documentVerification: Decoder<DocumentVerification> = object<DocumentVerification>(
  {
    state: str, pages: safeInt, pdfSha256: str, verifiers: documentVerifiers, checks: orEmpty(documentCheck),
    previews: orEmpty(documentPreview), visualReview: documentVisualReview,
  },
  { stale: str, texSha256: str, sourceSha256: str, manifestHash: str, template: str },
);

const documentReview: Decoder<DocumentReview> = object<DocumentReview>(
  { status: str, revisions: safeInt, factual: safeInt, narrative: safeInt, rejected: safeInt },
  { reason: str },
);

const documentView: Decoder<DocumentView> = object<DocumentView>(
  {
    id: safeInt, bundle: str, version: safeInt, kind: str, mode: str, path: str, sourcePath: str, pdfState: str, paper: str,
    pageLimit: safeInt, language: str, generation: str, warnings: orEmpty(str), contentHash: str,
    manifestHash: str, current: bool, ready: bool, frozen: bool, refinePending: bool, createdAt: time,
  },
  { candidacyId: safeInt, pdfPath: str, pages: safeInt, pdfSha256: str, verification: documentVerification, review: documentReview, sourcesChanged: orEmpty(str) },
);

export const documentList: Decoder<DocumentList> = object<DocumentList>({ documents: orEmpty(documentView) }, { activeRunId: str, noReport: bool, belowFloor: bool });

export const documentPreferences: Decoder<DocumentPreferences> = object<DocumentPreferences>({ language: str, paper: str, resumePages: safeInt, coverWhenAsked: str, revision: safeInt });
