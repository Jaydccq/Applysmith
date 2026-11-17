// A small in-memory stand-in for the chrome.* APIs the service worker uses.
import { vi } from "vitest";

type Area = Record<string, unknown>;

function area(name: string, log: string[]) {
  const data: Area = {};
  return {
    data,
    get: vi.fn(async (keys?: string | string[]) => {
      if (keys === undefined) return { ...data };
      const list = Array.isArray(keys) ? keys : [keys];
      return Object.fromEntries(list.filter((k) => k in data).map((k) => [k, data[k]]));
    }),
    set: vi.fn(async (items: Area) => {
      Object.assign(data, JSON.parse(JSON.stringify(items)));
    }),
    remove: vi.fn(async (keys: string | string[]) => {
      for (const k of Array.isArray(keys) ? keys : [keys]) delete data[k];
    }),
    setAccessLevel: vi.fn(async (o: { accessLevel: string }) => {
      log.push(`${name}:${o.accessLevel}`);
    }),
  };
}

export function installChrome(extra: Record<string, unknown> = {}) {
  const levels: string[] = [];
  const local = area("local", levels);
  const session = area("session", levels);
  const chromeMock = {
    runtime: {
      id: "ext-id",
      getURL: (p: string) => `chrome-extension://ext-id/${p}`,
      getManifest: () => ({ version: "0.1.0.16282", version_name: "0.1.0+3f9a0c27d41e8b65" }),
      sendMessage: vi.fn(),
      onMessage: { addListener: vi.fn() },
    },
    storage: { local, session },
    cookies: { getAll: vi.fn(async () => []) },
    ...extra,
  };
  (globalThis as unknown as { chrome: unknown }).chrome = chromeMock;
  return { chrome: chromeMock, local, session, levels };
}

export interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: unknown;
  signal?: AbortSignal;
}

/** Replaces fetch with a function answering from `route`; records every call. */
export function installFetch(route: (c: Call) => Response | Promise<Response>) {
  const calls: Call[] = [];
  const fake = vi.fn(async (input: URL | string, init?: RequestInit) => {
    const call: Call = {
      url: String(input),
      method: init?.method ?? "GET",
      headers: Object.fromEntries(Object.entries((init?.headers ?? {}) as Record<string, string>)),
      signal: init?.signal ?? undefined,
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    };
    calls.push(call);
    return route(call);
  });
  vi.stubGlobal("fetch", fake);
  return calls;
}

export function ok(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ ok: true, requestId: "req_test0001", data }), { status, headers: { "Content-Type": "application/json" } });
}

export function fail(code: string, message: string, status: number, extra: Record<string, unknown> = {}, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify({ ok: false, requestId: "req_test0001", error: { code, message, retryable: false, ...extra } }), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

/** A bare page object for the page-world network hook, so the hook wraps these functions and nothing in the test environment. */
export function fakeHookPage() {
  const posted: { msg: unknown; origin: string }[] = [];
  class FakeXhr {
    status = 200;
    listeners: Record<string, () => void> = {};
    open(_m: string, _u: string): string { return "opened"; }
    send(_b?: unknown): string { return "sent"; }
    addEventListener(t: string, f: () => void) { this.listeners[t] = f; }
  }
  const page = {
    location: { href: "https://example.com/jobs/1/apply", origin: "https://example.com" },
    postMessage: (msg: unknown, origin: string) => void posted.push({ msg, origin }),
    fetch: vi.fn(),
    XMLHttpRequest: FakeXhr,
  };
  return { page: page as unknown as Window & typeof globalThis, posted, FakeXhr, fetchMock: page.fetch };
}
