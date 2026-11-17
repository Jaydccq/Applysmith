// Clean-room: docs/provenance.md#CR-ENGINE
// Lever's location box: a plain text input with no ARIA markup that searches on
// key down, next to a hidden input the form actually submits. What the person
// sees is only a search; the row counts as filled when the hidden field holds
// the chosen place.

import { geoChooser } from "./choosers";
import { type WriteCtx, waitFor } from "./env";
import { pointerSequence, typeText } from "./gestures";
import { collapse, flatText } from "./dom";
import type { OptionRow } from "./popup";
import { isRendered } from "./visibility";
import type { WriteOutcome } from "./writers";

/** The hidden input that stores the selected place, in the box's form or container. */
function leverHidden(input: Element): HTMLInputElement | null {
  const scope = input.closest("form") ?? input.parentElement?.parentElement ?? input.parentElement;
  return scope?.querySelector<HTMLInputElement>('input[type="hidden"][name="selectedLocation"], input[type="hidden"][id="selectedLocation"]') ?? null;
}

/** Structural, not by host: a location text box that has the hidden sibling and no combobox markup. */
export function isLeverLocation(el: Element): boolean {
  if (el.localName !== "input" || (el.getAttribute("type") ?? "text") !== "text") return false;
  if (el.getAttribute("role") || el.getAttribute("aria-autocomplete") || el.getAttribute("aria-haspopup")) return false;
  return /location/i.test(`${el.getAttribute("name") ?? ""} ${el.getAttribute("id") ?? ""}`) && leverHidden(el) !== null;
}

/** The row holds the place only when the hidden field does. */
export const leverFilled = (input: Element): boolean => (leverHidden(input)?.value ?? "").trim() !== "";

function suggestions(doc: Document): OptionRow[] {
  return Array.from(doc.querySelectorAll<HTMLElement>(".dropdown-location"))
    .filter((e) => isRendered(e))
    .map((el) => ({ el, label: collapse(flatText(el)), value: "", selected: false, disabled: false }))
    .filter((r) => r.label !== "");
}

/** Lever's loading row, beside the box, shown while its place search runs. */
function searching(input: Element): boolean {
  const spinner = input.parentElement?.querySelector<HTMLElement>(".dropdown-loading-results");
  return !!spinner && isRendered(spinner);
}

/**
 * Types the city, one full prefix per key with a key down before each (the
 * search fires from the key down), picks a suggestion that holds the city and
 * as many of the other segments as possible, commits it with the mouse
 * sequence and waits for the hidden field. Lever's place search can outlast
 * the wait (Aledade: about 3 s after the last key): past it, the wait goes on
 * while the loading row shows, up to its cap.
 */
export async function writeLeverLocation(input: HTMLInputElement, value: string, ctx: WriteCtx): Promise<WriteOutcome> {
  const hidden = leverHidden(input) as HTMLInputElement;
  const city = (value.split(",")[0] as string).trim();
  await typeText(input, city, ctx, { gapMs: ctx.timing.leverKeyMs });
  const choose = geoChooser(value);
  const patience = Date.now() + ctx.timing.leverSuggestWaitMs;
  const settled = await waitFor(
    ctx,
    () => {
      const row = choose(suggestions(ctx.doc));
      if (row) return { row };
      return Date.now() >= patience && !searching(input) ? { row: null } : null;
    },
    ctx.timing.leverSearchCapMs,
    ctx.timing.pollMs,
  );
  const pick = settled?.row ?? null;
  if (!pick) return { ok: false, reason: "option-not-found", abstain: true };
  if (pointerSequence(pick.el) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const landed = await waitFor(ctx, () => hidden.value.trim() !== "", ctx.timing.commitWaitMs);
  return landed ? { ok: true, optionLabel: pick.label } : { ok: false, reason: "write-not-committed" };
}
