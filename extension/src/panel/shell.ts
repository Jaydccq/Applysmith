// The floating panel: a closed shadow root attached to the page, so page
// styles and scripts can neither restyle nor read it.
import { h } from "../shared/dom";
import type { Connection, UiState } from "../shared/messages";
import { PANEL_CSS } from "./style";
import { mountTabs, type TabSpec, type TabsHandle } from "./tabs";

export const SHEET_MAX_WIDTH = 480;

export interface TabDef extends TabSpec {
  mount(panel: HTMLElement): void;
}

export interface Ui {
  get(): Promise<UiState>;
  set(patch: UiState): void;
}

export interface ShellOptions {
  doc: Document;
  win: Window;
  tabs: TabDef[];
  ui: Ui;
  /** True when the user switched the extension off on this host. */
  off: boolean;
  /** The tab shown when nothing was remembered for this host. */
  defaultTab?: string;
  /** Called when the panel opens or closes, for work that should run only while it is open. */
  onVisibility?(open: boolean): void;
}

export interface Shell {
  /** Held by the extension's own code only; the page gets no reference. */
  root: ShadowRoot;
  open(focus?: boolean): void;
  close(): void;
  toggle(focus?: boolean): void;
  isOpen(): boolean;
  /** Whether the panel's root is still in the page; a soft navigation can take it out. */
  attached(): boolean;
  setConnection(c: Connection): void;
  /** The extension was reloaded under this page: nothing in the panel can reach it any more. */
  stale(): void;
  tabs: TabsHandle;
  destroy(): void;
}

const LABEL: Record<Connection, string> = { connected: "Connected", unpaired: "Not paired", offline: "Offline" };

/** The event a drawn tab's panel receives when it is shown again. */
export const SHOWN = "applysmith-shown";

/** The event a tab's Check again button sends up to the panel, which asks the worker for the connection again. */
export const RECHECK = "applysmith-recheck";

/**
 * What a tab shows instead of its content while the application cannot be used, and the way back.
 * `checked`: drawn for the answer to Check again, which says so as the popup does.
 */
export function notConnected(c: Connection, checked = false): HTMLElement {
  // Keyed, so a redraw puts focus back on it although "Checking..." was its label.
  const again = h("button", { class: "act", type: "button", "data-key": "recheck" }, "Check again");
  again.addEventListener("click", () => {
    again.disabled = true;
    again.textContent = "Checking...";
    again.dispatchEvent(new CustomEvent(RECHECK, { bubbles: true }));
  });
  // The words are live, not the button.
  return h(
    "div",
    {},
    h(
      "p",
      { class: "bad", role: "status" },
      c === "unpaired"
        ? "Not paired. Click the Applysmith icon in the browser toolbar and type the pairing code the dashboard shows under Settings, Browser extension."
        : "The Applysmith application is not running. Open it, then press Check again. If it is open, pair again in the toolbar popup with a new code from Settings, Browser extension.",
    ),
    checked && h("p", { class: "muted", role: "status" }, "Checked just now."),
    again,
  );
}

export async function createShell(opts: ShellOptions): Promise<Shell> {
  const { doc, win } = opts;
  const host = doc.createElement("div");
  host.setAttribute("data-applysmith", "");
  const root = host.attachShadow({ mode: "closed" });
  const style = doc.createElement("style");
  style.textContent = PANEL_CSS;

  const saved = await opts.ui.get().catch((): UiState => ({}));

  const launcher = doc.createElement("button");
  launcher.className = "launcher";
  launcher.type = "button";
  launcher.textContent = "AJ";
  launcher.setAttribute("aria-label", "Open Applysmith");
  launcher.hidden = opts.off;

  const panel = doc.createElement("section");
  panel.className = "panel";
  panel.setAttribute("role", "region");
  panel.setAttribute("aria-label", "Applysmith");
  panel.hidden = true;

  const head = doc.createElement("div");
  head.className = "head";
  const title = doc.createElement("h1");
  title.textContent = "Applysmith";
  const pill = doc.createElement("span");
  pill.className = "pill offline";
  pill.setAttribute("role", "status");
  const closeBtn = doc.createElement("button");
  closeBtn.className = "close";
  closeBtn.type = "button";
  closeBtn.setAttribute("aria-label", "Close panel");
  closeBtn.textContent = "×";
  head.append(title, pill, closeBtn);
  const body = doc.createElement("div");
  panel.append(head, body);

  // A tab draws itself the first time it is shown, and hears each later showing.
  const mounted = new Set<string>();
  const show = (id: string) => {
    const def = opts.tabs.find((d) => d.id === id);
    if (!def) return;
    const el = tabs.panels.get(id)!;
    if (mounted.has(id)) el.dispatchEvent(new Event(SHOWN));
    else {
      mounted.add(id);
      def.mount(el);
    }
  };
  const first = [saved.tab, opts.defaultTab].find((id) => opts.tabs.some((d) => d.id === id)) ?? opts.tabs[0]!.id;
  const tabs = mountTabs(body, opts.tabs, first, (id) => {
    opts.ui.set({ tab: id });
    show(id);
  });
  show(tabs.selected());

  const layout = () => {
    panel.dataset.layout = win.innerWidth <= SHEET_MAX_WIDTH ? "sheet" : "float";
  };
  layout();
  win.addEventListener("resize", layout);

  const place = (pos?: { x: number; y: number }) => {
    if (!pos || panel.dataset.layout === "sheet") return;
    const x = Math.min(Math.max(0, pos.x), Math.max(0, win.innerWidth - 120));
    const y = Math.min(Math.max(0, pos.y), Math.max(0, win.innerHeight - 60));
    Object.assign(panel.style, { left: `${x}px`, top: `${y}px`, right: "auto", bottom: "auto" });
  };
  place(saved.pos);

  // Dragging by the header; the position is remembered per host.
  head.addEventListener("pointerdown", (e) => {
    if ((e.target as Element).closest("button") || panel.dataset.layout === "sheet") return;
    const rect = panel.getBoundingClientRect();
    const dx = e.clientX - rect.left;
    const dy = e.clientY - rect.top;
    const move = (m: PointerEvent) => place({ x: m.clientX - dx, y: m.clientY - dy });
    const up = (m: PointerEvent) => {
      win.removeEventListener("pointermove", move);
      win.removeEventListener("pointerup", up);
      opts.ui.set({ pos: { x: Math.max(0, m.clientX - dx), y: Math.max(0, m.clientY - dy) } });
    };
    win.addEventListener("pointermove", move);
    win.addEventListener("pointerup", up);
  });

  let isOpen = false;
  const shell: Shell = {
    root,
    tabs,
    open(focus = false) {
      if (isOpen) return;
      isOpen = true;
      panel.hidden = false;
      launcher.hidden = true;
      opts.ui.set({ open: true });
      opts.onVisibility?.(true);
      // An automatic open leaves the page's focused element alone.
      if (focus) tabs.focus();
    },
    close() {
      if (!isOpen) return;
      isOpen = false;
      // Focus inside the panel would be lost with it: the launcher takes it back.
      const hadFocus = panel.contains(root.activeElement);
      panel.hidden = true;
      launcher.hidden = opts.off;
      if (hadFocus && !launcher.hidden) launcher.focus();
      opts.ui.set({ open: false });
      opts.onVisibility?.(false);
    },
    toggle(focus = true) {
      if (isOpen) shell.close();
      else shell.open(focus);
    },
    isOpen: () => isOpen,
    attached: () => host.isConnected,
    setConnection(c) {
      pill.className = `pill ${c}`;
      pill.textContent = LABEL[c];
    },
    stale() {
      pill.className = "pill offline";
      pill.textContent = "Reload page";
      body.replaceChildren(
        h("p", { class: "bad", role: "alert" }, "The Applysmith extension was updated or reloaded, so this panel can no longer reach it. Reload this page to use it; reloading clears what you typed into the form."),
      );
    },
    destroy() {
      win.removeEventListener("resize", layout);
      host.remove();
    },
  };
  launcher.addEventListener("click", () => shell.open(true));
  closeBtn.addEventListener("click", () => shell.close());
  panel.addEventListener("keydown", (e) => {
    // An Escape that ends an input method's composition (pinyin, kana) belongs to it, not to the panel.
    if (e.key === "Escape" && !e.isComposing) {
      e.stopPropagation();
      shell.close();
    }
  });

  root.append(style, launcher, panel);
  (doc.body ?? doc.documentElement).append(host);
  return shell;
}
