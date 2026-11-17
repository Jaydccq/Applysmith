// Hosting the engine: the worker decides which frames get it, starts it on the
// user's request, serves its three bindings and reports what happened.
import type { AnswerBankSettings, AnswerMatchRequest, AnswerMatchResult, FillRunEnd, FillRunRequest, PostingRead, RunAccepted } from "../../../web/src/wire";
import { failedDecision, probeDecision, reduceUrl, staticDecision } from "../../../engine/src/frames";
import type {
  ApplicantSnapshot,
  BindingReply,
  FrameDecision,
  LearningEvent as EngineEvent,
  MatchRequest,
  MatchResponse,
  Resolution,
  ResumeRequest,
  Row,
  StartArgs,
  StartReply,
  StatusReport,
  Summary,
  TerminalNotice,
} from "../../../engine/src/protocol";
import { ApiError, UNAUTHENTICATED } from "./api";
import { Uploader, frameEvent, manualFill, reportSystemWrites, type EditContext } from "./learning";
import { on } from "./router";
import * as resumes from "./resumes";
import { api } from "./state";
import * as store from "./store";
import { postingOf } from "../shared/capture";
import type { FillPhase, FillView, PostingHint, RowView, UserEdit } from "../shared/messages";
import { anchorFrom, resolveJobContext, type JobContext, type PageReading } from "../../../engine/src/capture";

const MAX_FRAMES = 30;
const MAX_ROWS = 300;

interface FrameRun {
  frameId: number;
  status?: StatusReport;
  notice?: TerminalNotice;
  stall?: boolean;
}

interface Fill {
  tabId: number;
  /** The panel's token of the document the fill was started on; a fill never outlives that document. */
  doc: string;
  host: string;
  /** Origin and path of the page the fill started on. */
  page: string;
  /** The posting facts the page gave; the job text is not kept past the start. */
  posting: PostingHint;
  runId: string;
  startedAt: number;
  phase: FillPhase;
  message?: string;
  decisions: FrameDecision[];
  frames: Map<number, FrameRun>;
  /** What the system wrote, by selector, and the bank entry behind it. */
  context: EditContext;
  uploader: Uploader;
  /** True once the profile pass's writes were reported; edits count only after. */
  reported: boolean;
  finalizing: boolean;
  view?: Omit<FillView, "phase" | "runId" | "frames" | "elapsedMs">;
  /** The last record written to session storage, to skip identical writes. */
  saved?: string;
}

/**
 * What survives a worker restart: enough for the panel to keep showing the fill
 * and for the worker to read the engines' status again. Applicant data, the job
 * text, written values, summaries and learning events are never part of it.
 */
interface SavedFill {
  tabId: number;
  doc: string;
  host: string;
  page: string;
  posting: PostingHint;
  runId: string;
  startedAt: number;
  phase: FillPhase;
  message?: string;
  decisions: FrameDecision[];
  frames: number[];
  reported: boolean;
  view?: Fill["view"];
}

const fills = new Map<number, Fill>();
const loading = new Map<number, Promise<Fill | undefined>>();

const savedKey = (tabId: number) => `fill:${tabId}`;

function snapshot(f: Fill): SavedFill {
  const { company, title, url, location } = f.posting;
  return {
    tabId: f.tabId,
    doc: f.doc,
    host: f.host,
    page: f.page,
    posting: { company, title, url, location },
    runId: f.runId,
    startedAt: f.startedAt,
    phase: f.phase,
    message: f.message,
    decisions: f.decisions,
    frames: [...f.frames.keys()],
    reported: f.reported,
    view: f.view,
  };
}

async function persist(f: Fill): Promise<void> {
  const text = JSON.stringify(snapshot(f));
  if (text === f.saved) return;
  f.saved = text;
  await store.sessionPut(savedKey(f.tabId), JSON.parse(text));
}

function revive(s: SavedFill): Fill {
  return {
    ...s,
    frames: new Map(s.frames.map((frameId) => [frameId, { frameId }])),
    context: { runId: s.runId, host: s.host, before: new Map(), entry: new Map() },
    uploader: new Uploader(),
    finalizing: false,
    saved: JSON.stringify(s),
  };
}

/** The fill of a tab as held in memory, else read back from session storage after a worker restart. */
async function load(tabId: number): Promise<Fill | undefined> {
  const held = fills.get(tabId);
  if (held) return held;
  let pending = loading.get(tabId);
  if (!pending) {
    pending = store.sessionGet<SavedFill>(savedKey(tabId)).then((saved) => {
      loading.delete(tabId);
      if (!saved) return undefined;
      const f = fills.get(tabId) ?? revive(saved);
      fills.set(tabId, f);
      return f;
    });
    loading.set(tabId, pending);
  }
  return pending;
}

const PAGE_CHANGED = "The page changed, so the fill ended.";

/**
 * Whether the tab's top frame still holds the document the fill was started on.
 * The panel stamps its document with a token that lives in the page's isolated
 * world, so a replaced document has none, whatever the browser's own frame ids
 * say while a navigation commits.
 */
async function onPage(f: { tabId: number; doc: string }): Promise<boolean> {
  return (await run<string>(f.tabId, 0, docFn)) === f.doc;
}

/**
 * Ends a fill whose document is gone: its engines went with it. The record stays
 * so the panel of the new document can say why, until it is dismissed.
 */
async function endForPageChange(f: Fill): Promise<void> {
  if (f.reported || f.finalizing) return;
  f.reported = true;
  f.phase = "stopped";
  f.message = PAGE_CHANGED;
  await persist(f);
  await f.uploader.flush();
  await endRun(f.runId, "cancelled");
}

/** The fill of a tab, ended first when the document it was started on is no longer the tab's page. */
async function fillOf(tabId: number): Promise<Fill | undefined> {
  const f = await load(tabId);
  if (f && !f.reported && !f.finalizing && !(await onPage(f))) await endForPageChange(f);
  return f;
}

/** A new top-frame document: a finished result is dropped, a running fill ends. */
async function pageChanged(tabId: number): Promise<void> {
  const f = await load(tabId);
  if (!f || (await onPage(f))) return;
  if (f.reported) await forget(tabId);
  else await endForPageChange(f);
}

async function forget(tabId: number): Promise<void> {
  const f = fills.get(tabId);
  fills.delete(tabId);
  await f?.uploader.flush();
  if (f && !f.reported) void endRun(f.runId, "cancelled");
  if (f || (await store.sessionGet(savedKey(tabId))) !== undefined) await store.sessionRemove(savedKey(tabId));
}

// These run inside the page's isolated world, serialized by the browser, so
// each one is self-contained and closes over nothing.
type EngineGlobal = { __applysmithEngine?: Record<string, (...a: unknown[]) => unknown> };
// The top frame's start names the panel's document token and refuses in the page itself when
// the token is not that document's: nothing is written into a page that replaced the one the
// person pressed Start on.
const startFn = (args: StartArgs, doc?: string) => {
  const g = globalThis as unknown as EngineGlobal & { __applysmithDoc?: string };
  if (doc !== undefined && g.__applysmithDoc !== doc) return { started: false, reason: "page-changed" };
  return g.__applysmithEngine?.start?.(args) ?? null;
};
const docFn = () => (globalThis as unknown as { __applysmithDoc?: string }).__applysmithDoc ?? null;
const statusFn = () => (globalThis as unknown as EngineGlobal).__applysmithEngine?.status?.() ?? null;
const probeFn = () => (globalThis as unknown as EngineGlobal).__applysmithEngine?.probe?.() ?? null;
// The light probe is a separate tiny script: a frame is counted without the engine in it.
export const lightProbeFn = () => (globalThis as unknown as { __applysmithProbe?: () => unknown }).__applysmithProbe?.() ?? null;
const logFn = () => (globalThis as unknown as EngineGlobal).__applysmithEngine?.log?.() ?? [];
const frameReportFn = (decisions: FrameDecision[]) => (globalThis as unknown as EngineGlobal).__applysmithEngine?.frameReport?.(decisions) ?? [];
const controlFn = (op: "pause" | "resume" | "stop", runId: string) => {
  const e = (globalThis as unknown as EngineGlobal).__applysmithEngine;
  if (!e) return false;
  if (op === "stop") return e.stop?.(runId, "cancelled") ?? false;
  e[op]?.();
  return true;
};

export async function run<T>(tabId: number, frameId: number, func: (...a: never[]) => unknown, args: unknown[] = []): Promise<T | undefined> {
  try {
    const injection = { target: { tabId, frameIds: [frameId] }, func, args } as chrome.scripting.ScriptInjection<unknown[], unknown>;
    const [res] = await chrome.scripting.executeScript(injection);
    return (res?.result ?? undefined) as T | undefined;
  } catch {
    return undefined;
  }
}

export async function injectFiles(tabId: number, frameId: number, files: string[]): Promise<boolean> {
  try {
    await chrome.scripting.executeScript({ target: { tabId, frameIds: [frameId] }, files });
    return true;
  } catch {
    return false;
  }
}

const injectEngine = (tabId: number, frameId: number) => injectFiles(tabId, frameId, ["engine.js", "bridge.js"]);

/**
 * The engine's ladder for every frame of the tab. A registry frame gets the
 * engine; a deny-listed one is never touched; an unknown one is counted by a
 * small side-effect-free probe script and gets the engine only when it holds a
 * form. The top frame always runs the engine, which decides for itself whether
 * the page is an application.
 */
export async function admitFrames(tabId: number): Promise<{ decisions: FrameDecision[]; admitted: number[] }> {
  const all = (await chrome.webNavigation.getAllFrames({ tabId })) ?? [];
  const frames = all.filter((f) => f.documentLifecycle !== "prerender").slice(0, MAX_FRAMES);
  const decisions: FrameDecision[] = [];
  const admitted: number[] = [];
  for (const f of frames) {
    const id = String(f.frameId);
    const top = f.frameId === 0;
    const fixed = top ? null : staticDecision(id, f.url);
    if (fixed && fixed.outcome !== "admitted-registry") {
      decisions.push(fixed);
      continue;
    }
    if (f.errorOccurred) {
      decisions.push(failedDecision(id, f.url, "failed-probe", "the frame did not load"));
      continue;
    }
    if (fixed || top) {
      if (!(await injectEngine(tabId, f.frameId))) {
        decisions.push(failedDecision(id, f.url, "failed-inject", "the browser refused the injection"));
        continue;
      }
      if (fixed) {
        decisions.push(fixed);
        admitted.push(f.frameId);
        continue;
      }
      const probe = await run<{ controls: number; required: number }>(tabId, f.frameId, probeFn);
      if (!probe) {
        decisions.push(failedDecision(id, f.url, "failed-no-listener", "the engine did not answer"));
        continue;
      }
      decisions.push({ ...probeDecision(id, f.url, probe), outcome: "admitted-probe" });
      admitted.push(f.frameId);
      continue;
    }
    // An unknown frame: count its controls first, inject the engine only if it qualifies.
    if (!(await injectFiles(tabId, f.frameId, ["probe.js"]))) {
      decisions.push(failedDecision(id, f.url, "failed-probe", "the browser refused the probe"));
      continue;
    }
    const probe = await run<{ controls: number; required: number }>(tabId, f.frameId, lightProbeFn);
    if (!probe) {
      decisions.push(failedDecision(id, f.url, "failed-probe", "the probe did not answer"));
      continue;
    }
    const d = probeDecision(id, f.url, probe);
    if (d.outcome === "admitted-probe" && !(await injectEngine(tabId, f.frameId))) {
      decisions.push(failedDecision(id, f.url, "failed-inject", "the browser refused the injection"));
      continue;
    }
    decisions.push(d);
    if (d.outcome === "admitted-probe") admitted.push(f.frameId);
  }
  return { decisions, admitted };
}

function tabOf(sender: chrome.runtime.MessageSender): number {
  const id = sender.tab?.id;
  if (id === undefined) throw new ApiError("bad_request", "That needs a page.");
  return id;
}

const reply = <T>(result: T): string => JSON.stringify({ ok: true, result } satisfies BindingReply<T>);
const refuse = (error: string): string => JSON.stringify({ ok: false, error } satisfies BindingReply<never>);

async function fillFor(sender: chrome.runtime.MessageSender, runId: string): Promise<Fill | undefined> {
  const f = sender.tab?.id === undefined ? undefined : await fillOf(sender.tab.id);
  return f && f.runId === runId ? f : undefined;
}

/** The engine's question shape to the matcher's, and its answer back. */
export function toMatchRequest(req: MatchRequest): AnswerMatchRequest {
  const body: AnswerMatchRequest = {
    host: req.host,
    runId: req.runId,
    posting: {
      company: req.posting.company,
      title: req.posting.title,
      description: req.posting.jdText,
      location: req.posting.location,
      url: req.posting.url,
    },
    questions: req.questions.map((q) => ({
      selector: q.selector,
      text: q.text,
      section: q.section,
      kind: q.kind,
      inputType: q.inputType,
      options: q.options.map((label) => ({ value: label, label })),
      required: q.required,
      maxLength: q.maxLength,
    })),
  };
  if (req.coverLetterAttached) body.coverLetterAttached = true;
  if (req.resumeAttached) body.resumeAttached = true;
  return body;
}

export function toResolutions(res: AnswerMatchResult): Resolution[] {
  return res.results.map((r) => ({
    selector: r.selector,
    outcome: r.outcome as Resolution["outcome"],
    value: r.answer,
    optionLabel: r.chosenLabel,
    optionLabels: r.chosenLabels,
    reason: r.reason,
    terminal: r.terminal,
    clientMayOverwrite: r.clientMayOverwrite,
    ownAnswer: r.ownAnswer,
  }));
}

async function serveMatch(payload: string, sender: chrome.runtime.MessageSender): Promise<string> {
  const req = JSON.parse(payload) as MatchRequest;
  const fill = await fillFor(sender, req.runId);
  if (!fill) return refuse("no fill is running for this run");
  const out = await api.call<AnswerMatchResult>("/api/answer-bank/match", { method: "POST", body: toMatchRequest(req) });
  // An edit names only an entry that answered its control: an abstain can carry an entry id while the profile pass writes the value.
  for (const r of out.results) if (r.entryId && r.outcome !== "abstained") fill.context.entry.set(r.selector, r.entryId);
  return reply<MatchResponse>({ results: toResolutions(out) });
}

function summaryOf(f: Fill): { rows: (Row & { frame: number })[]; summaries: Summary[] } {
  const summaries: Summary[] = [];
  const rows: (Row & { frame: number })[] = [];
  for (const fr of f.frames.values()) {
    const s = fr.notice?.summary ?? (fr.status && "summary" in fr.status ? (fr.status.summary as Summary) : undefined);
    if (!s) continue;
    summaries.push(s);
    for (const r of s.rows) rows.push({ ...r, frame: fr.frameId });
  }
  return { rows, summaries };
}

const TERMINAL = new Set(["done", "error", "stopped"]);

function phaseOf(f: Fill): FillPhase {
  const states = [...f.frames.values()].map((fr) => fr.notice?.state ?? fr.status?.state ?? "running");
  if (states.length === 0) return f.phase;
  if (states.some((s) => s === "running")) return "running";
  if (states.some((s) => s === "paused")) return "paused";
  if (states.every((s) => s === "error")) return "error";
  return states.some((s) => s === "stopped") ? "stopped" : "done";
}

/** Tells the server the fill is over; one that never hears it ends the run after an idle wait. */
async function endRun(runId: string, status: "succeeded" | "cancelled"): Promise<void> {
  try {
    await api.call(`/api/autofill/runs/${encodeURIComponent(runId)}/end`, { method: "POST", body: { status } satisfies FillRunEnd });
    await chrome.storage.local.remove(`pending-fill:${runId}`);
  } catch {
    /* the server ends an idle run itself */
  }
}

async function finalize(f: Fill, phase: FillPhase): Promise<void> {
  const { rows, summaries } = summaryOf(f);
  for (const r of rows) if (r.value && r.outcome === "filled") f.context.before.set(r.selector, r.value);
  await reportSystemWrites(f.runId, rows);
  await endRun(f.runId, phase === "done" ? "succeeded" : "cancelled");
  f.reported = true;
  const counts = { filled: 0, prefilled: 0, review: 0, abstained: 0, failed: 0, skipped: 0, requiredUnfilled: 0 };
  for (const s of summaries) for (const k of Object.keys(counts) as (keyof typeof counts)[]) counts[k] += s.counts[k];
  let stall = false;
  for (const fr of f.frames.values()) {
    const lines = await run<string[]>(f.tabId, fr.frameId, logFn);
    if (lines?.some((l) => l.includes("watchdog") && l.includes("stall"))) stall = true;
  }
  // The top frame's engine logs the walk over the sub-frames and turns each one it could not enter into an event.
  const sub = f.decisions.filter((d) => d.frameId !== "0");
  const reported = sub.length ? await run<EngineEvent[]>(f.tabId, 0, frameReportFn, [sub]) : undefined;
  if (Array.isArray(reported)) for (const ev of reported) f.uploader.add(frameEvent(ev, f.runId));
  const first = [...f.frames.values()].find((fr) => fr.notice?.message)?.notice?.message;
  f.view = {
    counts,
    rows: rows.slice(0, MAX_ROWS).map(
      (r): RowView => ({
        label: r.label,
        lane: r.lane,
        outcome: r.outcome,
        // A terminal abstain is the matcher's: its own reason says why.
        reason: (r.reason === "terminal-abstain" && r.matcherReason) || r.reason || r.matcherReason,
        required: r.required,
        review: r.review,
        ...(r.frame === 0 ? { selector: r.selector } : {}),
      }),
    ),
    drafts: summaries.reduce((n, s) => n + s.drafts.length, 0),
    stall: stall ? "The page slowed down while the form was filled. Check the fields below." : undefined,
    unreachable: f.decisions.filter((d) => d.outcome.startsWith("failed-")).length,
    notAnApplication: summaries.length > 0 && summaries.every((s) => s.notAnApplication),
    matchUnavailable: summaries.some((s) => s.matchUnavailable),
  };
  if (phase === "error") f.message = first ?? "The fill failed.";
}

/** Refreshes each frame's status; finalizes once every frame has ended. */
async function refresh(f: Fill): Promise<void> {
  if (f.reported || f.finalizing) return;
  for (const fr of f.frames.values()) {
    if (fr.notice) continue;
    const st = await run<StatusReport>(f.tabId, fr.frameId, statusFn);
    if (st) fr.status = st;
  }
  // An overlapping refresh may have begun the end while these reads were out: the fill ends once.
  if (f.reported || f.finalizing) return;
  const next = phaseOf(f);
  const ended = f.frames.size > 0 && [...f.frames.values()].every((fr) => TERMINAL.has(fr.notice?.state ?? fr.status?.state ?? ""));
  if (ended) {
    f.finalizing = true;
    try {
      await finalize(f, next);
    } finally {
      f.finalizing = false;
    }
    f.phase = next;
  } else f.phase = next;
  await persist(f);
}

function viewOf(f: Fill): FillView {
  return {
    phase: f.phase,
    runId: f.runId,
    message: f.message,
    elapsedMs: Date.now() - f.startedAt,
    frames: f.decisions.map((d) => ({ frameId: d.frameId, url: d.url, outcome: d.outcome, controls: d.controls, reason: d.reason })),
    ...f.view,
  };
}

async function start(tabId: number, doc: string, tabUrl: string, posting: PostingHint): Promise<FillView> {
  const existing = await fillOf(tabId);
  if (existing && !existing.reported && (existing.phase === "starting" || existing.phase === "running" || existing.phase === "paused")) {
    throw new ApiError("conflict", "A fill is already running on this page.", 409);
  }
  const host = new URL(tabUrl).hostname;
  const [applicant, settings] = await Promise.all([
    api.call<ApplicantSnapshot>("/api/autofill/applicant"),
    // The setting fails closed: a read that does not answer is the same as the setting being off.
    api.call<AnswerBankSettings>("/api/answer-bank/settings").catch(() => undefined),
  ]);
  if (!(await onPage({ tabId, doc }))) throw new ApiError("conflict", PAGE_CHANGED, 409);
  // Issued after the reads, so a failed read leaves no run to time out.
  const run_ = await api.call<RunAccepted>("/api/autofill/runs", {
    method: "POST",
    body: { host, url: reduceUrl(tabUrl) } satisfies FillRunRequest,
    idempotencyKey: `fill-${crypto.randomUUID()}`,
  });
  const fill: Fill = {
    tabId,
    doc,
    host,
    page: reduceUrl(tabUrl),
    posting,
    runId: run_.runId,
    startedAt: Date.now(),
    phase: "starting",
    decisions: [],
    frames: new Map(),
    context: { runId: run_.runId, host, before: new Map(), entry: new Map() },
    uploader: new Uploader(),
    reported: false,
    finalizing: false,
  };
  existing?.uploader.flush();
  fills.set(tabId, fill);
  try {
    await store.restrictToTrusted();
    await chrome.storage.local.set({ [`pending-fill:${fill.runId}`]: tabId });
    await persist(fill);
  } catch (e) {
    fill.phase = "error";
    fill.reported = true;
    await endRun(fill.runId, "cancelled");
    // A partial session write must not restore a running fill on retry.
    await store.sessionRemove(savedKey(tabId)).catch(() => {});
    if (fills.get(tabId) === fill) fills.delete(tabId);
    throw e;
  }
  // Nothing is injected into a page that already replaced the one the person pressed Start on.
  if (!(await onPage(fill))) {
    await endForPageChange(fill);
    return viewOf(fill);
  }
  const { decisions, admitted } = await admitFrames(tabId);
  fill.decisions = decisions;
  const args: StartArgs = {
    runId: fill.runId,
    applicant,
    posting: { ...posting, url: reduceUrl(posting.url ?? tabUrl) },
    acceptLegalDeclarations: settings?.autoAcceptLegalDeclarations === true,
    // The bridge reports the person's edits itself (engine.edit); the engine's own watch would report them twice.
    watchCorrections: false,
  };
  for (const frameId of admitted) {
    if (fill.reported) return viewOf(fill);
    // The top frame checks the token in the page; a sub-frame is checked against the top frame just before.
    if (frameId !== 0 && !(await onPage(fill))) break;
    const r = await run<StartReply>(tabId, frameId, startFn, frameId === 0 ? [args, doc] : [args]);
    if (r?.reason === "page-changed") break;
    if (r?.started) fill.frames.set(frameId, { frameId });
    else {
      const d = fill.decisions.find((x) => x.frameId === String(frameId));
      if (d) Object.assign(d, failedDecision(d.frameId, d.url, "failed-no-listener", r?.reason ?? "the engine did not start"));
    }
  }
  // A page replaced while the engines were starting: stop the ones that did, then end the fill.
  if (!(await onPage(fill))) {
    for (const fr of fill.frames.values()) await run(tabId, fr.frameId, controlFn, ["stop", fill.runId]);
    await endForPageChange(fill);
    return viewOf(fill);
  }
  if (fill.reported) return viewOf(fill);
  if (fill.frames.size === 0) {
    fill.phase = "error";
    fill.message = "No fillable form was found on this page.";
    fill.reported = true;
    void endRun(fill.runId, "cancelled");
  } else fill.phase = "running";
  await persist(fill);
  return viewOf(fill);
}

async function control(tabId: number, op: "pause" | "resume" | "stop"): Promise<FillView> {
  const f = await fillOf(tabId);
  if (!f) throw new ApiError("not_found", "No fill is running on this page.", 404);
  for (const fr of f.frames.values()) await run(tabId, fr.frameId, controlFn, [op, f.runId]);
  await refresh(f);
  return viewOf(f);
}

// Local recovery holds no applicant, posting, host or form value. Session state
// survives worker suspension; its absence identifies an invalidated browser run.
async function recoverPendingFills(): Promise<void> {
  await store.restrictToTrusted();
  const records = await chrome.storage.local.get();
  for (const [key, tabId] of Object.entries(records)) {
    if (!key.startsWith("pending-fill:") || typeof tabId !== "number") continue;
    const runId = key.slice("pending-fill:".length);
    const saved = await store.sessionGet<SavedFill>(savedKey(tabId));
    if (saved?.runId === runId && !saved.reported) continue;
    await endRun(runId, saved?.runId === runId && saved.phase === "done" ? "succeeded" : "cancelled");
  }
}

export function registerFill(): void {
  let recovered: Promise<void> | undefined;
  const recover = (): Promise<void> => recovered ??= recoverPendingFills().catch((e) => {
    recovered = undefined;
    throw e;
  });
  // Observe eager failure; the next Start retries it instead of caching a
  // rejected promise for the worker's whole lifetime.
  void recover().catch(() => {});
  on("fill.start", async (m, s) => {
    await recover();
    if (s.frameId !== 0 || !s.tab?.url) throw new ApiError("forbidden", "A fill starts from the panel of the page.", 403);
    const tabId = tabOf(s);
    // The job in precedence order: the pin, this page or the anchored posting, the last fill.
    const job = await contextFor(tabId, readingOf(m.reading, s.tab.url));
    const view = await start(tabId, m.doc, s.tab.url, postingOf(job));
    await rememberFill(tabId, job);
    return view;
  });
  on("fill.pause", (_m, s) => control(tabOf(s), "pause"));
  on("fill.resume", (_m, s) => control(tabOf(s), "resume"));
  on("fill.stop", (_m, s) => control(tabOf(s), "stop"));
  // Dismissing a finished result drops its saved record; a running fill cannot be dismissed.
  on("fill.dismiss", async (_m, s) => {
    const tabId = tabOf(s);
    const f = await fillOf(tabId);
    if (f && !f.reported) throw new ApiError("conflict", "The fill is still running. Stop it first.", 409);
    await forget(tabId);
  });
  on("fill.status", async (_m, s) => {
    const f = await fillOf(tabOf(s));
    if (!f) return { phase: "idle", frames: [] } satisfies FillView;
    await refresh(f);
    return viewOf(f);
  });
  on("engine.match", async (m, s) => {
    try {
      return await serveMatch(m.payload, s);
    } catch (e) {
      return refuse(e instanceof ApiError && e.code === UNAUTHENTICATED ? "the extension is not paired" : "the matcher is unavailable");
    }
  });
  on("engine.resume", async (m, s) => {
    const req = JSON.parse(m.payload) as ResumeRequest;
    const f = await fillFor(s, req.runId);
    if (!f) return refuse("no fill is running for this run");
    if (req.document !== "resume") return reply(null);
    try {
      return reply(await resumes.payloadFor(f.tabId, f.page, f.posting));
    } catch (e) {
      return refuse(e instanceof ApiError && e.code === UNAUTHENTICATED ? "the extension is not paired" : "the résumé is unavailable");
    }
  });
  on("engine.terminal", async (m, s) => {
    const n = JSON.parse(m.payload) as TerminalNotice;
    const f = await fillFor(s, n.runId);
    const fr = f && s.frameId !== undefined ? f.frames.get(s.frameId) : undefined;
    if (fr) fr.notice = n;
    if (f) await refresh(f);
    return reply(null);
  });
  on("engine.edit", async (m, s) => {
    const f = s.tab?.id === undefined ? undefined : await fillOf(s.tab.id);
    if (!f?.reported) return;
    const ev = manualFill(m.edit satisfies UserEdit, f.context);
    if (ev) f.uploader.add(ev);
  });
  // A new document in the top frame ends the fill: its engines are gone.
  chrome.webNavigation.onCommitted.addListener((d) => {
    if (d.frameId === 0) void pageChanged(d.tabId);
  });
  chrome.tabs.onRemoved.addListener((tabId) => void forget(tabId));
}

// The job each tab is about: the person's pin, the anchor a posting page left
// for the application form that follows it, and the context the last fill used.
// The order is the engine's (`resolveJobContext`); this module only keeps the
// three per tab in session storage. A tab opened from another (an Apply button
// that opens the form in a new tab) starts with its opener's anchor.
/** An anchor older than this is no longer the job the tab is on. */
const ANCHOR_TTL_MS = 14 * 24 * 60 * 60 * 1000;

interface TabJob {
  pin?: JobContext;
  anchor?: JobContext & { at: number };
  last?: JobContext;
  /** A form-only page whose board the server could not read: it is not asked again. */
  boardMiss?: string;
}

const key = (tabId: number) => `job:${tabId}`;

async function read(tabId: number): Promise<TabJob> {
  return (await store.sessionGet<TabJob>(key(tabId))) ?? {};
}

async function write(tabId: number, t: TabJob): Promise<void> {
  await store.sessionPut(key(tabId), t);
}

/** A reading from a message: its shape is checked, never trusted. */
export function readingOf(raw: unknown, url: string): PageReading {
  const r = (raw && typeof raw === "object" ? raw : {}) as Partial<PageReading>;
  const str = (v: unknown) => (typeof v === "string" && v.trim() ? v : undefined);
  const source = r.jdSource === "structured" || r.jdSource === "container" || r.jdSource === "rendered" ? r.jdSource : undefined;
  return {
    url: str(r.url) ?? url,
    title: str(r.title),
    company: str(r.company),
    role: str(r.role),
    location: str(r.location),
    jdText: typeof r.jdText === "string" ? r.jdText.slice(0, 200_000) : "",
    jdSource: source,
    posting: r.posting === true,
    form: r.form === true,
  };
}

/** A Lever /apply or Ashby /application page: it holds only the form, and the posting is on the board. */
export function formOnlyBoardPage(url: string): boolean {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return false;
  }
  const segs = u.pathname.split("/").filter(Boolean);
  if (u.protocol !== "https:" || segs.length !== 3) return false;
  return (/^jobs(\.eu)?\.lever\.co$/.test(u.hostname) && segs[2] === "apply") || (/^jobs(\.eu)?\.ashbyhq\.com$/.test(u.hostname) && segs[2] === "application");
}

/**
 * The posting of a form-only board page, read once by the server from the
 * board and kept as the tab's anchor. A board the server could not read is
 * remembered; an unreachable or unpaired server is not.
 */
async function boardAnchor(t: TabJob, page: PageReading, now: number): Promise<TabJob["anchor"]> {
  const url = page.url.split(/[?#]/)[0];
  if (!page.form || t.boardMiss === url || !formOnlyBoardPage(url)) return undefined;
  try {
    const b = await api.call<PostingRead>("/api/autofill/posting", { query: { url } });
    if (!b.text.trim()) throw new ApiError("unavailable", "The board holds no text for the posting.");
    const title = b.title || page.role || page.title;
    const location = b.location || page.location;
    return { url, jdText: b.text.trim(), source: "board", at: now, ...(page.company ? { company: page.company } : {}), ...(title ? { title } : {}), ...(location ? { location } : {}) };
  } catch (e) {
    if (e instanceof ApiError && (e.code === "unavailable" || e.code === "validation_failed")) t.boardMiss = url;
    return undefined;
  }
}

/** The tab's job for this reading; a posting page, or a form-only page's board, becomes the tab's anchor. */
async function contextFor(tabId: number, page: PageReading, now = Date.now()): Promise<JobContext> {
  const t = await read(tabId);
  const fresh = anchorFrom(page);
  if (fresh) {
    t.anchor = { ...fresh, at: now };
    await write(tabId, t);
  }
  const anchor = t.anchor && now - t.anchor.at <= ANCHOR_TTL_MS ? t.anchor : undefined;
  const c = resolveJobContext({ pinned: t.pin, page, anchor, lastAutofill: t.last });
  if (c.source !== "none" && c.source !== "autofill") return c;
  const board = await boardAnchor(t, page, now);
  if (board) t.anchor = board;
  if (board || t.boardMiss) await write(tabId, t);
  return board ? resolveJobContext({ pinned: t.pin, page, anchor: board, lastAutofill: t.last }) : c;
}

/** The context a fill started with, kept as the tab's fallback. */
export async function rememberFill(tabId: number, c: JobContext): Promise<void> {
  if (!c.jdText?.trim()) return;
  const t = await read(tabId);
  const { source: _source, ...rest } = c;
  t.last = { ...rest, source: "autofill" };
  await write(tabId, t);
}

export function registerJobContext(): void {
  on("context.resolve", async (m, s) => {
    if (s.tab?.id === undefined) throw new ApiError("bad_request", "That needs a page.");
    return contextFor(s.tab.id, readingOf(m.reading, s.tab.url ?? ""));
  });
  // Pinning keeps the job the panel shows now; unpinning lets the page decide again.
  on("context.pin", async (m, s) => {
    const tabId = s.tab?.id;
    if (tabId === undefined) throw new ApiError("bad_request", "That needs a page.");
    const t = await read(tabId);
    delete t.pin;
    await write(tabId, t);
    const c = await contextFor(tabId, readingOf(m.reading, s.tab?.url ?? ""));
    if (!m.pin) return c;
    if (c.source === "none") throw new ApiError("validation_failed", "There is no job on this page to pin.", 422);
    const { source: _source, ...rest } = c;
    await write(tabId, { ...(await read(tabId)), pin: { ...rest, source: "pinned" } });
    return { ...rest, source: "pinned" } satisfies JobContext;
  });
  chrome.tabs.onCreated?.addListener((tab) => {
    if (tab.id === undefined || tab.openerTabId === undefined) return;
    const id = tab.id;
    void read(tab.openerTabId).then((opener) => (opener.anchor ? write(id, { anchor: opener.anchor }) : undefined));
  });
  chrome.tabs.onRemoved?.addListener((tabId) => void store.sessionRemove(key(tabId)));
}
