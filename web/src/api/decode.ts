// Runtime checks at the wire boundary. The generated declarations only say
// what the Go side sends; these functions confirm it, using the rules of
// internal/api/wire/scalars.go: safe integers, zoned timestamps, a closed set
// of error codes and no unknown fields.
import {
  CodeBadRequest,
  CodeConfigInvalid,
  CodeConflict,
  CodeForbidden,
  CodeInternal,
  CodeMethodNotAllowed,
  CodeNotFound,
  CodePayloadTooLarge,
  CodeRateLimited,
  CodeRevisionConflict,
  CodeUnauthenticated,
  CodeUnavailable,
  CodeUnsupportedMediaType,
  CodeValidationFailed,
  type BoardNotice,
  type BoardSnapshot,
  type ErrorBody,
  type ErrorCodeName,
  type Failure,
  type Health,
  type StreamReset,
  type Success,
} from "../wire";

export class WireError extends Error {
  constructor(path: string, message: string) {
    super(`${path || "$"}: ${message}`);
    this.name = "WireError";
  }
}

export type Decoder<T> = (value: unknown, path: string) => T;

const MAX_SAFE = BigInt(Number.MAX_SAFE_INTEGER);

// Every number token outside strings. JSON.parse would round an integer
// beyond 2^53 without a word, so the text is read first.
const NUMBER_TOKEN = /-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/y;

function checkNumbers(text: string): void {
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === '"') {
      i++;
      while (i < text.length && text[i] !== '"') i += text[i] === "\\" ? 2 : 1;
      i++;
    } else if (c === "-" || (c >= "0" && c <= "9")) {
      NUMBER_TOKEN.lastIndex = i;
      const m = NUMBER_TOKEN.exec(text);
      if (!m) throw new WireError("", `malformed number at offset ${i}`);
      const token = m[0];
      if (/[eE]/.test(token)) throw new WireError("", `${token} uses exponent notation, which the wire never carries`);
      if (!token.includes(".")) {
        const abs = BigInt(token.startsWith("-") ? token.slice(1) : token);
        if (abs > MAX_SAFE) throw new WireError("", `${token} is outside the safe integer range`);
      }
      i += token.length;
    } else {
      i++;
    }
  }
}

// parseJSON is JSON.parse that refuses what the wire does not carry.
export function parseJSON(text: string): unknown {
  checkNumbers(text);
  try {
    return JSON.parse(text);
  } catch (e) {
    throw new WireError("", `not valid JSON: ${(e as Error).message}`);
  }
}

export const str: Decoder<string> = (v, path) => {
  if (typeof v !== "string") throw new WireError(path, "expected a string");
  return v;
};

export const bool: Decoder<boolean> = (v, path) => {
  if (typeof v !== "boolean") throw new WireError(path, "expected a boolean");
  return v;
};

export const num: Decoder<number> = (v, path) => {
  if (typeof v !== "number" || !Number.isFinite(v)) throw new WireError(path, "expected a number");
  return v;
};

// safeInt accepts a whole number that both languages hold exactly.
export const safeInt: Decoder<number> = (v, path) => {
  if (typeof v !== "number" || !Number.isSafeInteger(v)) throw new WireError(path, "expected a safe integer");
  return v;
};

const ZONED = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

// time requires a zone designator and answers the instant as UTC with
// millisecond precision, the form the Go side writes.
export const time: Decoder<string> = (v, path) => {
  if (typeof v !== "string" || !ZONED.test(v)) throw new WireError(path, "expected an RFC 3339 timestamp with a zone");
  const ms = Date.parse(v);
  if (Number.isNaN(ms)) throw new WireError(path, "not a valid instant");
  return new Date(ms).toISOString();
};

export const list =
  <T>(item: Decoder<T>): Decoder<T[]> =>
  (v, path) => {
    if (!Array.isArray(v)) throw new WireError(path, "expected an array");
    return v.map((x, i) => item(x, `${path}[${i}]`));
  };

// orEmpty reads an array that Go encodes as null when the slice is nil.
export const orEmpty =
  <T>(item: Decoder<T>): Decoder<T[]> =>
  (v, path) =>
    v === null ? [] : list(item)(v, path);

export const dict =
  <T>(item: Decoder<T>): Decoder<Record<string, T>> =>
  (v, path) => {
    const rec = record(v, path);
    return Object.fromEntries(Object.entries(rec).map(([k, x]) => [k, item(x, `${path}.${k}`)]));
  };

function record(v: unknown, path: string): Record<string, unknown> {
  if (typeof v !== "object" || v === null || Array.isArray(v)) throw new WireError(path, "expected an object");
  return v as Record<string, unknown>;
}

type Shape<T> = { [K in keyof T]-?: Decoder<NonNullable<T[K]>> };

// object decodes a struct: every required key must be present, an optional
// key may be absent, and no other key is accepted. Absent stays undefined and
// an explicit null stays null; only fields declared nullable accept it.
export function object<T>(required: Partial<Shape<T>>, optional: Partial<Shape<T>> = {}, nullable: readonly (keyof T)[] = []): Decoder<T> {
  return (v, path) => {
    const rec = record(v, path);
    const known = new Set<string>([...Object.keys(required), ...Object.keys(optional)]);
    for (const key of Object.keys(rec)) {
      if (!known.has(key)) throw new WireError(`${path}.${key}`, "unknown field");
    }
    const out: Record<string, unknown> = {};
    for (const [key, dec] of Object.entries(required) as [string, Decoder<unknown>][]) {
      if (!(key in rec)) throw new WireError(`${path}.${key}`, "missing field");
      out[key] = dec(rec[key], `${path}.${key}`);
    }
    for (const [key, dec] of Object.entries(optional) as [string, Decoder<unknown>][]) {
      if (!(key in rec)) continue;
      if (rec[key] === null) {
        if (!nullable.includes(key as keyof T)) throw new WireError(`${path}.${key}`, "null is not allowed here");
        out[key] = null;
        continue;
      }
      out[key] = dec(rec[key], `${path}.${key}`);
    }
    return out as T;
  };
}

const ERROR_CODES: readonly ErrorCodeName[] = [
  CodeBadRequest,
  CodeUnauthenticated,
  CodeForbidden,
  CodeNotFound,
  CodeMethodNotAllowed,
  CodeConflict,
  CodeRevisionConflict,
  CodePayloadTooLarge,
  CodeUnsupportedMediaType,
  CodeValidationFailed,
  CodeConfigInvalid,
  CodeRateLimited,
  CodeInternal,
  CodeUnavailable,
];

const errorCode: Decoder<ErrorCodeName> = (v, path) => {
  const s = str(v, path);
  const code = ERROR_CODES.find((c) => c === s);
  if (!code) throw new WireError(path, `${JSON.stringify(s)} is not an error code`);
  return code;
};

const errorBody: Decoder<ErrorBody> = object<ErrorBody>(
  { code: errorCode, message: str, retryable: bool },
  { details: (v) => v },
);

export const failure: Decoder<Failure> = (v, path) => {
  const out = object<Failure>({ ok: bool, requestId: str, error: errorBody })(v, path);
  if (out.ok) throw new WireError(`${path}.ok`, "a failure envelope has ok false");
  return out;
};

export const success =
  <T>(data: Decoder<T>): Decoder<Success<T>> =>
  (v, path) => {
    const out = object<Success<T>>({ ok: bool, requestId: str, data: data as Decoder<NonNullable<T>> })(v, path);
    if (!out.ok) throw new WireError(`${path}.ok`, "a success envelope has ok true");
    return out;
  };

export const health: Decoder<Health> = object<Health>(
  { status: str, apiVersion: safeInt, version: str, buildId: str, workspaceId: str, startedAt: time },
  {
    uptimeSeconds: safeInt,
    schemaVersion: safeInt,
    componentsCheckedAt: time,
    componentsProbeMs: safeInt,
    components: list(object<NonNullable<Health["components"]>[number]>({ name: str, status: str, detail: str })),
  },
);

export const boardSnapshot: Decoder<BoardSnapshot> = object<BoardSnapshot>({ revisions: dict(safeInt) });

export const boardNotice: Decoder<BoardNotice> = object<BoardNotice>({ topic: str, revision: safeInt }, { file: str });

export const streamReset: Decoder<StreamReset> = object<StreamReset>({ reason: str });

// decode parses text and applies a decoder to it.
export function decode<T>(text: string, dec: Decoder<T>): T {
  return dec(parseJSON(text), "$");
}
