// Clean-room: docs/provenance.md#CR-ENGINE
// Turning a matcher resolution (or a host's late answer) into a row.

import { applyValue } from "./apply";
import { normalize } from "./dom";
import { MAX_OPTIONS_PER_QUESTION, type Lane, type Resolution, type Row, type RowOutcome, type RowReason } from "./protocol";
import type { Run } from "./run";
import { isFreeText, type ControlInfo } from "./scan";

/**
 * Machine-populated answers a document parser may have guessed wrong, and work-authorization answers a site carried
 * over from its own records (ADP fills "proof of your legal right to work" in): asked again over what the page shows.
 */
export const REOPEN = /current (company|employer)|\bvisa\b|\blocation\b|\bcity\b|\bright to work\b|\bauthori[sz]ed to work\b|\bwork authori[sz]ation\b|\bsponsorship\b/i;

/** Matcher reasons a row carries as its own; every other abstain is a row "no-match". */
export const KNOWN_ABSTAIN: RowReason[] = ["section-policy", "context-policy", "duplicate-link", "self-id-policy", "ask-me"];
/** Abstains that mean the blank is intended; they never count as required-unfilled. */
export const DELIBERATE_BLANK: ReadonlySet<RowReason> = new Set<RowReason>(["context-policy", "self-id-policy", "duplicate-link", "section-policy", "terminal-abstain", "marketing-opt-in", "user-edited"]);

/**
 * Matcher reasons that mean the blank is intended (nothing to answer, a policy, a sensitive category); the rest need
 * the person. The names are the matcher's own (internal/answerbank/match), pinned by engine/test/matcher-reasons.json.
 */
export const DELIBERATE_MATCHER: ReadonlySet<string> = new Set(["placeholder-empty", "section-policy", "duplicate-link", "sensitive-category"]);

const CHOICE: ReadonlySet<string> = new Set(["select", "radio-group", "checkbox", "custom-listbox"]);

export function makeRow(
  info: ControlInfo,
  lane: Lane,
  outcome: RowOutcome,
  extra: { reason?: RowReason; value?: string; optionLabel?: string; review?: boolean; matcherReason?: string; detail?: string; options?: string[] } = {},
): Row {
  const row: Row = {
    selector: info.selector,
    label: info.label.text,
    section: info.section,
    lane,
    kind: info.kind,
    // The row says what the page demands; the matcher's question keeps the broader default.
    required: info.markedRequired,
    outcome,
    review: extra.review ?? false,
  };
  if (extra.reason) row.reason = extra.reason;
  if (extra.matcherReason) row.matcherReason = extra.matcherReason;
  if (info.kind === "file") {
    if (extra.value) row.fileName = extra.value;
  } else if (extra.value !== undefined && info.sensitivity !== "withhold") {
    row.value = extra.value;
  }
  if (extra.optionLabel && info.sensitivity !== "withhold") row.optionLabel = extra.optionLabel;
  // Only a site's own refusal reaches the user; other writer details are diagnostics for the log.
  if (extra.detail && (extra.reason === "format-not-accepted" || extra.reason === "out-of-range")) row.detail = extra.detail;
  // A choice left unanswered carries its options, so the check can still answer it; a lone box's only option is its label.
  const options = extra.options ?? info.options.map((o) => o.label);
  if (outcome !== "filled" && outcome !== "prefilled" && CHOICE.has(info.kind) && options.length > (info.kind === "checkbox" ? 1 : 0)) {
    row.options = options.slice(0, MAX_OPTIONS_PER_QUESTION);
  }
  return row;
}

function abstainRow(info: ControlInfo, res: Resolution): Row {
  if (res.terminal) return makeRow(info, "bank", "abstained", { reason: "terminal-abstain", matcherReason: res.reason });
  const reason = KNOWN_ABSTAIN.includes(res.reason as RowReason) ? (res.reason as RowReason) : "no-match";
  return makeRow(info, "bank", "abstained", { reason, matcherReason: res.reason });
}

/** A text or select abstain the matcher marked overwritable goes back to the profile pass. */
/** Abstains that keep their claim wherever the control is: the matcher decided, and no lane may second-guess it. */
const KEEPS_CLAIM: ReadonlySet<string> = new Set(["context-policy", "duplicate-link", "self-id-policy", "ask-me"]);

export function releasesClaim(info: ControlInfo, res: Resolution | undefined, inRow = false): boolean {
  // Inside a repeated row a policy abstain or a miss goes back to the profile pass, which writes only the row's own entry.
  if (inRow && (!res || (res.outcome === "abstained" && (res.reason === "section-policy" || (!res.terminal && !KEEPS_CLAIM.has(res.reason ?? "")))))) return true;
  if (!res) return info.kind === "text";
  return res.outcome === "abstained" && res.clientMayOverwrite === true && (info.kind === "text" || info.kind === "select");
}

/** A third party's box: an employer, a reference, an emergency contact. The applicant's own details do not belong in it. */
const THIRD_PARTY = /\b(employer|reference|emergency|supervisor|manager|previous|former|spouse|relative|friend)\b/i;
/** A box that names an employer itself ("Employer Name", "Previous Employer", "What is your current or most recent employer?", Stripe's "Who is your current or previous employer?", Squarespace's "What is the name of your current or most recent employer?") asks for one of the applicant's own employers. */
const EMPLOYER_WHEN = "(?:previous|former|current|most recent)";
const EMPLOYER_NAME = new RegExp(`^(?:(?:what|who)(?: is|'s)\\s+(?:the\\s+name\\s+of\\s+)?your\\s+)?(?:${EMPLOYER_WHEN}(?:\\s+or\\s+${EMPLOYER_WHEN})?\\s+)?(?:employer|company)(?:\\s+name)?[\\s?*]*$`, "i");

function ownEmployer(run: Run, text: string): boolean {
  return (run.applicant?.history?.employment ?? []).some((e) => typeof e.employer === "string" && e.employer.trim() !== "" && normalize(e.employer) === normalize(text));
}

function ownDetail(run: Run, text: string): boolean {
  const c = run.applicant?.candidate;
  if (!c) return false;
  // The applicant's own name, or the name of one of their employers or schools, is no referee and no emergency contact either.
  const history = [...(run.applicant?.history?.employment ?? []), ...(run.applicant?.history?.education ?? [])];
  const names = [c.fullName, [c.firstName, c.lastName].filter(Boolean).join(" "), ...history.flatMap((e) => [e.employer, e.school, e.institution])];
  if (names.some((n) => typeof n === "string" && n.trim() !== "" && normalize(n) === normalize(text))) return true;
  const digits = (s: string) => s.replace(/\D/g, "").replace(/^1(?=\d{10}$)/, "");
  const same = (a?: string) => !!a && a.trim() !== "" && normalize(a) === normalize(text);
  return (!!c.phone && digits(c.phone).length >= 7 && digits(c.phone) === digits(text)) || same(c.email) || same(c.address?.line1);
}

/** Questions whose answer a model must never compose: a criminal-history explanation, or the "If yes, ..." detail slot of a yes/no question. */
const UNSAFE_TO_COMPOSE = /\b(convict\w*|felon\w*|misdemeanou?r\w*|criminal|offen[cs]e\w*|arrest\w*)\b|\bif (yes|you answered yes)\b/i;

export function isUnsafeToCompose(text: string): boolean {
  return UNSAFE_TO_COMPOSE.test(text);
}

/** The degree of the applicant's education entries whose field of study is this answer, when they name one: a catalog may file the subject under its faculty. */
function degreeOfMajor(run: Run, major: string): string | undefined {
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  const degrees = new Set(
    (run.applicant?.history?.education ?? [])
      .filter((e) => str(e.field) !== "" && normalize(str(e.field)) === normalize(major))
      .map((e) => str(e.degree))
      .filter(Boolean),
  );
  return degrees.size === 1 ? [...degrees][0] : undefined;
}

export async function answerControl(run: Run, info: ControlInfo, res: Resolution, written: Map<string, string>, deadline?: number): Promise<Row> {
  if (res.outcome === "abstained") return abstainRow(info, res);
  const text = (res.value ?? res.optionLabel ?? "").trim();
  // The matcher answers from the applicant's identity; a box that asks about someone else gets nothing of it.
  if (THIRD_PARTY.test(info.label.text) && ownDetail(run, text) && !(EMPLOYER_NAME.test(info.label.text.trim()) && ownEmployer(run, text))) {
    return makeRow(info, "bank", "abstained", { reason: "section-policy" });
  }
  const labels = res.optionLabels?.filter((l) => l.trim()) ?? [];
  if ((!text && !labels.length) || /\bNO_ANSWER\b/.test(text)) return makeRow(info, "bank", "abstained", { reason: "no-answer" });

  // A model never composes an answer for these, not even as a draft.
  if (res.outcome === "filled-model" && isUnsafeToCompose(info.label.text)) return makeRow(info, "bank", "abstained", { reason: "no-answer", matcherReason: "unsafe-to-compose" });

  // A composed draft waits for the user (or the apply chain's grounding checks).
  if (res.outcome === "filled-model" && isFreeText(info) && res.reviewed !== true) {
    run.drafts.push({ selector: info.selector, label: info.label.text, text });
    return makeRow(info, "bank", "skipped", { reason: "awaiting-review" });
  }
  const review = res.outcome === "filled-review" || res.outcome === "filled-model";
  const r = await applyValue(
    info,
    {
      value: text || (labels[0] as string),
      optionLabel: res.optionLabel,
      optionLabels: labels.length ? labels : undefined,
      alternatives: res.alternatives,
      // A question a document parser tends to get wrong is answered over what the page shows, on every host.
      // The user's own words replace whatever the box holds.
      replaceDefault: res.outcome === "filled-review" || REOPEN.test(info.label.text) || res.replace === true,
      multiSelectBinding: res.multiSelectBinding,
      // A model draft is never the user's own answer, whatever the host says.
      ownAnswer: res.ownAnswer === true && res.outcome !== "filled-model",
      degree: degreeOfMajor(run, text),
      overwrite: res.overwrite === true,
    },
    { ctx: run.ctx(), written, mode: "replace", acceptLegalDeclarations: run.acceptLegalDeclarations, edits: run.edits, deadline },
  );
  if (r.outcome === "failed" && r.detail?.includes("stage=no-options")) run.unsatisfied.set(info.selector, { ...res, selector: info.selector });
  return makeRow(info, "bank", r.outcome, {
    reason: r.reason,
    value: r.value,
    optionLabel: r.optionLabel,
    review: review && r.outcome === "filled",
    detail: r.detail,
    options: r.options,
  });
}
