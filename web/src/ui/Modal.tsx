import { X } from "lucide-react";
import { useEffect, useId, useRef, type ReactNode, type RefObject } from "react";
import { focusMain, focusables, keepFocus, openerOf } from "./focus";

// containWheel keeps a wheel over a dialog inside it: the wheel is cancelled
// and scrolls the innermost scroller under the pointer that can move. WebKit,
// the desktop app's engine, otherwise hands it to the page behind and does not
// scroll a drawer's body (measured on the run and skip-list drawers).
function containWheel(e: WheelEvent) {
  e.preventDefault();
  const dlg = e.currentTarget as HTMLElement;
  const across = Math.abs(e.deltaX) > Math.abs(e.deltaY);
  const delta = across ? e.deltaX : e.deltaY;
  for (let el = e.target instanceof Element ? e.target : null; el && dlg.contains(el); el = el.parentElement) {
    if (!(el instanceof HTMLElement)) continue;
    const overflow = across ? getComputedStyle(el).overflowX : getComputedStyle(el).overflowY;
    if (overflow !== "auto" && overflow !== "scroll") continue;
    const [at, size, view] = across ? [el.scrollLeft, el.scrollWidth, el.clientWidth] : [el.scrollTop, el.scrollHeight, el.clientHeight];
    if ((delta < 0 && at > 0) || (delta > 0 && at + view < size - 1)) {
      const by = delta * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? view : 1);
      return el.scrollBy(across ? { left: by } : { top: by });
    }
  }
}

// Modal wraps the native dialog element, which makes the page behind it inert
// and closes on Escape. Tab and Shift+Tab wrap inside it rather than leaving
// for the browser's own controls. Focus starts on initialFocus, else on the
// first control, and goes back to the control that opened it, or to the page
// content when that control is gone. A dialog an address opened has no such
// control (the page content held focus, or nothing did); returnFocus names
// the one to go back to then. A drawer keeps its title and a close control in
// a header that stays put while its body scrolls; a wheel over any dialog
// stays in it (containWheel).
export function Modal({ title, onClose, children, variant = "dialog", initialFocus, returnFocus }: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  variant?: "dialog" | "drawer" | "palette" | "wide";
  initialFocus?: RefObject<HTMLElement | null>;
  returnFocus?: () => HTMLElement | null | undefined;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const opener = useRef<Element | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const returnRef = useRef(returnFocus);
  returnRef.current = returnFocus;
  const heading = useId();

  // The dialog opens once per mount; the focus target is read at that time.
  useEffect(() => {
    const dlg = ref.current;
    if (!dlg) return;
    opener.current = openerOf();
    dlg.addEventListener("wheel", containWheel, { passive: false });
    // A control that goes (Mark read, once read) hands focus to its neighbour
    // in the dialog, not to the page behind it.
    const stopKeeping = keepFocus(dlg);
    if (!dlg.open) dlg.showModal();
    if (initialFocus?.current) initialFocus.current.focus();
    else if (!dlg.contains(document.activeElement)) (focusables(dlg)[0] ?? dlg).focus();
    return () => {
      dlg.removeEventListener("wheel", containWheel);
      stopKeeping();
      if (dlg.open) dlg.close();
      const from = opener.current;
      const page = !(from instanceof HTMLElement) || !from.isConnected || from === document.body || from === document.getElementById("main");
      const back = page ? returnRef.current?.() : from;
      if (back?.isConnected) back.focus();
      else focusMain();
    };
  }, []);

  return (
    <dialog
      ref={ref}
      className={`modal ${variant}`}
      aria-labelledby={heading}
      onCancel={(e) => {
        e.preventDefault();
        onCloseRef.current();
      }}
      onKeyDown={(e) => {
        // A dialog opened from inside this one bubbles its keys through React;
        // only the innermost dialog answers them.
        if ((e.target as Element).closest("dialog") !== e.currentTarget) return;
        if (e.key === "Escape") {
          e.preventDefault();
          onCloseRef.current();
        } else if (e.key === "Tab") {
          const all = focusables(e.currentTarget);
          if (all.length === 0) return;
          const [first, last] = [all[0], all[all.length - 1]];
          const at = document.activeElement;
          if (e.shiftKey && (at === first || at === e.currentTarget)) {
            e.preventDefault();
            last.focus();
          } else if (!e.shiftKey && at === last) {
            e.preventDefault();
            first.focus();
          }
        }
      }}
    >
      {variant === "drawer" ? (
        <>
          <div className="modal-head">
            <h2 id={heading}>{title}</h2>
            <button type="button" className="ghost icon-only" aria-label="Close" onClick={() => onCloseRef.current()}>
              <X />
            </button>
          </div>
          <div className="modal-body">{children}</div>
        </>
      ) : (
        <div className="modal-body">
          <h2 id={heading}>{title}</h2>
          {children}
        </div>
      )}
    </dialog>
  );
}

// ConfirmDialog names the action and the object, states the consequence and
// puts the first focus on Cancel. The confirming button carries the verb.
export function ConfirmDialog({ title, consequence, details, verb, onConfirm, onCancel, busy }: {
  title: string;
  consequence: ReactNode;
  /** More facts the user should read before confirming, such as a list. */
  details?: ReactNode;
  verb: string;
  onConfirm: () => void;
  onCancel: () => void;
  busy?: boolean;
}) {
  const cancel = useRef<HTMLButtonElement>(null);
  return (
    <Modal title={title} onClose={onCancel} initialFocus={cancel}>
      <p>{consequence}</p>
      {details}
      <div className="actions">
        <button type="button" ref={cancel} onClick={onCancel}>
          Cancel
        </button>
        <button type="button" className="danger" onClick={onConfirm} disabled={busy}>
          {verb}
        </button>
      </div>
    </Modal>
  );
}
