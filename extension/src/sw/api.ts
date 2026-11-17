// The single client for the local API: every network call of the extension
// goes through here, so the loopback-only rule, the bearer token, the request
// id, the timeouts and the body cap live in one place.

export const DEFAULT_ADDRESS = "http://127.0.0.1:47319";
export const CALL_TIMEOUT_MS = 30_000;
export const STREAM_IDLE_MS = 60_000;
const MAX_BODY_BYTES = 10 * 1024 * 1024;

export const OFFLINE = "offline";
export const UNAUTHENTICATED = "unauthenticated";

export class ApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status = 0,
    readonly retryAfter?: number,
    /** The server's machine-readable reason, such as `secret_not_found`. */
    readonly reason?: string,
    /** The run a conflict waits on, when the server named one. */
    readonly runId?: string,
  ) {
    super(message);
  }
}

const LOOPBACK = new Set(["127.0.0.1", "localhost", "[::1]"]);

/** The API address the user typed, reduced to its origin; only loopback is accepted. */
export function parseAddress(input: string): { ok: true; origin: string } | { ok: false; message: string } {
  let u: URL;
  try {
    u = new URL(input.trim().includes("://") ? input.trim() : `http://${input.trim()}`);
  } catch {
    return { ok: false, message: "That is not an address. Use a form like 127.0.0.1:47319." };
  }
  if (!LOOPBACK.has(u.hostname) || u.protocol !== "http:") {
    return { ok: false, message: "Only loopback addresses are allowed: 127.0.0.1, localhost or [::1], over http." };
  }
  return { ok: true, origin: u.origin };
}

export interface Connection {
  address(): Promise<string>;
  token(): Promise<string | undefined>;
  /** Called when the server rejected the stored token. */
  rejected(): Promise<void>;
  /** The X-Applysmith-Extension line naming this install, sent with the token. */
  client?(): Promise<string>;
}

export interface CallOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  query?: Record<string, string>;
  /** Pairing claims and other public routes send no token. */
  anonymous?: boolean;
  signal?: AbortSignal;
  token?: string;
  /** A stream lives as long as it keeps talking; only the idle limit ends it. */
  stream?: boolean;
  /** Sent as the Idempotency-Key header of a call that starts a run. */
  idempotencyKey?: string;
}

function requestId(): string {
  return `req_${crypto.randomUUID().replaceAll("-", "").slice(0, 16)}`;
}

function isLoopbackOrigin(origin: string): boolean {
  const r = parseAddress(origin);
  return r.ok && r.origin === origin;
}

interface Envelope {
  ok: boolean;
  data?: unknown;
  error?: { code: string; message: string; details?: { retryAfterSeconds?: number; reason?: string; runId?: string } };
}

export class Api {
  constructor(private readonly conn: Connection) {}

  private async request(path: string, opts: CallOptions, accept: string): Promise<Response> {
    const origin = await this.conn.address();
    if (!isLoopbackOrigin(origin)) throw new ApiError("forbidden", "The API address is not a loopback address.");
    const url = new URL(path, origin);
    for (const [k, v] of Object.entries(opts.query ?? {})) url.searchParams.set(k, v);
    const headers: Record<string, string> = { Accept: accept, "X-Request-Id": requestId() };
    if (!opts.anonymous) {
      const token = opts.token ?? (await this.conn.token());
      if (!token) throw new ApiError(UNAUTHENTICATED, "The extension is not paired.", 401);
      headers.Authorization = `Bearer ${token}`;
      const client = await this.conn.client?.().catch(() => "");
      if (client) headers["X-Applysmith-Extension"] = client;
    }
    if (opts.body !== undefined) headers["Content-Type"] = "application/json";
    if (opts.idempotencyKey) headers["Idempotency-Key"] = opts.idempotencyKey;
    const signals = opts.stream ? [] : [AbortSignal.timeout(CALL_TIMEOUT_MS)];
    if (opts.signal) signals.push(opts.signal);
    try {
      return await fetch(url, {
        method: opts.method ?? "GET",
        headers,
        body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
        signal: AbortSignal.any(signals),
        credentials: "omit",
        cache: "no-store",
      });
    } catch {
      throw new ApiError(OFFLINE, `The Applysmith application did not answer at ${origin}.`);
    }
  }

  /** The body, read to the cap. */
  private async read(res: Response): Promise<Uint8Array> {
    const length = Number(res.headers.get("Content-Length") ?? 0);
    if (length > MAX_BODY_BYTES) throw new ApiError("payload_too_large", "The response is larger than 10 MiB.", res.status);
    const reader = res.body?.getReader();
    if (!reader) return new Uint8Array();
    const chunks: Uint8Array[] = [];
    let total = 0;
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        total += value.byteLength;
        if (total > MAX_BODY_BYTES) {
          void reader.cancel();
          throw new ApiError("payload_too_large", "The response is larger than 10 MiB.", res.status);
        }
        chunks.push(value);
      }
    } catch (e) {
      if (e instanceof ApiError) throw e;
      throw new ApiError(OFFLINE, "The connection to the Applysmith application was lost.");
    }
    const out = new Uint8Array(total);
    let at = 0;
    for (const c of chunks) {
      out.set(c, at);
      at += c.byteLength;
    }
    return out;
  }

  private async failure(res: Response, bytes: Uint8Array): Promise<ApiError> {
    let env: Envelope = { ok: false };
    try {
      env = JSON.parse(new TextDecoder().decode(bytes)) as Envelope;
    } catch {
      /* not an envelope */
    }
    const e = env.error;
    const header = Number(res.headers.get("Retry-After") ?? "");
    const retryAfter = e?.details?.retryAfterSeconds ?? (Number.isFinite(header) && header > 0 ? header : undefined);
    return new ApiError(e?.code ?? "internal", e?.message ?? `The server answered ${res.status}.`, res.status, retryAfter, e?.details?.reason, e?.details?.runId);
  }

  /**
   * One JSON call. A read is retried once when the server cannot be reached;
   * a write never is, because a repeated write would duplicate work.
   */
  async call<T>(path: string, opts: CallOptions = {}): Promise<T> {
    const attempts = (opts.method ?? "GET") === "GET" ? 2 : 1;
    let last: ApiError | undefined;
    for (let i = 0; i < attempts; i++) {
      try {
        const res = await this.request(path, opts, "application/json");
        const bytes = await this.read(res);
        if (!res.ok) {
          const err = await this.failure(res, bytes);
          if (err.code === UNAUTHENTICATED && !opts.anonymous && !opts.token) await this.conn.rejected();
          throw err;
        }
        return (JSON.parse(new TextDecoder().decode(bytes)) as Envelope).data as T;
      } catch (e) {
        if (!(e instanceof ApiError) || e.code !== OFFLINE) throw e;
        last = e;
      }
    }
    throw last as ApiError;
  }

  /** A binary body (a document), as base64 text. It must have the media type the caller expects. */
  async bytes(path: string, mediaType: string): Promise<string> {
    const res = await this.request(path, {}, mediaType);
    const bytes = await this.read(res);
    if (!res.ok) throw await this.failure(res, bytes);
    if (!(res.headers.get("Content-Type") ?? "").toLowerCase().startsWith(mediaType)) throw new ApiError("unavailable", `The application did not return ${mediaType}.`, res.status);
    let bin = "";
    for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    return btoa(bin);
  }

  /**
   * Reads a server-sent event stream until `onEvent` returns true or the stream
   * ends. After 60 s with nothing the stream is reopened once.
   */
  async stream(path: string, onEvent: (name: string, data: unknown) => boolean | void, signal?: AbortSignal): Promise<void> {
    for (let attempt = 0; attempt < 2; attempt++) {
      const idle = new AbortController();
      let timer = setTimeout(() => idle.abort(), STREAM_IDLE_MS);
      const res = await this.request(path, { stream: true, signal: signal ? AbortSignal.any([signal, idle.signal]) : idle.signal }, "text/event-stream");
      if (!res.ok) throw await this.failure(res, await this.read(res));
      const reader = res.body?.getReader();
      if (!reader) return;
      const decoder = new TextDecoder();
      let buf = "";
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) return;
          clearTimeout(timer);
          timer = setTimeout(() => idle.abort(), STREAM_IDLE_MS);
          buf += decoder.decode(value, { stream: true });
          let cut: number;
          while ((cut = buf.indexOf("\n\n")) >= 0) {
            const block = buf.slice(0, cut);
            buf = buf.slice(cut + 2);
            let name = "message";
            let data = "";
            for (const line of block.split("\n")) {
              if (line.startsWith("event:")) name = line.slice(6).trim();
              else if (line.startsWith("data:")) data += line.slice(5).trim();
            }
            if (data && onEvent(name, JSON.parse(data))) return;
          }
        }
      } catch {
        if (signal?.aborted) return;
      } finally {
        clearTimeout(timer);
        void reader.cancel().catch(() => {});
      }
    }
    throw new ApiError(OFFLINE, "The event stream stopped answering.");
  }
}
