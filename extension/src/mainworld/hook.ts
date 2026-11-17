// Runs in the page's own world, on application pages only, injected by the
// worker. It watches the page's own requests and tells the extension's script
// in this document the method, the status and the path of each accepted POST,
// PUT or PATCH. It never blocks, changes, delays or repeats a request, never
// throws into page code, reads no body, header or query, and posts only to the
// page's own origin.

export const HOOK_TAG = "applysmith-net";
const INSTALLED = Symbol.for("applysmith.hook");

export interface NetNote {
  tag: typeof HOOK_TAG;
  method: string;
  status: number;
  /** Origin and path of the request, without query or fragment. */
  path: string;
}

function note(win: Window, method: string, status: number, target: string): void {
  try {
    // Only a write that was accepted can say the application went through.
    if (!/^(POST|PUT|PATCH)$/i.test(method) || !(status >= 200 && status < 300)) return;
    const u = new URL(target, win.location.href);
    const msg: NetNote = { tag: HOOK_TAG, method: method.toUpperCase(), status, path: `${u.origin}${u.pathname}` };
    win.postMessage(msg, win.location.origin);
  } catch {
    /* an observer must never disturb the page */
  }
}

export function installHook(win: Window & typeof globalThis): void {
  const w = win as unknown as Record<symbol, unknown>;
  if (w[INSTALLED]) return;
  Object.defineProperty(win, INSTALLED, { value: true, enumerable: false });

  const original = win.fetch;
  if (typeof original === "function") {
    win.fetch = function (this: unknown, input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
      const result = original.call(this, input, init);
      try {
        const method = init?.method ?? (input instanceof Request ? input.method : "GET");
        const target = input instanceof Request ? input.url : String(input);
        // A separate branch: the page keeps the very promise it was given.
        result.then(
          (res) => note(win, method, res.status, target),
          () => {},
        );
      } catch {
        /* see note() */
      }
      return result;
    } as typeof win.fetch;
  }

  const proto = win.XMLHttpRequest?.prototype;
  if (proto) {
    const open = proto.open;
    const send = proto.send;
    const seen = new WeakMap<XMLHttpRequest, { method: string; url: string }>();
    proto.open = function (this: XMLHttpRequest, method: string, url: string | URL, ...rest: unknown[]): void {
      try {
        seen.set(this, { method, url: String(url) });
      } catch {
        /* see note() */
      }
      return (open as (...a: unknown[]) => void).call(this, method, url, ...rest);
    } as typeof proto.open;
    proto.send = function (this: XMLHttpRequest, body?: Document | XMLHttpRequestBodyInit | null): void {
      try {
        const info = seen.get(this);
        if (info) this.addEventListener("loadend", () => note(win, info.method, this.status, info.url));
      } catch {
        /* see note() */
      }
      return send.call(this, body);
    } as typeof proto.send;
  }
}

installHook(globalThis as unknown as Window & typeof globalThis);
