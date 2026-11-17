// Decoders for the Gmail page. Each mirrors a struct of internal/api/wire; the
// object decoder rejects unknown fields.
import type {
  GmailConfirmation, GmailConfirmationDecided, GmailConfirmationList, GmailCounts, GmailDigest, GmailDigestItem, GmailDigestList, GmailDigestRated, GmailDigestVerdicts,
  GmailConnected, GmailHealth, GmailInboxPage, GmailInboxThread, GmailLinkConfirmed, GmailLinkItem, GmailLinkList, GmailLinkRejected, GmailLinkRestored, GmailMove, GmailMoveList, GmailOutcome, GmailRun, GmailSettings, GmailStage,
  GmailDraft, GmailSummary, GmailSyncStarted, GmailTest, GmailThreadDraft, GmailThreadDetail, GmailThreadDismissed, GmailThreadDone, GmailTimelineEntry,
} from "../wire";
import { bool, dict, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const stage = object<GmailStage>({ stage: str, status: str }, { error: str });
const counts = object<GmailCounts>({
  listed: safeInt, fetched: safeInt, classified: safeInt, deduplicated: safeInt, stored: safeInt, review: safeInt, ignored: safeInt, linked: safeInt, tombstoned: safeInt, failed: safeInt,
});
const run = object<GmailRun>(
  {
    id: safeInt, trigger: str, status: str, mode: str, startedAt: time, coverageComplete: bool, fallbackUsed: bool, stages: orEmpty(stage), counts,
  },
  { failedStage: str, error: str },
);

export const gmailHealth: Decoder<GmailHealth> = object<GmailHealth>(
  { connected: bool, login: str, sync: str, running: bool },
  { loginError: str, cursorAgeSeconds: safeInt, failedStage: str, latestRun: run, mailbox: str },
);
export const gmailConnected: Decoder<GmailConnected> = object<GmailConnected>({ health: gmailHealth }, { previous: str });
export const gmailTest: Decoder<GmailTest> = object<GmailTest>({ messages: safeInt });

const inboxThread = object<GmailInboxThread>(
  {
    threadId: str, employer: str, subject: str, state: str, kind: str, lane: str, owner: str, attention: str, nextAction: str, stalenessDays: safeInt, lastIncomingAt: time, link: str, done: bool,
  },
  { role: str, draftKind: str, dueAt: time, candidacyId: safeInt },
);

export const gmailThreadDone: Decoder<GmailThreadDone> = object<GmailThreadDone>({ threadId: str });
export const gmailThreadDismissed: Decoder<GmailThreadDismissed> = object<GmailThreadDismissed>({ threadId: str, signalIds: orEmpty(str) });

export const gmailInboxPage: Decoder<GmailInboxPage> = object<GmailInboxPage>({ threads: orEmpty(inboxThread), total: safeInt }, { next: str });

const outcome = object<GmailOutcome>({ result: str }, { fromStatus: str, toStatus: str, reason: str, candidacyId: safeInt });
const timelineEntry = object<GmailTimelineEntry>(
  {
    messageId: str, type: str, tier: str, direction: str, employer: str, subject: str, snippet: str, receivedAt: time, ruleId: str, reviewReasons: orEmpty(str), link: str, linkReasons: orEmpty(str), putBack: bool,
  },
  { role: str, dueAt: time, evidence: (v) => v, outcome, rowStatus: str },
);

export const gmailThreadDetail: Decoder<GmailThreadDetail> = object<GmailThreadDetail>({ thread: inboxThread, timeline: orEmpty(timelineEntry) });
export const gmailSummary: Decoder<GmailSummary> = object<GmailSummary>({ text: str, generatedAt: time, method: str, cached: bool });

export const gmailThreadDraft: Decoder<GmailThreadDraft> = object<GmailThreadDraft>(
  { kind: str },
  { draft: object<GmailDraft>({ text: str, kind: str, generatedAt: time, method: str }, { confirmed: orEmpty(str), toConfirm: orEmpty(str), note: str }) },
);

const linkItem = object<GmailLinkItem>(
  {
    signalId: str, threadId: str, type: str, tier: str, employer: str, subject: str, receivedAt: time, decision: str, score: num, runnerUp: num, reasons: orEmpty(str), strength: str, nearlyAsClose: bool,
  },
  { role: str, candidacyId: safeInt, outcome },
);
export const gmailLinkList: Decoder<GmailLinkList> = object<GmailLinkList>({ items: orEmpty(linkItem) });
export const gmailLinkConfirmed: Decoder<GmailLinkConfirmed> = object<GmailLinkConfirmed>({ signalId: str, threadId: str, candidacyId: safeInt }, { outcome });
export const gmailLinkRejected: Decoder<GmailLinkRejected> = object<GmailLinkRejected>({ signalId: str });
export const gmailLinkRestored: Decoder<GmailLinkRestored> = object<GmailLinkRestored>({ signalId: str });

const confirmation = object<GmailConfirmation>({ id: safeInt, signalId: str, threadId: str, candidacyId: safeInt, status: str, state: str, createdAt: time }, { company: str, role: str });
export const gmailConfirmationList: Decoder<GmailConfirmationList> = object<GmailConfirmationList>({ confirmations: orEmpty(confirmation) });
export const gmailConfirmationDecided: Decoder<GmailConfirmationDecided> = object<GmailConfirmationDecided>({ id: safeInt, state: str }, { rowStatus: str });
export const gmailMoveList: Decoder<GmailMoveList> = object<GmailMoveList>({
  moves: orEmpty(object<GmailMove>({ threadId: str, subject: str, at: time, outcome, company: str, role: str, status: str, putBack: bool })),
});

const digestItem = object<GmailDigestItem>(
  { key: str, section: str, company: str, lane: str, state: str, owner: str, nextAction: str, line: str },
  { role: str, due: str, draftKind: str, draft: str },
);
const gmailDigest: Decoder<GmailDigest> = object<GmailDigest>(
  {
    id: safeInt, kind: str, trigger: str, status: str, createdAt: time, windowStart: time, windowEnd: time, method: str, headline: str, text: str, items: orEmpty(digestItem),
    muted: orEmpty(digestItem), omitted: safeInt, receipts: safeInt, delivery: dict(str),
  },
  { funnel: dict(safeInt), narrative: str, error: str },
);
export const gmailDigestList: Decoder<GmailDigestList> = object<GmailDigestList>({ digests: orEmpty(gmailDigest) });
export const gmailDigestVerdicts: Decoder<GmailDigestVerdicts> = object<GmailDigestVerdicts>({ verdicts: dict(str) });
export const gmailDigestRated: Decoder<GmailDigestRated> = object<GmailDigestRated>({ digestId: safeInt, threadKey: str, verdict: str });

export const gmailSettings: Decoder<GmailSettings> = object<GmailSettings>({ alerts: bool, scheduledDigests: bool });
export const gmailSyncStarted: Decoder<GmailSyncStarted> = object<GmailSyncStarted>({ started: bool });
