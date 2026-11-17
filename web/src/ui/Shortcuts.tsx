import { useRef } from "react";
import { PAGES } from "../pages";
import { Link } from "../router";
import { Modal } from "./Modal";

// One command registry drives the shell's keyboard handler, the palette's
// Actions group and the shortcuts list, so a key the list shows is a key that
// works. A binding names its key as KeyboardEvent.key does, after an optional
// "Mod" (Cmd on a Mac, Ctrl elsewhere), "Alt" and "Shift". Every binding is a
// chord with Mod: a single-character key fires while someone clicks or types
// elsewhere and cannot be turned off (WCAG 2.1.4). No command submits or
// approves anything: they open, move and show.
export interface Command {
  id: string;
  title: string;
  keys: readonly string[];
  /** Fires while a text field has the keyboard; only a Mod chord may. */
  inFields?: boolean;
  /** Fires over the run drawer, the one dialog it may leave. */
  overDrawer?: boolean;
  /** Listed under Actions in the palette. */
  palette?: boolean;
  run: () => void;
}

interface CommandHost {
  openPalette: () => void;
  openShortcuts: () => void;
  nextNeedingYou: () => void;
  cycleTheme: () => void;
  toggleSidebar: () => void;
  toggleBrowser: () => void;
  showNotices: () => void;
  go: (path: string) => void;
}

// focusSearch moves the keyboard to the page's own search field, the one that
// carries data-page-search (Mod+/ is the shortcuts list, Mod+F the browser's).
function focusSearch() {
  const field = document.querySelector<HTMLInputElement>("main [data-page-search]");
  field?.focus();
  field?.select();
}

// openAt opens a page and moves the keyboard to the control `selector` finds
// there once the page has rendered it: Home's paste box, or the Start of the
// first source on Discovery that can start (the scan launcher; the press stays
// the user's).
function openAt(host: CommandHost, path: string, selector: string) {
  host.go(path);
  let tries = 0;
  const look = () => {
    const box = document.querySelector<HTMLElement>(selector);
    if (box) box.focus();
    else if (++tries < 40) setTimeout(look, 50);
  };
  look();
}

export function commands(host: CommandHost): Command[] {
  return [
    { id: "palette", title: "Search pages, settings, applications, reports, runs, conversations and actions", keys: ["Mod+K"], inFields: true, run: host.openPalette },
    { id: "next", title: "Go to the next item needing you", keys: ["Mod+Shift+N"], overDrawer: true, palette: true, run: host.nextNeedingYou },
    { id: "shortcuts", title: "Show keyboard shortcuts", keys: ["Mod+/"], palette: true, run: host.openShortcuts },
    { id: "search", title: "Focus the search field of this page", keys: ["Mod+Shift+F"], inFields: true, run: focusSearch },
    { id: "post", title: "Evaluate a job post", keys: [], palette: true, run: () => openAt(host, "/", "#home-post") },
    { id: "scan", title: "Start a scan", keys: [], palette: true, run: () => openAt(host, "/discovery", ".source-list button[aria-label^='Start ']:not(:disabled)") },
    { id: "notices", title: "Show notices", keys: [], palette: true, run: host.showNotices },
    { id: "browser", title: "Show or hide the browser panel", keys: ["Mod+Shift+B"], palette: true, run: host.toggleBrowser },
    { id: "sidebar", title: "Narrow the sidebar to icons, or show the page names", keys: [], palette: true, run: host.toggleSidebar },
    { id: "theme", title: "Change the theme (auto, light or dark mode)", keys: [], palette: true, run: host.cycleTheme },
    // The first nine pages in sidebar order, then Settings.
    ...PAGES.slice(0, 9).map((p, i) => ({ id: `go:${p.path}`, title: `Go to ${p.title}`, keys: [`Mod+${i + 1}`], run: () => host.go(p.path) })),
    { id: "go:/settings", title: "Go to Settings", keys: ["Mod+,"], run: () => host.go("/settings") },
    { id: "back", title: "Go back", keys: ["Mod+["], run: () => window.history.back() },
    { id: "forward", title: "Go forward", keys: ["Mod+]"], run: () => window.history.forward() },
  ];
}

function parse(binding: string) {
  const parts = binding.split("+");
  return { key: parts[parts.length - 1].toLowerCase(), mod: parts.includes("Mod"), alt: parts.includes("Alt"), shift: parts.includes("Shift") };
}

/** canonical is the form two bindings share when they press the same keys. */
export function canonical(binding: string): string {
  const { key, mod, alt, shift } = parse(binding);
  return `${mod ? "Mod+" : ""}${alt ? "Alt+" : ""}${shift ? "Shift+" : ""}${key}`;
}

const mac = () => typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform ?? "");

// A binding that names Shift needs it; one that does not ignores it, because
// on some layouts Shift is how its key is typed. On a Mac Mod is Cmd alone:
// there Ctrl+K in a text field deletes to the end of the line.
export function matches(e: KeyboardEvent, binding: string): boolean {
  const { key, mod, alt, shift } = parse(binding);
  return e.key.toLowerCase() === key && mod === (e.metaKey || (!mac() && e.ctrlKey)) && alt === e.altKey && (!shift || e.shiftKey);
}

export function keyLabel(binding: string): string {
  return binding.replace("Mod", mac() ? "Cmd" : "Ctrl");
}

/** ariaKeys is the aria-keyshortcuts value of a binding. */
export function ariaKeys(binding: string): string {
  const rest = binding.replace(/^Mod\+/, "");
  return rest === binding ? binding : `Meta+${rest} Control+${rest}`;
}

// Keys the browser or a widget answers itself, listed so the dialog is the
// whole story.
const NATIVE: [string, string][] = [
  ["Tab and Shift+Tab", "Move between controls"],
  ["Enter or Space", "Activate the focused control"],
  ["Escape", "Close a dialog or drawer without acting"],
  [keyLabel("Mod+S"), "Save the form you changed, where a save bar shows, and an editor in a dialog or drawer"],
];

export function Shortcuts({ list, onClose }: { list: readonly Command[]; onClose: () => void }) {
  // The numbered pages read as one row, in place of the first of them.
  const numbered = list.filter((c) => /^Mod\+\d$/.test(c.keys[0] ?? ""));
  const rows = list.flatMap((c): [string, string][] => {
    if (c.keys.length === 0 || (numbered.includes(c) && c !== numbered[0])) return [];
    if (c === numbered[0]) return [[`${keyLabel(c.keys[0])} to ${keyLabel(numbered[numbered.length - 1].keys[0])}`, `Go to the first ${numbered.length} pages, in sidebar order`]];
    return [[c.keys.map(keyLabel).join(" or "), c.title]];
  });
  rows.push(...NATIVE);
  // Focus starts on Close, not on the guide link inside the text.
  const close = useRef<HTMLButtonElement>(null);
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} initialFocus={close}>
      <p>Only search works while a field has the keyboard, and an open dialog keeps its own keys.</p>
      <dl className="facts">
        {rows.map(([keys, what]) => (
          <div key={keys} style={{ display: "contents" }}>
            <dt>
              <kbd>{keys}</kbd>
            </dt>
            <dd>{what}</dd>
          </div>
        ))}
      </dl>
      <p className="muted">
        More help: the <Link to="/guide" onClick={onClose}>user guide</Link>.
      </p>
      <div className="actions">
        <button type="button" ref={close} onClick={onClose}>
          Close
        </button>
      </div>
    </Modal>
  );
}
