// Clean-room: docs/provenance.md#CR-ENGINE
// One piece of work at a time per document: the first caller of a key does it, and
// every caller that arrives meanwhile waits for its result instead of starting
// a second run that would race the first.

export interface Flights {
  begin(key: string): { leader: boolean };
  end(key: string, result: unknown): void;
  wait(key: string, timeoutMs: number): Promise<{ result: unknown; settled: boolean }>;
}

interface Entry {
  promise: Promise<unknown>;
  resolve: (v: unknown) => void;
}

export function flights(): Flights {
  const active = new Map<string, Entry>();
  return {
    begin(key) {
      if (active.has(key)) return { leader: false };
      let resolve: (v: unknown) => void = () => {};
      const promise = new Promise<unknown>((r) => (resolve = r));
      active.set(key, { promise, resolve });
      return { leader: true };
    },
    end(key, result) {
      const e = active.get(key);
      // The key clears first, so a fresh request after this one runs again.
      active.delete(key);
      e?.resolve(result);
    },
    async wait(key, timeoutMs) {
      const e = active.get(key);
      if (!e) return { result: undefined, settled: true };
      let timer: ReturnType<typeof setTimeout> | undefined;
      const out = await Promise.race([e.promise.then((result) => ({ result, settled: true })), new Promise<{ result: unknown; settled: boolean }>((r) => (timer = setTimeout(() => r({ result: undefined, settled: false }), timeoutMs)))]);
      clearTimeout(timer);
      return out;
    },
  };
}
