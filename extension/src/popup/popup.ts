import { h } from "../shared/dom";
import { normalizeHost, type StateView } from "../shared/messages";
import { failureOf, send } from "../shared/rpc";
import { renderPopup, type PageInfo, type PopupActions } from "./view";

async function activeTab(): Promise<chrome.tabs.Tab | undefined> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  return tab;
}

async function pageInfo(tab: chrome.tabs.Tab | undefined): Promise<PageInfo> {
  let host = "";
  let supported = false;
  try {
    const u = new URL(tab?.url ?? "");
    supported = u.protocol === "http:" || u.protocol === "https:";
    // A browser page (a new tab, the extensions page) has no host to switch off.
    if (supported) host = normalizeHost(u.hostname);
  } catch {
    /* a browser page has no address of ours */
  }
  const off = supported ? (await send<{ off: boolean }>({ kind: "site.get", host })).off : false;
  return { diskStamp: __BUILD_STAMP__, host, supported, off };
}

async function draw(notice = ""): Promise<void> {
  const root = document.getElementById("app");
  if (!root) return;
  const tab = await activeTab();
  let state: StateView;
  try {
    state = await send<StateView>({ kind: "state" });
  } catch (e) {
    // Only status lines are live regions, so the failure is announced as one.
    root.replaceChildren(h("p", { role: "alert" }, failureOf(e).message));
    return;
  }
  const page = await pageInfo(tab);
  const again = (n = "") => draw(n);
  const actions: PopupActions = {
    claim: async (code) => {
      await send({ kind: "pair.claim", code });
      await again();
    },
    token: async (token) => {
      await send({ kind: "pair.token", token });
      await again();
    },
    forget: async () => {
      await send({ kind: "pair.forget" });
      await again();
    },
    address: async (address) => {
      await send({ kind: "settings.address", address });
      await again("Saved.");
    },
    prefs: async (prefs) => {
      await send({ kind: "settings.prefs", prefs });
    },
    togglePanel: async () => {
      if (tab?.id !== undefined) await send({ kind: "panel.show", tabId: tab.id, toggle: true });
      window.close();
    },
    setOff: async (off) => {
      await send({ kind: "site.set", host: page.host, off });
      await again();
    },
    syncSite: async () => {
      const r = await send<{ imported: number; failed: number }>({ kind: "sync.host", host: page.host, tabId: tab?.id });
      return `Imported ${r.imported}, failed ${r.failed}.`;
    },
    recheck: () => again("Checked just now."),
    reload: () => chrome.runtime.reload(),
  };
  renderPopup(root, state, page, actions, notice);
}

void draw();
