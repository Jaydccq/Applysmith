// Clean-room: docs/provenance.md#CR-ENGINE
// The core's end of the channel to the main-world helper: requests go out as
// nonce-named DOM events carrying a string, replies come back the same way.

import type { Win } from "./dom";
import { OBS_KEY, OBS_TTL_MS, reqEvent, resEvent, type HelperReply, type HelperRequest } from "./mainworld/channel";
import type { SubmitObservation } from "./protocol";

class HelperUnavailable extends Error {
  constructor(reason: string) {
    super(`binding-unavailable: ${reason}`);
    this.name = "HelperUnavailable";
  }
}

export class MainWorld {
  /** Ids start at a random offset: replies are broadcast on the document, and two clients must not read each other's. */
  private next = Math.floor(Math.random() * 1_000_000_000) + 1;
  private readonly waiting = new Map<number, (r: HelperReply) => void>();
  private readonly listener: (ev: Event) => void;

  constructor(
    private readonly win: Win,
    readonly nonce: string,
  ) {
    this.listener = (ev) => {
      try {
        const r = JSON.parse(String((ev as CustomEvent).detail)) as HelperReply;
        this.waiting.get(r.id)?.(r);
      } catch {
        /* not ours */
      }
    };
    win.document.addEventListener(resEvent(nonce), this.listener);
  }

  dispose(): void {
    this.win.document.removeEventListener(resEvent(this.nonce), this.listener);
    for (const done of Array.from(this.waiting.values())) done({ id: -1, ok: false, error: "disposed" });
    this.waiting.clear();
  }

  /** One request; rejects with HelperUnavailable when no helper answers in time. */
  request<T = unknown>(op: string, args?: unknown, timeoutMs = 3000): Promise<T> {
    const id = this.next++;
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.waiting.delete(id);
        reject(new HelperUnavailable(op));
      }, timeoutMs);
      this.waiting.set(id, (r) => {
        clearTimeout(timer);
        this.waiting.delete(id);
        if (r.ok) resolve(r.result as T);
        else reject(new Error(r.error));
      });
      const req: HelperRequest = { id, op, args };
      this.win.document.dispatchEvent(new this.win.CustomEvent(reqEvent(this.nonce), { detail: JSON.stringify(req) }));
    });
  }

  /** A request whose reply is not waited for: the client may be disposed right after. */
  send(op: string, args?: unknown): void {
    const req: HelperRequest = { id: this.next++, op, args };
    this.win.document.dispatchEvent(new this.win.CustomEvent(reqEvent(this.nonce), { detail: JSON.stringify(req) }));
  }

  /** What the submit observer saw in the last `sinceMs` milliseconds, from the storage both worlds share. */
  observations(sinceMs: number): SubmitObservation[] {
    try {
      const raw = this.win.sessionStorage.getItem(OBS_KEY);
      const now = Date.now();
      const all = raw ? (JSON.parse(raw) as SubmitObservation[]) : [];
      return all.filter((e) => now - e.time <= Math.min(sinceMs, OBS_TTL_MS)).sort((a, b) => a.time - b.time);
    } catch {
      return [];
    }
  }
}
