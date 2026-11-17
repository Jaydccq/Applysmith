// Clean-room: docs/provenance.md#CR-ENGINE
// The listbox driver: open the trigger, find the popup that belongs to it,
// choose an option, activate it with escalating gestures, verify the trigger's
// own display, and leave the widget closed.

import { displayMatches, scoreChooser, type Chooser } from "./choosers";
import { committedTokens, holdsSearchText, triggerDisplay } from "./committed";
import { clickLabel, isUnsafeOptionLabel } from "./click-guard";
import { composedAncestors, fire, fireInput, flatText, nativeValueSetter, normalize } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { clearText, focusEl, pointerSequence, press, safePress, tap, typeText } from "./gestures";
import { byIdDeep, categoryLabel, cleanOptionLabel, classTokens, hasEmptyPopup, isCategoryRow, isChoiceRow, optionsOf, PATH_SEP, populatedPopups, popupFor, popupSearchBox, reactSelectControl, type OptionRow } from "./popup";
import { isRendered } from "./visibility";
import { declineSeeds, isDecline, type WriteOutcome } from "./writers";

export interface Traits {
  /** The text box that takes a search, when the trigger is or holds one. */
  input: HTMLInputElement | null;
  /** Suggestions mount only after typing, so the seed is typed before any click. */
  typeahead: boolean;
  reactSelect: boolean;
  /** Toggles shut on a trailing click. */
  rippling: boolean;
  /** Declares a menu id, or the Fabric toggle class. */
  fabric: boolean;
  /** SuccessFactors paginated picklist: server filter, never committed by Enter. */
  paginated: boolean;
  minQuery: number;
  /** Names its popup with aria-controls or aria-owns. */
  hasRef: boolean;
}

const KEYBOARD_FIRST = new WeakSet<Document>();

function innerInput(trigger: HTMLElement): HTMLInputElement | null {
  if (trigger.localName === "input") return trigger as HTMLInputElement;
  const control = reactSelectControl(trigger);
  const scope: Element = control ?? trigger;
  return scope.querySelector<HTMLInputElement>("input:not([type=hidden]):not([type=checkbox]):not([type=radio])");
}

function minQueryOf(trigger: Element): number {
  let n = 0;
  let depth = 0;
  for (const el of [trigger, ...composedAncestors(trigger)]) {
    if (depth++ > 12) break;
    const v = el.getAttribute("minquerylength") ?? el.getAttribute("min-query-length") ?? el.getAttribute("data-minquerylength");
    if (v && /^\d+$/.test(v)) n = Math.max(n, parseInt(v, 10));
  }
  return n;
}

export function traitsOf(trigger: HTMLElement): Traits {
  const box = innerInput(trigger);
  // A read-only box that is the trigger takes no typed text: its list is opened and chosen from, never searched.
  const input = box === trigger && trigger.hasAttribute("readonly") ? null : box;
  const wrapper = trigger.closest("[role=combobox]") ?? (trigger.getAttribute("role") === "combobox" ? trigger : null);
  const auto = (input?.getAttribute("aria-autocomplete") ?? wrapper?.getAttribute("aria-autocomplete") ?? trigger.getAttribute("aria-autocomplete") ?? "").toLowerCase();
  const reactSelect = reactSelectControl(trigger) !== null;
  const minQuery = minQueryOf(input ?? trigger);
  // A combobox wrapper around a text box mounts suggestions only once the box has text.
  const aria11 = wrapper !== null && wrapper.localName !== "input" && input !== null && wrapper.contains(input);
  const ancestors = [trigger, ...composedAncestors(trigger)].slice(0, 6);
  return {
    input,
    typeahead: input !== null && !reactSelect && (auto === "list" || auto === "both" || aria11 || minQuery > 0),
    reactSelect,
    rippling: ancestors.some((a) => classTokens(a).some((t) => t.startsWith("rpd-"))),
    fabric: trigger.hasAttribute("data-menu-id") || classTokens(trigger).includes("fab-SelectToggle"),
    paginated: !!trigger.closest(".paginatedPicklistContainer") || classTokens(trigger).includes("rcmpaginatedselectinput"),
    minQuery,
    hasRef: !!(trigger.getAttribute("aria-controls") || trigger.getAttribute("aria-owns") || input?.getAttribute("aria-controls")),
  };
}

export interface ListboxRequest {
  /** One answer for a single choice; several, in order, for a multi-value control. */
  answers: string[];
  /** The question, for choosing search seeds. */
  label: string;
  multi?: boolean;
  /** "select up to N" on a multi-value control. */
  limit?: number;
  chooser?: Chooser;
  /** What to type into a search box, most specific first. */
  seeds?: string[];
  /** Whether the trigger's display shows that this option is committed. */
  accept?: (display: string, row: OptionRow) => boolean;
  /** A committed value that holds neither the option label nor this seed is cleared. */
  cleanupSeed?: string;
  /** A street box: when its search lists nothing at all for the street, the street typed is the answer and stays. */
  typedStands?: boolean;
}

export interface Trace {
  stage: string;
  labels: string[];
  /** The text last typed into a search box: it surviving is not a commit. */
  typed?: string;
}

const nativeSet = (el: HTMLInputElement, v: string): void => nativeValueSetter(el)?.(v);

const note = (trace: Trace, stage: string): void => {
  trace.stage = stage;
};

/** The first labels of a menu, for the diagnostic log. */
const labelsOf = (rows: OptionRow[]): string[] => rows.slice(0, 20).map((o) => o.label);

interface Listed {
  popup: Element;
  rows: OptionRow[];
}

/** The trigger's popup and the rows it offers, from one lookup and one read of its options. */
function listedNow(trigger: HTMLElement, ctx: WriteCtx): Listed | null {
  const l = popupFor(trigger, ctx.doc);
  return l.state === "found" ? l : null;
}

function popupNow(trigger: HTMLElement, ctx: WriteCtx): Element | null {
  return listedNow(trigger, ctx)?.popup ?? null;
}

/** The rows of the trigger's popup as it is now; of the popup in hand when the lookup finds none. */
function rowsNow(trigger: HTMLElement, ctx: WriteCtx, held: Element): OptionRow[] {
  return listedNow(trigger, ctx)?.rows ?? optionsOf(held);
}

function expanded(trigger: HTMLElement, t: Traits): boolean {
  return [trigger, t.input, trigger.closest("[role=combobox]")].some((e) => e?.getAttribute("aria-expanded") === "true");
}

/** Leave the widget closed: Escape, then one toggle if it still reports itself open. */
export async function closeWidget(trigger: HTMLElement, t: Traits, popup: Element | null, ctx: WriteCtx): Promise<void> {
  const open = () => expanded(trigger, t) || (popup !== null && popup.isConnected && isRendered(popup) && optionsOf(popup).length > 0 && expandedOrUnknown(trigger, t));
  if (!open()) return;
  press(t.input ?? trigger, "Escape");
  await ctx.sleep(ctx.timing.pollMs);
  if (expanded(trigger, t)) {
    pointerSequence(trigger);
    await ctx.sleep(ctx.timing.pollMs);
  }
}

/** A popup with no aria-expanded to read counts as open while it is rendered. */
function expandedOrUnknown(trigger: HTMLElement, t: Traits): boolean {
  const has = [trigger, t.input, trigger.closest("[role=combobox]")].some((e) => e?.hasAttribute("aria-expanded"));
  return !has || expanded(trigger, t);
}

async function drainStale(t: Traits, ctx: WriteCtx): Promise<void> {
  if (t.hasRef || t.fabric) return;
  if (!populatedPopups(ctx.doc).length) return;
  await waitFor(ctx, () => populatedPopups(ctx.doc).length === 0, ctx.timing.staleDrainMs);
}

async function waitPopup(trigger: HTMLElement, ctx: WriteCtx, ms: number): Promise<Element | null> {
  return waitFor(ctx, () => popupNow(trigger, ctx), ms);
}

/**
 * Opens the trigger's popup with the pointer sequence (on the control
 * container of a react-select, without the trailing click on a Rippling-style
 * combobox). A miss is retried with blur, a pause, refocus, the pointer
 * sequence and ArrowDown; a document whose first widget opened only on
 * ArrowDown tries the keyboard first from then on.
 */
export interface OpenWaits {
  open: number;
  retry: number;
}

export async function openPopup(trigger: HTMLElement, t: Traits, seed: string, ctx: WriteCtx, trace: Trace, waits?: OpenWaits): Promise<Element | "unsafe-click" | null> {
  const timing = ctx.timing;
  const target = (t.reactSelect ? (reactSelectControl(trigger) as HTMLElement | null) : null) ?? trigger;
  const pointer = (): "ok" | "unsafe-click" => {
    focusEl(trigger);
    return pointerSequence(target, { click: !t.rippling });
  };
  await drainStale(t, ctx);
  note(trace, "open");
  const openWait = waits?.open ?? (t.paginated ? timing.picklistOpenMs : timing.openWaitMs);
  let popup: Element | null = null;
  if (KEYBOARD_FIRST.has(ctx.doc) && !t.reactSelect && !t.typeahead) {
    focusEl(trigger);
    press(trigger, "ArrowDown");
    popup = await waitPopup(trigger, ctx, openWait);
    if (popup) return popup;
    const searched = await searchInPopup(trigger, t, seed, ctx, trace);
    if (searched !== "no-box") return searched;
    if (pointer() === "unsafe-click") return "unsafe-click";
    popup = await waitPopup(trigger, ctx, openWait);
    if (popup) return popup;
  } else {
    if (pointer() === "unsafe-click") return "unsafe-click";
    popup = await waitPopup(trigger, ctx, openWait);
    if (popup) return popup;
  }
  const searched = await searchInPopup(trigger, t, seed, ctx, trace);
  if (searched !== "no-box") return searched;

  note(trace, "open-retry");
  trigger.blur();
  await ctx.sleep(timing.tapMs * 3);
  focusEl(trigger);
  if (pointer() === "unsafe-click") return "unsafe-click";
  popup = await waitPopup(trigger, ctx, timing.pollMs * 3);
  if (popup) return popup;
  press(t.input ?? trigger, "ArrowDown");
  popup = await waitPopup(trigger, ctx, waits?.retry ?? timing.retryWaitMs);
  if (popup) {
    if (!t.reactSelect && !t.typeahead) KEYBOARD_FIRST.add(ctx.doc);
    return popup;
  }
  if (t.input && !t.typeahead && seed) {
    note(trace, "open-by-typing");
    await typeText(t.input, seed, ctx);
    trace.typed = seed;
    popup = await waitPopup(trigger, ctx, timing.suggestWaitMs);
    if (popup) return popup;
  }
  note(trace, hasEmptyPopup(trigger) ? "no-options" : "not-opened");
  return null;
}

/**
 * A popup that opened empty on its own search box (Nebius's city lookup)
 * lists nothing until something is typed: the seed is typed into that box,
 * and the popup is never clicked again, which would shut it. With no seed
 * (the prescan) it stops at stage "search-box". "no-box" when the open popup
 * carries no such box.
 */
async function searchInPopup(trigger: HTMLElement, t: Traits, seed: string, ctx: WriteCtx, trace: Trace): Promise<Element | null | "no-box"> {
  const box = t.input ? null : popupSearchBox(trigger);
  if (!box) return "no-box";
  if (!seed) {
    note(trace, "search-box");
    return null;
  }
  note(trace, "search-in-popup");
  await typeText(box, seed, ctx);
  trace.typed = seed;
  const popup = await waitPopup(trigger, ctx, ctx.timing.suggestWaitMs);
  if (!popup) note(trace, hasEmptyPopup(trigger) ? "no-options" : "not-opened");
  return popup;
}

function firstAlnum(text: string): string {
  return /[a-z0-9]/i.exec(text)?.[0] ?? "";
}

/**
 * What may be scrolled to bring more of a list in: the popup, whatever scrolls
 * inside it, and a box around it (three ancestors at most) that scrolls by its
 * own style. The page itself is never scrolled for a list.
 */
function scrollers(popup: Element): HTMLElement[] {
  const out: HTMLElement[] = [popup as HTMLElement];
  const styled = (h: HTMLElement): boolean => {
    const y = h.ownerDocument.defaultView?.getComputedStyle(h).overflowY ?? "";
    return y === "auto" || y === "scroll";
  };
  popup.querySelectorAll("*").forEach((el) => {
    const h = el as HTMLElement;
    if ((styled(h) || h.scrollHeight > h.clientHeight) && !out.includes(h)) out.push(h);
  });
  const doc = popup.ownerDocument;
  let up = popup.parentElement;
  for (let i = 0; up && up !== doc.body && up !== doc.documentElement && i < 3; i++, up = up.parentElement) {
    if (styled(up) && !out.includes(up)) out.push(up);
  }
  return out;
}

/**
 * A row in a long or virtualised list: type-ahead on the first letter (5
 * tries), ArrowDown (40 presses, giving up after 2 samples that did not move),
 * then scrolling every scroller (45 steps each, giving up on one that sticks).
 * A scroller that brought no row in is put back where it was.
 */
async function searchLongList(trigger: HTMLElement, t: Traits, popup: Element, choose: Chooser, answer: string, ctx: WriteCtx): Promise<OptionRow | null> {
  const key = t.input ?? trigger;
  // The popup in hand is read while it is on the page and shown; the document is searched for it again only once it is not.
  let held = popup;
  const look = (): OptionRow | null => {
    if (!held.isConnected || !isRendered(held)) held = popupNow(trigger, ctx) ?? held;
    return choose(optionsOf(held));
  };
  const letter = firstAlnum(answer);
  if (letter) {
    for (let i = 0; i < 5; i++) {
      press(key, letter);
      await ctx.sleep(ctx.timing.pollMs);
      const hit = look();
      if (hit) return hit;
    }
  }
  let stagnant = 0;
  let last = activeId(key, trigger);
  for (let i = 0; i < 40 && stagnant < 2; i++) {
    press(key, "ArrowDown");
    await ctx.sleep(1);
    const hit = look();
    if (hit) return hit;
    if (i % 3 === 2) {
      const now = activeId(key, trigger);
      stagnant = now === last ? stagnant + 1 : 0;
      last = now;
    }
  }
  for (const s of scrollers(popup)) {
    let stuck = 0;
    const start = s.scrollTop;
    s.scrollTop = 0;
    for (let i = 0; i < 45 && stuck < 2; i++) {
      const before = s.scrollTop;
      s.scrollTop = before + Math.max(160, Math.round(s.clientHeight * 0.85));
      fire(s, "scroll");
      fire(s, "wheel");
      await ctx.sleep(ctx.timing.pollMs);
      const hit = look();
      if (hit) return hit;
      stuck = s.scrollTop === before ? stuck + 1 : 0;
    }
    s.scrollTop = start;
    fire(s, "scroll");
  }
  return null;
}

/** The most nudges to bring a row into its scroller, and the margin left around it. */
const NUDGES = 6;
const NUDGE_PAD = 8;

/**
 * Brings a row into view inside the scroller that holds it: the innermost box
 * around the row that has more content than height. A fixed popup with an
 * inner scroller ignores the page-level scroll that scrollIntoView does, so
 * the scroller itself is moved by the row's distance from its edge.
 */
async function nudgeIntoView(el: Element, popup: Element, ctx: WriteCtx): Promise<void> {
  let holder: HTMLElement | null = null;
  for (const s of scrollers(popup)) {
    if (s !== el && s.contains(el) && s.scrollHeight > s.clientHeight && (!holder || holder.contains(s))) holder = s;
  }
  if (!holder) return;
  for (let i = 0; i < NUDGES; i++) {
    const row = el.getBoundingClientRect();
    const view = holder.getBoundingClientRect();
    const delta = row.top < view.top + NUDGE_PAD ? row.top - view.top - NUDGE_PAD : row.bottom > view.bottom - NUDGE_PAD ? row.bottom - view.bottom + NUDGE_PAD : 0;
    if (delta === 0) return;
    holder.scrollTop += delta;
    fire(holder, "scroll");
    await ctx.sleep(ctx.timing.pollMs);
  }
}

function activeId(key: Element, trigger: Element): string {
  return key.getAttribute("aria-activedescendant") ?? trigger.getAttribute("aria-activedescendant") ?? "";
}

/** The label of the highlighted option: the one aria-activedescendant names, else the popup's focused row (the popup is looked up only then). */
function activeLabel(key: Element, trigger: Element, popup: () => Element): string {
  const id = activeId(key, trigger);
  const el = id ? byIdDeep(trigger, id) : (popup().querySelector('[class*="--is-focused"]') ?? null);
  return el ? cleanOptionLabel(flatText(el)) : "";
}

type Activation = "ok" | "unsafe-click" | "verify-mismatch" | "failed";

/**
 * A suggestion in a search box: a row the search already highlights with
 * aria-selected (Ashby's place search) is committed with Enter. Otherwise it is
 * clicked directly with no scrolling (scrolling re-renders a streaming menu and
 * detaches the row). When that does not commit and the row is the first
 * option, Enter (after ArrowDown unless the row is already highlighted) is
 * tried, twice at most, retyping the seed before the second try.
 */
async function activateSuggestion(
  trigger: HTMLElement,
  t: Traits,
  input: HTMLInputElement,
  popup: Element,
  row: OptionRow,
  holds: () => boolean,
  ctx: WriteCtx,
  trace: Trace,
  seed: string,
): Promise<Activation> {
  // A write whose budget is spent makes no gesture: the run has moved on to the next row.
  await ctx.checkpoint();
  if (row.selected) {
    note(trace, "suggestion-enter");
    if (safePress(input, "Enter") === "unsafe-click") return "unsafe-click";
    await ctx.sleep(ctx.timing.activateMs);
    if (holds()) return "ok";
  }
  note(trace, "suggestion-click");
  if (pointerSequence(row.el) === "unsafe-click") return "unsafe-click";
  await ctx.sleep(ctx.timing.activateMs);
  if (holds()) return "ok";
  const rows = rowsNow(trigger, ctx, popup);
  if (t.paginated || !rows.length || normalize(rows[0]?.label ?? "") !== normalize(row.label)) return "failed";
  for (let attempt = 0; attempt < 2; attempt++) {
    note(trace, "suggestion-keyboard");
    if (attempt > 0) {
      await typeText(input, seed, ctx);
      // The suggestions arrive after the seed; the target must be first again before the keyboard is trusted.
      const again = await waitFor(ctx, () => {
        const first = listedNow(trigger, ctx)?.rows[0];
        return first && normalize(first.label) === normalize(row.label) ? first : null;
      }, ctx.timing.suggestWaitMs);
      if (!again) return "failed";
    }
    // ArrowDown from a highlighted first row would move to the row below it.
    if (!rowsNow(trigger, ctx, popup)[0]?.selected) press(input, "ArrowDown");
    await ctx.sleep(1);
    if (safePress(input, "Enter") === "unsafe-click") return "unsafe-click";
    await ctx.sleep(ctx.timing.activateMs);
    if (holds()) return "ok";
  }
  return "failed";
}

/**
 * Activates an option with escalating gestures, reading the trigger back after
 * each one: a direct pointer on a react-select; a keyboard commit (the first
 * letter, then ArrowDown up to 20 times, giving up after 2 probes that did not
 * move, then Enter); scrolling the option into its own scroller and looking it
 * up again; the pointer sequence on the centre; focus with Enter and Space.
 */
async function activate(trigger: HTMLElement, t: Traits, popup: Element, row: OptionRow, holds: () => boolean, ctx: WriteCtx, trace: Trace, seed: string): Promise<Activation> {
  // A write whose budget is spent makes no gesture: the run has moved on to the next row.
  await ctx.checkpoint();
  const wait = () => ctx.sleep(ctx.timing.activateMs);
  const live = (): OptionRow | null => rowsNow(trigger, ctx, popup).find((o) => normalize(o.label) === normalize(row.label)) ?? null;
  // No gesture of any kind, key presses included, is aimed at a destructive or terminal option.
  if (isUnsafeOptionLabel(clickLabel(row.el))) return "unsafe-click";
  // An option that already advertises selection is never clicked: some widgets deselect on a second click.
  // A search's aria-selected is only its highlight, so a suggestion goes on to be committed.
  if (row.selected && !(t.typeahead && t.input)) {
    note(trace, "selected-row");
    return holds() ? "ok" : "verify-mismatch";
  }
  const key = t.input ?? trigger;
  if (t.typeahead && t.input) return activateSuggestion(trigger, t, t.input, popup, row, holds, ctx, trace, seed);
  if (t.reactSelect) {
    note(trace, "react-select-pointer");
    if (pointerSequence(row.el) === "unsafe-click") return "unsafe-click";
    await wait();
    if (holds()) return "ok";
  }
  if (!t.paginated) {
    note(trace, "keyboard-commit");
    focusEl(key);
    const letter = firstAlnum(row.label);
    if (letter) press(key, letter);
    let unmoved = 0;
    let lastActive = activeId(key, trigger);
    for (let i = 0; i < 20; i++) {
      if (normalize(activeLabel(key, trigger, () => popupNow(trigger, ctx) ?? popup)) === normalize(row.label)) {
        if (safePress(key, "Enter") === "unsafe-click") return "unsafe-click";
        await wait();
        if (holds()) return "ok";
        break;
      }
      press(key, "ArrowDown");
      await ctx.sleep(1);
      const now = activeId(key, trigger);
      unmoved = now === lastActive ? unmoved + 1 : 0;
      lastActive = now;
      if (unmoved >= 2) break;
    }
  }
  note(trace, "scroll-and-click");
  const first = live() ?? row;
  first.el.scrollIntoView?.({ block: "nearest" });
  await nudgeIntoView(first.el, popupNow(trigger, ctx) ?? popup, ctx);
  const relooked = live() ?? first;
  if (pointerSequence(relooked.el) === "unsafe-click") return "unsafe-click";
  await wait();
  if (holds()) return "ok";
  note(trace, "pointer-centre");
  const inner = (live() ?? relooked).el.firstElementChild as HTMLElement | null;
  if (inner && pointerSequence(inner) === "unsafe-click") return "unsafe-click";
  await wait();
  if (holds()) return "ok";
  note(trace, "focus-enter-space");
  const target = (live() ?? relooked).el;
  target.focus?.();
  if (safePress(target, "Enter") === "unsafe-click") return "unsafe-click";
  await wait();
  if (holds()) return "ok";
  if (safePress(target, " ") === "unsafe-click") return "unsafe-click";
  await wait();
  if (holds()) return "ok";
  return "failed";
}

/**
 * Enter on an input-backed list that showed no rows at all: it can only accept
 * the typed text, so the text must be the answer itself. A box left holding a
 * search seed (a place's country, a degree's level word) is retyped with the
 * answer first, and Enter is not pressed when that lists suggestions after
 * all, since it would take one of them. It counts as a commit only when a new
 * selected token appears that reads as the answer and the box has let go of
 * the typed text (a search box's own text reads as its widget's value while it
 * is typed); otherwise the box gets its previous text back so no search text
 * is left dangling.
 */
async function enterCommit(trigger: HTMLElement, input: HTMLInputElement, answer: string, prior: string, tokensBefore: string[], ctx: WriteCtx, trace: Trace): Promise<boolean> {
  await ctx.checkpoint(); // a spent budget presses nothing
  const restore = (): void => {
    if (input.value === prior) return;
    if (prior === "") clearText(input);
    else {
      nativeSet(input, prior);
      fireInput(input, prior, "insertReplacementText");
      fire(input, "change");
    }
  };
  if (normalize(input.value) !== normalize(answer)) {
    await typeText(input, answer, ctx);
    trace.typed = answer;
    if (await waitPopup(trigger, ctx, ctx.timing.suggestWaitMs)) {
      restore();
      return false;
    }
  }
  if (safePress(input, "Enter") === "unsafe-click") return false;
  const landed = await waitFor(ctx, () => {
    const now = committedTokens(trigger) ?? [];
    return normalize(input.value) !== normalize(answer) && now.join("|") !== tokensBefore.join("|") && now.some((x) => displayMatches(x, answer));
  }, ctx.timing.commitWaitMs);
  if (landed) return true;
  restore();
  return false;
}

/**
 * A search box: each seed is typed one key at a time, the popup is looked at
 * again on every poll (suggestions arrive in waves, the stale prefix wave
 * first) and the first option the chooser accepts is taken. When no popup ever
 * mounted, the open is retried with blur, a pause, refocus, the pointer
 * sequence and ArrowDown. A seed shorter than the widget's minimum is refused.
 */
async function typeaheadPick(
  trigger: HTMLElement,
  t: Traits,
  input: HTMLInputElement,
  seeds: string[],
  choose: Chooser,
  ctx: WriteCtx,
  trace: Trace,
): Promise<{ pick: OptionRow | null; popup: Element | null } | "not-opened" | "unsafe-click"> {
  await drainStale(t, ctx);
  const usable = seeds.filter((s) => s.length >= t.minQuery);
  if (!usable.length) {
    note(trace, "seed-below-minimum");
    return "not-opened";
  }
  const waitMs = t.paginated ? ctx.timing.picklistFilterMs : ctx.timing.suggestWaitMs;
  let popup: Element | null = null;
  let mounted = false;
  const look = () => {
    const l = listedNow(trigger, ctx);
    if (!l) return null;
    popup = l.popup;
    mounted = true;
    trace.labels = labelsOf(l.rows);
    return choose(l.rows);
  };
  note(trace, "type-seed");
  for (const [i, seed] of usable.entries()) {
    if (i > 0) note(trace, "retry-shorter-seed");
    await typeText(input, seed, ctx);
    trace.typed = seed;
    const pick = await waitFor(ctx, look, waitMs);
    if (pick) return { pick, popup };
  }
  if (!mounted) {
    note(trace, "open-retry");
    input.blur();
    await ctx.sleep(ctx.timing.tapMs * 3);
    focusEl(input);
    const target = (t.reactSelect ? (reactSelectControl(trigger) as HTMLElement | null) : null) ?? trigger;
    if (pointerSequence(target, { click: !t.rippling }) === "unsafe-click") return "unsafe-click";
    press(input, "ArrowDown");
    const pick = await waitFor(ctx, look, ctx.timing.retryWaitMs);
    if (pick) return { pick, popup };
    if (!mounted) return "not-opened";
  }
  return { pick: null, popup };
}

/**
 * Opens the category a "Category > Choice" answer names (a menu read whole by
 * the prescan) and returns its sub-menu, or null when the menu has no such
 * category or it shows nothing new.
 */
async function openCategory(trigger: HTMLElement, popup: Element, category: string, ctx: WriteCtx, trace: Trace): Promise<Element | null> {
  const row = optionsOf(popup).find((r) => normalize(categoryLabel(r.label)) === normalize(category));
  if (!row || !isCategoryRow(row.el) || isChoiceRow(row.el)) return null;
  note(trace, "open-category");
  const before = optionsOf(popup).map((o) => o.label).join("|");
  if (pointerSequence(row.el) === "unsafe-click") return null;
  return waitFor(ctx, () => {
    const l = listedNow(trigger, ctx);
    return l && l.rows.map((o) => o.label).join("|") !== before ? l.popup : null;
  }, ctx.timing.openWaitMs);
}

async function pickOne(trigger: HTMLElement, t: Traits, answer: string, req: ListboxRequest, ctx: WriteCtx, trace: Trace): Promise<WriteOutcome> {
  let choose = req.chooser ?? scoreChooser([answer], t.reactSelect);
  const seeds = req.seeds?.length ? req.seeds : [answer];
  trace.typed = undefined;
  const priorInput = t.input?.value ?? "";
  const before = committedTokens(trigger) ?? [];
  // A value a multi-value control already holds is left alone: choosing a chosen option again removes it on some widgets.
  if (req.multi && before.some((x) => normalize(x) === normalize(answer))) return { ok: true, optionLabel: answer };
  let popup: Element | null = null;
  const done = async (out: WriteOutcome): Promise<WriteOutcome> => {
    await closeWidget(trigger, t, popup, ctx);
    if (!out.ok) {
      const detail = `stage=${trace.stage} labels=${trace.labels.join("|")}`;
      ctx.log("listbox-failed", { reason: out.reason, stage: trace.stage, labels: trace.labels });
      return { ...out, detail };
    }
    return out;
  };
  /** A search text is no answer: a box left holding one gets back what it held before the search. */
  const restoreInput = async (): Promise<void> => {
    if (!t.input || t.input.value === priorInput) return;
    if (priorInput === "") clearText(t.input);
    else {
      await typeText(t.input, priorInput, ctx);
      fire(t.input, "change");
    }
  };
  /** The option's own selected state, read from the list as it is now (a list that closed keeps its options' state). */
  const optionSelected = (row: OptionRow): boolean => {
    const rows = listedNow(trigger, ctx)?.rows ?? (popup ? optionsOf(popup) : []);
    return rows.some((o) => o.selected && normalize(o.label) === normalize(row.label));
  };
  const acceptDisplay = (row: OptionRow) => (): boolean => {
    // A list of several choices that shows no chips the engine can read (a button with a count) holds a choice on the option itself.
    // A search's aria-selected is only its highlight, so a search box is never verified this way.
    if (req.multi && committedTokens(trigger) === null && !(t.typeahead && t.input)) return optionSelected(row);
    if (t.input && holdsSearchText(t.input)) return false;
    const d = triggerDisplay(trigger);
    if (d === "") return false;
    // A combobox wrapper has committed only once it has closed: the typed seed surviving is not a commit.
    if (t.typeahead && trigger !== t.input && expanded(trigger, t)) return false;
    // The typed seed is a search, not an answer: it counts only when it is the option itself.
    if (trace.typed !== undefined && normalize(d) === normalize(trace.typed) && normalize(d) !== normalize(row.label)) return false;
    if (req.multi) {
      const now = committedTokens(trigger) ?? [];
      const names = (x: string): boolean => normalize(x) === normalize(row.label) || displayMatches(x, answer);
      // A new chip, or the chip of an option that was selected from the start.
      return now.some(names) && (now.length > before.length || before.some(names));
    }
    if (req.accept) return req.accept(d, row);
    // A place may be written differently in the box than in the suggestion; it must still hold the city.
    if (req.cleanupSeed !== undefined && normalize(d).includes(normalize(req.cleanupSeed))) return true;
    return displayMatches(d, row.label) || displayMatches(d, answer);
  };

  // A search that lists suggestions only once something is typed is never answered by a near miss: it is left to the person.
  let searched = t.typeahead || t.paginated;
  let pick: OptionRow | null = null;
  if (t.typeahead && t.input) {
    // A search lists only what contains what was typed, so a decline is also searched under the other words an option can use for it.
    const found = await typeaheadPick(trigger, t, t.input, [...seeds, ...declineSeeds(answer)], choose, ctx, trace);
    if (found === "unsafe-click") return done({ ok: false, reason: "unsafe-click" });
    const noRows = found === "not-opened" || !found.popup || optionsOf(found.popup).length === 0;
    if (req.typedStands && noRows && trace.typed !== undefined && t.input.value === trace.typed) {
      note(trace, "typed-stands");
      fire(t.input, "change");
      fire(t.input, "focusout");
      t.input.blur();
      await ctx.sleep(ctx.timing.settleMs);
      if (t.input.value === trace.typed) return done({ ok: true });
    }
    if (found !== "not-opened") {
      pick = found.pick;
      popup = found.popup;
    }
    // A decline is an option of a list or nothing: it is never typed in as free text.
    if (!pick && noRows && !t.paginated && !t.minQuery && t.input.value !== "" && !isDecline(answer)) {
      note(trace, "enter-commit");
      if (await enterCommit(trigger, t.input, answer, priorInput, before, ctx, trace)) return done({ ok: true, optionLabel: answer });
    }
    // A search that never lists anything for a decline has no decline option to commit: the box stays blank.
    if (found === "not-opened") {
      await restoreInput();
      return done({ ok: false, reason: "listbox-not-opened", abstain: isDecline(answer) });
    }
  } else {
    const opened = await openPopup(trigger, t, seeds[0] as string, ctx, trace);
    if (opened === "unsafe-click") return done({ ok: false, reason: "unsafe-click" });
    if (!opened) return done({ ok: false, reason: "listbox-not-opened" });
    popup = opened;
    // The popup mounted only once the seed was typed: it lists suggestions for the seed, not a list to scroll.
    const bySearch = trace.typed !== undefined;
    searched ||= bySearch;
    note(trace, "choose");
    let rows = optionsOf(popup);
    let target = answer;
    // A choice inside a category is reached through the category.
    const path = answer.split(PATH_SEP);
    if (path.length === 2 && !rows.some((r) => normalize(r.label) === normalize(answer))) {
      const inner = await openCategory(trigger, popup, path[0] as string, ctx, trace);
      if (inner) {
        popup = inner;
        rows = optionsOf(inner);
        target = path[1] as string;
        choose = scoreChooser([target], t.reactSelect);
      }
    }
    trace.labels = labelsOf(rows);
    if (!rows.length) note(trace, "no-options");
    pick = choose(rows);
    if (!pick && bySearch) {
      // Suggestions arrive in waves for the typed seed: the final wave is waited for.
      pick = await waitFor(ctx, () => {
        const l = listedNow(trigger, ctx);
        if (!l) return null;
        popup = l.popup;
        trace.labels = labelsOf(l.rows);
        return choose(l.rows);
      }, ctx.timing.suggestWaitMs);
    }
    // A react-select with a search box is searched by typing: walking its list reads the whole menu at every step, and a catalog lists only its first page (Greenhouse's schools).
    if (!pick && rows.length && !bySearch && !(t.reactSelect && t.input)) pick = await searchLongList(trigger, t, popup, choose, target, ctx);
    if (!pick && t.input && !bySearch) {
      // An async-search input filters as it is typed; the typing keeps focus. Each seed is typed in turn (a field of study, then its endings).
      note(trace, "type-to-filter");
      for (const seed of seeds) {
        await typeText(t.input, seed, ctx);
        pick = await waitFor(ctx, () => {
          const l = listedNow(trigger, ctx);
          if (!l) return null;
          popup = l.popup;
          trace.labels = labelsOf(l.rows);
          return choose(l.rows);
        }, ctx.timing.suggestWaitMs);
        if (pick) break;
      }
    }
  }

  if (!pick) {
    note(trace, `${trace.stage}:no-candidate`);
    await restoreInput();
    // A menu with no option that declines leaves a decline blank: it is not a write that failed.
    return done({ ok: false, reason: "option-not-found", abstain: searched || isDecline(answer) });
  }

  const row = pick;
  const result = await activate(trigger, t, popup ?? trigger, row, acceptDisplay(row), ctx, trace, trace.typed ?? (seeds[seeds.length - 1] as string));
  if (result === "unsafe-click") return done({ ok: false, reason: "unsafe-click" });
  if (result === "verify-mismatch") return done({ ok: false, reason: "verify-mismatch" });
  if (result === "failed") {
    note(trace, "not-committed");
    if (req.cleanupSeed !== undefined && t.input && triggerDisplay(t.input) !== "") clearText(t.input);
    else if (t.input && holdsSearchText(t.input)) await restoreInput();
    return done({ ok: false, reason: "write-not-committed" });
  }
  if (trigger.localName === "button") await tap(trigger, ctx);
  return done({ ok: true, optionLabel: row.label });
}

/**
 * Drives a custom listbox, combobox or search box to the answer. Every exit
 * leaves the widget closed; a failure carries its last stage and the labels
 * the menu showed, and an abstain carries nothing it may not have committed.
 */
export async function writeListbox(trigger: HTMLElement, req: ListboxRequest, ctx: WriteCtx): Promise<WriteOutcome> {
  const t = traitsOf(trigger);
  const trace: Trace = { stage: "start", labels: [] };
  if (!req.multi) return pickOne(trigger, t, req.answers[0] as string, req, ctx, trace);
  const answers = req.limit ? req.answers.slice(0, req.limit) : req.answers;
  const chosen: string[] = [];
  let firstFailure: WriteOutcome | null = null;
  for (const answer of answers) {
    await ctx.checkpoint();
    const r = await pickOne(trigger, t, answer, { ...req, seeds: undefined }, ctx, trace);
    if (r.ok) chosen.push(r.optionLabel ?? answer);
    else firstFailure ??= r;
  }
  if (chosen.length) return { ok: true, optionLabel: chosen.join(", ") };
  return firstFailure ?? { ok: false, reason: "option-not-found" };
}
