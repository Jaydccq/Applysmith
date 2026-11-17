// Clean-room: docs/provenance.md#CR-ENGINE
// Oracle Recruiting Cloud (Candidate Experience): grid comboboxes whose rows
// are content spans, committed by a hover followed by a click on the grid
// cell, and the address master whose suggestions stream in after typing.

import { cleanOptionLabel, classTokens, byIdDeep } from "./popup";
import { collapse, fire, flatText, nativeValueSetter, normalize, whole } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { fireInput } from "./dom";
import { clearText, focusEl, hover, pointerSequence, press, typeText } from "./gestures";
import { STATE_NAMES } from "./states";
import { isRendered } from "./visibility";
import { degreeChooser, seedKind, type AddressHints } from "./choosers";
import { countryNames, type WriteOutcome } from "./writers";

const ROW = ".cx-select__list-item--content";
const NO_RESULTS = /^(no results?( found)?|\d+ matches? found|loading\.*)$/i;

/** The address block's own fields: the master fills the rest, so a cascade field is never opened on its own. */
export const ADDRESS_CASCADE = new Set(["addressLine1", "addressLine2", "city", "region1", "region2", "postalCode"]);

/**
 * A plain grid combobox. The address master is not one (it streams
 * suggestions), and neither is the phone's country-code prefix, whose 300-row
 * list belongs to the phone number beside it.
 */
export function isOracleSelect(el: Element): boolean {
  return el.localName === "input" && classTokens(el).includes("cx-select-input") && el.getAttribute("role") === "combobox" && !isOracleAddress(el) && !isOracleCountryCode(el);
}

/** The country-code prefix of the phone number: never a question, never opened. */
export function isOracleCountryCode(el: Element): boolean {
  return el.localName === "input" && classTokens(el).includes("cx-select-input") && (el.getAttribute("id") ?? "").startsWith("country-codes-dropdown");
}

/** The address master: the addressLine1 box that suggests addresses from the maps service. */
export function isOracleAddress(el: Element): boolean {
  return el.localName === "input" && /addressLine1$/i.test(fieldName(el)) && (classTokens(el).includes("cx-select-input--auto-suggest") || /oracle-maps/i.test(el.getAttribute("aria-describedby") ?? ""));
}

/** The stable field name: ids carry volatile suffixes, the name does not. */
export function fieldName(el: Element): string {
  return (el.getAttribute("name") ?? el.getAttribute("id") ?? "").replace(/-\d+$/, "");
}

export const isCascadeField = (el: Element): boolean => ADDRESS_CASCADE.has(fieldName(el));
export const isOracleCountry = (el: Element): boolean => isOracleSelect(el) && fieldName(el) === "country";

/**
 * The Postal Code of a block with no address master (Address Line 1 a plain box): a server search over every postal
 * code whose row ("29401, Charleston, Charleston, SC") fills City, State and County, so the profile writes it.
 */
export function isOraclePostalLead(el: Element): boolean {
  return isOracleSelect(el) && fieldName(el) === "postalCode" && !Array.from(el.ownerDocument.querySelectorAll("input")).some(isOracleAddress);
}

/**
 * The Work and Education History editor's lists, by their stable id: the month
 * and year pickers, Major, School and Country. Each filters on what is typed;
 * School is a server search that lists only the closest names until the whole
 * name is typed.
 */
const EDITOR_LISTS = /^(?:(?:month|year)-(?:start|end)Date|major|educationalEstablishment|countryCode)$/;
/** School and Country take only the row that equals the answer. */
const EXACT_ONLY = /^(?:educationalEstablishment|countryCode)$/;
const editorId = (el: Element): string => (el.getAttribute("id") ?? "").replace(/-\d+$/, "");

export interface OracleDrive {
  typeFirst: boolean;
  exactOnly: boolean;
  /** A postal list: its code is typed and its row chosen by code, then by place. */
  postal?: boolean;
  /** A degree list: a row it spells differently ("MS - Master of Science") is chosen as any degree list's is. */
  degree?: boolean;
}

/** How a select is driven: typed first (the country gating the address, the editor's lists), and whether only the exact row counts. */
export function oracleDrive(el: Element, label = ""): OracleDrive {
  const id = editorId(el);
  return { typeFirst: isOracleCountry(el) || EDITOR_LISTS.test(id), exactOnly: EXACT_ONLY.test(id), postal: fieldName(el) === "postalCode", degree: seedKind(label) === "degree" };
}

function degreeRow(rows: OracleRow[], answer: string): OracleRow | null {
  const hit = degreeChooser(answer)(rows.map((r) => ({ el: r.cell, label: r.label, value: r.label, selected: false, disabled: false })));
  return rows.find((r) => r.cell === hit?.el) ?? null;
}

export interface OracleRow {
  label: string;
  /** The cell a hover and click commit through. */
  cell: HTMLElement;
}

/** The popup that belongs to the input: its own grid, else the open dropdown, else the document. */
function scopeOf(input: Element): ParentNode {
  const id = input.getAttribute("aria-controls");
  const own = id ? byIdDeep(input, id) : null;
  if (own) return own;
  return input.ownerDocument.querySelector(".cx-select-dropdown--open") ?? input.ownerDocument;
}

export function oracleRows(input: Element): OracleRow[] {
  const out: OracleRow[] = [];
  scopeOf(input)
    .querySelectorAll<HTMLElement>(ROW)
    .forEach((span) => {
      const label = cleanOptionLabel(flatText(span));
      if (!label || NO_RESULTS.test(label)) return;
      const cell = span.closest<HTMLElement>("[role=gridcell]") ?? span;
      if (isRendered(cell)) out.push({ label, cell });
    });
  return out;
}

const STOP = new Set(["a", "an", "the", "i", "am", "are", "is", "of", "to", "and", "or", "you", "your", "my", "be", "have", "do", "does", "this"]);
const NEGATIVE = new Set(["not", "no", "non", "never", "decline", "without"]);

const words = (s: string): string[] => normalize(s).split(" ").filter(Boolean);

/**
 * The row an answer names: the exact text; a unique word-boundary prefix (the
 * answer "No" picks "No, I am not a military spouse", never "Not a Veteran");
 * the row whose content words are all inside the answer, with the most words,
 * never a negative row for a positive answer, and a tie abstains.
 */
export function chooseOracleRow(rows: OracleRow[], answer: string): OracleRow | null {
  const a = normalize(answer);
  if (!a) return null;
  const exact = rows.filter((r) => normalize(r.label) === a);
  if (exact.length) return exact.length === 1 ? (exact[0] as OracleRow) : null;
  const prefix = rows.filter((r) => normalize(r.label).startsWith(`${a} `));
  if (prefix.length) return prefix.length === 1 ? (prefix[0] as OracleRow) : null;
  const aw = new Set(words(answer));
  const answerNegative = Array.from(aw).some((w) => NEGATIVE.has(w));
  const fits = rows
    .map((r) => ({ r, content: words(r.label).filter((w) => !STOP.has(w)) }))
    .filter(({ content }) => content.length > 0 && content.every((w) => aw.has(w)))
    .filter(({ r }) => answerNegative || !words(r.label).some((w) => NEGATIVE.has(w)));
  if (!fits.length) return null;
  const best = Math.max(...fits.map((f) => f.content.length));
  const top = fits.filter((f) => f.content.length === best);
  return top.length === 1 ? (top[0] as { r: OracleRow }).r : null;
}

const part = (s: string, i: number): string => normalize(s.split(",")[i] ?? "");
/** The code an answer leads with, as the list keys it: a ZIP+4 by its ZIP. */
const leadCode = (s: string): string => (s.split(",")[0] ?? "").trim().replace(/^(\d{5})-\d{4}$/, "$1");
const countyName = (s: string): string => normalize(s).replace(/ (?:county|parish|borough)$/, "");

/**
 * The postal row an answer names, its parts "code, city, county, state": its exact label, else the one row opening
 * with the answer's code, else among those the one of the applicant's city (or the answer's own second part: "29492,
 * Charleston" beside "29492, Daniel Island"), then of the applicant's county ("29401, Charleston, Charleston, SC"
 * beside "29401, Charleston, Berkeley, SC"); a tie abstains.
 */
export function choosePostalRow(rows: OracleRow[], answer: string, place: AddressHints = {}): OracleRow | null {
  const exact = rows.filter((r) => normalize(r.label) === normalize(answer));
  if (exact.length) return exact.length === 1 ? (exact[0] as OracleRow) : null;
  const code = normalize(leadCode(answer));
  let same = code ? rows.filter((r) => part(r.label, 0) === code) : [];
  if (same.length <= 1) return same[0] ?? null;
  const city = normalize(place.city ?? "") || part(answer, 1);
  same = city ? same.filter((r) => part(r.label, 1) === city) : [];
  const county = countyName(place.county ?? "");
  if (same.length > 1 && county) same = same.filter((r) => countyName(part(r.label, 2)) === county);
  return same.length === 1 ? (same[0] as OracleRow) : null;
}

function seed(input: HTMLInputElement, text: string): void {
  focusEl(input);
  nativeValueSetter(input)?.(text);
  fireInput(input, text, "insertText");
}

/** Wait for rows while the list re-renders: the first poll after a seed can still show the previous result set. */
async function pollRows<T>(input: HTMLInputElement, ctx: WriteCtx, ms: number, pick: (rows: OracleRow[]) => T | null): Promise<T | null> {
  return waitFor(ctx, () => pick(oracleRows(input)), ms, ctx.timing.oracleRowPollMs);
}

/**
 * Reads the options of a closed select and leaves it as it was: a click
 * first, then a space seed for a tenant that lists nothing on click; the seed
 * is cleared, the list closed and the focus given back.
 */
export async function readOracleOptions(input: HTMLInputElement, ctx: WriteCtx): Promise<string[] | null> {
  if (input.value.trim() !== "") return null;
  const active = ctx.doc.activeElement as HTMLElement | null;
  if (pointerSequence(input) === "unsafe-click") return null;
  let rows = await pollRows(input, ctx, ctx.timing.prescanReadMs, (r) => (r.length ? r : null));
  if (!rows) {
    seed(input, " ");
    rows = await pollRows(input, ctx, ctx.timing.prescanReadMs, (r) => (r.length ? r : null));
  }
  const labels = rows ? Array.from(new Set(rows.map((r) => r.label))) : null;
  if (input.value !== "") clearText(input);
  press(input, "Escape");
  input.blur();
  active?.focus?.();
  return labels;
}

async function closeWithoutChoice(input: HTMLInputElement): Promise<void> {
  if (input.value !== "") clearText(input);
  press(input, "Escape");
  input.blur();
}

/**
 * Drives a select to the answer. The list is opened by a click and read; when
 * the wanted row is not offered the box is seeded with a space and then with
 * the answer's first letter (a tenant that filters on substring), and the
 * list is watched for a second after each seed because it re-renders late.
 * The commit is a hover and a click on the grid cell, verified against the
 * row's own text. A postal list is a server search: its code is typed, and its row chosen by code and then by the
 * applicant's city and county.
 */
export async function writeOracleSelect(input: HTMLInputElement, answer: string, ctx: WriteCtx, drive: OracleDrive = { typeFirst: false, exactOnly: false }, place: AddressHints = {}): Promise<WriteOutcome> {
  const lateMs = ctx.timing.oracleLateMs;
  if (pointerSequence(input) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  let row: OracleRow | null = null;
  // What the run itself put in the box, which never proves a commit.
  let typed = "";
  if (drive.postal) {
    typed = leadCode(answer);
    await typeText(input, typed, ctx, { gapMs: ctx.timing.typeGapMs });
    row = await pollRows(input, ctx, ctx.timing.oracleCountryWaitMs, (r) => choosePostalRow(r, answer, place));
  } else if (drive.typeFirst) {
    // The list filters on what is typed, one key at a time, and a slow tenant (or a server search) is waited for.
    // A country answered by its code is typed by its common name and chosen by any of its names: "US" lists no "United States".
    const names = countryNames(answer).length > 1 ? countryNames(answer) : [answer];
    typed = names[0] as string;
    await typeText(input, typed, ctx, { gapMs: ctx.timing.typeGapMs });
    const one = (r: OracleRow[], name: string) => (drive.exactOnly ? (r.find((x) => normalize(x.label) === normalize(name)) ?? null) : chooseOracleRow(r, name));
    const choose = (r: OracleRow[]) => names.reduce<OracleRow | null>((hit, name) => hit ?? one(r, name), null);
    row = await pollRows(input, ctx, ctx.timing.oracleCountryWaitMs, choose);
  } else {
    const choose = (r: OracleRow[]) => chooseOracleRow(r, answer) ?? (drive.degree ? degreeRow(r, answer) : null);
    await pollRows(input, ctx, ctx.timing.openWaitMs, (r) => (r.length ? r : null));
    row = choose(oracleRows(input));
    if (!row) {
      seed(input, " ");
      row = await pollRows(input, ctx, lateMs, choose);
    }
    if (!row) {
      const first = (collapse(answer)[0] ?? "").toUpperCase();
      if (first) {
        typed = first;
        seed(input, first);
        row = await pollRows(input, ctx, lateMs, choose);
      }
    }
  }
  if (!row) {
    await closeWithoutChoice(input);
    return { ok: false, reason: "option-not-found", abstain: true };
  }
  const picked = row;
  hover(picked.cell);
  if (pointerSequence(picked.cell) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  // The input's own value, not the matcher's wording, proves the commit. A
  // postal list's row reads "29401, Charleston, Charleston, SC" and the chosen
  // row shows only its leading "29401". A value that is only what the run
  // typed (the code, or the whole name of a typed-first list) proves nothing
  // by itself: then the list closing on the click is what proves it.
  const shows = (v: string) => (v === normalize(picked.label) || v === part(picked.label, 0)) && (v !== normalize(typed) || input.getAttribute("aria-expanded") !== "true");
  const landed = await waitFor(ctx, () => shows(normalize(input.value)), ctx.timing.commitWaitMs);
  if (!landed) {
    await closeWithoutChoice(input);
    return { ok: false, reason: "write-not-committed" };
  }
  // The grid closes a moment after the choice (Chase's postal list), and an
  // open select reads as holding nothing, so the write ends once it closed.
  await waitFor(ctx, () => input.getAttribute("aria-expanded") !== "true", ctx.timing.commitWaitMs);
  return { ok: true, optionLabel: picked.label };
}

// --- the address master -------------------------------------------------------

export interface AddressTarget {
  city?: string;
  state?: string;
  postalCode?: string;
}

/**
 * How well a suggestion fits the applicant's locality, or null. The segments
 * after the street are the locality; one must be the state, spelled out or
 * abbreviated, and nothing else ("Virginia" is not "West Virginia"), and one
 * the city: exactly, as a prefix ("Atlanta Metro Area"), or, for a city of
 * several words, as a run of whole words inside it ("Fort Worth" in "Dallas
 * Fort Worth Metroplex"; "York" is never "New York"). A city that appears only
 * in the street never counts. Higher is better: city tier first, then the
 * postal code (a ZIP+4 row carries its 5-digit prefix).
 */
export function addressScore(label: string, t: AddressTarget): number | null {
  const segs = label.split(",").map((s) => s.trim());
  if (segs.length < 2) return null;
  const locality = segs.slice(1);
  const code = (t.state ?? "").trim().toUpperCase();
  const stateNames = [normalize(code), normalize(STATE_NAMES[code] ?? t.state ?? "")].filter(Boolean);
  const stateOk = locality.some((s) => {
    const n = normalize(s.replace(/\d{5}(-\d{4})?|\([^)]*\)/g, " "));
    return stateNames.includes(n);
  });
  if (!stateOk) return null;
  const city = normalize(t.city ?? "");
  if (!city) return null;
  let tier = 0;
  for (const s of locality) {
    const n = normalize(s);
    if (n === city) tier = Math.max(tier, 3);
    else if (n.startsWith(`${city} `)) tier = Math.max(tier, 2);
    else if (city.includes(" ") && whole(n, city)) tier = Math.max(tier, 1);
  }
  if (!tier) return null;
  const zip = (t.postalCode ?? "").replace(/\D/g, "").slice(0, 5);
  const zipHit = zip.length === 5 && new RegExp(`\\b${zip}(-\\d{4})?\\b`).test(label);
  return tier * 10 + (zipHit ? 5 : 0);
}

/** The suggestion to commit: only rows with a comma and the typed street number, the best fit first, Oracle's order on a tie. */
export function chooseAddressRow(rows: OracleRow[], street: string, t: AddressTarget): OracleRow | null {
  const number = /^\s*(\d+)/.exec(street)?.[1];
  let best: { row: OracleRow; score: number } | null = null;
  for (const row of rows) {
    if (!row.label.includes(",") || (number && !new RegExp(`\\b${number}\\b`).test(row.label))) continue;
    const score = addressScore(row.label, t);
    if (score !== null && (!best || score > best.score)) best = { row, score };
  }
  return best?.row ?? null;
}

const CASCADE_FIELDS = ["city", "region2", "region1", "postalCode"];

/** What the cascade fields of the master's own block hold: those under its nearest ancestor that has any. */
function cascadeValues(input: Element): string[] {
  const fields = CASCADE_FIELDS.map((n) => `[name="${n}"]`).join(", ");
  for (let node = input.parentElement; node; node = node.parentElement) {
    const found = Array.from(node.querySelectorAll<HTMLInputElement>(fields));
    if (found.length) return found.map((el) => el.value.trim());
  }
  return [];
}

/**
 * Types the street one key at a time (the suggestions come from a debounced
 * request), watches the master's own list while it streams, commits the row
 * that holds the applicant's city and state on its grid cell, and waits for
 * the page's cascade: a cascade field that changed since before the street
 * was typed is the proof, never a value the block already held. No fitting
 * row leaves the box empty.
 */
export async function writeOracleAddress(input: HTMLInputElement, street: string, target: AddressTarget, ctx: WriteCtx): Promise<WriteOutcome> {
  // A master that already holds the street keeps the block: re-driving it would wipe the cascaded values.
  if (input.value.trim() !== "") return { ok: true, optionLabel: input.value };
  const before = cascadeValues(input).join("\n");
  await typeText(input, street, ctx, { gapMs: ctx.timing.typeGapMs });
  const row = await waitFor(ctx, () => chooseAddressRow(oracleRows(input), street, target), ctx.timing.oracleAddressWaitMs, ctx.timing.oraclePollMs);
  if (!row) {
    await closeWithoutChoice(input);
    return { ok: false, reason: "option-not-found", abstain: true };
  }
  hover(row.cell);
  if (pointerSequence(row.cell) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const landed = await waitFor(ctx, () => input.value.trim() !== "" && cascadeValues(input).some(Boolean) && cascadeValues(input).join("\n") !== before, ctx.timing.oracleCascadeWaitMs, ctx.timing.oraclePollMs);
  if (!landed) {
    fire(input, "change");
    return { ok: false, reason: "write-not-committed", detail: "stage=cascade" };
  }
  return { ok: true, optionLabel: row.label };
}
