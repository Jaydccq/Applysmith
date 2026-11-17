export const PANEL_CSS = `
:host { all: initial; }
* { box-sizing: border-box; }
.launcher, .panel { position: fixed; z-index: 2147483647; font: 13px/1.45 system-ui, sans-serif; color: var(--fg); }
:host { --bg: #fff; --fg: #1b1f24; --muted: #5b6570; --line: #d5dae0; --accent: #1f5fbf; --ok: #176b3a; --bad: #a3261e; }
@media (prefers-color-scheme: dark) { :host { --bg: #1c2128; --fg: #e6e9ed; --muted: #9aa5b1; --line: #3a424c; --accent: #6aa5ff; --ok: #5fcf8a; --bad: #ff8a80; } }
.launcher { right: 16px; bottom: 16px; width: 40px; height: 40px; border-radius: 20px; border: 1px solid var(--line); background: var(--bg); color: var(--accent); font-weight: 700; cursor: pointer; box-shadow: 0 2px 8px rgba(0,0,0,.25); }
.panel { right: 16px; bottom: 16px; width: 380px; max-height: min(560px, calc(100vh - 32px)); display: flex; flex-direction: column; background: var(--bg); border: 1px solid var(--line); border-radius: 10px; box-shadow: 0 6px 24px rgba(0,0,0,.3); overflow: hidden; transition: opacity .12s ease; }
.panel[hidden], .launcher[hidden] { display: none; }
.panel[data-layout=sheet] { left: 0; right: 0; bottom: 0; width: auto; max-height: 70vh; border-radius: 12px 12px 0 0; }
@media (prefers-reduced-motion: reduce) { .panel { transition: none; } }
.head { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border-bottom: 1px solid var(--line); cursor: grab; touch-action: none; }
.head h1 { font-size: 13px; margin: 0; flex: 1; }
.pill { font-size: 11px; padding: 1px 8px; border-radius: 10px; border: 1px solid var(--line); }
.pill.connected { color: var(--ok); } .pill.offline, .pill.unpaired { color: var(--bad); }
[role=tablist] { display: flex; border-bottom: 1px solid var(--line); }
[role=tab] { flex: 1; padding: 7px 4px; font: inherit; color: var(--muted); background: transparent; border: 0; border-bottom: 2px solid transparent; cursor: pointer; }
[role=tab][aria-selected=true] { color: var(--fg); border-bottom-color: var(--accent); }
[role=tabpanel] { padding: 10px 12px; overflow: auto; }
button.act { padding: 5px 10px; font: inherit; color: var(--fg); background: transparent; border: 1px solid var(--line); border-radius: 6px; cursor: pointer; margin: 0 6px 6px 0; }
button.act:hover:not(:disabled) { border-color: var(--accent); } button:disabled { opacity: .5; cursor: default; }
button.act.primary { background: var(--accent); border-color: var(--accent); color: var(--bg); font-weight: 600; }
button.link { padding: 0; font: inherit; color: var(--accent); background: transparent; border: 0; text-decoration: underline; cursor: pointer; }
button.close { border: 0; background: transparent; color: var(--muted); font-size: 16px; cursor: pointer; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.muted { color: var(--muted); } .bad { color: var(--bad); } .ok { color: var(--ok); }
table { width: 100%; border-collapse: collapse; } td { padding: 2px 4px; vertical-align: top; border-top: 1px solid var(--line); }
ul.list { margin: 4px 0; padding-left: 18px; }
.card { border: 1px solid var(--line); border-radius: 8px; padding: 8px; margin: 6px 0 10px; }
.q { margin-top: 8px; }
.preview { display: block; width: 100%; max-height: 240px; margin: 6px 0; padding: 5px 8px; font: inherit; color: var(--fg); background: transparent; border: 1px solid var(--line); border-left: 3px solid var(--accent); border-radius: 6px; resize: vertical; }
ol.steps { margin: 6px 0; padding-left: 20px; } ol.steps li[aria-current=step] { font-weight: 700; }
input[type=text] { width: 100%; padding: 5px 8px; font: inherit; color: var(--fg); background: transparent; border: 1px solid var(--line); border-radius: 6px; margin: 4px 0; }
a { color: var(--accent); }
`;
