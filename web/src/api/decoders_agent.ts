// Decoders for the Ask Agent, Agent control, usage and notification pages.
// Each mirrors a struct of internal/api/wire; the object decoder rejects
// unknown fields.
import type {
  AgentIdeaStarted, AgentTask, AgentTaskList, AgentTaskStep, AgentHarness, AgentHarnessVersion, AgentImproveRound, AgentLesson, AgentPrompt, AgentPromptDeleted, AgentPromptList, AgentSettings, ApplySkipStatus, ChatCancelResult, ChatCitation, ChatDeleted, ChatEvent, ChatMessage, ChatSession, ChatSessionDetail, ChatSessionPage, ChatSteerAccepted, ChatTurn, ChatTurnAccepted,
  ComputerUseStatus, ConfirmationCard, ConfirmationList, ConfirmationOutcome, DesktopActivity, DesktopEvent, DesktopTarget, Goal, GoalCadence, GoalDeleted, GoalList, GoalProposal, GoalStep, MemoryDeleted, MemoryExport,
  MemoryFact, MemoryFactEdited, MemoryFactList, MemoryReviewItem, MemoryReviewList, MemoryRule, MemoryRuleList, NotificationSettings, NotificationTest, NotificationTestChannel, PromptFiring, PromptLast, PromptTriggerSpec,
  StandingApproval, StandingApprovalList, StandingApprovalRevoked, TakeoverStarted, TelegramPairingCode, TelegramStatus, UsageCall, UsageCallPage, UsageDay, UsageDays, UsageSpan,
  UsageTrace, UsageTraceDetail, UsageTraceList,
} from "../wire";
import { bool, dict, list, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";
import { remoteQR } from "./decoders_remote";

export const chatSession: Decoder<ChatSession> = object<ChatSession>(
  { id: str, title: str, channel: str, createdAt: time, updatedAt: time },
  { parentId: str, parentMessageId: safeInt },
);

export const chatSessionPage: Decoder<ChatSessionPage> = object<ChatSessionPage>({ sessions: orEmpty(chatSession), total: safeInt });

const chatCitation = object<ChatCitation>({ label: str }, { snippet: str, url: str });

const chatMessage = object<ChatMessage>(
  { id: safeInt, role: str, text: str, ok: bool, createdAt: time },
  { turnId: str, toolName: str, class: str, toolCallId: str, systemMade: bool, citations: orEmpty(chatCitation), limitations: orEmpty(str), nextAction: str, runId: str },
);

const chatTurn = object<ChatTurn>(
  { id: str, channel: str, origin: str, status: str, startedAt: time },
  { ruleId: str, tier: str, reason: str, detail: str, finishedAt: time },
);

export const chatSessionDetail: Decoder<ChatSessionDetail> = object<ChatSessionDetail>(
  { session: chatSession, messages: orEmpty(chatMessage), turns: orEmpty(chatTurn) },
  { activeTurnId: str },
);

export const chatTurnAccepted: Decoder<ChatTurnAccepted> = object<ChatTurnAccepted>({ sessionId: str, turnId: str });
export const chatSteerAccepted: Decoder<ChatSteerAccepted> = object<ChatSteerAccepted>({ turnId: str, queued: safeInt });
export const chatCancelResult: Decoder<ChatCancelResult> = object<ChatCancelResult>({ turnId: str, runs: orEmpty(str), note: str });
export const chatDeleted: Decoder<ChatDeleted> = object<ChatDeleted>({ id: str });

export const chatEvent: Decoder<ChatEvent> = object<ChatEvent>(
  { type: str },
  { citations: orEmpty(chatCitation), limitations: orEmpty(str), nextAction: str, turnId: str, text: str, toolCallId: str, tool: str, class: str, phrase: str, ok: bool, summary: str, code: str, reason: str, detail: str, activeTurnId: str, confirmationId: str, runId: str },
);

const confirmationCard: Decoder<ConfirmationCard> = object<ConfirmationCard>(
  {
    id: str, sessionId: str, turnId: str, channel: str, tool: str, summary: str, tier: safeInt, code: str, reason: str, args: str, purpose: str,
    scopes: orEmpty(str), state: str, createdAt: time, expiresAt: time,
  },
  { company: str, role: str, host: str, decidedAt: time },
);

export const confirmationList: Decoder<ConfirmationList> = object<ConfirmationList>({ cards: orEmpty(confirmationCard) });

const standingApproval: Decoder<StandingApproval> = object<StandingApproval>(
  { id: str, tool: str, target: str, scope: str, createdAt: time, expiresAt: time, uses: safeInt },
  { sessionId: str, lastUsedAt: time },
);

export const standingApprovalList: Decoder<StandingApprovalList> = object<StandingApprovalList>({ approvals: orEmpty(standingApproval) });
export const standingApprovalRevoked: Decoder<StandingApprovalRevoked> = object<StandingApprovalRevoked>({ id: str });

export const confirmationOutcome: Decoder<ConfirmationOutcome> = object<ConfirmationOutcome>(
  { card: confirmationCard },
  { approval: standingApproval, result: str },
);

const goalStep = object<GoalStep>({ text: str }, { status: str, note: str, applicationId: str, company: str });
const goalCadence = object<GoalCadence>({ days: orEmpty(str), time: str });
const goalProposal = object<GoalProposal>(
  { id: str, reason: str, steps: orEmpty(goalStep), createdAt: time },
  { dueDate: str, cadence: goalCadence },
);

export const goal: Decoder<Goal> = object<Goal>(
  { id: str, title: str, state: str, version: safeInt, cadence: goalCadence, steps: orEmpty(goalStep), createdAt: time, updatedAt: time },
  { dueDate: str, proposal: goalProposal, nextCheckin: time },
);

export const goalList: Decoder<GoalList> = object<GoalList>({ goals: orEmpty(goal) });
export const goalDeleted: Decoder<GoalDeleted> = object<GoalDeleted>({ id: str });

const agentTaskStep = object<AgentTaskStep>({ text: str, status: str }, { note: str });
export const agentTask: Decoder<AgentTask> = object<AgentTask>(
  { id: str, sessionId: str, title: str, mandate: str, state: str, steps: orEmpty(agentTaskStep), turns: safeInt, maxTurns: safeInt, createdAt: time, updatedAt: time },
  { idea: str, question: str, outcome: str },
);
export const agentTaskList: Decoder<AgentTaskList> = object<AgentTaskList>({ tasks: orEmpty(agentTask) });
export const agentIdeaStarted: Decoder<AgentIdeaStarted> = object<AgentIdeaStarted>({ sessionId: str, taskId: str });

export const notificationSettings: Decoder<NotificationSettings> = object<NotificationSettings>({
  desktop: bool, telegram: bool, failedScans: bool, telegramPaired: bool,
});

export const notificationTest: Decoder<NotificationTest> = object<NotificationTest>({
  channels: list(object<NotificationTestChannel>({ channel: str, state: str, detail: str })),
});

export const telegramStatus: Decoder<TelegramStatus> = object<TelegramStatus>(
  { tokenSet: bool, paired: bool, health: str, unauthorizedAttempts: safeInt },
  { botUsername: str },
);

export const telegramPairingCode: Decoder<TelegramPairingCode> = object<TelegramPairingCode>({ code: str, botUsername: str, expiresAt: time }, { link: str, qr: remoteQR });

export const applySkipStatus: Decoder<ApplySkipStatus> = object<ApplySkipStatus>({ company: str, listed: bool }, { entry: str });

const usageCall = object<UsageCall>(
  { id: safeInt, callId: str, startedAt: time, task: str, provider: str, model: str, attempt: safeInt, outcome: str, inputTokens: safeInt, outputTokens: safeInt, cachedTokens: safeInt, latencyMs: safeInt, downgraded: bool },
  { costUsd: num, runId: str, reason: str, ttftMs: safeInt },
);
export const usageCallPage: Decoder<UsageCallPage> = object<UsageCallPage>({ calls: orEmpty(usageCall), total: safeInt });

const usageDay = object<UsageDay>({ day: str, costUsd: num, calls: safeInt });
export const usageDays: Decoder<UsageDays> = object<UsageDays>({ days: orEmpty(usageDay) });

const usageTrace = object<UsageTrace>({ runId: str, title: str, kind: str, startedAt: time, calls: safeInt, costUsd: num }, { status: str, stage: str });
export const usageTraceList: Decoder<UsageTraceList> = object<UsageTraceList>({ traces: orEmpty(usageTrace) });

const usageSpan = object<UsageSpan>(
  { kind: str, name: str, startMs: safeInt, durationMs: safeInt },
  { outcome: str, provider: str, model: str, attempt: safeInt, inputTokens: safeInt, outputTokens: safeInt, costUsd: num, reason: str, error: str },
);
export const usageTraceDetail: Decoder<UsageTraceDetail> = object<UsageTraceDetail>({ trace: usageTrace, spans: orEmpty(usageSpan) });

export const memoryFact: Decoder<MemoryFact> = object<MemoryFact>(
  { id: str, kind: str, subject: str, body: str, source: str, trust: str, pii: str, createdAt: time },
  { subjectRef: str },
);
export const memoryFactList: Decoder<MemoryFactList> = object<MemoryFactList>({ facts: orEmpty(memoryFact), maxFacts: safeInt, maxFactBody: safeInt });
export const memoryFactEdited: Decoder<MemoryFactEdited> = object<MemoryFactEdited>({}, { fact: memoryFact, reviewId: str });
export const memoryRule: Decoder<MemoryRule> = object<MemoryRule>({ id: str, body: str, state: str, source: str, createdAt: time, updatedAt: time });
export const memoryRuleList: Decoder<MemoryRuleList> = object<MemoryRuleList>({ rules: orEmpty(memoryRule), maxActive: safeInt, maxRuleBody: safeInt });
const memoryReviewItem = object<MemoryReviewItem>({ id: str, kind: str, subject: str, body: str, reason: str, source: str, createdAt: time }, { subjectRef: str });
export const memoryReviewList: Decoder<MemoryReviewList> = object<MemoryReviewList>({ items: orEmpty(memoryReviewItem) });
export const memoryDeleted: Decoder<MemoryDeleted> = object<MemoryDeleted>({ id: str });
export const memoryExport: Decoder<MemoryExport> = object<MemoryExport>({
  exportedAt: time, facts: orEmpty(memoryFact), rules: orEmpty(memoryRule), review: orEmpty(memoryReviewItem),
});

export const agentSettings: Decoder<AgentSettings> = object<AgentSettings>({
  compaction: bool, consolidation: bool, selfImprove: bool, tokenBudget: safeInt, tokenBudgetMin: safeInt, tokenBudgetMax: safeInt, tokenBudgetDefault: safeInt,
});

const toolNotes: Decoder<Record<string, string>> = (v, path) => (v === null ? {} : dict(str)(v, path));

const harnessVersion = object<AgentHarnessVersion>({
  id: safeInt, parentId: safeInt, source: str, summary: str, guidance: orEmpty(str), toolNotes, active: bool, createdAt: time,
});

const improveRound = object<AgentImproveRound>(
  { id: str, startedAt: time, outcome: str, detail: str, adopted: safeInt, probes: safeInt },
  { finishedAt: time },
);

const lesson = object<AgentLesson>({ id: str, cue: str, lesson: str, evidence: str, confidence: str, version: safeInt, undone: bool, createdAt: time });

export const agentHarness: Decoder<AgentHarness> = object<AgentHarness>({
  active: safeInt, running: bool, versions: orEmpty(harnessVersion), rounds: orEmpty(improveRound), lessons: orEmpty(lesson),
});

const promptTrigger = object<PromptTriggerSpec>({ signals: orEmpty(str) }, { company: str });
const promptLast = object<PromptLast>({ at: time, status: str }, { sessionId: str, summary: str });
const promptFiring = object<PromptFiring>(
  { signalType: str, state: str, at: time },
  { employer: str, detail: str, sessionId: str, draft: str, proposedStatus: str, proposedReason: str },
);

export const agentPrompt: Decoder<AgentPrompt> = object<AgentPrompt>(
  { id: str, kind: str, text: str, state: str, origin: str, firings: orEmpty(promptFiring), createdAt: time, updatedAt: time },
  { cadence: goalCadence, trigger: promptTrigger, nextRun: time, last: promptLast },
);
export const agentPromptList: Decoder<AgentPromptList> = object<AgentPromptList>({ prompts: orEmpty(agentPrompt), limit: safeInt });
export const agentPromptDeleted: Decoder<AgentPromptDeleted> = object<AgentPromptDeleted>({ id: str });

export const computerUseStatus: Decoder<ComputerUseStatus> = object<ComputerUseStatus>(
  { health: str, accessibility: bool, screenRecording: bool, servicePaused: bool, backgroundOff: bool, paused: bool, imagesToModel: bool },
  { reason: str },
);

const desktopEvent = object<DesktopEvent>(
  { id: str, at: time, tool: str, verdict: str },
  { sessionId: str, turnId: str, app: str, window: str, role: str, control: str, reason: str, detail: str, shotId: str, target: object<DesktopTarget>({ x: num, y: num, width: num, height: num }) },
);
export const desktopActivity: Decoder<DesktopActivity> = object<DesktopActivity>({ events: orEmpty(desktopEvent) });

export const takeoverStarted: Decoder<TakeoverStarted> = object<TakeoverStarted>({ sessionId: str });
