// Clean-room: docs/provenance.md#CR-ENGINE
// The main-world helper. It runs in the page's own world because it must wrap
// the page's network calls, reach framework state hung on page objects and
// call a component's own methods. It holds no profile data and no binding, it
// answers only requests that carry the per-run nonce in their event name, and
// nothing it does may throw into page code.

import { ashbyQueue, type AshbyQueue } from "./ashby-queue";
import { OBS_MAX, OBS_TTL_MS, OBS_KEY, reqEvent, resEvent, type HelperReply, type HelperRequest } from "./channel";
import { callComponent, callReact, commitSdf, setTinymce, srSearch, srValidity } from "./components";
import { flights } from "./flight";
import { SfUpload } from "./sf-upload";

export const HOLDER = Symbol.for("applysmith.mainworld");

type W = Window & typeof globalThis;

interface Observation {
  time: number;
  kind: "request" | "submit-event";
  method?: string;
  status?: number;
  url: string;
}

interface Holder {
  stamp: string;
  /** Whether this instance answers requests under the nonce. The nonce itself is never readable from the page. */
  listens(nonce: string): boolean;
  dispose(): void;
}

function reduce(win: W, raw: string): string {
  try {
    const u = new URL(raw, win.location.href);
    return `${u.origin}${u.pathname}`;
  } catch {
    return "";
  }
}

/**
 * Installs the helper for one document, replacing first an instance of another build or one that listens on another
 * nonce (a later host of the same document has a nonce of its own).
 */
export function installHelper(win: W, nonce: string, stamp: string): void {
  const slot = win as unknown as Record<symbol, Holder | undefined>;
  const old = slot[HOLDER];
  if (old?.stamp === stamp && old.listens(nonce)) return;
  old?.dispose();

  const origFetch = win.fetch;
  const origOpen = win.XMLHttpRequest?.prototype.open;
  const origSend = win.XMLHttpRequest?.prototype.send;
  const queue: AshbyQueue = ashbyQueue();
  const sf = new SfUpload(win);
  const once = flights();

  const record = (o: Observation): void => {
    try {
      const raw = win.sessionStorage.getItem(OBS_KEY);
      const now = Date.now();
      const list = (raw ? (JSON.parse(raw) as Observation[]) : []).filter((e) => now - e.time <= OBS_TTL_MS);
      list.push(o);
      win.sessionStorage.setItem(OBS_KEY, JSON.stringify(list.slice(-OBS_MAX)));
    } catch {
      /* storage may be unavailable; evidence is best effort */
    }
  };
  const noteRequest = (method: string, url: string, status: number): void => {
    if (!/^(POST|PUT|PATCH)$/.test(method) || status < 200 || status > 299) return;
    record({ time: Date.now(), kind: "request", method, status, url: reduce(win, url) });
  };

  // Network calls are observed, never altered. A request the Ashby queue owns waits for its turn.
  win.fetch = function patchedFetch(this: unknown, ...args: Parameters<typeof fetch>): Promise<Response> {
    const call = (): Promise<Response> => origFetch.apply(this ?? win, args);
    let method = "GET";
    let url = "";
    try {
      const input = args[0] as RequestInfo | URL;
      const req = typeof input === "object" && input !== null && "url" in input ? (input as Request) : null;
      url = req ? req.url : String(input);
      method = String(args[1]?.method ?? req?.method ?? "GET").toUpperCase();
    } catch {
      return call();
    }
    let p: Promise<Response>;
    try {
      p = queue.owns(method, url) ? queue.enqueue(call) : call();
    } catch {
      return call();
    }
    try {
      p.then((res) => noteRequest(method, url, res.status)).catch(() => {});
    } catch {
      /* ignore */
    }
    return p;
  } as typeof fetch;

  if (win.XMLHttpRequest && origOpen && origSend) {
    const meta = new WeakMap<XMLHttpRequest, { method: string; url: string }>();
    win.XMLHttpRequest.prototype.open = function patchedOpen(this: XMLHttpRequest, ...a: unknown[]): void {
      try {
        meta.set(this, { method: String(a[0]).toUpperCase(), url: String(a[1]) });
      } catch {
        /* ignore */
      }
      return (origOpen as (...x: unknown[]) => void).apply(this, a);
    } as typeof origOpen;
    win.XMLHttpRequest.prototype.send = function patchedSend(this: XMLHttpRequest, body?: Document | XMLHttpRequestBodyInit | null): void {
      try {
        const m = meta.get(this);
        if (m) this.addEventListener("loadend", () => noteRequest(m.method, m.url, this.status));
      } catch {
        /* ignore */
      }
      return origSend.call(this, body);
    } as typeof origSend;
  }

  const onSubmit = (ev: Event): void => {
    try {
      const form = ev.target as HTMLFormElement | null;
      record({ time: Date.now(), kind: "submit-event", url: reduce(win, form?.action ?? win.location.href) });
    } catch {
      /* ignore */
    }
  };
  win.addEventListener("submit", onSubmit, true);

  const reply = (r: HelperReply): void => {
    try {
      win.document.dispatchEvent(new win.CustomEvent(resEvent(nonce), { detail: JSON.stringify(r) }));
    } catch {
      /* ignore */
    }
  };
  const handle = async (req: HelperRequest): Promise<unknown> => {
    const a = (req.args ?? {}) as Record<string, unknown>;
    switch (req.op) {
      case "queue.enable":
        queue.enable(String(a.ops ?? ""));
        return {};
      case "queue.disable":
        queue.disable();
        return {};
      case "queue.drain":
        return queue.drain(Number(a.timeoutMs ?? 10_000));
      case "queue.echo":
        return queue.echo();
      case "sf.stage":
        return sf.stage(String(a.name), String(a.type), String(a.base64), Number(a.ttlMs ?? 60_000));
      case "sf.attach":
        return sf.attach(String(a.selector));
      case "sf.clear":
        sf.clear();
        return {};
      case "flight.begin":
        return once.begin(String(a.key));
      case "flight.end":
        once.end(String(a.key), a.result);
        return {};
      case "flight.wait":
        return once.wait(String(a.key), Number(a.timeoutMs ?? 30_000));
      case "react.call":
        return callReact(win, String(a.selector), String(a.prop), (a.event ?? {}) as Record<string, unknown>);
      case "component.call":
        return callComponent(win, String(a.selector), String(a.method), a.args as unknown[], Number(a.timeoutMs ?? 2500));
      case "sr.search":
        return srSearch(win, String(a.selector), a.close === true);
      case "sr.validity":
        return srValidity(win, String(a.selector));
      case "sdf.commit":
        return commitSdf(win, String(a.selector), String(a.value ?? ""), String(a.label ?? ""));
      case "tinymce.set":
        return setTinymce(win, String(a.id), String(a.text ?? ""));
      default:
        throw new Error(`unknown op ${req.op}`);
    }
  };
  const onRequest = (ev: Event): void => {
    let req: HelperRequest;
    try {
      req = JSON.parse(String((ev as CustomEvent).detail)) as HelperRequest;
    } catch {
      return;
    }
    handle(req).then(
      (result) => reply({ id: req.id, ok: true, result }),
      (e: unknown) => reply({ id: req.id, ok: false, error: e instanceof Error ? e.message : String(e) }),
    );
  };
  win.document.addEventListener(reqEvent(nonce), onRequest);

  const restore = sf.install();
  slot[HOLDER] = {
    stamp,
    listens: (asked) => asked === nonce,
    dispose() {
      win.fetch = origFetch;
      if (win.XMLHttpRequest && origOpen && origSend) {
        win.XMLHttpRequest.prototype.open = origOpen;
        win.XMLHttpRequest.prototype.send = origSend;
      }
      win.removeEventListener("submit", onSubmit, true);
      win.document.removeEventListener(reqEvent(nonce), onRequest);
      restore();
      delete slot[HOLDER];
    },
  };
}
