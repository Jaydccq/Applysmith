// Clean-room: docs/provenance.md#CR-ENGINE
// Controls whose values must never be written or leave the page.

export type Sensitivity = "never" | "withhold" | null;

const NEVER_AUTOCOMPLETE = /^(cc-|new-password|current-password|one-time-code)/;
const NEVER_WORDING = [
  /\b(ssn|social security|national (id|identifier|insurance)|tax id|taxpayer)\b/,
  /\b(routing number|account number|iban|bank account|sort code|swift (code|bic|number))\b|\bbic\b/,
  /\b(one time|verification code|passcode|security code|sms code|otp|2fa)\b/,
  // A green card holder is a lawful permanent resident, not a payment card.
  /\b(card number|credit card|debit card|cvv|cvc)\b|(?<!\bgreen )\bcard ?holder\b/,
];
// A passport is the document; a citizenship question that names one only to
// say which countries count ("the country(ies) for which you are a citizen ...
// (detain a passport for)", HP) asks for countries, unless it also asks for
// the document's number, dates or issue.
const PASSPORT = /\bpassport\b/;
const PASSPORT_CITIZENSHIP = /\b(citizen|citizens|citizenship|nationality|nationalities)\b/;
const PASSPORT_DOCUMENT = /\b(number|no|num|id|identification|expiry|expiration|expires|expire|issue|issued|issuing|date|valid|validity)\b/;
// A driver's licence is the document too, in any spelling ("Driver License Number", "Driving licence number");
// a question about holding one ("Do you have a valid driver's license?") asks for a yes or no, unless it also
// asks for the licence's number, expiry or issue.
const LICENCE = /\bdriv(?:er|ers|er s|ing) licen[cs]e\b/;
const LICENCE_HELD = /\b(do|did|have|are|will|would|can|could) you\b|\bi (have|hold|possess)\b|\brequire(s|d|ment|ments)?\b|\bvalid\b/;
const LICENCE_DOCUMENT = /\blicen[cs]e (?:\w+ )?(number|num|no|id|identification|expir\w+|issu\w+)\b/;
const CAPTCHA = /g recaptcha response|h captcha response|cf turnstile|captcha/;
const WITHHOLD_WORDING = [
  /\b(date of birth|dob|birth date|birthday|birthdate)\b/,
  /\b(gender|sex|race|ethnicity|ethnic|hispanic|latino|veteran|disability|disabled|sexual orientation|religion|pronouns?|transgender)\b/,
];

function wording(el: Element, label: string): string {
  const parts = [label, el.getAttribute("name"), el.getAttribute("id"), el.getAttribute("placeholder")];
  return parts
    .filter(Boolean)
    .join(" ")
    .normalize("NFKD")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, " ");
}

type SensitivityRule = "type-password" | "autocomplete-sensitive" | "sensitive-label-or-name";

/** Which detector fired, and how strictly the control is held back. */
export function sensitivityRule(el: Element, label: string): { rule: SensitivityRule; level: "never" | "withhold" } | null {
  const type = (el.getAttribute("type") ?? "").toLowerCase();
  if (type === "password") return { rule: "type-password", level: "never" };
  const ac = (el.getAttribute("autocomplete") ?? "").toLowerCase().trim();
  if (NEVER_AUTOCOMPLETE.test(ac)) return { rule: "autocomplete-sensitive", level: "never" };
  if (ac === "bday" || ac.startsWith("bday-")) return { rule: "autocomplete-sensitive", level: "withhold" };
  const w = wording(el, label);
  if (CAPTCHA.test(w) || el.closest(".g-recaptcha, .h-captcha, .cf-turnstile")) return { rule: "sensitive-label-or-name", level: "never" };
  if (NEVER_WORDING.some((re) => re.test(w))) return { rule: "sensitive-label-or-name", level: "never" };
  if (PASSPORT.test(w) && (!PASSPORT_CITIZENSHIP.test(w) || PASSPORT_DOCUMENT.test(w))) return { rule: "sensitive-label-or-name", level: "never" };
  if (LICENCE.test(w) && (!LICENCE_HELD.test(w) || LICENCE_DOCUMENT.test(w))) return { rule: "sensitive-label-or-name", level: "never" };
  if (WITHHOLD_WORDING.some((re) => re.test(w))) return { rule: "sensitive-label-or-name", level: "withhold" };
  return null;
}

/**
 * "never": no read, no write, no row. "withhold": the control may be answered
 * but its value never appears in a row or an event.
 */
export function classifySensitivity(el: Element, label: string): Sensitivity {
  return sensitivityRule(el, label)?.level ?? null;
}
