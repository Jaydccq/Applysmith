// Decoders for the answer bank page. Each mirrors a struct of
// internal/api/wire; the object decoder rejects unknown fields, and the bank
// decoders name every key the file can hold, so a bank read here and saved back
// loses nothing.
import type {
  AnswerBank, AnswerBankBody, AnswerBankChoice, AnswerBankEmployer, AnswerBankShape, AnswerBankEntry, AnswerBankFile, AnswerBankLocationPolicy, AnswerBankNumericThreshold, AnswerBankOverride,
  AnswerBankAsked, AnswerBankDerived, AnswerBankSaved, AnswerBankScopeItem, AnswerBankSettings, AnswerBankTranscript, AnswerBankUnanswered, AnswerBankUsage, AnswerBankUsageRow, AnswerBankGap, AnswerCandidate,
  AnswerCandidateDecision, AnswerCandidateReview, AnswerComposeResult, AnswerCandidateGenerateResult, AnswerCandidateList, AnswerEdit,
} from "../wire";
import { bool, dict, list, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const strings = list(str);
const numericThreshold = object<AnswerBankNumericThreshold>({ extractor: str }, { max_value_profile_key: str, above_answer: str, on_parse_miss: str });
const locationPolicy = object<AnswerBankLocationPolicy>({ predicate: str }, { if_true: str, if_false: str });
const scopeItem = object<AnswerBankScopeItem>({ host: str, question: str }, { options: strings });

// The answer fields every entry kind shares with an override body. The three
// list fields are always present and null while the entry leaves them out.
const bodyKeys = {
  answer: str, fallback_text: str, fallback_any_option: bool, minimum: str, maximum: str, default_answer: str, currency: str, language: str,
  speaking: str, listening: str, reading: str, writing: str, answer_if_match: str, answer_if_no_match: str, abstain_if_ambiguous: bool,
  preferred: strings, proficiency_options: strings, selected: strings,
};
const bodyNullable = ["preferred", "proficiency_options", "selected"] as const;

const body = object<AnswerBankBody>({}, bodyKeys, bodyNullable);

const entry = object<AnswerBankEntry>(
  { id: str, description: str, kind: str },
  {
    ...bodyKeys,
    category: str, answer_key: str, input_types: strings, confidence: num, confidence_floor: num, examples: strings, exception_terms: strings,
    answer_if_exception: str, question_negation_terms: strings, numeric_threshold: numericThreshold, location_policy: locationPolicy,
    captured_scope: list(scopeItem), allowed_options: strings, linked_explain_id: str, skill_mode: str, skill_terms: strings,
    skill_blacklist_terms: strings, skill_negative_answer: str, placeholder: bool, needs_review: bool, pinned: bool, state: str, created_by: str,
    created_at: str, updated_at: str, stale_at: str, archived_at: str, stale_correction_count: safeInt,
  },
  bodyNullable,
);

const employer = object<AnswerBankEmployer>({ name: str, aliases: orEmpty(str) });
const override = object<AnswerBankOverride>({ id: str, body, fields: orEmpty(str) }, { kind: str });

const bank: Decoder<AnswerBank> = object<AnswerBank>({
  version: safeInt, employers: orEmpty(employer), entries: orEmpty(entry), overrides: dict(orEmpty(override)),
});


const shape = object<AnswerBankShape>({ type: str, choices: orEmpty(object<AnswerBankChoice>({ label: str, matches: orEmpty(str) })) });
export const answerBankFile: Decoder<AnswerBankFile> = object<AnswerBankFile>({
  exists: bool, revision: str, warnings: orEmpty(str), bank, defaultIds: orEmpty(str), shapes: dict(shape),
});


export const answerComposeResult: Decoder<AnswerComposeResult> = object<AnswerComposeResult>({ outcome: str, sources: orEmpty(str) }, { answer: str, reason: str });

export const answerBankSaved: Decoder<AnswerBankSaved> = object<AnswerBankSaved>({ revision: str, changed: bool });

const gap = object<AnswerBankGap>({ id: str, category: str, kind: str, blocking: bool });
const derived = object<AnswerBankDerived>({ id: str, step: str, answer: str });
const asked = object<AnswerBankAsked>({ question: str, kind: str, runId: str, host: str, runs: safeInt, everywhere: bool }, { options: orEmpty(str), entryId: str });

export const answerBankUnanswered: Decoder<AnswerBankUnanswered> = object<AnswerBankUnanswered>({
  unanswered: orEmpty(gap), blocking: orEmpty(gap), needsReview: orEmpty(gap), askMe: orEmpty(str), derived: orEmpty(derived), asked: orEmpty(asked),
});

export const answerBankSettings: Decoder<AnswerBankSettings> = object<AnswerBankSettings>({
  llmFallbackEnabled: bool, groundingMode: str, autoAcceptLegalDeclarations: bool, allowGoogleSignIn: bool, watchScans: bool, knowledgeSemanticRetrieval: bool,
});

export const answerBankTranscript: Decoder<AnswerBankTranscript> = object<AnswerBankTranscript>({ fileName: str });

const candidateReview = object<AnswerCandidateReview>({ verdict: str, reason: str, draft: str, cited: orEmpty(str) });

const candidate = object<AnswerCandidate>(
  {
    id: safeInt, key: str, type: str, state: str, source: str, targetEntryId: str, entryId: str, seedQuestion: str, questions: orEmpty(str),
    hosts: orEmpty(str), sections: orEmpty(str), options: orEmpty(str), multiple: bool, proposedKind: str, proposedAnswer: str, longForm: bool, flags: orEmpty(str), support: safeInt, firstSeenAt: time,
    lastSeenAt: time, createdAt: time, updatedAt: time, review: candidateReview,
  },
  { decidedAt: time },
);

export const answerEdit = object<AnswerEdit>({ fillId: safeInt, entryId: str, host: str, question: str, before: str, after: str });

export const answerCandidateList: Decoder<AnswerCandidateList> = object<AnswerCandidateList>({ candidates: orEmpty(candidate) }, { edits: orEmpty(answerEdit) });

export const answerCandidateDecision: Decoder<AnswerCandidateDecision> = object<AnswerCandidateDecision>({ candidate, entryId: str });

export const answerCandidateGenerateResult: Decoder<AnswerCandidateGenerateResult> = object<AnswerCandidateGenerateResult>({
  created: safeInt, refreshed: safeInt, reproposed: safeInt, corrections: safeInt, candidates: orEmpty(candidate),
});

const usageRow = object<AnswerBankUsageRow>(
  {
    entryId: str, matches: safeInt, applies: safeInt, abstains: safeInt, corrections: safeInt, lastMatchedAt: str, lastAppliedAt: str,
    lastCorrectedAt: str, lastActivityAt: str, exampleQuestions: orEmpty(str),
  },
  { averageConfidence: num },
);

export const answerBankUsage: Decoder<AnswerBankUsage> = object<AnswerBankUsage>({ windowDays: safeInt, rows: orEmpty(usageRow) });

