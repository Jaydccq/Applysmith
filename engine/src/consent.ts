// Clean-room: docs/provenance.md#CR-ENGINE
// Which boxes the engine may tick: optional marketing opt-ins never, required
// legal acknowledgements only when the user's setting allows it, and a
// statement that the application is the applicant's own work never: only the
// user's own answer settles it.

import { normalize } from "./dom";

const MARKETING = [
  /\b(marketing|newsletters?|promotions?|promotional|mailing list|subscribe|subscribed|subscribing|subscription)\b/,
  /\b(special|exclusive|partner|partners) offers?\b/,
  /\bjob alerts?\b/,
  /\b(sms|text messages?|texts)\b/,
  /\btalent (community|pool|network)\b/,
  /\bportfolio compan(y|ies)\b/,
  /\b(contact|consider|notify|email|message|text|call|update|inform|reach out to) (me|us|you)\b.*\b(future|other|similar|new|upcoming|additional) (jobs?|roles?|openings?|positions?|vacanc\w+|opportunit\w+)\b/,
  /\bkeep (me|us|you) (informed|posted|updated|in the loop)\b/,
  /\bto be (contacted|notified|informed|emailed)\b.*\b(future|other|similar|new|upcoming|additional) (jobs?|roles?|openings?|positions?|vacanc\w+|opportunit\w+)\b/,
];

const DECLARATION = [
  /^(i|we) (agree|accept|acknowledge|certify|consent|confirm|understand|authori[sz]e|have read)\b/,
  /\b(agree|accept|acknowledg\w*|certify|consent|understand|read|authori[sz]e)\b.*\b(terms|privacy|polic(y|ies)|disclaimer|conditions of (use|employment|service))\b/,
  /^(the |our |this )?(terms( and)? conditions|terms of (use|service)|privacy (policy|notice|statement)|cookie policy)$/,
  // A notice that points at the privacy notice ("Please be informed that your application will trigger some processing of your personal data ... refer to the privacy notice") is acknowledged by ticking it.
  /\bprivacy (policy|notice|statement)\b/,
  /\b(disclaimer|consent|certify|certification|attest|acknowledg(e|es|ement|ements|ment|ments)|declaration|background checks?|at will|e signature|electronic signature)\b/,
  // "The information I have provided ... is complete and accurate" states the truth of what was given, like a certification.
  /\b(information|statements?|answers?|details|everything)\b.{0,80}\b(provided|submitted|given|made|stated|supplied|furnished)\b.{0,80}\b(is|are)\s+(true|complete|accurate|correct)\b/,
];

/** An option that declines, so choosing it never assents to anything. */
const NEGATIVE = /^(no|n|false|not|never|decline|disagree|dont|don t|opt out|unsubscribe)\b|^i (do not|dont|don t|disagree|decline|refuse|will not|won t|am not)\b|^do not\b/;

/** A sweep may choose an acknowledgement only from its own affirmative wording.
 * Read contractions before punctuation is discarded; the DOM already decoded
 * entities. A positive lead or an unnegated acknowledgement establishes scope.
 */
export function negativeAcknowledgement(label: string): boolean {
  const quotes = label.normalize("NFKC").replace(/[\u200b\u200c\u200d\u2060]/g, "").replace(/[`‘’ʼ′]/g, "'");
  const text = normalize(quotes.replace(/\b(?:[a-z]*n\s*'\s*t|cannot)\b/gi, (verb) => {
    const word = verb.replace(/\s/g, "");
    return word.toLowerCase() === "cannot" ? "can not" : `${word.slice(0, -3)} not`;
  }));
  if (/^(yes|agree|accept)\b|^i (have read and )?(agree|accept|consent|confirm|acknowledge|attest|certify|declare|affirm)\b/.test(text)) return false;
  // "The information I have provided ... is complete and accurate" opens by asserting the truth; a
  // later clause about the site ("takes no responsibility") refuses nothing.
  if (/^(the |all )?(information|statements?|answers?|details) (i|we) (have )?(provided|submitted|given|made|stated|supplied|furnished)\b.{0,80}\b(is|are) (true|complete|accurate|correct)\b/.test(text)) return false;
  // A notice the box acknowledges ("Please be informed that your application will trigger some processing of your
  // personal data ... SmartRecruiters, the data processor, has no control over it") negates only what others do.
  if (/^please (be informed|note)\b/.test(text)) return false;
  const words = text.split(" ");
  const refuses = (word: string) => /^(not|no|non|never|without|neither|nor|decline|refuse|reject|dont|doesnt|havent|haven|wont)$/.test(word);
  for (let i = 0; i < words.length; i++) {
    if (/^(accept|acknowledge|agree|confirm)$/.test(words[i] as string) && !words.slice(Math.max(0, i - 4), i).some(refuses)) return false;
  }
  return NEGATIVE.test(text) || words.some(refuses);
}

/** An option label that opts in, as opposed to one that merely names a topic. */
const OPT_IN = /\b(send|receive|contact|notify|inform|keep|subscribe|sign me up|opt in|i agree|i consent|i would like|i want|yes|add me|share|(email|text|call|message) me)\b/;

/** Work-authorisation and similar answers are not declarations, whatever their opening words. */
const OUT_OF_SCOPE = /\b(authori[sz]ed to work|authori[sz]ation to work|work authori[sz]ation|eligible to work|right to work|sponsorship|citizen(ship)?|veteran|disabilit(y|ies))\b/;

// The applicant's authorship of this application: its content is their own,
// written by them, or made with or without AI, even when the same sentence also
// certifies that it is true. The words must be said of the application's
// content, within one sentence, so they are read with the punctuation kept.
const OWN_END = String.raw`(?:\s*[,.;:!?)]|\s*$|\s+(?:and|or|but|including|without|with|in|that|which|to|for)\b)`;
const OWN = String.raw`(?:my|your)\s+own(?:\s+(?:work|words|writing))?` + OWN_END;
const CONTENT = String.raw`\b(?:application|answers?|responses?|submission|essays?|cover\s+letter|writing|everything)\b`;
const DEGREE = String.raw`(?:(?:all|entirely|wholly|completely|solely|personally|only)\s+)?`;
const AI = String.raw`(?:ai|artificial\s+intelligence|chat\s?gpt|generative(?:\s+ai)?|llms?|(?:large\s+)?language\s+models?)\b`;
const AUTHORSHIP = new RegExp(
  [
    String.raw`\b(?:true|accurate|correct|complete|honest|genuine)\b[^.?!;]{0,30}\band\s+` + DEGREE + OWN,
    CONTENT + String.raw`[^.?!;]{0,40}\b(?:is|are|was|were|has\s+been|have\s+been)\s+` + DEGREE + String.raw`(?:written\s+)?(?:in\s+)?` + OWN,
    String.raw`\b(?:is|are|was|were)\s+(?:everything|(?:this|the|these|your|all)\b[^.?!;]{0,20}` + CONTENT + String.raw`)[^.?!;]{0,60}\b` + OWN,
    String.raw`\b(?:is|are|was|were)\b[^.?!;]{0,40}\b(?:my|your)\s+own\s+(?:work|words|writing)` + OWN_END,
    CONTENT + String.raw`[^.?!;]{0,60}\b(?:written|composed|authored|drafted|prepared|completed)\s+` + DEGREE + String.raw`by\s+(?:me|you|myself|yourself|the\s+(?:applicant|candidate))\b`,
    CONTENT +
      String.raw`[^.?!;]{0,60}\b(?:without\s+(?:the\s+)?(?:use|help|aid|assistance|support)\s+of\s+(?:any\s+)?(?:an?\s+)?|without\s+(?:using\s+)?(?:any\s+)?|(?:not\s+)?(?:been\s+)?(?:generated|written|created|produced|composed|drafted|completed)\s+(?:by|with|using)\s+(?:an?\s+|any\s+)?)` +
      AI,
    CONTENT + String.raw`[^.?!;]{0,60}\b(?:ai|machine)[-\s]generated\b`,
  ].join("|"),
);

function authorship(text: string): boolean {
  return AUTHORSHIP.test(text.normalize("NFKD").replace(/\p{M}+/gu, "").toLowerCase());
}

export type ConsentKind = "marketing" | "declaration";

export function consentKind(text: string): ConsentKind | null {
  const t = normalize(text);
  if (!t) return null;
  if (MARKETING.some((re) => re.test(t))) return "marketing";
  if (OUT_OF_SCOPE.test(t)) return null;
  return DECLARATION.some((re) => re.test(t)) ? "declaration" : null;
}

/** Whether a lone box reads as something the user is asked to agree to. */
export function looksLikeConsent(text: string): boolean {
  return consentKind(text) === "declaration";
}

/**
 * The text of a choice-group option when choosing it would assent to
 * something, else null: a declining option, an option that only names a
 * topic ("Email newsletter" as a way of hearing about the job), or one that
 * is not consent at all.
 */
export function assentingOption(option: string): string | null {
  const t = normalize(option).replace(/^yes\b\s*/, "");
  if (!t || NEGATIVE.test(t)) return null;
  const kind = consentKind(t);
  if (!kind || (kind === "marketing" && !OPT_IN.test(t))) return null;
  return option;
}

export type ConsentRefusal = "marketing-opt-in" | "legal-declaration-off" | "context-policy";

export interface ConsentBox {
  text: string;
  /** The page itself marks the control required; the engine's default of required never counts here. */
  markedRequired: boolean;
  /** The label itself says "optional". */
  optional: boolean;
}

/**
 * Why a box must stay unticked, or null when the engine may tick it. A consent
 * box counts as required only when the page marks it; an optional one is an
 * opt-in whatever its wording, and a required one follows the user's
 * legal-declarations setting, marketing wording included. A statement that the
 * application is the applicant's own work is refused with either setting.
 */
export function consentRefusal(box: ConsentBox, acceptLegalDeclarations: boolean): ConsentRefusal | null {
  if (authorship(box.text)) return "context-policy";
  if (!consentKind(box.text)) return null;
  const optional = box.optional || /\boptional\b/.test(normalize(box.text));
  if (optional || !box.markedRequired) return "marketing-opt-in";
  return acceptLegalDeclarations ? null : "legal-declaration-off";
}
