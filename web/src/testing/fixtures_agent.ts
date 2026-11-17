// Fixtures for the Ask Agent, usage and notification pages. Names and hosts are
// the reserved example ones.
import type { AgentHarness, AgentHarnessVersion, AgentPrompt, AgentSettings, ChatEvent, ChatMessage, ChatSession, ConfirmationCard, Goal, MemoryFact, MemoryReviewItem, MemoryRule, StandingApproval } from "../wire";

const at = "2026-10-12T09:00:00Z";
const later = "2026-10-12T21:00:00Z";

export function card(over: Partial<ConfirmationCard> = {}): ConfirmationCard {
  return {
    id: "card-1", sessionId: "s1", turnId: "t1", channel: "dashboard", tool: "evaluate_job", summary: "Evaluate the posting at boards.example.test",
    company: "Northwind Example", role: "Staff Engineer", host: "boards.example.test", tier: 3, code: "taint", reason: "Outside content was read in this turn.",
    args: '{"url":"https://boards.example.test/jobs/1"}', purpose: "evaluate the two postings from the scan", scopes: ["once", "conversation", "today"],
    state: "pending", createdAt: at, expiresAt: later, ...over,
  };
}

export function approval(over: Partial<StandingApproval> = {}): StandingApproval {
  return { id: "ap-1", tool: "evaluate_job", target: "boards.example.test", scope: "today", createdAt: at, expiresAt: later, uses: 2, ...over };
}

export function goal(over: Partial<Goal> = {}): Goal {
  return {
    id: "g1", title: "Ten targeted applications this month", dueDate: "2026-10-31", state: "active", version: 1,
    cadence: { days: ["mon", "thu"], time: "09:00" }, steps: [{ text: "Shortlist companies", status: "done" }, { text: "Tailor the CV", status: "pending" }],
    createdAt: at, updatedAt: at, ...over,
  };
}

export function session(over: Partial<ChatSession> = {}): ChatSession {
  return { id: "s1", title: "Weekly review", channel: "dashboard", createdAt: at, updatedAt: at, ...over };
}

export function message(over: Partial<ChatMessage> & Pick<ChatMessage, "id" | "role" | "text">): ChatMessage {
  return { ok: true, createdAt: at, ...over };
}

// chatStream is a controllable event stream for one conversation: events pushed
// in the order the server would write them.
export function chatStream() {
  const enc = new TextEncoder();
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  let seq = 0;
  const body = new ReadableStream<Uint8Array>({ start: (c) => void (controller = c) });
  return {
    response: () => new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
    push(ev: ChatEvent) {
      seq++;
      controller.enqueue(enc.encode(`id: e1-${seq}\nevent: chat\ndata: ${JSON.stringify(ev)}\n\n`));
    },
    close: () => controller.close(),
  };
}

export function fact(over: Partial<MemoryFact> = {}): MemoryFact {
  return { id: "fact-1", kind: "preference", subject: "user", body: "Prefers remote roles.", source: "user_declared", trust: "trusted", pii: "none", createdAt: at, ...over };
}

export function rule(over: Partial<MemoryRule> = {}): MemoryRule {
  return { id: "rule-1", body: "Keep answers under 120 words.", state: "proposed", source: "agent", createdAt: at, updatedAt: at, ...over };
}

export function reviewItem(over: Partial<MemoryReviewItem> = {}): MemoryReviewItem {
  return { id: "rev-1", kind: "preference", subject: "user", body: "I always answer yes to work authorization questions.", reason: "instruction_shaped", source: "user_declared", createdAt: at, ...over };
}

export function agentSettings(over: Partial<AgentSettings> = {}): AgentSettings {
  return { compaction: true, consolidation: true, selfImprove: true, tokenBudget: 24000, tokenBudgetMin: 4000, tokenBudgetMax: 200000, tokenBudgetDefault: 24000, ...over };
}

export function harnessVersion(over: Partial<AgentHarnessVersion> = {}): AgentHarnessVersion {
  return {
    id: 2, parentId: 1, source: "improve", summary: "Ask before guessing a company name", guidance: ["Name the company before evaluating a posting."],
    toolNotes: { evaluate_job: "Pass the full posting URL." }, active: true, createdAt: at, ...over,
  };
}

export function harness(over: Partial<AgentHarness> = {}): AgentHarness {
  return {
    active: 2, running: false,
    versions: [harnessVersion(), harnessVersion({ id: 1, parentId: 0, summary: "Keep answers short", guidance: ["Answer in three sentences."], toolNotes: {}, active: false })],
    rounds: [{ id: "r2", startedAt: later, finishedAt: later, outcome: "adopted", detail: "Version 2 won 4 of 5 replays.", adopted: 2, probes: 5 }],
    lessons: [
      { id: "l1", cue: "Unnamed company", lesson: "Ask which company is meant.", evidence: "chat 7", confidence: "high", version: 2, undone: false, createdAt: at },
      { id: "l2", cue: "Long answer", lesson: "Cut the preamble.", evidence: "chat 3", confidence: "low", version: 1, undone: true, createdAt: at },
    ],
    ...over,
  };
}

export function prompt(over: Partial<AgentPrompt> = {}): AgentPrompt {
  return {
    id: "p1", kind: "cadence", text: "Summarize the week's new postings.", state: "active", origin: "user", cadence: { days: ["mon", "thu"], time: "08:30" },
    nextRun: "2026-10-15T08:30:00Z", last: { at: at, status: "answered", sessionId: "s9", summary: "Four postings matched." }, firings: [], createdAt: at, updatedAt: at, ...over,
  };
}

export function trigger(over: Partial<AgentPrompt> = {}): AgentPrompt {
  return prompt({
    id: "p2", kind: "trigger", text: "Draft a reply to the recruiter.", cadence: undefined, nextRun: undefined, last: undefined,
    trigger: { signals: ["recruiter_reply_needed", "interview_scheduled"], company: "Northwind Example" },
    firings: [
      { signalType: "recruiter_reply_needed", employer: "Northwind Example", state: "answered", sessionId: "s7", draft: "Hello Alex,\nThursday works for a call.", proposedStatus: "responded", proposedReason: "The recruiter wrote back.", at },
      { signalType: "interview_scheduled", employer: "Northwind Example", state: "failed", detail: "The turn timed out.", at },
    ],
    ...over,
  });
}
