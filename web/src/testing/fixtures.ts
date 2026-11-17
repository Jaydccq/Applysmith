// Synthetic documents typed by the generated wire declarations. The persona
// and every address are fictional.
import type { ApplyList, Candidacy, CandidacyDetail, CandidacyPage, CandidacyTransitions, DocumentCheck, DocumentList, DocumentResumeContent, DocumentRunView, DocumentSource, DocumentVerification, DocumentView, LLMCost, LLMRouting, ReportDetail, ReportPage, ReportSummary, TrackerSummary } from "../wire";

const stamp = "2026-10-01T09:00:00.000Z";

export function candidacy(over: Partial<Candidacy> = {}): Candidacy {
  return {
    id: 1, date: "2026-09-28", company: "Northwind Example", role: "Staff Engineer", score: 4.3, scoreKind: "final", status: "evaluated",
    resumePdf: false, coverLetter: false, notes: "", postingUrl: "https://jobs.example.com/northwind/1", location: "Remote", identityKey: "northwind-example|staff-engineer",
    version: 2, createdAt: stamp, updatedAt: stamp, ...over,
  };
}

export const rows: Candidacy[] = [
  candidacy(),
  candidacy({ id: 2, company: "Contoso Example", role: "Platform Engineer", score: 3.8, status: "applied", appliedBy: "agent", appliedAt: "2026-09-30", source: "scan", reportNumber: 7 }),
  candidacy({ id: 3, company: "Fabrikam Example", role: "Backend Engineer", score: 4.6, status: "interview", appliedBy: "user", notes: "Phone screen booked" }),
];

export function page(entries: Candidacy[], total = entries.length, pageNumber = 1, pageSize = 50): CandidacyPage {
  return { entries, page: pageNumber, pageSize, total };
}

export const summary: TrackerSummary = { counts: { evaluated: 1, applied: 1, interview: 1 }, total: 3 };

export function detail(entry: Candidacy): CandidacyDetail {
  return {
    entry,
    aliases: [],
    events: [{ id: 1, at: stamp, actor: "evaluation", kind: "created", newStatus: entry.status }],
  };
}

export function transitions(status: string, ordinary: string[], correction: string[] = []): CandidacyTransitions {
  return { status, ordinary, correction };
}

export const applyList: ApplyList = {
  page: 1,
  pageSize: 10,
  counts: { states: { idle: 2 }, lanes: { "apply-now": 1, "marked-applied": 1 } },
  lanes: [
    { lane: "apply-now", total: 1, items: [{ id: 1, company: "Northwind Example", role: "Staff Engineer", score: 4.3, lane: "apply-now", reportNumber: 7, postingHost: "jobs.example.com", postingUrl: "https://jobs.example.com/northwind/1", version: 1, date: "2026-09-28", state: "idle" }] },
    { lane: "selective", total: 0, items: [] },
    { lane: "marked-applied", total: 1, items: [{ id: 5, company: "Fabrikam Example", role: "Backend Engineer", score: 3.9, lane: "marked-applied", version: 1, date: "2026-09-20", state: "idle" }] },
    { lane: "awaiting", total: 0, items: [] },
  ],
};

export function report(over: Partial<ReportSummary> = {}): ReportSummary {
  return {
    number: 7, kind: "final", company: "Northwind Example", role: "Staff Engineer", postingUrl: "https://jobs.example.com/northwind/1", score: 4.3,
    scoreKind: "final", recommendation: "apply", legitimacy: "credible", verification: "browser live 2026-09-28", createdAt: stamp, documentsEligible: false, ...over,
  };
}

export const reportPage: ReportPage = { page: 1, pageSize: 25, total: 2, reports: [report(), report({ number: 6, company: "Contoso Example", role: "Platform Engineer", score: 3.6, recommendation: "consider", legitimacy: "unclear", trackerStatus: "evaluated" })] };

export const reportBody = `# Northwind Example: Staff Engineer

## Summary

The role fits **well**. See [the posting](https://jobs.example.com/northwind/1).

<script>window.pwned = true</script>

| Area | Fit |
| --- | --- |
| Scope | High |
`;

export function reportDetail(over: Partial<ReportSummary> = {}, content = reportBody): ReportDetail {
  return { report: report(over), header: { score: "4.3" }, content };
}

export const routing: LLMRouting = {
  preset: "deepseek",
  dailyUsdCap: 5,
  capSpentUsd: 0,
  providers: [{ id: "deepseek", kind: "openai-compatible", structuredOutput: true, embeddings: false, keyConfigured: true }],
  presets: [
    { name: "deepseek", label: "DeepSeek", kind: "openai-compatible", inSetup: true, subscription: false, keyConfigured: true },
    { name: "anthropic", label: "Anthropic", kind: "anthropic", inSetup: true, subscription: false, keyConfigured: false },
    { name: "codex", label: "Codex subscription", kind: "cli", program: "codex", inSetup: false, subscription: true, keyConfigured: false },
  ],
  tasks: [
    { task: "evaluation.deep", group: "evaluation", class: "reasoning", provider: "deepseek", model: "deepseek-chat", thinking: "medium", source: "preset", fallbacks: [{ provider: "anthropic", model: "claude-example" }] },
    { task: "scan.rank", group: "discovery", class: "fast", provider: "deepseek", model: "deepseek-chat", source: "table" },
    { task: "answers.draft", group: "answers", class: "reasoning", unrouted: "no provider is configured for this class" },
  ],
  groups: {},
  rests: [],
  knownModels: [{ provider: "deepseek", model: "deepseek-chat" }, { provider: "deepseek", model: "deepseek-reasoner" }],
};

export const cost: LLMCost = {
  days: 1,
  byTask: [],
  byProvider: [{ name: "deepseek", calls: 4, successRate: 1, fallbackRate: 0, costUsd: 0.42, unpriced: 0, p50Ms: 900, p95Ms: 2100, rateLimitedRate: 0 }],
};

const hash = (c: string) => c.repeat(64);

export function docCheck(tool: string, id: string, status: string, reason?: string): DocumentCheck {
  return reason === undefined ? { tool, id, status } : { tool, id, status, reason };
}

export function verification(over: Partial<DocumentVerification> = {}): DocumentVerification {
  return {
    state: "ready", pages: 1, pdfSha256: hash("b"), texSha256: hash("c"), sourceSha256: hash("a"), manifestHash: hash("d"), template: "t1",
    verifiers: { rules: "1", pdfScript: "2", layoutScript: "2", layoutProfile: "resume-a4", poppler: "pdftotext version 26.10.0" },
    checks: [docCheck("verify_pdf", "text-layer", "pass"), docCheck("verify_layout", "clipping", "pass"), docCheck("final", "contact", "pass")],
    previews: [{ page: 1, name: "resume-page-1.png", sha256: hash("e") }],
    visualReview: { status: "pending" }, ...over,
  };
}

// documentRun is a finished document run with nothing retrieved.
export function documentRun(over: Partial<DocumentRunView> = {}): DocumentRunView {
  return { runId: "run-1", status: "succeeded", documents: [], warnings: [], retrieval: { query: "", retrieved: 0, items: [] }, ...over };
}

export function documentView(over: Partial<DocumentView> = {}): DocumentView {
  return {
    id: 11, bundle: "northwind-example-staff-engineer-2026-10-01", version: 2, kind: "resume", mode: "tailored", candidacyId: 1,
    path: "documents/northwind-example-staff-engineer-2026-10-01/2", sourcePath: "documents/northwind-example-staff-engineer-2026-10-01/2/resume.json",
    pdfPath: "documents/northwind-example-staff-engineer-2026-10-01/2/resume.pdf", pdfState: "ready", pages: 1, paper: "a4", pageLimit: 2, language: "en",
    generation: "deterministic", warnings: [], contentHash: hash("a"), pdfSha256: hash("b"), manifestHash: hash("d"),
    current: true, ready: true, frozen: false, refinePending: false, createdAt: stamp, verification: verification(), ...over,
  };
}

function resumeContent(over: Partial<DocumentResumeContent> = {}): DocumentResumeContent {
  return {
    profile: "Backend engineer focused on reliable data platforms.", profileEvidence: ["E2"],
    competencies: [{ label: "Languages", text: "Go, SQL", evidence: ["E5"] }],
    experience: [{ entry: "X1", bullets: [{ text: "Led the platform migration", evidence: ["E3"] }, { text: "Mentored four engineers", evidence: ["E4"] }] }],
    projects: [], ...over,
  };
}

export function documentSource(over: Partial<DocumentSource> = {}): DocumentSource {
  return { id: 11, kind: "resume", version: 2, frozen: false, text: "# Alex Example\n\nBackend engineer focused on reliable data platforms.\n", resume: resumeContent(),
    evidence: [{ id: "E3", source: "cv.md#Experience", text: "Led the platform migration across 12 services" }], ...over };
}

export function documentList(...documents: DocumentView[]): DocumentList {
  return { documents };
}

// A lane list with a skip-listed row and rows without a posting address.
export const applyListWithSkips: ApplyList = {
  page: 1,
  pageSize: 10,
  counts: { states: { idle: 2, skipped: 1 }, lanes: { "apply-now": 3 } },
  lanes: [
    {
      lane: "apply-now", total: 12, items: [
        { id: 1, company: "Northwind Example", role: "Staff Engineer", score: 4.3, lane: "apply-now", reportNumber: 7, postingHost: "jobs.example.com", postingUrl: "https://jobs.example.com/northwind/1", version: 1, date: "2026-09-28", state: "idle" },
        {
          id: 2, company: "Microsoft", role: "Platform Engineer", score: 4.5, lane: "apply-now", postingHost: "jobs.example.org", postingUrl: "https://jobs.example.org/microsoft/2", version: 1, date: "2026-09-29",
          skipEntry: "Microsoft", state: "skipped", leftOut: 'Microsoft is on your batch skip list (as "Microsoft"); apply there yourself',
        },
        { id: 3, company: "Tailspin Example", role: "Backend Engineer", score: 4.1, lane: "apply-now", version: 1, date: "2026-09-30", state: "idle" },
      ],
    },
    { lane: "selective", total: 0, items: [] },
    { lane: "marked-applied", total: 0, items: [] },
    { lane: "awaiting", total: 0, items: [] },
  ],
};
