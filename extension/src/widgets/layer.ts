// A page-level surface in a closed shadow root, so the page's styles and
// scripts can neither restyle nor read it.

export interface Layer {
  box: HTMLElement;
  /** False once the page replaced its body and took the surface with it. */
  alive(): boolean;
  destroy(): void;
}

const BASE_CSS = `
:host { all: initial; --bg: #fff; --fg: #1b1f24; --muted: #5b6570; --line: #d5dae0; --accent: #1f5fbf; --bad: #a3261e; --ok: #176b3a; }
@media (prefers-color-scheme: dark) { :host { --bg: #1c2128; --fg: #e6e9ed; --muted: #9aa5b1; --line: #3a424c; --accent: #6aa5ff; --bad: #ff8a80; --ok: #5fcf8a; } }
* { box-sizing: border-box; }
.surface { font: 13px/1.45 system-ui, sans-serif; color: var(--fg); }
button { font: inherit; color: var(--fg); background: var(--bg); border: 1px solid var(--line); border-radius: 6px; padding: 4px 10px; cursor: pointer; }
button:hover:not(:disabled) { border-color: var(--accent); } button:disabled { opacity: .5; cursor: default; }
input, textarea { font: inherit; color: var(--fg); background: var(--bg); border: 1px solid var(--line); border-radius: 6px; padding: 5px 8px; width: 100%; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.muted { color: var(--muted); } .bad { color: var(--bad); } .ok { color: var(--ok); }
@media (prefers-reduced-motion: reduce) { * { transition: none !important; animation: none !important; } }
`;

export function createLayer(doc: Document, css = ""): Layer {
  const host = doc.createElement("div");
  host.setAttribute("data-applysmith", "");
  const root = host.attachShadow({ mode: "closed" });
  const style = doc.createElement("style");
  style.textContent = BASE_CSS + css;
  const box = doc.createElement("div");
  box.className = "surface";
  root.append(style, box);
  (doc.body ?? doc.documentElement).append(host);
  return { box, alive: () => host.isConnected, destroy: () => host.remove() };
}
