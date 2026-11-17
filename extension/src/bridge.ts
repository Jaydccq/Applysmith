// Injected beside the engine into every frame a fill admits. It answers the
// engine's three bindings through the worker and reports what the user commits
// to a control after the fill. It holds no token and calls no API.
import { maskContacts } from "../../engine/src/fragment";
import { BINDINGS } from "../../engine/src/protocol";
import { resolveAll } from "../../engine/src/selector";
import { classifySensitivity } from "../../engine/src/sensitive";
import type { InspectRow } from "../../engine/src/protocol";
import type { UserEdit } from "./shared/messages";
import { send, WorkerDisconnected } from "./shared/rpc";

const FLAG = "__ajBridge";
const DEBOUNCE_MS = 400;
const g = globalThis as unknown as Record<string, unknown>;

function stopInvalidatedRun(payload: string, disconnected = false): boolean {
  if (!disconnected && typeof chrome !== "undefined" && chrome.runtime?.id) return false;
  const runId = (JSON.parse(payload) as { runId?: string }).runId;
  const engine = g.__applysmithEngine as { stop(runId: string, reason: string): unknown } | undefined;
  if (runId) engine?.stop?.(runId, "cancelled");
  return true;
}

async function relay(kind: "engine.match" | "engine.resume" | "engine.terminal", payload: string): Promise<string> {
  const invalid = JSON.stringify({ ok: false, error: "The extension was reloaded. Reload this page." });
  let timer: ReturnType<typeof setInterval> | undefined;
  const invalidated = new Promise<string>((resolve) => {
    timer = setInterval(() => { if (stopInvalidatedRun(payload)) resolve(invalid); }, 250);
  });
  try {
    // A worker suspension closes its pending message port without invalidating
    // this content script. One fresh message wakes the worker and reads the
    // persisted run; an invalidated extension context is never retried.
    const request = send<string>({ kind, payload }).catch((e: unknown) => {
      if (e instanceof WorkerDisconnected && !e.invalidated && !stopInvalidatedRun(payload)) return send<string>({ kind, payload });
      throw e;
    });
    const response = await Promise.race([request, invalidated]);
    // A pending message can settle before the next timer tick. Stop the engine
    // before returning its answer, so no late result can reach a writer.
    return stopInvalidatedRun(payload) ? invalid : response;
  } catch (e) {
    // A fresh message also failed: stop this exact run before the engine can
    // treat disconnection as a matcher abstention and fall through to writes.
    stopInvalidatedRun(payload, e instanceof WorkerDisconnected);
    return JSON.stringify({ ok: false, error: e instanceof Error ? e.message : "the extension did not answer" });
  } finally {
    clearInterval(timer);
  }
}

function isControl(el: Element): el is HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement {
  return el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement;
}

/** A password or one-time-code field, or a withheld self-identification field, is never reported. */
function reportable(el: Element): boolean {
  if (!isControl(el)) return false;
  if (el instanceof HTMLInputElement && ["password", "file", "hidden"].includes(el.type)) return false;
  return classifySensitivity(el, "") === null;
}

function editOf(el: Element, rows: InspectRow[]): UserEdit | undefined {
  const row = rows.find((r) => resolveAll(document, r.selector).includes(el));
  if (!row || row.value === undefined || row.value === "" || row.kind === "file") return undefined;
  // The engine withholds a value it must not report; a row that still carries one is trusted.
  if (classifySensitivity(el, row.label) !== null) return undefined;
  const edit: UserEdit = { selector: row.selector, label: row.label, kind: row.kind, value: row.value };
  if (el instanceof HTMLInputElement) edit.inputType = el.type;
  const autocomplete = (el as HTMLInputElement).autocomplete;
  if (autocomplete) edit.autocomplete = autocomplete;
  // A caption leaves the page as the engine's own events send it: an email address or a phone number in it is masked.
  if (row.section.length) edit.section = row.section.map(maskContacts);
  if (row.options) edit.options = row.options;
  return edit;
}

function watchEdits(): () => void {
  const pending = new Set<Element>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const flush = () => {
    const engine = g.__applysmithEngine as { inspect(): InspectRow[] } | undefined;
    if (!engine) return;
    const rows = engine.inspect();
    for (const el of pending) {
      const edit = editOf(el, rows);
      if (edit) void send({ kind: "engine.edit", edit }).catch(() => {});
    }
    pending.clear();
  };
  // Only a change the person made counts: a write by the fill, or by the page, is not trusted.
  const onChange = (e: Event) => {
    if (!e.isTrusted || !(e.target instanceof Element) || !reportable(e.target)) return;
    pending.add(e.target);
    clearTimeout(timer);
    timer = setTimeout(flush, DEBOUNCE_MS);
  };
  document.addEventListener("change", onChange, true);
  return () => {
    document.removeEventListener("change", onChange, true);
    clearTimeout(timer);
  };
}

// The flag holds the way to undo the injection, which the tests use.
if (!g[FLAG]) {
  Object.defineProperty(g, FLAG, { value: watchEdits(), enumerable: false, configurable: true });
  g[BINDINGS.match] = (payload: string) => relay("engine.match", payload);
  g[BINDINGS.resume] = (payload: string) => relay("engine.resume", payload);
  g[BINDINGS.terminal] = (payload: string) => relay("engine.terminal", payload);
}
