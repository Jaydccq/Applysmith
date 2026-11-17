// Clean-room: docs/provenance.md#CR-ENGINE
// The label rules every press is checked against. Pure text with no DOM, so the
// main-world helper carries the same rules as the engine core.

// Lookalike letters that have no compatibility decomposition, mapped to the
// Latin letter they imitate. Escapes keep the source ASCII.
const CONFUSABLES = new Map<string, string>(
  Object.entries({
    "\u0131": "i", "\u017f": "s",
    "\u0430": "a", "\u0432": "b", "\u0435": "e", "\u043a": "k", "\u043c": "m", "\u043d": "h",
    "\u043e": "o", "\u0440": "p", "\u0441": "c", "\u0442": "t", "\u0443": "y", "\u0445": "x",
    "\u0455": "s", "\u0456": "i", "\u0458": "j", "\u04bb": "h", "\u0501": "d", "\u051b": "q", "\u051d": "w",
    "\u03b1": "a", "\u03b2": "b", "\u03b5": "e", "\u03b9": "i", "\u03ba": "k", "\u03bd": "v",
    "\u03bf": "o", "\u03c1": "p", "\u03c4": "t", "\u03c5": "u", "\u03c7": "x",
    "\u03b7": "n", "\u03bc": "u", "\u03c9": "w", "\u03b3": "y", "\u0475": "v", "\u0251": "a", "\u0261": "g",
    "\u0561": "a", "\u0566": "q", "\u0570": "h", "\u0578": "n", "\u057d": "u", "\u0581": "g", "\u0585": "o",
    "\u13aa": "a", "\u13f4": "b", "\u13df": "c", "\u13a0": "d", "\u13ac": "e", "\u13bb": "h", "\u13c6": "i",
    "\u13ab": "j", "\u13e6": "k", "\u13de": "l", "\u13b7": "m", "\u13e2": "p", "\u13da": "s", "\u13a2": "t",
    "\u13d9": "v", "\u13b3": "w", "\u13c3": "z",
    "\u2019": "'", "\u2018": "'", "\u02bc": "'",
  }),
);

const DROPPED = /^[\p{Mn}\p{Cf}]$/u;
const SPACE = /^[\s\p{Cc}]$/u;

/**
 * The form every label is read in, the same as the Go gates read it: compatibility
 * and width forms unified, accents and format characters dropped, lookalike
 * letters mapped to Latin, lowercased, white space collapsed.
 */
export function foldLabel(s: string): string {
  let out = "";
  for (const ch of s.normalize("NFKD")) {
    if (DROPPED.test(ch)) continue;
    if (SPACE.test(ch)) {
      out += " ";
      continue;
    }
    let mapped = CONFUSABLES.get(ch);
    if (mapped === undefined) mapped = CONFUSABLES.get(ch.toLowerCase()) ?? ch.toLowerCase();
    out += mapped;
  }
  return out.split(/\s+/).filter(Boolean).join(" ");
}

const SCRIPTS = [/\p{Script=Latin}/u, /\p{Script=Cyrillic}/u, /\p{Script=Greek}/u, /\p{Script=Armenian}/u, /\p{Script=Cherokee}/u];

/** Whether one word spells its letters from two or more alphabets; no honest label does, so the guard refuses it rather than trust the fold. */
export function hasMixedScriptWord(s: string): boolean {
  let first = -1;
  for (const ch of s.normalize("NFKD")) {
    if (DROPPED.test(ch)) continue;
    if (!/^\p{L}$/u.test(ch)) {
      first = -1;
      continue;
    }
    let sc = SCRIPTS.findIndex((re) => re.test(ch));
    if (sc < 0) sc = SCRIPTS.length;
    if (first < 0) first = sc;
    else if (sc !== first) return true;
  }
  return false;
}

const UNSAFE_WORDS = /\b(submit|withdraw|discard|delete)/;
const TERMINAL_APPLY = /\bapply\s+(and\s+save|&\s*save|without\s+saving)\b/;
// Pages are advanced and sessions started by the apply chain's trusted input, never by the engine.
const ADVANCE = /\b(next(?!\s+(month|year|week|decade)\b)|continue|review|apply|save|register|sign[\s-]?in|log[\s-]?in|sign[\s-]?up|create (an |your )?account)\b/;

/** Submit, withdraw, discard, delete or a terminal apply phrase: what nobody presses, the apply chain and the Easy Apply walker included. */
export function isNeverLabel(label: string): boolean {
  if (hasMixedScriptWord(label)) return true;
  const f = foldLabel(label);
  return UNSAFE_WORDS.test(f) || TERMINAL_APPLY.test(f);
}

/** What the engine itself never presses: the never-press words, and every control that advances a page, signs in or saves. */
export function isUnsafeLabel(label: string): boolean {
  return isNeverLabel(label) || ADVANCE.test(foldLabel(label));
}

const UNSAFE_WHOLE_WORDS = /\b(submit|withdraw|discard|delete)\b/;
// A box names an apply step only by opening with it ("Apply", "Apply with LinkedIn"); "jobs I apply to" is a sentence.
const TOGGLE_ADVANCE = /\b(next(?!\s+(month|year|week|decade)\b)|continue|review|save|register|sign[\s-]?in|log[\s-]?in|sign[\s-]?up|create (an |your )?account)\b|^apply\b/;

/**
 * The rule for a native checkbox or radio, whose activation only flips it: the
 * same words, read whole. Its label is a sentence, and "By submitting this
 * application, I agree", "the information submitted is true" or "the resume
 * you submit" (HPE) is a declaration; a box that says submit, delete or
 * continue, or opens with Apply, is still refused.
 */
export function isUnsafeToggleLabel(label: string): boolean {
  if (hasMixedScriptWord(label)) return true;
  const f = foldLabel(label);
  return UNSAFE_WHOLE_WORDS.test(f.replace(SPOKEN_OF, " ")) || TERMINAL_APPLY.test(f) || TOGGLE_ADVANCE.test(f);
}

// A never-press word a person does in a sentence ("the information you submit will be kept confidential"): no
// control is named with a subject before its verb.
const SPOKEN_OF = /\b(you|we|they|i)\s+(?:(?:may|will|must|can|should|might|do|did|choose to|wish to|decide to)\s+)?(submit|withdraw|discard|delete)\b/g;

/**
 * The rule for the trigger of a list: its label is the question it asks
 * ("... candidates daily apply their knowledge ... Are you a veteran?",
 * Workday), and opening a list advances nothing, so only the never-press
 * words, read whole, are refused there, and not one a person does in a
 * sentence of the question (Cadence's VEVRAA notice: "The information you
 * submit will be kept confidential").
 */
export function isUnsafeListTriggerLabel(label: string): boolean {
  if (hasMixedScriptWord(label)) return true;
  const f = foldLabel(label);
  return UNSAFE_WHOLE_WORDS.test(f.replace(SPOKEN_OF, " ")) || TERMINAL_APPLY.test(f);
}

/**
 * The rule for an option of an open list: choosing one sets the answer and
 * advances nothing, so, as for the list's trigger, only the never-press words,
 * read whole, are refused. Roblox's only option is "I acknowledge that I
 * received and had the opportunity to review Roblox's Candidate Arbitration
 * Agreement ..."; an option naming Submit or Withdraw is still never chosen,
 * in a sentence too ("I withdraw my application").
 */
export function isUnsafeOptionLabel(label: string): boolean {
  if (hasMixedScriptWord(label)) return true;
  const f = foldLabel(label);
  return UNSAFE_WHOLE_WORDS.test(f) || TERMINAL_APPLY.test(f);
}

const REMOVAL_UNSAFE =/\b(submit|withdraw|discard)/;
const NAMES_APPLICATION = /\bapplication\b/;

/** Whether a row-removal control's label also names the application or carries another refused word. */
export function removalLabelRefused(label: string): boolean {
  if (hasMixedScriptWord(label)) return true;
  const f = foldLabel(label);
  return REMOVAL_UNSAFE.test(f) || TERMINAL_APPLY.test(f) || NAMES_APPLICATION.test(f);
}
