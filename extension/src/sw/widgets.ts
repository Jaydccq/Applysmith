// The worker's side of the page widgets: the field report, the applied banner
// and the page-world hook.
import type { LearningEvent, LearningEventsResult } from "../../../web/src/wire";
import { reduceUrl, classifyFrame, probeDecision } from "../../../engine/src/frames";
import { ApiError } from "./api";
import { lookup, APPLIED_OR_LATER } from "./analyze";
import { on } from "./router";
import { api } from "./state";
import * as store from "./store";
import type { BannerView, FieldReport } from "../shared/messages";
import { injectFiles, lightProbeFn, run } from "./fill";

const MAX_DISMISSED = 200;
/** The ledger's source of a submission the application chain made. */
const APPLY_CHAIN = "apply-chain";
/** The ledger's sources of a submission the extension saw the person make. */
const SEEN_BY_EXTENSION = new Set(["user-observed", "extension-claim"]);
const MAX_NOTE = 500;
const MAX_OPTIONS = 25;

/** The learning event for a pointed-at field: the question and its place, no value. */
export function fieldEvent(r: FieldReport, host: string, now = new Date()): LearningEvent {
  const ev: LearningEvent = {
    eventId: crypto.randomUUID(),
    kind: "user-reported",
    host,
    question: r.question.slice(0, 500),
    controlKind: r.controlKind,
    options: r.options.slice(0, MAX_OPTIONS),
    at: now.toISOString(),
    report: {
      note: r.note.slice(0, MAX_NOTE),
      tag: r.tag,
      role: r.role,
      accessibleName: r.accessibleName,
      path: r.path,
      nearbyText: r.nearbyText,
      url: reduceUrl(r.url),
      frameUrl: reduceUrl(r.frameUrl),
      shadowDepth: r.shadowDepth,
      snippet: r.snippet,
    },
  };
  if (r.inputType) ev.inputType = r.inputType;
  return ev;
}

async function sendReport(r: FieldReport, pageUrl: string): Promise<void> {
  const result = await api.call<LearningEventsResult>("/api/answer-bank/events", {
    method: "POST",
    body: { events: [fieldEvent(r, new URL(pageUrl).hostname)] },
  });
  const refused = result.rejected?.[0];
  if (refused) throw new ApiError("rejected", `The report was not accepted (${refused.reason}).`);
}

export async function bannerFor(url: string): Promise<BannerView> {
  if (await store.isOff(new URL(url).hostname)) return { show: false };
  let found;
  try {
    found = await lookup(url, false);
  } catch {
    return { show: false };
  }
  const sub = found.submission;
  const applied = found.found && found.status !== undefined && APPLIED_OR_LATER.has(found.status);
  if (!found.identityKey || (!applied && !sub)) return { show: false };
  if ((await store.get("dismissed")).includes(found.identityKey)) return { show: false, key: found.identityKey };
  // A submission of the ledger is quoted over the tracker's status, so that an unconfirmed click never reads as an application.
  if (sub) {
    const agent = sub.source === APPLY_CHAIN;
    return { show: true, key: found.identityKey, date: sub.at, submission: { confirmed: sub.confirmed, agent, fromLedger: !SEEN_BY_EXTENSION.has(sub.source) } };
  }
  return { show: true, key: found.identityKey, date: found.appliedAt?.slice(0, 10) };
}

export function registerWidgets(): void {
  // The switches page widgets obey; read from storage, no network call.
  on("flags.get", async (m) => ({ off: await store.isOff(m.host), editWithAI: (await store.prefs()).editWithAI }));
  on("report.field", async (m, s) => {
    if (s.tab?.url === undefined) throw new ApiError("bad_request", "That needs a page.");
    await sendReport(m.report, s.tab.url);
  });
  on("banner.get", (m) => bannerFor(m.url));
  on("banner.dismiss", async (m) => {
    const rest = (await store.get("dismissed")).filter((k) => k !== m.key);
    await store.put("dismissed", [...rest, m.key].slice(-MAX_DISMISSED));
  });
  on("hook.install", async (_m, s) => {
    const tabId = s.tab?.id;
    if (tabId === undefined || s.frameId !== 0 || !s.tab?.url || (await store.isOff(new URL(s.tab.url).hostname))) return { installed: false };
    // The application iframes of this page get the recorder too, whatever happens to the top frame's hook.
    void armFrames(tabId).catch(() => {});
    try {
      await chrome.scripting.executeScript({ target: { tabId }, files: ["hook.js"], world: "MAIN" });
      return { installed: true };
    } catch {
      return { installed: false };
    }
  });
}

// Where the submission recorder runs besides the top frame's widgets. Once the
// top frame of a tab has started its widgets (an application page on a host not
// switched off), every application iframe of that tab gets the recorder and the
// page-world network hook, including a frame that loads later. A same-document
// address change runs the loader again, so an application form a single-page
// site shows after the loader's watch has ended still gets the widgets. Every
// install is idempotent: one click is never recorded twice.
const armedKey = (tabId: number) => `observe:${tabId}`;

/** The recorder and the hook into one sub-frame, when it is an application frame. */
async function armFrame(tabId: number, frameId: number, url: string): Promise<void> {
  const cls = classifyFrame(url);
  if (cls === "denylist" || cls === "not-http") return;
  if (cls === "probe") {
    if (!(await injectFiles(tabId, frameId, ["probe.js"]))) return;
    const probe = await run<{ controls: number; required: number }>(tabId, frameId, lightProbeFn);
    if (!probe || probeDecision(String(frameId), url, probe).outcome !== "admitted-probe") return;
  }
  if (!(await injectFiles(tabId, frameId, ["frame.js"]))) return;
  try {
    await chrome.scripting.executeScript({ target: { tabId, frameIds: [frameId] }, files: ["hook.js"], world: "MAIN" });
  } catch {
    /* the recorder still sees the click; only the network confirmation is lost */
  }
}

/** Marks the tab as watched and arms every sub-frame it holds now. */
export async function armFrames(tabId: number): Promise<void> {
  await store.sessionPut(armedKey(tabId), true);
  const frames = (await chrome.webNavigation.getAllFrames({ tabId })) ?? [];
  for (const f of frames) {
    if (f.frameId === 0 || f.documentLifecycle === "prerender" || f.errorOccurred) continue;
    await armFrame(tabId, f.frameId, f.url);
  }
}

export function registerObserve(): void {
  chrome.webNavigation.onCommitted.addListener((d) => {
    // A new top document starts unwatched; its widgets arm it again.
    if (d.frameId === 0) void store.sessionRemove(armedKey(d.tabId)).catch(() => {});
  });
  chrome.webNavigation.onCompleted.addListener((d) => {
    if (d.frameId === 0) return;
    void (async () => {
      if (await store.sessionGet<boolean>(armedKey(d.tabId))) await armFrame(d.tabId, d.frameId, d.url);
    })().catch(() => {});
  });
  chrome.webNavigation.onHistoryStateUpdated.addListener((d) => {
    if (d.frameId !== 0) return;
    void (async () => {
      if (await store.isOff(new URL(d.url).hostname)) return;
      await injectFiles(d.tabId, 0, ["loader.js"]);
    })().catch(() => {});
  });
  chrome.tabs.onRemoved.addListener((tabId) => void store.sessionRemove(armedKey(tabId)).catch(() => {}));
}
