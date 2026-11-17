// Keyboard focus helpers shared by the dialogs and the shell.

const FOCUSABLE = 'a[href], button, input:not([type="hidden"]), select, textarea, summary, iframe, [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';

// focusables lists the controls inside root that Tab reaches, in page order: a
// disabled control, one inside a hidden or inert subtree and one taken out of
// the tab order are left out.
export function focusables(root: ParentNode): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (e) => !(e as HTMLButtonElement).disabled && e.tabIndex >= 0 && !e.closest("[hidden], [inert]") && !(e.closest("details:not([open])") && e.tagName !== "SUMMARY"),
  );
}

// WebKit, the desktop app's engine, does not focus a button on a mouse click,
// so when the click's handler opens a dialog document.activeElement is still
// what held focus before: the body, the page content after a navigation, or the
// drawer around the button. The control last pressed stands in for it (a key
// press clears it: the keyboard focuses controls itself).
let pressed: { el: HTMLElement; at: number } | null = null;
if (typeof document !== "undefined") {
  document.addEventListener("pointerdown", (e) => {
    const el = e.target instanceof Element ? e.target.closest<HTMLElement>(FOCUSABLE) : null;
    pressed = el ? { el, at: Date.now() } : null;
  }, true);
  document.addEventListener("keydown", () => void (pressed = null), true);
}

// openerOf returns the control that opens a dialog: the one pressed within the
// last ten seconds when focus is on an element around it, else the focused one.
export function openerOf(): Element | null {
  const active = document.activeElement;
  const recent = pressed && pressed.el.isConnected && Date.now() - pressed.at < 10_000 ? pressed.el : null;
  return recent && (!active || active.contains(recent)) ? recent : active;
}

// focusMain moves focus to the page content, where it goes when the control
// that had it is gone.
export function focusMain(): void {
  document.getElementById("main")?.focus({ preventScroll: true });
}

// barRoom keeps --bar-h at the height of the tallest sticky save or selection
// bar on the page, which the bottom scroll padding is made of, so a field the
// keyboard reaches stays clear of a bar that wraps onto two lines (WCAG
// 2.4.11). It returns the function that stops watching bar.
export function barRoom(bar: HTMLElement): () => void {
  const fit = () => document.documentElement.style.setProperty("--bar-h", `${Math.max(0, ...Array.from(document.querySelectorAll<HTMLElement>(".save-bar"), (b) => b.offsetHeight))}px`);
  if (typeof ResizeObserver === "undefined") return () => {};
  const ro = new ResizeObserver(fit);
  ro.observe(bar);
  return () => (ro.disconnect(), fit());
}

type Spot = { container: Element; next: Element | null; prev: Element | null };

// keepFocus watches root and puts focus back where the keyboard was when the
// browser drops it to the page body. Two things drop it. A button disabled
// while its write is in flight loses focus; it gets it back once it is enabled
// again. An element removed from the page, as when a list reloads without the
// row whose button was pressed, sends focus to the row that took its place
// (the next one, else the one before), else to the list, else to the content;
// inside a dialog that case is left to the dialog, which returns focus to its
// opener. It returns the function that stops watching.
export function keepFocus(root: HTMLElement): () => void {
  let spots: Spot[] = [];
  let last: Element | null = null;
  const onFocusIn = (e: FocusEvent) => {
    const t = e.target;
    if (!(t instanceof Element)) return;
    last = t;
    spots = [];
    for (let el: Element = t; el.parentElement && el !== root; el = el.parentElement) {
      spots.push({ container: el.parentElement, next: el.nextElementSibling, prev: el.previousElementSibling });
    }
  };
  // Focus that leaves for nothing from an element still in the page and still
  // enabled was moved by the user (a press on empty space); nothing is restored.
  // A browser also sends focusout while it removes the element, so the check
  // waits until the removal is done.
  const onFocusOut = (e: FocusEvent) => {
    const t = e.target;
    if (e.relatedTarget !== null || t !== last || !(t instanceof Element)) return;
    setTimeout(() => {
      if (last === t && t.isConnected && !(t as HTMLButtonElement).disabled) last = null;
    }, 0);
  };
  const dropped = () => {
    const active = document.activeElement;
    return !active || active === document.body || !active.isConnected;
  };
  const restore = () => {
    if (!last || !dropped()) return;
    if (last.isConnected) {
      // Still in the page: wait while it is disabled, take focus back after.
      if (last instanceof HTMLElement && !(last as HTMLButtonElement).disabled && last.matches(FOCUSABLE)) last.focus({ preventScroll: true });
      return;
    }
    if (last.closest("dialog")) {
      last = null;
      return;
    }
    last = null;
    for (const s of spots) {
      if (!s.container.isConnected) continue;
      for (const near of [s.next, s.prev]) {
        if (!near?.isConnected) continue;
        const target = near instanceof HTMLElement && near.matches(FOCUSABLE) ? near : focusables(near)[0];
        if (target) {
          target.focus({ preventScroll: false });
          return;
        }
      }
      const inside = focusables(s.container)[0];
      if (inside) {
        inside.focus();
        return;
      }
    }
    focusMain();
  };
  // The check waits a turn, so a page change that moves focus itself (the
  // shell focuses the new page) has done so first.
  const observer = new MutationObserver(() => setTimeout(restore, 0));
  observer.observe(root, { childList: true, subtree: true, attributes: true, attributeFilter: ["disabled"] });
  root.addEventListener("focusin", onFocusIn);
  root.addEventListener("focusout", onFocusOut);
  return () => {
    observer.disconnect();
    root.removeEventListener("focusin", onFocusIn);
    root.removeEventListener("focusout", onFocusOut);
  };
}
