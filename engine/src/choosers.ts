// Clean-room: docs/provenance.md#CR-ENGINE
// Which option of an open popup answers the question. A chooser returns the
// one option to activate, or null when nothing may be committed: a popup full
// of near misses is a reason to leave the control empty, not to guess.

import { normalize, whole } from "./dom";
import type { OptionRow } from "./popup";
import { STATE_NAMES } from "./states";
import { countryNames, countryOfAlias, countryPart, scoreOption } from "./writers";

export type Chooser = (rows: OptionRow[]) => OptionRow | null;

function pickBest(scored: { row: OptionRow; score: number }[]): OptionRow | null {
  if (!scored.length) return null;
  const top = Math.max(...scored.map((s) => s.score));
  const best = scored.filter((s) => s.score === top).sort((a, b) => a.row.label.length - b.row.label.length);
  return (best[0] as { row: OptionRow }).row;
}

/**
 * Options that clear the score threshold, best score first and the shorter
 * label on a tie ("United States of America" over "United States Minor
 * Outlying Islands"). Strict mode accepts an exact normalized label only.
 */
export function scoreChooser(answers: string[], strict = false): Chooser {
  return (rows) => {
    for (const answer of answers) {
      const scored = rows
        .filter((r) => !r.disabled)
        .map((row) => ({ row, score: scoreOption(row.label, answer) }))
        .filter((s) => (strict ? s.score === 3 : s.score >= 2));
      const hit = pickBest(scored);
      if (hit) return hit;
    }
    return null;
  };
}

/**
 * A country a list spells another way ("United States of America" for
 * "United States", Scopely's Greenhouse list) is the row that is exactly
 * another of its names; a near miss ("United States Minor Outlying Islands")
 * is none.
 */
export function countryChooser(answer: string, strict: boolean): Chooser {
  const plain = scoreChooser([answer], strict);
  const others = scoreChooser(countryNames(answer).filter((n) => n !== normalize(answer)), true);
  return (rows) => plain(rows) ?? others(rows);
}

const COUNTRIES = new Set(
  [
    "united states",
    "united kingdom",
    "united arab emirates",
    "canada",
    "mexico",
    "india",
    "germany",
    "france",
    "spain",
    "italy",
    "ireland",
    "australia",
    "new zealand",
    "japan",
    "china",
    "brazil",
    "singapore",
    "netherlands",
    "sweden",
    "switzerland",
    "poland",
    "israel",
  ].map(normalize),
);

/** Expands an abbreviation only when it is a whole comma segment: a country's other name to its common name, a state code to the state. */
function expandSegment(segment: string): string {
  const s = segment.trim();
  const country = countryOfAlias(s);
  if (country) return country.replace(/\b\w/g, (c) => c.toUpperCase());
  if (/^[A-Za-z]{2}$/.test(s) && STATE_NAMES[s.toUpperCase()]) return STATE_NAMES[s.toUpperCase()] as string;
  return s;
}

/**
 * A place suggestion for a target like "Durham, NC, United States". An exact
 * full-string match wins. Otherwise the city must appear; a single city hit is
 * taken, a bare city with several hits is not, qualifier tokens rank the rest
 * and no qualifier match commits nothing. When the target names a region (a
 * state or province between the city and the country), a row that places the
 * city somewhere is taken only when it names that region: sharing the country
 * is not enough, since the country holds many places of that name. A
 * country-only target commits only the bare row that is the country, because
 * the first row containing a country is some stranger's city. With no row for
 * the city, a bare row that is the target's country is taken: a menu of
 * countries asks for no city. The country row may spell it another way
 * ("United States of America" for "United States", GitLab's "country of
 * residence" list).
 */
function countryRow(rows: OptionRow[], country: string): OptionRow | null {
  if (!COUNTRIES.has(country)) return null;
  const names = countryNames(country);
  return rows.find((r) => !r.label.includes(",") && names.includes(normalize(expandSegment(r.label)))) ?? null;
}

/**
 * A menu of states or provinces holds no city (Optiver's Greenhouse "US -
 * South Carolina"): the one row that names the target's region and nothing
 * besides its country is taken; a row that names a city, or a second row for
 * the region (Georgia the state and the country), commits nothing.
 */
function regionRow(rows: OptionRow[], expanded: string[], regions: string[]): OptionRow | null {
  if (regions.length !== 1) return null;
  const region = regions[0] as string;
  const country = expanded.length >= 2 ? countryNames(expanded[expanded.length - 1] as string) : [];
  const hits = rows.filter((r) => {
    const t = normalize(r.label.split(",").map(expandSegment).join(" "));
    if (!whole(t, region)) return false;
    let rest = ` ${t} `.replace(` ${region} `, " ");
    for (const n of country) rest = rest.replace(` ${n} `, " ");
    return rest.trim() === "";
  });
  return hits.length === 1 ? (hits[0] as OptionRow) : null;
}

export function geoChooser(target: string): Chooser {
  const segments = target.split(",").map((s) => s.trim()).filter(Boolean);
  const expanded = segments.map(expandSegment);
  // Suggestions abbreviate too ("Durham, NC, USA"): both sides expand whole segments only.
  const text = (r: OptionRow) => normalize(r.label.split(",").map(expandSegment).join(" "));
  return (rows) => {
    const usable = rows.filter((r) => !r.disabled);
    const fulls = new Set([normalize(target), normalize(expanded.join(" "))]);
    const exact = usable.find((r) => fulls.has(normalize(r.label)) || fulls.has(text(r)));
    if (exact) return exact;
    if (!segments.length) return null;
    if (segments.length === 1 && COUNTRIES.has(normalize(expanded[0] as string))) return countryRow(usable, normalize(expanded[0] as string));
    const city = normalize(expanded[0] as string);
    const regions = (segments.length >= 3 ? expanded.slice(1, -1) : segments.length === 2 && !COUNTRIES.has(normalize(expanded[1] as string)) ? expanded.slice(1) : []).map(normalize);
    const hits = usable.filter((r) => whole(text(r), city) && (!r.label.includes(",") || regions.every((g) => whole(text(r), g))));
    if (!hits.length) return regionRow(usable, expanded, regions) ?? (expanded.length >= 2 ? countryRow(usable, normalize(expanded[expanded.length - 1] as string)) : null);
    const qualifiers = expanded
      .slice(1)
      .flatMap((s) => normalize(s).split(" "))
      .filter((t) => t.length >= 2);
    if (hits.length === 1) return hits[0] as OptionRow;
    if (!qualifiers.length) return null;
    const scored = hits.map((row) => ({ row, score: qualifiers.filter((q) => whole(text(row), q)).length })).filter((s) => s.score > 0);
    return pickBest(scored);
  };
}

export interface AddressHints {
  city?: string;
  state?: string;
  postalCode?: string;
  /** Tells apart the rows of a postal code that spans two counties. */
  county?: string;
}

/**
 * A street suggestion is committed only when its text holds the applicant's
 * city and state, preferring the one that also holds the postal code. Decoys
 * in other cities and states commit nothing.
 */
export function addressChooser(hints: AddressHints): Chooser {
  const city = normalize(hints.city ?? "");
  const stateCode = normalize(hints.state ?? "");
  const stateName = normalize(STATE_NAMES[(hints.state ?? "").toUpperCase()] ?? hints.state ?? "");
  const postal = normalize(hints.postalCode ?? "");
  return (rows) => {
    if (!city || !stateCode) return null;
    const hits = rows.filter((r) => {
      const l = normalize(r.label);
      return !r.disabled && whole(l, city) && (whole(l, stateCode) || whole(l, stateName));
    });
    if (!hits.length) return null;
    return hits.find((r) => postal !== "" && whole(normalize(r.label), postal)) ?? (hits[0] as OptionRow);
  };
}

/**
 * A phone-country option must hold a plus sign and digits and name the
 * country: a country part that equals the country scores 2, one that merely
 * contains it scores 1, and an exact match stops the search.
 */
export function phoneCountryChooser(country: string): Chooser {
  const names = countryNames(country);
  return (rows) => {
    let best: { row: OptionRow; score: number } | null = null;
    for (const row of rows) {
      if (row.disabled || !/\+\d/.test(row.label)) continue;
      const part = countryPart(row.label);
      const score = names.includes(part) ? 2 : names.some((n) => whole(part, n)) ? 1 : 0;
      if (score === 0) continue;
      if (score === 2) return row;
      if (!best || score > best.score) best = { row, score };
    }
    return best?.row ?? null;
  };
}

/** Whether a displayed value already equals the answer: equal, or a whole-word phrase of 3+ letters either way. */
export function displayMatches(display: string, answer: string): boolean {
  const d = normalize(display);
  const a = normalize(answer);
  if (!d || !a) return false;
  if (d === a) return true;
  return (a.length >= 3 && whole(d, a)) || (d.length >= 3 && whole(a, d));
}

/** Search seeds, most specific first: the answer, then each shorter run of its comma segments. */
export function progressiveSeeds(answer: string): string[] {
  const parts = answer.split(",").map((s) => s.trim()).filter(Boolean);
  const out: string[] = [];
  for (let n = parts.length; n >= 1; n--) out.push(parts.slice(0, n).join(", "));
  return out.length ? out : [answer];
}

const LEVEL = /\b(bachelor|master|doctor|phd|associate|diploma|certificate|high school|mba)/i;

/** The level word a degree answer holds, as the answer writes it ("Bachelor" in "Bachelor of Science"): what a degree list is searched for. */
export function levelWord(answer: string): string | undefined {
  return LEVEL.exec(answer)?.[0];
}

/** The degree level a text names; "" for none. */
export function degreeLevel(text: string): string {
  const m = /\b(bachelor|master|doctor|ph ?d|associate|high school)/.exec(normalize(text))?.[0] ?? "";
  return m.startsWith("ph") ? "doctor" : m;
}

const LEVEL_ONLY_WORD = /^(?:bachelors?|masters?|doctors?|doctorate|associates?|s|degree|or|to|equivalent|first|second|yrs?|years?|\d+)$/;

/**
 * A row that names a degree level and no field ("Bachelors", "Masters (+2 or
 * +3 yrs)", Visa's "Master's or Equivalent Second-Degree (ISCED 7: MA, MSc,
 * MEng, ...)"); what it lists in parentheses is not a field of its own.
 */
function levelOnly(label: string): boolean {
  return normalize(label.replace(/\([^)]*\)?/g, " ")).split(" ").filter(Boolean).every((w) => LEVEL_ONLY_WORD.test(w));
}

const DEGREE_TYPE_WORD = /^(?:arts?|sciences?|business|administration|technology|engineering|philosophy|fine|laws?)$/;
const CONNECTOR_WORD = /^(?:of|or|and|in|the)$/;
const stem = (w: string) => w.replace(/s$/, "");

/** The degree types a text names, read before any "in <field>": "science" for "Master of Science (M.S)". */
function degreeTypes(text: string): string[] {
  const head = text.replace(/\([^)]*\)?/g, " ").split(/\bin\b/i)[0] ?? "";
  return normalize(head).split(" ").filter((w) => DEGREE_TYPE_WORD.test(w)).map(stem);
}

/** A row that names a level and degree types and no field (KLA's "Masters of Arts or Science or Business Administration (Masters)"). */
function typeOnly(label: string): boolean {
  return normalize(label.replace(/\([^)]*\)?/g, " "))
    .split(" ")
    .filter(Boolean)
    .every((w) => LEVEL_ONLY_WORD.test(w) || DEGREE_TYPE_WORD.test(w) || CONNECTOR_WORD.test(w));
}

/**
 * A degree the catalog spells differently is the one option of its level ("Master of Science (M.S)" is "Masters (+2 or +3 yrs)"),
 * or, among several of its level, the one that names the level alone (Ensemble's "Bachelors" beside "Bachelor of Science Nursing"),
 * the first such row with no gloss when the list spells the level twice (ASM's "Masters" and "Master's Degree"),
 * else the one that names only degree types, the answer's among them (KLA's "Masters of Arts or Science or Business
 * Administration (Masters)" beside "Masters of Technology (Masters)"); any other tie picks nothing. A list that names no level
 * in words takes the answer's abbreviation (The Clearing House's Workday lists "BS", "MS", "MA": "Master of Science (M.S)" is "MS").
 */
export function degreeChooser(answer: string): Chooser {
  const plain = scoreChooser([answer]);
  const level = degreeLevel(answer);
  const abbrevs = degreeAbbrevs(answer, level);
  return (rows) => {
    const hit = plain(rows);
    if (hit || !level) return hit;
    const same = rows.filter((r) => !r.disabled && degreeLevel(r.label) === level);
    if (!same.length) {
      const short = rows.filter((r) => !r.disabled && abbrevs.includes(r.label.replace(/[.\s]/g, "").toLowerCase()));
      return short.length === 1 ? (short[0] ?? null) : null;
    }
    if (same.length === 1) return same[0] ?? null;
    const bare = same.filter((r) => levelOnly(r.label));
    if (bare.length === 1) return bare[0] ?? null;
    const plainBare = bare.filter((r) => !/[()]/.test(r.label));
    if (plainBare.length) return plainBare[0] ?? null;
    const want = degreeTypes(answer);
    if (!want.length) return null;
    const typed = same.filter((r) => typeOnly(r.label) && want.every((w) => degreeTypes(r.label).includes(w)));
    return typed.length === 1 ? (typed[0] ?? null) : null;
  };
}

/**
 * The abbreviations a degree answer stands for: what it writes in parentheses ("M.S" is "ms"), and a bachelor's, master's or
 * associate's of arts or science by its initials ("Bachelor of Science" is "bs").
 */
function degreeAbbrevs(answer: string, level: string): string[] {
  const out: string[] = [];
  for (const m of answer.matchAll(/\(([^)]*)\)/g)) {
    const a = (m[1] ?? "").replace(/[.\s]/g, "").toLowerCase();
    if (/^[a-z]{2,4}$/.test(a)) out.push(a);
  }
  const types = degreeTypes(answer).filter((t) => t === "art" || t === "science");
  if (/^(?:bachelor|master|associate)$/.test(level) && types.length === 1) out.push(level.charAt(0) + (types[0] as string).charAt(0));
  return out;
}

export type SeedKind = "degree" | "study" | "location" | "phone-country" | "plain";

export function seedKind(label: string): SeedKind {
  const l = normalize(label);
  if (/\b(degree|education level|level of education|highest education)\b/.test(l)) return "degree";
  if (/\b(discipline|major|field of study|area of study)\b/.test(l)) return "study";
  if (/\b(phone|dial|calling)\b/.test(l) && /\bcountry\b|\bcode\b/.test(l)) return "phone-country";
  if (/\b(location|city|town|based|located|reside|residence|hometown)\b/.test(l)) return "location";
  return "plain";
}

/**
 * What to type into a search box: a degree's level word, a location's city
 * token, a phone country's name, a field of study and then its shorter word
 * endings ("Applied Mathematics", then "Mathematics", which ASM's Greenhouse
 * list holds); otherwise progressively shorter runs of the answer.
 */
export function searchSeeds(kind: SeedKind, answer: string): string[] {
  if (kind === "study") {
    const words = answer.trim().split(/\s+/).filter(Boolean);
    const out = [answer.trim()];
    for (let i = 1; i < words.length; i++) if (!/^(?:and|or|of|in|the|&)$/i.test(words[i] as string)) out.push(words.slice(i).join(" "));
    return out;
  }
  if (kind === "degree") {
    const level = levelWord(answer);
    return [level ? level.replace(/^./, (c) => c.toUpperCase()) : (answer.split(/\s+/)[0] as string)];
  }
  if (kind === "location") {
    // A menu of countries finds no city (Runway's Ashby location field), nor does a menu of regions (n8n's lists "South Carolina,
    // United States" and no city): the one region, then the country, follows the city, as geoChooser prefers them.
    const parts = answer.split(",").map((p) => p.trim()).filter(Boolean);
    const last = parts.length >= 2 ? (parts[parts.length - 1] as string) : "";
    const country = COUNTRIES.has(normalize(expandSegment(last))) ? last : "";
    const regions = parts.slice(1, country ? -1 : undefined);
    return [parts[0] ?? answer.trim(), ...(regions.length === 1 ? [expandSegment(regions[0] as string)] : []), ...(country ? [country] : [])];
  }
  if (kind === "phone-country") return [countryPart(answer).replace(/\b\w/g, (c) => c.toUpperCase())];
  return progressiveSeeds(answer);
}
