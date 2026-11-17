// Clean-room: docs/provenance.md#CR-ENGINE
// Google-style places autocomplete: it reads the legacy key codes, fills its
// dependent fields from its own handler on Enter, and breaks when a suggestion
// is clicked. So the address is typed, the first suggestion is committed with
// ArrowDown and Enter, and nothing is ever clicked.

import type { WriteCtx } from "./env";
import { waitFor } from "./env";
import { press, releaseFocus, safePress, typeText } from "./gestures";
import { isRendered } from "./visibility";
import type { WriteOutcome } from "./writers";

const CONTAINER = ".pac-container";

export function isPlacesInput(el: Element): boolean {
  return /location/i.test(el.getAttribute("placeholder") ?? "") || /\bpac-target-input\b/.test(el.getAttribute("class") ?? "") || el.hasAttribute("data-places");
}

function suggestions(doc: Document): Element[] {
  return Array.from(doc.querySelectorAll(`${CONTAINER} .pac-item`)).filter((e) => isRendered(e));
}

export async function writePlaces(input: HTMLInputElement, value: string, ctx: WriteCtx): Promise<WriteOutcome> {
  await releaseFocus(input, ctx);
  await typeText(input, value, ctx, { gapMs: ctx.timing.typeGapMs });
  const doc = ctx.doc;
  // Without the widget's container the typed value stands.
  if (!doc.querySelector(CONTAINER)) {
    await ctx.sleep(ctx.timing.settleMs);
    return input.value === value ? { ok: true, optionLabel: value } : { ok: false, reason: "write-not-committed" };
  }
  const items = await waitFor(ctx, () => (suggestions(doc).length ? suggestions(doc) : null), ctx.timing.suggestWaitMs);
  if (!items) return input.value === value ? { ok: true, optionLabel: value } : { ok: false, reason: "write-not-committed" };
  press(input, "ArrowDown");
  await ctx.sleep(ctx.timing.activateMs);
  if (safePress(input, "Enter") === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  await ctx.sleep(ctx.timing.commitWaitMs / 2);
  return input.value.trim() !== "" ? { ok: true, optionLabel: input.value } : { ok: false, reason: "write-not-committed" };
}
