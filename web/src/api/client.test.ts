import { describe, expect, it, vi } from "vitest";
import { ApiError, RETRY_DELAY_MS, TIMEOUT_MS, fetchBlob, get, newRequestId, post, postCheck } from "./client";
import { bool, object, str } from "./decode";
import { adoptSessionKey, isSignedOut } from "./session";

const payload = object<{ name: string }>({ name: str });
const okBody = (data: unknown) => new Response(JSON.stringify({ ok: true, requestId: "r1", data }));

describe("API client", () => {
  it("unwraps the envelope and sends a request id of the allowed shape", async () => {
    const fetchMock = vi.fn(async () => okBody({ name: "alex" }));
    vi.stubGlobal("fetch", fetchMock);
    expect(await get("/api/x", payload)).toEqual({ name: "alex" });
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    const id = (init.headers as Record<string, string>)["X-Request-Id"];
    expect(id).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
    expect(newRequestId()).not.toBe(newRequestId());
  });

  it("turns a failure envelope into an ApiError with code, message, request id and field", async () => {
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ ok: false, requestId: "r-9", error: { code: "validation_failed", message: "bad", retryable: false, details: { field: "pageSize" } } }), { status: 422 }));
    const err = await get("/api/x", payload).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ kind: "validation_failed", message: "bad", requestId: "r-9", field: "pageSize", status: 422 });
  });

  it("reports an answer it cannot read as malformed, not as a crash", async () => {
    vi.stubGlobal("fetch", async () => okBody({ name: "alex", extra: true }));
    const err = await get("/api/x", payload).catch((e) => e);
    expect(err).toMatchObject({ kind: "malformed" });
    vi.stubGlobal("fetch", async () => new Response("<html>bad gateway</html>", { status: 502 }));
    expect(await get("/api/x", payload).catch((e) => e)).toMatchObject({ kind: "malformed", status: 502 });
  });

  it("retries a GET once after 300 ms on a network failure", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockRejectedValueOnce(new TypeError("offline")).mockResolvedValueOnce(okBody({ name: "ok" }));
    vi.stubGlobal("fetch", fetchMock);
    const p = get("/api/x", payload);
    await vi.advanceTimersByTimeAsync(RETRY_DELAY_MS - 1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(await p).toEqual({ name: "ok" });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("never repeats a write after a network failure", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("offline"));
    vi.stubGlobal("fetch", fetchMock);
    const err = await post("/api/x", object<{ ok: boolean }>({ ok: bool }), { a: 1 }).catch((e) => e);
    expect(err).toMatchObject({ kind: "network" });
    expect(err.requestId).toMatch(/^web-/);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("times out after thirty seconds", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", (_: string, init: RequestInit) => new Promise((_, reject) => init.signal!.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))));
    const p = get("/api/x", payload).catch((e) => e);
    await vi.advanceTimersByTimeAsync(TIMEOUT_MS);
    expect(await p).toMatchObject({ kind: "timeout" });
  });
});

describe("API client and reads asked for at the same moment", () => {
  it("shares one request between identical GETs in flight, and asks again once it answered", async () => {
    const answers: ((r: Response) => void)[] = [];
    const fetchMock = vi.fn(() => new Promise<Response>((r) => answers.push(r)));
    vi.stubGlobal("fetch", fetchMock);
    const a = get("/api/x", payload, { page: 1 });
    const b = get("/api/x", payload, { page: 1 });
    const other = get("/api/x", payload, { page: 2 });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    answers[0](okBody({ name: "alex" }));
    answers[1](okBody({ name: "page 2" }));
    expect(await a).toEqual({ name: "alex" });
    expect(await b).toEqual({ name: "alex" });
    expect(await other).toEqual({ name: "page 2" });
    const again = get("/api/x", payload, { page: 1 });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    answers[2](okBody({ name: "alex" }));
    await again;
  });

  it("does not hand a read asked for after a write the answer of one sent before it", async () => {
    const answers: ((r: Response) => void)[] = [];
    const fetchMock = vi.fn((_url: string, init: RequestInit) => (init.method === "GET" ? new Promise<Response>((r) => answers.push(r)) : Promise.resolve(okBody({ name: "saved" }))));
    vi.stubGlobal("fetch", fetchMock);
    const before = get("/api/x", payload);
    await post("/api/x", payload, {});
    const after = get("/api/x", payload);
    expect(answers).toHaveLength(2);
    answers[0](okBody({ name: "old" }));
    answers[1](okBody({ name: "new" }));
    expect(await before).toEqual({ name: "old" });
    expect(await after).toEqual({ name: "new" });
  });

  it("keeps sharing a read across a check that changes nothing (Home's setup check sent a second ready-to-apply read)", async () => {
    const answers: ((r: Response) => void)[] = [];
    const fetchMock = vi.fn((_url: string, init: RequestInit) => (init.method === "GET" ? new Promise<Response>((r) => answers.push(r)) : Promise.resolve(okBody({ name: "checked" }))));
    vi.stubGlobal("fetch", fetchMock);
    const badge = get("/api/x", payload);
    const checked = postCheck("/api/check", payload);
    const card = get("/api/x", payload);
    expect(answers).toHaveLength(1);
    answers[0](okBody({ name: "one" }));
    expect(await badge).toEqual({ name: "one" });
    expect(await card).toEqual({ name: "one" });
    expect(await checked).toEqual({ name: "checked" });
  });
});

describe("API client and the session", () => {
  const key = "K".repeat(43);

  it("sends the session key on every request once the page has one", async () => {
    window.localStorage.setItem("applysmith.sessionKey", key);
    adoptSessionKey();
    const fetchMock = vi.fn(async () => okBody({ name: "alex" }));
    vi.stubGlobal("fetch", fetchMock);
    await get("/api/x", payload);
    await post("/api/y", payload, { a: 1 });
    for (const call of fetchMock.mock.calls as unknown as [string, RequestInit][]) {
      expect((call[1].headers as Record<string, string>)["X-Applysmith-Session-Key"]).toBe(key);
    }
  });

  it("treats unauthenticated as a lost session, once and for the whole page", async () => {
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ ok: false, requestId: "r", error: { code: "unauthenticated", message: "no", retryable: false } }), { status: 401 }));
    expect(isSignedOut()).toBe(false);
    await expect(get("/api/x", payload)).rejects.toMatchObject({ kind: "unauthenticated", status: 401 });
    expect(isSignedOut()).toBe(true);
  });

  it("does not treat other refusals as a lost session", async () => {
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ ok: false, requestId: "r", error: { code: "forbidden", message: "no", retryable: false } }), { status: 403 }));
    await get("/api/x", payload).catch(() => {});
    expect(isSignedOut()).toBe(false);
  });
});

describe("fetchBlob", () => {
  it("returns the bytes of a binary answer and sends the request id", async () => {
    const fetchMock = vi.fn(async () => new Response("%PDF-1.7", { headers: { "Content-Type": "application/pdf" } }));
    vi.stubGlobal("fetch", fetchMock);
    const blob = await fetchBlob("/api/documents/pdf/1");
    expect(await blob.text()).toBe("%PDF-1.7");
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>)["X-Request-Id"]).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
  });

  it("turns a failure envelope into an ApiError and an unreadable answer into malformed", async () => {
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ ok: false, requestId: "r-3", error: { code: "not_found", message: "no PDF", retryable: false } }), { status: 404 }));
    expect(await fetchBlob("/api/documents/pdf/1").catch((e) => e)).toMatchObject({ kind: "not_found", message: "no PDF", requestId: "r-3", status: 404 });
    vi.stubGlobal("fetch", async () => new Response("<html>bad gateway</html>", { status: 502 }));
    expect(await fetchBlob("/api/documents/pdf/1").catch((e) => e)).toMatchObject({ kind: "malformed", status: 502 });
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("offline");
    });
    expect(await fetchBlob("/api/documents/pdf/1").catch((e) => e)).toMatchObject({ kind: "network", retryable: true });
  });

  // Every API route needs the session key, which only a fetch sends: a link or
  // a frame pointed at /api answers 401 (FileLink and PdfPreview fetch instead).
  it("is how the pages reach a file: no link, frame or image points at /api", () => {
    const sources = import.meta.glob(["../**/*.tsx", "!../**/*.test.tsx"], { query: "?raw", import: "default", eager: true }) as Record<string, string>;
    expect(Object.keys(sources).length).toBeGreaterThan(20);
    const direct = Object.entries(sources).filter(([, src]) => /\b(href|src)=\{?\s*(withQuery\(\s*)?[`"']\/api\//.test(src)).map(([file]) => file);
    expect(direct).toEqual([]);
  });
});
