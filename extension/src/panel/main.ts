// The panel bundle: injected by the worker into the top frame's isolated world,
// once. A second injection only reuses the first panel.
import { pageJob } from "../shared/capture";
import { isSupportedSite } from "../shared/site";
import type { Connection, PanelCommand, StateView } from "../shared/messages";
import { DOC_KEY, normalizeHost } from "../shared/messages";
import { send, WorkerDisconnected } from "../shared/rpc";
import { mountAnalyze, type AnalyzeTab } from "./analyze";
import { mountAnswer, type AnswerTab } from "./answer";
import { mountAutofill, type AutofillTab } from "./autofill";
import { mountResume, type ResumeTab } from "./resume";
import { startWidgets, type Widgets } from "../widgets/main";
import { defaultTab } from "./default-tab";
import { createShell, RECHECK, type Shell, type TabDef } from "./shell";

const KEY = "__applysmithPanel";

// A token for this document, kept in the page's isolated world: a fill is started against it,
// and a replaced document does not have it.
const world = globalThis as unknown as Record<string, string>;
// getRandomValues, not randomUUID: the latter does not exist on a plain http page.
world[DOC_KEY] ??= Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");
const HEALTH_MS = 60_000;

interface Holder {
  shell?: Shell;
  ready: Promise<Shell>;
}

async function boot(): Promise<Shell> {
  const host = normalizeHost(location.hostname);
  const site = await send<{ off: boolean }>({ kind: "site.get", host });
  const saved = await send<{ open?: boolean; auto?: boolean }>({ kind: "ui.get", host }).catch(() => ({}) as { open?: boolean; auto?: boolean });
  let timer: ReturnType<typeof setInterval> | undefined;
  let shell: Shell | undefined;
  let widgets: Widgets | undefined;
  let status: Connection = "offline";
  const mounted: { setConnection(c: Connection, checked?: boolean): void; dispose(): void }[] = [];
  const connected = (c: Connection, checked = false) => {
    status = c;
    shell?.setConnection(c);
    for (const t of mounted) t.setConnection(c, checked);
  };
  // A tab is drawn the first time it is shown and then learns the current connection.
  const add = <T extends { setConnection(c: Connection): void; dispose(): void }>(tab: T): void => {
    mounted.push(tab);
    tab.setConnection(status);
  };

  let stale = false;
  const back = () => {
    if (shell?.isOpen() && !document.hidden) void refresh();
  };
  let torn = false;
  /** Stops every timer, watcher and widget of this panel; a stale panel keeps only its message. */
  const teardown = () => {
    if (torn) return;
    torn = true;
    clearInterval(timer);
    window.removeEventListener("focus", back);
    document.removeEventListener("visibilitychange", back);
    widgets?.destroy();
    // A tab's timers and watchers outlive its elements unless it is told to stop.
    for (const t of mounted) t.dispose();
  };
  // checked: the person pressed Check again, and the tabs say it was read.
  const refresh = async (checked = false) => {
    if (stale) return;
    try {
      connected((await send<{ connection: Connection }>({ kind: "health" })).connection, checked);
    } catch (e) {
      // A reloaded extension leaves this page's script cut off from it: only a page reload helps.
      if (e instanceof WorkerDisconnected && e.invalidated) {
        stale = true;
        teardown();
        shell?.stale();
      } else connected("offline", checked);
    }
  };
  const tabs: TabDef[] = [
    { id: "analyze", label: "Analyze", mount: (el) => add<AnalyzeTab>(mountAnalyze(el)) },
    { id: "answer", label: "Answer", mount: (el) => add<AnswerTab>(mountAnswer(el)) },
    { id: "resume", label: "Résumé", mount: (el) => add<ResumeTab>(mountResume(el)) },
    { id: "autofill", label: "Autofill", mount: (el) => add<AutofillTab>(mountAutofill(el)) },
  ];
  shell = await createShell({
    doc: document,
    win: window,
    tabs,
    defaultTab: defaultTab(document, location.href),
    off: site.off,
    ui: {
      get: () => send({ kind: "ui.get", host }),
      set: (patch) => void send({ kind: "ui.set", host, ...patch }).catch(() => {}),
    },
    onVisibility: (open) => {
      if (torn) return;
      widgets?.setPanelOpen(open);
      clearInterval(timer);
      if (open) {
        void refresh();
        timer = setInterval(() => void refresh(), HEALTH_MS);
      }
    },
  });

  // Check again, and coming back to the page (after starting the application, say), ask at once.
  shell.root.addEventListener(RECHECK, () => void refresh(true));
  window.addEventListener("focus", back);
  document.addEventListener("visibilitychange", back);

  // A posting page leaves its description as the tab's anchor for the application form that follows.
  void pageJob(document);

  void startWidgets().then((w) => {
    if (torn) return w.destroy();
    widgets = w;
    w.setPanelOpen(shell?.isOpen() ?? false);
  });

  // Once per tab and host, on a supported site, unless switched off or disabled.
  if (saved.open) shell.open(false);
  else if (!site.off && isSupportedSite(location.href) && !saved.auto) {
    const state = await send<StateView>({ kind: "state" }).catch(() => undefined);
    if (state?.prefs.autoOpen) {
      shell.open(false);
      void send({ kind: "ui.set", host, auto: true }).catch(() => {});
    }
  }
  const destroy = shell.destroy;
  shell.destroy = () => {
    teardown();
    destroy();
  };
  return shell;
}

/** The holder's panel; a start that failed is tried again rather than kept for the life of the document. */
function ready(holder: Holder): Promise<Shell> {
  return holder.ready.catch(() => (holder.ready = boot()));
}

function install(): void {
  const w = window as unknown as Record<string, Holder | undefined>;
  const held = w[KEY];
  if (held) {
    // A soft navigation keeps this script's context and can still take the panel's root out of the page: draw it again.
    void ready(held).then(
      (shell) => {
        if (shell.attached()) return;
        shell.destroy();
        held.ready = boot();
        held.ready.catch(() => {});
      },
      () => {},
    );
    return;
  }
  const holder: Holder = { ready: boot() };
  holder.ready.catch(() => {});
  Object.defineProperty(w, KEY, { value: holder, enumerable: false });
  chrome.runtime.onMessage.addListener((msg: PanelCommand, sender, respond) => {
    if (sender.id !== chrome.runtime.id) return false;
    void ready(holder).then(
      (shell) => {
        if (msg.kind === "panel.toggle") shell.toggle(true);
        else if (msg.kind === "panel.open") shell.open(true);
        respond(true);
      },
      () => respond(false),
    );
    return true;
  });
}

install();
