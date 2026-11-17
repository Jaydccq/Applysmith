import { describe, expect, it, vi } from "vitest";
import type { ChatEvent } from "../wire";
import { sseResponse } from "../testing/api";
import { openChatStream } from "./chatstream";

const frame = (id: string, ev: ChatEvent) => `id: ${id}\nevent: chat\ndata: ${JSON.stringify(ev)}\n\n`;

const settle = () => new Promise((r) => setTimeout(r, 0));

describe("openChatStream", () => {
  it("delivers events in order and ends once on the final event", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => sseResponse([frame("e-1", { type: "state", activeTurnId: "t1" }), frame("e-2", { type: "token", text: "Hi" }), frame("e-3", { type: "final", text: "Hi" })])));
    const seen: string[] = [];
    const end = vi.fn();
    openChatStream("s1", (e) => seen.push(e.type), end);
    await vi.waitFor(() => expect(end).toHaveBeenCalledTimes(1));
    expect(seen).toEqual(["state", "token", "final"]);
  });

  it("ends without events when the conversation has no running turn", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => sseResponse([frame("e-1", { type: "state" })])));
    const onEvent = vi.fn();
    const end = vi.fn();
    openChatStream("s1", onEvent, end);
    await vi.waitFor(() => expect(end).toHaveBeenCalledTimes(1));
    expect(onEvent).not.toHaveBeenCalled();
  });

  it("attaches again with the id of the last event after the connection drops", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    const calls: Array<Record<string, string>> = [];
    const answers = [sseResponse([frame("e-1", { type: "state", activeTurnId: "t1" }), frame("e-2", { type: "token", text: "A" })], true), sseResponse([frame("e-3", { type: "token", text: "B" }), frame("e-4", { type: "final", text: "AB" })])];
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
      calls.push(init.headers as Record<string, string>);
      return answers.shift()!;
    }));
    const seen: string[] = [];
    const end = vi.fn();
    openChatStream("s1", (e) => seen.push(e.type + (e.text ?? "")), end);
    await vi.advanceTimersByTimeAsync(50);
    expect(calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(end).toHaveBeenCalledTimes(1);
    expect(calls[1]["Last-Event-ID"]).toBe("e-2");
    expect(seen).toEqual(["state", "tokenA", "tokenB", "finalAB"]);
  });

  it("ends at once, without events, when the conversation is unknown", async () => {
    const fetcher = vi.fn(async () => new Response("{}", { status: 404 }));
    vi.stubGlobal("fetch", fetcher);
    const onEvent = vi.fn();
    const end = vi.fn();
    openChatStream("s1", onEvent, end);
    await settle();
    expect(end).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(onEvent).not.toHaveBeenCalled();
  });

  it("stops after a bounded number of failed attempts", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    const fetcher = vi.fn(async () => {
      throw new Error("down");
    });
    vi.stubGlobal("fetch", fetcher);
    const end = vi.fn();
    openChatStream("s1", () => {}, end);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(end).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledTimes(6);
  });

  it("does not call onEnd when the page closes the stream", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => sseResponse([frame("e-1", { type: "state", activeTurnId: "t1" })])));
    const end = vi.fn();
    const stream = openChatStream("s1", () => {}, end);
    await settle();
    stream.close();
    await settle();
    expect(end).not.toHaveBeenCalled();
  });
});
