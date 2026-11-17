// Clean-room: docs/provenance.md#CR-ENGINE
// Synthetic widget-library DOM: react-select, Select2, Dojo dijit, intl-tel-input, react-day-picker, TinyMCE.
// Class names and attributes follow the libraries' shape; content is fictional.

export interface ReactSelectOptions {
  /** The input id. react-select derives its menu id from it. */
  inputId: string;
  label: string;
  options: string[];
  multi?: boolean;
  /** Mount the menu on the body instead of beside the control. */
  portal?: boolean;
  prefix?: string;
  /** Options respond to a pointer click on the option. */
  clickable?: boolean;
  hint?: string;
  /** A catalog: with nothing typed the menu lists only this many options; typing searches them all. */
  firstPage?: number;
}

export interface ReactSelect {
  input: HTMLInputElement;
  control: HTMLElement;
  value: () => string[];
  clicked: string[];
  /** The keys pressed in the input. */
  keys: string[];
}

/** react-select v5 shape: control, value container, combobox input, menu with ids derived from the input id. */
export function mountReactSelect(o: ReactSelectOptions): ReactSelect {
  const p = o.prefix ?? "select";
  const keyed = /^react-select-\d+-input$/.test(o.inputId);
  const listboxId = keyed ? o.inputId.replace(/-input$/, "-listbox") : `react-select-${o.inputId}-listbox`;
  const wrap = document.createElement("div");
  wrap.className = `${p}-container`;
  wrap.innerHTML = `<label for="${o.inputId}">${o.label}</label>${o.hint ? `<div class="hint">${o.hint}</div>` : ""}`;
  const control = document.createElement("div");
  control.className = `${p}__control`;
  const valueBox = document.createElement("div");
  valueBox.className = `${p}__value-container${o.multi ? ` ${p}__value-container--is-multi` : ""}`;
  const placeholder = document.createElement("div");
  placeholder.className = `${p}__placeholder`;
  placeholder.textContent = "Select...";
  const inputBox = document.createElement("div");
  inputBox.className = `${p}__input-container`;
  const input = document.createElement("input");
  input.id = o.inputId;
  input.setAttribute("role", "combobox");
  input.setAttribute("aria-haspopup", "true");
  input.setAttribute("aria-autocomplete", "list");
  input.setAttribute("aria-expanded", "false");
  inputBox.appendChild(input);
  valueBox.append(placeholder, inputBox);
  control.appendChild(valueBox);
  wrap.appendChild(control);
  document.body.appendChild(wrap);

  const chosen: string[] = [];
  const clicked: string[] = [];
  let menu: HTMLElement | null = null;
  const renderValue = () => {
    valueBox.querySelectorAll(`.${p}__single-value, .${p}__multi-value`).forEach((n) => n.remove());
    placeholder.hidden = chosen.length > 0;
    for (const text of chosen) {
      if (o.multi) {
        const chip = document.createElement("div");
        chip.className = `${p}__multi-value`;
        chip.innerHTML = `<div class="${p}__multi-value__label">${text}</div><div class="${p}__multi-value__remove" role="button" aria-label="Remove ${text}"></div>`;
        valueBox.insertBefore(chip, inputBox);
      } else {
        const single = document.createElement("div");
        single.className = `${p}__single-value`;
        single.textContent = text;
        valueBox.insertBefore(single, inputBox);
      }
    }
  };
  const close = () => {
    menu?.remove();
    menu = null;
    input.setAttribute("aria-expanded", "false");
  };
  const open = () => {
    if (menu) return;
    menu = document.createElement("div");
    menu.className = `${p}__menu`;
    const list = document.createElement("div");
    list.id = listboxId;
    list.className = `${p}__menu-list`;
    list.setAttribute("role", "listbox");
    o.options
      .filter((t) => t.toLowerCase().includes(input.value.toLowerCase()))
      .slice(0, input.value === "" && o.firstPage ? o.firstPage : undefined)
      .forEach((text, i) => {
        const opt = document.createElement("div");
        opt.className = `${p}__option`;
        opt.id = `${listboxId.replace(/-listbox$/, "")}-option-${i}`;
        opt.setAttribute("role", "option");
        opt.textContent = text;
        if (o.clickable !== false) {
          opt.addEventListener("click", () => {
            clicked.push(text);
            if (o.multi) chosen.push(text);
            else chosen.splice(0, chosen.length, text);
            input.value = "";
            renderValue();
            close();
          });
        }
        list.appendChild(opt);
      });
    menu.appendChild(list);
    (o.portal ? document.body : wrap).appendChild(menu);
    input.setAttribute("aria-expanded", "true");
  };
  control.addEventListener("mousedown", () => open());
  input.addEventListener("input", () => {
    close();
    open();
  });
  const keys: string[] = [];
  input.addEventListener("keydown", (e) => {
    const key = (e as KeyboardEvent).key;
    keys.push(key);
    if (key === "ArrowDown") open();
    if (key === "Escape") close();
  });
  return { input, control, value: () => chosen.slice(), clicked, keys };
}

export interface Select2Options {
  id: string;
  label: string;
  /** Rows the catalog holds. The native select starts empty (a search-backed catalog). */
  catalog: string[];
  /** Answer searches after this many ms, showing a Searching status first. */
  delayMs?: number;
  /** Rows shown with an empty search. */
  initial?: string[];
  /** An ajax catalog's debounce: the old rows stay up this long after a keystroke. */
  debounceMs?: number;
  /** A multi-select: each pick is a chip in the selection, and the search box sits inline beside the chips. */
  multiple?: boolean;
  /** An infinite-scroll catalog: the rows shown with an empty search end in a "Loading more results…" row. */
  paged?: boolean;
}

export interface Select2 {
  select: HTMLSelectElement;
  selection: HTMLElement;
  searches: string[];
  isOpen: () => boolean;
}

/** The stock select2 v4 rule for the native select it replaces. */
const SELECT2_CSS =
  ".select2-hidden-accessible{border:0!important;clip:rect(0 0 0 0)!important;clip-path:inset(50%)!important;height:1px!important;overflow:hidden!important;padding:0!important;position:absolute!important;width:1px!important;white-space:nowrap!important}";

/** happy-dom computes no clip: report the value a browser computes for the select2 rule. */
function emulateClip(): void {
  const real = window.getComputedStyle.bind(window);
  window.getComputedStyle = ((el: Element, pseudo?: string | null) => {
    const st = real(el, pseudo);
    if (!el.classList.contains("select2-hidden-accessible")) return st;
    return new Proxy(st, {
      get: (t, k) => {
        if (k === "clip") return "rect(0px, 0px, 0px, 0px)";
        const v = Reflect.get(t, k, t);
        return typeof v === "function" ? v.bind(t) : v;
      },
    });
  }) as typeof window.getComputedStyle;
}

/** Select2 v4 single select: native select behind, container with a selection, body-level dropdown while open. */
export function mountSelect2(o: Select2Options): Select2 {
  const wrap = document.createElement("div");
  wrap.innerHTML = `<label for="${o.id}" id="${o.id}-label">${o.label}</label>`;
  const select = document.createElement("select");
  select.id = o.id;
  select.className = "select2-hidden-accessible";
  select.setAttribute("data-select2-id", o.id);
  // Live select2 v4 hides the native select from people: aria-hidden, out of the tab order, clipped to one pixel.
  select.setAttribute("aria-hidden", "true");
  select.tabIndex = -1;
  select.multiple = !!o.multiple;
  select.innerHTML = o.multiple ? "" : `<option value=""></option>`;
  if (!document.getElementById("select2-css")) {
    const css = document.createElement("style");
    css.id = "select2-css";
    css.textContent = SELECT2_CSS;
    document.head.appendChild(css);
    emulateClip();
  }
  const container = document.createElement("span");
  container.className = "select2 select2-container";
  const selection = document.createElement("span");
  selection.className = `select2-selection select2-selection--${o.multiple ? "multiple" : "single"}`;
  selection.setAttribute("role", "combobox");
  selection.setAttribute("aria-owns", `select2-${o.id}-results`);
  selection.setAttribute("aria-expanded", "false");
  const rendered = document.createElement(o.multiple ? "ul" : "span");
  rendered.className = "select2-selection__rendered";
  rendered.innerHTML = o.multiple
    ? `<li class="select2-search select2-search--inline"><input class="select2-search__field" type="search" role="searchbox" autocomplete="off" aria-labelledby="${o.id}-label"></li>`
    : `<span class="select2-selection__placeholder">Select</span>`;
  selection.appendChild(rendered);
  container.appendChild(selection);
  wrap.append(select, container);
  document.body.appendChild(wrap);

  const searches: string[] = [];
  let dropdown: HTMLElement | null = null;
  const fillResults = (results: HTMLElement, query: string) => {
    const draw = () => {
      results.innerHTML = "";
      const rows = query === "" ? (o.initial ?? []) : o.catalog.filter((t) => t.toLowerCase().includes(query.toLowerCase()));
      if (!rows.length) {
        results.innerHTML = `<li class="select2-results__option select2-results__message" role="alert">${query === "" ? "Please enter 2 or more characters" : "No results found"}</li>`;
        return;
      }
      for (const text of rows) {
        const li = document.createElement("li");
        li.className = "select2-results__option";
        li.setAttribute("role", "option");
        li.textContent = text;
        li.addEventListener("click", () => {
          if (o.multiple) {
            const opt = document.createElement("option");
            opt.value = opt.textContent = text;
            opt.selected = true;
            select.appendChild(opt);
            const chip = document.createElement("li");
            chip.className = "select2-selection__choice";
            chip.title = text;
            chip.textContent = text;
            rendered.insertBefore(chip, rendered.lastElementChild);
          } else {
            select.innerHTML = `<option value="${text}" selected>${text}</option>`;
            select.value = text;
            rendered.innerHTML = `<span title="${text}">${text}</span>`;
          }
          select.dispatchEvent(new Event("change", { bubbles: true }));
          close();
        });
        results.appendChild(li);
      }
      if (o.paged && query === "") results.insertAdjacentHTML("beforeend", `<li class="select2-results__option select2-results__option--load-more" role="option" aria-disabled="true">Loading more results…</li>`);
    };
    if (o.delayMs && query !== "") {
      results.innerHTML = `<li class="select2-results__option loading-results" role="alert">Searching…</li>`;
      setTimeout(draw, o.delayMs);
    } else draw();
  };
  const close = () => {
    dropdown?.remove();
    dropdown = null;
    container.classList.remove("select2-container--open");
    selection.setAttribute("aria-expanded", "false");
  };
  const open = () => {
    if (dropdown) return;
    container.classList.add("select2-container--open");
    selection.setAttribute("aria-expanded", "true");
    dropdown = document.createElement("span");
    dropdown.className = "select2-container select2-container--default select2-container--open";
    const box = o.multiple ? "" : `<span class="select2-search select2-search--dropdown"><input class="select2-search__field" type="search" aria-controls="select2-${o.id}-results"></span>`;
    dropdown.innerHTML = `<span class="select2-dropdown select2-dropdown--below">${box}<span class="select2-results"><ul class="select2-results__options" role="listbox" id="select2-${o.id}-results"></ul></span></span>`;
    document.body.appendChild(dropdown);
    const results = dropdown.querySelector("ul") as HTMLElement;
    const field = (o.multiple ? rendered : dropdown).querySelector("input") as HTMLInputElement;
    fillResults(results, "");
    // Handlers, not listeners: the inline box of a multi-select outlives each opening.
    field.oninput = () => {
      const query = field.value;
      searches.push(query);
      if (o.debounceMs) setTimeout(() => fillResults(results, query), o.debounceMs);
      else fillResults(results, query);
    };
    field.onkeydown = (e) => {
      if (e.key === "Escape") close();
    };
  };
  selection.addEventListener("mousedown", () => open());
  return { select, selection, searches, isOpen: () => dropdown !== null };
}

/** react-day-picker: a trigger button with a dialog popup holding a month grid. */
export function mountDayPicker(o: { id: string; label: string; start: { y: number; m: number }; caption?: (y: number, m: number) => string; disabledDay?: number; noNext?: boolean }) {
  const wrap = document.createElement("div");
  wrap.innerHTML = `<label id="${o.id}-lbl">${o.label}</label>`;
  const trigger = document.createElement("button");
  trigger.type = "button";
  trigger.id = o.id;
  trigger.setAttribute("aria-haspopup", "dialog");
  trigger.setAttribute("aria-labelledby", `${o.id}-lbl`);
  trigger.textContent = "Select a date";
  wrap.appendChild(trigger);
  document.body.appendChild(wrap);
  const names = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
  let cur = { ...o.start };
  let panel: HTMLElement | null = null;
  const stats = { navClicks: 0, escapes: 0 };
  const draw = () => {
    if (!panel) return;
    const days = new Date(cur.y, cur.m, 0).getDate();
    panel.innerHTML =
      `<div class="rdp-caption"><span class="rdp-caption_label" aria-live="polite">${o.caption ? o.caption(cur.y, cur.m) : `${names[cur.m - 1]} ${cur.y}`}</span>` +
      `<button type="button" name="previous-month" aria-label="Go to previous month"></button>${o.noNext ? "" : `<button type="button" name="next-month" aria-label="Go to next month"></button>`}</div>` +
      `<div class="rdp-grid">${Array.from({ length: days }, (_, i) => `<button type="button" name="day" class="rdp-day"${o.disabledDay === i + 1 ? " disabled" : ""}>${i + 1}</button>`).join("")}</div>`;
    panel.querySelector("[name=previous-month]")?.addEventListener("click", () => {
      stats.navClicks++;
      cur = cur.m === 1 ? { y: cur.y - 1, m: 12 } : { y: cur.y, m: cur.m - 1 };
      draw();
    });
    panel.querySelector("[name=next-month]")?.addEventListener("click", () => {
      stats.navClicks++;
      cur = cur.m === 12 ? { y: cur.y + 1, m: 1 } : { y: cur.y, m: cur.m + 1 };
      draw();
    });
    panel.querySelectorAll("[name=day]").forEach((b) =>
      b.addEventListener("click", () => {
        trigger.textContent = `${cur.y}-${String(cur.m).padStart(2, "0")}-${String(Number(b.textContent)).padStart(2, "0")}`;
      }),
    );
  };
  trigger.addEventListener("click", () => {
    if (panel) {
      panel.remove();
      panel = null;
      return;
    }
    panel = document.createElement("div");
    panel.className = "rdp";
    panel.setAttribute("role", "dialog");
    wrap.appendChild(panel);
    draw();
  });
  trigger.addEventListener("keydown", (e) => {
    if ((e as KeyboardEvent).key === "Escape") stats.escapes++;
  });
  return { trigger, stats, isOpen: () => panel !== null };
}

const TOX_TOOLBAR = [["Undo", "Redo"], ["Bold", "Italic"], ["Align left", "Align center", "Align right"], ["Bullet list", "Numbered list"]];

/**
 * A textarea TinyMCE 5 has taken over, as Avature draws it: the textarea hidden from sight and from assistive tech, and
 * beside it the editor with its menu bar, toolbar groups (all but Undo/Redo hold a pressed state), editing frame and
 * status bar. No editor object exists in the page: `saved` is the HTML the editor last saved into the textarea.
 */
export function mountTinymce(o: { id: string; label: string; saved?: string }): HTMLTextAreaElement {
  const wrap = document.createElement("div");
  const menus = ["Edit", "View", "Insert", "Format", "Tools", "Help"].map((m) => `<button role="menuitem" type="button" tabindex="-1" aria-haspopup="true" aria-expanded="false" class="tox-mbtn"><span>${m}</span></button>`);
  const groups = TOX_TOOLBAR.map((g, i) => `<div role="toolbar" class="tox-toolbar__group">${g.map((b) => `<button type="button" tabindex="-1" class="tox-tbtn" aria-label="${b}" title="${b}"${i ? ' aria-pressed="false"' : ""}></button>`).join("")}</div>`);
  wrap.innerHTML = `<div role="group" aria-required="true"><label for="${o.id}">${o.label}<span aria-hidden="true">*</span></label><div>
    <textarea id="${o.id}" name="${o.id}" required aria-required="true" aria-hidden="true" style="display: none;"></textarea>
    <div role="application" class="tox tox-tinymce"><div class="tox-editor-container"><div class="tox-editor-header">
      <div role="menubar" class="tox-menubar">${menus.join("")}</div>
      <div role="group" class="tox-toolbar-overlord"><div role="group" class="tox-toolbar__primary">${groups.join("")}
        <div role="toolbar" class="tox-toolbar__group"><div role="button" aria-pressed="false" aria-haspopup="true" aria-label="Text color" class="tox-split-button"></div></div></div></div></div>
      <div class="tox-sidebar-wrap"><div class="tox-edit-area"><iframe id="${o.id}_ifr" class="tox-edit-area__iframe" title="Rich Text Area"></iframe></div></div></div>
      <div class="tox-statusbar"><div role="navigation" class="tox-statusbar__path"><div role="button" tabindex="-1" class="tox-statusbar__path-item">p</div></div></div></div>
  </div></div>`;
  document.body.appendChild(wrap);
  const area = document.getElementById(o.id) as HTMLTextAreaElement;
  area.value = o.saved ?? "";
  return area;
}
