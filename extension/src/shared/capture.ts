// Reading the current page as a job posting: its URL, title, main text, the
// structured posting data when the page carries it, and a guess at whether it
// is a posting at all. It runs in the panel's isolated world and only reads.
import { htmlToText, jobPostingOf, postingLocation, readJobDescription, resolveJobContext } from "../../../engine/src/capture";
import { siteOf } from "../../../engine/src/sites";
import { looksLikeApplicationForm } from "./site";
import type { Capture, DetectionLabel, JobContext, PageReading, PostingHint } from "./messages";
import { send } from "./rpc";

export const MAX_TEXT = 200_000;
export const HYDRATION_WAIT_MS = 5000;
const HYDRATION_STEP_MS = 250;
/** A page with less text than this is probably still rendering. */
const THIN_TEXT = 300;

const CLOSED = /(no longer (accepting|available|open)|(position|role|job|posting|vacancy) (has been |is |was )?(filled|closed|expired)|(job|posting|listing|application) (has )?expired|applications? (are |is |have )?(now )?closed|not accepting (any more )?applications)/i;

const SIGNALS: [string, RegExp][] = [
  ["responsibilities", /\bresponsibilit(y|ies)\b|\bwhat you('|’)?ll do\b/i],
  ["qualifications", /\bqualifications?\b|\brequirements?\b|\bwhat we('|’)?re looking for\b|\byou have\b/i],
  ["apply control", /\bapply (now|for this|to this)\b|\bsubmit (your )?application\b|\beasy apply\b/i],
  ["compensation", /\bsalary\b|\bcompensation\b|\bpay range\b|\$\s?\d{2,3}[,.]?\d{0,3}\s?(k|\/)/i],
  ["benefits", /\bbenefits\b|\b401\(?k\)?\b|\bpaid time off\b|\bequity\b/i],
  ["employment type", /\bfull[- ]time\b|\bpart[- ]time\b|\bcontract\b|\bremote\b|\bhybrid\b/i],
];

interface Ld {
  title?: string;
  description?: string;
  hiringOrganization?: { name?: string } | string;
}

function mainText(doc: Document): string {
  const root = doc.querySelector<HTMLElement>("main, [role=main], article") ?? doc.body;
  if (!root) return "";
  const raw = (root as HTMLElement).innerText || root.textContent || "";
  return raw.replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
}

/** The hiring systems and job boards: the page of one names the system, never the employer. */
const VENDOR = /^(greenhouse|lever|ashby|ashbyhq|workday|myworkdayjobs|icims|smartrecruiters|taleo|oracle|successfactors|sap|bamboohr|jobvite|workable|rippling|paylocity|paycom|ukg|adp|dayforce|ceridian|pinpoint|recruitee|teamtailor|breezy|gem|eightfold|linkedin|indeed|glassdoor|ziprecruiter)( (hr|jobs|careers|recruiting|talent|ats))?$/i;
/** A step of the form is not a company, with or without the noun that trails it ("EEO questions", "login page"). */
const STEP_HEADING = /^(log ?in|sign ?in|sign ?up|register|eeo|equal employment( opportunity)?|candidate (profile|home|portal)|(voluntary )?self[- ]identification|my (information|experience)|review|questions?|application( form)?|apply|thank you)\b( (questions?|page|form|step|section|survey))?$/i;
const NOISE_WORDS = /\b(careers?|jobs?|hiring|recruiting|talent)\b/gi;
const HOSTNAME = /^[\w-]+(\.[\w-]+)+$/;

/** The employer a page name stands for, or undefined when the name is the system's, a step's or an address. */
export function cleanEmployer(raw: string | undefined): string | undefined {
  let name = (raw ?? "").replace(/\s+/g, " ").trim().replace(/^(careers?|jobs?) (at|with|@) /i, "");
  if (HOSTNAME.test(name) || STEP_HEADING.test(name)) return undefined;
  name = name.replace(NOISE_WORDS, " ").replace(/\s+/g, " ").trim();
  if (name === "" || VENDOR.test(name) || STEP_HEADING.test(name)) return undefined;
  return name;
}

/**
 * The document titles of systems that name the role and the employer in a fixed
 * order. A generic split would take "Job Application for" as the role, or the
 * employer as the role where the employer comes first.
 */
const TITLE_SHAPES: Record<string, RegExp> = {
  greenhouse: /^Job Application for (?<role>.+) at (?<company>.+)$/i,
  "greenhouse-eu": /^Job Application for (?<role>.+) at (?<company>.+)$/i,
  lever: /^(?<company>.+?) [-–—] (?<role>.+)$/,
  ashby: /^(?<role>.+) @ (?<company>.+)$/,
};

function guessTitle(doc: Document): { role?: string; company?: string } {
  const og = cleanEmployer(doc.querySelector<HTMLMetaElement>('meta[property="og:site_name"]')?.content);
  const shape = TITLE_SHAPES[siteOf(doc.location?.href ?? "") ?? ""]?.exec(doc.title.trim())?.groups;
  if (shape?.role && shape.company) return { role: shape.role.trim(), company: og || cleanEmployer(shape.company) };
  const parts = doc.title.split(/\s+(?:[-|–—]|at)\s+/).map((p) => p.trim()).filter(Boolean);
  return { role: parts[0] || undefined, company: og || (parts.length > 1 ? cleanEmployer(parts[parts.length - 1]) : undefined) };
}

function detect(url: string, text: string, structured: boolean): Capture["detection"] {
  const signals: string[] = [];
  let score = 0;
  if (structured) {
    signals.push("structured job posting data");
    score += 0.6;
  }
  if (/(^|[^a-z])(jobs?|careers?|positions?|openings?|apply)([^a-z]|$)/i.test(new URL(url).pathname)) {
    signals.push("job-like address");
    score += 0.15;
  }
  for (const [name, re] of SIGNALS) {
    if (re.test(text)) {
      signals.push(name);
      score += 0.12;
    }
  }
  const confidence = Math.min(1, Math.round(score * 100) / 100);
  const label: DetectionLabel = confidence >= 0.7 ? "job_posting" : confidence >= 0.4 ? "likely_job_posting" : "not_job_posting";
  return { label, confidence, signals };
}

/** The company and role the page names, without reading its text; cheap enough for a click handler. */
export function postingFacts(doc: Document): { company?: string; role?: string } {
  const posting = jobPostingOf(doc) as Ld | undefined;
  const guess = guessTitle(doc);
  const org = posting?.hiringOrganization;
  return { company: (typeof org === "string" ? org : org?.name) || guess.company, role: posting?.title || guess.role };
}

export interface CaptureOptions {
  doc?: Document;
  sleep?: (ms: number) => Promise<void>;
}

/** Reads the page; a page that is still rendering is read again for up to 5 s. */
export async function capturePage(opts: CaptureOptions = {}): Promise<Capture> {
  const doc = opts.doc ?? document;
  const sleep = opts.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  let text = mainText(doc);
  for (let waited = 0; text.length < THIN_TEXT && waited < HYDRATION_WAIT_MS; waited += HYDRATION_STEP_MS) {
    await sleep(HYDRATION_STEP_MS);
    text = mainText(doc);
  }
  const url = doc.location?.href ?? "";
  const ld = jobPostingOf(doc);
  const posting = ld as Ld | undefined;
  const guess = guessTitle(doc);
  const org = posting?.hiringOrganization;
  const structured = posting !== undefined;
  const fromLd = posting?.description ? htmlToText(window, posting.description) : "";
  const body = (text.length >= THIN_TEXT || !fromLd ? text : fromLd).slice(0, MAX_TEXT);
  const closed = CLOSED.exec(body.slice(0, 4000))?.[0];
  const cap: Capture = {
    url,
    title: doc.title,
    text: body,
    company: (typeof org === "string" ? org : org?.name) || guess.company,
    role: posting?.title || guess.role,
    location: postingLocation(ld),
    structured,
    detection: detect(url, body, structured),
  };
  if (closed) cap.closed = closed;
  return cap;
}

/**
 * One read of the page for the job context, without waiting for it to render:
 * the facts the page names, its live description and where that came from,
 * and whether it is a posting or an application form.
 */
export function readPosting(doc: Document = document): PageReading {
  const url = doc.location?.href ?? "";
  // The one rule for "this page holds an application form", the submission recorder's too.
  const form = looksLikeApplicationForm(doc);
  const jd = readJobDescription(doc, form);
  const ld = jobPostingOf(doc);
  const facts = postingFacts(doc);
  const reading: PageReading = {
    url,
    title: doc.title,
    jdText: jd.text.slice(0, MAX_TEXT),
    posting: url !== "" && (siteOf(url) !== null || detect(url, mainText(doc).slice(0, MAX_TEXT), ld !== undefined).label !== "not_job_posting"),
    form,
  };
  if (jd.source) reading.jdSource = jd.source;
  if (facts.company) reading.company = facts.company;
  if (facts.role) reading.role = facts.role;
  const location = postingLocation(ld);
  if (location) reading.location = location;
  return reading;
}

/** The job this page is about, as the worker resolves it for the tab; this page alone when the worker cannot answer. */
export async function pageJob(doc: Document = document): Promise<JobContext> {
  const reading = readPosting(doc);
  const local = () => resolveJobContext({ page: reading });
  try {
    return (await send<JobContext | undefined>({ kind: "context.resolve", reading })) ?? local();
  } catch {
    return local();
  }
}

/** What the answer and résumé requests need to know about the job. */
export function postingOf(c: JobContext): PostingHint {
  return { company: c.company, title: c.title, url: c.url, jdText: c.jdText, location: c.location };
}
