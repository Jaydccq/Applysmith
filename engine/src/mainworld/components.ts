// Clean-room: docs/provenance.md#CR-ENGINE
// Calls into page objects: a React element's own handler, a web component's
// own method, and a TinyMCE editor's content. Events are ignored by these
// widgets, so the value goes in through the object the page itself wired.

import { isUnsafeLabel } from "../guard-text";

type W = Window & typeof globalThis;

const SHADOW = " >>> ";

/** The element a selector of the engine names, through the open shadow roots it steps into; null when it names none or is no selector. */
export function resolve(win: W, selector: string): Element | null {
  let roots: ParentNode[] = [win.document];
  const parts = selector.split(SHADOW);
  for (const [i, part] of parts.entries()) {
    const found: Element[] = [];
    for (const r of roots) {
      try {
        const hit = r.querySelector(part);
        if (hit) found.push(hit);
      } catch {
        return null;
      }
    }
    if (i === parts.length - 1) return found[0] ?? null;
    roots = found.map((f) => f.shadowRoot).filter((s): s is ShadowRoot => !!s);
    if (!roots.length) return null;
  }
  return null;
}

function propsOf(el: Element): Record<string, unknown> | null {
  const key = Object.keys(el).find((k) => k.startsWith("__reactProps$"));
  if (key) return (el as unknown as Record<string, Record<string, unknown>>)[key] ?? null;
  const fiberKey = Object.keys(el).find((k) => k.startsWith("__reactFiber$"));
  const fiber = fiberKey ? (el as unknown as Record<string, { memoizedProps?: Record<string, unknown> }>)[fiberKey] : null;
  return fiber?.memoizedProps ?? null;
}

const BUTTON_INPUT = /^(submit|button|reset|image)$/i;

/**
 * What the target is called, read here because this is the one place a page
 * handler is called from: no press reaches the page's own code at a control
 * the engine's click guard would refuse. A text box's value is what was typed
 * into it, not its name, so only a button-typed input is read by its value.
 */
const POPUP = "[role=listbox], [role=menu], [role=dialog], .rw-popup-container, .rw-list";

/** The element's own text: what a popup it hosts lists is not its name. */
function ownText(el: Element): string {
  let out = "";
  for (const n of Array.from(el.childNodes)) {
    if (n.nodeType === 3) out += n.textContent ?? "";
    else if (n.nodeType === 1 && !(n as Element).matches(POPUP)) out += ` ${ownText(n as Element)} `;
  }
  return out;
}

/** The text of the elements an aria-labelledby names, and the aria-labels nested in the control outside a popup it hosts. */
function referencedNames(el: Element): string[] {
  const doc = el.ownerDocument;
  const named = (el.getAttribute("aria-labelledby") ?? "").split(/\s+/).filter(Boolean).map((id) => doc.getElementById(id)?.textContent ?? "");
  const nested = Array.from(el.querySelectorAll("[aria-label]"))
    .filter((n) => !n.parentElement?.closest(POPUP) || !el.contains(n.parentElement.closest(POPUP)))
    .map((n) => n.getAttribute("aria-label") ?? "");
  return [...named, ...nested];
}

function labelOf(el: Element): string {
  const input = el as HTMLInputElement;
  const typed = el.localName === "input" && !BUTTON_INPUT.test(input.type);
  const named = Array.from((input.labels ?? []) as ArrayLike<Element>).map((l) => l.textContent);
  return [el.getAttribute("aria-label"), ...referencedNames(el), el.getAttribute("label"), ownText(el), el.getAttribute("title"), ...named, typed ? null : input.value]
    .filter((p): p is string => typeof p === "string" && p.trim() !== "")
    .join(" ");
}

/** Calls the React handler prop of an element with a synthetic event that carries persist(), because some handlers throw without it. */
export function callReact(win: W, selector: string, prop: string, event: Record<string, unknown>): { called: boolean; reason?: string } {
  const el = resolve(win, selector);
  if (!el) return { called: false, reason: "no-element" };
  if (isUnsafeLabel(labelOf(el))) return { called: false, reason: "unsafe-click" };
  const handler = propsOf(el)?.[prop];
  if (typeof handler !== "function") return { called: false, reason: "no-handler" };
  const fake = {
    type: prop.replace(/^on/, "").toLowerCase(),
    target: el,
    currentTarget: el,
    nativeEvent: {},
    persist() {},
    preventDefault() {},
    stopPropagation() {},
    isDefaultPrevented: () => false,
    isPropagationStopped: () => false,
    ...event,
  };
  try {
    (handler as (e: unknown) => void)(fake);
  } catch (e) {
    return { called: false, reason: e instanceof Error ? e.message : String(e) };
  }
  return { called: true };
}

/** Calls a method of a web component and waits for it, bounded: some validators never settle. */
export async function callComponent(win: W, selector: string, method: string, args: unknown[] | undefined, timeoutMs: number): Promise<{ called: boolean; settled: boolean }> {
  const el = resolve(win, selector) as (Element & Record<string, unknown>) | null;
  const fn = el?.[method];
  if (!el || typeof fn !== "function") throw new Error("no-method");
  if (isUnsafeLabel(labelOf(el))) throw new Error("unsafe-click");
  // A date crosses the channel as text; the component wants the object, in local time with no zone shift.
  const real = (args ?? []).map((v) => {
    const d = v && typeof v === "object" ? (v as { $date?: string }).$date : undefined;
    if (!d) return v;
    const [y, m, day] = d.split("-").map(Number) as [number, number, number];
    return new Date(y, m - 1, day);
  });
  const out = (fn as (...a: unknown[]) => unknown).apply(el, real);
  let timer: ReturnType<typeof setTimeout> | undefined;
  const settled = await Promise.race([
    Promise.resolve(out).then(() => true, () => true),
    new Promise<boolean>((r) => {
      timer = setTimeout(() => r(false), timeoutMs);
    }),
  ]);
  clearTimeout(timer);
  return { called: true, settled };
}

const fold = (v: unknown): string => (typeof v === "string" ? v.replace(/\s+/g, " ").trim().toLowerCase() : "");

/**
 * An sdf-select-simple's own commit, for a pane whose option takes no click: the
 * item the component itself lists is set as its value and announced with its own
 * change event, the way its item handler does. The item is found among the
 * component's own items by the value (or, when the option has none, the label)
 * the answer bound to, exactly and uniquely; nothing else is ever written.
 */
export function commitSdf(win: W, selector: string, value: string, label: string): { committed: boolean; reason?: string } {
  const el = resolve(win, selector) as (Element & { items?: unknown; value?: unknown }) | null;
  if (!el || el.localName !== "sdf-select-simple") return { committed: false, reason: "no-element" };
  if (isUnsafeLabel(labelOf(el))) return { committed: false, reason: "unsafe-click" };
  const items = Array.isArray(el.items) ? (el.items as unknown[]) : [];
  const byValue = fold(value) !== "";
  const hits = items.filter((it) => {
    if (!it || typeof it !== "object" || (it as { disabled?: unknown }).disabled === true) return false;
    const { value: v, label: l } = it as { value?: unknown; label?: unknown };
    return byValue ? fold(v) === fold(value) : fold(l) === fold(label) && fold(label) !== "";
  });
  if (hits.length !== 1) return { committed: false, reason: "not-an-option" };
  el.value = hits[0];
  el.dispatchEvent(new win.CustomEvent("sdfChange", { detail: hits[0], bubbles: true, composed: true, cancelable: true }));
  return { committed: true };
}

type TinyEditor = {
  setContent(html: string): unknown;
  save(): unknown;
  getContent(args: { format: string }): unknown;
  dispatch?: (name: string) => unknown;
  fire?: (name: string) => unknown;
};

/**
 * The content of the TinyMCE editor of a textarea (the editor's id is the
 * textarea's), set from plain text: one paragraph per line, each line set as
 * text so nothing in it becomes markup. The editor saves it into its textarea
 * and tells the page's own listeners with its input and change events; the
 * editor's own text is the read-back.
 */
export function setTinymce(win: W, id: string, text: string): { text: string } {
  const ed = (win as unknown as { tinymce?: { get?: (id: string) => TinyEditor | null } }).tinymce?.get?.(id);
  if (!ed || typeof ed.setContent !== "function") throw new Error("no-editor");
  const box = win.document.createElement("div");
  for (const line of text.split(/\r?\n/)) if (line.trim() !== "") box.appendChild(win.document.createElement("p")).textContent = line;
  ed.setContent(box.innerHTML);
  ed.save();
  const emit = (ed.dispatch ?? ed.fire)?.bind(ed);
  try {
    emit?.("input");
    emit?.("change");
  } catch {
    /* a page listener that throws does not undo the content: the read-back decides */
  }
  return { text: String(ed.getContent({ format: "text" }) ?? "") };
}

/** What an spl-autocomplete holds and lists; `value` is what it holds as text, to tell a new selection from the one it held; `custom` is its "use the typed text" option id, empty when it has none; `list` its fixed choices, empty for a box that takes typed text. */
export type SrSearchState = { loading: boolean; hasValue: boolean; value: string; custom: string; options: { id: string; label: string }[]; list: { id: string; label: string }[] };

function asText(v: unknown): string {
  try {
    return JSON.stringify(v) ?? "";
  } catch {
    return String(v);
  }
}

/** An spl-autocomplete's own state (the value is the component's); `close` hides its menu, which puts back its value's text. */
export function srSearch(win: W, selector: string, close: boolean): SrSearchState {
  const el = resolve(win, selector) as (Element & Record<string, unknown>) | null;
  if (el?.localName !== "spl-autocomplete") throw new Error("no-element");
  if (close) (el.dropdown as { hide?: () => void } | undefined)?.hide?.();
  const v = el.value;
  const opts = Array.isArray(el._optionsToRender) ? (el._optionsToRender as { id?: unknown; label?: unknown }[]) : [];
  const list = el.allowCustomValues !== true && Array.isArray(el.defaultOptions) ? (el.defaultOptions as { id?: unknown; label?: unknown }[]) : [];
  return {
    loading: el.isLoading === true,
    hasValue: v != null && v !== "" && !(Array.isArray(v) && !v.length),
    value: asText(v),
    custom: el.allowCustomValues === true ? String(el.customValueOptionId ?? "") : "",
    options: opts.map((o) => ({ id: String(o?.id ?? ""), label: String(o?.label ?? "") })),
    list: list.map((o) => ({ id: String(o?.id ?? ""), label: String(o?.label ?? "") })),
  };
}

/** A SmartRecruiters form element's own verdict on its value; `pending` while an async validator runs. */
export function srValidity(win: W, selector: string): { pending: boolean; valid: boolean; message: string } {
  const el = resolve(win, selector) as (Element & Record<string, unknown>) | null;
  if (!el) throw new Error("no-element");
  const err = el.error as { errorMessage?: unknown } | null | undefined;
  return { pending: el._isValidationPending === true, valid: el.valid !== false, message: String(err?.errorMessage ?? "") };
}
