// Clean-room: docs/provenance.md#CR-ENGINE
// What a custom widget currently shows as its committed value. The widget's
// own display is the truth: an input's text is only a search box.

import { collapse, flatText, walkElements } from "./dom";
import { classTokens, hasClassSuffix, reactSelectControl } from "./popup";
import { dijitHasChoice, isDijitSelect, isSdfSelect, sdfDisplay } from "./libraries";
import { isPlaceholderText } from "./placeholder";
import { isPctySelect, isRwDropdown, pctyDisplay, rwDisplay } from "./reactwidgets";
import { isWorkdayPrompt, pillTexts } from "./workday";

const OVERFLOW_TAG = /^\+\s*\d+/;

function textOf(el: Element): string {
  return collapse(el.getAttribute("title") ?? "") || collapse(flatText(el));
}

/** Texts of the matching elements. A "+N" chip is a multi-value overflow counter unless the caller reads one single value, where "+1" is a dial code. */
function tokens(root: Element, match: (el: Element) => boolean, single = false): string[] {
  const out: string[] = [];
  const visit = (el: Element) => {
    if (match(el)) out.push(textOf(el));
  };
  walkElements(root, visit);
  // The walk starts below the root, so a root that is itself a shadow host is entered here.
  const own = (root as HTMLElement).shadowRoot;
  if (own) walkElements(own, visit);
  return out.filter((t) => t !== "" && (single || !OVERFLOW_TAG.test(t)));
}

function reactSelectValue(control: Element): string[] {
  const single = tokens(control, (e) => hasClassSuffix(e, "__single-value"), true);
  if (single.length) return single;
  return tokens(control, (e) => hasClassSuffix(e, "__multi-value__label"));
}

const EMOTION_CONTROL = /^css-[a-z0-9]+-control$/;
const EMOTION_SINGLE = /^css-[a-z0-9]+-singleValue$/;

/** A react-select built without class names of its own carries generated ones ("css-<hash>-control"): the same widget, found by those. */
function emotionControl(el: Element): Element | null {
  let n: Element | null = el;
  for (let i = 0; n && i < 6; i++, n = n.parentElement) if (classTokens(n).some((t) => EMOTION_CONTROL.test(t))) return n;
  return null;
}

function antdValue(root: Element): string[] {
  return tokens(root, (e) => classTokens(e).includes("ant-select-selection-item"));
}

function select2Value(container: Element): string[] {
  const chips = tokens(container, (e) => classTokens(e).includes("select2-selection__choice"));
  // Select2 draws its remove control as "×": a label's own leading "x" (xUnit, x86) is part of the label.
  if (chips.length) return chips.map((c) => c.replace(/^×\s*/, ""));
  return tokens(container, (e) => classTokens(e).includes("select2-selection__rendered"));
}

const CHIP = /(^|[-_])(chip|tag|token|pill)([-_]|$)/i;
const CHIP_BOX = /(^|[-_])(tags|tag-input|chips|tokens|token-input)([-_]|$)/i;

/** The box that holds a generic chip input's chips. */
function chipBox(el: Element): Element | null {
  for (let n: Element | null = el, i = 0; n && i < 4; n = n.parentElement, i++) {
    if (classTokens(n).some((t) => CHIP_BOX.test(t))) return n;
  }
  return null;
}

function tagHost(el: Element): Element | null {
  if (el.localName === "spl-tags") return el;
  return el.closest("spl-tags") ?? el.querySelector("spl-tags") ?? null;
}

/**
 * The committed values of a widget element (its trigger, wrapper or inner
 * input), one per chip, or null when the element is not a widget the engine
 * knows. An empty list means nothing is committed; a placeholder never counts.
 */
export function committedTokens(el: Element): string[] | null {
  // A Workday prompt clears its search text on commit; the selected pills are the value.
  if (isWorkdayPrompt(el)) return pillTexts(el);
  if (isRwDropdown(el)) return rwDisplay(el) ? [rwDisplay(el)] : [];
  if (isPctySelect(el)) return pctyDisplay(el) ? [pctyDisplay(el)] : [];
  const antd = el.closest(".ant-select") ?? (classTokens(el).includes("ant-select") ? el : null);
  if (antd) return antdValue(antd);

  const s2 = el.closest(".select2-container");
  if (s2) return select2Value(s2);

  if (el.localName === "input" && classTokens(el).includes("cx-select-input")) {
    if (el.getAttribute("aria-expanded") === "true" || el.getAttribute("aria-invalid") === "true") return [];
    const v = collapse((el as HTMLInputElement).value);
    return v ? [v] : [];
  }

  const control = reactSelectControl(el);
  if (control) {
    // What the person is typing wins over the committed label.
    if (el.localName === "input" && (el as HTMLInputElement).value !== "") return [collapse((el as HTMLInputElement).value)];
    return reactSelectValue(control);
  }

  const emotion = el.localName === "input" || el.getAttribute("role") === "combobox" ? emotionControl(el) : null;
  if (emotion) {
    if (el.localName === "input" && (el as HTMLInputElement).value !== "") return [collapse((el as HTMLInputElement).value)];
    return tokens(emotion, (e) => classTokens(e).some((t) => EMOTION_SINGLE.test(t)));
  }

  if (isDijitSelect(el)) {
    // The hidden input is the truth; the displayed text is cosmetic.
    if (!dijitHasChoice(el)) return [];
    const shown = collapse(el.querySelector(".dijitButtonText")?.textContent ?? "").replace(/\u200b/g, "");
    const code = (el.parentElement?.querySelector('input[type="hidden"]') as HTMLInputElement | null)?.value ?? "";
    return [isPlaceholderText(shown) ? code : shown];
  }

  if (isSdfSelect(el)) {
    const shown = sdfDisplay(el);
    return shown ? [shown] : [];
  }

  const tags = tagHost(el);
  if (tags) return tokens(tags, (e) => e.localName === "spl-tag" || classTokens(e).includes("tag-label"));
  const box = chipBox(el);
  if (box) return tokens(box, (e) => e !== box && !e.querySelector("input") && classTokens(e).some((t) => CHIP.test(t)) && !classTokens(e).some((t) => /remove|close|delete/.test(t)));
  return null;
}

export function committedDisplay(el: Element): string | null {
  return committedTokens(el)?.join(", ") ?? null;
}

/**
 * True while the search box of a React-Select (or its Emotion-styled copy) holds typed text. Such a control shows its
 * choice in the value container and clears the box when a choice commits, so text in the box is a search: it proves
 * no answer, even when it spells the option.
 */
export function holdsSearchText(el: Element): boolean {
  if (el.localName !== "input" || (el as HTMLInputElement).value === "") return false;
  return reactSelectControl(el) !== null || emotionControl(el) !== null;
}

/** Display of a listbox trigger as a person sees it: placeholders read as empty. */
export function triggerDisplay(el: Element): string {
  const widget = committedDisplay(el);
  let raw: string;
  if (widget !== null) raw = widget;
  else if (el.localName === "input" || el.localName === "textarea") raw = collapse((el as HTMLInputElement).value);
  else {
    // A combobox wrapper shows what its inner text box holds.
    const inner = el.querySelector<HTMLInputElement>("input:not([type=hidden])");
    raw = inner && inner.value !== "" ? collapse(inner.value) : collapse(flatText(el, (e) => e.matches("[role=listbox], [role=menu]")));
    if (raw === "") raw = overlaidText(el);
  }
  return isPlaceholderText(raw) ? "" : raw;
}

/**
 * The text an empty toggle button is drawn over: Duolingo's box holds one span that shows the choice, a chevron and
 * the button. A box with a label, a legend, another field or more than one text holds no such display.
 */
function overlaidText(el: Element): string {
  const box = el.parentElement;
  if (el.localName !== "button" || !box || box.querySelector("label, legend, input:not([type=hidden]), select, textarea") || box.querySelectorAll("button").length !== 1) return "";
  const texts = Array.from(box.children).filter((c) => c !== el && collapse(c.textContent ?? "") !== "");
  return texts.length === 1 && texts[0].localName === "span" ? collapse(texts[0].textContent ?? "") : "";
}

/** True for an input that is the search box of a widget, not a plain text field. */
export function isWidgetInput(el: Element): boolean {
  return (
    el.getAttribute("role") === "combobox" ||
    el.hasAttribute("aria-haspopup") ||
    reactSelectControl(el) !== null ||
    isWorkdayPrompt(el) ||
    isPctySelect(el) ||
    classTokens(el).includes("cx-select-input") ||
    el.closest(".ant-select, .select2-container") !== null
  );
}
