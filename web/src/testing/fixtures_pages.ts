// Synthetic documents for the answer bank, profile and knowledge pages. The
// persona and every address are fictional.
import type {
  AnswerBankEntry, AnswerBankFile, AnswerBankSettings, AnswerBankUnanswered, AnswerBankUsageRow, AnswerCandidate, ConfigDocument, KBCoverage,
  KBDocEntry, KBDocument, KBPrincipleGrade,
} from "../wire";
import { KBDocSnippet } from "../wire";

const stamp = "2026-10-01T09:00:00.000Z";

export function entry(over: Partial<AnswerBankEntry> = {}): AnswerBankEntry {
  return { id: "work-authorization", description: "Authorized to work", kind: "fixed", preferred: null, proficiency_options: null, selected: null, category: "work-authorization", answer: "Yes", ...over };
}

export function bankFile(entries: AnswerBankEntry[], revision = "rev-1"): AnswerBankFile {
  return { exists: true, revision, warnings: [], bank: { version: 1, employers: [], entries, overrides: {} }, defaultIds: [], shapes: {} };
}

export function unanswered(over: Partial<AnswerBankUnanswered> = {}): AnswerBankUnanswered {
  return { unanswered: [], blocking: [], needsReview: [], askMe: [], derived: [], asked: [], ...over };
}

export const gap = (id: string, blocking = false) => ({ id, category: "contact", kind: "fixed", blocking });

export function candidate(over: Partial<AnswerCandidate> = {}): AnswerCandidate {
  return {
    id: 1, key: "new-entry:how did you hear about example corp", type: "new-entry", state: "pending", source: "manual-fill", targetEntryId: "", entryId: "how-did-you-hear",
    seedQuestion: "How did you hear about Example Corp?", questions: ["How did you hear about Example Corp?"], hosts: ["jobs.example.com"], sections: [], options: [], multiple: false, proposedKind: "fixed",
    proposedAnswer: "A colleague", longForm: false, flags: [], support: 3, firstSeenAt: stamp, lastSeenAt: stamp, createdAt: stamp, updatedAt: stamp,
    review: { verdict: "", reason: "", draft: "", cited: [] }, ...over,
  };
}

export const settings: AnswerBankSettings = { llmFallbackEnabled: false, groundingMode: "strict", autoAcceptLegalDeclarations: false, allowGoogleSignIn: false, watchScans: false, knowledgeSemanticRetrieval: false };

export function usageRow(over: Partial<AnswerBankUsageRow> = {}): AnswerBankUsageRow {
  return { entryId: "work-authorization", matches: 4, applies: 3, abstains: 1, corrections: 0, lastMatchedAt: stamp, lastAppliedAt: stamp, lastCorrectedAt: "", lastActivityAt: stamp, exampleQuestions: [], ...over };
}

export function config(file: string, over: Partial<ConfigDocument> = {}): ConfigDocument {
  return { file, exists: true, template: false, content: "", revision: `${file}-rev-1`, issues: [], ...over };
}

export function profileValues(over: { [key: string]: unknown } = {}): { [key: string]: unknown } {
  return {
    version: 1,
    candidate: { full_name: "Jordan Sample", address: { city: "Lisbon", region: "", country: "PT" } },
    work_authorization: { authorized_countries: ["PT"], requires_sponsorship_now: null, requires_sponsorship_future: null },
    preferences: { relocation: { places: ["Lisbon", "Oslo", "Berlin"], excluded: ["Atlantis"] } },
    resumes: [{ id: "backend", label: "Backend", file: "resumes/backend.pdf", default: true, tags: ["go"] }],
    ...over,
  };
}

export function docEntry(over: Partial<KBDocEntry> = {}): KBDocEntry {
  return { id: "reef-survey", kind: KBDocSnippet, path: "knowledge/projects/reef-survey.md", category: "projects", title: "Reef survey", description: "", modifiedAt: stamp, fileHash: "hash-1", tags: ["data"], principles: [], sourceType: "", sourceUrl: "", lint: [], ...over };
}

export function kbDoc(e: KBDocEntry, body = "I led a small survey team.", sections: KBDocument["sections"] = []): KBDocument {
  return { entry: e, body, sections, hasPrevious: false, links: [], linkedFrom: [] };
}

export function theme(over: Partial<KBPrincipleGrade> = {}): KBPrincipleGrade {
  return { id: "ownership", name: "Ownership", grade: "green", stories: ["quieting-the-pager"], missing: [], suggest: [], questions: ["Tell me about a time you took ownership."], ...over };
}

export function coverage(...themes: KBPrincipleGrade[]): KBCoverage {
  return { principles: themes, shapes: [] };
}
