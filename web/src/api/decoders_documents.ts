// Decoders for the stored text of a document version.
import type {
  DocumentBullet, DocumentCompetency, DocumentDiffLine, DocumentEntryContent, DocumentLetterContent, DocumentProposal, DocumentRefineTurn, DocumentRefinement,
  DocumentEvidence, DocumentResumeContent, DocumentSource,
} from "../wire";
import { bool, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const bullet: Decoder<DocumentBullet> = object<DocumentBullet>({ text: str, evidence: orEmpty(str) });
const entry: Decoder<DocumentEntryContent> = object<DocumentEntryContent>({ entry: str, bullets: orEmpty(bullet) });
const competency: Decoder<DocumentCompetency> = object<DocumentCompetency>({ label: str, text: str, evidence: orEmpty(str) });

const resume: Decoder<DocumentResumeContent> = object<DocumentResumeContent>({
  profile: str, profileEvidence: orEmpty(str), competencies: orEmpty(competency), experience: orEmpty(entry), projects: orEmpty(entry),
});

const letter: Decoder<DocumentLetterContent> = object<DocumentLetterContent>({
  opening: str, openingEvidence: orEmpty(str), body: str, bullets: orEmpty(bullet), closing: str, companyFacts: orEmpty(str),
});

export const documentSource: Decoder<DocumentSource> = object<DocumentSource>(
  { id: safeInt, kind: str, version: safeInt, frozen: bool, text: str, evidence: orEmpty(object<DocumentEvidence>({ id: str, source: str, text: str })) },
  { resume, letter },
);

const diffLine: Decoder<DocumentDiffLine> = object<DocumentDiffLine>({ op: str, text: str });
const turn: Decoder<DocumentRefineTurn> = object<DocumentRefineTurn>({ kind: str }, { text: str, decision: str });

const proposal: Decoder<DocumentProposal> = object<DocumentProposal>(
  { id: safeInt, documentId: safeInt, kind: str, instruction: str, text: str, diff: orEmpty(diffLine), createdAt: time },
  { resume, letter },
);

export const documentRefinement: Decoder<DocumentRefinement> = object<DocumentRefinement>({ turns: orEmpty(turn) }, { pending: proposal }, ["pending"]);
