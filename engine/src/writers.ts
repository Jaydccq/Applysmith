// Clean-room: docs/provenance.md#CR-ENGINE
// Native control writers. Each writer reads its control back after the settle
// delay and returns whether the write held; the caller trusts that outcome and
// compares nothing again.

import { safeToggleClick } from "./click-guard";
import { waitFor, type WriteCtx } from "./env";
import {
  collapse,
  fire,
  fireInput,
  nativeValueGetter,
  nativeValueSetter,
  normalize,
  stripSchoolingGloss,
  whole,
  winOf,
} from "./dom";
import { typeText } from "./gestures";
import { detectsCountry } from "./phone";
import { isPlacesInput } from "./places";
import { STATE_CODES, STATE_NAMES } from "./states";
import { labelElements, optionLabel } from "./labels";
import type { RowReason } from "./protocol";
import { isPlaceholderOption, optionText } from "./reader";
import { isRendered } from "./visibility";

export type WriteOutcome =
  | { ok: true; optionLabel?: string }
  | {
      ok: false;
      reason: RowReason;
      /** The driver looked and found nothing it may commit: a deliberate blank, not a failed write. */
      abstain?: boolean;
      /** The last stage a driver reached and the labels its menu showed; for the diagnostic log. */
      detail?: string;
      /** What a menu the question was asked without turned out to hold, for the check to choose from. */
      options?: string[];
    };

const fail = (reason: RowReason): WriteOutcome => ({ ok: false, reason });
const ok = (optionLabel?: string): WriteOutcome => ({ ok: true, optionLabel });

const DECLINE = [
  "prefer not to say",
  "i prefer not to say",
  "prefer not to answer",
  "i prefer not to answer",
  "decline to answer",
  "i do not wish to answer",
  "decline to self identify",
  "i do not wish to self identify",
  "choose not to disclose",
  "i choose not to disclose",
];

/** What a search box is asked for, after the answer itself, when the answer declines: one wording of each kind an option uses. */
const DECLINE_SEEDS = ["choose not to disclose", "prefer not to say", "decline to answer"];

const ALIASES: string[][] = [
  ["yes", "y", "true"],
  ["no", "n", "false"],
  DECLINE,
];

/** The one table of the other names a country goes by, keyed by its common name. */
const COUNTRY_ALIASES: Record<string, string[]> = {
  "united states": ["us", "usa", "united states of america"],
  "united kingdom": ["uk", "great britain", "gb"],
  "united arab emirates": ["uae"],
};

const COUNTRY_BY_ALIAS = new Map(Object.entries(COUNTRY_ALIASES).flatMap(([name, aliases]) => aliases.map((alias) => [alias.replace(/ /g, ""), name] as const)));

/** The common name, in lower case, of the country a text is another name of, its dots and spaces ignored ("U.S.", "UAE", "Great Britain"); undefined for anything else. */
export function countryOfAlias(text: string): string | undefined {
  return COUNTRY_BY_ALIAS.get(normalize(text).replace(/ /g, ""));
}

/** Every name a country goes by, its common name first; a country with no aliases is its own name. */
export function countryNames(country: string): string[] {
  const n = normalize(country);
  const group = Object.entries(COUNTRY_ALIASES).find(([k, v]) => k === n || v.includes(n));
  return group ? [group[0], ...group[1]] : [n];
}

/** Whether an answer declines to answer, however it words it. */
export function isDecline(answer: string): boolean {
  return DECLINE.includes(normalize(answer));
}

/** The other words a search box can be asked for an answer that declines; none for any other answer. */
export function declineSeeds(answer: string): string[] {
  return isDecline(answer) ? DECLINE_SEEDS.filter((x) => x !== normalize(answer)) : [];
}

function aliasGroup(norm: string): string[] | undefined {
  return ALIASES.find((g) => g.includes(norm));
}

type Polarity = "yes" | "no";

/** The polarity a text opens with ("No, I will not require ..." opens with no); a lone Y or N is a yes or a no. */
function leadingPolarity(norm: string): Polarity | null {
  if (norm === "y") return "yes";
  if (norm === "n") return "no";
  const first = norm.split(" ")[0];
  if (first === "yes" || first === "true") return "yes";
  if (first === "no" || first === "false") return "no";
  return null;
}

const MONTH_NAMES = ["january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"];

/** The month a full name or its abbreviation ("sep", "sept") names, one-based; 0 when the text is no month. */
function monthOf(norm: string): number {
  const at = MONTH_NAMES.findIndex((m) => m === norm || (norm.length >= 3 && m.startsWith(norm) && (norm.length === 3 || norm === "sept")));
  return at + 1;
}

/** The polarity of an answer that is only a yes or no word. */
function bareAnswerPolarity(norm: string): Polarity | null {
  if ((ALIASES[0] as string[]).includes(norm)) return "yes";
  if ((ALIASES[1] as string[]).includes(norm)) return "no";
  return null;
}

/**
 * How well an option label answers the text: 3 exact, 2 whole-word phrase
 * either way or a bare yes or no matching the label's leading polarity, else 0.
 * A label and an answer that open with opposite polarity never match.
 */
const DIAL_SUFFIX = /\s*\(?\+\d{1,4}\)?\s*$/;

/** A country option that closes with its dial code ("United States +1") names that country, unless the answer is itself a dial code. */
function stripDialSuffix(label: string, answer: string): string {
  if (/\+\d/.test(answer)) return label;
  const bare = label.replace(DIAL_SUFFIX, "").trim();
  return bare !== label.trim() && /\p{L}/u.test(bare) ? bare : label;
}

/** A leading article names no different thing: "The University of Westbridge". */
const LEADING_THE = /^the /;

export function scoreOption(label: string, answer: string): number {
  const a = normalize(stripSchoolingGloss(answer));
  const l = normalize(stripSchoolingGloss(stripDialSuffix(label, answer)));
  if (!a || !l) return 0;
  if (a === l || a.replace(LEADING_THE, "") === l.replace(LEADING_THE, "")) return 3;
  const month = monthOf(a);
  if (month !== 0 && month === monthOf(l)) return 3;
  const lead = leadingPolarity(l);
  const bare = bareAnswerPolarity(a);
  if (bare && lead) return bare === lead ? 2 : 0;
  const answerLead = leadingPolarity(a);
  if (answerLead && lead && answerLead !== lead) return 0;
  if ((a.length >= 3 && whole(l, a)) || (l.length >= 3 && whole(a, l))) return 2;
  // Two ways of declining to answer are one answer.
  if (DECLINE.includes(a) && DECLINE.includes(l)) return 2;
  return 0;
}

export function sameValue(read: string, expected: string, el: Element): boolean {
  if (read === expected) return true;
  if (collapse(read) === collapse(expected)) return true;
  const digits = expected.replace(/\D/g, "");
  const masked = el.hasAttribute("data-mask") || el.hasAttribute("data-inputmask") || el.hasAttribute("mask");
  const tel = (el.getAttribute("type") ?? "") === "tel" || el.getAttribute("inputmode") === "tel" || (el.getAttribute("autocomplete") ?? "").startsWith("tel");
  if ((masked || tel) && digits.length >= 7 && read.replace(/\D/g, "") === digits) return true;
  return false;
}

/**
 * Set while the engine edits through the browser's own editing (execCommand): the browser marks those events trusted,
 * as it marks a person's typing, so the user-edit tracker and the corrections watch take none of them for a person's.
 */
let editing = false;
export const engineEditing = (): boolean => editing;

/**
 * A real edit through the browser; synthesised when the page refuses execCommand. The box is left inside the edit, so the
 * trusted change the browser owes it is dispatched now and not when a person later leaves the box. The browser edits
 * whichever box holds the focus, so the edit is made only when this one took it: Oracle sends the focus back to the first
 * invalid field, and the last name was typed onto the end of the first name.
 */
function insertText(el: HTMLInputElement | HTMLTextAreaElement, value: string): void {
  el.focus();
  const set = nativeValueSetter(el);
  let done = false;
  editing = true;
  try {
    if ((el.getRootNode() as Document | ShadowRoot).activeElement === el) {
      el.select?.();
      done = el.ownerDocument.execCommand?.("insertText", false, value) === true;
    }
    if (done) el.blur();
  } catch {
    done = false;
  } finally {
    editing = false;
  }
  if (!done) {
    set?.(value);
    fireInput(el, value, "insertText");
  }
}

async function typeChars(el: HTMLInputElement | HTMLTextAreaElement, value: string, ctx: WriteCtx): Promise<void> {
  await typeText(el, value, ctx);
  fire(el, "change");
  fire(el, "focusout");
  el.blur();
}

/** Write a text or textarea value and verify it held. */
export async function writeText(
  el: HTMLInputElement | HTMLTextAreaElement,
  value: string,
  perChar: boolean,
  ctx: WriteCtx,
): Promise<WriteOutcome> {
  const set = nativeValueSetter(el);
  if (!set) return fail("write-not-committed");
  const get = nativeValueGetter(el);
  // A one-line box drops a line break instead of turning it into a space, so an
  // answer of several lines is written there on one line.
  if (el.tagName === "INPUT" && /[\r\n]/.test(value)) value = value.replace(/\s*[\r\n]+\s*/g, " ").trim();
  if (perChar) {
    if (detectsCountry(el)) {
      // The widget seeds its own dial code shortly after the first focus; that seed is typed over, not appended to.
      el.focus();
      await ctx.sleep(ctx.timing.seedSettleMs);
    }
    await typeChars(el, value, ctx);
  } else {
    set(value);
    fireInput(el, value, "insertReplacementText");
    fire(el, "change");
  }
  await ctx.sleep(ctx.timing.settleMs);
  let read = get();
  if (read === "" && value !== "" && !perChar && !isPlacesInput(el)) {
    await ctx.sleep(Math.max(0, ctx.timing.emptyRetryMs - ctx.timing.settleMs));
    read = get();
    if (read === "") {
      insertText(el, value);
      fire(el, "change");
      await ctx.sleep(ctx.timing.settleMs);
      read = get();
    }
  }
  return sameValue(read, value, el) ? ok() : fail("write-not-committed");
}

export async function writeEditable(el: HTMLElement, value: string, ctx: WriteCtx): Promise<WriteOutcome> {
  el.focus();
  let done = false;
  editing = true;
  try {
    const doc = el.ownerDocument;
    const range = doc.createRange();
    range.selectNodeContents(el);
    const sel = winOf(el).getSelection?.();
    sel?.removeAllRanges();
    sel?.addRange(range);
    done = doc.execCommand?.("insertText", false, value) === true;
  } catch {
    done = false;
  } finally {
    editing = false;
  }
  if (!done) {
    el.textContent = value;
    const win = winOf(el);
    try {
      el.dispatchEvent(new win.InputEvent("beforeinput", { bubbles: true, cancelable: true, composed: true, data: value, inputType: "insertText" }));
    } catch {
      /* the page did not take a beforeinput */
    }
    fireInput(el, value, "insertText");
  }
  await ctx.sleep(ctx.timing.settleMs);
  // The browser puts each line of an answer in a block of its own and textContent joins the blocks with nothing between
  // them, so the text held is compared with its white space left out.
  const bare = (text: string): string => text.replace(/\s+/g, "");
  return bare(el.textContent ?? "") === bare(value) ? ok() : fail("write-not-committed");
}

/** The part of an option value after a framework's type prefix ("string:NC", "number:7"). */
function valueTail(value: string): string {
  return value.slice(value.lastIndexOf(":") + 1);
}

/** The code and normalized name of the US state an answer names, by either form. */
function stateForms(answer: string): { code: string; name: string } | null {
  const t = answer.trim();
  const code = t.length === 2 ? t.toUpperCase() : STATE_CODES[t.toLowerCase()];
  const name = code ? STATE_NAMES[code] : undefined;
  return code && name ? { code: normalize(code), name: normalize(name) } : null;
}

export type SelectFind = { option: HTMLOptionElement } | { reason: "option-not-found" };

/** The country part of a phone-country row: the dial code, flag marks and brackets removed. */
export function countryPart(label: string): string {
  return normalize(label.replace(/\(?\+\d[\d\s-]*\)?/g, " "));
}

/**
 * Option whose normalized label equals the answer, then whose value does, then
 * an alias, then the single option that contains it or opens with the answer's
 * yes or no. Two such matches are never guessed between. A long list is matched
 * by exact label or value only.
 */
export function findOption(sel: HTMLSelectElement, answer: string, exactOnly: boolean): SelectFind {
  const opts = Array.from(sel.options).filter((o, i) => !isPlaceholderOption(o, i));
  const a = normalize(stripSchoolingGloss(answer));
  if (!a) return { reason: "option-not-found" };
  const byLabel = opts.filter((o) => normalize(stripSchoolingGloss(optionText(o))) === a);
  if (byLabel.length >= 1) return { option: byLabel[0] as HTMLOptionElement };
  const byValue = opts.filter((o) => normalize(o.value) === a || normalize(valueTail(o.value)) === a);
  if (byValue.length === 1) return { option: byValue[0] as HTMLOptionElement };
  const state = stateForms(answer);
  if (state) {
    // A state answered by code against a list of names, or the reverse.
    const named = opts.filter((o) => normalize(optionText(o)) === state.name || normalize(valueTail(o.value)) === state.code);
    if (named.length === 1) return { option: named[0] as HTMLOptionElement };
  }
  // A country answered by its code or long name is the same country in any list, however long ("US" is "United States"),
  // and an option that adds its dial code ("United States (+1)") still names it.
  const country = countryNames(/\+\d/.test(answer) ? countryPart(answer) : answer);
  if (country.length > 1) {
    const named = opts.filter((o) => country.includes(normalize(optionText(o))) || country.includes(countryPart(optionText(o))));
    if (named.length === 1) return { option: named[0] as HTMLOptionElement };
  }
  if (exactOnly) return { reason: "option-not-found" };
  const group = aliasGroup(a);
  if (group) {
    const hit = opts.filter((o) => group.includes(normalize(optionText(o))));
    if (hit.length === 1) return { option: hit[0] as HTMLOptionElement };
  }
  const contained = opts.filter((o) => scoreOption(optionText(o), answer) >= 2);
  if (contained.length === 1) return { option: contained[0] as HTMLOptionElement };
  return { reason: "option-not-found" };
}

/**
 * A select whose options depend on another control (a state list that is empty
 * until the country is set) is waited on, up to the dependent-wait limit, until
 * an option appears that answers.
 */
async function awaitDependentOptions(sel: HTMLSelectElement, answers: string[], ctx: WriteCtx): Promise<void> {
  const real = () => Array.from(sel.options).filter((o, i) => !isPlaceholderOption(o, i)).length;
  const answered = () => answers.some((a) => "option" in findOption(sel, a, Array.from(sel.options).length > 100));
  if (answered() || real() > 1) return;
  await waitFor(ctx, answered, ctx.timing.dependentWaitMs, 150 > ctx.timing.dependentWaitMs ? ctx.timing.pollMs : Math.min(150, ctx.timing.pollMs * 3));
}

const NOT_LISTED = /^other\b.*\bnot listed\b/;
const ENTITY_LIST = /\b(school|schools|university|universities|college|colleges|institution|institutions|employer|employers|company|companies|organi[sz]ation|organi[sz]ations)\b/;

/**
 * The one choice a school or employer list offers for a name it does not hold:
 * the single option that reads "Other ... not listed", when the question or the
 * option itself speaks of a school or an employer. Nothing else stands in for
 * an answer the list lacks.
 */
export function notListedOption(sel: HTMLSelectElement, question: string): HTMLOptionElement | null {
  const opts = Array.from(sel.options).filter((o, i) => !isPlaceholderOption(o, i));
  const hits = opts.filter((o) => NOT_LISTED.test(normalize(optionText(o))));
  if (hits.length !== 1) return null;
  const hit = hits[0] as HTMLOptionElement;
  return ENTITY_LIST.test(normalize(`${question} ${optionText(hit)}`)) ? hit : null;
}

/** No option answers; a list with nothing in it yet depends on a control not set so far, which a later pass may find set. */
function missed(sel: HTMLSelectElement): WriteOutcome {
  const real = Array.from(sel.options).filter((o, i) => !isPlaceholderOption(o, i)).length;
  return real > 1 ? fail("option-not-found") : { ok: false, reason: "option-not-found", detail: "stage=no-options" };
}

export async function writeSelect(sel: HTMLSelectElement, answers: string[], ctx: WriteCtx, question = ""): Promise<WriteOutcome> {
  await awaitDependentOptions(sel, answers, ctx);
  const exactOnly = Array.from(sel.options).length > 100;
  const picked: HTMLOptionElement[] = [];
  for (const answer of answers) {
    const found = findOption(sel, answer, exactOnly);
    if ("reason" in found) {
      if (!sel.multiple) {
        const other = notListedOption(sel, question);
        if (!other) return missed(sel);
        picked.push(other);
        break;
      }
      continue;
    }
    picked.push(found.option);
  }
  if (!picked.length) return missed(sel);
  const commit = () => {
    if (sel.multiple) {
      for (const o of Array.from(sel.options)) o.selected = picked.includes(o);
    } else {
      const target = picked[0] as HTMLOptionElement;
      const set = nativeValueSetter(sel);
      if (set && target.value !== "") set(target.value);
      if (!target.selected) target.selected = true;
    }
  };
  const held = () => picked.every((o) => o.selected) && (sel.multiple || Array.from(sel.options).filter((o) => o.selected).length === 1);
  commit();
  // Verified before any event: a controlled select resets synchronously.
  if (!held()) commit();
  fire(sel, "input");
  fire(sel, "change");
  await ctx.sleep(ctx.timing.selectRecommitMs);
  if (!held()) {
    commit();
    fire(sel, "input");
    fire(sel, "change");
    await ctx.sleep(ctx.timing.settleMs);
  }
  fire(sel, "focusout");
  sel.blur();
  return held() ? ok(optionText(picked[0] as HTMLOptionElement)) : fail("write-not-committed");
}

const AFFIRM = new Set(["yes", "y", "true", "checked", "on", "1", "agree", "i agree", "accept", "i accept"]);
const DENY = new Set(["no", "n", "false", "unchecked", "off", "0", "decline", "i do not agree"]);

/** Target state for a lone box: Yes ticks it, No clears it, its own label text ticks it. */
export function loneTarget(answer: string, ownLabel: string): boolean | null {
  const a = normalize(answer);
  if (AFFIRM.has(a)) return true;
  if (DENY.has(a)) return false;
  if (a && a === normalize(ownLabel)) return true;
  return null;
}

async function toggle(input: HTMLInputElement, want: boolean, ctx: WriteCtx): Promise<WriteOutcome> {
  if (input.checked !== want) {
    let target: HTMLElement = input;
    if (!isRendered(input)) {
      const label = labelElements(input).find((l) => isRendered(l));
      if (label) target = label as HTMLElement;
    }
    if (safeToggleClick(input, target) === "unsafe-click") return fail("unsafe-click");
    if (input.checked !== want) {
      const set = Object.getOwnPropertyDescriptor(winOf(input).HTMLInputElement.prototype, "checked")?.set;
      set?.call(input, want);
      fireInput(input, null, "insertReplacementText");
      fire(input, "change");
    }
  } else {
    // A box the page pre-set still has to tell the page's own state about it.
    fireInput(input, null, "insertReplacementText");
    fire(input, "change");
  }
  for (let i = 0; i < 8 && input.checked !== want; i++) await ctx.sleep(50);
  return input.checked === want ? ok() : fail("write-not-committed");
}

export interface ChoiceAnswer {
  labels: string[];
  /** The raw answer text, used as a value fallback and for a lone box. */
  value: string;
  multi: boolean;
}

export function bestMember(members: HTMLInputElement[], answer: string): HTMLInputElement | "tie" | null {
  const scored = members.map((m) => ({ m, s: scoreOption(optionLabel(m), answer) }));
  const top = Math.max(0, ...scored.map((x) => x.s));
  if (top === 0) {
    const byValue = members.filter((m) => normalize(m.value) === normalize(answer));
    return byValue.length === 1 ? (byValue[0] as HTMLInputElement) : null;
  }
  const best = scored.filter((x) => x.s === top);
  return best.length === 1 ? (best[0] as { m: HTMLInputElement }).m : "tie";
}

/** Whether a choice group already shows exactly the answer. */
export function choiceHolds(members: HTMLInputElement[], answer: ChoiceAnswer): boolean {
  const type = (members[0] as HTMLInputElement).type;
  if (members.length === 1 && type === "checkbox") {
    const want = loneTarget(answer.value, optionLabel(members[0] as HTMLInputElement));
    return want !== null && (members[0] as HTMLInputElement).checked === want;
  }
  if (type === "checkbox" && answer.multi) {
    const bound = answer.labels.map((l) => bestMember(members, l)).filter((m): m is HTMLInputElement => !!m && m !== "tie");
    return bound.length > 0 && bound.every((m) => m.checked);
  }
  const target = bestMember(members, answer.labels[0] ?? answer.value);
  if (!target || target === "tie") return false;
  return target.checked && members.every((m) => m === target || !m.checked);
}

/** Radio group, checkbox group or lone checkbox. */
export async function writeChoice(members: HTMLInputElement[], answer: ChoiceAnswer, ctx: WriteCtx): Promise<WriteOutcome> {
  const type = (members[0] as HTMLInputElement).type;
  if (members.length === 1 && type === "checkbox") {
    const want = loneTarget(answer.value, optionLabel(members[0] as HTMLInputElement));
    if (want === null) return fail("format-not-accepted");
    return toggle(members[0] as HTMLInputElement, want, ctx);
  }
  if (type === "checkbox" && answer.multi) {
    let any = false;
    for (const label of answer.labels) {
      const m = bestMember(members, label);
      if (m && m !== "tie") {
        const r = await toggle(m, true, ctx);
        if (r.ok) any = true;
      }
    }
    return any ? ok() : fail("option-not-found");
  }
  // A plural payload is a multi-select binding or nothing.
  if (answer.labels.length > 1) return fail("format-not-accepted");
  const target = bestMember(members, answer.labels[0] ?? answer.value);
  if (!target || target === "tie") return fail("option-not-found");
  if (type === "radio") {
    const r = await toggle(target, true, ctx);
    if (!r.ok) return r;
    // A page that undoes the click on a later tick has not taken the answer.
    await ctx.sleep(ctx.timing.toggleGapMs);
    if (!target.checked) return fail("verify-mismatch");
    const others = members.some((m) => m !== target && m.checked);
    return others ? fail("write-not-committed") : ok(optionLabel(target));
  }
  const r = await toggle(target, true, ctx);
  return r.ok ? ok(optionLabel(target)) : r;
}

export interface FilePayload {
  fileName: string;
  mediaType: string;
  contentBase64: string;
}

/** Assign a File through a DataTransfer; the caller hands off to the host when the input ignores it. */
export function attachFile(input: HTMLInputElement, payload: FilePayload): "attached" | "needs-native-upload" {
  const win = winOf(input);
  try {
    const bin = win.atob(payload.contentBase64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    const file = new win.File([bytes], payload.fileName, { type: payload.mediaType });
    const dt = new win.DataTransfer();
    dt.items.add(file);
    input.files = dt.files;
    fire(input, "input");
    fire(input, "change");
    return input.files?.length === 1 && input.files[0]?.name === payload.fileName ? "attached" : "needs-native-upload";
  } catch {
    return "needs-native-upload";
  }
}
