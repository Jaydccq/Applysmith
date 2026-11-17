// Clean-room: docs/provenance.md#CR-ENGINE
// After a fill the person finishes the form, and what they change is the best
// answer the system will ever see. Only trusted input counts (the engine's own
// writes never do, nor the trusted input the host drives under a hold), a typed control is captured when it is left and never on a
// timer, a menu or a box that ticks is captured a moment after it changed,
// a custom listbox is read by polling and attributed to a person only when a
// trusted pointer or key reached the page just before, and nothing of a
// sensitive control's value is kept.

import { captureDrop } from "./capture-filter";
import { clickLabel } from "./click-guard";
import { collapse, type Win } from "./dom";
import type { Timing } from "./env";
import { controlSnapshot } from "./fragment";
import { baseEvent, eventId, valueWithheld } from "./learning";
import type { LearningEvent, Row } from "./protocol";
import { readElement } from "./reader";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import { resolveSelector } from "./selector";
import { engineEditing } from "./writers";

const POINTER_KEYS = ["pointerdown", "mousedown", "touchstart", "keydown"];
/** What a diagnostic about a control the engine must never touch says: the kind, never the label. */
const UNSAFE: [string, RegExp][] = [
  ["submit", /\b(submit|apply now|finish|place order)\b/i],
  ["withdraw", /\bwithdraw\b/i],
  ["next", /\bnext\b/i],
  ["continue", /\bcontinue\b/i],
  ["review", /\breview\b/i],
  ["captcha", /\b(captcha|i am not a robot|i'm not a robot)\b/i],
];

export interface WatchDeps {
  doc: Document;
  win: Win;
  timing: Timing;
  site: string | null;
  /** The rows of the fill: what the engine wrote and decided. */
  rows: Row[];
}

/** The element an event began at: inside an open shadow root the document hears it at the host, and only the composed path names the control. */
function controlOf(ev: Event): Element | null {
  const target = ev.composedPath?.()[0] ?? ev.target;
  return target instanceof Element ? target : null;
}

/** The diagnostics kept between two drains: a host that drains rarely holds no unbounded list. */
export const MAX_DIAGNOSTICS = 100;

export class CorrectionWatcher {
  private readonly captures = new Map<string, LearningEvent>();
  private readonly diagnostics: LearningEvent[] = [];
  private readonly dirty = new Set<Element>();
  private readonly timers = new Map<Element, ReturnType<typeof setTimeout>>();
  private readonly attached = new Map<string, string>();
  /** What the host wrote after the fill (its page check), by selector: an edit measures against it as against the fill's own write. */
  private readonly late = new Map<string, Row>();
  private readonly listboxes = new Map<string, string>();
  private readonly scanned = new Set<string>();
  private revealTimer: ReturnType<typeof setTimeout> | undefined;
  private revealObserver: MutationObserver | null = null;
  private lastTrusted = 0;
  /** The boxes a trusted click reached: a box's change is a person's only after one. */
  private readonly clicked = new WeakSet<Element>();
  private readonly typedKinds = new Set<string>();
  private poll: ReturnType<typeof setInterval> | undefined;
  private off: (() => void)[] = [];
  private running = false;
  private held = false;

  constructor(private readonly env: WatchDeps) {}

  /** Starts watching; the values the controls hold now are what a later change is measured against. */
  start(): void {
    if (this.running) return;
    this.running = true;
    for (const info of scanControls(this.env.doc)) {
      const v = readInfo(info);
      this.attached.set(info.selector, v);
      if (info.kind === "custom-listbox") this.listboxes.set(info.selector, v);
      this.scanned.add(info.selector);
    }
    for (const row of this.env.rows) this.scanned.add(row.selector);
    this.watchReveals();
    const doc = this.env.doc;
    const on = (target: EventTarget, type: string, fn: (ev: Event) => void) => {
      target.addEventListener(type, fn, true);
      this.off.push(() => target.removeEventListener(type, fn, true));
    };
    // What the host's own input does while it holds the watch (hold), and the engine's own edit, is no person's.
    const person = (fn: (ev: Event) => void) => (ev: Event) => {
      if (!this.held && !engineEditing()) fn(ev);
    };
    for (const t of POINTER_KEYS) on(doc, t, person((ev) => ev.isTrusted && (this.lastTrusted = Date.now())));
    on(doc, "input", person((ev) => this.onInput(ev)));
    on(doc, "change", person((ev) => this.onChange(ev)));
    on(doc, "focusout", person((ev) => this.onLeave(ev)));
    on(doc, "click", person((ev) => this.onClick(ev)));
    on(this.env.win, "pagehide", () => this.stop());
    this.poll = setInterval(() => this.pollListboxes(), this.env.timing.listboxPollMs);
  }

  /** Ends the watch: whatever was being typed is kept, then the listeners and timers go. */
  stop(): void {
    if (!this.running) return;
    this.flush();
    this.running = false;
    clearInterval(this.poll);
    clearTimeout(this.revealTimer);
    this.revealObserver?.disconnect();
    this.revealObserver = null;
    for (const t of this.timers.values()) clearTimeout(t);
    this.timers.clear();
    for (const o of this.off) o();
    this.off = [];
  }

  /**
   * A value the host wrote after the fill through the engine's answer call, as the apply chain's page check does: the control
   * holds it now, so a later edit replaces it and carries it as its before value, as an edit of the fill's own write does.
   */
  wrote(info: ControlInfo, row: Row): void {
    if (!this.running) return;
    this.attached.set(info.selector, readInfo(info));
    this.late.set(info.selector, row);
  }

  /**
   * The host drives trusted input itself (the apply chain's computer-use rung): while held no event is a person's. On
   * release the listboxes are read again and the last trusted input is forgotten, so a change the host's input left
   * behind is never taken for a person's pick.
   */
  hold(on: boolean): void {
    this.held = on;
    if (on || !this.running) return;
    this.lastTrusted = 0;
    for (const selector of Array.from(this.listboxes.keys())) {
      const el = resolveSelector(this.env.doc, selector);
      if (el) this.listboxes.set(selector, readElement(el));
    }
  }

  /** The events captured so far, which are then forgotten. */
  drain(): LearningEvent[] {
    const out = [...this.captures.values(), ...this.diagnostics];
    this.captures.clear();
    this.diagnostics.length = 0;
    return out;
  }

  /** A control the page shows after the run (the person answered a gate the matcher could not) is reported once, when it is required and empty. */
  private watchReveals(): void {
    const win = this.env.win;
    const root = this.env.doc.documentElement;
    if (!root || !win.MutationObserver) return;
    this.revealObserver = new win.MutationObserver(() => {
      clearTimeout(this.revealTimer);
      this.revealTimer = setTimeout(() => this.reportRevealed(), this.env.timing.revealDebounceMs);
    });
    this.revealObserver.observe(root, { subtree: true, childList: true, attributes: true, attributeFilter: ["style", "class", "hidden", "aria-hidden"] });
  }

  private reportRevealed(): void {
    if (!this.running) return;
    for (const info of scanControls(this.env.doc)) {
      if (this.scanned.has(info.selector)) continue;
      this.scanned.add(info.selector);
      if (!info.required || info.answered || info.kind === "file" || !info.label.text) continue;
      const ev = baseEvent(info, "uncertainty", { win: this.env.win, site: this.env.site });
      ev.reason = "revealed-after-run";
      this.note(ev);
    }
  }

  private infoOf(el: Element): ControlInfo | null {
    // The inventory lists the controls the engine never fills too: the fact that the person edited one is kept, never its value.
    return scanControls(this.env.doc, { inventory: true }).find((i) => i.el === el || i.el.contains(el) || i.members.some((m) => m === el) || i.toggles.some((t) => t === el || t.contains(el))) ?? null;
  }

  private flush(): void {
    for (const el of Array.from(this.dirty)) this.commit(el);
  }

  private onInput(ev: Event): void {
    const el = controlOf(ev);
    if (!el) return;
    if (!ev.isTrusted) return;
    const ac = (el.getAttribute("autocomplete") ?? "").toLowerCase();
    const type = (el.getAttribute("type") ?? "").toLowerCase();
    if (type === "password" || /^(cc-|one-time-code)/.test(ac)) {
      const kind = type === "password" || ac === "one-time-code" ? "mfa" : "payment";
      // Every keystroke is an input event: the watch notes each kind once.
      if (!this.typedKinds.has(kind)) {
        this.typedKinds.add(kind);
        this.diagnose(kind);
      }
      return;
    }
    if (el.matches("select, [type=radio], [type=checkbox], [type=file]")) return;
    // A person typing: the control is captured when they leave it, however long they pause.
    this.dirty.add(el);
  }

  private onChange(ev: Event): void {
    const el = controlOf(ev);
    if (!el || !ev.isTrusted) return;
    if (el.matches("[type=file]")) {
      this.diagnose("file-upload");
      return;
    }
    if (!el.matches("select, [type=radio], [type=checkbox]")) return;
    // Chrome marks the change of a scripted click() trusted (the page answering another control, Phenom); a person's change of a box follows a trusted click on it.
    if (el.matches("[type=radio], [type=checkbox]") && !this.clicked.delete(el)) return;
    clearTimeout(this.timers.get(el));
    this.timers.set(
      el,
      setTimeout(() => {
        this.timers.delete(el);
        this.commit(el);
      }, this.env.timing.correctionDelayMs),
    );
  }

  private onLeave(ev: Event): void {
    const el = controlOf(ev);
    if (el && ev.isTrusted && this.dirty.has(el)) this.commit(el);
  }

  private onClick(ev: Event): void {
    if (!ev.isTrusted) return;
    const target = controlOf(ev);
    if (target?.matches("[type=radio], [type=checkbox]")) this.clicked.add(target);
    const el = target?.closest("button, a[role=button], input[type=submit], input[type=button], [role=button]");
    if (!el) return;
    const label = clickLabel(el);
    const hit = UNSAFE.find(([, re]) => re.test(label));
    if (!hit) return;
    // Whatever is still being typed is kept now, before the page can navigate away; the listeners stay, the person may still correct a field.
    if (hit[0] === "submit") this.flush();
    this.diagnose(hit[0]);
  }

  /** A scrubbed note that a person used a control the engine never touches: its kind, no label, no value. */
  private diagnose(kind: string): void {
    const loc = this.env.win.location;
    const ev: LearningEvent = {
      eventId: eventId(this.env.win),
      kind: "unsafe-control",
      host: loc?.hostname ?? "",
      url: loc ? `${loc.origin}${loc.pathname}` : "",
      question: kind,
      controlKind: "text",
      time: Date.now(),
    };
    if (this.env.site) ev.site = this.env.site;
    this.note(ev);
  }

  private note(ev: LearningEvent): void {
    if (this.diagnostics.length < MAX_DIAGNOSTICS) this.diagnostics.push(ev);
  }

  private pollListboxes(): void {
    if (!this.running || this.held) return;
    const recent = Date.now() - this.lastTrusted <= this.env.timing.trustedWindowMs;
    for (const [selector, was] of Array.from(this.listboxes)) {
      // Only the trigger's own display is read each tick; the page is scanned again only when it changed.
      const el = resolveSelector(this.env.doc, selector);
      if (!el) continue;
      const shown = readElement(el);
      if (shown === was) continue;
      this.listboxes.set(selector, shown);
      // A change nobody's hand was near is the page's own.
      const info = recent ? this.infoOf(el) : null;
      if (info) this.commitInfo(info, el);
    }
  }

  private commit(el: Element): void {
    this.dirty.delete(el);
    const info = this.infoOf(el);
    if (info) this.commitInfo(info, el);
  }

  private commitInfo(info: ControlInfo, el: Element): void {
    // What the person commits is their own answer to the question, whatever the fill decided there: the server keeps it as ground truth.
    const ev = baseEvent(info, "manual-fill", { win: this.env.win, site: this.env.site });
    ev.trustedInput = true;
    const prior = this.attached.get(info.selector) ?? "";
    if (valueWithheld(info)) {
      // The fact that it was edited is kept; nothing of the value.
      ev.edited = true;
      this.captures.set(info.selector, ev);
      return;
    }
    const after = readInfo(info);
    if (after === "") {
      // No positive read-back: an incomplete note, with no value.
      ev.incomplete = true;
      this.captures.set(info.selector, ev);
      return;
    }
    if (collapse(after) === collapse(prior)) {
      this.captures.delete(info.selector);
      return;
    }
    const row = this.late.get(info.selector) ?? this.env.rows.find((r) => r.selector === info.selector);
    if (captureDrop({ question: info.label.text, value: after, label: info.label.direct, controlKind: info.kind, valueAtAttach: prior })) {
      // The control holds this value now: an earlier capture of it names one it has lost.
      this.captures.delete(info.selector);
      return;
    }
    ev.after = after;
    if (info.wireKind !== "text") ev.optionLabel = after;
    if (row && row.outcome !== "skipped" && row.value) ev.before = row.value;
    // The value read when the watch attached, never for a control the fill skipped.
    if (prior !== "" && row?.outcome !== "skipped") ev.priorValue = prior;
    if (row) ev.fillOutcome = row.outcome;
    const frag = controlSnapshot(el, info.label.text);
    if (frag) ev.fragment = frag.html;
    // A changed value at flush supersedes an earlier capture of the same control.
    this.captures.set(info.selector, ev);
  }
}

