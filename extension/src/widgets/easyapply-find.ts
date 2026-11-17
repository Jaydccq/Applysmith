// What the step walker reads off a page and never changes: which subtree is the
// application step, whether the step shows a blocking error, whether it is a
// summary of data the person keeps on the host site, and the one control that
// carries no text. Nothing here clicks or writes.
import { clickLabel } from "../../../engine/src/click-guard";
import { collapse, composedParent, walkElements } from "../../../engine/src/dom";
import { isRendered } from "../../../engine/src/visibility";

/** A step's progress label. A calendar's pager ("Next month") and a sign-in choice ("Continue with Google") are not one, as in the engine's advance rule. */
export const PROGRESS = /^(continue(?!\s+with\b)|next(?!\s+(month|year|week|decade)\b)|review|save and continue)\b/i;
export const SUBMIT = /\bsubmit\b/i;
export const CONTROLS = "button, input[type=submit], input[type=button], [role=button]";
const DIALOGS = "dialog[open], [role=dialog], [role=alertdialog]";

const APPLY_DIALOG_TEXT = /\bapply to\b|application powered by|submitting this application|linkedin profile/i;
const SUBMIT_APPLICATION = /submit application/i;
const FOOTER_MAX = 400;

function everyElement(doc: Document): Element[] {
  const out: Element[] = [];
  walkElements(doc, (el) => void out.push(el));
  return out;
}

/** The text of the elements an aria-labelledby names, looked up in the tree the element lives in. */
function labelledText(el: Element): string {
  const root = el.getRootNode() as Document | ShadowRoot;
  return (el.getAttribute("aria-labelledby") ?? "")
    .split(/\s+/)
    .filter(Boolean)
    .map((id) => (root.getElementById?.(id) ?? el.ownerDocument.getElementById(id))?.textContent ?? "")
    .join(" ");
}

function hasAction(root: Element, back: boolean): boolean {
  return [...root.querySelectorAll<HTMLElement>(CONTROLS)].some((c) => {
    const label = clickLabel(c);
    return PROGRESS.test(label) || SUBMIT.test(label) || (back && /^back$/i.test(label));
  });
}

function looksLikeApplyDialog(d: Element): boolean {
  const text = collapse(`${d.getAttribute("aria-label") ?? ""} ${labelledText(d)} ${d.textContent ?? ""}`);
  return APPLY_DIALOG_TEXT.test(text) && hasAction(d, true);
}

/** The nearest ancestor, the start included, that holds a progress or submit control; the page body is never one. */
function ascendToAction(start: Element): Element | null {
  const doc = start.ownerDocument;
  for (let a: Element | null = start; a && a !== doc.body && a !== doc.documentElement; a = composedParent(a)) {
    if (hasAction(a, false)) return a;
  }
  return null;
}

/**
 * The application step's subtree, found by the most specific mark first so a
 * host that renames its class or drops a role is still found: the modal class,
 * a labelled dialog, a dialog holding an apply form, any dialog that names Easy
 * Apply or holds a Submit application button, an "Apply to" heading, and the
 * "Application powered by" footer. Open shadow roots are searched. A heading or
 * footer with no progress or submit control near it is ignored.
 */
export function findApplyModal(doc: Document): Element | null {
  const all = everyElement(doc);
  const dialogs = all.filter((e) => e.matches("[role=dialog], dialog[open]") && isRendered(e));
  const byClass = all.find((e) => e.classList.contains("jobs-easy-apply-modal") && isRendered(e));
  if (byClass) return byClass;
  const labelled = dialogs.find((d) => d.hasAttribute("aria-labelledby") && (/easy/i.test(d.getAttribute("aria-labelledby") ?? "") || looksLikeApplyDialog(d)));
  if (labelled) return labelled;
  const withForm = dialogs.find((d) => d.querySelector('form[aria-label*="apply" i]'));
  if (withForm) return withForm;
  const named = dialogs.find((d) => /easy apply/i.test(d.textContent ?? "") || looksLikeApplyDialog(d) || [...d.querySelectorAll<HTMLElement>(CONTROLS)].some((c) => SUBMIT_APPLICATION.test(clickLabel(c))));
  if (named) return named;
  for (const h of all.filter((e) => e.matches("h1, h2, h3, h4, h5, h6, [role=heading]") && /\bapply to\b/i.test(e.textContent ?? "") && isRendered(e))) {
    const found = ascendToAction(h);
    if (found) return found;
  }
  const footers = all.filter((e) => e.matches("p, span, div, footer, small, em") && (e.textContent ?? "").length < FOOTER_MAX && /application powered by/i.test(e.textContent ?? "") && isRendered(e));
  footers.sort((a, b) => (a.textContent ?? "").length - (b.textContent ?? "").length);
  for (const f of footers) {
    const found = ascendToAction(f);
    if (found) return found;
  }
  return null;
}

/** The step root: the application's own subtree, else the last visible dialog that has controls, else the page. */
export function stepRoot(doc: Document): Element {
  const modal = findApplyModal(doc);
  if (modal) return modal;
  const dialogs = [...doc.querySelectorAll(DIALOGS)].filter((d) => isRendered(d) && d.querySelector(CONTROLS));
  return dialogs[dialogs.length - 1] ?? doc.body;
}

const ERROR_TEXT = /required|invalid|error|enter a|select an|please|must|fix/i;
// The page-wide hint that starred fields are required is not an error.
const ASTERISK_HINT = /\b(?:fields?|items?|questions?) marked with (?:an? )?(?:asterisk|\*)[^.]*\.?/gi;
// A parsed item missing a field is plain static text, with no alert role to find it by.
const INCOMPLETE_ITEM = /required details (?:are )?missing|missing from this item|add (?:the )?required details/i;
const LEAF_TEXT_MAX = 200;
const MESSAGE_MAX = 160;

/**
 * A rendered error that a Continue would run into, as the message to show, or
 * null. Only an alert or assertive live region with error wording, a control
 * marked invalid, and a leaf text saying a parsed item lacks required details
 * count; prose that merely says "required" and anything hidden do not.
 */
export function blockingError(root: Element): string | null {
  for (const n of root.querySelectorAll('[role=alert], [aria-live=assertive]')) {
    if (!isRendered(n)) continue;
    const text = collapse((n.textContent ?? "").replace(ASTERISK_HINT, " "));
    if (text && ERROR_TEXT.test(text)) return text.slice(0, MESSAGE_MAX);
  }
  for (const c of root.querySelectorAll('[aria-invalid=true]')) {
    if (!isRendered(c)) continue;
    const described = (c.getAttribute("aria-describedby") ?? "")
      .split(/\s+/)
      .filter(Boolean)
      .map((id) => c.ownerDocument.getElementById(id)?.textContent ?? "")
      .join(" ");
    return collapse(described).slice(0, MESSAGE_MAX) || "A required field is invalid.";
  }
  for (const el of root.querySelectorAll("p, span, div, li")) {
    if (el.children.length > 0 || !isRendered(el)) continue;
    const text = collapse(el.textContent ?? "");
    if (text.length > 0 && text.length < LEAF_TEXT_MAX && INCOMPLETE_ITEM.test(text)) return text.slice(0, MESSAGE_MAX);
  }
  return null;
}

/**
 * A Work experience or Education step the host filled in from the person's own
 * profile there: a heading, an "N of M" counter and both Remove and Edit
 * controls. Headings only, so copy that mentions education elsewhere does not
 * match. The walker leaves such a step alone and only advances it.
 */
export function isProfileSummaryStep(root: Element): boolean {
  const headings = [...root.querySelectorAll("h1, h2, h3, h4")].map((h) => collapse(h.textContent ?? "").toLowerCase());
  if (!headings.some((h) => /\bwork experience\b|\beducation\b/.test(h))) return false;
  if (!/(\d+)\s+of\s+([1-9]\d*)/i.test(collapse(root.textContent ?? ""))) return false;
  const names = [...root.querySelectorAll("button, a")].map((e) => collapse(e.textContent ?? "").toLowerCase());
  return names.some((n) => /\bremove\b/.test(n)) && names.some((n) => /\bedit\b/.test(n));
}

// Only the two attach points seen to advance a step are admitted, so an
// unrecognized terminal control is still refused.
const PAGER_ADVANCE = '.srccarPager [data-dojo-attach-point="thePagerDualNext"], .srccarPager [data-dojo-attach-point="thePagerSingleGo"]';

/**
 * The rendered advance control of a Dojo pager, an empty div with no text,
 * title or label. It is found by structure and only while it still has no name
 * a label check could read.
 */
export function pagerAdvance(root: Element): HTMLElement | null {
  return [...root.querySelectorAll<HTMLElement>(PAGER_ADVANCE)].find((e) => isRendered(e) && !e.closest("[data-applysmith]") && clickLabel(e) === "") ?? null;
}
