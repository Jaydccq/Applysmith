// Clean-room: docs/provenance.md#CR-ENGINE
// The sweep over acknowledgement boxes: which required legal boxes the run
// ticks (only with the user's setting on), which it finds already ticked, and
// which it never touches. An optional opt-in is never ticked, whatever else its
// sentence says; marketing wording on a page-required box follows the setting.

import { makeRow } from "./answers";
import { consentKind, consentRefusal, negativeAcknowledgement } from "./consent";
import { walkElements } from "./dom";
import { optionLabel } from "./labels";
import { tap } from "./gestures";
import type { Run } from "./run";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import { writeToggles, toggleLabel } from "./toggles";
import { writeChoice } from "./writers";

/** "Resume needed and I have one": an acknowledgement that is true only when a document goes along. */
const DOCUMENT_CONDITIONAL = /\b(resume|cv|curriculum vitae)\b.*\b(attach\w*|upload\w*|have|needed|required)\b|\bi have (a|my) (resume|cv)\b/i;

function loneBox(info: ControlInfo): boolean {
  return info.kind === "checkbox" && (info.toggles.length ? info.toggles.length === 1 : info.members.length === 1);
}

function ownLabel(info: ControlInfo): string {
  return info.toggles.length ? toggleLabel(info.toggles[0] as HTMLElement) || optionLabel(info.toggles[0] as HTMLElement) : optionLabel(info.members[0] as HTMLInputElement);
}

/** The sentence the page authored for the box, as the person reads it. */
export function authoredLabel(info: ControlInfo): string {
  const own = ownLabel(info);
  return own && !info.label.text.includes(own) ? `${info.label.text} ${own}`.trim() : info.label.text || own;
}

/**
 * Ticks the required acknowledgement boxes the user's setting allows, reports
 * the ones it found already ticked as inherited (also with the setting off),
 * and taps each box it ticked. Native boxes are found inside shadow roots;
 * Radix-style boxes are driven through their role button.
 */
function hasBox(doc: Document): boolean {
  let found = false;
  walkElements(doc.body ?? doc, (el) => {
    if (!found && (el.matches("input[type=checkbox], [role=checkbox], [role=switch]"))) found = true;
  });
  return found;
}

export async function consentSweep(run: Run, written: Map<string, string>): Promise<void> {
  // A full scan is the expensive part; most pages have no acknowledgement box at all.
  if (!hasBox(run.doc)) return;
  const accept = run.acceptLegalDeclarations;
  const ctx = run.ctx();
  for (const info of scanControls(run.doc)) {
    if (!loneBox(info)) continue;
    const prior = run.rows.get(info.selector);
    if (prior && (prior.outcome === "filled" || prior.outcome === "prefilled")) continue;
    const sentence = authoredLabel(info);
    const conditional = DOCUMENT_CONDITIONAL.test(sentence);
    const kind = consentKind(sentence);
    if (!conditional && kind === null) continue;
    if (info.answered) {
      if (!prior && (conditional || kind === "declaration")) run.setRow(makeRow(info, "profile", "prefilled", { reason: "existing-value", value: readInfo(info), optionLabel: sentence }));
      continue;
    }
    if (run.edits.edited(info)) continue;
    // An affirmative legend cannot turn the box's own refusal into assent.
    if (negativeAcknowledgement(ownLabel(info) || sentence)) continue;
    // The consent rule runs first for every wording: an optional opt-in is never ticked, whatever else the sentence says.
    const refusal = consentRefusal({ text: sentence, markedRequired: info.markedRequired, optional: info.label.optional }, accept);
    // With the setting off a required declaration no lane settled is the person's to tick, and its row says so.
    if (refusal === "legal-declaration-off" && !conditional && (!prior || prior.outcome === "abstained")) run.setRow(makeRow(info, "profile", "skipped", { reason: refusal }));
    if (refusal || !accept) continue;
    if (conditional && (!run.hasDocument || !info.required || info.label.optional)) continue;
    await run.checkpoint();
    // The user may have taken the box while the run was parked at the checkpoint.
    if (run.edits.edited(info)) continue;
    const out = info.toggles.length ? await writeToggles(info.toggles, ["Yes"], ctx) : await writeChoice(info.members, { labels: ["Yes"], value: "Yes", multi: false }, ctx);
    if (!out.ok) {
      run.setRow(makeRow(info, "attestation", "failed", { reason: out.reason }));
      continue;
    }
    written.set(info.selector, readInfo(info));
    run.setRow(makeRow(info, "attestation", "filled", { value: readInfo(info), optionLabel: sentence }));
    await tap((info.toggles[0] ?? info.members[0]) as HTMLElement, ctx);
  }
}
