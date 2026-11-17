// Starts the page widgets that belong to an application page and connects
// each to the worker. The per-site switch silences all of them.
import { isApplicationPage, isRegistrySite, looksLikeApplicationUrl } from "../shared/site";
import type { BannerView, ComposeView, Flags } from "../shared/messages";
import { send } from "../shared/rpc";
import { startBanner } from "./banner";
import { startEditAI } from "./editai";
import { startGuard, type Guard } from "./guard";
import { openAuthorizationWindow, startOtp, type CodeMail } from "./otp";
import { postingContext } from "./posting";
import { startRecorder } from "./recorder";
import { startSignIn, type Credential } from "./signin";
import { startSigninLink, type LinkMail } from "./signinlink";

export interface Widgets {
  setPanelOpen(open: boolean): void;
  destroy(): void;
}

const NONE: Widgets = { setPanelOpen() {}, destroy() {} };

/** The one observer the page widgets share: each registers the refresh it runs once a burst of page changes has been quiet. */
export interface PageChanges {
  /** Runs `fn` once after each burst of DOM changes has been quiet for `quietMs`; the returned function stops it. */
  on(fn: () => void, quietMs: number): () => void;
}

/** Watches the whole document (frameworks replace the body and even the document element) only while a widget is registered; registrations with the same quiet share one timer and one pass. */
export function watchPage(doc: Document): PageChanges {
  const groups = new Map<number, { fns: Set<() => void>; timer?: ReturnType<typeof setTimeout> }>();
  const pass = (fns: Set<() => void>) => {
    for (const fn of [...fns]) {
      try {
        fn();
      } catch (e) {
        queueMicrotask(() => {
          throw e;
        });
      }
    }
  };
  const observer = new MutationObserver(() => {
    for (const [ms, g] of groups) {
      clearTimeout(g.timer);
      g.timer = setTimeout(() => pass(g.fns), ms);
    }
  });
  return {
    on(fn, quietMs) {
      if (groups.size === 0) observer.observe(doc, { childList: true, subtree: true });
      let g = groups.get(quietMs);
      if (!g) groups.set(quietMs, (g = { fns: new Set() }));
      const group = g;
      group.fns.add(fn);
      return () => {
        if (!group.fns.delete(fn) || group.fns.size > 0) return;
        clearTimeout(group.timer);
        groups.delete(quietMs);
        if (groups.size === 0) observer.disconnect();
      };
    },
  };
}

export async function startWidgets(doc: Document = document, win: Window = window): Promise<Widgets> {
  const flags = () => send<Flags>({ kind: "flags.get", host: win.location.hostname });
  try {
    if ((await flags()).off) return NONE;
  } catch {
    return NONE;
  }
  const stops: (() => void)[] = [];
  const changes = watchPage(doc);
  const edit = startEditAI({
    doc,
    win,
    changes,
    flags,
    // Read on every refresh: a single-page site can route to its application form after the widgets start.
    applicationPage: () => isRegistrySite(win.location.href) || looksLikeApplicationUrl(win.location.href),
    posting: () => postingContext(doc),
    compose: (req) => send<ComposeView>({ kind: "compose.run", ...req }),
    cancel: (key) => send({ kind: "compose.cancel", key }).then(() => {}),
  });
  stops.push(edit.destroy);
  stops.push(
    startBanner({
      doc,
      win,
      changes,
      flags,
      get: (url) => send<BannerView>({ kind: "banner.get", url }),
      dismiss: (key) => send({ kind: "banner.dismiss", key }).then(() => {}),
    }).destroy,
  );
  const recorder = startRecorder({
    doc,
    win,
    record: (entry) => send({ kind: "submission.record", entry }).then(() => {}),
    applicationPage: () => isApplicationPage(doc, win),
  });
  void send({ kind: "hook.install" }).catch(() => {});
  const guard: Guard = startGuard({ doc, win, onRelease: (el) => recorder.noteReleased(el) });
  stops.push(recorder.destroy, guard.destroy);
  stops.push(
    startSignIn({
      doc,
      win,
      changes,
      resolve: () => send<Credential>({ kind: "signin.resolve" }),
      settings: () => send<{ acceptLegalDeclarations: boolean }>({ kind: "signin.settings" }),
      onStart: () => openAuthorizationWindow(),
    }).destroy,
    startOtp({ doc, win, changes, latest: (since) => send<CodeMail | null>({ kind: "otp.latest", since }) }).destroy,
    startSigninLink({ doc, win, changes, latest: (since) => send<LinkMail | null>({ kind: "signin.link", since }) }).destroy,
  );
  return {
    setPanelOpen: (open) => guard.setPanelOpen(open),
    destroy: () => stops.forEach((s) => s()),
  };
}
