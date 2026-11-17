// Clean-room: docs/provenance.md#CR-ENGINE
// Protecting what the run wrote: the user's own edits are tracked so they are
// never overwritten, and a restore pass puts back values a framework blanked
// after the write, whichever lane wrote them.

import { applyValue } from "./apply";
import { displayMatches } from "./choosers";
import type { Win } from "./dom";
import { Background } from "./background";
import { StopSignal, type Timing, type WriteCtx } from "./env";
import type { Row, RowKind } from "./protocol";
import type { TailGuard } from "./refill";
import type { RowSweeper } from "./sections";
import type { RunLog } from "./run";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import { groupSelector, resolveSelector, selectorFor } from "./selector";
import { engineEditing } from "./writers";

const CONTROL = "input, select, textarea, [contenteditable], [role=combobox], [role=switch], [role=checkbox], [role=radio], button";
/** A box the page draws itself: a click answers it, as it answers a native checkbox or radio. */
const TOGGLE = "[role=switch], [role=checkbox], [role=radio], button[aria-pressed]";

/** Keys that change no control: they move the focus, are a modifier on its own, close a popup or belong to the browser. */
const IDLE_KEY = /^(Tab|Shift|Control|Alt|AltGraph|Meta|CapsLock|NumLock|ScrollLock|Escape|ContextMenu|F\d{1,2})$/;
/** Keys that only move the caret of a text box. */
const CARET_KEY = /^(Arrow(Left|Right|Up|Down)|Home|End|PageUp|PageDown)$/;
/** What makes a text box a search box whose arrows walk a list. */
const POPUP_ATTRS = ["role", "list", "aria-autocomplete", "aria-haspopup", "aria-expanded", "aria-controls", "aria-owns"];

/**
 * Whether a key press may have changed the control. A key that proves nothing (a browser that names none) counts;
 * a chord with Control or Command does not, since what it changes arrives as a cut, a paste or an input event.
 */
function keyEdits(ev: Event, control: Element, box: boolean): boolean {
  const { key, ctrlKey, metaKey } = ev as KeyboardEvent;
  if (!key) return true;
  if (IDLE_KEY.test(key) || ctrlKey || metaKey) return false;
  const text = control.localName === "textarea" || control.hasAttribute("contenteditable") || (control.localName === "input" && !box);
  return !(text && CARET_KEY.test(key) && !POPUP_ATTRS.some((a) => control.hasAttribute(a)));
}

/**
 * Trusted keyboard, paste, cut and input events mark a control as the user's,
 * except those of the engine's own edit through the browser (engineEditing)
 * and the keys that change nothing (Tab, a bare modifier, a text box's caret keys).
 * `change` is not counted, because frameworks synthesise it. A checkbox or
 * radio is the user's by its trusted click instead: the browser marks the
 * input event of any scripted click() trusted, the engine's own tick and the
 * page's answer to another control alike (Phenom), while a person's click, on
 * the box or its label, and a Space key reach the box as a trusted click. A
 * box the page draws itself (an ARIA switch, checkbox or radio, a button with
 * a pressed state) is the user's by a trusted click too.
 * Keyed by selector because a re-render replaces the node.
 */
export class UserEdits {
  private readonly selectors = new Set<string>();
  private off: (() => void) | null = null;
  /** Set while the host drives trusted input itself (the apply chain's computer-use rung): nothing it does is the user's. */
  held = false;

  constructor(private readonly doc: Document) {}

  start(): void {
    if (this.off) return;
    const handler = (ev: Event) => {
      if (!ev.isTrusted || this.held || engineEditing()) return;
      const origin = (ev.composedPath?.()[0] ?? ev.target) as Element | null;
      const control = origin?.closest?.(CONTROL);
      if (!control) return;
      const box = control.localName === "input" && ["checkbox", "radio"].includes((control as HTMLInputElement).type);
      if (box ? ev.type === "input" : ev.type === "click" && !control.matches(TOGGLE)) return;
      if (ev.type === "keydown" && !keyEdits(ev, control, box)) return;
      this.selectors.add(selectorFor(control));
      if (control.getAttribute("name") && control.localName === "input") this.selectors.add(groupSelector(control));
    };
    const types = ["input", "keydown", "paste", "cut", "click"];
    for (const t of types) this.doc.addEventListener(t, handler, true);
    this.off = () => types.forEach((t) => this.doc.removeEventListener(t, handler, true));
  }

  stop(): void {
    this.off?.();
    this.off = null;
  }

  /** Marks the controls the host knows the user changed by hand before the run. */
  mark(selectors: readonly string[]): void {
    for (const s of selectors) this.selectors.add(s);
  }

  edited(info: Pick<ControlInfo, "selector" | "members" | "toggles">): boolean {
    if (!this.selectors.size) return false;
    if (this.selectors.has(info.selector)) return true;
    // A nameless group is one control but each of its members is its own selector.
    return info.members.some((m) => this.selectors.has(selectorFor(m))) || info.toggles.some((t) => this.selectors.has(selectorFor(t)));
  }

  /** True when the user touched `root` or any control inside it. */
  editedWithin(root: Element): boolean {
    if (!this.selectors.size) return false;
    return [root, ...Array.from(root.querySelectorAll(CONTROL))].some((el) => this.selectors.has(selectorFor(el)));
  }
}

interface Entry {
  node: Element;
  value: string;
  /** The option label the writing lane committed; a listbox is re-driven with it, not with its display text. */
  label?: string;
  kind: RowKind;
  multi: boolean;
  writes: number;
  /** A restore landed a different value: the control is left alone from now on. */
  stopped: boolean;
}

export interface RestoreEnv {
  doc: Document;
  win: Win;
  timing: Timing;
  log: RunLog;
  edits: UserEdits;
  acceptLegalDeclarations: boolean;
  /** Looks for a late remount inside the same loop, so it never writes while a restore does. */
  tail?: TailGuard;
  /** What the lanes wrote, by row: the first non-empty value of a control comes from here, even if the page already blanked it. */
  rows: () => Row[];
  /** Removes again the rows the page makes again, on the same cadence as the repairs. */
  sweep?: RowSweeper;
  /** What a site driver needs while it writes a value back. */
  driver?: Pick<WriteCtx, "site" | "main">;
}

/** The most one control is written back before it is abandoned. */
const MAX_RESTORES_PER_CONTROL = 3;

export class Restorer {
  private readonly entries = new Map<string, Entry>();
  /** Selectors put back at least once. */
  readonly restored = new Set<string>();
  private readonly bg = new Background();
  private lastRestore = 0;
  /** The first look also takes what the lanes wrote, even where the page already blanked it. */
  private seedRows = false;
  private began = 0;
  /** The last look's scan, while the page is still as that look read it: a look that wrote something has none. */
  private fresh: ControlInfo[] | undefined;
  private onRestore: (selector: string) => void = () => {};

  constructor(private readonly env: RestoreEnv) {}

  /** Called with each selector as it is put back, so a summary can list them. */
  listen(fn: (selector: string) => void): void {
    this.onRestore = fn;
  }

  private ctx(): WriteCtx {
    return this.bg.ctx(this.env.doc, this.env.timing, (event, data) => this.env.log.add("restore", event, data), this.env.driver);
  }

  stop(): void {
    this.bg.stop();
    this.env.tail?.release();
  }

  /**
   * The snapshot: what the lanes wrote (a page may have blanked it already),
   * then every other control that holds a value. It grows with each look, and
   * the first non-empty value of a control stays.
   */
  private grow(infos: ControlInfo[], rows: Row[] = []): void {
    const bySelector = new Map(infos.map((i) => [i.selector, i]));
    for (const row of rows) {
      const info = bySelector.get(row.selector);
      if (!info || this.entries.has(row.selector) || row.kind === "file") continue;
      if ((row.outcome !== "filled" && row.outcome !== "prefilled") || !row.value) continue;
      this.entries.set(row.selector, {
        node: info.el,
        value: row.value,
        label: row.optionLabel,
        kind: row.kind,
        multi: info.inputType === "multi_select",
        writes: 0,
        stopped: false,
      });
    }
    for (const info of infos) {
      if (this.entries.has(info.selector) || info.kind === "file") continue;
      const value = readInfo(info);
      if (value === "") continue;
      this.entries.set(info.selector, { node: info.el, value, kind: info.kind, multi: info.inputType === "multi_select", writes: 0, stopped: false });
    }
  }

  private put(selector: string): void {
    this.restored.add(selector);
    this.onRestore(selector);
    this.lastRestore = Date.now();
  }

  /** One look: returns how many controls were put back. */
  async poll(): Promise<number> {
    const doc = this.env.doc;
    const infos = scanControls(doc);
    this.fresh = infos;
    const bySelector = new Map(infos.map((i) => [i.selector, i]));
    this.grow(infos, this.seedRows ? this.env.rows() : []);
    this.seedRows = false;
    let count = 0;
    for (const [selector, entry] of Array.from(this.entries)) {
      // Nothing is written once the pass was stopped, whatever look it was in the middle of.
      if (this.bg.stopped) throw new StopSignal("cancelled");
      // An entry lives only while its selector still resolves to the node that was written.
      const node = resolveSelector(doc, selector);
      if (!node || node !== entry.node) {
        this.entries.delete(selector);
        continue;
      }
      // Hidden, disabled or detached controls wait: a cascade child comes back after its parent commits.
      const info = bySelector.get(selector);
      if (!info || entry.stopped) continue;
      if (this.env.edits.edited(info)) {
        this.entries.delete(selector);
        continue;
      }
      if (readInfo(info) !== "" || entry.multi) continue;
      if (entry.writes >= MAX_RESTORES_PER_CONTROL) {
        entry.stopped = true;
        this.env.log.add("restore", "abandoned", { selector });
        continue;
      }
      entry.writes++;
      this.fresh = undefined;
      const r = await applyValue(
        info,
        { value: entry.label ?? entry.value, optionLabel: entry.label, replaceDefault: true },
        { ctx: this.ctx(), written: new Map(), mode: "replace", acceptLegalDeclarations: this.env.acceptLegalDeclarations, edits: this.env.edits },
      );
      const ok = r.outcome === "filled" || r.outcome === "prefilled";
      if (ok) {
        this.put(selector);
        count++;
        this.env.log.add("restore", "restored", { selector });
      } else {
        this.env.log.add("restore", "failed", { selector, reason: r.reason ?? "" });
      }
      // A listbox that now holds some other option is not fought: it is checked against the label the lane committed.
      if (entry.kind === "custom-listbox") {
        const shown = readInfo(info);
        if (shown !== "" && !displayMatches(shown, entry.label ?? entry.value)) {
          entry.stopped = true;
          this.env.log.add("restore", "landed-different", { selector });
        }
      }
    }
    return count;
  }

  /** The first look, after one poll interval: a wipe that follows the write closely is repaired before the summary. */
  async first(): Promise<void> {
    this.began = Date.now();
    this.env.tail?.begin();
    this.seedRows = true;
    await this.bg.sleep(this.env.timing.restorePollMs);
    try {
      await this.poll();
    } catch (e) {
      // The fill is finished: a look that fails costs the repair, never the summary.
      if (!(e instanceof StopSignal)) this.env.log.add("restore", "error", { message: e instanceof Error ? e.message : String(e) });
    }
  }

  /**
   * Keeps looking in the background: it ends 40 s after the last restore once
   * one has happened, or at 90 s in all, and never delays the summary. The
   * end and its reason go to the diagnostic log.
   */
  async watch(): Promise<void> {
    const t = this.env.timing;
    let reason = "stopped";
    try {
      while (!this.bg.stopped) {
        await this.bg.sleep(t.restorePollMs);
        if (this.bg.stopped) break;
        await this.poll();
        await this.env.tail?.step(this.ctx(), (selector) => this.put(selector), this.fresh);
        await this.env.sweep?.step(this.ctx());
        const now = Date.now();
        // The tail guard's and the sweep's own windows are part of the pass: the idle rule does not cut them short.
        const tailing = (this.env.tail && !this.env.tail.finished) || (this.env.sweep && !this.env.sweep.done);
        if (!tailing && this.restored.size > 0 && now - this.lastRestore >= t.restoreIdleMs) {
          reason = "idle";
          break;
        }
        if (now - this.began >= t.restoreCapMs) {
          reason = "cap";
          break;
        }
      }
    } catch (e) {
      if (!(e instanceof StopSignal)) {
        reason = "error";
        this.env.log.add("restore", "error", { message: e instanceof Error ? e.message : String(e) });
      }
    }
    this.env.tail?.release(reason);
    this.env.log.add("restore", "end", { reason });
  }
}
