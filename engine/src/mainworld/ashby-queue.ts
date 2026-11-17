// Clean-room: docs/provenance.md#CR-ENGINE
// Ashby saves each answer with its own whole-form request, so overlapping
// saves lose answers. While the queue is enabled, the form's save requests
// leave one at a time in the order the page asked for them, and the server's
// echo of each is remembered per field.

export interface EchoState {
  /** An echo has arrived; nothing is claimed before that. */
  seen: boolean;
  /** Field path -> the saved copy holds an answer. */
  held: Record<string, boolean>;
  /** Fields the form requires, shown and not hidden. */
  required: Record<string, boolean>;
}

export interface AshbyQueue {
  owns(method: string, url: string): boolean;
  enqueue(call: () => Promise<Response>): Promise<Response>;
  enable(ops: string): void;
  /** Ends the queue's ownership of requests; saves already queued still leave in order. */
  disable(): void;
  drain(timeoutMs: number): Promise<{ pending: number }>;
  echo(): EchoState;
}

type Json = Record<string, unknown>;
const isObj = (v: unknown): v is Json => typeof v === "object" && v !== null && !Array.isArray(v);

/** A held answer: a non-empty value, or an object with no value key (a file, a location). Null, empty text and an empty list are missing. */
export function isHeld(entry: Json): boolean {
  const raw = "value" in entry ? entry.value : "fieldValue" in entry ? entry.fieldValue : undefined;
  if (raw === undefined) return false;
  if (raw === null || raw === "") return false;
  if (Array.isArray(raw)) return raw.length > 0;
  if (isObj(raw)) return !("value" in raw) || (raw.value !== null && raw.value !== "" && !(Array.isArray(raw.value) && raw.value.length === 0));
  return true;
}

function pathOf(entry: Json): string {
  const f = entry.field;
  return String(entry.path ?? (isObj(f) ? f.path : undefined) ?? entry.id ?? "");
}

/** Visits any depth up to four for `sections` and folds each entry into the state. */
export function readEcho(body: unknown, into: EchoState, depth = 0): void {
  if (depth > 4 || !isObj(body)) return;
  if (Array.isArray(body.sections)) {
    into.seen = true;
    for (const section of body.sections) {
      if (!isObj(section)) continue;
      const shown = section.hidden !== true;
      const entries = (section.fieldEntries ?? section.fields) as unknown;
      for (const entry of Array.isArray(entries) ? entries : []) {
        if (!isObj(entry)) continue;
        const path = pathOf(entry);
        if (!path) continue;
        if (shown) into.held[path] = isHeld(entry);
        // The latest echo decides: a field the form hides is not required.
        if (shown && (entry.required === true || entry.isRequired === true) && entry.hidden !== true) into.required[path] = true;
        else delete into.required[path];
      }
    }
  }
  for (const v of Object.values(body)) readEcho(v, into, depth + 1);
}

const SUBMIT = /submit/i;

export function ashbyQueue(): AshbyQueue {
  let ops: RegExp | null = null;
  let tail: Promise<unknown> = Promise.resolve();
  let pending = 0;
  const waiters = new Set<() => void>();
  const state: EchoState = { seen: false, held: {}, required: {} };

  const settle = (): void => {
    pending--;
    if (pending === 0) for (const w of Array.from(waiters)) w();
  };
  return {
    // A submission is never held behind a save, whatever the host asked the queue to own.
    owns: (method, url) => ops !== null && method === "POST" && !SUBMIT.test(url) && ops.test(url),
    enable(source) {
      ops = new RegExp(source);
    },
    disable() {
      ops = null;
    },
    enqueue(call) {
      pending++;
      // Each request starts after the previous one has finished, whatever its outcome.
      const turn = tail.then(call, call) as Promise<Response>;
      tail = turn.then(
        async (res) => {
          try {
            readEcho(await res.clone().json(), state);
          } catch {
            /* a body that is not JSON carries no echo */
          }
        },
        () => {},
      );
      void tail.then(settle, settle);
      return turn;
    },
    drain(timeoutMs) {
      if (pending === 0) return Promise.resolve({ pending });
      return new Promise((resolve) => {
        const done = (): void => {
          clearTimeout(timer);
          waiters.delete(done);
          resolve({ pending });
        };
        const timer = setTimeout(done, timeoutMs);
        waiters.add(done);
      });
    },
    echo: () => ({ seen: state.seen, held: { ...state.held }, required: { ...state.required } }),
  };
}
