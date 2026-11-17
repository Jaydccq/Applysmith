// Clean-room: docs/provenance.md#CR-ENGINE
// What a person does to a widget: the full pointer sequence, key presses,
// typing one key at a time, and touching a control so the page notices.

import { clickLabel, isEditorSave, isUnsafeLabel, isUnsafeListTriggerLabel, isUnsafeOptionLabel, isUnsafeRemoval } from "./click-guard";
import { labelElements } from "./labels";
import type { WriteCtx } from "./env";
import type { Row } from "./protocol";
import { isRendered } from "./visibility";
import { collapse, fire, fireInput, flatText, fireKey, nativeValueGetter, nativeValueSetter, winOf } from "./dom";

export interface PointerOptions {
  /** Dispatch the trailing click. Some comboboxes toggle shut on it. */
  click?: boolean;
  /** A removal press: the control is a delete control and must sit inside this row. */
  removeWithin?: Element;
  /** A save press: the control must be exactly "Save" and sit inside this open row editor. */
  saveWithin?: Element;
  /** The element the events go to when it is not the guarded control itself (a web component's inner button). */
  target?: HTMLElement;
}

/** A box a person types into: a textarea, or an input of a text type. */
function isTextBox(el: Element): boolean {
  return el.localName === "textarea" || (el.localName === "input" && /^(?:|text|search|email|tel|url|number)$/.test((el.getAttribute("type") ?? "").toLowerCase()));
}

/** A control that opens a list and submits no form: a combobox or a listbox popup trigger that is not a submit button. */
function isListTrigger(el: Element): boolean {
  if (el.getAttribute("aria-haspopup") !== "listbox" && el.getAttribute("role") !== "combobox") return false;
  return !((el.localName === "button" || el.localName === "input") && (el as HTMLButtonElement).type === "submit");
}

/** The label rule for a press on el: a list's trigger and an option of an open list are read by their own rules. */
function unsafeFor(el: Element, label: string): boolean {
  if (isListTrigger(el)) return isUnsafeListTriggerLabel(label);
  if (el.getAttribute("role") === "option") return isUnsafeOptionLabel(label);
  return isUnsafeLabel(label);
}

/**
 * Pointer down, mouse down, pointer up, mouse up and click at the element's
 * centre with click detail 1: a bare click can leave aria-expanded false, and
 * some libraries ignore a click with detail 0. The target passes the click
 * guard first, so nothing is dispatched at a destructive or terminal control.
 */
export function pointerSequence(el: HTMLElement, opts: PointerOptions = {}): "ok" | "unsafe-click" {
  const label = clickLabel(el);
  // A text box's label is its question ("Can you submit verification of your
  // right to work?", Eightfold): a click on it opens or focuses, never submits.
  // A list trigger's label is its question too; opening the list advances nothing.
  const refused = opts.removeWithin
    ? isUnsafeRemoval(label, opts.removeWithin, el)
    : opts.saveWithin
      ? !isEditorSave(el, opts.saveWithin)
      : !isTextBox(el) && unsafeFor(el, label);
  if (refused) return "unsafe-click";
  const at = opts.target ?? el;
  const win = winOf(at);
  const r = at.getBoundingClientRect();
  const base = {
    bubbles: true,
    cancelable: true,
    composed: true,
    clientX: r.left + r.width / 2,
    clientY: r.top + r.height / 2,
    button: 0,
    view: win,
  };
  const pointer = (type: string, buttons: number) => {
    const Ctor = (win as unknown as { PointerEvent?: typeof PointerEvent }).PointerEvent ?? win.MouseEvent;
    at.dispatchEvent(new Ctor(type, { ...base, buttons, detail: 0, pointerType: "mouse", isPrimary: true } as PointerEventInit));
  };
  const mouse = (type: string, buttons: number, detail: number) => at.dispatchEvent(new win.MouseEvent(type, { ...base, buttons, detail }));
  pointer("pointerdown", 1);
  mouse("mousedown", 1, 1);
  pointer("pointerup", 0);
  mouse("mouseup", 0, 1);
  if (opts.click !== false) mouse("click", 0, 1);
  return "ok";
}

/**
 * Presses the Save control that is a descendant of an open repeatable-row
 * editor, and nothing else: the editor is never widened to a section or the
 * page. "no-save" leaves the editor for the person.
 */
export function pressEditorSave(editor: Element): "ok" | "unsafe-click" | "no-save" {
  const save = Array.from(editor.querySelectorAll<HTMLElement>("button, [role=button], spl-button")).find((b) => isRendered(b) && isEditorSave(b, editor));
  return save ? pointerSequence(save, { saveWithin: editor, target: innerButton(save) }) : "no-save";
}

/** The button a SmartRecruiters spl-button draws in its shadow root, else the control itself. */
export function innerButton(el: HTMLElement): HTMLElement {
  return el.shadowRoot?.querySelector<HTMLElement>("button") ?? el;
}

/** Hover: some grids commit only after the pointer rested on the row. */
export function hover(el: HTMLElement): void {
  const win = winOf(el);
  for (const type of ["pointerover", "mouseover", "mouseenter", "mousemove"]) {
    el.dispatchEvent(new win.MouseEvent(type, { bubbles: true, composed: true, view: win }));
  }
}

/** Key down and key up on an element; false when the page cancelled the key down. */
export function press(el: Element, key: string): boolean {
  const go = fireKey(el, "keydown", key);
  fireKey(el, "keyup", key);
  return go;
}

const BUTTON_INPUT = /^(submit|button|reset|image)$/i;

/**
 * Enter or Space on an element can press it, so it passes the same label check
 * as a click (a list trigger's question included). A text box's own value is what was typed into it, not what it is
 * called, so only a button-typed input is read by its value.
 */
export function safePress(el: Element, key: "Enter" | " "): "ok" | "unsafe-click" {
  const typed = el.localName === "input" && !BUTTON_INPUT.test((el as HTMLInputElement).type);
  const label = typed ? collapse([el.getAttribute("aria-label"), ...labelElements(el).map((l) => flatText(l)), el.getAttribute("title")].filter(Boolean).join(" ")) : clickLabel(el);
  if (unsafeFor(el, label)) return "unsafe-click";
  press(el, key);
  return "ok";
}

/** Focus the element and tell the page about it. */
export function focusEl(el: HTMLElement): void {
  el.focus();
  fire(el, "focusin");
}

/**
 * Take focus off whatever holds it and give the page a turn before another
 * control takes it. A component that re-renders on a blur is mid-commit for
 * the rest of that task; ADP's address box reads its own ref in a focus handler,
 * throws there, and the page unmounts the whole form.
 */
export async function releaseFocus(el: Element, ctx: Pick<WriteCtx, "sleep" | "timing">): Promise<void> {
  const held = el.ownerDocument.activeElement as HTMLElement | null;
  if (held && held !== el) held.blur();
  await ctx.sleep(ctx.timing.tapMs);
}

/** How a written control is committed by a blur: a filled text box, a closed-listbox button, or not at all. */
export function blurKind(kind: Row["kind"], el: HTMLInputElement): "text" | "button" | null {
  if (kind === "custom-listbox") return el.localName === "button" ? "button" : null;
  if (kind !== "text" && kind !== "textarea") return null;
  // A date's sub-inputs commit on their own gestures, and an empty box has nothing to validate.
  if (/dateSection/i.test(el.getAttribute("id") ?? "") || el.getAttribute("data-automation-id") === "dateInputWrapper" || el.value === "") return null;
  return "text";
}

/** Focus, rest briefly, blur: the page's validators leave their untouched state. */
export async function tap(el: HTMLElement, ctx: WriteCtx): Promise<void> {
  focusEl(el);
  await ctx.sleep(ctx.timing.tapMs);
  fire(el, "focusout");
  el.blur();
}

export interface TypeOptions {
  /** Pause between keys. */
  gapMs?: number;
}

/**
 * Types a text one key at a time: key down, native setter, an input event
 * carrying the key, key up. The control keeps focus; a blur would close the
 * menu of a search widget. Clears first with a delete input type.
 */
export async function typeText(el: HTMLInputElement | HTMLTextAreaElement, text: string, ctx: WriteCtx, opts: TypeOptions = {}): Promise<void> {
  const set = nativeValueSetter(el);
  if (!set) return;
  const get = nativeValueGetter(el);
  focusEl(el);
  if (get() !== "") {
    set("");
    fireInput(el, null, "deleteContentBackward");
  }
  let cur = "";
  for (const ch of text) {
    await ctx.checkpoint();
    fireKey(el, "keydown", ch);
    cur += ch;
    set(cur);
    fireInput(el, ch, "insertText");
    fireKey(el, "keyup", ch);
    if (opts.gapMs) await ctx.sleep(opts.gapMs);
  }
}

/** Replace the whole text at once and tell the page, as a paste would. */
export function replaceText(el: HTMLInputElement | HTMLTextAreaElement, text: string): void {
  nativeValueSetter(el)?.(text);
  fireInput(el, text, "insertReplacementText");
}

/** Empty an input and tell the page, so no search text is left dangling. */
export function clearText(el: HTMLInputElement | HTMLTextAreaElement): void {
  nativeValueSetter(el)?.("");
  fireInput(el, null, "deleteContentBackward");
  fire(el, "change");
}
