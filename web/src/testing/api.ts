// A scripted server for component tests: it answers fetch from a table of
// handlers, wraps replies in the real envelope and records every call.
import { vi } from "vitest";
import type { Board, BoardState } from "../api/board";
import type { ErrorCodeName } from "../wire";

export interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  body: unknown;
  headers: Headers;
}

export type Reply = { data: unknown; status?: number } | { error: ErrorCodeName; message: string; status?: number; details?: unknown; retryable?: boolean } | Response;
export type Handler = (call: Call) => Reply | Promise<Reply>;

const STATUS: Partial<Record<ErrorCodeName, number>> = {
  revision_conflict: 409, conflict: 409, validation_failed: 422, not_found: 404, unauthenticated: 401, internal: 500, unavailable: 503,
};

export function ok(data: unknown): Reply {
  return { data };
}

export function fail(error: ErrorCodeName, message: string, extra: { details?: unknown; retryable?: boolean } = {}): Reply {
  return { error, message, ...extra };
}

export interface FakeApi {
  calls: Call[];
  on(route: string, handler: Handler): void;
  callsTo(route: string): Call[];
}

export function fakeApi(routes: Record<string, Handler> = {}): FakeApi {
  const table = new Map(Object.entries(routes));
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://127.0.0.1:47319");
      const method = (init?.method ?? "GET").toUpperCase();
      const headers = new Headers(init?.headers);
      const call: Call = { method, path: url.pathname, query: url.searchParams, body: init?.body instanceof Blob ? init.body : init?.body ? JSON.parse(String(init.body)) : undefined, headers };
      calls.push(call);
      const handler = table.get(`${method} ${url.pathname}`);
      if (!handler) {
        return new Response(JSON.stringify({ ok: false, requestId: "req-test", error: { code: "not_found", message: `no handler for ${method} ${url.pathname}`, retryable: false } }), { status: 404 });
      }
      const reply = await handler(call);
      if (reply instanceof Response) return reply;
      const requestId = headers.get("X-Request-Id") ?? "req-test";
      if ("error" in reply) {
        const status = reply.status ?? STATUS[reply.error] ?? 400;
        return new Response(JSON.stringify({ ok: false, requestId, error: { code: reply.error, message: reply.message, retryable: reply.retryable ?? false, ...(reply.details ? { details: reply.details } : {}) } }), { status });
      }
      return new Response(JSON.stringify({ ok: true, requestId, data: reply.data }), { status: reply.status ?? 200 });
    }),
  );
  return {
    calls,
    on: (route, h) => void table.set(route, h),
    callsTo: (route) => calls.filter((c) => `${c.method} ${c.path}` === route),
  };
}

// sseResponse answers an event-stream route with the frames, then stays open
// as a live stream does, or ends when close is set.
export function sseResponse(frames: string[], close = false): Response {
  const enc = new TextEncoder();
  return new Response(
    new ReadableStream<Uint8Array>({
      start(c) {
        frames.forEach((f) => c.enqueue(enc.encode(f)));
        if (close) c.close();
      },
    }),
    { status: 200, headers: { "Content-Type": "text/event-stream" } },
  );
}

// manualBoard is a board the test moves by hand, as the server's notices do.
export function manualBoard(): Board & { set(revisions: Record<string, number>, epoch?: string): void; reconnect(reconnecting?: boolean): void } {
  let state: BoardState = { revisions: null, epoch: null, reconnecting: false, stale: false, links: 0 };
  const listeners = new Set<() => void>();
  return {
    getState: () => state,
    subscribe: (l) => (listeners.add(l), () => listeners.delete(l)),
    start() {},
    stop() {},
    retryNow() {},
    set(revisions, epoch = "aaaa") {
      state = { ...state, revisions, epoch };
      listeners.forEach((l) => l());
    },
    reconnect(reconnecting = false) {
      state = { ...state, reconnecting, links: state.links + (reconnecting ? 0 : 1) };
      listeners.forEach((l) => l());
    },
  };
}
