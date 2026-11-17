// Clean-room: docs/provenance.md#CR-ENGINE
// Synthetic pages for the application-system drivers. Each helper mounts a
// small piece of DOM that reproduces the markup and the timing of one
// vendor's widget, with fictional content only.

export const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export function add(html: string, parent: HTMLElement = document.body): HTMLElement {
  const holder = document.createElement("div");
  holder.innerHTML = html;
  const el = holder.firstElementChild as HTMLElement;
  parent.append(el);
  return el;
}

// --- Workday ----------------------------------------------------------------

export interface WorkdayPromptOptions {
  id: string;
  label: string;
  catalog: string[];
  widget?: string;
  /** Milliseconds the server takes to answer a search. */
  delayMs?: number;
  /** Rows the previous query left mounted stay until the new answer arrives (always true in the vendor's page). */
  responsive?: boolean;
  /** Mount only this many rows around the scroll position. */
  windowSize?: number;
  /** The single match commits on Enter, before any leaf click. */
  commitOnEnter?: boolean;
  /** A wrong pill the parser put there. */
  held?: string[];
  /** No widget id on the input: the trigger cannot tell which prompt is its own. */
  noWidgetId?: boolean;
  /** The server answers every search with the whole catalog. */
  matchAll?: boolean;
  /** Results by query text, for a search whose rows the test wants to name. */
  answers?: Record<string, string[]>;
  /** The prompt stays mounted after a commit, with the rows of the last search, as the vendor's page does. */
  keepRoot?: boolean;
  /** Hierarchical menu: category name -> leaves. The input opens it on click. */
  tree?: Record<string, string[]>;
  /** A sub-menu, and the way back, arrive from the server this late. */
  branchDelayMs?: number;
  /** The way back is Workday's clickable sub-menu header (an arrow icon and the category's title), not a Back button. */
  headerBack?: boolean;
  /** Rows as Workday draws them: an outer option row around the labelled promptOption, the chevron beside the label. */
  workdayRows?: boolean;
  /** The top level is drawn two rows first, the rest a moment later. */
  drawTwoAtFirst?: boolean;
  /** The list is already open when the run arrives and Enter filters nothing: it can only be scrolled. */
  scrollOnly?: boolean;
  /** The element the field is mounted in, the page body when unset. */
  parent?: HTMLElement;
}

export interface WorkdayPrompt {
  input: HTMLInputElement;
  prompt: () => HTMLElement | null;
  pills: () => string[];
  searches: string[];
  clicks: string[];
  doneClicks: () => number;
  closeClicks: () => number;
}

export function mountWorkdayPrompt(o: WorkdayPromptOptions): WorkdayPrompt {
  const widget = o.widget ?? `W-${o.id}`;
  const field = add(`<div data-automation-id="formField-${o.id}">
    <label for="${o.id}">${o.label}</label>
    <div data-automation-id="multiSelectContainer" id="msc-${o.id}" ${o.noWidgetId ? "" : `data-uxi-multiselect-id="${widget}"`}>
      <ul data-automation-id="selectedItemList"></ul>
      <input id="${o.id}" data-uxi-widget-type="selectinput" ${o.noWidgetId ? "" : `data-uxi-multiselect-id="${widget}"`}>
    </div></div>`, o.parent);
  const input = field.querySelector("input") as HTMLInputElement;
  const list = field.querySelector("ul") as HTMLElement;
  const searches: string[] = [];
  const clicks: string[] = [];
  let done = 0;
  let closes = 0;
  let root: HTMLElement | null = null;
  let level: string[] | null = null;
  let title = "";
  let all: string[] = [];

  const addPill = (label: string) => {
    const li = document.createElement("li");
    li.setAttribute("data-automation-id", "selectedItem");
    li.innerHTML = `<span>${label}</span><span data-automation-id="DELETE_charm" aria-label="press delete to clear value"></span>`;
    (li.querySelector("[data-automation-id=DELETE_charm]") as HTMLElement).addEventListener("click", () => li.remove());
    list.append(li);
  };
  const mountRoot = (): HTMLElement => {
    if (root?.isConnected) return root;
    root = document.createElement("div");
    root.setAttribute("data-automation-id", "activeListContainer");
    if (!o.noWidgetId) root.setAttribute("data-associated-widget", widget);
    if (o.responsive) {
      root.setAttribute("data-automation-mode", "reactresponsive");
      // As captured live: a header with the x icon, the options, a Done button.
      root.setAttribute("data-automation-id", "responsiveMonikerPrompt");
      root.innerHTML = `<div><div>${o.label}</div><div data-automation-id="icon" tabindex="0"><svg class="wd-icon-x wd-icon"></svg></div></div><ul role="listbox"></ul><div><button type="button"><span>Done</span></button></div>`;
      (root.querySelector("[data-automation-id=icon]") as HTMLElement).addEventListener("click", () => {
        closes++;
        root?.remove();
      });
      (root.querySelector("button") as HTMLElement).addEventListener("click", () => {
        done++;
        root?.remove();
        root = null;
      });
    } else root.innerHTML = `<ul role="listbox"></ul>`;
    document.body.append(root);
    return root;
  };
  const ul = () => (root as HTMLElement).querySelector("ul[role=listbox]") as HTMLElement;
  const commit = (label: string) => {
    clicks.push(label);
    addPill(label);
    input.value = "";
    if (!o.responsive && !o.keepRoot) {
      root?.remove();
      root = null;
    }
  };
  const later = (f: () => void) => (o.branchDelayMs ? setTimeout(f, o.branchDelayMs) : f());
  const row = (label: string, branch = false): HTMLElement => {
    const li = document.createElement("li");
    li.setAttribute("role", "option");
    li.setAttribute("data-row", "");
    if (o.workdayRows) {
      li.setAttribute("aria-label", `${label} not checked`);
      const chevron = branch ? '<div><span><svg class="wd-icon-chevron-right-small wd-icon"></svg></span></div>' : "";
      li.innerHTML = `<div data-automation-id="promptLeafNode"><div data-automation-id="promptOption" data-automation-label="${label}">${label}</div>${chevron}</div>`;
    } else {
      li.setAttribute("data-automation-id", "promptOption");
      li.setAttribute("data-automation-label", label);
      if (branch) li.setAttribute("aria-haspopup", "menu");
      li.innerHTML = `<div data-automation-id="promptLeafNode">${label}${branch ? '<span data-automation-id="promptChevron"></span>' : ""}</div>`;
    }
    li.addEventListener("click", () => {
      // A re-rendered row is gone: the site's root listener never sees it.
      if (!li.isConnected) return;
      if (branch) {
        const sub = (o.tree as Record<string, string[]>)[label] as string[];
        level = sub;
        title = label;
        // An empty category is Workday's "No Items." row under the way back.
        later(() => render(sub, sub.length === 0, true));
      } else commit(label);
    });
    return li;
  };
  const render = (labels: string[], noItems: boolean, back = false, results = false) => {
    mountRoot();
    const target = ul();
    target.innerHTML = "";
    target.scrollTop = 0;
    if (back) {
      const up = () => later(() => render(Object.keys(o.tree as Record<string, string[]>), false));
      if (o.headerBack) {
        const h = document.createElement("div");
        h.setAttribute("data-automation-id", "multiSelectHeader");
        h.setAttribute("data-uxi-multiselectlistheader-clickable", "true");
        h.innerHTML = `<svg data-automation-id="backButton"></svg><div data-automation-id="promptTitle" aria-hidden="true">${title}</div>`;
        h.addEventListener("click", up);
        target.append(h);
      } else {
        const b = document.createElement("button");
        b.textContent = "Back";
        b.addEventListener("click", up);
        target.append(b);
      }
    }
    // A search's results are leaves, whatever menu they replace.
    const isTree = !!o.tree && !back && !results;
    all = labels;
    const win = o.windowSize ?? labels.length;
    const at = () => Math.floor(target.scrollTop / 20);
    const paint = () => {
      target.querySelectorAll("[data-automation-id=promptOption], [data-row]").forEach((n) => n.remove());
      const shown = labels.slice(at(), at() + win);
      if (o.drawTwoAtFirst && isTree) {
        for (const l of shown.slice(0, 2)) target.append(row(l, isTree));
        const first = target.lastElementChild;
        setTimeout(() => {
          if (first?.isConnected) for (const l of shown.slice(2)) target.append(row(l, isTree));
        }, 30);
        return;
      }
      for (const l of shown) target.append(row(l, isTree));
    };
    paint();
    if (noItems) {
      const li = document.createElement("li");
      li.setAttribute("role", "option");
      li.setAttribute("data-automation-id", "promptOption");
      li.textContent = "No Items.";
      target.append(li);
    }
    if (o.windowSize) target.addEventListener("scroll", paint);
  };
  if (o.tree) input.addEventListener("click", () => render(Object.keys(o.tree as Record<string, string[]>), false));
  for (const h of o.held ?? []) addPill(h);
  if (o.scrollOnly) render(o.catalog, false);
  input.addEventListener("keydown", (e) => {
    if ((e as KeyboardEvent).key !== "Enter") return;
    const q = input.value.toLowerCase();
    searches.push(input.value);
    if (o.scrollOnly) return;
    setTimeout(() => {
      const tokens = q.split(/\s+/).filter(Boolean);
      const hits = o.answers?.[input.value] ?? (o.matchAll ? o.catalog : o.catalog.filter((c) => tokens.some((t) => c.toLowerCase().includes(t))));
      if (o.commitOnEnter && hits.length === 1) {
        commit(hits[0] as string);
        return;
      }
      render(hits, hits.length === 0, false, true);
    }, o.delayMs ?? 10);
  });
  void level;
  void all;
  return {
    input,
    prompt: () => root,
    pills: () => Array.from(list.querySelectorAll("li > span:first-child")).map((s) => s.textContent ?? ""),
    searches,
    clicks,
    doneClicks: () => done,
    closeClicks: () => closes,
  };
}

/** A prompt another field owns: its rows are never this field's. */
export function mountForeignPrompt(widget: string, rows: string[]): HTMLElement {
  const root = add(`<div data-automation-id="activeListContainer" data-associated-widget="${widget}"><ul role="listbox">${rows
    .map((r) => `<li role="option" data-automation-id="promptOption" data-automation-label="${r}"><div data-automation-id="promptLeafNode">${r}</div></li>`)
    .join("")}</ul></div>`);
  return root;
}

export interface DateTripleOptions {
  id: string;
  legend: string;
  questionnaire?: boolean;
  /** The segments ignore the first interaction (the widget has not hydrated yet). */
  coldStart?: boolean;
  noDay?: boolean;
  /** The tenant shows the month and day without a leading zero. */
  unpadded?: boolean;
}

export function mountDateTriple(o: DateTripleOptions): HTMLInputElement[] {
  const base = o.questionnaire === false ? `${o.id}-startDate` : `${o.id}--Questionnaire--q1`;
  const seg = (name: string, label: string) =>
    `<input id="${base}--dateSection${name}-input" role="spinbutton" aria-label="${label}" inputmode="numeric" maxlength="${name === "Year" ? 4 : 2}">`;
  const el = add(`<fieldset><legend>${o.legend}</legend><div data-automation-id="dateInputWrapper" id="${base}">${seg("Month", "Month")}${o.noDay ? "" : seg("Day", "Day")}${seg("Year", "Year")}</div></fieldset>`);
  const parts = Array.from(el.querySelectorAll("input"));
  if (o.unpadded) {
    for (const p of parts) p.addEventListener("change", () => (p.value = p.value.replace(/^0+(?=\d)/, "")));
  }
  if (o.coldStart) {
    // The widget has not hydrated: it discards everything typed in its first attempt.
    let swallow = o.noDay ? 6 : 8;
    for (const p of parts) {
      p.addEventListener("input", () => {
        if (swallow-- > 0) p.value = "";
      });
    }
  }
  return parts;
}

// --- Oracle Recruiting Cloud --------------------------------------------------

export interface OracleSelectOptions {
  id: string;
  name?: string;
  label: string;
  options: string[];
  /** "substring": the list filters on the typed text; a space seed lists nothing. */
  filter?: "none" | "substring";
  /** Milliseconds a re-render takes after a seed. */
  lagMs?: number;
  /** A server search lists only its first matches. */
  limit?: number;
  /** What the input shows for a chosen row: Oracle's postal list shows only the code before the first comma. */
  display?: (row: string) => string;
  /** Milliseconds the grid stays open after a choice (Chase's postal list). */
  closeDelayMs?: number;
}

export interface OracleSelect {
  input: HTMLInputElement;
  grid: HTMLElement;
  opened: () => number;
  commits: string[];
}

function oracleRow(text: string): string {
  return `<div role="row"><div role="gridcell"><span class="cx-select__list-item--content">${text}</span></div></div>`;
}

export function mountOracleSelect(o: OracleSelectOptions): OracleSelect {
  const wrap = add(`<div class="cx-select-container"><label for="${o.id}">${o.label}</label>
    <input class="cx-select-input" id="${o.id}" name="${o.name ?? o.id}" role="combobox" aria-haspopup="grid" aria-expanded="false" aria-controls="${o.id}-grid" autocomplete="off">
    <div id="${o.id}-grid" role="grid" class="cx-select-dropdown" hidden></div></div>`);
  const input = wrap.querySelector("input") as HTMLInputElement;
  const grid = wrap.querySelector("[role=grid]") as HTMLElement;
  const commits: string[] = [];
  let opens = 0;
  const show = (rows: string[]) => {
    grid.hidden = false;
    grid.classList.add("cx-select-dropdown--open");
    input.setAttribute("aria-expanded", "true");
    grid.innerHTML = rows.length ? rows.map(oracleRow).join("") : oracleRow("No results found");
    grid.querySelectorAll("[role=gridcell]").forEach((cell) => {
      let hovered = false;
      cell.addEventListener("mouseover", () => (hovered = true));
      // The library commits only a click that follows a hover.
      cell.addEventListener("click", () => {
        if (!hovered) return;
        const text = cell.textContent ?? "";
        if (/^no results/i.test(text)) return;
        commits.push(text);
        input.value = o.display ? o.display(text) : text;
        if (o.closeDelayMs) setTimeout(close, o.closeDelayMs);
        else close();
      });
    });
  };
  const close = () => {
    grid.hidden = true;
    grid.classList.remove("cx-select-dropdown--open");
    input.setAttribute("aria-expanded", "false");
  };
  const filtered = (): string[] => {
    if (o.filter !== "substring") return o.options;
    const q = input.value.trim().toLowerCase();
    return q ? o.options.filter((x) => x.toLowerCase().includes(q)).slice(0, o.limit) : [];
  };
  input.addEventListener("mousedown", () => {
    opens++;
    show(o.filter === "substring" ? [] : o.options);
    if (o.filter === "substring") grid.innerHTML = "";
  });
  input.addEventListener("input", () => {
    const render = () => show(filtered());
    if (o.lagMs) {
      // The list first serves the previous result set.
      setTimeout(render, o.lagMs);
    } else render();
  });
  input.addEventListener("keydown", (e) => {
    if ((e as KeyboardEvent).key === "Escape") close();
  });
  // Raw text that was never committed is discarded on blur.
  input.addEventListener("blur", () => {
    if (input.value && !commits.map((c) => (o.display ? o.display(c) : c)).includes(input.value)) input.value = "";
  });
  return { input, grid, opened: () => opens, commits };
}

export interface OracleAddressOptions {
  /** Suggestion lists by time: [delayMs, rows]. */
  waves: [number, string[]][];
  /** A phone-code list that is open at the same time and holds 300 rows. */
  phoneCodes?: boolean;
}

export interface OracleAddress {
  input: HTMLInputElement;
  fields: Record<string, HTMLInputElement>;
  commits: string[];
  typedAt: number[];
}

export function mountOracleAddress(o: OracleAddressOptions): OracleAddress {
  const wrap = add(`<div><label for="addressLine1">Address Line 1 *</label>
    <input id="addressLine1" name="addressLine1" class="cx-select-input cx-select-input--auto-suggest" role="combobox" aria-autocomplete="list" aria-haspopup="grid" aria-controls="addr-grid" autocomplete="off">
    <div id="addr-grid" role="grid"></div>
    <label for="city">City</label><input id="city" name="city">
    <label for="region1">State</label><input id="region1" name="region1">
    <label for="postalCode">Postal Code</label><input id="postalCode" name="postalCode">
    ${o.phoneCodes ? `<div id="codes" role="grid">${Array.from({ length: 300 }, (_, i) => oracleRow(`Country ${i}, +${i + 1}`)).join("")}</div>` : ""}</div>`);
  const input = wrap.querySelector("#addressLine1") as HTMLInputElement;
  const grid = wrap.querySelector("#addr-grid") as HTMLElement;
  const fields = Object.fromEntries(["city", "region1", "postalCode"].map((n) => [n, wrap.querySelector(`[name=${n}]`) as HTMLInputElement]));
  const commits: string[] = [];
  const typedAt: number[] = [];
  let timers: ReturnType<typeof setTimeout>[] = [];
  input.addEventListener("input", () => {
    typedAt.push(Date.now());
    timers.forEach(clearTimeout);
    timers = [];
    // The suggestions come from a debounced request: a burst of keys mounts none until they stop.
    for (const [delay, rows] of o.waves) {
      timers.push(
        setTimeout(() => {
          grid.innerHTML = rows.map(oracleRow).join("");
          grid.querySelectorAll("[role=gridcell]").forEach((cell) =>
            cell.addEventListener("click", () => {
              const text = cell.textContent ?? "";
              commits.push(text);
              input.value = text;
              const parts = text.split(",").map((s) => s.trim());
              setTimeout(() => {
                fields.city!.value = parts[1] ?? "";
                const m = /([A-Z]{2})\s+(\d{5})/.exec(parts[2] ?? "");
                fields.region1!.value = m?.[1] ?? "";
                fields.postalCode!.value = m?.[2] ?? "";
              }, 15);
              grid.innerHTML = "";
            }),
          );
        }, delay),
      );
    }
  });
  return { input, fields, commits, typedAt };
}

// --- Ashby ---------------------------------------------------------------------

export interface AshbyOptions {
  /** A `textbox` question is a text input whose save is sent `textDebounceMs` after the last keystroke (a blur sends it at once when `blurFlushes`). */
  questions: { path: string; text: string; required?: boolean; textbox?: boolean }[];
  textDebounceMs?: number;
  blurFlushes?: boolean;
  /** The server never keeps an answer of this value. */
  dropValue?: string;
  /** Latency of the n-th save request, so overlapping saves can arrive out of order. */
  latency?: (n: number) => number;
  /** The server loses the first `dropTimes` (two by default) Yes values it is sent for each of these paths. */
  dropOnce?: string[];
  dropTimes?: number;
}

export interface AshbyPage {
  /** The server's copy: the whole form of the last request that arrived. */
  server: Record<string, string>;
  sent: string[][];
  maxInflight: () => number;
  posts: () => number;
}

/** A form that saves every answer with its own whole-form request, as the vendor's page does. */
export function mountAshby(o: AshbyOptions): AshbyPage {
  const page: Record<string, string> = {};
  const server: Record<string, string> = {};
  const sent: string[][] = [];
  let inflight = 0;
  let maxIn = 0;
  let n = 0;
  const lost = new Map<string, number>();
  const w = window as unknown as { fetch: typeof fetch };
  w.fetch = (async (_url: string, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? "{}")) as { fields: Record<string, string> };
    const k = n++;
    inflight++;
    maxIn = Math.max(maxIn, inflight);
    sent.push(Object.keys(body.fields));
    await sleep(o.latency ? o.latency(k) : 5);
    inflight--;
    for (const key of Object.keys(server)) delete server[key];
    for (const [key, value] of Object.entries(body.fields)) {
      if (value === "Yes" && o.dropOnce?.includes(key) && (lost.get(key) ?? 0) < (o.dropTimes ?? 2)) {
        lost.set(key, (lost.get(key) ?? 0) + 1);
        continue;
      }
      if (value !== o.dropValue && value !== "") server[key] = value;
    }
    const echo = {
      data: {
        form: {
          sections: [
            { fieldEntries: o.questions.map((q) => ({ path: q.path, value: server[q.path] ?? null, required: q.required === true })) },
          ],
        },
      },
    };
    return new Response(JSON.stringify(echo), { status: 200, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  const html = o.questions
    .map((q) =>
      q.textbox
        ? `<div data-field-path="${q.path}" class="_fieldEntry_x1y2z"><label for="${q.path}">${q.text}${q.required ? " *" : ""}</label><input type="text" id="${q.path}" name="${q.path}"${q.required ? " required" : ""}></div>`
        : `<div data-field-path="${q.path}" class="_fieldEntry_x1y2z"><fieldset><legend>${q.text}${q.required ? " *" : ""}</legend>
        <div class="_yesno_a1b2c"><button type="button" aria-pressed="false">Yes</button><button type="button" aria-pressed="false">No</button></div></fieldset></div>`,
    )
    .join("");
  const root = add(`<div>${html}<label for="pad">Referral code</label><input id="pad" value="REF-1"><label for="pad2">Notes</label><input id="pad2" value="n"></div>`);
  root.querySelectorAll("[data-field-path]").forEach((field) => {
    const path = field.getAttribute("data-field-path") as string;
    const box = field.querySelector("input");
    if (box) {
      let timer: ReturnType<typeof setTimeout> | undefined;
      const send = (): void => {
        clearTimeout(timer);
        timer = undefined;
        void window.fetch("/api/non-user-graphql?op=ApiSetFormValue", { method: "POST", body: JSON.stringify({ fields: { ...page } }) });
      };
      box.addEventListener("input", () => {
        page[path] = box.value;
        clearTimeout(timer);
        timer = setTimeout(send, o.textDebounceMs ?? 20);
      });
      if (o.blurFlushes) box.addEventListener("blur", () => timer !== undefined && send());
    }
    field.querySelectorAll("button").forEach((b) =>
      b.addEventListener("click", () => {
        // Pressing the selected option clears it, as the vendor's widget does.
        const was = b.getAttribute("aria-pressed") === "true";
        field.querySelectorAll("button").forEach((x) => x.setAttribute("aria-pressed", String(x === b && !was)));
        page[path] = was ? "" : (b.textContent ?? "");
        // Every click sends the whole form as the page sees it now.
        void window.fetch("/api/non-user-graphql?op=ApiSetFormValue", { method: "POST", body: JSON.stringify({ fields: { ...page } }) });
      }),
    );
  });
  return { server, sent, maxInflight: () => maxIn, posts: () => n };
}

// --- iCIMS ---------------------------------------------------------------------

export interface IcimsOptions {
  id: string;
  label: string;
  rows: { text: string; value?: string }[];
  enabled?: "0" | "1";
  /** The list loads after it is opened. */
  ajaxMs?: number;
  /** The list is empty until text is typed and a key goes up. */
  search?: boolean;
}

export interface IcimsDropdown {
  select: HTMLSelectElement;
  display: () => string;
  list: HTMLElement;
  opened: () => number;
}

export function mountIcimsDropdown(o: IcimsOptions): IcimsDropdown {
  const attrs = `icimsdropdown-enabled="${o.enabled ?? "1"}"${o.ajaxMs ? " icimsdropdown-ajax" : ""}${o.search ? ' icimsdropdown-search="1"' : ""}`;
  const wrap = add(`<div class="iCIMS_Field"><label for="${o.id}">${o.label}</label>
    <select id="${o.id}" name="${o.id}" ${attrs}><option value="">Make a Selection</option></select>
    <a class="dropdown-select" href="#" role="button"><span class="dropdown-text">Make a Selection</span></a>
    <div class="dropdown-container">${o.search ? '<input type="text" class="dropdown-search">' : ""}<ul class="dropdown-results" style="display:none"></ul></div></div>`);
  const select = wrap.querySelector("select") as HTMLSelectElement;
  const list = wrap.querySelector("ul") as HTMLElement;
  const text = wrap.querySelector(".dropdown-text") as HTMLElement;
  let opens = 0;
  const fill = (query = "") => {
    // A search widget lists the rows that contain the typed text, literally.
    const rows = o.rows.filter((r) => r.text.toLowerCase().includes(query.toLowerCase()));
    list.innerHTML = rows.map((r, i) => `<li class="result-selectable" id="${o.id}-r${i}" ${r.value ? `data-value="${r.value}"` : ""}>${r.text}</li>`).join("");
    list.querySelectorAll("li").forEach((li, i) =>
      li.addEventListener("click", () => {
        const row = rows[i]!;
        select.innerHTML = `<option value="${row.value ?? row.text}" selected>${row.text}</option>`;
        select.value = row.value ?? row.text;
        text.textContent = row.text;
        list.style.display = "none";
      }),
    );
  };
  wrap.querySelector("a")!.addEventListener("mousedown", (e) => {
    e.preventDefault();
    opens++;
    list.style.display = list.style.display === "none" ? "block" : "none";
    if (!o.search) {
      if (o.ajaxMs) setTimeout(() => fill(), o.ajaxMs);
      else fill();
    }
  });
  if (!o.ajaxMs && !o.search) fill();
  const box = wrap.querySelector<HTMLInputElement>("input.dropdown-search");
  box?.addEventListener("keyup", () => fill(box.value));
  return { select, display: () => text.textContent ?? "", list, opened: () => opens };
}

// --- SuccessFactors --------------------------------------------------------------

export interface SfPage {
  uploads: string[];
  expandClicks: () => number;
  fileInput: HTMLInputElement;
}

/** Collapsed sections, an expand anchor, and an uploader that discards a file it did not ask for. */
export function mountSuccessFactors(o: { attached?: boolean } = {}): SfPage {
  const root = add(`<div><a id="app_expandAllSections" href="#">Expand all sections</a><a id="app_collapseAllSections" href="#">Collapse all sections</a>
    <section id="personal" style="display:none"><label for="fn">First name</label><input id="fn" name="fn"><label for="ln">Last name</label><input id="ln" name="ln"></section>
    <section id="resumeSec">${o.attached ? '<span id="r_trashIcon" class="trash">x</span>' : '<a id="r_attachIcon" class="plus" href="#">+</a>'}
      <form id="form_upload" style="display:none"><input type="file" name="fileData" class="fileUpload" id="sf_file"></form></section></div>`);
  let expands = 0;
  const uploads: string[] = [];
  root.querySelector("#app_expandAllSections")!.addEventListener("click", () => {
    expands++;
    setTimeout(() => ((root.querySelector("#personal") as HTMLElement).style.display = ""), 10);
  });
  const file = root.querySelector("#sf_file") as HTMLInputElement;
  let asked = false;
  root.querySelector("#r_attachIcon")?.addEventListener("click", () => {
    asked = true;
    file.click();
  });
  file.addEventListener("change", () => {
    // The uploader takes a file only when it asked for one.
    if (!asked) {
      file.value = "";
      return;
    }
    asked = false;
    uploads.push(file.files?.[0]?.name ?? "");
  });
  return { uploads, expandClicks: () => expands, fileInput: file };
}

// --- Dayforce (Ant Design) ---------------------------------------------------------

export interface AntdOptions {
  id: string;
  label: string;
  options: string[];
  /** The widget replaces its own DOM the first time it is opened (a store-driven re-render). */
  remountOnFirstOpen?: boolean;
}

export interface Antd {
  shown: () => string;
  mounts: () => number;
}

export function mountAntdSelect(o: AntdOptions): Antd {
  let mounts = 0;
  let value = "";
  let opened = false;
  const wrap = add(`<div class="ant-select-wrap"></div>`);
  const render = () => {
    mounts++;
    wrap.innerHTML = `<label for="${o.id}">${o.label}</label><div class="ant-select"><div class="ant-select-selector">
      <input id="${o.id}" role="combobox" aria-haspopup="listbox" aria-expanded="false" aria-controls="${o.id}_list" class="ant-select-selection-search-input">
      ${value ? `<span class="ant-select-selection-item" title="${value}">${value}</span>` : ""}</div></div>`;
    const input = wrap.querySelector("input") as HTMLInputElement;
    input.addEventListener("mousedown", () => {
      document.getElementById(`${o.id}_list`)?.remove();
      const list = document.createElement("div");
      list.id = `${o.id}_list`;
      list.setAttribute("role", "listbox");
      list.innerHTML = o.options.map((t) => `<div role="option" class="ant-select-item-option" aria-selected="false">${t}</div>`).join("");
      document.body.append(list);
      input.setAttribute("aria-expanded", "true");
      list.querySelectorAll("[role=option]").forEach((opt) =>
        opt.addEventListener("click", () => {
          value = opt.textContent ?? "";
          list.remove();
          render();
        }),
      );
      if (o.remountOnFirstOpen && !opened) {
        opened = true;
        setTimeout(() => {
          list.remove();
          render();
        }, 8);
      }
    });
  };
  render();
  return { shown: () => value, mounts: () => mounts };
}

// --- Lever -----------------------------------------------------------------------

export interface LeverLocation {
  input: HTMLInputElement;
  hidden: HTMLInputElement;
  /** Values the search saw, in order. */
  searched: string[];
  clicks: string[];
  /** The box's text at each input event, and the key downs seen. */
  typed: string[];
  downs: () => number;
}

/**
 * A jQuery-style search: it fires from key down, lists suggestions, and commits only a mouse down, up, click sequence.
 * With a spinner, a loading row shows while the search runs, as Lever's does.
 */
export function mountLeverLocation(o: { places: string[]; lagMs?: number; spinner?: boolean }): LeverLocation {
  const wrap = add(`<div><label for="location-input">Current location</label>
    <input id="location-input" name="location" type="text" autocomplete="off"><input type="hidden" name="selectedLocation" value="">
    <div class="dropdown-container"></div><div class="dropdown-loading-results" style="display: none;">Loading</div></div>`);
  const input = wrap.querySelector("#location-input") as HTMLInputElement;
  const hidden = wrap.querySelector("input[type=hidden]") as HTMLInputElement;
  const box = wrap.querySelector(".dropdown-container") as HTMLElement;
  const loading = wrap.querySelector(".dropdown-loading-results") as HTMLElement;
  const searched: string[] = [];
  const clicks: string[] = [];
  const typed: string[] = [];
  let downs = 0;
  input.addEventListener("input", () => typed.push(input.value));
  input.addEventListener("keydown", () => {
    downs++;
    if (o.spinner) loading.style.display = "flex";
    setTimeout(() => {
      loading.style.display = "none";
      const q = input.value.toLowerCase();
      searched.push(input.value);
      box.innerHTML = "";
      if (!q) return;
      for (const place of o.places.filter((p) => p.toLowerCase().includes(q))) {
        const row = document.createElement("div");
        row.className = "dropdown-location";
        row.textContent = place;
        let down = false;
        let up = false;
        row.addEventListener("mousedown", () => (down = true));
        row.addEventListener("mouseup", () => (up = down));
        row.addEventListener("click", () => {
          if (!(down && up)) {
            input.value = "";
            return;
          }
          clicks.push(place);
          input.value = place;
          hidden.value = JSON.stringify({ name: place });
          box.innerHTML = "";
        });
        box.append(row);
      }
    }, o.lagMs ?? 5);
  });
  return { input, hidden, searched, clicks, typed, downs: () => downs };
}

// --- SmartRecruiters ---------------------------------------------------------------

/** A component with a shadow root that ignores input events and takes a value only through handleChange. */
export function defineSplInput(tag = "spl-input"): void {
  if (customElements.get(tag)) return;
  customElements.define(
    tag,
    class extends HTMLElement {
      calls: unknown[] = [];
      connectedCallback() {
        if (this.shadowRoot) return;
        const root = this.attachShadow({ mode: "open" });
        root.innerHTML = `<label for="inner">${this.getAttribute("label") ?? ""}</label>${tag === "spl-textarea" ? '<textarea id="inner"></textarea>' : '<input id="inner">'}`;
        // Events never reach the component's state.
        root.querySelector("input, textarea")!.addEventListener("input", (e) => {
          (e.target as HTMLInputElement).value = "";
        });
      }
      handleChange(v: unknown) {
        this.calls.push(v);
        const el = this.shadowRoot!.querySelector("input, textarea") as HTMLInputElement;
        el.value = v instanceof Date ? `${v.getFullYear()}-${String(v.getMonth() + 1).padStart(2, "0")}-${String(v.getDate()).padStart(2, "0")}` : String(v);
        return Promise.resolve();
      }
      // A value matching data-refuse is held but marked invalid, as SmartRecruiters' own validators do.
      get valid() {
        const re = this.getAttribute("data-refuse");
        return !re || !new RegExp(re).test((this.shadowRoot!.querySelector("input, textarea") as HTMLInputElement).value);
      }
      get error() {
        return this.valid ? null : { errorMessage: `Please enter a valid ${this.getAttribute("label")} URL.` };
      }
      clear() {
        (this.shadowRoot!.querySelector("input, textarea") as HTMLInputElement).value = "";
      }
    },
  );
}

// --- Repeated rows ------------------------------------------------------------------

export type RowStyle = "workday" | "dayforce" | "adp" | "heading" | "paylocity";

export interface RowFixtureOptions {
  entity: "employment" | "education";
  style: RowStyle;
  rows: Record<string, string>[];
  /** Add mounts nothing. */
  noAdd?: boolean;
  /** A remove asks for confirmation in a dialog with this title. */
  confirm?: string;
  /** After a row is removed the page makes this one again, up to `times` times. */
  recreate?: { row: Record<string, string>; times: number; afterMs: number };
  /** The page-level controls that must never be pressed. */
  cancelButton?: boolean;
}

export interface RowFixture {
  root: HTMLElement;
  values: () => Record<string, string>[];
  addClicks: () => number;
  removed: () => string[];
  pressed: string[];
  addRow: (values: Record<string, string>) => void;
}

const FIELDS: Record<RowFixtureOptions["entity"], { key: string; label: string; id: string }[]> = {
  employment: [
    { key: "company", label: "Company", id: "company" },
    { key: "title", label: "Job Title", id: "jobTitle" },
    { key: "start", label: "From", id: "startDate" },
    { key: "end", label: "To", id: "endDate" },
  ],
  education: [
    { key: "school", label: "School or University", id: "school" },
    { key: "degree", label: "Degree", id: "degree" },
    { key: "major", label: "Field of Study", id: "major" },
    { key: "start", label: "From", id: "startDate" },
    { key: "end", label: "To", id: "endDate" },
  ],
};

export function mountRows(o: RowFixtureOptions): RowFixture {
  const word = o.entity === "employment" ? "Work Experience" : "Education";
  const pressed: string[] = [];
  const removedLog: string[] = [];
  let adds = 0;
  let serial = 0;
  const root = add(`<div class="section"><h3>${word}</h3><div class="rows"></div>
    ${o.noAdd ? "" : `<button type="button" class="add">Add ${word}</button>`}
    ${o.style === "paylocity" ? `<button type="button" class="section-delete">Delete This ${o.entity === "employment" ? "Work" : "Education"} History</button>` : ""}
    ${o.cancelButton ? '<button type="button" class="cancel">Cancel</button><button type="button" class="next">Next</button>' : ""}</div>`);
  const holder = root.querySelector(".rows") as HTMLElement;
  const rowEls: HTMLElement[] = [];
  const idFor = (f: { id: string }, n: number): { id: string; name: string } => {
    const cap = f.id[0]!.toUpperCase() + f.id.slice(1);
    switch (o.style) {
      case "workday":
        return { id: `${o.entity === "employment" ? "workExperience" : "education"}-${n + 1}--${f.id}`, name: "" };
      case "dayforce":
        return { id: `jobPostingApplication_${o.entity === "employment" ? "workHistory" : "educationHistory"}_${n}_${f.id}`, name: "" };
      case "adp":
        return { id: "", name: `${o.entity === "employment" ? "employer" : "school"}${cap}_${n + 1}_x` };
      case "paylocity":
        return { id: `${o.entity === "employment" ? "workHistory" : "educationHistory"}.${n}.${f.id}`, name: "" };
      default:
        return { id: `${f.id}-${n + 1}`, name: "" };
    }
  };
  const render = () => {
    holder.innerHTML = "";
    rowEls.length = 0;
  };
  render();
  const makeRow = (values: Record<string, string>) => {
    const n = rowEls.length;
    serial++;
    const tag = o.style === "dayforce" ? "form" : "div";
    const el = document.createElement(tag);
    el.className = "row";
    if (o.style === "dayforce") el.id = `${o.entity === "employment" ? "workHistory" : "educationHistory"}-${n}`;
    const inner = FIELDS[o.entity]
      .map((f) => {
        const { id, name } = idFor(f, n);
        const label = `<label for="${id || `f${serial}-${f.id}`}">${f.label}</label>`;
        return `${label}<input ${id ? `id="${id}"` : `id="f${serial}-${f.id}"`} ${name ? `name="${name}"` : ""} value="${values[f.key] ?? ""}">`;
      })
      .join("");
    const heading = o.style === "heading" ? `<h4>${word} ${n + 1}</h4>` : "";
    const hidden = o.style === "adp" ? `<input type="hidden" name="$$${o.entity === "employment" ? "employer" : "school"}Id_${n + 1}_x" value="${n + 1}">` : "";
    const del = o.style === "paylocity" ? "" : `<button type="button" class="del" aria-label="Delete ${word} ${n + 1}">Delete</button>`;
    el.innerHTML = `${heading}${hidden}${inner}${del}`;
    el.querySelector(".del")?.addEventListener("click", () => {
      pressed.push(`delete-${n}`);
      const finish = () => {
        removedLog.push(el.querySelector("input")?.getAttribute("value") ?? (el.querySelector("input") as HTMLInputElement).value);
        el.remove();
        rowEls.splice(rowEls.indexOf(el), 1);
        maybeRecreate();
      };
      if (!o.confirm) return finish();
      const dlg = document.createElement("div");
      dlg.setAttribute("role", "dialog");
      dlg.setAttribute("aria-label", o.confirm);
      dlg.innerHTML = `<h2>${o.confirm}</h2><button type="button" class="ok">OK</button><button type="button" class="no">Cancel</button>`;
      dlg.querySelector(".ok")!.addEventListener("click", () => {
        pressed.push("confirm");
        dlg.remove();
        finish();
      });
      document.body.append(dlg);
    });
    holder.append(el);
    rowEls.push(el);
    return el;
  };
  let recreated = 0;
  const maybeRecreate = () => {
    const r = o.recreate;
    if (!r || recreated >= r.times) return;
    recreated++;
    setTimeout(() => makeRow(r.row), r.afterMs);
  };
  for (const r of o.rows) makeRow(r);
  root.querySelector(".add")?.addEventListener("click", () => {
    adds++;
    if (!o.noAdd) makeRow({});
  });
  root.querySelector(".section-delete")?.addEventListener("click", () => {
    pressed.push("section-delete");
    const last = rowEls[rowEls.length - 1];
    if (last && rowEls.length > 1) {
      last.remove();
      rowEls.pop();
    }
  });
  root.querySelector(".cancel")?.addEventListener("click", () => pressed.push("cancel"));
  root.querySelector(".next")?.addEventListener("click", () => pressed.push("next"));
  const fieldKeys = FIELDS[o.entity].map((f) => f.key);
  return {
    root,
    values: () => rowEls.map((r) => Object.fromEntries(Array.from(r.querySelectorAll("input:not([type=hidden])")).map((i, k) => [fieldKeys[k] as string, (i as HTMLInputElement).value]))),
    addClicks: () => adds,
    removed: () => removedLog,
    pressed,
    addRow: (v) => makeRow(v),
  };
}

// --- Paylocity address block ------------------------------------------------------------

/** A work-history address whose fields change shape when the country does. */
export function mountPaylocityAddress(): { fields: () => string[]; value: (id: string) => string; writes: string[] } {
  const writes: string[] = [];
  const root = add(`<div><label for="workHistory.0.country">Country</label><input id="workHistory.0.country" value="United States"><div class="block"></div></div>`);
  const block = root.querySelector(".block") as HTMLElement;
  const shape = (country: string) => {
    const us = /united states/i.test(country);
    block.innerHTML = (us
      ? [["county", "County"], ["usState", "State"], ["zip", "Zip"]]
      : [["locality", "City or Locality"], ["adminArea", "Administrative Area"], ["postalCode", "Postal Code"]]
    )
      .map(([id, label]) => `<label for="workHistory.0.${id}">${label}</label><input id="workHistory.0.${id}">`)
      .join("");
    block.querySelectorAll("input").forEach((i) => i.addEventListener("input", () => writes.push(i.id)));
  };
  shape("United States");
  const country = root.querySelector("input") as HTMLInputElement;
  country.addEventListener("input", () => setTimeout(() => shape(country.value), 3));
  return { fields: () => Array.from(block.querySelectorAll("input")).map((i) => i.id), value: (id) => (document.getElementById(id) as HTMLInputElement).value, writes };
}

// --- react-widgets and React-controlled text ----------------------------------------------

function props(el: Element, p: Record<string, unknown>): void {
  (el as unknown as Record<string, unknown>)["__reactProps$test"] = p;
}

export interface RwDropdown {
  root: HTMLElement;
  shown: () => string;
  /** Clicks that reached the DOM (a delegated handler would see them; this widget does not). */
  domClicks: () => number;
  opens: () => number;
}

/** A DropdownList whose open and option commit exist only as the component's own onClick props. */
export function mountRwDropdown(o: { id: string; label: string; options: string[]; onCommit?: (label: string) => void }): RwDropdown {
  const wrap = add(`<div class="rw-wrap"><label id="${o.id}-l" for="${o.id}">${o.label}</label>
    <div id="${o.id}" class="rw-dropdownlist rw-widget" role="combobox" aria-haspopup="listbox" aria-labelledby="${o.id}-l" tabindex="0"><span class="rw-dropdownlist-value">Select...</span></div></div>`);
  const root = wrap.querySelector(".rw-dropdownlist") as HTMLElement;
  const value = root.querySelector(".rw-dropdownlist-value") as HTMLElement;
  let domClicks = 0;
  let opens = 0;
  document.addEventListener("click", () => domClicks++, true);
  props(root, {
    onClick: () => {
      opens++;
      if (wrap.querySelector(".rw-popup-container")) return;
      const pop = document.createElement("div");
      pop.className = "rw-popup-container";
      pop.innerHTML = `<ul class="rw-list" role="listbox">${o.options.map((t) => `<li class="rw-list-option" role="option">${t}</li>`).join("")}</ul>`;
      pop.querySelectorAll("li").forEach((li) =>
        props(li, {
          onClick: () => {
            value.textContent = li.textContent ?? "";
            pop.remove();
            o.onCommit?.(li.textContent ?? "");
          },
        }),
      );
      wrap.append(pop);
    },
    onKeyDown: (e: { key: string }) => {
      if (e.key === "Escape") wrap.querySelector(".rw-popup-container")?.remove();
    },
  });
  return { root, shown: () => (value.textContent === "Select..." ? "" : (value.textContent ?? "")), domClicks: () => domClicks, opens: () => opens };
}

/** A text field whose React state follows only its onChange handler; a native event alone is reverted. */
export function mountControlledText(o: { id: string; label: string }): { input: HTMLInputElement; state: () => string; calls: string[] } {
  const wrap = add(`<div><label for="${o.id}">${o.label}</label><input id="${o.id}"></div>`);
  const input = wrap.querySelector("input") as HTMLInputElement;
  let state = "";
  const calls: string[] = [];
  props(input, {
    onChange: (e: { persist?: () => void; target: HTMLInputElement }) => {
      // The handler throws without persist(), as some do.
      e.persist!();
      state = e.target.value;
      calls.push("change");
    },
    onBlur: (e: { persist?: () => void }) => {
      e.persist!();
      calls.push("blur");
    },
  });
  input.addEventListener("input", () => {
    setTimeout(() => {
      if (input.value !== state) input.value = state;
    }, 1);
  });
  return { input, state: () => state, calls };
}

/** Paylocity's own select: ArrowDown opens the menu after a delay, options commit on mouse down, and the display renders only after blur. */
export function mountPctySelect(o: { id: string; label: string; options: string[]; dead?: boolean }): { input: HTMLInputElement; shown: () => string; clicks: () => number } {
  const wrap = add(`<div><label for="${o.id}">${o.label}</label><div class="pcty-input-select"><input id="${o.id}" role="combobox" aria-autocomplete="list"></div></div>`);
  const root = wrap.querySelector(".pcty-input-select") as HTMLElement;
  const input = wrap.querySelector("input") as HTMLInputElement;
  let chosen = "";
  let clicks = 0;
  root.addEventListener("click", () => clicks++, true);
  const render = () => {
    root.querySelector(".input-select-input-single-value")?.remove();
    if (chosen && document.activeElement !== input) {
      const d = document.createElement("div");
      d.className = "input-select-input-single-value";
      d.textContent = chosen;
      root.append(d);
    }
  };
  props(input, {
    onKeyDown: (e: { key: string }) => {
      if (e.key !== "ArrowDown") return;
      setTimeout(() => {
        const menu = document.createElement("div");
        menu.className = "input-select-menu";
        menu.innerHTML = o.options.map((t) => `<div class="input-select-option" role="option">${t}</div>`).join("");
        // A dead menu's options carry no handler at all.
        if (!o.dead) menu.querySelectorAll(".input-select-option").forEach((opt) =>
          props(opt, {
            onMouseDown: () => {
              chosen = opt.textContent ?? "";
              menu.remove();
            },
          }),
        );
        root.append(menu);
      }, 20);
    },
    onBlur: () => render(),
  });
  input.addEventListener("blur", render);
  return { input, shown: () => root.querySelector(".input-select-input-single-value")?.textContent ?? "", clicks: () => clicks };
}
