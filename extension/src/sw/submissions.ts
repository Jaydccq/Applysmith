// Recording that the person submitted an application. The page reports an
// entry; the worker adds the score and report the tracker knows, sends it once,
// and when the application cannot take it the entry waits in a short queue.
// A write is never retried in a loop: one attempt per entry per flush.
import type { SubmissionRecord } from "../../../web/src/wire";
import { ApiError } from "./api";
import { lookup } from "./analyze";
import { on } from "./router";
import { api } from "./state";
import * as store from "./store";
import type { QueuedSubmission } from "./store";
import type { SubmissionEntry } from "../shared/messages";

export const MAX_QUEUE = 50;
export const MAX_AGE_MS = 30 * 24 * 60 * 60 * 1000;
export const FLUSH_ALARM = "submission-flush";
export const CONFIRM_WINDOW_MS = 20_000;

const THANKS_PATH = /(thank|confirm|submitted|success|complete|received|done)/i;
/** Errors that mean the entry can never be accepted; everything else waits. */
const PERMANENT = new Set(["validation_failed", "bad_request", "forbidden", "payload_too_large"]);

interface Awaiting {
  entry: SubmissionEntry;
  until: number;
}

const awaiting = new Map<number, Awaiting>();

async function bodyFor(entry: SubmissionEntry): Promise<SubmissionRecord> {
  const body: SubmissionRecord = {
    entryId: entry.entryId,
    url: entry.url,
    host: entry.host,
    title: entry.title,
    observedAt: entry.at,
    confirmed: entry.confirmed,
    ...(entry.claimId ? { claimId: entry.claimId } : {}),
    ...(entry.company ? { company: entry.company } : {}),
    ...(entry.role ? { role: entry.role } : {}),
  };
  try {
    const known = await lookup(entry.url);
    if (known.report) {
      body.reportNumber = known.report.number;
      if (known.report.score !== undefined) body.score = known.report.score;
    }
  } catch {
    /* the score is optional; the record goes without it */
  }
  return body;
}

async function post(entry: SubmissionEntry): Promise<"sent" | "wait" | "drop"> {
  try {
    await api.call("/api/submissions", { method: "POST", body: await bodyFor(entry) });
    return "sent";
  } catch (e) {
    return e instanceof ApiError && PERMANENT.has(e.code) ? "drop" : "wait";
  }
}

async function enqueue(entry: SubmissionEntry, now: number): Promise<void> {
  const rest = (await store.get("submissionQueue")).filter((q) => q.entry.entryId !== entry.entryId);
  const queue: QueuedSubmission[] = [...rest, { queuedAt: now, entry }].filter((q) => now - q.queuedAt < MAX_AGE_MS);
  await store.put("submissionQueue", queue.slice(-MAX_QUEUE));
}

/** Sends what is waiting, oldest first, stopping at the first entry the application cannot take. */
export async function flush(now = Date.now()): Promise<void> {
  const queue = (await store.get("submissionQueue")).filter((q) => now - q.queuedAt < MAX_AGE_MS);
  if (queue.length === 0) {
    await store.put("submissionQueue", []);
    return;
  }
  if (!(await store.get("token"))) return;
  const left: QueuedSubmission[] = [];
  let blocked = false;
  for (const q of queue) {
    if (blocked) {
      left.push(q);
      continue;
    }
    const result = await post(q.entry);
    if (result === "wait") {
      blocked = true;
      left.push(q);
    }
  }
  await store.put("submissionQueue", left);
}

export async function record(entry: SubmissionEntry, tabId: number | undefined, now = Date.now()): Promise<{ queued: boolean }> {
  if (tabId !== undefined) {
    if (entry.confirmed) awaiting.delete(tabId);
    else awaiting.set(tabId, { entry, until: now + CONFIRM_WINDOW_MS });
  }
  const result = await post(entry);
  if (result === "wait") {
    await enqueue(entry, now);
    return { queued: true };
  }
  if (result === "sent") await flush(now);
  return { queued: false };
}

export function registerSubmissions(): void {
  on("submission.record", async (m, s) => {
    if (s.tab?.url && (await store.isOff(new URL(s.tab.url).hostname))) return { queued: false };
    return record(m.entry, s.tab?.id);
  });
  void chrome.alarms.create(FLUSH_ALARM, { periodInMinutes: 1 });
  chrome.alarms.onAlarm.addListener((a) => {
    if (a.name === FLUSH_ALARM) void flush().catch(() => {});
  });
  // A page that submits by loading a new document is confirmed by where it lands.
  chrome.webNavigation?.onCommitted.addListener((d) => {
    if (d.frameId !== 0) return;
    const pending = awaiting.get(d.tabId);
    if (!pending) return;
    awaiting.delete(d.tabId);
    if (Date.now() > pending.until) return;
    let path = "";
    try {
      path = new URL(d.url).pathname;
    } catch {
      return;
    }
    if (THANKS_PATH.test(path)) void record({ ...pending.entry, confirmed: true }, d.tabId).catch(() => {});
  });
}

