// Decoders for the Setup page and the Home setup card. Each mirrors a struct of
// internal/api/wire; the object decoder rejects unknown fields.
import type {
  DemoCreated, DocumentToolchain, IntakeDocInfo, IntakeField, IntakeItem, IntakeOption, IntakePreview, IntakeResult, IntakeSource, DoctorReport, DoctorResult, ResumeEntry, ResumeList, SetupApplied, SetupDraft, SetupField, SetupReason, SetupSecret, SetupState, SetupStatus,
  SetupStep, ToolchainBudget, ToolchainComponent, ToolchainExternal,
} from "../wire";
import { bool, dict, object, orEmpty, safeInt, str, type Decoder } from "./decode";
import { draftJob, draftStudy } from "./decoders_pages";

const reason = object<SetupReason>({ text: str }, { field: str, ackable: bool, blank: bool });
const field = object<SetupField>({ name: str, type: str, doc: str }, { required: bool, enum: orEmpty(str) });

export const setupStep: Decoder<SetupStep> = object<SetupStep>(
  {
    n: safeInt, id: str, title: str, phase: str, required: bool, status: str, reasons: orEmpty(reason), warnings: orEmpty(str), purpose: str, storage: str, guide: str,
    commands: orEmpty(str), fields: orEmpty(field),
  },
  { skipCost: str, needsHuman: bool, instruction: str, page: str },
);

const secret = object<SetupSecret>({ name: str, present: bool }, { savedAt: str });

export const setupStatus: Decoder<SetupStatus> = object<SetupStatus>({ demo: bool });

export const demoCreated: Decoder<DemoCreated> = object<DemoCreated>({ workspace: str, created: orEmpty(str), postings: safeInt, notes: orEmpty(str), start: str });

export const setupState: Decoder<SetupState> = object<SetupState>({ progress: safeInt, next: str, steps: orEmpty(setupStep), secrets: orEmpty(secret) });

const draft = object<SetupDraft>({ employment: orEmpty(draftJob), education: orEmpty(draftStudy), skills: orEmpty(str) });

export const setupApplied: Decoder<SetupApplied> = object<SetupApplied>(
  { step: setupStep, wrote: bool, notes: orEmpty(str), untouched: orEmpty(str) },
  { dryRun: bool, draft },
);

const doctorResult = object<DoctorResult>({ id: str, status: str, required: bool, summary: str }, { fix: str, step: str });
export const doctorReport: Decoder<DoctorReport> = object<DoctorReport>({ platform: str, results: orEmpty(doctorResult) });

const component = object<ToolchainComponent>({ name: str, status: str }, { version: str, detail: str, fix: str });
const external = object<ToolchainExternal>({ name: str, install: str, size: str });
const budget = object<ToolchainBudget>({ downloads: orEmpty(str), diskBytes: safeInt, external: orEmpty(external) });
export const documentToolchain: Decoder<DocumentToolchain> = object<DocumentToolchain>(
  { state: str, missing: orEmpty(str), smoke: str, components: orEmpty(component), budget },
  { code: str, fix: str },
);

const resumeEntry = object<ResumeEntry>({ id: str, label: str, tags: orEmpty(str), default: bool, description: str, fileName: str, size: safeInt });
export const resumeList: Decoder<ResumeList> = object<ResumeList>({ resumes: orEmpty(resumeEntry) });

const intakeSource = object<IntakeSource>({ label: str, hash: str, locator: str, origin: str });
const intakeOption = object<IntakeOption>({ value: str, sources: orEmpty(intakeSource) });
const intakeField = object<IntakeField>({ field: str, options: orEmpty(intakeOption) }, { profile: str });
const intakeItem = object<IntakeItem>(
  { id: str, type: str, kind: str, summary: str, entry: dict(str), sources: orEmpty(intakeSource), fields: orEmpty(intakeField), needsMonth: orEmpty(str) },
  { question: str },
);
const intakeDoc = object<IntakeDocInfo>({ name: str, label: str, kind: str, hash: str, signal: bool }, { duplicate: bool });

export const intakePreview: Decoder<IntakePreview> = object<IntakePreview>({ revision: str, documents: orEmpty(intakeDoc), items: orEmpty(intakeItem), notes: orEmpty(str) });
export const intakeResult: Decoder<IntakeResult> = object<IntakeResult>({ wrote: bool, revision: str, applied: orEmpty(intakeItem), notes: orEmpty(str) });

export const cvExtracted = object<{ markdown: string }>({ markdown: str });
