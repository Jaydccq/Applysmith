// Decoders for the profile and knowledge pages.
import type {
  ConfigBackupList, ConfigDocument, ConfigIssue, CVDraft, CVJob, CVStudy, ConfigSaved, KBCoverage, KBDraft, KBDraftFact, KBDraftList, KBDocEntry, KBDocument, KBDocumentList, KBGitHubAccount, KBGitHubRepo, KBGitHubRepos, KBGitHubSignIn, KBPageLink, KBPageRef, KBPrevious, KBLint, KBMiningEntry, KBMiningList, KBMiningQuestion, KBMiningSession, KBMiningValue, KBPrincipleGrade, KBQuadrantSuggestion, KBQuadrantSuggestions, KBFileNote, KBIngestReport, KBRepo, KBRepoList, KBRestored, KBUndo,
  KBSection, KBShapeGrade, KBStoryFinding, KBStoryProposal, KBStoryProposals, KBStorySaved, KBStoryUse, ResumeUploaded, RiskBaselineRow, RiskCooldown, RiskSignal, RiskView,
} from "../wire";
import { WireError, bool, dict, list, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const anyObject: Decoder<{ [key: string]: unknown }> = (v, path) => {
  if (typeof v !== "object" || v === null || Array.isArray(v)) throw new WireError(path, "expected an object");
  return v as { [key: string]: unknown };
};

const issue = object<ConfigIssue>({ code: str, message: str }, { path: str, line: safeInt });

export const configDocument: Decoder<ConfigDocument> = object<ConfigDocument>(
  { file: str, exists: bool, template: bool, content: str, revision: str, issues: orEmpty(issue) },
  { values: anyObject },
);

export const configBackupList: Decoder<ConfigBackupList> = object<ConfigBackupList>({ backups: orEmpty(object<ConfigBackupList["backups"][number]>({ savedAt: time, content: str })) });

export const configSaved: Decoder<ConfigSaved> = object<ConfigSaved>({ revision: str, changed: bool });

export const draftJob = object<CVJob>({ employer: str }, { title: str, start: str, end: str });
export const draftStudy = object<CVStudy>({ school: str }, { degree: str, start: str, end: str });

export const cvDraft: Decoder<CVDraft> = object<CVDraft>({ revision: str, employment: orEmpty(draftJob), education: orEmpty(draftStudy), skills: orEmpty(str), identity: dict(str), dismissed: orEmpty(str) });

export const resumeUploaded: Decoder<ResumeUploaded> = object<ResumeUploaded>({ file: str });

const lint = object<KBLint>({ code: str, message: str });

const docEntry: Decoder<KBDocEntry> = object<KBDocEntry>({
  id: str, kind: str, path: str, category: str, title: str, description: str, modifiedAt: str, fileHash: str, tags: orEmpty(str),
  principles: orEmpty(str), sourceType: str, sourceUrl: str, lint: orEmpty(lint),
});

const storyUse = object<KBStoryUse>({ storyId: str, candidacyId: safeInt, company: str, role: str });

export const kbDocumentList: Decoder<KBDocumentList> = object<KBDocumentList>({ entries: orEmpty(docEntry), uses: orEmpty(storyUse) }, { briefRunId: str });

const section = object<KBSection>({ heading: str, level: safeInt, text: str });

const pageLink = object<KBPageLink>({ label: str, path: str, id: str });
const pageRef = object<KBPageRef>({ id: str, title: str });

export const kbDocument: Decoder<KBDocument> = object<KBDocument>({ entry: docEntry, body: str, sections: orEmpty(section), hasPrevious: bool, links: orEmpty(pageLink), linkedFrom: orEmpty(pageRef) });

export const kbPrevious: Decoder<KBPrevious> = object<KBPrevious>({ savedAt: time, title: str, tags: orEmpty(str), body: str });


const principle = object<KBPrincipleGrade>({ id: str, name: str, grade: str, stories: orEmpty(str), missing: orEmpty(str), suggest: orEmpty(str), questions: orEmpty(str) });
const shape = object<KBShapeGrade>({ id: str, name: str, need: safeInt, have: safeInt, grade: str, stories: orEmpty(str) });

export const kbCoverage: Decoder<KBCoverage> = object<KBCoverage>({ principles: orEmpty(principle), shapes: orEmpty(shape) });

const finding = object<KBStoryFinding>({ code: str, severity: str, message: str });

export const kbStorySaved: Decoder<KBStorySaved> = object<KBStorySaved>({ draftId: safeInt, published: bool, path: str, findings: orEmpty(finding) });

const proposal = object<KBStoryProposal>({ title: str, principles: orEmpty(str), situation: str, task: str, action: str, result: str });

export const kbStoryProposals: Decoder<KBStoryProposals> = object<KBStoryProposals>({ proposals: orEmpty(proposal) });
export const kbMiningSession: Decoder<KBMiningSession> = object<KBMiningSession>(
  { id: str, quadrant: str, sourceType: str, state: str, notesFrom: str, fields: dict(object<KBMiningValue>({ text: str, source: str })), log: orEmpty(object<KBMiningEntry>({ role: str, field: str, text: str, at: str })), draftId: safeInt, createdAt: str, updatedAt: str, ignored: bool },
  { next: object<KBMiningQuestion>({ field: str, text: str }) },
);
export const kbMiningList: Decoder<KBMiningList> = object<KBMiningList>({ sessions: orEmpty(kbMiningSession) });
export const kbQuadrantSuggestions: Decoder<KBQuadrantSuggestions> = object<KBQuadrantSuggestions>({ quadrants: orEmpty(object<KBQuadrantSuggestion>({ quadrant: str, score: safeInt, reasons: orEmpty(str) })) });

const riskBaselineRow = object<RiskBaselineRow>({ source: str, runs: safeInt, visits: safeInt, challenges: safeInt, rateLimited: safeInt });
const riskSignal = object<RiskSignal>({ system: str, signal: str, at: time }, { host: str, runId: str, endsAt: time });
const riskCooldown = object<RiskCooldown>({ kind: str, system: str, signal: str, until: time }, { host: str });

export const riskView: Decoder<RiskView> = object<RiskView>({ days: safeInt, baseline: orEmpty(riskBaselineRow), signals: orEmpty(riskSignal), cooldowns: orEmpty(riskCooldown) });


const kbDraftFact = object<KBDraftFact>({ text: str, origin: str, pending: bool });
export const kbDraft: Decoder<KBDraft> = object<KBDraft>({ id: safeInt, revision: safeInt, status: str, kind: str, origin: str, category: str, snippetId: str, markdown: str, body: str, facts: orEmpty(kbDraftFact) }, { principles: list(str) });
export const kbDraftList: Decoder<KBDraftList> = object<KBDraftList>({ drafts: orEmpty(kbDraft) });

const fileNote = object<KBFileNote>({ path: str, reason: str });
const ingestReport = object<KBIngestReport>({
  status: str, commit: str, branch: str, listing: str, considered: num, read: num, reused: num, fetched: num,
  skipped: orEmpty(fileNote), redacted: orEmpty(fileNote), drafts: orEmpty(safeInt), notes: orEmpty(str),
});
const kbRepo = object<KBRepo>({ id: safeInt, owner: str, name: str, branch: str, commitSha: str, status: str, report: ingestReport, createdAt: str, ingestedAt: str }, { readingRunId: str });
export const kbRepoList: Decoder<KBRepoList> = object<KBRepoList>({ repos: orEmpty(kbRepo) });
export const kbRepoAdded: Decoder<KBRepo> = kbRepo;
export const kbGitHubAccount: Decoder<KBGitHubAccount> = object<KBGitHubAccount>({ state: str, login: str, source: str }, { userCode: str, verificationUri: str, expiresAt: time, error: str });
export const kbGitHubSignIn: Decoder<KBGitHubSignIn> = object<KBGitHubSignIn>({ userCode: str, verificationUri: str, expiresAt: time });
const githubRepo = object<KBGitHubRepo>({ fullName: str, private: bool, description: str, pushedAt: str, defaultBranch: str, language: str, alreadyAdded: bool });
export const kbGitHubRepos: Decoder<KBGitHubRepos> = object<KBGitHubRepos>({ repos: orEmpty(githubRepo), page: safeInt, more: bool });
export const kbUndo: Decoder<KBUndo> = object<KBUndo>({ undo: str });
export const kbRestored: Decoder<KBRestored> = object<KBRestored>({ kind: str, name: str });
