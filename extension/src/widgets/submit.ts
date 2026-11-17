// Telling a submit control from the rest. The submit guard and the submission
// recorder share it, so they agree on what a click on "submit" is.
import { clickLabel } from "../../../engine/src/click-guard";

/** Every element a submit control can be. */
export const SUBMIT_CANDIDATES = "button, input[type=submit], input[type=image], input[type=button], [role=button], a[role=button]";
const SUBMIT_WORDS = /\bsubmit\b|send (my |your )?application|finish (my |your )?application|complete (my |your )?application/i;
/** The words of a step or an account action: a control that carries one never submits an application. */
const STEP_WORDS = /\b(search|sign ?in|log ?in|sign ?up|register|create (an )?account|subscribe|next|continue|back|previous|cancel|save|upload|add|edit|skip|start|remove|delete|withdraw|discard|attach|filters?|apply now)\b/i;
/** The two plain words that count on a control that does not say it submits. */
const PLAIN_SUBMIT = /^(apply|confirm)$/i;

/**
 * The control a click landed on, when it looks like the one that submits an
 * application. A control that says `type=submit` counts unless it carries a
 * step word; one that does not (a button with no type reads as a submit button
 * by default, and so does every helper button) counts only when its own words
 * say submit.
 */
export function submitControlOf(target: EventTarget | null): HTMLElement | null {
  if (!(target instanceof Element)) return null;
  const el = target.closest<HTMLElement>(SUBMIT_CANDIDATES);
  if (!el || (el as HTMLButtonElement).disabled) return null;
  const label = clickLabel(el);
  if (STEP_WORDS.test(label) && !SUBMIT_WORDS.test(label)) return null;
  const form = el.closest("form");
  if (form?.querySelector("input[type=password]")) return null;
  const type = (el.getAttribute("type") ?? "").toLowerCase();
  const explicit = (el.localName === "button" || el.localName === "input") && (type === "submit" || type === "image") && form !== null;
  return explicit || SUBMIT_WORDS.test(label) || PLAIN_SUBMIT.test(label) ? el : null;
}
