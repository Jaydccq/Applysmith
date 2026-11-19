// Clean-room: docs/provenance.md#CR-ENGINE
// Component libraries with their own commit rules: Dojo dijit selects, Stencil
// sdf-select-simple, typeahead.js text boxes, intl-tel-input countries and
// TinyMCE editors.

import { scoreChooser } from "./choosers";
import { collapse, fire, flatText, normalize, walkElements } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { hover, pointerSequence, press, replaceText } from "./gestures";
import { classTokens, optionsOf, type OptionRow } from "./popup";
import { isPlaceholderText } from "./placeholder";
import { selectorFor } from "./selector";
import { STATE_NAMES } from "./states";
import { isRendered } from "./visibility";
import { countryNames, scoreOption, type WriteOutcome } from "./writers";

const clean = (t: string) => collapse(t.replace(/[​ ]/g, " "));

// --- Dojo dijit.form.Select -------------------------------------------------

/** The trigger is a table with a listbox role, a widget id and a popup; its own menu twin lacks the popup flag. */
export function isDijitSelect(el: Element): boolean {
  return el.hasAttribute("widgetid") && el.getAttribute("role") === "listbox" && el.hasAttribute("aria-haspopup") && classTokens(el).some((t) => t.startsWith("dijit"));
}

function dijitHidden(trigger: Element): HTMLInputElement | null {
  return trigger.parentElement?.querySelector<HTMLInputElement>('input[type="hidden"]') ?? null;
}

/** A hidden value that is empty, a single space or a "please specify" prompt holds no choice. */
export function dijitHasChoice(trigger: Element): boolean {
  const v = dijitHidden(trigger)?.value ?? "";
  return v.trim() !== "" && !isPlaceholderText(v);
}

function mouseGesture(el: HTMLElement): "ok" | "unsafe-click" {
  hover(el);
  return pointerSequence(el);
}

/**
 * The popup mounts lazily and its options depend on earlier choices, so they
 * are read at open time. A unique exact label, else the one label that holds
 * the answer (or is held by it) as whole words, the rule a native select's
 * options are matched by; ambiguity abstains with the popup closed. Success is the hidden input
 * changing to a value that is a choice, not the cosmetic display: a value it
 * held before the click proves no commit.
 */
export async function writeDijit(trigger: HTMLElement, answer: string, ctx: WriteCtx): Promise<WriteOutcome> {
  const widgetId = trigger.getAttribute("widgetid") as string;
  const hidden = dijitHidden(trigger);
  const before = hidden?.value ?? "";
  const target = answer.length === 2 && STATE_NAMES[answer.toUpperCase()] ? (STATE_NAMES[answer.toUpperCase()] as string) : answer;
  if (mouseGesture(trigger) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const popup = await waitFor(ctx, () => {
    const p = trigger.ownerDocument.getElementById(`${widgetId}_dropdown`);
    return p && optionsOf(p).length ? p : null;
  }, ctx.timing.openWaitMs);
  if (!popup) return { ok: false, reason: "listbox-not-opened" };
  const rows: OptionRow[] = optionsOf(popup).map((r) => ({ ...r, label: clean(r.label) }));
  const n = normalize(target);
  const exact = rows.filter((r) => normalize(r.label) === n);
  const loose = rows.filter((r) => scoreOption(r.label, target) >= 2);
  const pick = exact.length === 1 ? exact[0] : exact.length === 0 && loose.length === 1 ? loose[0] : undefined;
  const close = async () => {
    press(trigger, "Escape");
    await ctx.sleep(ctx.timing.pollMs);
  };
  if (!pick) {
    await close();
    return { ok: false, reason: "option-not-found", abstain: true };
  }
  if (mouseGesture(pick.el) === "unsafe-click") {
    await close();
    return { ok: false, reason: "unsafe-click" };
  }
  const landed = await waitFor(ctx, () => {
    const v = hidden?.value ?? "";
    return v !== before && v.trim() !== "" && !isPlaceholderText(v);
  }, ctx.timing.commitWaitMs);
  if (!landed) {
    await close();
    return { ok: false, reason: "write-not-committed" };
  }
  return { ok: true, optionLabel: pick.label };
}

// --- Stencil sdf-select-simple ---------------------------------------------

/** Exact label or value, then a unique word-boundary prefix or suffix, then a unique containment. */
export function sdfPick(rows: OptionRow[], answer: string): OptionRow | null {
  if (rows.length < 2) return null;
  const a = normalize(answer);
  const exact = rows.filter((r) => normalize(r.label) === a || normalize(r.value) === a);
  if (exact.length === 1) return exact[0] as OptionRow;
  const edge = rows.filter((r) => {
    const l = normalize(r.label);
    return l.startsWith(`${a} `) || l.endsWith(` ${a}`);
  });
  if (edge.length === 1) return edge[0] as OptionRow;
  const contains = rows.filter((r) => ` ${normalize(r.label)} `.includes(` ${a} `));
  return contains.length === 1 ? (contains[0] as OptionRow) : null;
}

function sdfItems(host: Element): OptionRow[] {
  const doc = host.ownerDocument;
  const out: OptionRow[] = [];
  const scan = (root: ParentNode) =>
    walkElements(root, (el) => {
      if (el.localName === "sdf-select-item" && isRendered(el)) {
        out.push({ el: el as HTMLElement, label: clean(flatText(el)), value: el.getAttribute("value") ?? "", selected: el.getAttribute("aria-selected") === "true", disabled: false });
      }
    });
  scan(host.shadowRoot ?? host);
  scan(doc.body ?? doc);
  return out.filter((r, i, all) => all.findIndex((x) => x.el === r.el) === i);
}

export function isSdfSelect(el: Element): boolean {
  return el.localName === "sdf-select-simple";
}

/** The value the component holds as text; the item object it keeps in the page's own world is not text. */
function heldValue(host: Element): string | undefined {
  const v = (host as HTMLElement & { value?: unknown }).value;
  return typeof v === "string" ? v : undefined;
}

/** What the component shows; when its display shows nothing, the value it holds itself. */
export function sdfDisplay(host: Element): string {
  const shown = host.shadowRoot?.querySelector(".trigger-button, [part=trigger]") ?? null;
  const held = clean(heldValue(host) ?? host.getAttribute("value") ?? "");
  const display = shown ? clean(flatText(shown)) : "";
  return display !== "" && !isPlaceholderText(display) ? display : isPlaceholderText(held) ? "" : held;
}

export async function writeSdf(host: HTMLElement, answer: string, ctx: WriteCtx): Promise<WriteOutcome> {
  const trigger = host.shadowRoot?.querySelector<HTMLElement>(".trigger-button, [part=trigger], button") ?? host;
  if (pointerSequence(trigger) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const rows = await waitFor(ctx, () => {
    const r = sdfItems(host);
    return r.length ? r : null;
  }, ctx.timing.openWaitMs);
  if (!rows) return { ok: false, reason: "listbox-not-opened" };
  const pick = sdfPick(rows, answer);
  if (!pick) {
    press(trigger, "Escape");
    return { ok: false, reason: "option-not-found", abstain: true };
  }
  if (pointerSequence(pick.el) === "unsafe-click") {
    press(trigger, "Escape");
    return { ok: false, reason: "unsafe-click" };
  }
  const holds = () => normalize(sdfDisplay(host)) === normalize(pick.label) || normalize(heldValue(host) ?? "") === normalize(pick.value || pick.label);
  if (await waitFor(ctx, holds, ctx.timing.commitWaitMs)) return { ok: true, optionLabel: pick.label };
  // A display that shows another choice is a mismatch; one that shows nothing means the click committed nothing.
  if (sdfDisplay(host) !== "") return { ok: false, reason: "verify-mismatch" };
  // The option was there and took no click: the component's own commit, run in the page's world, is the one other route.
  const out = await ctx.main?.request<{ committed: boolean; reason?: string }>("sdf.commit", { selector: selectorFor(host), value: pick.value, label: pick.label }).catch(() => null);
  if (out?.reason === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  if (out?.committed && (await waitFor(ctx, holds, ctx.timing.commitWaitMs))) return { ok: true, optionLabel: pick.label };
  return { ok: false, reason: sdfDisplay(host) !== "" ? "verify-mismatch" : "write-not-committed" };
}

// --- typeahead.js -----------------------------------------------------------

export function isTypeaheadJs(el: Element): boolean {
  return el.localName === "input" && classTokens(el).includes("tt-input") && !!el.closest(".twitter-typeahead");
}

/**
 * Typeahead.js only suggests: when no menu mounts the typed text is the
 * answer and the box must keep exactly that text.
 */
export async function writeTypeaheadJs(input: HTMLInputElement, value: string, ctx: WriteCtx): Promise<WriteOutcome> {
  replaceText(input, value);
  const menu = input.closest(".twitter-typeahead")?.querySelector(".tt-menu");
  const rows = await waitFor(ctx, () => {
    const r = menu ? optionsOf(menu).length : 0;
    return r > 0 && menu ? optionsOf(menu) : null;
  }, ctx.timing.suggestWaitMs);
  if (rows) {
    const pick = scoreChooser([value])(rows);
    if (pick) {
      if (pointerSequence(pick.el) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
      await ctx.sleep(ctx.timing.settleMs);
      return normalize(input.value) === normalize(pick.label) ? { ok: true, optionLabel: pick.label } : { ok: false, reason: "verify-mismatch" };
    }
    replaceText(input, value);
  }
  fire(input, "change");
  fire(input, "focusout");
  input.blur();
  await ctx.sleep(ctx.timing.settleMs);
  return input.value === value ? { ok: true } : { ok: false, reason: "verify-mismatch" };
}

// --- intl-tel-input country -------------------------------------------------

export function itiRoot(el: Element): HTMLElement | null {
  return el.closest<HTMLElement>(".iti");
}

/**
 * Picks a country by its two-letter code or by any of its names (dial code
 * stripped): the row that is exactly a name, else the one row that holds a
 * name as whole words ("Germany (Deutschland)"); "UK" is never Ukraine. With
 * the phone's dial code given, only a country whose row carries that code is
 * picked. The pick is verified by the row's own selected state, or by the
 * hidden input where a page updates one on a pick (the library itself fills
 * it only on submit). A country already selected is not clicked again.
 */
export async function writeItiCountry(root: HTMLElement, country: string, ctx: WriteCtx, dial = ""): Promise<WriteOutcome> {
  // intl-tel-input 20 and later name the button iti__selected-country.
  const flag = root.querySelector<HTMLElement>(".iti__selected-flag, .iti__selected-country");
  const hidden = root.querySelector<HTMLInputElement>('input[type="hidden"]');
  if (!flag) return { ok: false, reason: "driver-unavailable" };
  const before = hidden?.value ?? "";
  if (pointerSequence(flag) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const list = await waitFor(ctx, () => {
    const l = root.querySelector<HTMLElement>(".iti__country-list");
    return l && isRendered(l) && l.querySelector(".iti__country") ? l : null;
  }, ctx.timing.openWaitMs);
  const close = () => press(flag, "Escape");
  if (!list) {
    close();
    return { ok: false, reason: "listbox-not-opened" };
  }
  const dialOf = (el: HTMLElement) => `+${(el.getAttribute("data-dial-code") ?? el.querySelector(".iti__dial-code")?.textContent ?? "").replace(/\D/g, "")}`;
  const items = Array.from(list.querySelectorAll<HTMLElement>(".iti__country")).filter((i) => !dial || dialOf(i) === dial);
  const names = countryNames(country);
  const codes = names.filter((n) => /^[a-z]{2}$/.test(n));
  const nameOf = (el: HTMLElement) => normalize(clean(el.querySelector(".iti__country-name")?.textContent ?? flatText(el)).replace(/\+\d+/g, ""));
  const holding = items.filter((i) => names.some((n) => n.length >= 3 && ` ${nameOf(i)} `.includes(` ${n} `)));
  const pick =
    items.find((i) => codes.includes((i.getAttribute("data-country-code") ?? "").toLowerCase())) ||
    items.find((i) => names.includes(nameOf(i))) ||
    (holding.length === 1 ? holding[0] : undefined);
  if (!pick) {
    close();
    return { ok: false, reason: "option-not-found" };
  }
  const selected = () => classTokens(pick).includes("iti__active") || pick.getAttribute("aria-selected") === "true";
  if (selected()) {
    close();
    return { ok: true, optionLabel: nameOf(pick) };
  }
  if (pointerSequence(pick) === "unsafe-click") {
    close();
    return { ok: false, reason: "unsafe-click" };
  }
  const changed = await waitFor(ctx, () => selected() || (hidden?.value ?? "") !== before, ctx.timing.activateMs * 3);
  if (changed) return { ok: true, optionLabel: nameOf(pick) };
  // A list that took no pick is still open.
  if (isRendered(list)) close();
  return { ok: false, reason: "write-not-committed" };
}

// --- TinyMCE ------------------------------------------------------------------

/** The editor TinyMCE (version 5 and later, "tox" markup) draws beside the textarea it hides, or null. */
export function tinymceEditor(el: Element): HTMLElement | null {
  const next = el.localName === "textarea" ? el.nextElementSibling : null;
  return next && classTokens(next).includes("tox-tinymce") ? (next as HTMLElement) : null;
}

/** Inside the editor drawn for a textarea: its menu bar, toolbar and status bar are no field and no choice. */
export function insideTinymceEditor(el: Element): boolean {
  const prev = el.closest(".tox-tinymce")?.previousElementSibling;
  return !!prev && tinymceEditor(prev) !== null;
}

/** What the editor last saved into its textarea (HTML), as text. */
export function tinymceText(area: HTMLTextAreaElement): string {
  const t = area.ownerDocument.createElement("template");
  t.innerHTML = area.value;
  return flatText(t.content);
}

/**
 * The editor holds its content in the page's own world and saves it into the
 * textarea, so a value written to the textarea alone is overwritten by the
 * next save. The helper sets the editor's content and saves it, and the
 * editor's own text is the read-back. With no helper or no editor nothing is
 * written.
 */
export async function writeTinymce(area: HTMLTextAreaElement, value: string, ctx: WriteCtx): Promise<WriteOutcome> {
  if (!ctx.main) return { ok: false, reason: "binding-unavailable" };
  try {
    const out = await ctx.main.request<{ text: string }>("tinymce.set", { id: area.id, text: value });
    return collapse(out.text) === collapse(value) ? { ok: true } : { ok: false, reason: "verify-mismatch" };
  } catch (e) {
    return { ok: false, reason: e instanceof Error && /binding-unavailable/.test(e.message) ? "binding-unavailable" : "driver-unavailable" };
  }
}
