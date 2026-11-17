// Clean-room: docs/provenance.md#CR-ENGINE
// Single flight within one document: the work runs once, and every caller of
// that document's main-world helper that arrives while it runs gets its
// result. Each frame has its own helper, so frames never share a flight.

import type { MainWorld } from "./mainworld-client";

/**
 * Runs `work` unless another caller of the same key is already running it, in
 * which case the other's result is returned. The key clears when the leader
 * finishes, so a fresh request later runs again.
 */
export async function singleFlight<T>(main: MainWorld, key: string, work: () => Promise<T>, waitMs = 30_000): Promise<T | undefined> {
  const { leader } = await main.request<{ leader: boolean }>("flight.begin", { key });
  if (!leader) return (await main.request<{ result: T }>("flight.wait", { key, timeoutMs: waitMs }, waitMs + 1000)).result;
  let result: T | undefined;
  try {
    result = await work();
    return result;
  } finally {
    await main.request("flight.end", { key, result }).catch(() => {});
  }
}
