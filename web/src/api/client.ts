// The one typed client of the dashboard. It unwraps the envelope, sends a
// request id, retries a GET once after a network failure, never retries a
// write, and turns every failure into an ApiError.
import type { ErrorCodeName, ErrorDetails } from "../wire";
import { WireError, failure, parseJSON, success, type Decoder } from "./decode";
import { markSignedOut, withSessionKey } from "./session";

type FailureKind = ErrorCodeName | "network" | "timeout" | "malformed";

export class ApiError extends Error {
  readonly kind: FailureKind;
  readonly requestId: string;
  readonly retryable: boolean;
  readonly status: number;
  readonly details?: ErrorDetails;

  constructor(kind: FailureKind, message: string, init: { requestId: string; retryable?: boolean; status?: number; details?: ErrorDetails }) {
    super(message);
    this.name = "ApiError";
    this.kind = kind;
    this.requestId = init.requestId;
    this.retryable = init.retryable ?? false;
    this.status = init.status ?? 0;
    this.details = init.details;
  }

  get field(): string | undefined {
    return this.details?.field;
  }
}

export const TIMEOUT_MS = 30_000;
// MODEL_TIMEOUT_MS is the wait for a call that makes a model answer in the
// request. It outlasts the server's modelDeadline (internal/api/routes.go, 2
// minutes; a Go test holds the two together), so the answer, or the fallback
// the server falls back to, arrives before the page gives up.
const MODEL_TIMEOUT_MS = 130_000;
export const RETRY_DELAY_MS = 300;

export function newRequestId(): string {
  const bytes = new Uint8Array(8);
  crypto.getRandomValues(bytes);
  return "web-" + Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

type Query = Record<string, string | number | boolean | undefined>;

interface RequestOptions {
  query?: Query;
  body?: unknown;
  /** A file sent as it is, for the upload routes. */
  file?: Blob;
  headers?: Record<string, string>;
  /** A POST that changes nothing (the setup check, a schedule's preview): the reads on their way stay shared. */
  check?: boolean;
  /** A call that waits on a model (MODEL_TIMEOUT_MS); the default is TIMEOUT_MS. */
  timeoutMs?: number;
}

export function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v !== undefined && v !== "") params.set(k, String(v));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

// attempt sends one request and reads its whole body inside one deadline (30
// seconds unless the call waits on a model). The race matters because a
// stalled body never rejects on its own.
async function attempt(method: string, url: string, init: RequestInit, requestId: string, ms: number): Promise<{ res: Response; text: string }> {
  const ctl = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      ctl.abort();
      reject(new ApiError("timeout", `The request took longer than ${ms / 1000} seconds.`, { requestId, retryable: method === "GET" }));
    }, ms);
  });
  try {
    const work = (async () => {
      const res = await fetch(url, { ...init, method, signal: ctl.signal });
      return { res, text: await res.text() };
    })();
    work.catch(() => {});
    return await Promise.race([work, deadline]);
  } catch (e) {
    if (e instanceof ApiError) throw e;
    throw new ApiError("network", "The server could not be reached.", { requestId, retryable: true });
  } finally {
    clearTimeout(timer);
  }
}

// pendingReads counts the GET requests in flight, so a page can tell when what
// it asked for on opening has arrived. Writes are left out: a slow one (the
// setup check) must not hold a page.
let pendingReads = 0;
const readListeners = new Set<() => void>();

export function readsInFlight(): number {
  return pendingReads;
}

export function onReadsChange(fn: () => void): () => void {
  readListeners.add(fn);
  return () => readListeners.delete(fn);
}

// quietly runs a load whose reads the page does not wait for: it already
// draws the answer it kept (useResource) and only asks again in case.
let quiet = false;
export function quietly<T>(load: () => T): T {
  quiet = true;
  try {
    return load();
  } finally {
    quiet = false;
  }
}

export async function request<T>(method: string, path: string, dec: Decoder<T>, opts: RequestOptions = {}): Promise<T> {
  if (method !== "GET" || quiet) return send(method, path, dec, opts);
  pendingReads++;
  try {
    return await send(method, path, dec, opts);
  } finally {
    pendingReads--;
    readListeners.forEach((fn) => fn());
  }
}

// readsOnTheWay holds the plain GET requests in flight, by URL. Parts of a
// page that ask for the same thing at the same moment (the sidebar's badge and
// a Home card, two panels of one tab) share one request and each decode its
// answer; an ask after the answer arrived goes to the server again.
const readsOnTheWay = new Map<string, Promise<{ res: Response; text: string }>>();

// writes counts the writes sent, so an answer read before one is known to be
// old (useResource).
let writes = 0;
export function writesSent(): number {
  return writes;
}

// forgetReadsOnTheWay drops the shared reads, for a test that leaves a request
// unanswered on purpose.
export function forgetReadsOnTheWay(): void {
  readsOnTheWay.clear();
}

// exchange sends a request and retries a GET once after a network failure.
async function exchange(method: string, url: string, init: RequestInit, requestId: string, ms = TIMEOUT_MS): Promise<{ res: Response; text: string }> {
  try {
    return await attempt(method, url, init, requestId, ms);
  } catch (e) {
    if (method !== "GET" || !(e instanceof ApiError) || e.kind !== "network") throw e;
    await new Promise((r) => setTimeout(r, RETRY_DELAY_MS));
    return await attempt(method, url, init, requestId, ms);
  }
}

async function send<T>(method: string, path: string, dec: Decoder<T>, opts: RequestOptions): Promise<T> {
  const requestId = newRequestId();
  const headers: Record<string, string> = withSessionKey({ Accept: "application/json", "X-Request-Id": requestId, ...opts.headers });
  const init: RequestInit = { headers, credentials: "same-origin" };
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }
  if (opts.file) {
    headers["Content-Type"] = opts.file.type || "application/octet-stream";
    init.body = opts.file;
  }
  const url = withQuery(path, opts.query);

  let got: { res: Response; text: string };
  if (method === "GET" && !opts.headers) {
    let shared = readsOnTheWay.get(url);
    if (!shared) {
      const mine = exchange(method, url, init, requestId, opts.timeoutMs);
      readsOnTheWay.set(url, mine);
      mine.then(
        () => readsOnTheWay.get(url) === mine && readsOnTheWay.delete(url),
        () => readsOnTheWay.get(url) === mine && readsOnTheWay.delete(url),
      );
      shared = mine;
    }
    got = await shared;
  } else {
    // A write may change what a read on its way will answer, so the reads
    // asked for after it go to the server again. It is counted as it leaves
    // and as it ends: a read answered in between may predate it.
    const write = method !== "GET" && !opts.check;
    if (write) {
      readsOnTheWay.clear();
      writes++;
    }
    try {
      got = await exchange(method, url, init, requestId, opts.timeoutMs);
    } finally {
      if (write) writes++;
    }
  }

  const { res, text } = got;
  const id = res.headers.get("X-Request-Id") ?? requestId;
  try {
    const value = parseJSON(text);
    if (!res.ok) {
      const f = failure(value, "$");
      if (f.error.code === "unauthenticated") markSignedOut();
      throw new ApiError(f.error.code, f.error.message, { requestId: f.requestId, retryable: f.error.retryable, status: res.status, details: f.error.details as ErrorDetails | undefined });
    }
    return success(dec)(value, "$").data;
  } catch (e) {
    if (e instanceof ApiError) throw e;
    if (e instanceof WireError) throw new ApiError("malformed", `The server sent an answer this page cannot read (${e.message}).`, { requestId: id, status: res.status });
    throw e;
  }
}

export const get = <T>(path: string, dec: Decoder<T>, query?: Query) => request("GET", path, dec, { query });
export const post = <T>(path: string, dec: Decoder<T>, body?: unknown) => request("POST", path, dec, { body });
// postModel and getModel are the calls that wait on a model in the request.
export const postModel = <T>(path: string, dec: Decoder<T>, body?: unknown) => request("POST", path, dec, { body, timeoutMs: MODEL_TIMEOUT_MS });
export const getModel = <T>(path: string, dec: Decoder<T>) => request("GET", path, dec, { timeoutMs: MODEL_TIMEOUT_MS });
export const postCheck = <T>(path: string, dec: Decoder<T>, body?: unknown) => request("POST", path, dec, { body, check: true });
export const patch = <T>(path: string, dec: Decoder<T>, body: unknown) => request("PATCH", path, dec, { body });
export const put = <T>(path: string, dec: Decoder<T>, body: unknown) => request("PUT", path, dec, { body });
export const upload = <T>(path: string, dec: Decoder<T>, file: Blob, query?: Query) => request("POST", path, dec, { file, query });
export const del = <T>(path: string, dec: Decoder<T>) => request("DELETE", path, dec);

/**
 * fetchBlob reads a binary answer such as a PDF. The request carries the same
 * credentials as every other call, which is why a preview cannot simply point
 * a frame at the route: a frame cannot send the session key.
 */
export async function fetchBlob(path: string): Promise<Blob> {
  const requestId = newRequestId();
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), TIMEOUT_MS);
  // The deadline ends the request by aborting it, in the fetch or in the body read.
  const late = () => new ApiError("timeout", `The request took longer than ${TIMEOUT_MS / 1000} seconds.`, { requestId, retryable: true });
  try {
    let res: Response;
    try {
      res = await fetch(path, { headers: withSessionKey({ "X-Request-Id": requestId }), credentials: "same-origin", signal: ctl.signal });
    } catch {
      if (ctl.signal.aborted) throw late();
      throw new ApiError("network", "The server could not be reached.", { requestId, retryable: true });
    }
    if (res.ok) {
      try {
        return await res.blob();
      } catch (e) {
        throw ctl.signal.aborted ? late() : e;
      }
    }
    let value: unknown;
    try {
      value = parseJSON(await res.text());
    } catch {
      throw new ApiError("malformed", "The server sent an answer this page cannot read.", { requestId, status: res.status });
    }
    const f = failure(value, "$");
    if (f.error.code === "unauthenticated") markSignedOut();
    throw new ApiError(f.error.code, f.error.message, { requestId: f.requestId, retryable: f.error.retryable, status: res.status, details: f.error.details as ErrorDetails | undefined });
  } finally {
    clearTimeout(timer);
  }
}
