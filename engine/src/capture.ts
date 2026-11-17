// Clean-room: docs/provenance.md#CR-ENGINE
// What the posting page says about itself: the job description, the company,
// the title and the place. Only fields the caller left empty are filled.
import { collapse, flatText, winOf, type Win } from "./dom";
import type { PostingContext } from "./protocol";
import { ariaHiddenBlocks, isRendered } from "./visibility";

export const MAX_JD_CHARS = 8000;
/** A body shorter than this is page furniture, not a posting. */
export const MIN_JD_CHARS = 200;

interface LdPosting {
  title?: string;
  description?: string;
  company?: string;
  location?: string;
}

function asArray<T>(v: T | T[] | undefined): T[] {
  return v === undefined ? [] : Array.isArray(v) ? v : [v];
}

function isJobPosting(node: Record<string, unknown>): boolean {
  return asArray(node["@type"] as string | string[] | undefined).some((t) => String(t).toLowerCase() === "jobposting");
}

/** Every JSON-LD node of the document in document order, @graph members included. */
function* jsonLdNodes(doc: Document): Generator<Record<string, unknown>> {
  for (const el of doc.querySelectorAll('script[type="application/ld+json"]')) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(el.textContent ?? "");
    } catch {
      continue;
    }
    const stack: unknown[] = [parsed];
    while (stack.length) {
      const n = stack.pop();
      if (Array.isArray(n)) for (let i = n.length - 1; i >= 0; i--) stack.push(n[i]);
      else if (n && typeof n === "object") {
        yield n as Record<string, unknown>;
        stack.push((n as Record<string, unknown>)["@graph"]);
      }
    }
  }
}

const text = (v: unknown): string => (typeof v === "string" ? collapse(v) : "");

/** A schema.org value that is a name or an object with one. */
const nameOf = (v: unknown): string => (v && typeof v === "object" ? text((v as Record<string, unknown>).name) : text(v));

function placeOf(address: unknown): string {
  if (!address || typeof address !== "object") return text(address);
  const a = address as Record<string, unknown>;
  return [text(a.addressLocality), text(a.addressRegion), nameOf(a.addressCountry)].filter(Boolean).join(", ");
}

/**
 * The posting's own location from its JobPosting. A telecommute posting is "Remote", with the one country its
 * applicants must live in when it names one; any other posting is its first place, "City, Region, Country".
 */
export function postingLocation(posting: Record<string, unknown> | undefined): string | undefined {
  if (!posting) return undefined;
  const countries = [...new Set(asArray(posting.applicantLocationRequirements as unknown).map(nameOf).filter(Boolean))];
  if (asArray(posting.jobLocationType as unknown).includes("TELECOMMUTE")) return countries.length === 1 ? `Remote, ${countries[0]}` : "Remote";
  const places = asArray(posting.jobLocation as unknown)
    .map((l) => (l && typeof l === "object" ? placeOf((l as Record<string, unknown>).address ?? l) : text(l)))
    .filter(Boolean);
  return places[0] ?? countries[0];
}

/** The document's schema.org JobPosting: of several (a board page lists many) the one with the longest description. */
export function jobPostingOf(doc: Document): Record<string, unknown> | undefined {
  let best: Record<string, unknown> | undefined;
  const length = (n: Record<string, unknown> | undefined) => (typeof n?.description === "string" ? n.description.length : 0);
  for (const n of jsonLdNodes(doc)) if (isJobPosting(n) && (!best || length(n) > length(best))) best = n;
  return best;
}

/** Markup to plain text without loading anything: the parse happens in an inert document. */
export function htmlToText(win: Win, html: string): string {
  const parse = (s: string): string => {
    const doc = new win.DOMParser().parseFromString(s, "text/html");
    doc.querySelectorAll("script, style, noscript, template").forEach((n) => n.remove());
    doc.querySelectorAll("br, p, li, div, h1, h2, h3, h4, h5, h6, tr").forEach((n) => n.append(" "));
    return collapse(doc.body?.textContent ?? "");
  };
  let out = parse(html);
  // A description that was escaped once more arrives as visible tags.
  if (/<\/?[a-z][^>]*>/i.test(out)) out = parse(out);
  return out;
}

/** Every JobPosting the page declares. */
export function jsonLdPostings(doc: Document, win: Win): LdPosting[] {
  const out: LdPosting[] = [];
  for (const node of jsonLdNodes(doc)) {
    if (!isJobPosting(node)) continue;
    out.push({
      title: text(node.title),
      description: typeof node.description === "string" ? htmlToText(win, node.description) : "",
      company: nameOf(node.hiringOrganization),
      location: postingLocation(node) ?? "",
    });
  }
  return out;
}

/** Sentences in order, each kept once: boards repeat a summary above and below the fold. */
export function dedupeSentences(body: string): string {
  const seen = new Set<string>();
  const kept: string[] = [];
  let start = 0;
  const flush = (end: number) => {
    const s = body.slice(start, end).trim();
    start = end;
    if (!s) return;
    const key = s.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    kept.push(s);
  };
  for (let i = 0; i < body.length; i++) {
    const c = body[i];
    if ((c === "." || c === "!" || c === "?") && (i + 1 === body.length || /\s/.test(body[i + 1] as string))) flush(i + 1);
  }
  flush(body.length);
  return kept.join(" ");
}

const FURNITURE = "form, nav, footer, header, [role=navigation], [role=banner], [role=contentinfo]";

/** The visible page text outside forms and navigation. */
export function strippedPageText(doc: Document): string {
  if (!doc.body) return "";
  return flatText(doc.body, (el) => el.matches(FURNITURE));
}

/**
 * The job description: the longer of the page text and the structured
 * description, because a script-built page ships the posting only in the
 * latter and a server-rendered page only in the former. Under 200 characters
 * it is no posting at all.
 */
export function pageDescription(doc: Document, win: Win): string {
  const ld = jsonLdPostings(doc, win)
    .map((p) => p.description ?? "")
    .sort((a, b) => b.length - a.length)[0] ?? "";
  const body = dedupeSentences(strippedPageText(doc));
  const best = ld.length > body.length ? ld : body;
  const capped = best.slice(0, MAX_JD_CHARS).trim();
  return capped.length >= MIN_JD_CHARS ? capped : "";
}

/**
 * The page's role heading: the first h1 a person can see. A hidden panel's
 * heading ("Application submitted") sits earlier in some boards' markup and is
 * not it. The document title is the last resort.
 */
export function pageHeading(doc: Document): string {
  for (const h of Array.from(doc.querySelectorAll("h1"))) {
    if (!isRendered(h) || ariaHiddenBlocks(h)) continue;
    const t = collapse(flatText(h));
    if (t) return t;
  }
  return collapse(doc.title ?? "");
}

/** The caller's posting context with the page's own facts added where the caller gave none. */
export function withPagePosting(given: PostingContext, doc: Document, win: Win): PostingContext {
  const out: PostingContext = { ...given };
  const postings = jsonLdPostings(doc, win).sort((a, b) => (b.description ?? "").length - (a.description ?? "").length);
  const lead = postings[0];
  if (!out.company && lead?.company) out.company = lead.company;
  if (!out.title) out.title = lead?.title || pageHeading(doc) || undefined;
  if (!out.title) delete out.title;
  if (!out.location) {
    const place = postings.map((p) => p.location).find(Boolean);
    if (place) out.location = place;
  }
  if (!out.jdText) {
    const jd = pageDescription(doc, win);
    if (jd) out.jdText = jd;
  }
  return out;
}

// Which job a page belongs to, and where its job description comes from. The
// order is fixed: the person's own pin beats everything; then the page (the
// live description on a posting page; the anchored posting on an application
// form; a live description on a form only when it came from structured posting
// data or a known description container, never the form's rendered text); then
// the context of the last autofill. An anchor is made only from a posting page
// with at least 200 characters of description, so an application form never
// overwrites a good description with its own chrome.
/** Where a live job description was read from. */
export type JdSource = "structured" | "container" | "rendered";

/** What one read of the current page found. */
export interface PageReading {
  url: string;
  title?: string;
  company?: string;
  role?: string;
  /** The posting's own location: "City, Region, Country" or "Remote". */
  location?: string;
  jdText: string;
  jdSource?: JdSource;
  /** The page reads as a job posting (a known job site or posting signals). */
  posting: boolean;
  /** The page holds an application form. */
  form: boolean;
}

/** "board" is an anchor read from the public board of a page that holds only the form. */
export type JobContextSource = "pinned" | "page" | "anchor" | "board" | "autofill" | "none";

/** The job a request is about. */
export interface JobContext {
  url: string;
  company?: string;
  title?: string;
  location?: string;
  jdText?: string;
  source: JobContextSource;
}

export const MIN_ANCHOR_JD_CHARS = 200;

/** The anchor a posting page leaves for the application form that follows it, or null. */
export function anchorFrom(page: PageReading): JobContext | null {
  const jdText = page.jdText.trim();
  if (page.form || !page.posting || jdText.length < MIN_ANCHOR_JD_CHARS) return null;
  return { url: page.url, jdText, source: "anchor", ...facts(page) };
}

function facts(page: PageReading): Pick<JobContext, "company" | "title" | "location"> {
  return {
    ...(page.company ? { company: page.company } : {}),
    ...(page.role || page.title ? { title: page.role || page.title } : {}),
    ...(page.location ? { location: page.location } : {}),
  };
}

const hasJd = (c: JobContext | null | undefined): c is JobContext => Boolean(c?.jdText?.trim());

/** The job context in precedence order: pin, page, last autofill. */
export function resolveJobContext(input: { pinned?: JobContext | null; page: PageReading; anchor?: JobContext | null; lastAutofill?: JobContext | null }): JobContext {
  const { page, anchor } = input;
  if (input.pinned) return { ...input.pinned, source: "pinned" };
  const live = page.jdText.trim();
  if (live && page.posting && !page.form) return { url: page.url, jdText: live, source: "page", ...facts(page) };
  if (hasJd(anchor)) return { ...anchor, source: anchor.source === "board" ? "board" : "anchor" };
  if (live && page.form && (page.jdSource === "structured" || page.jdSource === "container")) return { url: page.url, jdText: live, source: "page", ...facts(page) };
  if (hasJd(input.lastAutofill)) return { ...input.lastAutofill, source: "autofill" };
  return { url: page.url, source: "none", ...facts(page) };
}

// Reading the page.

/** Description containers of known application systems: they hold the posting's text even on a form page. */
const DESCRIPTION_CONTAINERS = [
  "[data-automation-id='jobPostingDescription']",
  ".job__description",
  ".jobs-description__content",
  ".jobs-description-content__text",
  ".jobs-box__html-content",
];

const clean = (s: string) => s.replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();

function textOf(el: Element): string {
  return clean((el as HTMLElement).innerText || el.textContent || "");
}

/** The live job description and its source; a form page never gives its rendered text. */
export function readJobDescription(doc: Document, form: boolean): { text: string; source?: JdSource } {
  const description = jobPostingOf(doc)?.description;
  if (typeof description === "string" && description.trim()) {
    const text = htmlToText(winOf(doc), description);
    if (text) return { text, source: "structured" };
  }
  for (const sel of DESCRIPTION_CONTAINERS) {
    const el = doc.querySelector(sel);
    if (el) {
      const text = textOf(el);
      if (text) return { text, source: "container" };
    }
  }
  if (form) return { text: "" };
  const root = doc.querySelector("main, [role=main], article") ?? doc.body;
  return root ? { text: textOf(root), source: "rendered" } : { text: "" };
}
