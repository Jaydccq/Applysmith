// The one always-present content script. It runs once in the top frame, makes
// no network call and asks the worker for the panel only where a form is.
import { send } from "./shared/rpc";
import { formControlCount, isRegistrySite, looksLikeApplicationUrl } from "./shared/site";

const PROBE_DEBOUNCE_MS = 500;
const PROBE_LIMIT_MS = 30_000;
const MIN_CONTROLS = 2;

async function inject(): Promise<void> {
  try {
    await send({ kind: "loader.inject" });
  } catch {
    /* the worker is asleep or the extension reloaded; the next page load asks again */
  }
}

// The worker runs this script again when a single-page site changes its address:
// a page that already holds the panel needs nothing, and an earlier watch ends.
const world = window as unknown as Record<string, unknown>;

async function run(): Promise<void> {
  const url = location.href;
  if (window !== window.top || !/^https?:$/.test(location.protocol)) return;
  if (world.__applysmithPanel) return;
  (world.__applysmithLoaderStop as (() => void) | undefined)?.();
  let off = true;
  try {
    off = (await send<{ off: boolean }>({ kind: "site.get", host: location.hostname })).off;
  } catch {
    return;
  }
  if (off) return;
  if (isRegistrySite(url) || looksLikeApplicationUrl(url)) {
    await inject();
    return;
  }
  // Any other page: wait for a form, for a short while, and stay quiet if none comes.
  let timer: ReturnType<typeof setTimeout> | undefined;
  const observer = new MutationObserver(() => {
    clearTimeout(timer);
    timer = setTimeout(check, PROBE_DEBOUNCE_MS);
  });
  const stop = () => {
    observer.disconnect();
    clearTimeout(timer);
    clearTimeout(limit);
  };
  const check = () => {
    if (formControlCount(document) >= MIN_CONTROLS) {
      stop();
      void inject();
    }
  };
  const limit = setTimeout(stop, PROBE_LIMIT_MS);
  world.__applysmithLoaderStop = stop;
  observer.observe(document.documentElement, { childList: true, subtree: true });
  check();
}

void run();
