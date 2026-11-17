// Message routing. A message is answered only when its sender is a context of
// this extension; pairing and settings accept the popup alone, so a content
// script that a page managed to influence cannot change them.
import { ApiError } from "./api";
import type { Failure, Reply, Request } from "../shared/messages";

type Kind = Request["kind"];
type Of<K extends Kind> = Extract<Request, { kind: K }>;
type Handler<K extends Kind> = (msg: Of<K>, sender: chrome.runtime.MessageSender) => Promise<unknown>;

interface Entry {
  run: (msg: Request, sender: chrome.runtime.MessageSender) => Promise<unknown>;
  popupOnly: boolean;
}

const handlers = new Map<string, Entry>();

export function on<K extends Kind>(kind: K, run: Handler<K>, opts: { popupOnly?: boolean } = {}): void {
  handlers.set(kind, { run: run as Entry["run"], popupOnly: opts.popupOnly === true });
}

export function failure(e: unknown): Failure {
  if (e instanceof ApiError) return { code: e.code, message: e.message, retryAfter: e.retryAfter, reason: e.reason };
  return { code: "internal", message: "The extension could not do that." };
}

/** True for the extension's own popup page; the browser sets `url`, a page cannot forge it. */
export function isPopup(sender: chrome.runtime.MessageSender): boolean {
  return sender.url === chrome.runtime.getURL("popup.html");
}

export async function dispatch(msg: unknown, sender: chrome.runtime.MessageSender): Promise<Reply<unknown>> {
  if (sender.id !== chrome.runtime.id) return { ok: false, error: { code: "unauthorized", message: "Not a message of this extension." } };
  const kind = (msg as { kind?: unknown } | null)?.kind;
  const entry = typeof kind === "string" ? handlers.get(kind) : undefined;
  if (!entry) return { ok: false, error: { code: "bad_request", message: "Unknown message." } };
  if (entry.popupOnly && !isPopup(sender)) return { ok: false, error: { code: "unauthorized", message: "Only the popup may do that." } };
  try {
    return { ok: true, data: await entry.run(msg as Request, sender) };
  } catch (e) {
    return { ok: false, error: failure(e) };
  }
}

export function listen(): void {
  chrome.runtime.onMessage.addListener((msg, sender, respond) => {
    void dispatch(msg, sender).then(respond);
    return true;
  });
}
