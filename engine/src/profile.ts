// Clean-room: docs/provenance.md#CR-ENGINE
// Profile-field pass: identity and contact data from the applicant snapshot,
// bound to a control by what the control says it is.

import { normalize } from "./dom";
import { compositeNamePart } from "./labels";
import { itiRoot } from "./libraries";
import { findPhoneCountryPicker } from "./phone-country";
import { isOracleCountry, isOraclePostalLead } from "./oracle";
import type { ApplicantSnapshot } from "./protocol";
import { isParserHost, rowControlSelectors } from "./row-plan";
import { askedField, readInfo } from "./scan";
import { STATE_CODES, STATE_NAMES } from "./states";
import type { ControlInfo } from "./scan";

export type ProfileKey =
  | "fullName"
  | "firstName"
  | "lastName"
  | "email"
  | "confirmEmail"
  | "phone"
  | "linkedin"
  | "github"
  | "portfolio"
  | "addressLine1"
  | "addressLine2"
  | "city"
  | "county"
  | "state"
  | "postalCode"
  | "country"
  | "signatureDate"
  | "signatureName"
  | "preferredLastName";

interface Rule {
  key: ProfileKey;
  label: RegExp;
  /** Autocomplete tokens that bind regardless of the label. */
  ac?: string[];
}

const NOT_NAME = /\b(middle|preferred|nick|maiden|former|previous|employer|company|reference|referee|referrer|referral|emergency|supervisor|manager)\b/;

const RULES: Rule[] = [
  { key: "confirmEmail", label: /\b(confirm|re ?enter|retype|verify|repeat)\b.*\be ?mail\b/ },
  {
    key: "fullName",
    label: /^((your|legal|candidate|applicant) )?(full|legal)( legal)? name$|^(first and last|first last|first name and last name) name$|^((full|legal) )?name first and last$|^your name$/,
    ac: ["name"],
  },
  // Another person's box (a supervisor, a reference, an emergency contact, whoever referred the applicant) is never the applicant's own.
  { key: "firstName", label: /^(?!.*\b(middle|preferred|nick|maiden|former|previous|employer|reference|referee|referrer|referral|emergency|supervisor|manager)\b)(.*\b)?(first|given|forename)( legal)? name$|^legal first name$/, ac: ["given-name"] },
  { key: "lastName", label: /^(?!.*\b(middle|preferred|nick|maiden|former|previous|employer|reference|referee|referrer|referral|emergency|supervisor|manager)\b)(.*\b)?(last|family|sur)( legal)? ?name$|^legal last name$/, ac: ["family-name"] },
  // A preferred last name with none recorded is the applicant's own (owner rule: name variants take the normal name).
  // Abnormal Security asks for it as a request naming both words: "Please enter your preferred Last Name/Surname".
  { key: "preferredLastName", label: /^(please (enter|provide|type) )?(your )?preferred (last|family|sur) ?name( (or )?surname)?$/ },
  { key: "email", label: /^(?!.*\b(contact me|notif|subscribe|alert|updates?|employer|reference|referee|referrer|referral|emergency|supervisor|manager)\b)(.*\b)?e ?mail( address)?$/, ac: ["email"] },
  { key: "phone", label: /^(?!.*\b(employer|reference|referee|referrer|referral|emergency|supervisor|manager|extension|ext|device|type|code)\b)(.*\b)?(phone|mobile|cell|telephone)( number)?$/, ac: ["tel", "tel-national"] },
  // Firecrawl's "Linkedln" types the capital I as a lower-case l.
  { key: "linkedin", label: /linked[il]n/ },
  { key: "github", label: /github/ },
  { key: "portfolio", label: /^(?!.*\b(company|employer)\b)(.*\b)?(portfolio|personal (web ?)?site|website|homepage|personal url)( url| link)?$/ },
  { key: "addressLine2", label: /^(address( line)? (2|two)|apt|apartment|suite|unit)\b/, ac: ["address-line2"] },
  { key: "addressLine1", label: /^((street|home|mailing|current|residential) )?address( line)?( 1| one)?$|^street( address)?$/, ac: ["address-line1", "street-address"] },
  { key: "city", label: /^(city|town|city (or )?town)$/, ac: ["address-level2"] },
  { key: "county", label: /^county$/ },
  { key: "state", label: /^(state|province|region|state (or )?province|state (or )?territory)$/, ac: ["address-level1"] },
  { key: "postalCode", label: /^(zip|zip code|postal|postal code|postcode|zip postal code|zip or postal code)$/, ac: ["postal-code"] },
  { key: "country", label: /^country( region)?( of residence)?$/, ac: ["country", "country-name"] },
  { key: "signatureDate", label: /\b(signature|signed)\b.*\bdate\b|\bdate\b.*\b(signed|signature)\b|^today s date$/ },
];

const CC305 = /voluntary self.?identification of disability|\bcc.?305\b/;
const SELF_ID = /\b(voluntary self identification|self identification|self identify|equal opportunity|equal employment|eeo)\b/;

/**
 * The heading of the nearest of eight ancestors that holds one: its last heading before the control, or none when they all come after it.
 * When that last one is a sub-heading (h5), the last section heading before it is read too: Lever heads its disability form with an h4 and
 * then asks "How do you know if you have a disability?" in an h5 above the signature block.
 */
function nearestHeading(el: Element): string {
  for (let n = el.parentElement, hop = 0; n && hop < 8; n = n.parentElement, hop++) {
    const heads = Array.from(n.querySelectorAll("h1, h2, h3, h4, h5, legend, [role=heading]")).filter((h) => (h.textContent ?? "").trim());
    if (!heads.length) continue;
    const before = heads.filter((h) => h.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING && !(h.localName === "legend" && !h.parentElement?.contains(el)));
    const last = before[before.length - 1];
    const section = last?.localName === "h5" ? before.filter((h) => h.localName !== "h5").pop() : undefined;
    return [section, last].map((h) => (h?.textContent ?? "").trim().slice(0, 120)).filter(Boolean).join(" ");
  }
  return "";
}

const OWN_PHONE = /\b(home|mobile|cell|primary|personal|daytime|evening)\b/;
const OTHER_PHONE = /\b(emergency|reference|work)\b/;
const PHONE_SCOPE = /\b(phone|mobile|telephone)\b/;

/** A country box that picks a dial code sits in a phone group or lists codes: it is not the address country. */
function isDialCodePicker(info: ControlInfo): boolean {
  if (PHONE_SCOPE.test(normalize(info.section.join(" ")))) return true;
  const codes = info.options.filter((o) => /\+\d/.test(o.label)).length;
  return info.kind === "select" && codes >= 3 && codes * 2 >= info.options.length;
}

const ADDRESS_KEYS = new Set<ProfileKey>(["addressLine1", "addressLine2", "city", "county", "state", "postalCode", "country", "phone"]);
const ROW_SECTION = /education|employment|work history|work experience|experience/i;
const NO_PROFILE_SECTION = /reference|emergency/i;

/** A ZIP add-on box takes four digits; the profile's five-digit code is refused by it. */
const ZIP_ADD_ON = /\+\s*4\b|\b(zip|postal)\s*(code\s*)?(4|plus\s*4|extension|ext|add ?on|suffix)\b|longpostalcode|zip4|plus4/i;

const FORMAT_HINT = /\([^()]*\b(?:ex|e\.g|eg|example|format|enter)\b[^()]*\)/gi;

/** Which profile field a control asks for, or null. A narrative or question-shaped label asks for none. */
export function recognise(info: ControlInfo): ProfileKey | null {
  const addressBox = info.kind === "custom-listbox" && info.owner === "profile";
  // A tenant's own question may draw a profile field as a multi-line box (DRW's "Legal First Name"); the scan gives it to this pass.
  if (info.kind !== "text" && info.kind !== "textarea" && info.kind !== "select" && !addressBox) return null;
  const section = info.section.join(" ");
  if (NO_PROFILE_SECTION.test(section)) return null;
  // A format hint in brackets (ADP: "Phone (Enter with dashes. Ex: 555-555-0100)") is no part of what the box asks for.
  const asked = askedField(info.label.direct);
  const direct = normalize(asked.replace(FORMAT_HINT, " "));
  const ac = (info.el.getAttribute("autocomplete") ?? "").toLowerCase().trim().split(/\s+/).pop() ?? "";
  if (direct.length > 80 || asked.includes("?")) return null;
  if (ZIP_ADD_ON.test(`${info.label.direct} ${info.el.getAttribute("id") ?? ""} ${info.el.getAttribute("name") ?? ""}`)) return null;
  // Ashby's own applicant-name field is the full name under its bare "Name"; its résumé parser guesses into it ("JordanAvery").
  // A tenant that relabels it with a part of the name asks for that part (Arsenals: "First Name", beside a "Last Name" box of its own);
  // the full-name rule reads first, so "First and last name" (n8n) stays the whole name.
  if (info.kind === "text" && (info.el.id === "_systemfield_name" || info.el.getAttribute("name") === "_systemfield_name")) {
    return RULES.find((r) => (r.key === "fullName" || r.key === "firstName" || r.key === "lastName") && r.label.test(direct))?.key ?? "fullName";
  }
  // SuccessFactors signs its application declaration in a bare "Name" box whose own name says it is the consent's (applicantConsentName).
  if (info.kind === "text" && direct === "name" && /consent|signature/i.test(`${info.el.getAttribute("name") ?? ""} ${info.el.id}`)) return "signatureName";
  // The disability form's signature block asks for a bare Name and Date; anywhere else those words name nothing. Workday heads the form
  // with an h3 before its fields rather than around them, so the nearest heading counts as well as the section.
  if (info.kind === "text" && (direct === "name" || direct === "date") && CC305.test(normalize(`${section} ${nearestHeading(info.el)}`))) {
    if (direct === "name") return "signatureName";
    if (direct === "date") return "signatureDate";
  }
  // The only date a self-identification or equal-opportunity form asks for is the day it is signed (Jobvite's block is plain divs under a heading).
  if (direct === "date" && info.kind === "text" && SELF_ID.test(normalize(`${section} ${nearestHeading(info.el)}`))) return "signatureDate";
  // Composite sub-labels: First and Last bind their parts, and Middle binds nothing.
  const part = info.kind === "text" ? compositeNamePart(info.el, info.label.direct) : null;
  if (part) return part === "first" ? "firstName" : part === "last" ? "lastName" : null;
  for (const rule of RULES) {
    // Oracle's address block is the master's to fill, but its country is the profile's, and so is a Postal Code that leads a block with no master.
    if (addressBox && rule.key !== "addressLine1" && !(rule.key === "country" && isOracleCountry(info.el)) && !(rule.key === "postalCode" && isOraclePostalLead(info.el))) continue;
    if (ROW_SECTION.test(section) && ADDRESS_KEYS.has(rule.key)) continue;
    if (rule.key === "country" && isDialCodePicker(info)) continue;
    if (direct) {
      if (rule.label.test(direct)) return rule.key;
      if (rule.ac?.includes(ac) && !NOT_NAME.test(direct)) return rule.key;
    } else {
      const hint = normalize(`${info.el.getAttribute("placeholder") ?? ""} ${info.el.getAttribute("name") ?? ""} ${info.el.getAttribute("id") ?? ""}`);
      if (rule.ac?.includes(ac) || (hint && rule.label.test(hint))) return rule.key;
    }
  }
  return null;
}

const PATH: Record<ProfileKey, string> = {
  fullName: "fullName",
  firstName: "firstName",
  lastName: "lastName",
  email: "email",
  confirmEmail: "email",
  phone: "phone",
  linkedin: "links.linkedin",
  github: "links.github",
  portfolio: "links.portfolio",
  addressLine1: "address.line1",
  addressLine2: "address.line2",
  city: "address.city",
  county: "address.county",
  state: "address.state",
  postalCode: "address.postalCode",
  country: "address.country",
  signatureDate: "",
  signatureName: "fullName",
  preferredLastName: "lastName",
};

function snapshotValue(key: ProfileKey, a: ApplicantSnapshot): string | undefined {
  const c = a.candidate;
  const addr = c.address ?? {};
  const links = a.links ?? {};
  const full = c.fullName ?? [c.firstName, c.middleName, c.lastName].filter(Boolean).join(" ");
  const map: Partial<Record<ProfileKey, string | undefined>> = {
    fullName: full || undefined,
    signatureName: full || undefined,
    firstName: c.firstName,
    lastName: c.lastName,
    preferredLastName: c.lastName,
    email: c.email,
    confirmEmail: c.email,
    phone: c.phone,
    linkedin: links.linkedin,
    github: links.github,
    portfolio: links.portfolio,
    addressLine1: addr.line1,
    addressLine2: addr.line2,
    city: addr.city,
    county: addr.county,
    state: addr.state,
    postalCode: addr.postalCode,
    country: addr.country,
  };
  const v = map[key];
  return v && v.trim() ? v.trim() : undefined;
}

function isAskMe(key: ProfileKey, a: ApplicantSnapshot): boolean {
  const path = PATH[key];
  return path !== "" && (a.askMe ?? []).includes(path);
}

type RowKey = `row:${string}`;

export interface ProfileItem {
  info: ControlInfo;
  key: ProfileKey | RowKey;
  /** The page's own parser guessed this control: the applicant's value replaces what it shows. */
  overwrite?: boolean;
  /** "write" when there is a value to put in, otherwise why there is not. */
  action: "write" | "prefilled" | "ask-me" | "no-data";
  value?: string;
  /** A field of study's row degree, which names the faculty a catalog may file the subject under. */
  degree?: string;
}

/**
 * Bind unclaimed controls to profile keys, in document order. One key claims
 * one control, a full-name claim is released when separate first and last name
 * controls exist, and a committed value is never overwritten.
 */
/** The page's value and the profile's are the same: equal, the same digits of a phone, or a state by code and by name. */
export function agrees(key: ProfileKey, shown: string, value: string): boolean {
  const a = normalize(shown);
  const b = normalize(value);
  if (a === b) return true;
  if (key === "phone") return shown.replace(/\D/g, "").replace(/^1(?=\d{10}$)/, "") === value.replace(/\D/g, "").replace(/^1(?=\d{10}$)/, "");
  if (key === "state") {
    const code = (s: string) => (s.length === 2 ? s.toUpperCase() : (STATE_CODES[s.toLowerCase()] ?? s));
    return code(shown.trim()) === code(value.trim()) || normalize(STATE_NAMES[value.toUpperCase()] ?? value) === a;
  }
  return false;
}

export function planProfile(infos: ControlInfo[], applicant: ApplicantSnapshot, claimed: Set<string>, site: string | null = null): ProfileItem[] {
  // A control in a repeated row is answered from its row's entry; the home profile never reaches it.
  const inRows = rowControlSelectors(infos, infos[0]?.el.ownerDocument ?? document);
  const bound: { info: ControlInfo; key: ProfileKey }[] = [];
  for (const info of infos) {
    if (claimed.has(info.selector) || inRows.has(info.selector)) continue;
    const key = recognise(info);
    if (key) bound.push({ info, key });
  }
  // A personal step may ask for the applicant's phone twice (mobile and home), both required and labelled only "Phone Number".
  const doubled = (info: ControlInfo): boolean => info.el.getAttribute("type") === "tel" && info.required && normalize(info.label.direct) === "phone number";
  // A further phone box takes the same number when its own question names the applicant's phone (home, mobile, cell, primary, personal, daytime, evening) and neither it nor the first phone box is someone else's.
  const question = (info: ControlInfo): string => normalize(`${info.label.direct} ${info.section.join(" ")}`);
  let firstPhone = "";
  const ownSecondPhone = (info: ControlInfo): boolean => OWN_PHONE.test(question(info)) && !OTHER_PHONE.test(question(info)) && !OTHER_PHONE.test(firstPhone);
  const hasParts = bound.some((b) => b.key === "firstName" || b.key === "lastName");
  // A form that asks the name and then the legal name (Anduril's Greenhouse "First Name", then "Legal First Name") is asked the applicant's own name twice.
  const legalPart = (info: ControlInfo): boolean => /^legal (first|last) name$/.test(normalize(askedField(info.label.direct)));
  // Mattermost asks "What is your legal name?" after its First and Last Name: like the legal parts, the applicant's whole name again,
  // where a plain "Full name" box beside the parts takes nothing.
  // Khan Academy names the parts after it: "Legal Name (First and Last)".
  const legalWhole = (info: ControlInfo): boolean => /^(full )?legal name( first and last)?$/.test(normalize(askedField(info.label.direct)));
  const taken = new Set<ProfileKey>();
  const items: ProfileItem[] = [];
  for (const { info, key } of bound) {
    if (key === "fullName" && hasParts && !legalWhole(info)) continue;
    // Every signature control is filled (Lever's disability form signs with a Name of its own below the applicant's Full name, and a
    // form that asks the name in parts still has the form signed in full); any other key claims one control.
    if (key !== "confirmEmail" && key !== "signatureDate" && key !== "signatureName" && key !== "preferredLastName" && taken.has(key) && !(key === "phone" && (doubled(info) || ownSecondPhone(info))) && !legalPart(info)) continue;
    if (key === "phone" && !taken.has(key)) firstPhone = question(info);
    taken.add(key);
    if (isAskMe(key, applicant)) {
      items.push({ info, key, action: "ask-me" });
      continue;
    }
    if (key === "signatureDate") {
      items.push({ info, key, action: info.answered ? "prefilled" : "write", value: "" });
      continue;
    }
    const value = snapshotValue(key, applicant);
    if (value === undefined) items.push({ info, key, action: "no-data" });
    else if (info.answered) {
      // Where the page's own parser guesses into these boxes, the applicant's value wins over a different guess.
      const shown = readInfo(info);
      // A parser that put the whole international number into the national box of a split phone gets it back in the national shape.
      const splitPhone = key === "phone" && shown.trim().startsWith("+") && (itiRoot(info.el) !== null || (info.el.localName === "input" && findPhoneCountryPicker(info.el as HTMLInputElement) !== null));
      if (isParserHost(site) && shown !== "" && (!agrees(key, shown, value) || splitPhone)) items.push({ info, key, action: "write", value, overwrite: true });
      else items.push({ info, key, action: "prefilled", value });
    } else items.push({ info, key, action: "write", value });
  }
  return items;
}
