// Clean-room: docs/provenance.md#CR-ENGINE
// Which controls a person could see and use, and which are decoys.

import { composedAncestors, hasLayout, winOf } from "./dom";

const DIALOG = "dialog[open], [role=dialog], [role=alertdialog]";

export type RenderCache = WeakMap<Element, boolean>;

function styleOf(el: Element): CSSStyleDeclaration | null {
  try {
    return winOf(el).getComputedStyle(el);
  } catch {
    return null;
  }
}

/** Removed from rendering by its own display or hidden attribute (ancestors not consulted). */
export function selfHidden(el: Element, cache?: RenderCache): boolean {
  const known = cache?.get(el);
  if (known !== undefined) return known;
  let hidden = false;
  if (el.hasAttribute("hidden") && el.getAttribute("hidden") !== "until-found") hidden = true;
  else if (el.classList.contains("iti__hide")) hidden = true;
  else {
    const st = styleOf(el);
    if (st && st.display === "none") hidden = true;
  }
  cache?.set(el, hidden);
  return hidden;
}

/** Not removed from rendering by display, visibility or the hidden attribute; has a box when layout exists. */
export function isRendered(el: Element, cache?: RenderCache): boolean {
  if (selfHidden(el, cache)) return false;
  for (const a of composedAncestors(el)) if (selfHidden(a, cache)) return false;
  const st = styleOf(el);
  if (st && st.visibility === "hidden") return false;
  if (hasLayout(el.ownerDocument) && el.getClientRects().length === 0) return false;
  return true;
}

function isVisibleDialog(dialog: Element): boolean {
  if (dialog.localName === "dialog" && !dialog.hasAttribute("open")) return false;
  return isRendered(dialog);
}

/**
 * True when an aria-hidden ancestor hides the control behind an open dialog.
 * aria-hidden means unreachable only while some dialog is shown: with none
 * open it is the site's noise over the form a person is looking at, and inside
 * the nearest dialog it marks the dialog's own content. aria-hidden at or
 * above that dialog still hides it.
 */
export function ariaHiddenBlocks(el: Element): boolean {
  const chain = [el, ...composedAncestors(el)];
  const marked = (n: Element) => n.getAttribute("aria-hidden") === "true";
  if (!chain.some(marked)) return false;
  if (!Array.from(el.ownerDocument.querySelectorAll(DIALOG)).some(isVisibleDialog)) return false;
  const dialog = chain.findIndex((n) => n.matches(DIALOG));
  if (dialog < 0) return true;
  return chain.slice(dialog + 1).some(marked);
}

/** An open, visible dialog that does not contain the element. */
export function openDialogElsewhere(doc: Document, el: Element): boolean {
  for (const d of Array.from(doc.querySelectorAll(DIALOG))) {
    if (!d.contains(el) && isVisibleDialog(d)) return true;
  }
  return false;
}

function px(value: string): number | null {
  const m = /^(-?\d+(?:\.\d+)?)px$/.exec(value.trim());
  return m ? parseFloat(m[1] as string) : null;
}

/** Far outside the page or clipped to nothing: a bot trap, not a field. */
export function isOffScreen(el: Element): boolean {
  const chain = [el, ...Array.from(composedAncestors(el)).slice(0, 3)];
  for (const n of chain) {
    const st = styleOf(n);
    if (!st) continue;
    const pos = st.position;
    if (pos === "absolute" || pos === "fixed") {
      const left = px(st.left);
      const top = px(st.top);
      if ((left !== null && left <= -1000) || (top !== null && top <= -1000)) return true;
    }
    const indent = px(st.textIndent ?? "");
    if (indent !== null && indent <= -1000) return true;
    const clip = (st.clip ?? "").replace(/\s+/g, "");
    if (clip === "rect(0,0,0,0)" || clip === "rect(0px,0px,0px,0px)") {
      const w = px(st.width);
      const h = px(st.height);
      if ((w !== null && w <= 1) || (h !== null && h <= 1)) return true;
    }
  }
  if (hasLayout(el.ownerDocument)) {
    const r = el.getBoundingClientRect();
    const win = winOf(el);
    const left = r.width > 0 && r.right + (win.scrollX ?? 0) < -500;
    const above = r.height > 0 && r.bottom + (win.scrollY ?? 0) < -500;
    if (left || above) {
      // A box scrolled out of a pane that scrolls on its own (Ashby's form) is above the viewport by the pane's
      // scroll, not outside the page: a scrolled ancestor's offset counts as the window's does.
      const { x, y } = paneScroll(el);
      if (left && r.right + (win.scrollX ?? 0) + x < -500) return true;
      if (above && r.bottom + (win.scrollY ?? 0) + y < -500) return true;
    }
  }
  return false;
}

/** The scroll offsets of the element's scrolled ancestors below the document's own scroller. */
function paneScroll(el: Element): { x: number; y: number } {
  const doc = el.ownerDocument;
  let x = 0;
  let y = 0;
  for (const a of composedAncestors(el)) {
    if (a === doc.documentElement || a === doc.body) continue;
    x += a.scrollLeft;
    y += a.scrollTop;
  }
  return { x, y };
}

function isFaded(el: Element): boolean {
  for (const n of [el, ...Array.from(composedAncestors(el)).slice(0, 2)]) {
    const st = styleOf(n);
    if (st && st.opacity === "0") return true;
  }
  return false;
}

// A label that is the word itself ("honeypot", Oracle's honeyPotForm) names the trap too.
const HONEYPOT_LABEL =
  /leave (this )?(field |box |input )?(empty|blank)|keep (this )?(field |box )?(empty|blank)|do not (fill|enter)|if you are (a )?(human|not a (robot|bot))|for (bots|spam)|spam trap|\bhoney[\s_-]?pot\b/i;

export function isHoneypotLabel(label: string): boolean {
  return HONEYPOT_LABEL.test(label);
}

const TWIN_SIZE = /auto-?height|auto-?size/i;
const TWIN_ROLE = /mirror|twin|clone|shadow|sizer|measure/i;

/**
 * The hidden copy of a text area that a page keeps in step with the real one
 * to measure its height. It is not a field, whatever label it was given.
 */
export function isMeasurementTwin(el: Element): boolean {
  if (el.localName !== "textarea") return false;
  const names = `${el.getAttribute("class") ?? ""} ${el.getAttribute("id") ?? ""} ${el.getAttribute("data-role") ?? ""}`;
  return TWIN_SIZE.test(names) && TWIN_ROLE.test(names);
}

/**
 * A control a person is not meant to fill: off-screen, or concealed in a way
 * that also leaves it without any label. A concealed input that has a real
 * label (a styled checkbox) stays fillable.
 */
export function isConcealedDecoy(el: Element, hasLabel: boolean): boolean {
  if (isOffScreen(el)) return true;
  if (hasLabel) return false;
  if (isFaded(el)) return true;
  if (el.getAttribute("aria-hidden") === "true") return true;
  return el.getAttribute("tabindex") === "-1" && el.getAttribute("aria-hidden") !== null;
}
