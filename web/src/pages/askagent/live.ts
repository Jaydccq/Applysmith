import { useCallback, useEffect, useRef, useState } from "react";
import { openChatStream, type ChatStream } from "../../api/chatstream";
import type { ChatEvent } from "../../wire";
import { stopWords } from "./Transcript";

export interface LiveTool {
  id: string;
  tool: string;
  class?: string;
  phrase?: string;
  state: "running" | "ok" | "failed" | "held";
  summary?: string;
  runId?: string;
}

export interface Live {
  text: string;
  tools: LiveTool[];
  stopped?: string;
}

const emptyLive: Live = { text: "", tools: [] };

// applyEvent folds one stream event into what the page shows while a turn runs.
function applyEvent(live: Live, ev: ChatEvent): Live {
  switch (ev.type) {
    case "turn.start":
      return emptyLive;
    case "token":
      return { ...live, text: live.text + (ev.text ?? "") };
    case "tool.call":
      return { ...live, tools: [...live.tools, { id: ev.toolCallId ?? "", tool: ev.tool ?? "", class: ev.class, phrase: ev.phrase, state: "running" }] };
    case "tool.result":
      return { ...live, tools: live.tools.map((t) => (t.id === ev.toolCallId ? { ...t, state: ev.ok === false ? "failed" : "ok", summary: ev.summary, runId: ev.runId } : t)) };
    case "tool.held":
      return { ...live, tools: live.tools.map((t) => (t.id === ev.toolCallId ? { ...t, state: "held", summary: ev.reason } : t)) };
    case "final":
      return { ...live, text: ev.text ?? live.text };
    case "error":
      return { ...live, stopped: stopWords(ev.reason, ev.detail) };
    default:
      return live;
  }
}

// useLiveTurn follows the running turn of one conversation. attach opens the
// stream; when the turn ends, onSettled runs so the page can load the stored
// transcript, and the live view is dropped except for the reason a turn stopped.
// ended names the turn a final or error event closed, so the page stops showing
// it as running even when the next read of the conversation fails (the server
// stopped) and still names it.
export function useLiveTurn(onSettled: () => void, onHeld: () => void): { live: Live | null; stopped: string | null; ended: string | null; attach: (sessionId: string) => void } {
  const [live, setLive] = useState<Live | null>(null);
  const [stopped, setStopped] = useState<string | null>(null);
  const [ended, setEnded] = useState<string | null>(null);
  const stream = useRef<ChatStream | null>(null);
  const settled = useRef(onSettled);
  const held = useRef(onHeld);
  settled.current = onSettled;
  held.current = onHeld;

  const attach = useCallback((sessionId: string) => {
    stream.current?.close();
    setStopped(null);
    setLive(emptyLive);
    let last: Live = emptyLive;
    stream.current = openChatStream(
      sessionId,
      (ev) => {
        last = applyEvent(last, ev);
        setLive(last);
        if (ev.type === "tool.held") held.current();
        if ((ev.type === "final" || ev.type === "error") && ev.turnId) setEnded(ev.turnId);
      },
      () => {
        stream.current = null;
        setStopped(last.stopped ?? null);
        setLive(null);
        settled.current();
      },
    );
  }, []);

  useEffect(
    () => () => {
      stream.current?.close();
    },
    [],
  );
  return { live, stopped, ended, attach };
}
