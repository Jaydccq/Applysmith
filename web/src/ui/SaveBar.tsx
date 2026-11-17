import { CircleCheck, CircleDot } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { holdPage } from "../router";
import { barRoom } from "./focus";
import { ConfirmDialog } from "./Modal";
import { ariaKeys, matches } from "./Shortcuts";

// Changed says the saved text changed after the editor opened it, so a save
// was or would be refused. The user's text stays until they choose. A page
// whose Keep does more than keep the text names it and says what it does. It
// comes into view, as it often lands below a long editor while the keyboard is in it.
export function Changed({ onKeep, onLoad, keep = "Keep my text", children }: { onKeep: () => void; onLoad: () => void; keep?: string; children?: ReactNode }) {
  const note = useRef<HTMLDivElement>(null);
  useEffect(() => {
    note.current?.scrollIntoView?.({ block: "nearest" });
  }, []);
  return (
    <div role="alert" className="note error" ref={note}>
      <p>{children ?? "Someone or something changed this since you opened it; your edits are not saved yet. Keeping your text makes the next save replace the new version; loading the new version drops your edits."}</p>
      <div className="actions">
        <button type="button" onClick={onKeep}>{keep}</button>
        <button type="button" onClick={onLoad}>Load the new version</button>
      </div>
    </div>
  );
}

// SaveBar closes a long form: it stays in view at the bottom while the form
// scrolls, says whether there are unsaved changes or when the last save
// landed, and holds the form's save button; the page's scroll padding
// follows its height (barRoom). An edit after a save reads as
// unsaved again. The state is a polite live region, so a save is announced
// without moving focus. While unsaved, leaving the page or the window asks
// (useUnsaved), and Mod+S presses the bar's last button, its save, when enabled: the bar
// whose form has the keyboard, else the only unsaved bar on the page.
//
//   <SaveBar dirty={dirty} savedAt={savedAt}>
//     <button type="submit" disabled={!dirty}>Save policy</button>
//   </SaveBar>
export function SaveBar({ dirty, savedAt, children }: { dirty: boolean; savedAt: string; children: ReactNode }) {
  const [edited, setEdited] = useState(false);
  const clean = useRef(!dirty);
  const bar = useRef<HTMLDivElement>(null);
  const note = useRef<HTMLSpanElement>(null);
  // The save button is disabled once saved, and a disabled control cannot keep
  // focus, so the saved note takes it from the bar's own buttons or the body.
  useEffect(() => {
    setEdited(false);
    const at = document.activeElement;
    if (savedAt && (!at || at === document.body || bar.current?.contains(at))) note.current?.focus();
  }, [savedAt]);
  useEffect(() => {
    if (dirty && clean.current) setEdited(true);
    clean.current = !dirty;
  }, [dirty]);
  useEffect(() => barRoom(bar.current!), []);
  const state = savedAt && !edited ? "saved" : dirty ? "dirty" : "clean";
  const ask = useUnsaved(state === "dirty");
  useEffect(() => {
    if (state !== "dirty") return;
    const save = (e: KeyboardEvent) => {
      if (e.defaultPrevented || !matches(e, "Mod+S") || !bar.current || document.querySelector("dialog[open]")) return;
      const scope = bar.current.closest("form") ?? bar.current.parentElement;
      const unsaved = document.querySelectorAll(".save-bar[data-state=dirty]");
      if (!scope?.contains(document.activeElement) && unsaved.length > 1) return;
      e.preventDefault();
      const buttons = bar.current.querySelectorAll<HTMLButtonElement>(".save-actions button");
      const last = buttons[buttons.length - 1];
      if (last && !last.disabled) last.click();
    };
    window.addEventListener("keydown", save);
    return () => window.removeEventListener("keydown", save);
  }, [state]);
  return (
    <div className="save-bar" data-state={state} ref={bar}>
      <span aria-live="polite" className="save-state" tabIndex={-1} ref={note}>
        {state === "saved" && (
          <>
            <CircleCheck aria-hidden="true" />
            Saved at {savedAt}.
          </>
        )}
        {state === "dirty" && (
          <>
            <CircleDot aria-hidden="true" />
            Unsaved changes.
          </>
        )}
      </span>
      <span className="save-actions">{children}</span>
      {ask}
    </div>
  );
}

// useSaveKey gives an editor in a dialog or drawer the Mod+S of a save bar:
// it presses the button it is set on once something changed (changed), if
// that button is enabled. Only the topmost open dialog answers, so a question
// asked over the editor keeps the keys; a dialog with more than one such
// button saves the form that has the keyboard.
export function useSaveKey(changed: boolean): (button: HTMLButtonElement | null) => void {
  const button = useRef<HTMLButtonElement | null>(null);
  const live = useRef(changed);
  live.current = changed;
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      const b = button.current;
      const dialog = b?.closest("dialog");
      if (e.defaultPrevented || !matches(e, "Mod+S") || !b || !dialog) return;
      const open = document.querySelectorAll("dialog[open]");
      if (open[open.length - 1] !== dialog) return;
      if (dialog.querySelectorAll(`[aria-keyshortcuts="${ariaKeys("Mod+S")}"]`).length > 1 && !b.form?.contains(document.activeElement)) return;
      e.preventDefault();
      if (live.current && !b.disabled) b.click();
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
  return useCallback((el: HTMLButtonElement | null) => {
    button.current = el;
    el?.setAttribute("aria-keyshortcuts", ariaKeys("Mod+S"));
  }, []);
}

// useUnsaved holds the page while a form has text not saved: a link, a
// shortcut, Back or Forward, and a close or tab switch made through leave()
// ask first, as do a browser's close or reload and the desktop app's reload,
// restart, workspace switch and quit. It returns the question.
export function useUnsaved(unsaved: boolean): ReactNode {
  const [leaving, setLeaving] = useState<{ go: () => void; stay: () => void } | null>(null);
  useEffect(() => {
    if (!unsaved) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    const release = holdPage((go, stay) => setLeaving({ go, stay }));
    return () => {
      release();
      window.removeEventListener("beforeunload", warn);
    };
  }, [unsaved]);
  return (
    leaving &&
    createPortal(
      <ConfirmDialog
        title="Leave without saving?"
        consequence="The changes you have not saved here are discarded."
        verb="Leave"
        onCancel={() => (setLeaving(null), leaving.stay())}
        onConfirm={() => (setLeaving(null), leaving.go())}
      />,
      document.body,
    )
  );
}
