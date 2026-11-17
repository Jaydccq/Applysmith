// Fixtures for the Setup page and the Home setup card: the 18-step catalog as
// the state endpoint answers it. Names and hosts are the reserved example ones.
import type { ConfigDocument, DoctorReport, DocumentToolchain, LLMRouting, ResumeList, SetupApplied, SetupField, SetupState, SetupStep } from "../wire";
import { fakeApi, ok, type FakeApi, type Handler } from "./api";
import { health } from "./fixtures_gmail";

const f = (name: string, type: string, doc: string, extra: Partial<SetupField> = {}): SetupField => ({ name, type, doc, ...extra });

const IDENTITY: SetupField[] = [
  f("full_name", "string", "Full legal name; typed verbatim into application forms."),
  f("preferred_name", "string", "Optional preferred name."),
  f("email", "string", "Contact email; must be a real address."),
  f("phone", "string", "Phone number in international form."),
  f("city", "string", "City of residence."),
  f("region", "string", "State, province or region of residence."),
  f("country", "string", "Country of residence as an ISO 3166-1 alpha-2 code."),
  f("street_address", "string", "Street address, or @ask-me to leave the field for yourself."),
  f("postal_code", "string", "Postal code, or @ask-me to leave the field for yourself."),
  f("linkedin", "string", "Optional https URL on linkedin.com."),
  f("other_links", "string-list", "Optional additional https URLs."),
];

const VISA = ["H-1B", "H-1B1", "E-3", "E-2", "L-1", "O-1", "TN", "other"];
const STATUSES = ["citizen", "permanent_resident", "work_visa", "student_work_authorization", "other", "not_authorized", "prefer_not_to_say"];

const WORK_AUTH: SetupField[] = [
  f("authorization_status", "string", "Your work authorization status; there is no default.", { enum: STATUSES }),
  f("status_detail", "string", "For work_visa the visa class; for student_work_authorization the end date YYYY-MM-DD.", { enum: VISA }),
  f("authorized_countries", "string-list", "ISO 3166-1 alpha-2 codes of the countries where you can work."),
  f("requires_sponsorship_now", "bool", "Whether you need sponsorship now; never inferred from the status."),
  f("requires_sponsorship_future", "bool", "Whether you will need sponsorship in the future; a separate question."),
  f("work_modes", "string-list", "Any of remote, hybrid, onsite.", { enum: ["remote", "hybrid", "onsite"] }),
];

const TARGETS: SetupField[] = [
  f("roles", "object-list", "Target roles: title, seniority and priority; replaces the stored list.", { required: true }),
  f("locations", "string-list", "Target locations, or \"anywhere or remote\"; replaces the stored list."),
  f("compensation", "object", "currency, period, minimum and target; minimum not above target."),
  f("batch_skip_companies", "string-list", "Employers that batch applying leaves to you."),
  f("skip_list_confirmed", "bool", "Confirm the batch skip list as it is now."),
];

export const CLI_LIMITS = "A command-line preset supports plain completion only: the Ask Agent will run its deterministic router only, and embeddings are disabled.";

const LLM: SetupField[] = [
  f("preset", "string", "A one-key provider preset.", { enum: ["openrouter", "deepseek", "openai", "anthropic", "gemini", "chatgpt"] }),
  f("cli", "string", "A signed-in command-line tool instead of a key.", { enum: ["codex", "claude"] }),
  f("offline", "bool", "Offline simulation for a user with no key yet."),
  f("fallback", "string", "Optional second preset."),
  f("acknowledge_cli_limits", "bool", "Required with cli: you have read that " + CLI_LIMITS),
];

const PACK_DOC = "The schedule pack to create: none creates nothing, daily scans each enabled source from 07:00, twice-daily adds a second round from 17:00.";

export const EDGAR_DOC = "Every request to the SEC carries your profile email in its User-Agent header, which sends that address to the SEC. The source is skipped while the profile email is blank.";

const FIELDS: Record<string, SetupField[]> = {
  workspace: [],
  identity: IDENTITY,
  cv: [
    f("markdown", "string", "The CV as markdown."),
    f("employment", "object-list", "Confirmed employment entries."),
    f("education", "object-list", "Confirmed education entries."),
    f("skills", "object-list", "Confirmed skills."),
    f("review_complete", "bool", "Say the extracted draft was reviewed."),
  ],
  "work-authorization": WORK_AUTH,
  targets: TARGETS,
  story: [f("narrative", "string", "The narrative as markdown."), f("voice", "string", "Samples of your own writing.")],
  resumes: [f("roster", "object-list", "The résumé roster; replaces the stored one.")],
  "answer-bank": [f("answers", "object", "Entry id to answer or @ask-me."), f("confirm_review", "bool", "Confirm the reviewed defaults.")],
  llm: LLM,
  browser: [],
  gmail: [],
  discovery: [f("pack", "string", PACK_DOC, { required: true, enum: ["none", "daily", "twice-daily"] }), f("seed_registry", "bool", "Fetch the public board lists into the ATS registry now; `applysmith registry seed` runs it again."), f("enable_edgar", "bool", EDGAR_DOC), f("acknowledge_edgar_disclosure", "bool", "Required with enable_edgar: you have read the disclosure."), f("use_target_roles", "bool", "Use my target roles in portals.yml.")],
};

interface Row {
  id: string;
  title: string;
  phase: string;
  required: boolean;
  human?: { instruction: string; page: string };
}

// The catalog in order, as the server lists it.
const CATALOG: Row[] = [
  { id: "prerequisites", title: "Check this computer", phase: "foundations", required: true },
  { id: "workspace", title: "Create the workspace", phase: "foundations", required: true },
  { id: "cv", title: "Your CV", phase: "about-you", required: true },
  { id: "identity", title: "Your identity and contact details", phase: "about-you", required: true },
  { id: "work-authorization", title: "Work authorization and location", phase: "about-you", required: true },
  { id: "targets", title: "What you are looking for", phase: "about-you", required: true },
  { id: "story", title: "Your story and voice", phase: "about-you", required: true },
  { id: "resumes", title: "Résumé PDFs", phase: "about-you", required: true },
  { id: "answer-bank", title: "Answer bank review", phase: "about-you", required: true },
  { id: "llm", title: "Language-model provider", phase: "connections", required: true },
  { id: "browser", title: "Apply window", phase: "connections", required: true, human: { instruction: "Open the apply window and sign in once to the job sites you use.", page: "/browser" } },
  { id: "gmail", title: "Gmail", phase: "connections", required: false, human: { instruction: "Turn on 2-Step Verification for your Google Account, then create an app password (Google Account, Security, App passwords). Enter your Gmail address and the app password in Settings, Gmail and press Connect.", page: "/settings#settings-gmail" } },
  { id: "telegram", title: "Telegram", phase: "connections", required: false, human: { instruction: "Create a bot with BotFather, save its token, then send the pairing code to the bot.", page: "/settings#settings-telegram" } },
  { id: "credentials", title: "Sign-in credentials", phase: "connections", required: false, human: { instruction: "Store a shared default sign-in. Passwords are typed only into the key store.", page: "/settings#settings-credentials" } },
  { id: "captcha", title: "Captcha solver", phase: "connections", required: false, human: { instruction: "Create an account with the solver service and save its key.", page: "/settings#settings-captcha" } },
  { id: "discovery", title: "Job sources and schedule", phase: "tools", required: false },
  { id: "extension", title: "Chrome extension", phase: "tools", required: false, human: { instruction: "Load the unpacked extension in Chrome, then type the pairing code into it.", page: "/settings#settings-extension" } },
  { id: "orientation", title: "Orientation", phase: "start", required: true },
];

export function setupStep(id: string, over: Partial<SetupStep> = {}): SetupStep {
  const i = CATALOG.findIndex((r) => r.id === id);
  const row = CATALOG[i];
  return {
    n: i, id, title: row.title, phase: row.phase, required: row.required, status: "complete", reasons: [], warnings: [],
    purpose: `Without ${row.title.toLowerCase()} the next steps cannot work.`, storage: "the profile file", guide: `docs/setup/${String(i).padStart(2, "0")}-${id}.md`,
    commands: [`applysmith setup apply ${id} --file <path|->`], fields: FIELDS[id] ?? [],
    ...(row.required ? {} : { skipCost: `Skipping ${row.title} loses what it adds.` }),
    ...(row.human ? { needsHuman: true, instruction: row.human.instruction, page: row.human.page, commands: [`applysmith ${id} connect`] } : {}),
    ...over,
  };
}

// setupState builds the state endpoint's answer. Progress and next follow the
// steps the way the server computes them: the satisfied required steps over the
// required ones, and the first required step that is not complete.
export function setupState(patch: Record<string, Partial<SetupStep>> = {}): SetupState {
  const steps = CATALOG.map((r) => setupStep(r.id, patch[r.id]));
  const required = steps.filter((s) => s.required);
  const done = required.filter((s) => s.status === "complete").length;
  return { progress: Math.round((done * 100) / required.length), next: required.find((s) => s.status !== "complete")?.id ?? "", steps, secrets: [] };
}

export const doctorClean: DoctorReport = {
  platform: "macOS 14 on arm64 is supported.",
  results: [
    { id: "workspace.files", status: "pass", required: true, summary: "The workspace holds every file." },
    { id: "llm.test", status: "pass", required: true, summary: "The last test call succeeded." },
    { id: "runtime.port", status: "pass", required: true, summary: "The server port is free." },
  ],
};

export const toolchain: DocumentToolchain = {
  state: "unavailable",
  code: "tex-missing",
  missing: ["tectonic", "pdftoppm"],
  smoke: "none",
  fix: "Install TeX and Poppler, then check again.",
  components: [
    { name: "python", status: "ok", version: "3.12.1" },
    { name: "tectonic", status: "missing", fix: "install TeX (macOS: `brew install tectonic`)" },
    { name: "pdftoppm", status: "missing", fix: "install Poppler (macOS: `brew install poppler`)" },
  ],
  budget: { downloads: ["LaTeX packages the app caches itself"], diskBytes: 41943040, external: [
      { name: "TeX (tectonic)", install: "install TeX (macOS: `brew install tectonic`)", size: "90 MB" },
      { name: "Python 3", install: "use the system Python, or install one (macOS: `brew install python`)", size: "120 MB" },
      { name: "Poppler (pdftoppm)", install: "install Poppler (macOS: `brew install poppler`)", size: "30 MB" },
    ] },
};

export const profileDoc = (values: Record<string, unknown> = {}): ConfigDocument => ({ file: "profile.yml", exists: true, template: false, content: "", revision: "r1", issues: [], values });

export const llmRouting: LLMRouting = {
  preset: "",
  dailyUsdCap: 5,
  capSpentUsd: 0,
  providers: [],
  presets: [
    { name: "openrouter", label: "OpenRouter", kind: "openai-compatible", keyUrl: "https://openrouter.ai/keys", inSetup: true, subscription: false, keyConfigured: false },
    { name: "deepseek", label: "DeepSeek", kind: "openai-compatible", keyUrl: "https://platform.deepseek.com/api_keys", inSetup: true, subscription: false, keyConfigured: true },
    { name: "codex", label: "Codex", kind: "cli", program: "codex", inSetup: true, subscription: true, keyConfigured: false },
    { name: "claude", label: "Claude", kind: "cli", program: "claude", inSetup: true, subscription: true, keyConfigured: false },
    { name: "chatgpt", label: "ChatGPT plan (sign in)", kind: "chatgpt", inSetup: true, subscription: true, keyConfigured: false },
  ],
  tasks: [],
  groups: {},
  rests: [],
  knownModels: [],
};

export const resumes: ResumeList = { resumes: [{ id: "general", label: "General", tags: [], default: true, description: "", fileName: "general.pdf", size: 52_000 }] };

export const applied = (step: SetupStep, over: Partial<SetupApplied> = {}): SetupApplied => ({ step, wrote: true, notes: [], untouched: [], ...over });

interface SetupApi extends FakeApi {
  /** Replaces what the state endpoint answers from now on. */
  serve(state: SetupState): void;
}

// setupApi serves the Setup page's routes. Every apply answers with the step as
// it stands; a test adds its own handler to say otherwise.
export function setupApi(state: SetupState, routes: Record<string, Handler> = {}): SetupApi {
  let current = state;
  const api = fakeApi({
    "GET /api/setup/state": () => ok(current),
    "GET /api/setup/status": () => ok({ demo: false }),
    "GET /api/config/profile": () => ok(profileDoc()),
    "GET /api/config/narrative": () => ok({ ...profileDoc(), file: "narrative.md", template: true, content: "# Seed" }),
    "GET /api/config/voice": () => ok({ ...profileDoc(), file: "voice.md", exists: false }),
    "GET /api/llm/routing": () => ok(llmRouting),
    "GET /api/resumes": () => ok(resumes),
    "GET /api/documents/toolchain": () => ok(toolchain),
    "POST /api/setup/doctor": () => ok(doctorClean),
    "GET /api/gmail/health": () => ok(health({ connected: false, login: "" })),
    ...routes,
  });
  for (const s of state.steps) {
    for (const kind of ["apply", "ack", "skip", "unskip"] as const) {
      const route = `POST /api/setup/steps/${s.id}/${kind}`;
      if (!(route in routes)) api.on(route, () => ok(kind === "apply" ? applied(s) : s));
    }
  }
  return Object.assign(api, { serve: (next: SetupState) => void (current = next) });
}
