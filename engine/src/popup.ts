// Clean-room: docs/provenance.md#CR-ENGINE
// Which popup belongs to a trigger, and the options it offers. A trigger never
// borrows a neighbour's menu: when its own popup is not there yet the answer
// is "wait", not "use the one that is open".

import { collapse, composedAncestors, flatText, stripSchoolingGloss, walkElements } from "./dom";
import { isRendered } from "./visibility";

export interface OptionRow {
  el: HTMLElement;
  label: string;
  value: string;
  selected: boolean;
  disabled: boolean;
}

const OPTION_SEL = "[role=option], [role=menuitem], [role=menuitemradio], [role=menuitemcheckbox], [role=treeitem]";
/** What a screen reader is told after an option's label; a whole phrase set off from the label, so "Unselected" holds none. */
const STATE_SUFFIX = /(?:^|[\s,]+)(not checked|not selected|currently selected|selected|press delete to clear value)\s*$/i;
const GUID_SUFFIX = /\s*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\s*$/i;
const ZERO_WIDTH = /[\u200b-\u200d\ufeff]/g;
/**
 * A row that swaps the widget for a manual entry form is never an answer. It is a whole phrase: an instruction to enter
 * something by hand ("Fill in manually", "Enter manual address", "Manually enter your school") or a statement that the
 * thing cannot be found. An option that only holds the word ("Manual Testing") is an option.
 */
const ENTRY_VERB = "(?:enter|fill(?:\\s+\\w+)?\\s+in|add|type|input)";
const ESCAPE_ROW = new RegExp(`\\b${ENTRY_VERB}\\b.*\\bmanual(?:ly)?\\b|\\bmanually\\s+${ENTRY_VERB}\\b|can.?t find|cannot find|couldn.?t find`, "i");

export function classTokens(el: Element): string[] {
  return (el.getAttribute("class") ?? "").split(/\s+/).filter(Boolean);
}

export function hasClassSuffix(el: Element, suffix: string): boolean {
  return classTokens(el).some((t) => t.endsWith(suffix));
}

export function cleanOptionLabel(raw: string): string {
  let t = collapse(raw.replace(ZERO_WIDTH, ""));
  for (let i = 0; i < 3; i++) {
    // A label that is nothing but such a phrase ("Not Selected", "Selected") is the option's own name.
    const bare = t.replace(STATE_SUFFIX, "");
    const next = (bare.trim() === "" ? t : bare).replace(GUID_SUFFIX, "").trim();
    if (next === t) break;
    t = next;
  }
  return stripSchoolingGloss(t);
}

export function isEscapeRow(label: string): boolean {
  return ESCAPE_ROW.test(label);
}

function isSelected(el: Element): boolean {
  if (["aria-selected", "aria-checked", "aria-pressed"].some((a) => el.getAttribute(a) === "true")) return true;
  return classTokens(el).some((t) => t.endsWith("--is-selected") || t === "selected" || t === "is-selected");
}

/** Option rows of a popup, in order. Role-less menus fall back to library classes and list items. */
/** A part of a row its own markup hides (dijit's expand marker "+" sits in a span styled visibility: hidden). */
function styledHidden(el: Element): boolean {
  const style = (el as HTMLElement).style;
  return el.hasAttribute("hidden") || style?.display === "none" || style?.visibility === "hidden";
}

export function optionsOf(popup: Element): OptionRow[] {
  let found: HTMLElement[] = [];
  walkElements(popup, (el) => {
    if (el.matches(OPTION_SEL)) found.push(el as HTMLElement);
  });
  if (!found.length) {
    walkElements(popup, (el) => {
      const cls = classTokens(el);
      if (cls.some((t) => /__option$|__option--/.test(t) && !t.includes("__option-")) || /-option-\d+$/.test(el.getAttribute("id") ?? "") || el.localName === "li") {
        found.push(el as HTMLElement);
      }
    });
  }
  // Menus that list plain children carry no roles at all: the popup's own children are the rows. A menu notice ("Loading...", "No options") is none.
  if (!found.length) found = Array.from(popup.children).filter((c): c is HTMLElement => c instanceof (popup.ownerDocument.defaultView?.HTMLElement ?? HTMLElement) && flatText(c) !== "" && !c.matches("script, style") && !classTokens(c).some((t) => t.includes("__menu-notice")));
  // An option inside a component's shadow root is the clickable one; the host that wraps it is not a second row.
  // Each row's ancestors are walked once, so a long list (Squarespace's 2,557 schools) reads in milliseconds.
  const wrappers = new Set<Element>();
  for (const o of found) for (const a of composedAncestors(o)) wrappers.add(a);
  found = found.filter((el) => !wrappers.has(el));
  const rows: OptionRow[] = [];
  for (const el of found) {
    const label = cleanOptionLabel(flatText(el, styledHidden) || el.getAttribute("aria-label") || "");
    if (!label || isEscapeRow(label)) continue;
    rows.push({
      el,
      label,
      value: el.getAttribute("data-value") ?? el.getAttribute("value") ?? "",
      selected: isSelected(el),
      disabled: el.getAttribute("aria-disabled") === "true",
    });
  }
  return rows;
}

// --- menus of categories ---------------------------------------------------------

/** How a sub-menu's option is written once flattened: "Job Sites > LinkedIn". */
export const PATH_SEP = " > ";
const CHEVRON_TEXT = /\s*[›▸❯>]\s*$/;
const CHEVRON_ICON = /chevron|caret|arrow/i;
const BACK = /^(back\b|back to\b|[←‹<]\s)/i;

/** A row that says it opens a sub-menu: a popup or expanded state, a chevron, caret or arrow icon, or a trailing chevron. */
export function isCategoryRow(el: Element): boolean {
  if (el.hasAttribute("aria-haspopup") || el.hasAttribute("aria-expanded")) return true;
  const icon = Array.from(el.querySelectorAll("*")).some((d) =>
    ["class", "data-icon", "data-uxi-element-id"].some((a) => CHEVRON_ICON.test(d.getAttribute(a) ?? "")),
  );
  return icon || CHEVRON_TEXT.test(collapse(el.getAttribute("aria-label") ?? flatText(el)));
}

/** A row that holds a choice (a radio, a checkbox, a checked state): clicking it commits, so it is never opened to look inside. */
export function isChoiceRow(el: Element): boolean {
  return el.hasAttribute("aria-checked") || !!el.querySelector('input[type=radio], input[type=checkbox], [role=radio], [role=checkbox]');
}

/** A category's label without the chevron that marks it. */
export const categoryLabel = (label: string): string => label.replace(CHEVRON_TEXT, "").trim();

/** The visible Back control of a sub-menu, in the popup or its container. */
export function backControl(popup: Element): HTMLElement | null {
  const scope = popup.closest("[role=dialog], [role=combobox], [data-uxi-element-id]") ?? popup.parentElement ?? popup;
  const all = [...Array.from(popup.querySelectorAll<HTMLElement>("button, [role=button], [role=option]")), ...Array.from(scope.querySelectorAll<HTMLElement>("button, [role=button]"))];
  return all.find((b) => isRendered(b) && BACK.test(collapse(b.getAttribute("aria-label") || flatText(b)))) ?? null;
}

/** Whether a label is a sub-menu's own Back row or the category it repeats. */
export const isBackRow = (label: string, category: string): boolean => BACK.test(label) || /^back$/i.test(label) || collapse(label).toLowerCase() === collapse(category).toLowerCase();

function attrEq(name: string, value: string): string {
  return `[${name}="${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"]`;
}

/** The root of the element and the roots of every shadow host above it. */
function rootChain(el: Element): ParentNode[] {
  const out: ParentNode[] = [];
  let r: Node = el.getRootNode();
  for (;;) {
    out.push(r as ParentNode);
    const host = r.nodeType === 11 ? (r as ShadowRoot).host : null;
    if (!host) break;
    r = host.getRootNode();
  }
  return out;
}

export function byIdDeep(from: Element, id: string): Element | null {
  for (const root of rootChain(from)) {
    const hit = root.querySelector(attrEq("id", id));
    if (hit) return hit;
  }
  return null;
}

/** The trigger, the combobox wrapper around it and the text box inside it (a hidden input, a checkbox or a radio is not the box that names the popup). */
export function triggerFamily(trigger: Element): Element[] {
  const out = new Set<Element>([trigger]);
  const wrapper = trigger.closest("[role=combobox]");
  if (wrapper) out.add(wrapper);
  for (const root of Array.from(out)) {
    const inner = root.querySelector("input:not([type=hidden]):not([type=checkbox]):not([type=radio])");
    if (inner) out.add(inner);
  }
  return Array.from(out);
}

export function reactSelectControl(el: Element): Element | null {
  let n: Element | null = el;
  for (let i = 0; n && i < 6; i++, n = n.parentElement) if (hasClassSuffix(n, "__control")) return n;
  return null;
}

function inListbox(target: Element): Element {
  return target.matches("[role=listbox]") ? target : (target.querySelector("[role=listbox]") ?? target);
}

/** A found popup comes with the option rows the lookup read from it, so a caller reads them once. */
export type PopupLookup = { state: "found"; popup: Element; rows: OptionRow[] } | { state: "waiting" } | { state: "none" };

const waiting: PopupLookup = { state: "waiting" };
const found = (popup: Element, rows: OptionRow[]): PopupLookup => ({ state: "found", popup, rows });

function reactSelectPopup(trigger: Element): PopupLookup {
  const id = trigger.getAttribute("id") ?? "";
  const keyed = /^react-select-(.+)-input$/.exec(id);
  const ids = keyed ? [`react-select-${keyed[1]}-listbox`] : id ? [`react-select-${id}-listbox`, `${id}-list`] : [];
  for (const candidate of ids) {
    const hit = byIdDeep(trigger, candidate);
    const rows = hit ? optionsOf(hit) : [];
    if (hit && rows.length) {
      const popup = inListbox(hit);
      return found(popup, popup === hit ? rows : optionsOf(popup));
    }
  }
  const control = reactSelectControl(trigger);
  const menu = control?.parentElement?.querySelector('[class*="__menu"]');
  const rows = menu ? optionsOf(menu) : [];
  if (menu && rows.length) return found(menu, rows);
  return waiting;
}

function isTriggerLike(el: Element): boolean {
  const role = el.getAttribute("role");
  return role === "combobox" || el.getAttribute("aria-haspopup") === "listbox" || (el.localName === "button" && el.hasAttribute("aria-haspopup"));
}

/** A popup that names another listbox trigger, or that another control points at, is not ours. */
function ownedByOther(popup: Element, family: Element[]): boolean {
  for (const id of (popup.getAttribute("aria-labelledby") ?? "").split(/\s+/).filter(Boolean)) {
    const named = byIdDeep(popup, id);
    if (named && !family.includes(named) && isTriggerLike(named)) return true;
  }
  const own = popup.getAttribute("id");
  if (own) {
    for (const root of rootChain(popup)) {
      for (const el of Array.from(root.querySelectorAll(`${attrEq("aria-controls", own)}, ${attrEq("aria-owns", own)}`))) {
        if (!family.includes(el)) return true;
      }
    }
  }
  return false;
}

function isChipStrip(popup: Element): boolean {
  // Workday's list of a prompt's chosen items marks each pill role=option; it
  // is never the popup of another control.
  if (popup.getAttribute("data-automation-id") === "selectedItemList") return true;
  const marks = `${popup.getAttribute("data-automation-id") ?? ""} ${popup.getAttribute("class") ?? ""}`;
  return /selecteditemlist|selected-items|chip|pill/i.test(marks) && optionsOf(popup).every((o) => !o.el.hasAttribute("role"));
}

/** Every rendered listbox in the document that could be a popup, in document order. */
export function visibleListboxes(doc: Document): Element[] {
  const out: Element[] = [];
  walkElements(doc.body ?? doc, (el) => {
    if (!el.matches("[role=listbox], [role=menu]")) return;
    if (el.getAttribute("aria-hidden") === "true" || el.closest(".iti__hide, [aria-hidden=true]")) return;
    if (!isRendered(el)) return;
    out.push(el);
  });
  return out;
}

/**
 * The popup of a trigger. A named popup (aria-controls, aria-owns, a menu id,
 * a react-select id) is the only candidate: when that reference exists but its
 * target is missing or empty the answer is "waiting". Without a reference an
 * id-less popup that names the trigger wins, then the last rendered listbox
 * that is not a chip strip and belongs to no other control.
 */
export function popupFor(trigger: Element, doc: Document): PopupLookup {
  const family = triggerFamily(trigger);
  const refs = family.flatMap((f) => [f.getAttribute("aria-controls"), f.getAttribute("aria-owns")]).flatMap((v) => (v ?? "").split(/\s+/).filter(Boolean));
  if (refs.length) {
    for (const id of refs) {
      const hit = byIdDeep(trigger, id);
      if (!hit) continue;
      const popup = inListbox(hit);
      const rows = optionsOf(popup);
      if (rows.length && isRendered(popup)) return found(popup, rows);
    }
    return waiting;
  }
  const menuId = trigger.getAttribute("data-menu-id");
  if (menuId) {
    const hit = byIdDeep(trigger, menuId);
    const rows = hit ? optionsOf(hit) : [];
    return hit && rows.length ? found(hit, rows) : waiting;
  }
  if (reactSelectControl(trigger)) return reactSelectPopup(trigger);

  const listboxes = visibleListboxes(doc);
  const own = trigger.getAttribute("id");
  if (own) {
    const named = listboxes.find((l) => (l.getAttribute("aria-labelledby") ?? "").split(/\s+/).includes(own));
    const rows = named ? optionsOf(named) : [];
    if (named && rows.length) return found(named, rows);
  }
  // The last one that qualifies: looked for from the end, so the options of the listboxes before it are never read.
  for (let i = listboxes.length - 1; i >= 0; i--) {
    const l = listboxes[i] as Element;
    if (isChipStrip(l) || ownedByOther(l, family)) continue;
    const rows = optionsOf(l);
    if (rows.length) return found(l, rows);
  }
  return { state: "none" };
}

/** The trigger names a popup that is open but offers no options: it depends on something not chosen yet. */
export function hasEmptyPopup(trigger: Element): boolean {
  const refs = triggerFamily(trigger).flatMap((f) => [f.getAttribute("aria-controls"), f.getAttribute("aria-owns")]).flatMap((v) => (v ?? "").split(/\s+/).filter(Boolean));
  return refs.some((id) => {
    const hit = byIdDeep(trigger, id);
    return !!hit && isRendered(hit) && optionsOf(inListbox(hit)).length === 0;
  });
}

/**
 * The search box an open popup carries for the trigger's listbox while that
 * list offers nothing (Nebius's Gravity UI city lookup lists nothing until a
 * city is typed, and soon unmounts the empty list for a "Start typing" hint):
 * a rendered text box, outside the trigger, whose aria-controls names the
 * same listbox.
 */
export function popupSearchBox(trigger: Element): HTMLInputElement | null {
  const family = triggerFamily(trigger);
  const refs = family.flatMap((f) => [f.getAttribute("aria-controls"), f.getAttribute("aria-owns")]).flatMap((v) => (v ?? "").split(/\s+/).filter(Boolean));
  for (const id of refs) {
    const hit = byIdDeep(trigger, id);
    if (hit && isRendered(hit) && optionsOf(inListbox(hit)).length) continue;
    for (const root of rootChain(trigger)) {
      for (const box of Array.from(root.querySelectorAll<HTMLInputElement>(`input${attrEq("aria-controls", id)}`))) {
        if (!family.includes(box) && isRendered(box)) return box;
      }
    }
  }
  return null;
}

/** Rendered, populated listboxes that are open right now, whoever owns them. */
export function populatedPopups(doc: Document): Element[] {
  return visibleListboxes(doc).filter((l) => !isChipStrip(l) && optionsOf(l).length > 0);
}
