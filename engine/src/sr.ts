// Clean-room: docs/provenance.md#CR-ENGINE
// SmartRecruiters' web components ignore events, so a value goes in through the
// component's own method, called from the page's world by the helper.

import { composedAncestors, collapse, normalize } from "./dom";
import { type WriteCtx } from "./env";
import { selectorFor } from "./selector";
import { isoDate } from "./dates";
import type { WriteOutcome } from "./writers";
import { waitFor } from "./env";
import { geoChooser } from "./choosers";
import type { SrSearchState } from "./mainworld/components";

/** Components whose value is set through handleChange. */
const PLAIN = new Set(["spl-input", "spl-textarea", "spl-phone-field", "spl-datepicker"]);

/** The plain-field component a control lives in, or null. */
export function srHost(el: Element): Element | null {
  for (const a of composedAncestors(el)) if (PLAIN.has(a.localName)) return a;
  return null;
}

export const isSrComponent = (el: Element): boolean => (el.localName === "input" || el.localName === "textarea") && srHost(el) !== null;

/** A history card's editor also holds a title or company search box and month fields, set the same way. */
const CARD = new Set([...PLAIN, "spl-autocomplete", "spl-date-field"]);
function cardHost(el: Element): Element | null {
  for (const a of composedAncestors(el)) if (CARD.has(a.localName)) return a;
  return null;
}
export const isSrCardComponent = (el: Element): boolean => (el.localName === "input" || el.localName === "textarea") && cardHost(el) !== null;
/** A card's "From" or "To": a month and a year are its whole value. */
export const isSrMonthField = (el: Element): boolean => cardHost(el)?.getAttribute("type") === "month-year";

/** "Office location" spawns a required country field and blocks Save, so it is never touched. */
export const isOfficeLocation = (label: string): boolean => normalize(label) === "office location";

/** A screening or self-identification select: an spl-autocomplete with fixed choices (a place or school box searches). */
const CHOICE_FIELDS = new Set(["sr-question-field-select", "sr-question-field-eeo"]);
export function srChoiceBox(el: Element): Element | null {
  let box: Element | null = null;
  for (const a of composedAncestors(el)) {
    if (a.localName === "spl-autocomplete") box ??= a;
    else if (box && CHOICE_FIELDS.has(a.localName)) return box;
  }
  return null;
}

/** The fixed choices of such a box, read without opening it; null when unreadable. */
export const readSrChoices = async (box: Element, ctx: WriteCtx): Promise<{ label: string; value: string }[] | null> =>
  (await ctx.main?.request<SrSearchState>("sr.search", { selector: selectorFor(box), close: false }, ctx.timing.srAwaitMs).catch(() => null))?.list.map((o) => ({ label: o.label, value: o.id })) ?? null;

/** The applicant's state and country, to tell one town of a name from another. */
export type SrPlace = { state?: string; country?: string };
type Opt = { id: string; label: string };

/**
 * A search box (spl-autocomplete) holds its own value; the spl-input inside shows only the query. It commits the
 * option its label names exactly (or a bare Yes/No opens), else the typed text when it takes that, else (a place box) the place the
 * applicant's state and country single out, else nothing. A box that held a value before the selection proves the
 * write only by holding another one after it.
 */
async function writeSrSearchBox(box: Element, value: string, ctx: WriteCtx, where?: SrPlace): Promise<WriteOutcome> {
  const main = ctx.main;
  if (!main) return { ok: false, reason: "binding-unavailable" };
  const selector = selectorFor(box);
  const wait = ctx.timing.srAwaitMs;
  const call = (method: string, args: unknown[]) => main.request("component.call", { selector, method, args, timeoutMs: wait }, wait + 1000);
  const read = (close = false) => main.request<SrSearchState>("sr.search", { selector, close }, wait);
  try {
    const held = await read();
    await call("searchByQuery", [value]);
    let state = await read();
    for (const end = Date.now() + wait; state.loading && Date.now() < end; state = await read()) await ctx.sleep(ctx.timing.pollMs);
    const own = state.options.filter((o) => o.id !== state.custom && o.label.trim() !== "");
    const exact = own.filter((o) => normalize(o.label) === normalize(value));
    // A bare Yes or No takes the one option that opens with it ("No, I have not been employed by ...").
    const polar = /^(?:yes|no)$/.test(normalize(value)) ? own.filter((o) => normalize(o.label).startsWith(normalize(value) + " ")) : [];
    let pick: Opt | undefined = exact.length === 1 ? exact[0] : polar.length === 1 ? polar[0] : state.options.find((o) => state.custom && o.id === state.custom);
    if (!pick && /location/.test(box.getAttribute("data-test") ?? "")) {
      const target = value.includes(",") ? value : [value, where?.state, where?.country].filter((p) => p?.trim()).join(", ");
      const hit = geoChooser(target)(own.map((o) => ({ el: box as HTMLElement, label: o.label, value: o.id, selected: false, disabled: false })));
      pick = own.find((o) => o.id === hit?.value);
    }
    if (!pick) {
      await read(true);
      return { ok: false, reason: "option-not-found", abstain: true, detail: own.map((o) => o.label).slice(0, 5).join(" | ") };
    }
    await call("handleOptionSelect", [{ detail: { value: pick.id } }]);
    const now = await read();
    return now.hasValue && (!held.hasValue || now.value !== held.value) ? { ok: true, optionLabel: pick.label } : { ok: false, reason: "write-not-committed" };
  } catch (e) {
    return { ok: false, reason: e instanceof Error && /binding-unavailable/.test(e.message) ? "binding-unavailable" : "write-not-committed" };
  }
}

export async function writeSrComponent(el: HTMLInputElement | HTMLTextAreaElement, value: string, ctx: WriteCtx, where?: SrPlace): Promise<WriteOutcome> {
  const box = Array.from(composedAncestors(el)).find((a) => a.localName === "spl-autocomplete");
  if (box) return writeSrSearchBox(box, value, ctx, where);
  const host = (srHost(el) ?? cardHost(el)) as Element;
  if (!ctx.main) return { ok: false, reason: "binding-unavailable" };
  let arg: unknown = value;
  if (host.localName === "spl-textarea") arg = value.replace(/;/g, ",");
  if (host.localName === "spl-datepicker" || host.localName === "spl-date-field") {
    // The validator calls getFullYear(), so the component gets a real Date in local time.
    const iso = isoDate(value);
    if (!iso) return { ok: false, reason: "format-not-accepted" };
    arg = { $date: iso };
  }
  const main = ctx.main;
  const selector = selectorFor(host);
  const wait = ctx.timing.srAwaitMs;
  const call = (method: string, args: unknown[]) => main.request("component.call", { selector, method, args, timeoutMs: wait }, wait + 1000);
  // Text the box already showed proves nothing: the write shows the value sent, or other text than before.
  const before = collapse(el.value);
  try {
    await call("handleChange", [arg]);
  } catch (e) {
    return { ok: false, reason: e instanceof Error && /binding-unavailable/.test(e.message) ? "binding-unavailable" : "write-not-committed" };
  }
  const took = (now: string) => now !== "" && (now !== before || now === collapse(String(arg)));
  const shown = await waitFor(ctx, () => took(collapse(el.value)), ctx.timing.settleMs * 4 + 20);
  if (!shown) return { ok: false, reason: "write-not-committed" };
  if (!PLAIN.has(host.localName)) return { ok: true, optionLabel: el.value };
  // The component validates its own value; one it refuses would hold Next, so it is cleared and left to the user.
  try {
    const read = () => main.request<{ pending: boolean; valid: boolean; message: string }>("sr.validity", { selector }, wait);
    let v = await read();
    for (const end = Date.now() + wait; v.pending && Date.now() < end; v = await read()) await ctx.sleep(ctx.timing.pollMs);
    if (!v.valid) {
      ctx.log("sr-refused", { message: v.message });
      await call("clear", []).catch(() => undefined);
      return { ok: false, reason: "format-not-accepted", detail: v.message };
    }
  } catch {
    // A component with no verdict to read keeps the value it showed.
  }
  return { ok: true, optionLabel: el.value };
}
