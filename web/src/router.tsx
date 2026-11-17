// A small history router. Pages keep their filters in the query string, so a
// reload or a shared link opens the same view.
import { useCallback, useSyncExternalStore, type AnchorHTMLAttributes, type MouseEvent, type ReactNode } from "react";

const CHANGE = "applysmith:navigate";

function subscribe(onChange: () => void) {
  window.addEventListener("popstate", onChange);
  window.addEventListener(CHANGE, onChange);
  return () => {
    window.removeEventListener("popstate", onChange);
    window.removeEventListener(CHANGE, onChange);
  };
}

const snapshot = () => window.location.pathname + window.location.search;

// A form with unsaved edits holds the page (useUnsaved): a move to another
// address, and a tab switch or close that would drop the form (leave), waits
// for its answer. Only the fragment may change freely: it picks a section of
// the same page, which keeps every form mounted. The hold calls go on Leave and
// stay on Stay.
type Ask = (go: () => void, stay: () => void) => void;
const holds: Ask[] = [];
const noop = () => {};
// shown is the address the held page is on. Back and Forward change the
// address before anything can ask, so a held page puts it back and asks.
let shown = snapshot();
window.addEventListener("popstate", () => {
  const to = snapshot();
  if (holds.length > 0 && to !== shown) {
    window.history.pushState(null, "", shown);
    holds[0](() => move(to, {}), noop);
  } else shown = to;
});
export function holdPage(ask: Ask): () => void {
  shown = snapshot();
  holds.push(ask);
  return () => void holds.splice(holds.indexOf(ask), 1);
}
export const leave = (go: () => void, stay = noop) => (holds.length > 0 ? holds[0](go, stay) : go());
// The desktop app asks through these before it reloads or replaces the page,
// restarts or switches its server, or quits (mayLeave in app/tray.go), as its
// WebKit never shows the beforeunload question. applysmithLeave answers true at
// once when nothing holds the page, else once Leave is chosen; false on Stay,
// or when a newer question takes over the dialog.
let asking: (leave: boolean) => void = noop;
Object.assign(window, {
  applysmithHeld: () => holds.length > 0,
  applysmithLeave: () =>
    new Promise<boolean>((done) => {
      asking(false);
      asking = done;
      leave(() => done(true), () => done(false));
    }),
});

// navigate is a link, a shortcut or a command: while a form holds the page,
// one that changes the path or the query asks first.
export function navigate(to: string, opts: { replace?: boolean } = {}): void {
  if (to === snapshot()) return;
  const url = new URL(to, window.location.href);
  if (holds.length > 0 && url.pathname + url.search !== snapshot()) return leave(() => move(to, opts));
  move(to, opts);
}

function move(to: string, opts: { replace?: boolean }) {
  if (opts.replace) window.history.replaceState(null, "", to);
  else window.history.pushState(null, "", to);
  shown = snapshot();
  window.dispatchEvent(new Event(CHANGE));
}

function subscribeHash(onChange: () => void) {
  const off = subscribe(onChange);
  window.addEventListener("hashchange", onChange);
  return () => {
    off();
    window.removeEventListener("hashchange", onChange);
  };
}

// useHash reads the fragment, which a page uses to pick a section of itself.
// navigate() and a plain in-page link both update it.
export function useHash(): string {
  return useSyncExternalStore(subscribeHash, () => window.location.hash.slice(1));
}

export function useLocation(): { pathname: string; search: URLSearchParams } {
  const raw = useSyncExternalStore(subscribe, snapshot);
  const q = raw.indexOf("?");
  return { pathname: q < 0 ? raw : raw.slice(0, q), search: new URLSearchParams(q < 0 ? "" : raw.slice(q + 1)) };
}

// useSearchParams reads the query and returns a setter that merges changes.
// An empty or undefined value removes the key. Filters replace the history
// entry; opening a row pushes one so Back closes it. The page owns these
// changes, so they never ask: one that drops a form goes through leave().
export function useSearchParams(): [URLSearchParams, (patch: Record<string, string | undefined>, opts?: { replace?: boolean }) => void] {
  const { pathname, search } = useLocation();
  const set = useCallback(
    (patch: Record<string, string | undefined>, opts: { replace?: boolean } = {}) => {
      const next = new URLSearchParams(window.location.search);
      for (const [k, v] of Object.entries(patch)) {
        if (v === undefined || v === "") next.delete(k);
        else next.set(k, v);
      }
      const qs = next.toString();
      const to = pathname + (qs ? `?${qs}` : "");
      if (to !== snapshot()) move(to, opts);
    },
    [pathname],
  );
  return [search, set];
}

export function Link({ to, onClick, children, ...rest }: { to: string; children: ReactNode } & AnchorHTMLAttributes<HTMLAnchorElement>) {
  const click = (e: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(e);
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || rest.target === "_blank") return;
    e.preventDefault();
    navigate(to);
  };
  return (
    <a {...rest} href={to} onClick={click}>
      {children}
    </a>
  );
}
