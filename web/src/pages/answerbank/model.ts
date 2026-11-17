import type { AnswerBankDerived, AnswerBankEntry, AnswerBankOverride, AnswerBankUnanswered, AnswerCandidate } from "../../wire";

// The reserved value that means "leave this field for me". The server defines
// it; the editor writes it only where the answer-bank spec accepts it.
export const ASK_ME = "@ask-me";

// askTo opens a new Ask Agent conversation with text in its composer, for the
// user to finish and send: the agent saves what the user says through the
// bank's own rules.
export const askTo = (text: string) => `/ask?draft=${encodeURIComponent(text)}`;

// ADD_PROMPT starts a message that adds answers to the bank.
export const ADD_PROMPT = "Add to my answer bank: ";

// suggested says the review leaves a waiting suggestion in the queue: not
// reviewed yet, or worth an entry. The others are listed apart, each with the
// review's reason.
export const suggested = (c: AnswerCandidate) => c.review.verdict === "" || c.review.verdict === "reusable";

// SiteOverride is one override of an entry with the host it applies on.
export interface SiteOverride {
  host: string;
  o: AnswerBankOverride;
}

const isAsk = (v: string | undefined) => (v ?? "").trim() === ASK_ME;

// holdsAskMe mirrors the server rule: the marker sits in the one position of
// the kind that takes an answer.
export function holdsAskMe(e: AnswerBankEntry): boolean {
  switch (e.kind) {
    case "fixed":
      return isAsk(e.answer);
    case "pick-option":
      return e.preferred?.length === 1 && isAsk(e.preferred[0]);
    case "compensation-range":
      return isAsk(e.default_answer);
    case "multi-select":
      return e.selected?.length === 1 && isAsk(e.selected[0]);
    default:
      return false;
  }
}

// canAskMe is true for the kinds whose answer can be the marker.
export const canAskMe = (kind: string) => kind === "fixed" || kind === "pick-option" || kind === "compensation-range" || kind === "multi-select";

export function withAskMe(e: AnswerBankEntry): AnswerBankEntry {
  switch (e.kind) {
    case "fixed":
      return { ...e, answer: ASK_ME };
    case "pick-option":
      return { ...e, preferred: [ASK_ME] };
    case "compensation-range":
      return { ...e, default_answer: ASK_ME };
    case "multi-select":
      return { ...e, selected: [ASK_ME] };
    default:
      return e;
  }
}

export const categoryOf = (e: AnswerBankEntry) => e.category || "other";

const blank = (s: string | undefined) => (s ?? "").trim() === "";
const anyText = (l: string[] | null | undefined) => (l ?? []).some((s) => !blank(s));

// bodyEmpty mirrors the server rule: an entry with no answer must be marked a
// placeholder, or the save is refused.
export function bodyEmpty(e: AnswerBankEntry): boolean {
  switch (e.kind) {
    case "fixed":
      return blank(e.answer);
    case "pick-option":
      return !anyText(e.preferred) && blank(e.fallback_text) && !e.fallback_any_option;
    case "compensation-range":
      return [e.minimum, e.maximum, e.default_answer, e.currency].every(blank);
    case "language-proficiency":
      return [e.language, e.speaking, e.listening, e.reading, e.writing].every(blank) && !anyText(e.proficiency_options);
    case "employer-history":
      return blank(e.answer_if_match) && blank(e.answer_if_no_match);
    case "multi-select":
      return e.selected === null || e.selected === undefined;
    default:
      return false;
  }
}

// slug makes an id from a question: lower case words joined by dashes.
export const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 60).replace(/-$/, "");

// sourceOf says where an entry came from: learned from the user's forms,
// a shipped default the user has not reviewed yet, or the user's own.
export function sourceOf(e: AnswerBankEntry): "learned" | "default" | "user" {
  if (e.created_by === "agent") return "learned";
  return e.needs_review ? "default" : "user";
}

export interface Needs {
  unanswered: Set<string>;
  // optional holds the blank links, which no number of the entries to answer
  // counts (see optionalLink).
  optional: Set<string>;
  blocking: Set<string>;
  // review holds the shipped defaults the user has not looked at yet.
  review: Set<string>;
  askMe: Set<string>;
  // derived holds the entries a setup step answers, by id.
  derived: Map<string, AnswerBankDerived>;
}

// optionalLink is a links entry the file leaves blank and no setup step
// answers. A form asks a link as an optional field, and a run never stops for
// one, so it is a link the user may add, not an answer anything waits on;
// one a waiting run's question reaches (asked) is that question.
const optionalLink = (e: AnswerBankEntry, derived: Map<string, AnswerBankDerived>, asked: Set<string>) =>
  categoryOf(e) === "links" && e.state !== "archived" && bodyEmpty(e) && !derived.get(e.id)?.answer && !asked.has(e.id);

export function needsOf(u: AnswerBankUnanswered, entries: AnswerBankEntry[]): Needs {
  const derived = new Map(u.derived.map((d) => [d.id, d]));
  const asked = new Set(u.asked.map((a) => a.entryId ?? ""));
  const optional = new Set(entries.filter((e) => optionalLink(e, derived, asked)).map((e) => e.id));
  return {
    unanswered: new Set(u.unanswered.map((g) => g.id).filter((id) => !optional.has(id))), optional, blocking: new Set(u.blocking.map((g) => g.id)), review: new Set(u.needsReview.map((g) => g.id)),
    askMe: new Set(u.askMe), derived,
  };
}

// SETUP_STEP names the setup steps a derived answer comes from.
export const SETUP_STEP: Record<string, string> = { identity: "Identity", cv: "CV", "work-authorization": "Work authorization", targets: "Targets" };

export const toLines = (list: string[] | null | undefined) => (list ?? []).join("\n");
export const fromLines = (text: string) =>
  text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

// previewOf is the stored answer in one line, as the list shows it under the
// question: empty when the entry stores nothing to show.
export function previewOf(e: AnswerBankEntry): string {
  if (holdsAskMe(e)) return "";
  const list = (l: string[] | null | undefined) => (l ?? []).join(", ");
  switch (e.kind) {
    case "fixed":
      return (e.answer ?? "").trim();
    case "pick-option":
      return list(e.preferred) || (e.fallback_text ?? "");
    case "multi-select":
      return e.selected?.length === 0 ? "None of them" : list(e.selected);
    case "compensation-range":
      return e.default_answer || [e.minimum, e.maximum].filter(Boolean).join("–") + (e.currency && (e.minimum || e.maximum) ? ` ${e.currency}` : "");
    case "employer-history":
      return [e.answer_if_match, e.answer_if_no_match].filter(Boolean).join(" / ");
    case "language-proficiency":
      return [e.language, e.speaking].filter(Boolean).join(": ");
    default:
      return "";
  }
}
