// Which pages get the panel: a site the engine knows, a URL that reads like an
// application, or a page that shows a form once it has rendered.
import { classifyFrame } from "../../../engine/src/frames";
import { SITES, siteOf } from "../../../engine/src/sites";

export function isRegistrySite(url: string): boolean {
  return classifyFrame(url) === "registry";
}

/** A supported site, where the panel opens by itself once per tab: an application system of the engine's site table, or a host whose frames a fill admits. */
export function isSupportedSite(url: string): boolean {
  const id = siteOf(url);
  return isRegistrySite(url) || (id !== null && SITES.some((s) => s.id === id && s.kind === "ats"));
}

const APPLICATION_WORDS = /(^|[^a-z])(apply|application|applications|careers?|jobs?|openings?|positions?|vacanc\w*|recruit\w*|hiring|talent)([^a-z]|$)/i;

/** A permissive guess from the address alone; a wrong guess only costs an idle probe. */
export function looksLikeApplicationUrl(url: string): boolean {
  try {
    const u = new URL(url);
    return APPLICATION_WORDS.test(`${u.hostname}${u.pathname}`);
  } catch {
    return false;
  }
}

const NOT_DATA = new Set(["hidden", "submit", "reset", "button", "image", "search", "checkbox", "radio", "file", "range", "color"]);
const SEARCH_OR_SIGNUP = /search|query|newsletter|subscribe|signup-email|^q$/i;

function visible(el: Element): boolean {
  const h = el as HTMLElement;
  if ((h as HTMLInputElement).disabled) return false;
  return typeof h.checkVisibility === "function" ? h.checkVisibility({ visibilityProperty: true }) : h.getClientRects().length > 0;
}

function isDataEntry(el: Element): boolean {
  if (el.localName === "input") {
    if (NOT_DATA.has((el.getAttribute("type") ?? "text").toLowerCase())) return false;
  } else if (el.localName !== "textarea" && el.localName !== "select" && el.getAttribute("contenteditable") !== "true") {
    return false;
  }
  if (el.closest("[role=search], [role=searchbox]") || el.getAttribute("role") === "searchbox") return false;
  const own = [el.getAttribute("name"), el.getAttribute("id"), el.getAttribute("placeholder"), el.getAttribute("aria-label")].filter(Boolean).join(" ");
  return !SEARCH_OR_SIGNUP.test(own) && visible(el);
}

/**
 * The most visible data-entry controls that share a form (or the page, for
 * controls outside any form), leaving out search boxes and newsletter fields.
 * A form of two or more is the signal that this is an application.
 */
export function formControlCount(doc: Document): number {
  const groups = new Map<Element | Document, number>();
  for (const el of doc.querySelectorAll("input, textarea, select, [contenteditable=true]")) {
    if (!isDataEntry(el)) continue;
    const key = el.closest("form") ?? doc;
    groups.set(key, (groups.get(key) ?? 0) + 1);
  }
  return Math.max(0, ...groups.values());
}

const NOT_COUNTED = new Set(["hidden", "submit", "reset", "button", "image"]);
const EMAIL_WORDING = /e-?mail/i;
const NAME_WORDING = /(^|\s)(first|last|full|legal|preferred|given|family|your) name($|\s)|^name$/i;
const PHONE_WORDING = /phone|mobile|telephone|\bcell\b/i;
const APPLICATION_WORDING = /\b(r[eé]sum[eé]|cv|cover letter|linkedin|work authori[sz]ation|(legally )?authori[sz]ed to work|sponsorship|right to work|salary expectations?)\b/i;
/** Fewer countable controls than this is a search, a sign-up or a contact box, whatever it says. */
const MIN_APPLICATION_CONTROLS = 3;

/** Every field of the document, open shadow roots included. */
function fieldsOf(root: ParentNode, out: HTMLInputElement[] = []): HTMLInputElement[] {
  for (const el of root.querySelectorAll<HTMLInputElement>("*")) {
    if (el.localName === "input" || el.localName === "textarea" || el.localName === "select") out.push(el);
    if (el.shadowRoot) fieldsOf(el.shadowRoot, out);
  }
  return out;
}

/** What a field is called: its own attributes and label, looked up in the root it lives in (an id never crosses a shadow root). */
function nameOf(el: HTMLInputElement): string {
  const root = el.getRootNode() as Document | ShadowRoot;
  const byId = (id: string) => root.querySelector(`[id="${id.replace(/"/g, "")}"]`)?.textContent ?? "";
  const labelled = (el.getAttribute("aria-labelledby") ?? "").split(/\s+/).filter(Boolean).map(byId).join(" ");
  const forId = el.id ? Array.from(root.querySelectorAll("label")).filter((l) => l.getAttribute("for") === el.id).map((l) => l.textContent ?? "").join(" ") : "";
  return [el.getAttribute("aria-label"), el.getAttribute("placeholder"), labelled, forId, el.closest("label")?.textContent].filter(Boolean).join(" ").replace(/\s+/g, " ").trim();
}

/**
 * The strict reading of "this page holds an application form": no password
 * field anywhere, at least three countable fields, an email field, and at least
 * two independent kinds of evidence (the applicant's name, a phone, a file
 * upload, wording only an application uses). An email box and one upload is a
 * webshop newsletter with an attachment, not an application.
 */
export function looksLikeApplicationForm(doc: Document): boolean {
  const fields = fieldsOf(doc);
  if (fields.some((f) => f.type === "password")) return false;
  const counted = fields.filter((f) => !NOT_COUNTED.has(f.type));
  if (counted.length < MIN_APPLICATION_CONTROLS) return false;
  const named = (f: HTMLInputElement) => `${nameOf(f)} ${f.name} ${f.id} ${f.getAttribute("autocomplete") ?? ""}`;
  if (!counted.some((f) => f.type === "email" || EMAIL_WORDING.test(named(f)))) return false;
  const evidence = [
    counted.some((f) => NAME_WORDING.test(nameOf(f))),
    counted.some((f) => f.type === "tel" || PHONE_WORDING.test(named(f))),
    counted.some((f) => f.type === "file"),
    APPLICATION_WORDING.test((doc.body?.textContent ?? "").slice(0, 50_000)),
  ];
  return evidence.filter(Boolean).length >= 2;
}

const RESUME_WORDING = /\b(r[eé]sum[eé]|cv|curriculum vitae)\b/i;
const DOCUMENT_TYPES = /pdf|\.docx?\b|msword|wordprocessingml/i;

/**
 * Whether the page holds both a résumé upload (a file control named for a
 * résumé or accepting a PDF or Word document) and an identity field (email,
 * the applicant's name, phone): what every application form has and a
 * vendor's contact or sign-up form lacks.
 */
export function hasUploadAndIdentity(doc: Document): boolean {
  const fields = fieldsOf(doc);
  const named = (f: HTMLInputElement) => `${nameOf(f)} ${f.name} ${f.id} ${f.getAttribute("autocomplete") ?? ""}`;
  const upload = fields.some((f) => f.type === "file" && (RESUME_WORDING.test(named(f)) || DOCUMENT_TYPES.test(f.accept)));
  const identity = fields.some((f) => !NOT_COUNTED.has(f.type) && f.type !== "file" && (f.type === "email" || f.type === "tel" || EMAIL_WORDING.test(named(f)) || NAME_WORDING.test(nameOf(f)) || PHONE_WORDING.test(named(f))));
  return upload && identity;
}

/**
 * Whether the Documents section shows a tab's report here: a record the page's
 * own lookup found is trusted; one the tab carried from another page (the
 * posting's Apply hop) shows only on a page with a résumé upload and an
 * identity field, so documents never surface on a contact or vendor form.
 */
export function surfaceDocuments(inherited: boolean, doc: Document): boolean {
  return !inherited || hasUploadAndIdentity(doc);
}

/** Whether a submit click on this page is an application's: a known system, a fill run on the page, or the strict reading of its form. */
export function isApplicationPage(doc: Document, win: Window): boolean {
  if (isRegistrySite(win.location.href)) return true;
  const engine = (win as unknown as { __applysmithEngine?: { status(): { state: string } } }).__applysmithEngine;
  if (engine && engine.status().state !== "idle") return true;
  return looksLikeApplicationForm(doc);
}
