// The stream of one run: a fetch-based reader, so the session key header
// travels with it. A stream that drops is attached again with the id of the
// last event; a run that has ended sends its final event and closes. Scans
// publish progress lines, so the Monitor tab follows a scan through this.
import { parseSse } from "./board";
import { parseJSON } from "./decode";
import { runFinal, runProgress } from "./decoders_discovery";
import { markSignedOut, withSessionKey } from "./session";
import type { RunFinal } from "../wire";

const RETRY_MS = 1_000;
const MAX_RETRIES = 5;

export type RunStreamEvent = { type: "progress"; line: string } | { type: "final"; final: RunFinal };

interface RunStream {
  close(): void;
}

// openRunStream calls onEvent for every progress line and for the final event,
// and onEnd once, when the run finished or the stream could not be kept.
export function openRunStream(runId: string, onEvent: (ev: RunStreamEvent) => void, onEnd: () => void): RunStream {
  const ctl = new AbortController();
  let lastId = "";
  let ended = false;
  let retries = 0;

  const end = () => {
    if (ended) return;
    ended = true;
    ctl.abort();
    onEnd();
  };

  async function attach(): Promise<void> {
    const headers: Record<string, string> = { Accept: "text/event-stream" };
    if (lastId) headers["Last-Event-ID"] = lastId;
    const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/events`, { headers: withSessionKey(headers), credentials: "same-origin", signal: ctl.signal });
    if (res.status === 401) {
      markSignedOut();
      return end();
    }
    if (res.status === 404) return end();
    if (!res.ok || !res.body) throw new Error("no stream");
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return;
      buffer += decoder.decode(value, { stream: true });
      const parsed = parseSse(buffer);
      buffer = parsed.rest;
      for (const sse of parsed.events) {
        if (sse.id) lastId = sse.id;
        if (sse.event === "shutdown" || sse.event === "reset") return;
        retries = 0;
        if (sse.event === "progress") onEvent({ type: "progress", line: runProgress(parseJSON(sse.data), "$").line });
        else if (sse.event === "final") {
          onEvent({ type: "final", final: runFinal(parseJSON(sse.data), "$") });
          return end();
        }
      }
    }
  }

  void (async () => {
    while (!ended && retries <= MAX_RETRIES) {
      try {
        await attach();
      } catch {
        // A failed read and a dropped connection end the same way: attach again.
      }
      if (ended || ctl.signal.aborted) return;
      retries++;
      await new Promise((r) => setTimeout(r, RETRY_MS));
    }
    end();
  })();

  return {
    close() {
      ended = true;
      ctl.abort();
    },
  };
}
