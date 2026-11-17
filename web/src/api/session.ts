// The dashboard's second secret. The session cookie travels to every server on
// this host whatever its port; this key lives in the origin's own storage, so
// only this page can present it. The server hands it over once, in the
// fragment of the redirect that opens the dashboard, and never in a request.
import { useSyncExternalStore } from "react";

export const SESSION_KEY_HEADER = "X-Applysmith-Session-Key";
const STORAGE_KEY = "applysmith.sessionKey";
const FRAGMENT_PARAM = "k";
const KEY_SHAPE = /^[A-Za-z0-9_-]{20,128}$/;

let key: string | null = null;
let signedOut = false;
const listeners = new Set<() => void>();

function store(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function remember(value: string): void {
  key = value;
  try {
    store()?.setItem(STORAGE_KEY, value);
  } catch {
    // The key then lasts as long as this page does.
  }
}

// adoptSessionKey takes a key from the address fragment when there is one,
// else from storage, and removes the fragment from the address bar.
export function adoptSessionKey(): void {
  const params = new URLSearchParams(window.location.hash.replace(/^#/, ""));
  const fromLink = params.get(FRAGMENT_PARAM);
  if (fromLink !== null && KEY_SHAPE.test(fromLink)) {
    remember(fromLink);
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
    signedOut = false;
    return;
  }
  try {
    const saved = store()?.getItem(STORAGE_KEY) ?? null;
    if (saved !== null && KEY_SHAPE.test(saved)) key = saved;
  } catch {
    // No stored key.
  }
}

// sessionKey reads storage each time, so a tab that was opened with a newer
// link hands its key to the tabs that were open before it.
export function sessionKey(): string | null {
  try {
    const saved = store()?.getItem(STORAGE_KEY) ?? null;
    if (saved !== null && KEY_SHAPE.test(saved)) return saved;
  } catch {
    // Fall back to the copy this page holds.
  }
  return key;
}

// withSessionKey adds the key header to a set of request headers.
export function withSessionKey(headers: Record<string, string>): Record<string, string> {
  const k = sessionKey();
  return k === null ? headers : { ...headers, [SESSION_KEY_HEADER]: k };
}

export function isSignedOut(): boolean {
  return signedOut;
}

// markSignedOut is called when the server refuses this page's credentials. The
// key is dropped: the server that issued it no longer knows the session.
export function markSignedOut(): void {
  if (signedOut) return;
  signedOut = true;
  forgetKey();
  window.addEventListener("storage", adoptFromAnotherTab);
  listeners.forEach((l) => l());
}

// adoptFromAnotherTab ends the signed-out state when a new link opened in
// another tab of this browser stores its key, so this page comes back by
// itself.
function adoptFromAnotherTab(e: StorageEvent): void {
  if (!signedOut || e.key !== STORAGE_KEY || e.newValue === null || !KEY_SHAPE.test(e.newValue)) return;
  key = e.newValue;
  signedOut = false;
  listeners.forEach((l) => l());
}

function forgetKey(): void {
  key = null;
  try {
    store()?.removeItem(STORAGE_KEY);
  } catch {
    // Nothing stored to remove.
  }
}

// forgetSession resets the module, for tests.
export function forgetSession(): void {
  forgetKey();
  signedOut = false;
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useSignedOut(): boolean {
  return useSyncExternalStore(subscribe, isSignedOut);
}
