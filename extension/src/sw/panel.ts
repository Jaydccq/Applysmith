// Putting the panel into a page: on demand, in the top frame only.
import { ApiError } from "./api";
import { on } from "./router";
import * as store from "./store";
import type { PanelCommand } from "../shared/messages";

async function inject(tabId: number): Promise<void> {
  try {
    await chrome.scripting.executeScript({ target: { tabId, frameIds: [0] }, files: ["panel.js"] });
  } catch {
    throw new ApiError("unavailable", "The panel cannot open on this page.");
  }
}

async function command(tabId: number, kind: PanelCommand["kind"]): Promise<void> {
  await inject(tabId);
  await chrome.tabs.sendMessage(tabId, { kind } satisfies PanelCommand);
}

export function registerPanel(): void {
  // The loader's request is an automatic surface, so the per-site switch silences it.
  on("loader.inject", async (_m, sender) => {
    const tabId = sender.tab?.id;
    const url = sender.tab?.url ?? sender.url;
    if (tabId === undefined || !url) return { injected: false };
    if (await store.isOff(new URL(url).hostname)) return { injected: false };
    await inject(tabId);
    return { injected: true };
  });
  // An explicit request (the popup, the keyboard shortcut) ignores the switch.
  on("panel.show", async (m) => command(m.tabId, m.toggle ? "panel.toggle" : "panel.open"));
  chrome.commands.onCommand.addListener(async (name) => {
    if (name !== "toggle-panel") return;
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (tab?.id !== undefined) await command(tab.id, "panel.toggle").catch(() => {});
  });
}
