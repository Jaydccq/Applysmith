// The Analyze tab's side of the worker: look a posting up, start an
// evaluation, follow it on the run stream and add the posting to the tracker.
import type { ApplicationLookup, Candidacy, EvaluationRequest, EvaluationView as ServerEvaluation, ReportDetail, RunAccepted, RunFinal, RunPhase } from "../../../web/src/wire";
import { ApiError } from "./api";
import { on } from "./router";
import { api } from "./state";
import type { Capture, EvaluationView, LookupView, ReportView } from "../shared/messages";

const MAX_TEXT = 200_000;
/** Statuses at which the tracker already holds an application. */
export const APPLIED_OR_LATER = new Set(["applied", "responded", "interview", "offer", "rejected"]);

const toReport = (r: ReportDetail["report"]): ReportView => ({
  number: r.number,
  company: r.company,
  role: r.role,
  score: r.score,
  recommendation: r.recommendation,
});

async function reportOf(number: number): Promise<ReportView> {
  return toReport((await api.call<ReportDetail>(`/api/reports/${number}`)).report);
}

/** The tracker's answer for a page, with the report it points at when there is one. */
export async function lookup(url: string, withReport = true): Promise<LookupView> {
  const res = await api.call<ApplicationLookup>("/api/applications/lookup", { query: { url } });
  const submission = res.submission ? { submission: res.submission } : {};
  if (!res.found || !res.entry) return { found: false, identityKey: res.identityKey, ...submission };
  const e: Candidacy = res.entry;
  const view: LookupView = {
    found: true,
    identityKey: res.identityKey,
    status: e.status,
    appliedAt: e.appliedAt || res.history?.find((h) => h.status === "applied")?.at.toString(),
    candidacyId: e.id,
    ...(e.reportNumber ? { reportNumber: e.reportNumber } : {}),
    ...submission,
  };
  if (withReport && e.reportNumber) {
    try {
      view.report = await reportOf(e.reportNumber);
    } catch (err) {
      if (!(err instanceof ApiError) || err.code !== "not_found") throw err;
    }
  }
  return view;
}

const MAX_WATCHES = 20;
const SETTLED = new Set(["queued", "running", "cancelling"]);

interface Watch {
  view: EvaluationView;
  abort: AbortController;
}

const watches = new Map<string, Watch>();

async function finish(w: Watch, final: Pick<RunFinal, "outcome" | "reason" | "reportNumber" | "review"> & { explanation?: string }): Promise<void> {
  w.view.outcome = final.outcome;
  w.view.reason = final.reason;
  w.view.review = final.review;
  w.view.explanation = final.explanation;
  if (final.reportNumber) {
    try {
      w.view.report = await reportOf(final.reportNumber);
    } catch {
      /* the score is then simply not shown; the report number still is */
    }
  }
  // Last, so a status read never sees an ended run without its report.
  w.view.done = true;
}

function follow(runId: string, w: Watch): void {
  void api
    .stream(
      `/api/runs/${runId}/events`,
      (name, data) => {
        if (name === "phase") w.view.phase = (data as RunPhase).phase;
        if (name === "final") {
          void finish(w, data as RunFinal);
          return true;
        }
        return false;
      },
      w.abort.signal,
    )
    .catch(() => {
      // The stream gave up after its one reconnect: the next status call asks the run itself.
      watches.delete(runId);
    });
}

/** The current state of an evaluation; after a worker restart the first call re-attaches. */
export async function status(runId: string): Promise<EvaluationView> {
  const known = watches.get(runId);
  if (known) {
    // A finished run is handed over once and forgotten; asking again reads the run itself.
    if (known.view.done) watches.delete(runId);
    return { ...known.view };
  }
  const s = await api.call<ServerEvaluation>(`/api/evaluations/${runId}`);
  const w: Watch = { view: { runId, phase: s.phase ?? s.status, done: !SETTLED.has(s.status) }, abort: new AbortController() };
  if (w.view.done) {
    await finish(w, { outcome: s.outcome, reason: s.reason, reportNumber: s.reportNumber, review: s.review, explanation: s.explanation });
    return { ...w.view };
  }
  watches.set(runId, w);
  follow(runId, w);
  return { ...w.view };
}

export async function start(c: Capture, force: boolean): Promise<{ runId: string }> {
  const body: EvaluationRequest = { url: c.url, text: c.text.slice(0, MAX_TEXT), title: c.role || c.title, source: "extension" };
  if (c.company) body.company = c.company;
  if (c.location) body.location = c.location;
  if (force) body.force = true;
  const accepted = await api.call<RunAccepted>("/api/evaluations", { method: "POST", body, idempotencyKey: `ext_${crypto.randomUUID()}` });
  const w: Watch = { view: { runId: accepted.runId, phase: "queued", done: false }, abort: new AbortController() };
  for (const old of [...watches.keys()].slice(0, Math.max(0, watches.size - MAX_WATCHES + 1))) {
    watches.get(old)?.abort.abort();
    watches.delete(old);
  }
  watches.set(accepted.runId, w);
  follow(accepted.runId, w);
  return accepted;
}

/** Adds the posting to the tracker; one already there (every evaluation's report puts it there) counts as tracked. */
export async function track(c: Capture): Promise<LookupView> {
  try {
    const row = await api.call<Candidacy>("/api/candidacies", {
      method: "POST",
      body: { company: c.company || hostName(c.url), role: c.role || c.title, url: c.url, location: c.location, source: "extension" },
    });
    return { found: true, status: row.status, candidacyId: row.id };
  } catch (e) {
    if (e instanceof ApiError && e.reason === "duplicate_candidacy") return { found: true };
    throw e;
  }
}

function hostName(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return "Unknown";
  }
}

export function registerAnalyze(): void {
  on("analyze.lookup", (m) => lookup(m.url));
  on("analyze.start", (m) => start(m.capture, m.force === true));
  on("analyze.status", (m) => status(m.runId));
  on("analyze.track", (m) => track(m.capture));
}
