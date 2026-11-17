// Clean-room: docs/provenance.md#CR-ENGINE
// Value-shape guards: a value the control's own type or wording cannot hold is
// refused before any write, and a value it can hold is put in its shape.

import { datePartSeparator, formatDate, isoDate, layoutHint, monthValue, parseDate, rangeEdge, todayFor } from "./dates";
import { collapse } from "./dom";
import { detectsCountry, looksLikePhone, nanpDigits, phoneHint, separateDialCode, shapePhone } from "./phone";
import { findPhoneCountryPicker } from "./phone-country";
import { STATE_CODES, STATE_NAMES } from "./states";

/** A refusal carries `detail` when the page's own bound refused the value: the user is told the bound. */
export type Shaped = { ok: true; value: string; perChar: boolean } | { ok: false; detail?: string };

const refuse: Shaped = { ok: false };
const ISO_DAY = /^\d{4}-\d{2}-\d{2}$/;
const pass = (value: string, perChar = false): Shaped => ({ ok: true, value, perChar });

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const ANY_SITE = /website|portfolio|blog|personal site|deployed|demo|other (link|site|url)/i;
const SALARY = /salary|compensation|\bpay\b|\bwage\b|\bbase\b.*\bpay\b|expected earnings/i;
const COUNT = /\b(years?|yrs?|how many)\b/i;
const NETWORKS: [string, string][] = [
  ["linkedin", "linkedin.com"],
  ["github", "github.com"],
];

function wordingOf(el: Element, label: string): string {
  return [label, el.getAttribute("name"), el.getAttribute("id"), el.getAttribute("placeholder"), el.getAttribute("title")]
    .filter(Boolean)
    .join(" ");
}

function urlHost(value: string): string | null {
  const v = value.trim();
  if (/\s/.test(v)) return null;
  const m = /^(?:[a-z][a-z0-9+.-]*:\/\/)?(?:www\.)?([^/?#:]+)(?:[:/?#]|$)/i.exec(v);
  if (!m) return null;
  const host = (m[1] as string).toLowerCase();
  return host.includes(".") ? host : null;
}

/** A URL on another host is refused for a control that names exactly one network. */
function networkOk(wording: string, value: string): boolean {
  const w = wording.toLowerCase();
  const named = NETWORKS.filter(([n]) => w.includes(n));
  if (named.length !== 1 || ANY_SITE.test(wording)) return true;
  const host = urlHost(value);
  if (!host) return true;
  const want = (named[0] as [string, string])[1];
  return host === want || host.endsWith(`.${want}`);
}

/**
 * Lower bound of a range, with k and m scaling; null when the text holds no number. A bare number joined to a
 * scaled one by a range mark takes that scale: "$120-150k" starts at 120000.
 */
export function lowerBound(value: string): string | null {
  const found = [...value.matchAll(/(\d[\d,]*(?:\.\d+)?)\s*([km])?(?![a-z])/gi)];
  if (!found.length) return null;
  const nums: number[] = [];
  let scale = 1;
  for (let i = found.length - 1; i >= 0; i--) {
    const m = found[i] as RegExpMatchArray;
    const suffix = (m[2] ?? "").toLowerCase();
    const next = found[i + 1];
    const between = next ? value.slice((m.index as number) + m[0].length, next.index) : "";
    if (suffix === "k") scale = 1000;
    else if (suffix === "m") scale = 1_000_000;
    else if (!next || !/^\s*(?:-|–|—|to)\s*\$?\s*$/i.test(between)) scale = 1;
    nums.push(parseFloat((m[1] as string).replace(/,/g, "")) * scale);
  }
  return String(Math.round(Math.min(...nums)));
}

/** Exactly one plain number, thousands commas dropped. */
export function plainNumber(value: string): string | null {
  const t = value.trim().replace(/^\$/, "");
  if (/^\d{1,3}(,\d{3})+(\.\d+)?$/.test(t)) return t.replace(/,/g, "");
  return /^\d+(\.\d+)?$/.test(t) ? t : null;
}

function numberInRange(el: HTMLInputElement, raw: string): string {
  let n = parseFloat(raw);
  const min = el.getAttribute("min") !== null ? parseFloat(el.getAttribute("min") as string) : null;
  const max = el.getAttribute("max") !== null ? parseFloat(el.getAttribute("max") as string) : null;
  const stepAttr = el.getAttribute("step");
  const step = stepAttr && stepAttr !== "any" ? parseFloat(stepAttr) : null;
  if (min !== null && Number.isFinite(min) && n < min) n = min;
  if (max !== null && Number.isFinite(max) && n > max) n = max;
  if (step && step > 0) {
    const base = min !== null && Number.isFinite(min) ? min : 0;
    n = base + Math.round((n - base) / step) * step;
    if (max !== null && Number.isFinite(max) && n > max) n -= step;
  }
  return String(Number(n.toFixed(6)));
}

function isNumericOnlyText(el: HTMLInputElement): boolean {
  const mode = (el.getAttribute("inputmode") ?? "").toLowerCase();
  const pattern = el.getAttribute("pattern") ?? "";
  return mode === "numeric" || mode === "decimal" || /^\^?(\[0-9\]|\\d)[*+]?\$?$/.test(pattern);
}

function stateValue(el: HTMLInputElement, wording: string, value: string): string {
  const v = collapse(value);
  const code = v.length === 2 ? v.toUpperCase() : (STATE_CODES[v.toLowerCase()] ?? null);
  const max = parseInt(el.getAttribute("maxlength") ?? "", 10);
  const wantsCode = (max > 0 && max <= 2) || /\b(abbrev\w*|code)\b/i.test(wording);
  if (wantsCode) return code ?? v;
  if (v.length === 2) return STATE_NAMES[v.toUpperCase()] ?? v;
  return v;
}

function isStateControl(el: Element, wording: string): boolean {
  if ((el.getAttribute("autocomplete") ?? "").includes("address-level1")) return true;
  return /(^|\s)(state|province)(\s|$|\/)/i.test(wording.replace(/[_-]/g, " ")) && !/\b(statement|united states)\b/i.test(wording);
}

export interface ShapeContext {
  now: Date;
  /** Marks the value as the derived current date: it is re-derived here from the local clock. */
  currentDate?: boolean;
  /** The profile phone key sets this so any phone-like control is shaped. */
  phone?: boolean;
}

/** Shape (or refuse) a value for a text-like control. Other controls pass through. */
export function shapeValue(el: HTMLElement, label: string, rawValue: string, ctx: ShapeContext): Shaped {
  if (el.localName !== "input") return pass(rawValue);
  const input = el as HTMLInputElement;
  const type = (input.getAttribute("type") ?? "text").toLowerCase();
  const ac = (input.getAttribute("autocomplete") ?? "").toLowerCase();
  const wording = wordingOf(input, label);
  const value = rawValue;

  if (ctx.currentDate) {
    const hint = `${input.getAttribute("placeholder") ?? ""} ${label} ${input.getAttribute("pattern") ?? ""}`;
    return pass(todayFor(ctx.now, type, hint));
  }
  if (type === "email" || ac.includes("email")) return EMAIL.test(value.trim()) ? pass(value.trim()) : refuse;
  if (!networkOk(wording, value)) return refuse;

  if (type === "month") {
    const m = monthValue(value, wording);
    return m ? pass(m) : refuse;
  }
  if (type === "date") {
    // A month and year (a graduation a person states with no day) is its first
    // day, as a picker's and an education row's are; a range gives the edge
    // the wording names.
    const p = parseDate(rangeEdge(value, wording));
    if (!p) return refuse;
    const iso = isoDate(`${p.y}-${p.m}-${p.d ?? 1}`);
    if (!iso) return refuse;
    // The page's own bounds: Citadel's Graduation Date takes only dates after today.
    const min = input.getAttribute("min") ?? "";
    const max = input.getAttribute("max") ?? "";
    if (ISO_DAY.test(min) && iso < min) return { ok: false, detail: `the page accepts dates from ${min} on` };
    if (ISO_DAY.test(max) && iso > max) return { ok: false, detail: `the page accepts dates up to ${max}` };
    return pass(iso);
  }
  if (type === "number") {
    // A phone the page renders as a number box (Freed's Ashby "Phone Number" Number field) takes the number's digits,
    // a North American number its ten: "+", spaces and dashes are no number.
    if ((ac.startsWith("tel") || /\b(phone|mobile|cell)\b/i.test(wording)) && looksLikePhone(value)) {
      return pass(nanpDigits(value) ?? value.replace(/\D/g, ""));
    }
    const n = SALARY.test(wording) ? lowerBound(value) : plainNumber(value);
    return n === null ? refuse : pass(numberInRange(input, n));
  }
  if (type === "text" || type === "tel" || type === "search" || type === "url" || type === "") {
    const idName = `${input.getAttribute("id") ?? ""} ${input.getAttribute("name") ?? ""}`;
    if (/-numeric\b|_numeric\b/i.test(idName)) {
      const n = plainNumber(value);
      return n === null ? refuse : pass(n);
    }
    // A picker keeps text it cannot parse while the form holds no date: it gets one parsable date, or nothing.
    if (input.closest(".react-datepicker__input-container")) {
      const p = parseDate(rangeEdge(value, wording));
      if (!p) return refuse;
      const hint = `${input.getAttribute("placeholder") ?? ""} ${label}`;
      return pass(formatDate({ y: p.y, m: p.m, d: p.d ?? 1 }, layoutHint(hint) ?? "us", datePartSeparator(hint)));
    }
    const phoneish = type === "tel" || ac.startsWith("tel") || (/\b(phone|mobile|cell)\b/i.test(wording) && ctx.phone === true);
    if (phoneish && looksLikePhone(value)) return pass(shapePhoneFor(input, label, value), true);
    // A digits-only box proves only that it holds digits: a ZIP code keeps its leading zero, a GPA its decimals,
    // and an answer with no digit is refused.
    const numericOnly = isNumericOnlyText(input);
    if (numericOnly && !/\d/.test(value)) return refuse;
    // A salary box takes the amount as one number: Workday's digits-only text box
    // keeps each digit it is typed, so a range "$100K-250K" would commit as "100250".
    // A digits-only box whose wording asks for a count takes a range's lower bound the same way.
    const amount = SALARY.test(wording) ? /\d/.test(value) : numericOnly && COUNT.test(wording) && plainNumber(value) === null;
    if (amount) {
      const n = lowerBound(value);
      return n === null ? refuse : pass(n);
    }
    if (isStateControl(input, wording)) return pass(stateValue(input, wording, value));
    const maskAttr = ["data-mask", "data-inputmask", "mask"].some((a) => input.hasAttribute(a));
    const dateSection = /dateSection/i.test(input.getAttribute("id") ?? "");
    const hint = `${input.getAttribute("placeholder") ?? ""} ${label} ${input.getAttribute("pattern") ?? ""} ${input.getAttribute("data-mask") ?? ""}`;
    const layout = layoutHint(hint);
    if (layout) {
      const p = parseDate(value);
      if (p) return pass(formatDate(p, layout, datePartSeparator(hint)), maskAttr || dateSection);
    }
    return pass(value, maskAttr || dateSection);
  }
  return pass(value);
}

function shapePhoneFor(input: HTMLInputElement, label: string, value: string): string {
  if (detectsCountry(input)) {
    const d = nanpDigits(value);
    return d ? `+1${d}` : value;
  }
  // A country-code picker beside the box owns the dial code, and a box the page marks tel-national asks for
  // the number without it (Oracle's, beside a picker it labels by id): the box takes the national digits.
  if (findPhoneCountryPicker(input) || input.getAttribute("autocomplete")?.trim().toLowerCase() === "tel-national") {
    const d = nanpDigits(value);
    if (d) return d;
  }
  const dial = separateDialCode(input);
  if (dial) {
    const d = nanpDigits(value);
    return dial === "+1" && d ? d : value;
  }
  return shapePhone(value, phoneHint(input, label));
}
