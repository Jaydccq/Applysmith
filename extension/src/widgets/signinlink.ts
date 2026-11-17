// "Show the sign-in link": a card on a page that says a sign-in link was
// emailed. Mail is read only after the person's click, or inside the window a
// sign-in run the person started has opened. The card shows the link's host and
// never the whole address; opening it is the person's own second click, in a
// new tab, so the form they are on survives.
import { h } from "../shared/dom";
import { createLayer, type Layer } from "./layer";
import type { PageChanges } from "./main";
import { LOOKBACK_MS, windowOpen } from "./otp";

const CSS = `.card { position: fixed; z-index: 2147483644; right: 16px; bottom: 16px; width: 280px; padding: 8px 10px; background: var(--bg); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 4px 16px rgba(0,0,0,.25); } .row { display: flex; gap: 6px; margin-top: 6px; justify-content: flex-end; } p { margin: 0; }`;
const SENT = /\b(sent|emailed|e-mailed)\b[^.]{0,60}\b(sign[\s-]?in|log[\s-]?in|magic|secure|one[\s-]?time)\s+link\b|\b(sign[\s-]?in|log[\s-]?in|magic)\s+link\b[^.]{0,60}\b(sent|on its way|emailed)\b/i;

export interface LinkMail {
  url: string;
  host: string;
  receivedAt: string;
  from: string;
}

export interface SigninLinkDeps {
  doc: Document;
  win: Window;
  /** The widgets' shared page observer. */
  changes: PageChanges;
  /** Asks for the latest sign-in link that arrived after `since`; null while there is none. */
  latest(since: string): Promise<LinkMail | null>;
  now?: () => number;
}

export interface SigninLink {
  refresh(): void;
  destroy(): void;
}

const TEXT_MAX = 4000;

/** The page's first TEXT_MAX characters of text, read node by node: no layout, and no more of the page than that. */
function leadingText(doc: Document): string {
  if (!doc.body) return "";
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT, {
    acceptNode: (n) => (n.parentElement?.closest("script, style, noscript, template") ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT),
  });
  let out = "";
  for (let n = walker.nextNode(); n && out.length < TEXT_MAX; n = walker.nextNode()) out += `${n.nodeValue ?? ""} `;
  return out.slice(0, TEXT_MAX);
}

/** Whether the page says a sign-in link was sent, and asks for no password or code itself. */
export function linkSentPage(doc: Document): boolean {
  if (doc.querySelector("input[type=password]")) return false;
  return SENT.test(leadingText(doc).replace(/\s+/g, " "));
}

export function startSigninLink(deps: SigninLinkDeps): SigninLink {
  const { doc, win } = deps;
  const now = deps.now ?? Date.now;
  let layer: Layer | undefined;
  let card: HTMLElement | undefined;
  let busy = false;
  let autoRan = false;

  /** The first card, and the one Close goes back to while the page still says a link was sent. */
  function initial(): void {
    card?.remove();
    card = h("div", { class: "card" }, h("p", { class: "muted" }, "This page says a sign-in link was emailed."), h("div", { class: "row" }, h("button", { type: "button", onclick: () => void fetchLink() }, "Show the link")));
    layer?.box.append(card);
  }

  function say(text: string, tone: string, ...buttons: HTMLElement[]): void {
    card?.remove();
    card = h("div", { class: "card", role: "status" }, h("p", { class: tone }, text), h("div", { class: "row" }, ...buttons));
    layer?.box.append(card);
  }

  async function fetchLink(): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      say("Looking for the sign-in mail.", "muted");
      const link = await deps.latest(new Date(now() - LOOKBACK_MS).toISOString());
      if (!link) return say("No sign-in link has arrived yet.", "bad", h("button", { type: "button", onclick: () => void fetchLink() }, "Look again"), h("button", { type: "button", onclick: initial }, "Close"));
      const url = link.url;
      say(`Sign-in link from ${link.host}.`, "ok", h("button", { type: "button", onclick: () => win.open(url, "_blank", "noopener,noreferrer") }, "Open in a new tab"), h("button", { type: "button", onclick: initial }, "Close"));
    } catch (e) {
      say((e as { message?: string }).message || "The link could not be read.", "bad", h("button", { type: "button", onclick: initial }, "Close"));
    } finally {
      busy = false;
    }
  }

  function refresh(): void {
    if (!linkSentPage(doc)) {
      layer?.destroy();
      layer = card = undefined;
      return;
    }
    if (layer && !layer.alive()) layer = card = undefined;
    if (!layer) {
      layer = createLayer(doc, CSS);
      initial();
    }
    // Inside the window a sign-in run opened, the link is read without a second click.
    if (windowOpen(now()) && !autoRan) {
      autoRan = true;
      void fetchLink();
    }
  }

  const stopWatching = deps.changes.on(refresh, 500);
  refresh();
  return {
    refresh,
    destroy() {
      stopWatching();
      layer?.destroy();
    },
  };
}
