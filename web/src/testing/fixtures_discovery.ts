// Synthetic documents for the Discovery and Leads and Signals pages, typed by
// the generated wire declarations. The persona and every address are fictional.
import type { InboxRow, Lead, ScanRecord, ScanSource, Schedule, SignalAssessment, SignalDetail, SignalRow } from "../wire";

const stamp = "2026-10-12T09:00:00.000Z";

export function scan(over: Partial<ScanRecord> = {}): ScanRecord {
  return {
    id: "scan-1", source: "registry", status: "completed", startedAt: stamp, updatedAt: stamp, finishedAt: stamp, durationMs: 61_000, boardsVisited: 3,
    counts: {
      discovered: 40, filtered: { title: 10 }, undated: 0, duplicates: { inbox: 20 }, inserted: 10, queued: 5, enriched: 0, enrichmentDegraded: 0, enrichmentFailed: 0,
      enrichmentSkipped: 0, gateRejected: 0, outcomes: {}, truncatedPostings: 0, boardErrors: 0,
    },
    ...over,
  };
}

export function source(over: Partial<ScanSource> = {}): ScanSource {
  const name = over.name ?? "registry";
  return { name, title: name, kind: "api", readiness: "ready", running: false, ...over };
}

export const sources: ScanSource[] = [
  source({ lastScan: scan() }),
  source({ name: "theirstack", needs: "key", readiness: "needs key" }),
  source({ name: "linkedin", kind: "browser", needs: "session", readiness: "needs sign-in", lastScan: scan({ id: "scan-2", source: "linkedin", status: "needs-sign-in", reason: "needs-sign-in" }) }),
  source({ name: "indeed", kind: "browser", cooldownUntil: "2026-10-12T15:00:00.000Z", lastScan: scan({ id: "scan-3", source: "indeed", status: "blocked" }) }),
  source({ name: "github", kind: "feed", running: true, lastScan: scan({ id: "scan-4", source: "github", status: "running", finishedAt: undefined, runId: "run_gh" }) }),
];

export function lead(over: Partial<Lead> = {}): Lead {
  return {
    id: 1, source: "signals", category: "posting", state: "new", company: "Northwind Example", roles: ["Staff Engineer"], locations: ["Remote"], applyLinks: ["https://jobs.example.com/northwind/1"],
    contactEmails: [], confidence: 0.82, evidenceUrl: "https://news.example.com/northwind", evidenceTitle: "Northwind raises a round", evidenceText: "Northwind Example is hiring staff engineers.",
    evidenceAdapter: "alerts", evidenceAt: stamp, createdAt: stamp, updatedAt: stamp, evaluable: true, ...over,
  };
}

export function signalRow(over: Partial<SignalRow> = {}): SignalRow {
  return { companyId: 7, company: "Contoso Example", tier: "hot", momentum: 8.4, scanPriority: 3.2, state: "new", blocked: false, sources: ["signals-edgar", "signals-hn"], newestEvidence: stamp, ...over };
}

export const signalPage = (rows: SignalRow[], total = rows.length) => ({ rows, page: 1, pageSize: 25, total });

export function assessment(over: Partial<SignalAssessment> = {}): SignalAssessment {
  return {
    id: 1, status: "ok", eventType: "funding", hiringIntent: "strong", affectedTeams: ["engineering"], scale: "headcount", strength: 8, confidence: 0.9,
    reasoning: "A round that names engineering hiring.", quotes: [{ documentId: 3, quote: "will hire 20 engineers", verified: true }], ...over,
  };
}

export function signalDetail(over: Partial<SignalDetail> = {}): SignalDetail {
  return {
    row: signalRow(),
    events: [
      {
        id: 1, type: "funding", time: stamp, amount: 14_000_000, held: false,
        documents: [{ id: 3, adapter: "edgar", url: "https://www.sec.gov/example/filing", title: "Form D", date: stamp }],
        assessment: assessment(),
      },
    ],
    factors: [{ eventId: 1, eventType: "funding", eventTime: stamp, hiringIntent: "strong", typeWeight: 1, strengthFactor: 0.8, confidence: 0.9, sourceFactor: 1, timeDecay: 0.9, contribution: 0.65, weak: false }],
    identity: { status: "found", ats: "greenhouse", slug: "contoso-example", careersUrl: "https://boards.greenhouse.io/contoso-example" },
    postings: [{ id: 1, title: "Backend Engineer", location: "Remote", url: "https://boards.greenhouse.io/contoso-example/jobs/1", verdict: "pass", reasons: [], firstSeen: stamp }],
    outcomes: [],
    searchLinks: [],
    ...over,
  };
}

export function schedule(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 1, name: "Morning scan", kind: "discover.registry", inputs: {}, preset: { kind: "daily", time: "07:00", timezone: "UTC" }, enabled: true, missedPolicy: "skip", dtstart: stamp,
    createdAt: stamp, updatedAt: stamp, next: ["2026-10-13T07:00:00.000Z", "2026-10-14T07:00:00.000Z"], ...over,
  };
}

export const statusOk = {
  outcomes: { hits: 3, misses: 1, pending: 2, hitRate: 0.75, medianLeadDays: 12, byType: [] },
  tiers: { hot: 1, warm: 2, watch: 0, cold: 5 },
  reviewOpen: 1,
  lanes: [
    { source: "signals-edgar", title: "SEC EDGAR filings", enabled: true, lastProduced: stamp, disclosure: "Every request to the SEC carries your contact email, as the SEC fair-access policy requires." },
    { source: "signals-hn", title: "Hacker News", enabled: false },
  ],
  lastTick: { tickId: 4, status: "ok", startedAt: stamp, limited: false, sources: [], ingested: 12, events: 5, assessed: 4, held: 1, tierChanges: 2, handoffs: 1, alarms: [], budgetReached: false },
};

export function inboxRow(over: Partial<InboxRow> = {}): InboxRow {
  return {
    id: 1, url: "https://jobs.example.com/northwind/1", originalUrl: "https://jobs.example.com/northwind/1", aliases: [], identityKey: "northwind-example|staff-engineer", company: "Northwind Example",
    role: "Staff Engineer", location: "Remote", source: "registry", detail: "", score: 7, reasons: ["title match"], earlyCareer: false, clearanceRequired: false, flags: [], notes: "",
    status: "pending", attempts: 0, firstSeenAt: stamp, updatedAt: stamp, ...over,
  };
}
