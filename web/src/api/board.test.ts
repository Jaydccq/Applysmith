import { describe, expect, it, vi } from "vitest";
import { createBoard, parseSse } from "./board";
import { adoptSessionKey, isSignedOut } from "./session";

const encoder = new TextEncoder();

// A stream the test feeds by hand.
function liveStream() {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({ start: (c) => void (controller = c) });
  return {
    response: () => new Response(body, { headers: { "Content-Type": "text/event-stream" } }),
    send: (text: string) => controller.enqueue(encoder.encode(text)),
    end: () => controller.close(),
  };
}

const snapshot = (revs: Record<string, number>) => `id: e-1\nevent: snapshot\ndata: ${JSON.stringify({ revisions: revs })}\n\n`;
const notice = (topic: string, revision: number) => `event: notice\ndata: ${JSON.stringify({ topic, revision })}\n\n`;

describe("parseSse event ids", () => {
  it("returns the id of an event", () => {
    const { events } = parseSse('id: abc-4\nevent: snapshot\ndata: {"revisions":{}}\n\n');
    expect(events[0]).toEqual({ id: "abc-4", event: "snapshot", data: '{"revisions":{}}' });
  });
});

describe("parseSse", () => {
  it("returns complete events, keeps the unfinished one and ignores comment lines", () => {
    const { events, rest } = parseSse(": keep-alive\n\nevent: notice\ndata: {\"a\":1}\n\nevent: notice\ndata: {\"b\"");
    expect(events).toEqual([{ id: "", event: "notice", data: '{"a":1}' }]);
    expect(rest).toBe('event: notice\ndata: {"b"');
  });
});

describe("board stream", () => {
  it("holds the snapshot revisions and applies notices", async () => {
    const s = liveStream();
    vi.stubGlobal("fetch", async () => s.response());
    const board = createBoard(() => 0.5);
    board.start();
    s.send(snapshot({ tracker: 38, runs: 5 }));
    await vi.waitFor(() => expect(board.getState().revisions).toEqual({ tracker: 38, runs: 5 }));
    s.send(notice("tracker", 39));
    await vi.waitFor(() => expect(board.getState().revisions?.tracker).toBe(39));
    board.stop();
  });

  it("treats forty-five seconds of silence as a lost connection and reconnects with backoff", async () => {
    vi.useFakeTimers();
    const first = liveStream();
    const second = liveStream();
    const streams = [first, second];
    const fetchMock = vi.fn().mockImplementation(async (_: string, init: RequestInit) => {
      const s = streams.shift()!;
      const res = s.response();
      init.signal?.addEventListener("abort", () => s.end());
      return res;
    });
    vi.stubGlobal("fetch", fetchMock);
    const board = createBoard(() => 0.5);
    board.start();
    await vi.advanceTimersByTimeAsync(0);
    first.send(snapshot({ tracker: 1 }));
    await vi.advanceTimersByTimeAsync(44_999);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    board.stop();
  });
});

describe("board stream and the session", () => {
  it("sends the session key", async () => {
    window.localStorage.setItem("applysmith.sessionKey", "K".repeat(43));
    adoptSessionKey();
    const s = liveStream();
    const fetchMock = vi.fn(async () => s.response());
    vi.stubGlobal("fetch", fetchMock);
    const board = createBoard(() => 0.5);
    board.start();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>)["X-Applysmith-Session-Key"]).toBe("K".repeat(43));
    board.stop();
  });

  it("stops at an unauthenticated answer instead of retrying, and reports a lost session", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ ok: false, requestId: "r", error: { code: "unauthenticated", message: "no", retryable: false } }), { status: 401 }));
    vi.stubGlobal("fetch", fetchMock);
    const board = createBoard(() => 0.5);
    board.start();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(isSignedOut()).toBe(true);
    expect(board.getState().reconnecting).toBe(false);
    board.stop();
  });

  it("keeps the stream epoch so a restarted server is noticed", async () => {
    const first = liveStream();
    const second = liveStream();
    const streams = [first, second];
    vi.useFakeTimers();
    vi.stubGlobal("fetch", async (_: string, init: RequestInit) => {
      const s = streams.shift()!;
      init.signal?.addEventListener("abort", () => s.end());
      return s.response();
    });
    const board = createBoard(() => 0.5);
    board.start();
    await vi.advanceTimersByTimeAsync(0);
    first.send(`id: aaaa-1\nevent: snapshot\ndata: ${JSON.stringify({ revisions: { tracker: 3 } })}\n\n`);
    await vi.advanceTimersByTimeAsync(0);
    expect(board.getState().epoch).toBe("aaaa");
    first.end();
    await vi.advanceTimersByTimeAsync(2_000);
    second.send(`id: bbbb-1\nevent: snapshot\ndata: ${JSON.stringify({ revisions: { tracker: 0 } })}\n\n`);
    await vi.advanceTimersByTimeAsync(0);
    expect(board.getState()).toMatchObject({ epoch: "bbbb", revisions: { tracker: 0 } });
    board.stop();
  });
});
