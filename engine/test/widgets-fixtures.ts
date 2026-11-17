// Clean-room: docs/provenance.md#CR-ENGINE
// Synthetic widgets that reproduce the DOM and event shape of the widget
// families the drivers handle. Fictional content only.

export interface Menu {
  trigger: HTMLElement;
  list: HTMLElement;
  /** What the trigger shows. */
  shown: () => string;
  /** Options activated by a click, in order. */
  clicked: string[];
  open: () => void;
  close: () => void;
  /** The page blanks the widget: placeholder text back, nothing selected. */
  reset: () => void;
}

export type OpenOn = "sequence" | "click" | "arrow";

export interface MenuOptions {
  id: string;
  label: string;
  options: string[];
  openOn?: OpenOn;
  /** Name the popup with aria-controls. Default true. */
  ref?: boolean;
  tag?: "button" | "div";
  placeholder?: string;
  /** Options respond to clicks. When false only the keyboard commits. */
  clickable?: boolean;
  /** Arrow keys and Enter move through and commit options. Default true. */
  keyboard?: boolean;
  parent?: HTMLElement;
}

const KEY = (e: Event) => (e as KeyboardEvent).key;

/** A button-triggered listbox with an ARIA popup. */
export function mountMenu(o: MenuOptions): Menu {
  const parent = o.parent ?? document.body;
  const wrap = document.createElement("div");
  const placeholder = o.placeholder ?? "Select one";
  wrap.innerHTML = `<label id="${o.id}-lbl" for="${o.id}">${o.label}</label>`;
  const trigger = document.createElement(o.tag ?? "button");
  trigger.id = o.id;
  trigger.setAttribute("role", "combobox");
  trigger.setAttribute("aria-haspopup", "listbox");
  trigger.setAttribute("aria-expanded", "false");
  trigger.setAttribute("aria-labelledby", `${o.id}-lbl`);
  if (o.tag !== "div") trigger.setAttribute("type", "button");
  if (o.ref !== false) trigger.setAttribute("aria-controls", `${o.id}-list`);
  trigger.textContent = placeholder;
  const list = document.createElement("ul");
  list.id = `${o.id}-list`;
  list.setAttribute("role", "listbox");
  list.hidden = true;
  const clicked: string[] = [];
  let active = -1;
  const rows: HTMLElement[] = [];
  const pick = (label: string) => {
    clicked.push(label);
    trigger.textContent = label;
    rows.forEach((r) => r.setAttribute("aria-selected", String(r.textContent === label)));
    close();
  };
  for (const [i, text] of o.options.entries()) {
    const li = document.createElement("li");
    li.id = `${o.id}-opt-${i}`;
    li.setAttribute("role", "option");
    li.setAttribute("aria-selected", "false");
    li.textContent = text;
    if (o.clickable !== false) li.addEventListener("click", () => pick(text));
    list.appendChild(li);
    rows.push(li);
  }
  const open = () => {
    list.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
  };
  function close() {
    list.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    trigger.removeAttribute("aria-activedescendant");
    active = -1;
  }
  const seen = new Set<string>();
  for (const t of ["pointerdown", "mousedown", "pointerup", "mouseup"]) trigger.addEventListener(t, () => seen.add(t));
  trigger.addEventListener("click", (e) => {
    const full = ["pointerdown", "mousedown", "pointerup", "mouseup"].every((t) => seen.has(t));
    seen.clear();
    if (o.openOn === "arrow") return;
    if (o.openOn === "sequence" && !(full && (e as MouseEvent).detail === 1)) return;
    if (list.hidden) open();
    else close();
  });
  trigger.addEventListener("keydown", (e) => {
    if (KEY(e) === "Escape") close();
    if (o.keyboard === false) return;
    if (KEY(e) === "ArrowDown") {
      if (list.hidden) {
        if (o.openOn === "arrow" || o.openOn === undefined) open();
        return;
      }
      if (!rows.length) return;
      active = Math.min(rows.length - 1, active + 1);
      trigger.setAttribute("aria-activedescendant", (rows[active] as HTMLElement).id);
    }
    if (KEY(e) === "Enter" && active >= 0) pick((rows[active] as HTMLElement).textContent ?? "");
  });
  wrap.appendChild(trigger);
  wrap.appendChild(list);
  parent.appendChild(wrap);
  const reset = () => {
    trigger.textContent = placeholder;
    rows.forEach((r) => r.setAttribute("aria-selected", "false"));
  };
  return { trigger, list, shown: () => trigger.textContent ?? "", clicked, open, close, reset };
}

export interface Combobox {
  input: HTMLInputElement;
  list: HTMLElement;
  /** Option labels activated by a click, in order. */
  clicked: string[];
  /** Every key event the input received, as "type:key". */
  keys: string[];
  /** Queries the suggestion function was asked. */
  queries: string[];
  chosen: () => string;
}

export interface ComboboxOptions {
  id: string;
  label: string;
  /** Suggestions for the text typed so far. */
  suggest: (query: string) => string[] | Promise<string[]>;
  /** Search on key down (some widgets) instead of on input. */
  searchOn?: "input" | "keydown";
  autocomplete?: "list" | "both";
  /** Wrap the input in a div[role=combobox] (ARIA 1.1). */
  wrapper?: boolean;
  minQuery?: number;
  parent?: HTMLElement;
  /** Draw the suggestion list this many ms after the query. */
  delayMs?: number;
  /**
   * Ashby's place search: the first suggestion is highlighted with
   * aria-selected, ArrowDown moves the highlight, Enter commits it, and a
   * click commits nothing.
   */
  highlightFirst?: boolean;
  /**
   * Ashby's value select (Thumbtack, live): a chevron button beside the box,
   * type submit outside any form, that shows this whole list; Escape closes it.
   */
  toggle?: string[];
  /** Ashby's place search: the same chevron shows only this message ("No results") until something is typed. */
  toggleEmpty?: string;
}

/** A search-backed combobox whose suggestions mount only once the input has text. */
export function mountCombobox(o: ComboboxOptions): Combobox {
  const parent = o.parent ?? document.body;
  const wrap = document.createElement("div");
  wrap.innerHTML = `<label for="${o.id}">${o.label}</label>`;
  const host = document.createElement("div");
  const input = document.createElement("input");
  input.id = o.id;
  input.type = "text";
  input.setAttribute("aria-autocomplete", o.autocomplete ?? "list");
  input.setAttribute("aria-controls", `${o.id}-list`);
  if (o.minQuery) input.setAttribute("minquerylength", String(o.minQuery));
  if (o.wrapper) {
    host.setAttribute("role", "combobox");
    host.setAttribute("aria-expanded", "false");
    host.setAttribute("aria-owns", `${o.id}-list`);
    input.removeAttribute("aria-controls");
  } else {
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-expanded", "false");
  }
  const list = document.createElement("ul");
  list.id = `${o.id}-list`;
  list.setAttribute("role", "listbox");
  list.hidden = true;
  const clicked: string[] = [];
  const keys: string[] = [];
  const queries: string[] = [];
  let token = 0;
  let active = 0;
  const flag = (open: boolean) => (o.wrapper ? host : input).setAttribute("aria-expanded", String(open));
  const commit = (text: string) => {
    input.value = text;
    input.dispatchEvent(new Event("change", { bubbles: true }));
    list.hidden = true;
    flag(false);
  };
  const highlight = (i: number) => {
    active = i;
    list.querySelectorAll("li").forEach((li, j) => li.setAttribute("aria-selected", String(j === i)));
  };
  const render = (labels: string[]) => {
    list.innerHTML = "";
    for (const text of labels) {
      const li = document.createElement("li");
      li.setAttribute("role", "option");
      li.textContent = text;
      li.addEventListener("click", () => {
        clicked.push(text);
        if (!o.highlightFirst) commit(text);
      });
      list.appendChild(li);
    }
    if (o.highlightFirst && labels.length) highlight(0);
    list.hidden = labels.length === 0;
    flag(labels.length > 0);
  };
  const search = async () => {
    const q = input.value;
    queries.push(q);
    if (q === "") {
      list.hidden = true;
      flag(false);
      return;
    }
    const mine = ++token;
    const labels = await o.suggest(q);
    if (o.delayMs) await new Promise((r) => setTimeout(r, o.delayMs));
    if (mine === token) render(labels);
  };
  input.addEventListener("input", () => {
    if ((o.searchOn ?? "input") === "input") void search();
  });
  input.addEventListener("keydown", (e) => {
    keys.push(`keydown:${(e as KeyboardEvent).key}`);
    if ((e as KeyboardEvent).key === "Escape") {
      list.hidden = true;
      flag(false);
    }
    const rows = list.hidden ? [] : Array.from(list.querySelectorAll("li"));
    if (o.highlightFirst && rows.length && (e as KeyboardEvent).key === "ArrowDown") highlight(Math.min(active + 1, rows.length - 1));
    if (o.highlightFirst && rows.length && (e as KeyboardEvent).key === "Enter") commit(rows[active]?.textContent ?? "");
    if (o.searchOn === "keydown") queueMicrotask(() => void search());
  });
  input.addEventListener("keyup", (e) => keys.push(`keyup:${(e as KeyboardEvent).key}`));
  host.appendChild(input);
  if (o.toggle || o.toggleEmpty) {
    const chevron = document.createElement("button");
    chevron.innerHTML = "<svg></svg>";
    const show = () => {
      if (o.toggle) return render(o.toggle);
      list.innerHTML = `<div data-empty=""><div class="noResults"><p>${o.toggleEmpty}</p></div></div>`;
      list.hidden = false;
      flag(true);
    };
    chevron.addEventListener("click", () => (list.hidden ? show() : ((list.hidden = true), flag(false))));
    host.appendChild(chevron);
  }
  wrap.appendChild(host);
  wrap.appendChild(list);
  parent.appendChild(wrap);
  return { input, list, clicked, keys, queries, chosen: () => input.value };
}
