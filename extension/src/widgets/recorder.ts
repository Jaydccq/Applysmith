// The submission recorder. It only looks: a capture-phase listener sees the
// person's own click on a submit control and reports that an application was
// submitted. It never prevents, delays or triggers the click, and an entry
// carries the posting, never a form value.
import { reduceUrl } from "../../../engine/src/frames";
import { siteOf } from "../../../engine/src/sites";
import { postingFacts } from "../shared/capture";
import type { SubmissionEntry } from "../shared/messages";
import type { NetNote } from "../mainworld/hook";
import { guardActive } from "./drafts";
import { SUBMIT_CANDIDATES, submitControlOf } from "./submit";

export const DUPLICATE_MS = 30_000;
export const CONFIRM_MS = 20_000;
const POLL_MS = 500;

const THANKS_PATH = /(thank|confirm|submitted|success|complete|received|done)/i;
const THANKS_TEXT = /thank you for (applying|your application|submitting)|application (has been |was )?(submitted|received)|we('ve| have) received your application|successfully submitted/i;
const SUBMIT_PATH = /(apply|application|submit|candidate|job)/i;
/** Analytics, telemetry and tag hosts: a call to one of them says nothing about the application. */
const NOISE_HOST = /google-analytics|googletagmanager|doubleclick|segment\.(io|com)|sentry|datadoghq|mixpanel|amplitude|hotjar|fullstory|logrocket|newrelic|nr-data|bugsnag|intercom|facebook|clarity\.ms/i;
const STATIC_ASSET = /\.(js|mjs|css|map|png|jpe?g|gif|svg|webp|ico|woff2?|ttf)$/i;

export interface RecorderDeps {
  doc: Document;
  win: Window;
  /** Hands an entry to the worker; a failure here never reaches the page. */
  record(entry: SubmissionEntry): Promise<void>;
  /** Whether a click on this page can be an application's; a page that is not one records nothing. Absent, every page is. */
  applicationPage?(): boolean;
  now?: () => number;
}

export interface Recorder {
  /** A click the submit guard held and then let through. */
  noteReleased(el: HTMLElement): void;
  destroy(): void;
}

function pageText(doc: Document): string {
  const body = doc.body as HTMLElement | null;
  return ((body?.innerText || body?.textContent) ?? "").slice(0, 20_000);
}

/** The entry for a submission seen now: the posting and the time, never a form value. */
export function submissionEntry(doc: Document, win: Window, t: number): SubmissionEntry {
  const facts = postingFacts(doc);
  return {
    entryId: crypto.randomUUID(),
    url: reduceUrl(win.location.href),
    host: win.location.hostname,
    title: doc.title,
    at: new Date(t).toISOString(),
    confirmed: false,
    ...(facts.company ? { company: facts.company } : {}),
    ...(facts.role ? { role: facts.role } : {}),
  };
}

/**
 * Whether a call the page made can be the submission: a write that answered
 * 2xx, to a path (never a host name) that reads like the submission. It is half
 * of the evidence only: a bounced submit, an autosave or an upload answers 2xx
 * too, so the call confirms only once no submit control is left on the page.
 * Ashby's API answers a bounced submit with the same 200 as an accepted one, so
 * there only the page's own words confirm.
 */
function submittingCall(win: Window, d: Partial<NetNote>): boolean {
  if (!["POST", "PUT", "PATCH"].includes(d.method ?? "")) return false;
  if (typeof d.status !== "number" || d.status < 200 || d.status >= 300 || !d.path) return false;
  if (siteOf(win.location.href) === "ashby") return false;
  let u: URL;
  try {
    u = new URL(d.path);
  } catch {
    return false;
  }
  return !NOISE_HOST.test(u.hostname) && !STATIC_ASSET.test(u.pathname) && SUBMIT_PATH.test(u.pathname);
}

/** Whether the page still offers a submit control: then the form is still there, whatever the server answered. */
function submitShown(doc: Document): boolean {
  return [...doc.querySelectorAll(SUBMIT_CANDIDATES)].some((el) => submitControlOf(el) !== null);
}

export interface ConfirmationWatch {
  stop(): void;
  /** Settles when the watch has ended: confirmed, timed out or stopped. */
  ended: Promise<void>;
}

/**
 * Watches for CONFIRM_MS after a submission for the page's own sign that it
 * went through, and then records the entry again as confirmed. The recorder
 * starts one for the person's click and the step walker for its own press,
 * which the recorder does not see (a script's click is not trusted).
 */
export function watchConfirmation(
  deps: { doc: Document; win: Window; record(entry: SubmissionEntry): Promise<void>; now?: () => number },
  entry: SubmissionEntry,
): ConfirmationWatch {
  const { doc, win } = deps;
  const now = deps.now ?? Date.now;
  const startedAt = now();
  const page = reduceUrl(win.location.href);
  const phraseBefore = THANKS_TEXT.test(pageText(doc));
  let done = false;
  let wrote = false;
  let release: () => void = () => {};
  const ended = new Promise<void>((r) => (release = r));
  const confirm = () => {
    if (done) return;
    stop();
    void deps.record({ ...entry, confirmed: true }).catch(() => {});
  };
  const onMessage = (e: MessageEvent) => {
    const d = e.data as Partial<NetNote> | null;
    if (e.source !== win || e.origin !== win.location.origin || d?.tag !== "applysmith-net") return;
    if (!submittingCall(win, d)) return;
    wrote = true;
    if (!submitShown(doc)) confirm();
  };
  const poll = setInterval(() => {
    if (now() - startedAt > CONFIRM_MS) return stop();
    const moved = reduceUrl(win.location.href) !== page;
    if ((moved && THANKS_PATH.test(win.location.pathname)) || (!phraseBefore && THANKS_TEXT.test(pageText(doc))) || (wrote && !submitShown(doc))) confirm();
  }, POLL_MS);
  const timeout = setTimeout(() => stop(), CONFIRM_MS);
  function stop(): void {
    done = true;
    clearInterval(poll);
    clearTimeout(timeout);
    win.removeEventListener("message", onMessage);
    release();
  }
  win.addEventListener("message", onMessage);
  return { stop, ended };
}

export function startRecorder(deps: RecorderDeps): Recorder {
  const { doc, win } = deps;
  const now = deps.now ?? Date.now;
  const lastByPage = new Map<string, number>();
  const watchers = new Set<ConfirmationWatch>();

  function observe(): void {
    const t = now();
    const page = reduceUrl(win.location.href);
    const prev = lastByPage.get(page);
    if (prev !== undefined && t - prev < DUPLICATE_MS) return;
    lastByPage.set(page, t);
    const entry = submissionEntry(doc, win, t);
    void deps.record(entry).catch(() => {});
    const w = watchConfirmation({ doc, win, now, record: deps.record }, entry);
    watchers.add(w);
    void w.ended.then(() => watchers.delete(w));
  }

  // Passive: nothing here calls preventDefault or stops the event.
  const onClick = (e: MouseEvent): void => {
    if (!e.isTrusted || guardActive() || !submitControlOf(e.target)) return;
    if (deps.applicationPage && !deps.applicationPage()) return;
    observe();
  };
  win.addEventListener("click", onClick, true);

  return {
    noteReleased: () => observe(),
    destroy() {
      win.removeEventListener("click", onClick, true);
      for (const w of [...watchers]) w.stop();
    },
  };
}
