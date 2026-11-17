// The drafts that are still being decided. The Answer tab adds to and removes
// from this set; the submit guard and the Easy Apply walk read it. It is per
// page and in memory only.

const pending = new Set<string>();
const listeners = new Set<(count: number) => void>();
const clearedListeners = new Set<() => void>();

function emit(): void {
  for (const l of listeners) l(pending.size);
}

/** A draft is pending from the moment it is requested until it is filled or skipped. */
export function markPending(key: string): void {
  if (!pending.has(key)) {
    pending.add(key);
    emit();
  }
}

export function settle(key: string): void {
  if (pending.delete(key)) emit();
}

/** Skips every pending draft at once; the cards that held them are told. */
export function clearPending(): void {
  if (pending.size) {
    pending.clear();
    for (const l of clearedListeners) l();
    emit();
  }
}

export function onPendingCleared(fn: () => void): () => void {
  clearedListeners.add(fn);
  return () => clearedListeners.delete(fn);
}

export const pendingCount = (): number => pending.size;

export function onPendingChange(fn: (count: number) => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

let guarding = false;

/** True while the submit guard is holding clicks; the recorder then waits for the guard to release one. */
export const guardActive = (): boolean => guarding;
export function setGuardActive(on: boolean): void {
  guarding = on;
}
