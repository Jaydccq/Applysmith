// Clean-room: docs/provenance.md#CR-ENGINE
// Question text, required flag and section path for a control or a choice group.

import { collapse, composedAncestors, composedParent, flatText, walkElements } from "./dom";
import { isPlaceholderText, isSampleValue } from "./placeholder";
import { byAttribute } from "./selector";
import { isOffScreen, selfHidden } from "./visibility";

export type LabelSource = "label" | "aria-labelledby" | "container" | "legend" | "aria-label" | "placeholder" | "none";

export interface Label {
  /** The question the matcher sees. */
  text: string;
  /** Only the control's own label sources (label element, labelledby, aria-label); used to recognise identity fields. */
  direct: string;
  source: LabelSource;
  /** A label, legend or title element carried a required marker (asterisk or required class). */
  marked: boolean;
  /** The label said "optional". */
  optional: boolean;
  /** Elements whose class names may say "required". */
  titleEl: Element | null;
  /**
   * The description under the question that the text does not take (Oklo: what the options A, B and C stand for), held to
   * DESCRIPTION_MAX characters.
   */
  description?: string;
}

/** The most characters of a description that travels with its question. */
const DESCRIPTION_MAX = 4000;

// A presentation input (a dijit widget's validation icon or arrow) is drawing, and a hidden input a carrier: neither is a control.
const CONTROL_SEL = "input:not([role=presentation]):not([type=hidden]), textarea, select, sdf-select-simple, [contenteditable=''], [contenteditable='true'], [contenteditable='plaintext-only'], [role=textbox], [role=combobox], [role=listbox]";
const BOUNDARY = new Set(["body", "html", "form", "main", "header", "footer", "nav", "aside", "dialog"]);
const TITLE_TAGS = new Set(["label", "legend", "dt", "th", "td", "strong", "b", "p", "span", "div", "h3", "h4", "h5", "h6", "li"]);
const DESCRIPTION_CLASS = /(^|[\s_-])(description|desc|help|helptext|hint|subtext|question-body|helper|body)([\s_-]|$)/i;
const TITLE_CLASS = /label|title|question|legend|heading|prompt/i;
/**
 * What a react-select shows, its value or its placeholder, by its own class or emotion's generated one
 * ("css-<hash>-singleValue"), and the name of the file an upload holds (Lever's "filename").
 */
const SHOWN_VALUE_CLASS = /(?:^|[\s_-])(?:(?:single|multi)-?value|placeholder|file-?name)(?:[\s_-]|$)/i;
const MARK = /[\s]*[*∗＊✱✻]+\s*$/;
const LEADING_MARK = /^[*∗＊✱✻]+\s*/;

export function isControlEl(el: Element): boolean {
  return el.matches(CONTROL_SEL);
}

function hasControl(el: Element): boolean {
  return isControlEl(el) || !!el.querySelector(CONTROL_SEL);
}

/** The fields a box around a lone control may hold only one of; its own open list is no second field. */
const LONE_FIELD_SEL = "input:not([type=hidden]), textarea, select, button, [role=combobox], [contenteditable=''], [contenteditable='true'], [contenteditable='plaintext-only']";

function esc(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

function rootOf(el: Element): Document | ShadowRoot {
  return el.getRootNode() as Document | ShadowRoot;
}

/** The labels whose `for` names `id`, in tree order: within a look from its index, else from the root. */
function labelsFor(el: Element, id: string): Element[] {
  return byAttribute(rootOf(el), "label", "for", id) ?? Array.from(rootOf(el).querySelectorAll(`label[for="${esc(id)}"]`));
}

export function labelElements(el: Element): Element[] {
  const out = new Set<Element>();
  const id = el.getAttribute("id");
  // Null outside a look; an element without an id is named by no label.
  const indexed = byAttribute(rootOf(el), "label", "for", id ?? "");
  const named = indexed && (id ? indexed : []);
  const wrap = el.closest("label");
  // `labels` walks the whole root for every control. Within a look it is skipped where it can only repeat, in the same
  // order, the labels named for the control and its one wrapping label: none of the first, or none of the second.
  const skip = named !== null && !wrap?.parentElement?.closest("label") && (named.length === 0 || !wrap);
  const direct = skip ? null : (el as HTMLInputElement).labels;
  if (direct) for (const l of Array.from(direct)) out.add(l);
  if (id) for (const l of named ?? labelsFor(el, id)) out.add(l);
  if (wrap) out.add(wrap);
  // A Fabric select's label points at the hidden select that carries its value, not at the toggle button.
  if (!out.size && el.localName === "button" && el.hasAttribute("data-menu-id")) {
    const carrier = el.closest(".fab-Select, [data-fabric-component=Select]")?.querySelector("select[id]");
    if (carrier) for (const l of labelsFor(el, carrier.id)) out.add(l);
  }
  // A label can point at the box drawn around a lone control (Duolingo's role=group around its toggle button): the
  // nearest such box a few levels up that holds no other field.
  for (let a = el.parentElement, hops = 0; !out.size && a && hops < 4 && a.querySelectorAll(LONE_FIELD_SEL).length <= 1; a = a.parentElement, hops++) {
    const aid = a.getAttribute("id");
    if (aid) for (const l of labelsFor(el, aid)) out.add(l);
  }
  return Array.from(out).filter((l) => l.getAttribute("role") !== "button");
}

/** Live regions and status lines announce progress or errors; they are never part of a question. */
const ANNOUNCER = "[aria-live]:not([aria-live=off]), [role=status], [role=alert], [role=log], [role=progressbar]";

function textWithoutControls(node: Element, isLabel = false): string {
  // Dojo's help line sits inside the question's label and is not part of the question.
  // A label element that is itself a live region (ADP draws every label with aria-live) is still the question.
  return flatText(
    node,
    (e) => isControlEl(e) || e.localName === "select" || e.localName === "button" || (e.getAttribute("class") ?? "").includes("indentedButtonSubText") || (e.matches(ANNOUNCER) && !(isLabel && e === node)) || selfHidden(e),
  );
}

/**
 * The question a label states for its control. A label that wraps its control
 * states it before the control's branch: what follows it there (the widget's
 * own button text, an upload's status lines, a search's empty notice) is not
 * the question. A wrapping label with nothing before the control reads whole.
 */
function labelQuestion(label: Element, el: Element): string {
  if (label !== el && label.contains(el)) {
    let branch: Node = el;
    while (branch.parentNode && branch.parentNode !== label) branch = branch.parentNode;
    const parts: string[] = [];
    for (const n of Array.from(label.childNodes)) {
      if (n === branch) break;
      if (n.nodeType === 3) parts.push(n.nodeValue ?? "");
      else if (n.nodeType === 1 && !hasControl(n as Element)) parts.push(textWithoutControls(n as Element));
    }
    const before = collapse(parts.join(" "));
    if (stripMarker(before).text) return before;
  }
  return textWithoutControls(label, true);
}

function stripMarker(raw: string): { text: string; marked: boolean; optional: boolean } {
  let text = collapse(raw);
  let marked = false;
  let optional = false;
  const opt = /\s*[([]?\boptional\b[)\]]?\s*$/i;
  if (opt.test(text) && text.replace(opt, "").length > 0) {
    optional = true;
    text = text.replace(opt, "");
  }
  if (MARK.test(text)) {
    marked = true;
    text = text.replace(MARK, "");
  }
  if (LEADING_MARK.test(text)) {
    marked = true;
    text = text.replace(LEADING_MARK, "");
  }
  // "(used only if required)" and "if required" say when the answer is needed: no marker.
  const req = /\s*(?:\(\s*required\s*\)|(?<!\b(?:if|when|where|as|unless|not)\s+)(?<!\()\brequired)\s*$/i;
  if (req.test(text) && text.replace(req, "").length > 0) {
    marked = true;
    text = text.replace(req, "");
  }
  return { text: collapse(text.replace(/\s[*∗＊✱✻]+\s/g, " ")), marked, optional };
}

const UNIT_CAPTION = /^(\/\s*\w+|per\s+\w+|%|[$€£]|usd|eur|gbp|(days?|hours?|weeks?|months?|years?))$/i;
/** A date or number format hint standing where a label should be. */
const FORMAT_MASK = /^(m{1,2}|d{1,2}|y{2,4})([/.-](m{1,2}|d{1,2}|y{2,4})){0,2}$/i;
/** A caption that tells the person how to answer and asks nothing. */
/** A label that only names a place in a ranked set ("Preference 2", "Second choice"). */
const SLOT_LABEL = /^(?:preference|choice|priority|rank)\s*#?\s*\d{1,2}$|^(?:first|second|third|1st|2nd|3rd)\s+(?:preference|choice|priority)$/i;
const INSTRUCTION_ONLY = /^(please )?(check|select|choose|tick|mark|pick)( all| one| any| up to \d+)?( that apply| of the following| the following| below| from the list)?[\s.:*]*$/i;
const FURNITURE = /^(select( one| an option| an item)?|choose( one)?|please select)[\s*.…-]*(required)?$/i;

export interface CleanContext {
  docTitle?: string;
  tokens?: string[];
}

function collapseRepeats(text: string): string {
  const re = /(?<!\S)(\S.{1,298}?\S)\s+\1(?!\S)/g;
  let cur = text;
  for (let i = 0; i < 4; i++) {
    const next = cur.replace(re, "$1");
    if (next === cur) break;
    cur = next;
  }
  return cur;
}

function stripTitleChrome(text: string, title: string): string {
  const t = collapse(title);
  if (!t) return text;
  const segments = [t, ...t.split(/\s[|–—·-]\s/).map(collapse)].filter((s) => s.length >= 4);
  // The page's title can be glued on at the front, the back, or both.
  let cur = text;
  for (let round = 0; round < 3; round++) {
    let next = cur;
    for (const seg of segments) {
      if (next.length > seg.length + 2 && next.startsWith(seg) && /^\s/.test(next.slice(seg.length))) next = collapse(next.slice(seg.length));
      else if (next.length > seg.length + 2 && next.endsWith(seg) && /\s$/.test(next.slice(0, next.length - seg.length))) next = collapse(next.slice(0, next.length - seg.length));
    }
    if (next === cur) break;
    cur = next;
  }
  return cur;
}

/** Removes page chrome, opaque ids and the control's own id/name tokens from question text. */
function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function isFormatMask(text: string): boolean {
  return FORMAT_MASK.test(collapse(text));
}

export function cleanQuestion(raw: string, ctx: CleanContext = {}): string {
  let text = collapse(raw).replace(/^\*\*\s*/, "");
  text = text
    .replace(/indicates a required field/gi, "")
    .replace(/\bstep \d+ of \d+\b/gi, "")
    .replace(/back to job posting/gi, "")
    .replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi, "")
    .replace(/\b[0-9a-f]{16,}\b/gi, "");
  text = collapse(text);
  const tokens = (ctx.tokens ?? []).filter((t) => t.length >= 3).sort((a, b) => b.length - a.length);
  for (const tok of tokens) {
    // Only whole tokens are removed: a field named "auth" must not turn
    // "authorized" into "orized". A plain word such as "visa" is part of the
    // question unless it is glued onto either end of the label text.
    const t = escapeRegExp(tok);
    const identifier = /[^A-Za-z]|[a-z][A-Z]/.test(tok);
    const whole = identifier
      ? new RegExp(`(?<![A-Za-z0-9])${t}(?![A-Za-z0-9])`, "g")
      : new RegExp(`^${t}(?![A-Za-z0-9])|(?<![A-Za-z0-9])${t}$`, "g");
    if (!whole.test(text)) continue;
    // A plain word is the field's own id glued on when the text says it twice or sets it after punctuation; said once inside a sentence, it is the question.
    if (!identifier) {
      const twice = (text.match(new RegExp(`(?<![A-Za-z0-9])${t}(?![A-Za-z0-9])`, "gi")) ?? []).length >= 2;
      const afterPunctuation = new RegExp(`[?.:*!]\\s*${t}$`).test(text) || new RegExp(`^${t}\\s*[?.:*!]`).test(text);
      if (!twice && !afterPunctuation) continue;
    }
    whole.lastIndex = 0;
    const next = collapse(text.replace(whole, " "));
    if (next) text = next;
  }
  text = collapseRepeats(text);
  if (ctx.docTitle) text = stripTitleChrome(text, ctx.docTitle);
  if (UNIT_CAPTION.test(text) || FURNITURE.test(text)) return "";
  return text;
}

function clampQuestion(text: string): string {
  if (text.length <= 2000) return text;
  const q = text.indexOf("?");
  if (q >= 0 && q < 400) return text.slice(0, q + 1);
  const end = text.lastIndexOf("?", 2000);
  if (end > 0) {
    const start = Math.max(text.lastIndexOf(".", end) + 1, end - 400);
    return collapse(text.slice(start, end + 1));
  }
  return text.slice(0, 400);
}

/** A hint that says which parts of a place the answer names ("Please list city and state."). */
const PLACE_PARTS_HINT = /\b(?:list|enter|include|provide|specify|give)\b[^.?!]{0,40}\b(?:city|state|province|country|zip|postal code)\b/i;

/** A hint that asks the field to be signed (Abacus: "Please sign and date below."): it says what the box takes. */
const SIGN_HINT =
  /^\W*(?:please\s+)?sign\b|^\W*(?:please\s+)?(?:type|enter|print|write)\s+(?:in\s+)?your\s+(?:(?:full|legal)\s+)*name\b[^.?!]*\b(?:sign|signature|signify)\b/i;

/**
 * A sentence that names the answer giving an acknowledgement (Anthropic: "... confirm your understanding by selecting
 * “Yes.”"; Zapier: "Please select "Yes" to confirm your understanding and agreement.").
 */
const ANSWER_NAMING =
  /\bby\s+(?:responding|answering|selecting|entering|choosing|checking|marking)\s+["'\u2018\u2019\u201C\u201D]?yes\b|\b(?:respond|answer|select|enter|choose|check|mark)\s+["'\u2018\u2019\u201C\u201D]?yes["'\u2018\u2019\u201C\u201D]?\s+to\s+(?:confirm|acknowledge|indicate)\b/i;

/** The sentences of a description, each with its closing mark and quote. */
const sentencesOf = (text: string): string[] => text.split(/(?<=[.!?]["'\u2019\u201D]?)\s+/);

/** An Ashby field's description, held to 600 characters; a longer one keeps the sentence that names the Yes giving an acknowledgement. */
function heldDetail(detail: string): string {
  const head = detail.slice(0, 600);
  const naming = detail.length > 600 ? sentencesOf(detail).find((x) => ANSWER_NAMING.test(x)) : undefined;
  if (!naming || naming.length > 360 || head.includes(naming)) return head;
  return `${detail.slice(0, 600 - naming.length - 1).trimEnd()} ${naming}`;
}

/**
 * A description that is a question, says which parts of a place to name, or asks to sign: it states what the field asks. Of a
 * longer description that names the answer giving an acknowledgement, the sentence that names it.
 */
function describedBy(el: Element): string {
  for (const text of descriptionTexts(el)) {
    if (text.length <= 360 && (/[?]/.test(text) || PLACE_PARTS_HINT.test(text) || SIGN_HINT.test(text))) return text;
    const naming = sentencesOf(text).find((s) => ANSWER_NAMING.test(s));
    if (naming && naming.length <= 360) return naming;
  }
  return "";
}

/** The non-empty texts of a field's descriptions: its aria-describedby targets, then its `<id>-description` element. */
function descriptionTexts(el: Element): string[] {
  const ids = (el.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean);
  const own = el.getAttribute("id");
  if (own) ids.push(`${own}-description`);
  const root = rootOf(el);
  const out: string[] = [];
  for (const id of ids) {
    const target = root.getElementById?.(id) ?? null;
    const text = target ? flatText(target) : "";
    if (text) out.push(text);
  }
  return out;
}

/** The longest description of the field that the question text does not hold, held to DESCRIPTION_MAX characters. */
function untakenDescription(el: Element, text: string): string | undefined {
  const rest = descriptionTexts(el).filter((d) => !text.includes(d));
  if (!rest.length) return undefined;
  return rest.reduce((a, b) => (b.length > a.length ? b : a)).slice(0, DESCRIPTION_MAX);
}

/** A node drawn only for screen readers: a status line or a hidden count, not a name. */
const SCREEN_READER_ONLY = /(^|[^a-z])(sr-only|screen-?reader(-only)?|visually-?hidden|a11y-hidden)([^a-z]|$)/i;

function screenReaderOnly(node: Element): boolean {
  const named = `${node.getAttribute("class") ?? ""} ${node.getAttribute("data-testid") ?? ""} ${node.getAttribute("data-test") ?? ""}`;
  return SCREEN_READER_ONLY.test(named) || isOffScreen(node);
}

/**
 * The words the element's aria-labelledby names. A live region or a screen-reader-only line named beside the
 * real label ("Total 0 file selected" ahead of the field's title) is a status, not part of the name; it is
 * left out whenever another named node says something.
 */
function labelledByText(el: Element): string {
  const ids = (el.getAttribute("aria-labelledby") ?? "").split(/\s+/).filter(Boolean);
  const root = rootOf(el);
  const targets = ids.map((id) => root.getElementById?.(id) ?? null).filter((t): t is HTMLElement => t !== null);
  const quiet = targets.filter((t) => !t.matches(ANNOUNCER) && !screenReaderOnly(t));
  const used = quiet.some((t) => flatText(t)) ? quiet : targets;
  return collapse(used.map((t) => flatText(t)).join(" "));
}

interface Container {
  title: string;
  description: string;
  titleEl: Element;
}

function isTitleCandidate(child: Element, members: Element[]): boolean {
  if (!TITLE_TAGS.has(child.localName) && !TITLE_CLASS.test(child.getAttribute("class") ?? "")) return false;
  if (child.localName === "label" && child.getAttribute("for")) {
    const root = rootOf(child);
    const target = root.getElementById?.(child.getAttribute("for") as string);
    // A label for the wrapper of the group's own boxes names the group (Phenom's demographic checkboxes).
    if (target && !isControlEl(target) && !members.some((m) => target.contains(m))) return false;
  }
  return !hasControl(child);
}

/**
 * The popups the members own through aria-controls or aria-owns, and the box Select2 draws right after a select it
 * replaces (Veeva's Lever university picker): a list drawn open beside its box, or a box drawn for it, is part of it.
 */
function ownedPopups(members: Element[]): Element[] {
  const out: Element[] = [];
  for (const m of members) {
    const drawn = m.localName === "select" ? m.nextElementSibling : null;
    if (drawn && /(?:^|\s)select2-container(?:\s|$)/.test(drawn.getAttribute("class") ?? "")) out.push(drawn);
    const root = rootOf(m);
    for (const attr of ["aria-controls", "aria-owns"]) {
      for (const id of (m.getAttribute(attr) ?? "").split(/\s+/).filter(Boolean)) {
        const target = root.getElementById?.(id) ?? null;
        if (target && target !== m) out.push(target);
      }
    }
  }
  return out;
}

/**
 * The per-field container title: the nearest ancestor (4 levels at most) that
 * holds nothing but this control or group owns a text block before it. The
 * climb stops at a container that holds another control; the list a box has
 * open beside it is no other control. A box whose own aria-label says nothing
 * ("Select") has only the title to go by, so the wrappers that hold nothing but
 * the node are styling there and do not use up a level.
 */
const INLINE_TAGS = new Set(["a", "span", "b", "strong", "em", "i", "u", "small"]);

/**
 * A label that only draws the box (it holds the box's wrapper and no text) captions nothing; the inline text after it
 * does (ByteDance's sign-in: "<label><span><input><span></span></span></label>I have read and agree to the <a>User
 * Agreement</a> and <a>Applicant Privacy Policy</a>").
 */
function drawnLabelCaption(labels: Element[], wrap: Element): string {
  const l = labels[0] as Element;
  if (labels.length !== 1 || !l.contains(wrap) || l.querySelectorAll(CONTROL_SEL).length !== 1 || collapse(textWithoutControls(l)) !== "") return "";
  const kids = Array.from(l.parentElement?.childNodes ?? []);
  const after = kids.slice(kids.indexOf(l) + 1);
  if (after.some((n) => n.nodeType === 1 && (hasControl(n as Element) || !INLINE_TAGS.has((n as Element).localName)))) return "";
  const t = collapse(after.map((n) => n.textContent ?? "").join(""));
  return t.length <= 200 ? t : "";
}

function containerTitle(branch: Element, members: Element[], styledWrappers = false): Container | null {
  let node: Element = branch;
  const popups = ownedPopups(members);
  const upload = members.length === 1 && members[0]?.localName === "input" && (members[0] as HTMLInputElement).type === "file";
  for (let depth = 0, hops = 0; depth < 5 && hops < 12; hops++) {
    const parent = composedParent(node);
    if (!parent || BOUNDARY.has(parent.localName) || parent.matches("[role=dialog], [role=alertdialog]")) return null;
    if (!styledWrappers || parent.children.length > 1) depth++;
    // A hidden duplicate (MUI's aria-hidden autosize twin of a text area) is not a second field.
    const others = Array.from(parent.querySelectorAll(CONTROL_SEL)).filter((c) => !members.includes(c) && !node.contains(c) && !popups.some((p) => p === c || p.contains(c)) && c.getAttribute("aria-hidden") !== "true");
    if (others.length > 0) return null;
    const kids = Array.from(parent.children);
    const at = kids.indexOf(node);
    const before = at >= 0 ? kids.slice(0, at) : [];
    const descriptions: string[] = [];
    for (let i = before.length - 1; i >= 0; i--) {
      const child = before[i] as Element;
      const text = textWithoutControls(child);
      // A widget's placeholder ("--Select One--") shows an empty value, a glyph with no letter or digit (a
      // typeahead's clear cross) is a button's face, and what a select shows is its answer or its prompt; none is the question.
      // The face of an upload's own button ("Upload file" drawn around Lever's file input) names no question either.
      if (!text || isPlaceholderText(text) || !/[\p{L}\p{N}]/u.test(text) || SHOWN_VALUE_CLASS.test(child.getAttribute("class") ?? "") || (upload && UPLOAD_BUTTON.test(collapse(text)))) continue;
      if (hasControl(child)) return null;
      if (DESCRIPTION_CLASS.test(child.getAttribute("class") ?? "")) {
        descriptions.unshift(text);
        continue;
      }
      if (isTitleCandidate(child, members)) {
        return { title: text, description: heldDetail(descriptions.join(" ")), titleEl: child };
      }
    }
    const lead = Array.from(parent.childNodes)
      .slice(0, Math.max(0, Array.from(parent.childNodes).indexOf(node)))
      .filter((n) => n.nodeType === 3)
      .map((n) => n.nodeValue ?? "")
      .join(" ");
    if (collapse(lead)) return { title: collapse(lead), description: heldDetail(descriptions.join(" ")), titleEl: parent };
    node = parent;
  }
  return null;
}

/**
 * The heading of the nearest container that opens with one, when the control
 * is the container's first: Lever draws a card's question as its h4 ("How did
 * you hear about us?") over a field that only says how to answer ("Please
 * check all that apply:"). A field further down the card is not the one the
 * heading asks.
 */
function openingHeading(el: Element): string {
  for (let a = el.parentElement, hops = 0; a && hops < 8 && !BOUNDARY.has(a.localName); a = a.parentElement, hops++) {
    const h = a.querySelector(":scope > h1, :scope > h2, :scope > h3, :scope > h4, :scope > h5");
    if (!h) continue;
    const first = a.querySelector(CONTROL_SEL);
    return first === el && (h.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0 ? collapse(textWithoutControls(h)) : "";
  }
  return "";
}

/** A stem past the usual length is kept only when it asks one question or opens by saying which choice to make. */
const LONG_STEM = 480;
const CHOOSE = /\b(?:check|select|choose|indicate|mark|tick)\b/i;
const CHOICES = /\b(?:yes|no|one|option|box|boxes|following|appropriate|below)\b/i;

/**
 * The stem a choice group's wrapper is preceded by, when no rung above named
 * it: up to three levels up, the nearest of three earlier siblings with text.
 * A sibling that holds a control is another question's and ends the search.
 * Paycom draws its SMS consent group in a fieldset with an empty legend under
 * a paragraph two levels up; Rippling's stem is a directive ("Check Yes or No
 * to indicate your agreement ...") with no question mark.
 */
function precedingStem(anchor: Element, members: Element[]): string {
  for (let level: Element | null = anchor, climbed = 0; level && climbed < 3; climbed++, level = level.parentElement) {
    if (level.localName === "body" || level.localName === "form") break;
    let sib = level.previousElementSibling;
    for (let steps = 0; sib && steps < 3; steps++, sib = sib.previousElementSibling) {
      if (sib.localName === "style" || sib.localName === "script") continue;
      if (members.some((m) => sib?.contains(m)) || hasControl(sib) || sib.querySelector("fieldset, [role=radiogroup]")) return "";
      const text = collapse(textWithoutControls(sib));
      if (!text) continue;
      if (text.length <= 180) return text;
      const first = text.split(/(?<=[.?!])\s/)[0] ?? "";
      return text.length <= LONG_STEM && (text.includes("?") || (CHOOSE.test(first) && CHOICES.test(first))) ? text : "";
    }
  }
  return "";
}

function closingQuestion(legend: string): string {
  if (legend.length <= 400) return legend;
  // Past the cap the legend opens with its question and carries detail after it.
  if (legend.length > 2000) {
    const q = legend.indexOf("?");
    if (q >= 0 && q < 400) return `${legend.slice(0, q + 1)}${/\*\s*$/.test(legend) ? " *" : ""}`;
  }
  const mark = /\*\s*$/.test(legend) ? " *" : "";
  // A one-letter abbreviation ("U.S.", "8 U.S.C.", "e.g.") ends no sentence: "Are you a U.S. Person?" is one question.
  // A stop inside closing quotes ('by responding "Yes."') ends one. The required mark after the last stop ("business. *",
  // HP) is no sentence of its own.
  const sentences = legend.replace(/\s*\*\s*$/, "").split(/(?<=[?.!]["”’']?)(?<!\b[A-Za-z]\.)\s+/);
  let last = `${sentences[sentences.length - 1] ?? legend}${mark}`;
  // A note after the question (GM's list of example subsidiaries) is not the question.
  const asked = sentences.filter((s) => s.trimEnd().endsWith("?")).pop();
  if (asked && !last.includes("?")) last = `${asked}${mark}`;
  // With no question, the line that says how to answer is the ask, and the pledge before it is what it confirms
  // ("By proceeding, you acknowledge and agree not to utilize any AI tools ... Please enter Yes if you acknowledge.").
  let directive = asked ? -1 : sentences.length - 1;
  while (directive >= 0 && !ANSWER_DIRECTIVE.test(sentences[directive] as string)) directive--;
  if (directive >= 0) {
    const pledge = sentences.slice(0, directive).filter((s) => PLEDGE.test(s)).pop();
    const list = listBelow(sentences, directive);
    const ask = [pledge, sentences[directive], list].filter(Boolean).join(" ");
    if (ask.length <= (list ? LIST_CAP : 400)) return `${ask}${mark}`;
  }
  return last.length <= 400 ? last : last.slice(-400);
}

/**
 * The list an ask points at below it, when later sentences of the legend enumerate it: HP's "Please indicate if you
 * are a citizen or permanent resident of ONE OR MORE of the countries listed below, by answering 'yes'." is answered
 * from "The countries are: Armenia, ..., Yemen." after it.
 */
function listBelow(sentences: string[], at: number): string {
  if (at < 0 || !LIST_BELOW.test(sentences[at] as string)) return "";
  return sentences
    .slice(at + 1)
    .filter((s) => (s.match(/,/g)?.length ?? 0) >= 3)
    .join(" ");
}

const LIST_BELOW = /\b(?:listed|named|shown)\s+below\b|\bthe\s+following\s+countr(?:y|ies)\b/i;
const LIST_CAP = 1600;

const ANSWER_DIRECTIVE =
  /^(?:please\s+)?(?:enter|select|choose|answer|indicate|mark|check|respond)\b.*\b(?:yes|no|if you)\b|\bby\s+(?:responding|answering|selecting|entering|choosing|checking|marking)\s+["“‘']?(?:yes|no)\b/i;
const PLEDGE = /\b(?:acknowledge|agree|confirm|certify|attest|consent)\b/i;

/** A class named required once its build suffix is gone: a hash, or a hash and an index (Ashby's `_required_f7cvd_91`). */
function classSaysRequired(el: Element | null): boolean {
  if (!el) return false;
  for (const tok of (el.getAttribute("class") ?? "").split(/\s+/)) {
    const bare = tok.replace(/^[_-]+/, "").replace(/[_-][A-Za-z0-9]{4,8}([_-]\d{1,4})?$/, "").replace(/[_-]+$/, "");
    if (bare.toLowerCase() === "required") return true;
    // A vendor's own prefix (Taleo's `oracletaleocwsv2-required`), never a negation.
    if (VENDOR_REQUIRED.test(tok) && !/^(?:not|non|un)[-_]/i.test(tok)) return true;
  }
  return false;
}

const VENDOR_REQUIRED = /^[a-z][a-z0-9]*[-_]required$/i;

/** A label or legend: what a page that marks by class could have marked. A caption beside a box (Taleo's agreement) could not. */
function classMarkable(el: Element | null): boolean {
  return el?.localName === "label" || el?.localName === "legend";
}

/** What one look at the whole page found, kept for a scan so each control does not look again. */
interface PageMemo {
  /** By root: the wordings, lower-cased, of the marked labels whose `for` names nothing. */
  orphanMarks: Map<Node, Set<string>>;
  /** FormValidation's "must not be empty" messages. */
  fvMessages?: Element[];
}

export interface LabelContext extends CleanContext {
  /** The scan's memory of the page; without one every control looks for itself. */
  page?: PageMemo;
  /** Whether the page marks required questions with a trailing asterisk. */
  asteriskConvention?: boolean;
  /** Whether the page marks required questions with a required class on a label or legend. */
  classConvention?: boolean;
  /** Hidden inputs that back the control (a button choice's proxy): they are not other controls. */
  ignore?: Element[];
}

function ownTokens(el: Element): string[] {
  // A name with a space is wording (Ashby names a box after its option), not an id.
  return [el.getAttribute("id"), el.getAttribute("name"), ...Array.from(el.classList)].filter((t): t is string => !!t && !/\s/.test(t));
}

/** The fieldset that asks exactly this one group, if any. Its legend is the question; otherwise the legend is a section caption. */
function questionFieldset(members: Element[], ignore: Element[] = []): Element | null {
  let fs = members[0]?.closest("fieldset") ?? null;
  // Workday wraps a checkbox group's own fieldset, which has no legend, in the fieldset that asks it.
  while (fs && !fs.querySelector(":scope > legend")) {
    const outer = fs.parentElement?.closest("fieldset");
    if (!outer) break;
    fs = outer;
  }
  return fs && asksOnly(fs, members, ignore) ? fs : null;
}

/** Whether a box holds the controls of this one question only: with another question's control inside, its caption names a section. */
function asksOnly(box: Element, members: Element[], ignore: Element[] = []): boolean {
  for (const c of Array.from(box.querySelectorAll(CONTROL_SEL))) {
    // A box the page disabled is no other question.
    if (members.includes(c) || ignore.includes(c) || (c as HTMLInputElement).disabled) continue;
    const n = c.getAttribute("name");
    if (n && members.some((m) => m.getAttribute("name") === n)) continue;
    return false;
  }
  return true;
}

/** The nearest element that holds every box of a group of two or more. */
function boxesWrapper(members: Element[]): Element | null {
  if (members.length < 2) return null;
  let node = members[0]?.parentElement ?? null;
  while (node && !members.every((m) => node?.contains(m))) node = node.parentElement;
  return node;
}

/** Ashby declares one checkbox question as this fieldset, whatever the boxes are named. */
export const ASHBY_CHECKBOX_GROUP = "fieldset.ashby-application-form-input-checkbox-group";

/**
 * A label whose `for` names nothing on the page but whose words are this question carries the question's own
 * marker: ADP draws the label with its asterisk beside a custom element that holds the same words in an attribute.
 */
function orphanLabelMarks(el: Element, question: string, memo: PageMemo = { orphanMarks: new Map() }): boolean {
  const root = rootOf(el);
  let marked = memo.orphanMarks.get(root);
  if (!marked) {
    marked = new Set();
    for (const l of Array.from(root.querySelectorAll("label[for]"))) {
      const id = l.getAttribute("for") ?? "";
      if (!id || root.querySelector(`[id="${esc(id)}"]`)) continue;
      const s = stripMarker(textWithoutControls(l, true));
      if (s.marked) marked.add(collapse(s.text).toLowerCase());
    }
    memo.orphanMarks.set(root, marked);
  }
  return marked.has(collapse(question).toLowerCase());
}

const TEXT_LIKE = new Set(["text", "email", "tel", "url", "number", "search"]);

/**
 * The field's own placeholder as its question, the last resort when the page names it nowhere else (Breezy and
 * JazzHR draw "Full Name" and "City" as placeholders alone). A widget's prompt, an instruction and a sample value
 * ("e.g. 555-0100", "john@example.com") ask nothing, and a combobox shows its placeholder as a hint.
 */
function placeholderQuestion(el: Element): string {
  const type = (el.getAttribute("type") ?? "text").toLowerCase();
  if (!(el.localName === "textarea" || (el.localName === "input" && TEXT_LIKE.has(type)))) return "";
  if (el.matches("[role=combobox], [aria-haspopup]:not([aria-haspopup=false]), [aria-autocomplete]")) return "";
  const hint = collapse(el.getAttribute("placeholder") ?? "");
  return isPlaceholderText(hint) || isSampleValue(hint) || isFormatMask(hint) ? "" : hint;
}

/**
 * A lone box's field wraps a title of its own beside the box ("Please check the box to acknowledge *" over
 * "I acknowledge"): the title's marker is the box's requirement.
 */
function titleMarks(el: Element): boolean {
  let node: Element = el;
  for (let depth = 0; depth < 3; depth++) {
    const parent = composedParent(node);
    if (!parent || BOUNDARY.has(parent.localName)) return false;
    if (Array.from(parent.querySelectorAll(CONTROL_SEL)).some((c) => c !== el && !node.contains(c) && (c as HTMLInputElement).type !== "hidden")) return false;
    for (const l of Array.from(parent.querySelectorAll("label"))) {
      if (!l.contains(el) && !hasControl(l) && l.getAttribute("for") !== el.getAttribute("id") && stripMarker(textWithoutControls(l, true)).marked) return true;
    }
    node = parent;
  }
  return false;
}

/** Whether a host draws more than one field, light and shadow trees included. */
function holdsSeveralFields(host: Element): boolean {
  const stack: ParentNode[] = [host];
  let fields = 0;
  while (stack.length) {
    const node = stack.pop() as ParentNode;
    for (const c of Array.from(node.children)) {
      if (c.matches("input, select, textarea") && ++fields > 1) return true;
      stack.push(c);
      if (c.shadowRoot) stack.push(c.shadowRoot);
    }
  }
  return false;
}

/** The aria-label of the custom element a box is the one field of (SmartRecruiters' "Select Gender"), when the box has none. */
function hostAriaLabel(el: Element): string {
  for (const a of composedAncestors(el)) {
    if (!a.localName.includes("-")) continue;
    if (holdsSeveralFields(a)) return "";
    const named = a.getAttribute("aria-label");
    if (named) return named;
  }
  return "";
}

/** The wrapper a box is drawn in on its own (Taleo Business Edition's icon and input): it holds no other field and no text. */
export function ownWrapper(el: Element): Element | null {
  const wrap = el.parentElement;
  return wrap && wrap.querySelectorAll(CONTROL_SEL).length === 1 && collapse(textWithoutControls(wrap)) === "" ? wrap : null;
}

/**
 * The caption of a lone box drawn in a wrapper of its own: the wrapper's aria-labelledby, else the text right after
 * the wrapper when it holds no field. A box with a label element of its own is captioned there (ADP's dijit box
 * inside "<label> ... Reviewed</label>").
 */
function wrapperCaption(el: Element): string {
  const wrap = ownWrapper(el);
  if (!wrap) return "";
  const labels = labelElements(el);
  if (labels.length > 0) return drawnLabelCaption(labels, wrap);
  const by = labelledByText(wrap);
  if (by) return by;
  const next = wrap.nextElementSibling;
  if (!next || !["span", "label"].includes(next.localName) || hasControl(next)) return "";
  const t = collapse(next.textContent ?? "");
  return t.length <= 200 ? t : "";
}

const UPLOAD_BUTTON = /^(?:attach|upload|browse|choose|select)(?: (?:a )?files?)?$/i;

export function resolveLabel(el: Element, members: Element[] = [el], ctx: LabelContext = {}): Label {
  const isGroup = members.length > 1 || el.matches("input[type=radio], input[type=checkbox]");
  const lone = members.length <= 1 && el.matches("input[type=checkbox]");
  let marked = false;
  let optional = false;
  let titleEl: Element | null = null;
  const note = (raw: string): string => {
    const s = stripMarker(raw);
    if (s.marked) marked = true;
    if (s.optional) optional = true;
    return s.text;
  };
  const clean = (text: string): string => {
    const c = cleanQuestion(text, { docTitle: ctx.docTitle, tokens: ownTokens(el) });
    // A field id set after the required mark ("Preferred Name* input-yJ791fwK") leaves the mark at the end once it is gone.
    if (!MARK.test(c)) return c;
    marked = true;
    return collapse(c.replace(MARK, ""));
  };

  let direct = "";
  let source: LabelSource = "none";
  let text = "";
  /** A format hint in a label's place: kept only when no later rung names the field better. */
  let maskOnly = "";

  if (isGroup) {
    const fs = questionFieldset(members, ctx.ignore);
    const legend = fs?.querySelector(":scope > legend");
    if (legend) {
      const t = clean(note(closingQuestion(textWithoutControls(legend))));
      if (t) {
        text = t;
        source = "legend";
        titleEl = legend;
        // Ashby states the real question's detail in a description beside its title.
        if (fs && (fs.closest("[data-field-path]") || /fieldEntry/i.test(fs.getAttribute("class") ?? ""))) {
          const detail = heldDetail(
            Array.from(fs.children)
              .filter((c) => c !== legend && DESCRIPTION_CLASS.test(c.getAttribute("class") ?? "") && !hasControl(c))
              .map((c) => collapse(textWithoutControls(c)))
              .filter(Boolean)
              .join(" "),
          );
          if (detail && !text.includes(detail)) text = `${text} ${detail}`;
        }
      }
    }
    if (!text) {
      // A custom element states its question in a label attribute, on the host or a few levels up.
      let hops = 0;
      for (const a of composedAncestors(el)) {
        if (hops++ >= 4) break;
        // A host that draws another question's control too names a section.
        const attr = a.hasAttribute("label") && !["input", "label"].includes(a.localName) && asksOnly(a, members, ctx.ignore) ? clean(note(a.getAttribute("label") ?? "")) : "";
        if (attr) {
          text = attr;
          source = "aria-labelledby";
          direct = attr;
          break;
        }
      }
    }
    if (!text) {
      // A radio group is one question; a plain group that holds another question's control is a section (Workday draws both).
      const near = el.closest("[role=radiogroup], [role=group]");
      const group = near && (near.getAttribute("role") === "radiogroup" || asksOnly(near, members, ctx.ignore)) ? near : null;
      const lb = group ? labelledByText(group) : "";
      const t = clean(note(lb || (group?.getAttribute("aria-label") ?? "")));
      if (t) {
        text = t;
        source = "aria-labelledby";
        direct = t;
      }
    }
  } else {
    for (const l of labelElements(el)) {
      // A label drawn around a widget can carry its name only in aria-labelledby (Rippling's upload button).
      const own = labelQuestion(l, el);
      const t = clean(note(own || labelledByText(l)));
      // A file input's label that only names its button ("Attach") yields to the upload group's title.
      if (t && (isFormatMask(t) || (el as HTMLInputElement).type === "file" && UPLOAD_BUTTON.test(t))) {
        maskOnly ||= t;
        continue;
      }
      if (t) {
        direct = t;
        text = t;
        source = own ? "label" : "aria-labelledby";
        titleEl = l;
        break;
      }
    }
    if (!text) {
      const t = clean(note(labelledByText(el)));
      if (t) {
        direct = t;
        text = t;
        source = "aria-labelledby";
      }
    }
    if (!text && el.localName === "input" && (el as HTMLInputElement).type === "file") {
      // An upload area names its file input from around it (Workday's attachments-FileUpload).
      const area = el.parentElement?.closest("[aria-labelledby]");
      const t = area && area.querySelectorAll("input[type=file]").length === 1 ? clean(note(labelledByText(area))) : "";
      if (t) {
        direct = t;
        text = t;
        source = "aria-labelledby";
      }
    }
  }

  if (!text && !isGroup && el.localName === "button" && el.getAttribute("aria-haspopup") === "listbox") {
    // A questionnaire's listbox trigger is the one question of its fieldset, whatever hidden carrier sits beside it.
    // A fieldset with a second trigger draws a section: its legend asks neither.
    const fs = el.closest("fieldset");
    const legend = fs && fs.querySelectorAll("button[aria-haspopup=listbox]").length === 1 ? fs.querySelector(":scope > legend") : null;
    const t = legend ? clean(note(closingQuestion(textWithoutControls(legend)))) : "";
    if (legend && t) {
      text = t;
      source = "legend";
      titleEl = legend;
    }
  }

  if (!text && isGroup) {
    // Ashby's checkbox and radio groups: a title and a description in the fieldset, the title's `for` pointing at nothing;
    // an empty title leaves the question to the description.
    const fs = members[0]?.closest(`${ASHBY_CHECKBOX_GROUP}, fieldset.ashby-application-form-input-radio-group`);
    const title = fs?.querySelector(".ashby-application-form-question-title");
    const t = title ? clean(note(textWithoutControls(title))) : "";
    const detail = fs && title ? collapse(textWithoutControls(fs.querySelector(".ashby-application-form-question-description") ?? title)) : "";
    const asked = t || clean(note(detail));
    if (fs && title && asked) {
      text = t && detail && detail !== collapse(textWithoutControls(title)) && !t.includes(detail) ? `${t} ${detail}` : asked;
      source = "legend";
      titleEl = title;
    }
  }

  if (!text && lone) {
    const t = clean(note(wrapperCaption(el)));
    if (t) {
      direct = t;
      text = t;
      source = "label";
    }
  }

  if (!text) {
    const ariaSaysNothing = !isGroup && el.hasAttribute("aria-label") && clean(stripMarker(el.getAttribute("aria-label") ?? "").text) === "";
    // The wrappers that hold nothing but a group of choices are styling too (Gem nests its Yes and No under three).
    const branch = isGroup ? (members[0]?.closest("fieldset, [role=radiogroup], [role=group]") ?? el) : el;
    let container = containerTitle(branch, [...members, ...(ctx.ignore ?? [])], ariaSaysNothing || members.length > 1);
    // A fieldset that draws a whole section (Phenom's demographic questions) titles no one group; the wrapper of the group's own boxes is titled.
    const boxes = isGroup && !container ? boxesWrapper(members) : null;
    if (boxes && boxes !== branch && branch.contains(boxes)) container = containerTitle(boxes, [...members, ...(ctx.ignore ?? [])], true);
    if (container) {
      const t = clean(note(container.title));
      if (t) {
        text = clampQuestion(t);
        source = "container";
        titleEl = container.titleEl;
        // The title of the control's own single-control field names it: a label whose `for` points nowhere, a title row
        // whose whole title is one label (Bending Spoons), a caption in a span above the box (Gem).
        if (!isGroup) direct = text;
        if (container.description) text = `${text} ${container.description}`.trim();
      }
    }
  }

  if (!text && lone) {
    // A box that stands alone states its own label; there is no group question above it to prefer.
    for (const l of labelElements(el)) {
      // A label that also holds an ARIA box is that custom control's, and the native input behind it is its hidden carrier.
      if (l.querySelector("[role=checkbox], [role=switch]")) continue;
      const t = clean(note(labelQuestion(l, el)));
      if (t) {
        direct = t;
        text = t;
        source = "label";
        titleEl = l;
        break;
      }
    }
    if (text && !marked && titleMarks(el)) marked = true;
  }

  if (!text) {
    const aria = isGroup && !lone ? "" : clean(note(el.getAttribute("aria-label") ?? (isGroup ? "" : hostAriaLabel(el))));
    if (aria) {
      direct = aria;
      text = aria;
      source = "aria-label";
    }
  }

  if (!text && isGroup) {
    const t = clean(note(precedingStem(members[0]?.closest("fieldset, [role=radiogroup], [role=group]") ?? el, members)));
    if (t) {
      text = t;
      source = "container";
    }
  }

  if (!text && !isGroup && !maskOnly) {
    const hint = clean(note(placeholderQuestion(el)));
    if (hint) {
      direct = hint;
      text = hint;
      source = "placeholder";
    }
  }

  if (text && !isGroup && source === "label") {
    // Ashby states the real question in a description block of the same field entry, ahead of the control.
    const entry = el.closest('[data-field-path], [class*="fieldEntry"]');
    if (entry && Array.from(entry.querySelectorAll(CONTROL_SEL)).every((c) => c === el)) {
      const detail = heldDetail(
        Array.from(entry.children)
          .filter((c) => DESCRIPTION_CLASS.test(c.getAttribute("class") ?? "") && !hasControl(c) && (c.compareDocumentPosition(el) & 4) !== 0)
          .map((c) => collapse(textWithoutControls(c)))
          .filter(Boolean)
          .join(" "),
      );
      if (detail && !text.includes(detail)) text = `${text} ${detail}`;
    }
  }

  if (text && !isGroup && source === "label" && SLOT_LABEL.test(text)) {
    // A ranked set states its question once, in the legend of the fieldset around its slots (Citadel: "Please select your
    // top two preferred coding languages for interviews:" over "Preference 1" and "Preference 2").
    const legend = el.closest("fieldset")?.querySelector(":scope > legend");
    const ask = legend ? clean(stripMarker(closingQuestion(textWithoutControls(legend))).text) : "";
    if (ask && !text.includes(ask)) text = `${ask} ${text}`;
  }

  if (isGroup && INSTRUCTION_ONLY.test(text)) {
    // A legend that only says how to answer: the heading inside the group's own container is the question.
    const own = containerTitle(members[0]?.closest("fieldset, [role=radiogroup], [role=group]") ?? el, [...members, ...(ctx.ignore ?? [])]);
    const heading = own ? clean(note(own.title)) : "";
    if (heading && !text.includes(heading)) text = `${heading} ${text}`;
    else {
      const opening = openingHeading(members[0] ?? el);
      if (opening && !text.includes(opening)) text = `${opening} ${text}`;
    }
  }

  if (!text && maskOnly) {
    direct = maskOnly;
    text = maskOnly;
    source = "label";
  }

  if (text && !marked && ctx.asteriskConvention && orphanLabelMarks(el, text, ctx.page)) marked = true;
  let description: string | undefined;
  if (text) {
    const described = describedBy(el);
    if (described && !text.includes(described)) text = `${text} ${described}`;
    description = untakenDescription(el, text);
  }
  if (classSaysRequired(titleEl)) marked = true;
  const label: Label = { text: clampQuestion(text), direct, source: text ? source : "none", marked, optional, titleEl };
  if (description) label.description = description;
  return label;
}

/** Label of one option of a choice group. */
export function optionLabel(input: Element): string {
  for (const l of labelElements(input)) {
    const t = stripMarker(textWithoutControls(l, true)).text;
    if (t) return t;
  }
  const aria = collapse(input.getAttribute("aria-label") ?? "") || labelledByText(input);
  if (aria) return aria;
  const next = input.nextSibling;
  if (next && next.nodeType === 3 && collapse(next.nodeValue ?? "")) return collapse(next.nodeValue ?? "");
  return collapse(input.getAttribute("value") ?? "");
}

/**
 * How the page marks its required questions, read off every label and legend, those inside open shadow roots
 * included (the scan reads controls there too): with a trailing asterisk, or with a required class on the label or
 * legend, its asterisk drawn by CSS (Taleo).
 */
export function pageConventions(doc: Document): { asteriskConvention: boolean; classConvention: boolean; page: PageMemo } {
  const found = { asteriskConvention: false, classConvention: false, page: { orphanMarks: new Map<Node, Set<string>>() } };
  walkElements(doc, (el) => {
    if (el.localName !== "label" && el.localName !== "legend") return;
    found.asteriskConvention ||= MARK.test(collapse(el.textContent ?? ""));
    found.classConvention ||= classSaysRequired(el);
  });
  return found;
}

/** A FormValidation field: a not-empty attribute, or the validator's own message element beside the control or naming it. */
function fvRequired(el: Element, memo?: PageMemo): boolean {
  const flag = el.getAttribute("data-fv-notempty");
  if (flag !== null) return flag !== "false";
  const name = el.getAttribute("name") ?? "";
  const messages = memo?.fvMessages ?? Array.from(el.ownerDocument.querySelectorAll('[data-fv-validator="notEmpty"]'));
  if (memo) memo.fvMessages = messages;
  return messages.some((m) => (name !== "" && m.getAttribute("data-fv-for") === name) || m.parentElement?.contains(el) === true);
}

/**
 * An unticked box the form library already holds invalid: Angular marks a
 * box under its requiredTrue validator `ng-invalid` from the first render
 * (ADP's privacy acknowledgement), and nothing else makes a lone box invalid.
 */
function invalidUnticked(el: Element): boolean {
  const box = el instanceof HTMLInputElement && el.type === "checkbox" ? !el.checked : el.getAttribute("role") === "checkbox" && el.getAttribute("aria-checked") === "false";
  return box && (el.classList.contains("ng-invalid") || el.getAttribute("aria-invalid") === "true");
}

/** Whether the page itself says the control is required (an attribute, ARIA or a marker), not a default. */
function pageMarksRequired(el: Element, members: Element[], label: Label, memo?: PageMemo): boolean {
  const all = members.length ? members : [el];
  if (all.some((m) => m.hasAttribute("required") || m.getAttribute("aria-required") === "true" || fvRequired(m, memo))) return true;
  if (invalidUnticked(el)) return true;
  const group = el.closest("[role=radiogroup], [role=group]");
  if (group?.getAttribute("aria-required") === "true") return true;
  if (all.every((m) => m.getAttribute("aria-required") === "false")) return false;
  if (label.marked) return true;
  const fs = el.closest("fieldset");
  return !!fs && classSaysRequired(fs.querySelector(":scope > legend"));
}

function isRequired(marked: boolean, el: Element, members: Element[], label: Label, ctx: LabelContext, isFile: boolean): boolean {
  if (marked) return true;
  const all = members.length ? members : [el];
  if (all.every((m) => m.getAttribute("aria-required") === "false")) return false;
  if (label.optional) return false;
  if (label.source !== "none" && !label.marked && (ctx.asteriskConvention || (ctx.classConvention && classMarkable(label.titleEl)))) return false;
  return !isFile;
}

/** Whether the control must be answered, and whether the page itself marks it so: the page's marks are read once for both. */
export function requirement(el: Element, members: Element[], label: Label, ctx: LabelContext, isFile: boolean): { required: boolean; markedRequired: boolean } {
  const markedRequired = pageMarksRequired(el, members, label, ctx.page);
  return { required: isRequired(markedRequired, el, members, label, ctx, isFile), markedRequired };
}

const SECTION_WORDS =
  /education|employment|work history|work experience|experience|reference|personal|contact|address|equal opportunity|eeo|self[- ]identif|voluntary|legal|authorization|emergency|skills|links|additional|application questions|^entry\s*#?\s*\d+$/i;
/** A custom element named for the entity it holds (`oc-experience`). */
const ENTITY_TAG = /^[a-z]+-(experience|employment|education|reference)s?\b/;
/** A control name that encodes its row (`$$employerState_1_x`). */
const NAME_ROW: ReadonlyArray<[RegExp, string]> = [
  [/(employer|employment|workhistory|workexperience|company)[a-z]*[_\-.[]\d+/i, "Employment"],
  [/(school|education|degree)[a-z]*[_\-.[]\d+/i, "Education"],
];

function nameRow(el: Element): string | null {
  const names = [el, ...Array.from(el.querySelectorAll("input, select, textarea")).slice(0, 6)].flatMap((n) => [n.getAttribute("name"), n.getAttribute("id")]).filter((n): n is string => !!n);
  const found = new Set(names.flatMap((n) => NAME_ROW.filter(([re]) => re.test(n)).map(([, label]) => label)));
  return found.size === 1 ? ([...found][0] as string) : null;
}

/** Structural section captions around the control, outermost first, each capped at 80 characters. */
export function sectionPath(el: Element, skip: Element | null = null): string[] {
  const out: string[] = [];
  const push = (raw: string) => {
    const t = collapse(raw);
    if (t && t.length <= 80 && SECTION_WORDS.test(t) && !out.includes(t)) out.unshift(t);
  };
  for (const a of composedAncestors(el)) {
    if (a.localName === "fieldset") {
      const legend = a.querySelector(":scope > legend");
      if (legend && legend !== skip) push(textWithoutControls(legend));
    }
    const lb = a.getAttribute("aria-labelledby");
    if (lb && (a.getAttribute("role") === "group" || a.getAttribute("role") === "region" || a.localName === "section")) {
      push(labelledByText(a));
    } else if (a.getAttribute("aria-label") && ["group", "region"].includes(a.getAttribute("role") ?? "")) {
      push(a.getAttribute("aria-label") as string);
    }
    const tag = ENTITY_TAG.exec(a.localName);
    if (tag) push((tag[1] as string).replace(/^./, (c) => c.toUpperCase()));
    // Workable names its history blocks only in a data attribute, beside a plain-text caption.
    const ui = a.getAttribute("data-ui");
    if (ui === "experience" || ui === "education") push(ui.replace(/^./, (c) => c.toUpperCase()));
    // Greenhouse titles its education block with a paragraph; the block's own class names it.
    if (a.classList?.contains("education--container")) push("Education");
    // A section's own heading, directly or through a header wrapper; a row container is told apart by its "Entry #N" caption.
    const h = a.querySelector(":scope > h1, :scope > h2, :scope > h3, :scope > h4, :scope > header > h1, :scope > header > h2, :scope > header > h3");
    if (h && (a.localName === "section" || a.getAttribute("role") === "region" || /^entry\s*#?\s*\d+$/i.test(collapse(h.textContent ?? "")))) push(textWithoutControls(h));
  }
  // Nothing structural: the name of the control itself says which row it belongs to.
  if (!out.length) {
    const row = nameRow(el);
    if (row) out.push(row);
  }
  return out;
}

const COMPOSITE_PART = /^(first|middle|last)$/;
const NAME_GROUP = /^(your |full |legal |applicant |candidate )?name$/;

/**
 * A bare First, Middle or Last sub-label that sits under one composite name
 * group (a fieldset whose legend is just "Name"). A bare sub-label anywhere
 * else names nothing and is not a name part.
 */
export function compositeNamePart(el: Element, direct: string): "first" | "middle" | "last" | null {
  const word = collapse(direct).toLowerCase();
  if (!COMPOSITE_PART.test(word)) return null;
  const group = el.closest("fieldset, [role=group]");
  const legend = group?.querySelector(":scope > legend") ?? null;
  const caption = collapse(legend ? textWithoutControls(legend) : (group?.getAttribute("aria-label") ?? "")).toLowerCase().replace(/[*:]+$/, "").trim();
  if (!group || !NAME_GROUP.test(caption) || group.querySelectorAll("input:not([type=hidden]):not([type=checkbox]):not([type=radio])").length < 2) return null;
  return word as "first" | "middle" | "last";
}
