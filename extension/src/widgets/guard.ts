// While a composed draft is pending, a click on the page's submit control is
// held back and the person chooses: wait, submit anyway, or skip the pending
// fields. The guard never disables the page's button, looks only at the
// person's own clicks, and exists only while a draft is pending and the panel
// is open.
import { h } from "../shared/dom";
import { clearPending, onPendingChange, pendingCount, setGuardActive } from "./drafts";
import { createLayer, type Layer } from "./layer";
import { submitControlOf } from "./submit";

const CSS = `
.veil { position: fixed; inset: 0; z-index: 2147483647; background: rgba(0,0,0,.45); display: grid; place-items: center; margin: 0; border: 0; padding: 0; width: 100%; height: 100%; max-width: none; max-height: none; }
.dlg { width: min(380px, calc(100vw - 32px)); padding: 14px; background: var(--bg); border: 1px solid var(--line); border-radius: 10px; }
.dlg .row { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 10px; justify-content: flex-end; }
`;

export interface GuardDeps {
  doc: Document;
  win: Window;
  /** Called when the person lets a held click through, so the submission recorder still sees it. */
  onRelease?(el: HTMLElement): void;
}

export interface Guard {
  /** The guard is removed while the panel is closed. */
  setPanelOpen(open: boolean): void;
  isActive(): boolean;
  destroy(): void;
}

export function startGuard(deps: GuardDeps): Guard {
  const { doc, win } = deps;
  let panelOpen = true;
  let installed = false;
  let layer: Layer | undefined;

  const closeOverlay = () => {
    layer?.destroy();
    layer = undefined;
  };

  function release(el: HTMLElement, form: HTMLFormElement | null): void {
    closeOverlay();
    deps.onRelease?.(el);
    if (el.isConnected) el.click();
    else form?.requestSubmit();
  }

  function overlay(el: HTMLElement): void {
    closeOverlay();
    const form = el.closest("form");
    layer = createLayer(doc, CSS);
    const wait = h("button", { type: "button" }, "Wait");
    const anyway = h("button", { type: "button" }, "Submit anyway");
    const skip = h("button", { type: "button" }, "Skip the pending fields");
    const n = pendingCount();
    const veil = h(
      "div",
      { class: "veil", popover: "manual" },
      h("div", { class: "dlg", role: "alertdialog", "aria-modal": "true", "aria-label": "Drafts are not reviewed" }, h("p", {}, `${n} ${n === 1 ? "draft is" : "drafts are"} not filled in yet. Submitting now sends the form without ${n === 1 ? "it" : "them"}.`), h("div", { class: "row" }, wait, skip, anyway)),
    );
    layer.box.append(veil);
    try {
      (veil as HTMLElement & { showPopover?: () => void }).showPopover?.();
    } catch {
      /* the fixed, top-most veil stands in for the top layer */
    }
    wait.addEventListener("click", closeOverlay);
    skip.addEventListener("click", () => {
      clearPending();
      release(el, form);
    });
    anyway.addEventListener("click", () => release(el, form));
    wait.focus();
  }

  const onClick = (e: MouseEvent): void => {
    if (!e.isTrusted) return;
    const el = submitControlOf(e.target);
    if (!el) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    overlay(el);
  };

  function sync(): void {
    const want = panelOpen && pendingCount() > 0;
    if (want && !installed) win.addEventListener("click", onClick, true);
    if (!want && installed) {
      win.removeEventListener("click", onClick, true);
      closeOverlay();
    }
    installed = want;
    setGuardActive(want);
  }

  const stop = onPendingChange(sync);
  sync();
  return {
    setPanelOpen(open) {
      panelOpen = open;
      sync();
    },
    isActive: () => installed,
    destroy() {
      stop();
      panelOpen = false;
      sync();
    },
  };
}
