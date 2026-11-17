// Fixtures for the Gmail page. Names and hosts are the reserved example ones.
import type { GmailConfirmation, GmailDigest, GmailDigestItem, GmailHealth, GmailInboxThread, GmailLinkItem, GmailTimelineEntry } from "../wire";

const at = "2026-10-12T09:00:00Z";

// localTime is an instant at a given hour of the machine's own day, since a brief is named for the part of the local day it was made in.
export const localTime = (day: number, hour: number) => new Date(2026, 9, day, hour).toISOString();

export function health(over: Partial<GmailHealth> = {}): GmailHealth {
  return {
    connected: true, login: "ok", sync: "healthy", running: false, cursorAgeSeconds: 600, mailbox: "alex@example.com",
    latestRun: {
      id: 4, trigger: "timer", status: "done", mode: "incremental", startedAt: at, coverageComplete: true, fallbackUsed: false, stages: [],
      counts: { listed: 12, fetched: 12, classified: 11, deduplicated: 0, stored: 11, review: 2, ignored: 1, linked: 6, tombstoned: 0, failed: 0 },
    },
    ...over,
  };
}

export function thread(over: Partial<GmailInboxThread> = {}): GmailInboxThread {
  return {
    threadId: "th1", employer: "Northwind Example", role: "Staff Engineer", subject: "Can we schedule a call?", state: "responded", kind: "recruiter_reply_needed", lane: "do_now", owner: "user",
    attention: "urgent", nextAction: "reply", draftKind: "reply", stalenessDays: 2, lastIncomingAt: at, link: "auto", candidacyId: 7, done: false, ...over,
  };
}

export function entry(over: Partial<GmailTimelineEntry> = {}): GmailTimelineEntry {
  return {
    messageId: "m1", type: "recruiter_reply_needed", tier: "actionable", direction: "incoming", employer: "Northwind Example", subject: "Can we schedule a call?",
    snippet: "Are you free on Thursday?", receivedAt: at, ruleId: "rule-recruiter", reviewReasons: [], link: "auto", linkReasons: ["clear_single_match"], putBack: false, ...over,
  };
}

export function linkItem(over: Partial<GmailLinkItem> = {}): GmailLinkItem {
  return {
    signalId: "sig1", threadId: "th1", type: "rejection", tier: "actionable", employer: "Northwind Example", role: "Staff Engineer", subject: "Your application",
    receivedAt: at, decision: "review", candidacyId: 7, score: 0.62, runnerUp: 0.4, reasons: ["ambiguous_runner_up"], strength: "close", nearlyAsClose: true, ...over,
  };
}

export function confirmation(over: Partial<GmailConfirmation> = {}): GmailConfirmation {
  return { id: 3, signalId: "sig9", threadId: "th9", candidacyId: 12, status: "offer", state: "pending", createdAt: at, ...over };
}

function digestItem(over: Partial<GmailDigestItem> = {}): GmailDigestItem {
  return {
    key: "th1", section: "waiting_on_you", company: "Northwind Example", role: "Staff Engineer", lane: "do_now", state: "responded", owner: "user", nextAction: "reply",
    line: "Northwind Example asks for a call on Thursday.", draftKind: "reply", draft: "Hello,\nThursday at 10 works for me.", ...over,
  };
}

export function digest(over: Partial<GmailDigest> = {}): GmailDigest {
  return {
    id: 5, kind: "daily", trigger: "scheduled", status: "done", createdAt: localTime(12, 8), windowStart: "2026-10-11T18:00:00Z", windowEnd: at, method: "model",
    headline: "One reply is waiting", text: "You have one thread that needs a reply.", items: [digestItem()], muted: [], omitted: 3, receipts: 4, delivery: { telegram: "sent", desktop: "skipped" }, ...over,
  };
}
