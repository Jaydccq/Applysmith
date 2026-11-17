// The "You applied to this job" banner. The application's tracker and its
// submissions ledger decide whether it shows; this words it, draws it in its
// own shadow root and remembers, through the worker, that the person
// dismissed it.
import { h } from "../shared/dom";
import type { BannerView, Flags } from "../shared/messages";
import { createLayer, type Layer } from "./layer";
import type { PageChanges } from "./main";

const CSS = `
.banner { position: fixed; left: 50%; top: 8px; transform: translateX(-50%); z-index: 2147483645; max-width: min(560px, calc(100vw - 16px)); display: flex; gap: 10px; align-items: center; padding: 6px 12px; background: var(--bg); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 4px 16px rgba(0,0,0,.25); }
`;
const WATCH_MS = 1500;
const REMOUNT_MS = 400;

export interface BannerDeps {
  doc: Document;
  win: Window;
  /** The widgets' shared page observer. */
  changes: PageChanges;
  flags(): Promise<Flags>;
  get(url: string): Promise<BannerView>;
  dismiss(key: string): Promise<void>;
}

/** The date as M/D/YY: a bare calendar day is not shifted by the time zone, a moment is read in the local one. */
function formatApplied(date: string): string {
  const day = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  const d = day ? new Date(Number(day[1]), Number(day[2]) - 1, Number(day[3])) : new Date(date);
  if (Number.isNaN(d.getTime())) return date;
  return `${d.getMonth() + 1}/${d.getDate()}/${String(d.getFullYear()).slice(2)}`;
}

/**
 * The banner's words, which never claim more than the record holds: the
 * application chain's submission says so, an unconfirmed one (recorded on the
 * click, before any confirmation) says "(unconfirmed)", and a record the
 * extension did not see being made names the ledger it came from.
 */
export function appliedText(view: BannerView): string {
  const sub = view.submission;
  const when = view.date ? formatApplied(view.date) : "";
  const lead = sub?.agent ? `Applied by Applysmith agent${when ? ` on ${when}` : ""}.` : when ? `You applied to this job on ${when}.` : "You applied to this job.";
  return `${lead}${sub && !sub.confirmed ? " (unconfirmed)" : ""}${sub?.fromLedger ? " (from your application ledger)" : ""}`;
}

export function startBanner(deps: BannerDeps): { destroy(): void } {
  const { doc, win } = deps;
  let layer: Layer | undefined;
  let shownFor = "";
  let watching: ReturnType<typeof setInterval> | undefined;
  let last = win.location.href;
  // What is on screen for the current posting, so a hydrating page that throws
  // the banner away gets it back without another question to the application.
  let shown: { key: string; text: string } | undefined;
  // A dismissal holds for the session: a hydration burst cannot restore it.
  const dismissed = new Set<string>();
  // Registered on the shared observer only while a banner is on screen: with none shown, a page change has nothing to restore.
  let stopWatching: (() => void) | undefined;

  const hide = () => {
    layer?.destroy();
    layer = undefined;
    shown = undefined;
    stopWatching?.();
    stopWatching = undefined;
  };

  function draw(key: string, text: string): void {
    layer = createLayer(doc, CSS);
    const close = h("button", { type: "button", "aria-label": "Dismiss" }, "Dismiss");
    close.addEventListener("click", () => {
      dismissed.add(key);
      hide();
      void deps.dismiss(key).catch(() => {});
    });
    layer.box.append(h("div", { class: "banner", role: "status" }, h("span", {}, text), close));
    shown = { key, text };
    // Both the body and the document element are replaced by some frameworks; a lost banner is drawn again after a short quiet.
    stopWatching ??= deps.changes.on(() => {
      if (shown && layer && !layer.alive() && !dismissed.has(shown.key)) draw(shown.key, shown.text);
    }, REMOUNT_MS);
  }

  async function check(): Promise<void> {
    hide();
    const url = win.location.href;
    shownFor = url;
    try {
      const flags = await deps.flags();
      if (flags.off || shownFor !== url) return;
      const view = await deps.get(url);
      if (!view.show || !view.key || shownFor !== url) return;
      const key = view.key;
      if (dismissed.has(key)) return;
      draw(key, appliedText(view));
    } catch {
      /* no banner when the application cannot be reached */
    }
  }

  void check();
  // A single-page site changes the address without a new document.
  watching = setInterval(() => {
    if (win.location.href !== last) {
      last = win.location.href;
      void check();
    }
  }, WATCH_MS);
  return {
    destroy() {
      clearInterval(watching);
      shownFor = "";
      hide();
    },
  };
}
