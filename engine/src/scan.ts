// Clean-room: docs/provenance.md#CR-ENGINE
// Control discovery: which controls the page offers a person, grouped into
// the units the engine answers (one text box, one select, one choice group).

import { looksLikeConsent } from "./consent";
import { collapse, flatText, normalize, walkElements } from "./dom";
import { container as select2Container, insideSelect2Widget } from "./select2";
import { insideTinymceEditor, tinymceEditor } from "./libraries";
import {
  ASHBY_CHECKBOX_GROUP,
  compositeNamePart,
  labelElements,
  optionLabel,
  ownWrapper,
  pageConventions,
  requirement,
  resolveLabel,
  sectionPath,
  type Label,
  type LabelContext,
} from "./labels";
import type { InputType, QuestionKind, RowKind, WireQuestion } from "./protocol";
import { MAX_OPTIONS_PER_QUESTION } from "./protocol";
import { isPlaceholderOption, optionText, readElement, readGroup, readSelect } from "./reader";
import { isPillProxy, readToggles, toggleGroupOf, toggleLabel, toggleOn, toggleProxies } from "./toggles";
import { driverOf, isMultiValue, readOnlyBlocks, wrapperOwning, type Driver } from "./widgets";
import { scanRoot } from "./easyapply";
import { isSfUploader } from "./successfactors";
import { icimsDisplay, icimsRows, isSearchWidget } from "./icims";
import { isOfficeLocation, srChoiceBox, srHost } from "./sr";
import { isCascadeField, isOracleAddress, isOracleCountry, isOracleCountryCode } from "./oracle";
import { dateSegments, isWorkdayPrompt, pillTexts } from "./workday";
import { classifySensitivity, type Sensitivity } from "./sensitive";
import { groupSelector, resolveAll, selectorFor, sharingAnchors } from "./selector";
import {
  ariaHiddenBlocks,
  isConcealedDecoy,
  isHoneypotLabel,
  isMeasurementTwin,
  isRendered,
  type RenderCache,
} from "./visibility";

export interface Option {
  label: string;
  value: string;
}

export interface ControlInfo {
  /** The element itself, or the first member of a choice group. */
  el: HTMLElement;
  members: HTMLInputElement[];
  kind: RowKind;
  inputType: InputType;
  wireKind: QuestionKind;
  selector: string;
  label: Label;
  required: boolean;
  /** The page itself marks the control required, not a default. */
  markedRequired: boolean;
  section: string[];
  maxLength?: number;
  options: Option[];
  sensitivity: Sensitivity;
  owner: "profile" | "matcher";
  /** The control already holds an answer. */
  answered: boolean;
  /** Admitted as a question for the matcher (lone boxes and some groups are claimed without one). */
  admitted: boolean;
  /** The widget driver that operates the control, when it is not a native one. */
  driver: Driver | null;
  /** The members of a choice made of buttons or ARIA widgets; empty for native inputs. */
  toggles: HTMLElement[];
  /** A custom listbox whose options have not been read yet: it is not asked until they are. */
  needsOptions: boolean;
}

export interface ScanOptions {
  /** Read-only inventory: controls the engine never fills (a password, a one-time code) are listed too, so the host sees their structure. */
  inventory?: boolean;
  /** Also list upload inputs that sit in a menu or dialog the page keeps closed until its trigger is pressed. */
  hiddenUploads?: boolean;
}

const CHROME = "[role=search], nav, footer";

const SUBMITS = "button[type=submit], button:not([type]), input[type=submit]";
const SIGNUP = /\b(?:job alerts?|talent (?:community|network|pool)|newsletter|subscribe)\b/;

/**
 * A form whose every submit signs the visitor up for job alerts, a talent community or a newsletter is the site's,
 * never the application (Procore's "Sign Up for Job Alerts" beside its "Apply Now" form). A sign-up box inside the
 * application is a checkbox, not the form's submit, so it never hides the form.
 */
function signupForm(el: Element, memo: Map<Element, boolean>): boolean {
  const form = el.closest("form");
  if (!form) return false;
  let hit = memo.get(form);
  if (hit === undefined) {
    const submits = Array.from(form.querySelectorAll<HTMLElement>(SUBMITS));
    hit = (submits.length > 0 && submits.every((b) => SIGNUP.test(submitWords(b)))) || inviteForm(form);
    memo.set(form, hit);
  }
  return hit;
}

const submitWords = (b: HTMLElement) => normalize(b.localName === "input" ? (b as HTMLInputElement).value : flatText(b));
const INVITE = /\b(?:job alerts?|talent (?:community|network|pool)|newsletter|get notified|stay in touch)\b/;
const BOXES = "input:not([type=hidden]), select, textarea";

/**
 * A short form under an invitation to job alerts is the site's too, though the invitation stands outside it
 * (Phenom's "Get notified for similar jobs" over an email box and an Activate button): a block of at most three
 * boxes and under 400 characters whose text outside the form names the invitation. A form with an upload, or one
 * whose submit applies, is never one.
 */
function inviteForm(form: Element): boolean {
  if (form.querySelector("input[type=file]")) return false;
  if (Array.from(form.querySelectorAll<HTMLElement>(SUBMITS)).some((b) => /\bapply\b/.test(submitWords(b)))) return false;
  for (let n = form.parentElement; n && n.localName !== "body"; n = n.parentElement) {
    if (n.querySelectorAll(BOXES).length > 3 || collapse(flatText(n)).length >= 400) return false;
    if (INVITE.test(normalize(flatText(n, (e) => e === form)))) return true;
  }
  return false;
}

/** The extension's own surfaces and another extension's panel are no part of the page's form, wherever they sit and across shadow roots. */
function inForeignSurface(el: Element): boolean {
  for (let n: Element | null = el; n; ) {
    if (/^(applysmith-|auto-job-)/.test(n.getAttribute("id") ?? "") || (n.getAttribute("class") ?? "").includes("simplify-jobs")) return true;
    n = n.parentElement ?? (n.getRootNode() as ShadowRoot).host ?? null;
  }
  return false;
}
const IDENTITY_LABEL = new RegExp(
  "^(" +
    [
      "(full |legal |your )?name",
      "(legal )?(first|given) name",
      "(legal )?(last|family|sur) ?name",
      "first and last name",
      "(full |legal )?name first and last",
      "e ?mail( address)?",
      "(confirm|re enter|retype|verify)( your)? e ?mail( address)?",
      "(mobile |home |cell |primary )?phone( number)?",
      "(street )?address( line)?( 1)?",
      "city",
      "county",
      "state( province)?",
      "(zip|postal)( code)?",
      "country",
      "signature",
      "employer",
      "(position|job) title",
      "major",
      "school",
    ].join("|") +
    ")$",
);
const IDENTITY_AUTOCOMPLETE = /^(name|given-name|family-name|email|tel|tel-national|street-address|address-line1|address-level2|address-level1|postal-code|country-name|country)$/;
const ROW_SECTION = /education|employment|work history|work experience|experience|reference/i;
const MULTI = /select all|check all|choose all|all that apply/i;
/** Lever draws a multiple-select question as this list (its multiple-choice questions are radios). */
const LEVER_CHECKBOX_LIST = 'ul[data-qa="checkboxes"]';
/** Workday draws a question's boxes in this fieldset, ticked as many as apply (The Clearing House's "Shift Available": Day, Night). */
const WORKDAY_CHECKBOX_GROUP = 'fieldset[data-automation-id$="-CheckboxGroup"]';

const SKIP_INPUT = new Set(["hidden", "submit", "reset", "button", "image", "search", "range", "color"]);
const TEXT_TYPES = new Set(["", "text", "email", "tel", "url", "number", "date", "month", "time", "datetime-local", "week"]);

/** A short question that only asks for a field names that field: "What is your legal first name?" (Dropbox) is "legal first name". */
const ASKED_FIELD = /^\s*what(?:\s+is|['\u2019]s)\s+your\s+((?:\S+\s+){0,4}\S+?)\s*\?\s*$/i;

export function askedField(text: string): string {
  return ASKED_FIELD.exec(text)?.[1] ?? text;
}

function nameWord(value: string): string {
  return collapse(value.normalize("NFKD").toLowerCase().replace(/[^a-z0-9]+/g, " "));
}

/** Disabled by its own attribute or by a disabled fieldset around it (the first legend is exempt). */
function isDisabled(el: Element): boolean {
  if ((el as HTMLInputElement).disabled) return true;
  const fs = el.closest("fieldset[disabled]");
  if (!fs) return false;
  const legend = fs.querySelector(":scope > legend");
  return !(legend && legend.contains(el));
}

function namedGroup(el: HTMLInputElement): HTMLInputElement[] {
  const root = el.getRootNode() as Document | ShadowRoot;
  const type = el.type;
  const name = el.getAttribute("name") as string;
  // Workday repeats a row's box name in every row (currentlyWorkHere); the id's row prefix tells the rows apart.
  const row = (i: Element): string | null => /^([A-Za-z]+-\d+)--/.exec(i.id)?.[1] ?? null;
  return Array.from(root.querySelectorAll<HTMLInputElement>(`input[type="${type}"]`)).filter(
    (i) => i.getAttribute("name") === name && i.form === el.form && row(i) === row(el),
  );
}

/** Whether a container holds a control besides the boxes of one group. */
function holdsOthers(node: Element, same: HTMLInputElement[]): boolean {
  return Array.from(node.querySelectorAll("input, select, textarea")).some((c) => !same.includes(c as HTMLInputElement) && (c as HTMLInputElement).type !== "hidden");
}

/**
 * Whether a wrapper holds a section rather than one question: a select beside
 * other controls (HPE's), or two or more controls besides the boxes (Qualtrics
 * wraps a whole step, its name and email boxes and its separate opt-in and
 * privacy boxes, in one fieldset). One box beside a question's options, its
 * "Other" text, leaves the wrapper the question's.
 */
function holdsSection(node: Element, same: HTMLInputElement[]): boolean {
  const others = Array.from(node.querySelectorAll("input, select, textarea")).filter((c) => !same.includes(c as HTMLInputElement) && (c as HTMLInputElement).type !== "hidden");
  return others.length >= 2 || (others.length > 0 && node.querySelector("select") !== null);
}

/** Nameless inputs group under their nearest question wrapper. */
function namelessGroup(el: HTMLInputElement): HTMLInputElement[] {
  const sel = `input[type="${el.type}"]`;
  const wrapper = el.closest("fieldset, [role=radiogroup], [role=group]");
  if (wrapper) {
    const same = Array.from(wrapper.querySelectorAll<HTMLInputElement>(sel)).filter((i) => !i.getAttribute("name"));
    if (same.length >= 1 && !holdsSection(wrapper, same)) return same;
  }
  let node = el.parentElement;
  for (let depth = 0; node && depth < 8; depth++, node = node.parentElement) {
    if (["form", "body", "html"].includes(node.localName)) break;
    const same = Array.from(node.querySelectorAll<HTMLInputElement>(sel)).filter((i) => !i.getAttribute("name"));
    if (same.length >= 2) return holdsOthers(node, same) ? [el] : same;
  }
  return [el];
}

/**
 * Two checkboxes named after themselves that answer Yes and No inside one
 * form group are one question, not two lone boxes.
 */
function yesNoBoxPair(el: HTMLInputElement): HTMLInputElement[] | null {
  if (el.type !== "checkbox") return null;
  const group = el.closest(".form-group, fieldset, [role=group]");
  if (!group) return null;
  const boxes = Array.from(group.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'));
  if (boxes.length !== 2) return null;
  const words = boxes.map((b) => nameWord(optionLabel(b))).sort();
  return words[0] === "no" && words[1] === "yes" ? boxes : null;
}

/**
 * ADP Workforce Now names each box of a check-all-that-apply question after
 * its own value ("<question>_<value>"): the stem left once the value is cut
 * off is the question, or null for any other box.
 */
function valueStem(el: HTMLInputElement): string | null {
  if (el.type !== "checkbox" || !el.value) return null;
  const name = el.getAttribute("name") ?? "";
  const tail = `_${el.value}`;
  return name.length > tail.length && name.endsWith(tail) ? name.slice(0, -el.value.length) : null;
}

function valueNamedGroup(el: HTMLInputElement): HTMLInputElement[] | null {
  const stem = valueStem(el);
  if (!stem) return null;
  const root = el.getRootNode() as Document | ShadowRoot;
  const boxes = Array.from(root.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')).filter((i) => i.form === el.form && valueStem(i) === stem);
  return boxes.length > 1 ? boxes : null;
}

/** A group selector matches every member; boxes with different names are matched through their form group. */
function choiceSelector(first: HTMLElement, members: HTMLInputElement[]): string {
  const names = new Set(members.map((m) => m.getAttribute("name")));
  if (members.length < 2 || names.size < 2) {
    // A name other rows repeat (Workday's currentlyWorkHere) names this group only through its own box.
    const sel = groupSelector(first);
    return members.length === 1 && resolveAll(first.ownerDocument, sel).length > 1 ? selectorFor(first) : sel;
  }
  const group = first.closest(".form-group, fieldset, [role=group]");
  const base = group ? selectorFor(group) : "";
  return group && !base.includes(" >>> ") ? `${base} input[type="${members[0]?.type}"]` : groupSelector(first);
}

/**
 * Ashby names every box of a checkbox group after its own option, so grouping
 * by name splits one question into lone boxes; the group's fieldset is the
 * form's own declaration of one field.
 */
function groupMembers(el: HTMLInputElement): HTMLInputElement[] {
  const ashby = el.type === "checkbox" ? el.closest(ASHBY_CHECKBOX_GROUP) : null;
  const fieldset = ashby ? Array.from(ashby.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')) : null;
  const members = fieldset ?? valueNamedGroup(el) ?? yesNoBoxPair(el) ?? (el.getAttribute("name") ? namedGroup(el) : namelessGroup(el));
  const enabled = members.filter((m) => !isDisabled(m));
  // Workday's disability form locks its answer by disabling every box but the ticked one: the locked boxes are still its options.
  return enabled.length > 0 && enabled.length < members.length && enabled.every((m) => m.checked) ? members : enabled;
}

function choiceVisible(input: HTMLInputElement, cache: RenderCache): boolean {
  if (isRendered(input, cache)) return true;
  for (const l of labelElements(input)) if (isRendered(l, cache)) return true;
  const stand = input.closest("[role=radio], [role=checkbox], [role=switch]");
  if (stand) return isRendered(stand, cache);
  // A hidden box drawn by the icon beside it in a wrapper of its own (Taleo Business Edition) is that drawn box.
  const wrap = ownWrapper(input);
  return !!wrap && Array.from(wrap.children).some((c) => c !== input && isRendered(c, cache));
}

const ADDRESS_LINE = /^((street|home|mailing|current|residential) )?address( line)?( 1| one)?$|^street( address)?$/;

function ownerOf(label: Label, kind: RowKind, el: Element, section: string[]): "profile" | "matcher" {
  if (kind === "file") return "profile";
  // A street box that suggests addresses is the profile's; every other custom listbox is the matcher's.
  // The phone box commits its own country code picker.
  if (kind === "custom-listbox" && isWorkdayPrompt(el) && /\bcountry\b.*\bcode\b|\bphone code\b/i.test(label.text)) return "profile";
  // Oracle: the address master and the block it fills, and the country that gates it, belong to the profile.
  if (kind === "custom-listbox" && (isOracleAddress(el) || isCascadeField(el) || isOracleCountry(el))) return "profile";
  if (kind === "custom-listbox") return el.localName === "input" && !section.some((s) => ROW_SECTION.test(s)) && ADDRESS_LINE.test(nameWord(label.direct)) ? "profile" : "matcher";
  if (section.some((s) => ROW_SECTION.test(s))) return "matcher";
  const ac = (el.getAttribute("autocomplete") ?? "").toLowerCase().trim().split(/\s+/).pop() ?? "";
  if (label.direct && IDENTITY_LABEL.test(nameWord(askedField(label.direct)))) return "profile";
  // A First, Middle or Last box under one composite name group is a part of the applicant's name; the profile pass decides which.
  if (label.direct && kind === "text" && compositeNamePart(el, label.direct)) return "profile";
  if (!label.direct && IDENTITY_AUTOCOMPLETE.test(ac)) return "profile";
  return "matcher";
}

function textInputType(el: HTMLElement, type: string): InputType {
  if (el.localName === "textarea" || el.getAttribute("contenteditable") !== null || el.getAttribute("role") === "textbox") return "long_text";
  if (type === "email") return "email";
  if (type === "tel") return "tel";
  if (type === "url") return "url";
  if (type === "number") return "number";
  if (type === "date" || type === "month") return "date";
  // A react-datepicker box (Ashby's dates) is a text input that holds one date.
  if (el.closest(".react-datepicker__input-container")) return "date";
  // A single-line box is a short answer whatever its length limit (Greenhouse gives every one maxlength 255).
  return "text";
}

function yesNo(options: Option[]): boolean {
  const n = options.map((o) => nameWord(o.label)).sort();
  return n.length === 2 && n[0] === "no" && n[1] === "yes";
}

/** A follow-up stem that asks nothing on its own: it only means something beside the question before it. */
const BARE_STEM = /^(if [a-z ]{1,24}, )?(please )?(specify|explain|describe|elaborate|clarify|list|provide details|give details)( below| here)?[\s.:*]*$/i;

/** A bare stem takes the question of the nearest control before it in the same fieldset or section; with none, it stays bare. */
function qualifyStems(found: ControlInfo[]): void {
  // A group whose question is the legend of its own fieldset is that fieldset: it sits in the section around it.
  const scopeOf = (info: ControlInfo) => {
    const own = info.label.source === "legend" ? info.label.titleEl?.parentElement : null;
    return (own?.localName === "fieldset" ? own.parentElement : info.el)?.closest("fieldset, section, [role=group]") ?? null;
  };
  found.forEach((info, i) => {
    if (info.kind !== "text" && info.kind !== "textarea") return;
    if (!BARE_STEM.test(info.label.text)) return;
    // A follow-up stem is a short answer, not a narrative.
    if (info.inputType === "long_text" && info.kind === "textarea") info.inputType = "text";
    const scope = scopeOf(info);
    for (let j = i - 1; j >= 0; j--) {
      const before = found[j] as ControlInfo;
      if (before.kind === "file" || scopeOf(before) !== scope) continue;
      const text = before.label.text;
      if (text.length < 8 || text.length > 160 || BARE_STEM.test(text)) continue;
      info.label = { ...info.label, text: `${text} ${info.label.text}` };
      return;
    }
  });
}

/** How likely a candidate is the trigger of the popup it names: a slotted trigger and a button outrank a bare box. */
function triggerScore(el: HTMLElement): number {
  return (el.getAttribute("slot") === "trigger" ? 2 : 0) + (el.localName === "button" ? 2 : 0);
}

/** Candidates that name one popup (aria-controls) are one widget: the best-scored trigger is asked, a tie keeps them all. */
function popupTriggers(found: ControlInfo[]): ControlInfo[] {
  const popups = new Map<Node, Map<string, ControlInfo[]>>();
  for (const info of found) {
    const id = info.driver === "listbox" ? info.el.getAttribute("aria-controls") : null;
    if (!id) continue;
    const root = info.el.getRootNode();
    const ids = popups.get(root) ?? new Map<string, ControlInfo[]>();
    ids.set(id, [...(ids.get(id) ?? []), info]);
    popups.set(root, ids);
  }
  const dropped = new Set<ControlInfo>();
  for (const ids of popups.values()) {
    for (const group of ids.values()) {
      const best = Math.max(...group.map((g) => triggerScore(g.el)));
      for (const g of group) if (triggerScore(g.el) < best) dropped.add(g);
    }
  }
  return dropped.size ? found.filter((i) => !dropped.has(i)) : found;
}

export function scanControls(doc: Document, opts: ScanOptions = {}): ControlInfo[] {
  return sharingAnchors(() => scanPage(doc, opts));
}

function scanPage(doc: Document, opts: ScanOptions): ControlInfo[] {
  const cache: RenderCache = new WeakMap();
  const labelCtx: LabelContext = { docTitle: doc.title, ...pageConventions(doc) };
  const out: ControlInfo[] = [];
  const seen = new Set<Element>();
  const signups = new Map<Element, boolean>();
  const roots: ParentNode = scanRoot(doc) ?? doc.body ?? doc;

  walkElements(roots, (el) => {
    if (seen.has(el)) return;
    const tag = el.localName;
    const isInput = tag === "input";
    const isEditable =
      (el.getAttribute("contenteditable") === "" ||
        el.getAttribute("contenteditable") === "true" ||
        el.getAttribute("contenteditable") === "plaintext-only" ||
        (el.getAttribute("role") === "textbox" && el.getAttribute("aria-multiline") === "true")) &&
      !el.closest("input, textarea");
    const native = isInput || tag === "textarea" || tag === "select" || isEditable;
    const seg = isInput ? dateSegments(el) : null;
    if (seg) {
      if (seg.parts.some((p) => seen.has(p))) return;
      seg.parts.forEach((p) => seen.add(p));
      const info = buildSegmentInfo(seg, labelCtx, cache);
      if (info) out.push(info);
      return;
    }
    const driver = driverOf(el);
    if (!native && !driver) return;
    if (!native && (insideSelect2Widget(el) || insideTinymceEditor(el))) return;
    if (isInput && (wrapperOwning(el) || isPillProxy(el))) return;
    const type = isInput ? ((el.getAttribute("type") ?? "text").toLowerCase()) : "";
    if (isInput && SKIP_INPUT.has(type)) return;
    if (el.closest(CHROME) || inForeignSurface(el) || signupForm(el, signups)) return;

    const html = el as HTMLElement;
    if (driver === "toggle") {
      const group = toggleGroupOf(el);
      if (!group || group.some((g) => seen.has(g))) return;
      group.forEach((g) => seen.add(g));
      const proxies = toggleProxies(group);
      proxies.forEach((p) => seen.add(p));
      if (!isRendered(group[0] as HTMLElement, cache) || ariaHiddenBlocks(group[0] as HTMLElement)) return;
      const label = resolveLabel(group[0] as HTMLElement, group, { ...labelCtx, ignore: proxies });
      const sensitivity = classifySensitivity(group[0] as HTMLElement, label.text);
      if (sensitivity === "never") return;
      const info = buildToggleInfo(group, label, labelCtx, sensitivity);
      if (info) out.push(info);
      return;
    }
    if (driver === "listbox" && el.localName !== "input") el.querySelectorAll("input").forEach((i) => seen.add(i));
    const isChoice = isInput && (type === "radio" || type === "checkbox");
    const isFile = isInput && type === "file";
    if (isDisabled(el) || readOnlyBlocks(el, driver)) return;
    // The host page of an iCIMS posting has job-search widgets of its own; the form is in the content frame.
    if (isInput && el.closest('[id^="advanced-search"]') && doc.getElementById("icims_content_iframe")) return;
    if (isInput && isOracleCountryCode(el)) return;
    // Select2 marks its own native select aria-hidden, and TinyMCE hides its textarea: a person reaches the widget drawn beside it.
    const editor = tinymceEditor(el);
    const reached = driver === "select2" ? (select2Container(el as HTMLSelectElement) ?? el) : (editor ?? el);
    if (ariaHiddenBlocks(reached) && !(isFile && opts.hiddenUploads)) return;
    if (isChoice) {
      if (!choiceVisible(html as HTMLInputElement, cache)) return;
    } else if (isFile) {
      const holder = el.parentElement;
      // The uploader of SuccessFactors sits in a form that opens only when its trigger is pressed.
      if (holder && !isRendered(holder, cache) && !isSfUploader(el) && !opts.hiddenUploads) return;
    } else if (!isRendered(editor ?? el, cache) && !(driver === "icims" && isRendered((el.parentElement as Element).querySelector("a.dropdown-select") as Element, cache))) return;

    // Group first so members are consumed together.
    let members: HTMLInputElement[] = [html as HTMLInputElement];
    if (isChoice) {
      members = groupMembers(html as HTMLInputElement);
      if (members.length === 0) return;
      for (const m of members) seen.add(m);
    }
    const first = isChoice ? (members[0] as HTMLInputElement) : html;
    const label = resolveLabel(first, isChoice ? members : [first], labelCtx);
    const sensitivity = classifySensitivity(first, label.text);
    if (sensitivity === "never" && !opts.inventory) return;
    if (driver === "sr-component" && isOfficeLocation(label.text) && srHost(first)) return;
    // A clear or chevron button inside a combobox has no text and asks nothing; its wrapper is the question.
    if (driver === "listbox" && !isInput && label.source === "none" && collapse(el.textContent ?? "") === "" && el.parentElement?.closest("[role=combobox]")) return;
    // Select2 clips its native select to a pixel on purpose: that select is the field, not a decoy.
    if (!isChoice && !isFile && driver !== "select2") {
      if (isHoneypotLabel(label.text) || isMeasurementTwin(el) || isConcealedDecoy(el, label.source !== "placeholder" && (label.text !== "" || label.source !== "none"))) return;
    }
    const nameAttr = [el.getAttribute("name"), el.getAttribute("id"), el.getAttribute("aria-label"), el.getAttribute("placeholder")].join(" ");
    // A site search box is not a question; a Workday prompt's input is also
    // labelled "Search", but it is the field its label names.
    if (!isChoice && !isFile && driver !== "workday-prompt" && /(^|[\s_-])search($|[\s_-])/i.test(nameAttr)) return;

    const info = buildInfo(first, members, label, type, labelCtx, sensitivity, driver);
    if (info) out.push(info);
  });
  const asked = popupTriggers(out);
  qualifyStems(asked);
  return asked;
}

function buildInfo(
  el: HTMLElement,
  members: HTMLInputElement[],
  label: Label,
  type: string,
  labelCtx: LabelContext,
  sensitivity: Sensitivity,
  driver: Driver | null,
): ControlInfo | null {
  const section = sectionPath(el, label.titleEl);
  const tag = el.localName;
  let kind: RowKind;
  let wireKind: QuestionKind = "text";
  let options: Option[] = [];
  let inputType: InputType = "text";
  let answered = false;
  let maxLength: number | undefined;
  let admitted = true;
  let needsOptions = false;

  const max = parseInt(el.getAttribute("maxlength") ?? "", 10);
  if (max > 0) maxLength = max;

  if (driver === "icims") {
    // The select is a hidden carrier; the custom list is what a person chooses from.
    kind = "custom-listbox";
    wireKind = "custom-listbox";
    inputType = "single_select";
    options = isSearchWidget(el) ? [] : icimsRows(el).map((r) => ({ label: r.label, value: r.value }));
    answered = icimsDisplay(el) !== "" || readSelect(el as HTMLSelectElement) !== "";
  } else if (type === "radio" || type === "checkbox") {
    kind = type === "radio" ? "radio-group" : "checkbox";
    wireKind = type === "radio" ? "radio-group" : "checkbox";
    // A radio with no text of its own is not an answer a person can pick.
    options = members.map((m) => ({ label: optionLabel(m), value: m.value })).filter((o) => type !== "radio" || o.label !== "");
    answered = members.some((m) => m.checked);
    const multi = type === "checkbox" && members.length > 1 && (MULTI.test(label.text) || /\[\]$/.test(el.getAttribute("name") ?? "") || el.closest(`${ASHBY_CHECKBOX_GROUP}, ${LEVER_CHECKBOX_LIST}, ${WORKDAY_CHECKBOX_GROUP}`) !== null);
    inputType = multi ? "multi_select" : yesNo(options) || members.length === 1 ? "yes_no" : "single_select";
    if (type === "radio" && members.length === 1) {
      // A lone radio is a question only when the page insists on it.
      admitted = el.hasAttribute("required") || el.getAttribute("aria-required") === "true" || label.marked;
    } else if (type === "checkbox" && members.length === 1) {
      const required = el.hasAttribute("required") || el.getAttribute("aria-required") === "true" || label.marked;
      admitted = required || looksLikeConsent(label.text);
    } else if (type === "checkbox") {
      admitted = multi || yesNo(options) || members.some((m) => m.hasAttribute("required")) || label.marked;
    }
  } else if (type === "file") {
    kind = "file";
    answered = ((el as HTMLInputElement).files?.length ?? 0) > 0;
    admitted = false;
  } else if (tag === "select") {
    kind = "select";
    wireKind = "select";
    const sel = el as HTMLSelectElement;
    options = Array.from(sel.options)
      .filter((o, i) => !isPlaceholderOption(o, i))
      .map((o) => ({ label: optionText(o), value: o.value }));
    inputType = sel.multiple ? "multi_select" : "single_select";
    // A search-backed Select2 holds no option until it is opened: its rows are read first.
    needsOptions = driver === "select2" && options.length === 0;
    answered = readSelect(sel) !== "";
  } else if (driver === "workday-prompt") {
    kind = "custom-listbox";
    wireKind = "custom-listbox";
    // A search prompt is asked with no options: the answer is typed and searched.
    inputType = isMultiValue(el) ? "multi_select" : "single_select";
    answered = pillTexts(el).length > 0;
  } else if (driver === "listbox") {
    kind = "custom-listbox";
    wireKind = "custom-listbox";
    inputType = isMultiValue(el) ? "multi_select" : "single_select";
    needsOptions = true;
    answered = readElement(el) !== "";
  } else if (driver === "calendar") {
    kind = "text";
    inputType = "date";
    answered = /^\d{4}-\d{2}-\d{2}$/.test(collapse(el.textContent ?? ""));
  } else if (driver === "react-widgets" || driver === "pcty-select") {
    // The list is not readable without the page's own handler: the question is asked without options.
    kind = "custom-listbox";
    wireKind = "custom-listbox";
    inputType = "single_select";
    answered = readElement(el) !== "";
  } else if (driver === "lever-location") {
    kind = "text";
    inputType = "text";
    answered = readElement(el) !== "";
  } else if (driver === "oracle-cx" || driver === "oracle-address") {
    kind = "custom-listbox";
    wireKind = "custom-listbox";
    inputType = "single_select";
    // A select's rows are read before it is asked; the address master is a search box and is asked with no options.
    needsOptions = driver === "oracle-cx";
    answered = readElement(el) !== "";
  } else if (driver === "sdf" || driver === "dijit") {
    kind = "custom-listbox";
    wireKind = "custom-listbox";
    inputType = "single_select";
    needsOptions = true;
    answered = readElement(el) !== "";
  } else if (driver === "sr-component" && srChoiceBox(el)) {
    kind = "custom-listbox";
    wireKind = "custom-listbox";
    inputType = "single_select";
    needsOptions = true;
    answered = readElement(el) !== "";
  } else if (tag === "textarea") {
    kind = "textarea";
    inputType = textInputType(el, type);
    answered = readElement(el).trim() !== "";
  } else if (tag === "input") {
    kind = "text";
    inputType = textInputType(el, type);
    if (!TEXT_TYPES.has(type) && sensitivity !== "never") return null;
    answered = readElement(el) !== "";
  } else {
    kind = "contenteditable";
    inputType = "long_text";
    answered = collapse(el.textContent ?? "") !== "";
    // A document-style editor gives every paragraph an editable region: only one bound to a form, fieldset, dialog or label, or named, is a field.
    if (label.source === "none" && !el.closest("form, fieldset, dialog, label")) return null;
  }

  const selector = type === "radio" || type === "checkbox" ? choiceSelector(el, members) : selectorFor(el);
  const info: ControlInfo = {
    el,
    members,
    kind,
    inputType,
    wireKind,
    selector,
    label,
    ...requirement(el, members, label, labelCtx, kind === "file"),
    section,
    maxLength,
    options,
    sensitivity,
    owner: ownerOf(label, kind, el, section),
    answered,
    admitted,
    driver,
    toggles: [],
    needsOptions,
  };
  return info;
}

/** Month, day and year inputs of one date: one control, asked once when it is a questionnaire answer. */
function buildSegmentInfo(seg: NonNullable<ReturnType<typeof dateSegments>>, labelCtx: LabelContext, cache: RenderCache): ControlInfo | null {
  const first = seg.parts[0] as HTMLInputElement;
  if (seg.parts.some((p) => p.disabled || p.hasAttribute("readonly")) || ariaHiddenBlocks(first) || !isRendered(seg.wrapper, cache)) return null;
  const label = resolveLabel(first, seg.parts, labelCtx);
  const sensitivity = classifySensitivity(first, label.text);
  if (sensitivity === "never") return null;
  return {
    el: seg.wrapper,
    members: seg.parts,
    kind: "text",
    inputType: "date",
    wireKind: "text",
    selector: selectorFor(seg.wrapper),
    label,
    ...requirement(first, seg.parts, label, labelCtx, false),
    section: sectionPath(first, label.titleEl),
    options: [],
    sensitivity,
    // A row's own date stays with the profile lane; only a questionnaire date is the matcher's.
    owner: seg.questionnaire ? "matcher" : "profile",
    answered: seg.parts.every((p) => p.value.trim() !== ""),
    admitted: true,
    driver: "date-segments",
    toggles: [],
    needsOptions: false,
  };
}

function buildToggleInfo(group: HTMLElement[], label: Label, labelCtx: LabelContext, sensitivity: Sensitivity): ControlInfo {
  const first = group[0] as HTMLElement;
  const lone = group.length === 1;
  const options = group.map((g) => ({ label: toggleLabel(g), value: g.getAttribute("value") ?? g.getAttribute("data-value") ?? "" }));
  const marked = first.getAttribute("aria-required") === "true" || label.marked;
  const kind: RowKind = lone ? "checkbox" : "radio-group";
  return {
    el: first,
    members: [],
    kind,
    inputType: lone || yesNo(options) ? "yes_no" : "single_select",
    wireKind: lone ? "checkbox" : "radio-group",
    selector: selectorFor(first),
    label,
    ...requirement(first, group, label, labelCtx, false),
    section: sectionPath(first, label.titleEl),
    options,
    sensitivity,
    owner: "matcher",
    answered: group.some(toggleOn),
    admitted: lone ? marked || looksLikeConsent(label.text) : true,
    driver: "toggle",
    toggles: group,
    needsOptions: false,
  };
}

/** A date shown as month, day and year inputs reads as ISO once every segment holds a number, else as empty; with no day input it reads as year-month. */
function readSegments(parts: HTMLInputElement[]): string {
  const part = (re: RegExp) => parts.find((p) => re.test(p.id))?.value.trim() ?? "";
  const m = part(/dateSectionMonth-input$/i);
  const d = part(/dateSectionDay-input$/i);
  const y = part(/dateSectionYear-input$/i);
  const hasDay = parts.some((p) => /dateSectionDay-input$/i.test(p.id));
  if (!/^\d{1,2}$/.test(m) || !/^\d{4}$/.test(y) || (hasDay && !/^\d{1,2}$/.test(d))) return "";
  return hasDay ? `${y}-${m.padStart(2, "0")}-${d.padStart(2, "0")}` : `${y}-${m.padStart(2, "0")}`;
}

/** The current value of a control or group, as one reader reports it. */
export function readInfo(info: ControlInfo): string {
  if (info.driver === "date-segments") return readSegments(info.members);
  if (info.toggles.length) return readToggles(info.toggles);
  if (info.kind === "radio-group" || info.kind === "checkbox") return readGroup(info.members);
  return readElement(info.el);
}

/** The question for the matcher, or null when the control is not the matcher's. */
export function toQuestion(info: ControlInfo): WireQuestion | null {
  if (info.owner !== "matcher" || !info.admitted || info.needsOptions) return null;
  if (!info.label.text || info.kind === "file") return null;
  const big = info.options.length > MAX_OPTIONS_PER_QUESTION;
  // A menu offering exactly Yes and No is a Yes/No question, whatever it is made of.
  const menu = (info.kind === "select" || info.kind === "custom-listbox") && info.inputType === "single_select" && yesNo(info.options);
  const q: WireQuestion = {
    selector: info.selector,
    text: info.label.text,
    kind: info.wireKind,
    inputType: menu ? "yes_no" : info.inputType,
    options: big ? [] : info.options.map((o) => o.label),
    required: info.required,
  };
  if (info.section.length) q.section = info.section;
  if (info.maxLength !== undefined) q.maxLength = info.maxLength;
  if (info.label.description) q.description = info.label.description;
  return q;
}

export function isFreeText(info: ControlInfo): boolean {
  return info.kind === "textarea" || info.kind === "contenteditable" || info.inputType === "long_text";
}
