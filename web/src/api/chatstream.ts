// The chat stream of one conversation: a fetch-based reader, so the session
// key header travels with it. A stream that drops during a turn is attached
// again with the id of the last event it received; a turn that has finished
// ends the stream and the caller loads the stored transcript.
import type { ChatEvent } from "../wire";
import { parseSse } from "./board";
import { chatEvent } from "./decoders_agent";
import { parseJSON } from "./decode";
import { markSignedOut, withSessionKey } from "./session";

const RETRY_MS = 1_000;
const MAX_RETRIES = 5;

export interface ChatStream {
  close(): void;
}

// openChatStream calls onEvent for every event and onEnd once, when the turn
// finished, failed, or the stream could not be kept.
export function openChatStream(sessionId: string, onEvent: (ev: ChatEvent) => void, onEnd: () => void): ChatStream {
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
    const res = await fetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/events`, { headers: withSessionKey(headers), credentials: "same-origin", signal: ctl.signal });
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
        if (sse.event !== "chat") continue;
        const ev = chatEvent(parseJSON(sse.data), "$");
        retries = 0;
        // The first event of a connection says whether a turn is running; a
        // conversation with none has nothing to follow.
        if (ev.type === "state" && !ev.activeTurnId) return end();
        onEvent(ev);
        if (ev.type === "final" || ev.type === "error") return end();
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
