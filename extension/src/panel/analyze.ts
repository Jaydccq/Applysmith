// The Analyze tab: read this page, show an evaluation the tracker already has,
// or evaluate the page as a job posting and follow the phases to a score.
import { h, redraw } from "../shared/dom";
import { capturePage, pageJob } from "../shared/capture";
import type { Capture, Connection, EvaluationView, LookupView, ReportView, StateView } from "../shared/messages";
import { failureOf, send } from "../shared/rpc";
import { notConnected } from "./shell";

const POLL_MS = 1000;

/** The phases an evaluation passes, in order, as the server names them. */
export const PHASES: [string, string][] = [
  ["queued", "Waiting"],
  ["reading", "Reading the posting"],
  ["gating", "Checking your filters"],
  ["screening", "Screening"],
  ["evaluating", "Evaluating"],
  ["writing", "Writing the report"],
];

type Stage = "idle" | "capturing" | "captured" | "liveness" | "awaiting-expiry" | "starting" | "progress" | "scored" | "actioned" | "error";

export interface AnalyzeTab {
  setConnection(c: Connection, checked?: boolean): void;
  dispose(): void;
}

export interface AnalyzeOptions {
  doc?: Document;
  pollMs?: number;
}

export function mountAnalyze(el: HTMLElement, opts: AnalyzeOptions = {}): AnalyzeTab {
  const doc = opts.doc ?? document;
  const pollMs = opts.pollMs ?? POLL_MS;
  let connection: Connection = "offline";
  let checked = false;
  let stage: Stage = "idle";
  let capture: Capture | undefined;
  let found: LookupView | undefined;
  let progress: EvaluationView | undefined;
  let report: ReportView | undefined;
  let error = "";
  let notice = "";
  let confirming: "" | "not-posting" | "re-evaluate" = "";
  let closedOk = false;
  let address = "";
  let timer: ReturnType<typeof setTimeout> | undefined;
  let epoch = 0;
  let tracking = false;

  const fail = (e: unknown) => {
    error = failureOf(e).message;
    stage = "error";
    draw();
  };

  async function begin(): Promise<void> {
    const mine = ++epoch;
    clearTimeout(timer);
    stage = "capturing";
    error = notice = "";
    found = report = progress = undefined;
    closedOk = false;
    draw();
    try {
      capture = await capturePage({ doc });
      // Read once more now that the page has rendered, so a posting leaves its anchor.
      void pageJob(doc);
      if (mine !== epoch) return;
      address = (await send<StateView>({ kind: "state" })).address;
      found = await send<LookupView>({ kind: "analyze.lookup", url: capture.url });
      if (mine !== epoch) return;
      if (found.found && found.report) {
        report = found.report;
        stage = "scored";
      } else {
        // The liveness check reads the page's own wording; a closed posting waits for the person.
        stage = "liveness";
        draw();
        await new Promise((r) => setTimeout(r, 0));
        if (mine !== epoch) return;
        stage = capture.closed ? "awaiting-expiry" : "captured";
      }
    } catch (e) {
      return fail(e);
    }
    draw();
  }

  /** A page that does not read as a posting needs one more yes before it is evaluated. */
  function requestEvaluate(): void {
    if (capture?.closed && !closedOk) {
      stage = "awaiting-expiry";
      draw();
    } else if (capture?.detection.label === "not_job_posting" && confirming !== "not-posting") {
      confirming = "not-posting";
      draw();
    } else void evaluate(false);
  }

  async function evaluate(force: boolean): Promise<void> {
    if (!capture) return;
    const mine = epoch;
    confirming = "";
    stage = "starting";
    draw();
    try {
      const { runId } = await send<{ runId: string }>({ kind: "analyze.start", capture, force });
      if (mine !== epoch) return;
      stage = "progress";
      progress = { runId, phase: "queued", done: false };
      draw();
      void poll(runId, mine);
    } catch (e) {
      if (mine === epoch) fail(e);
    }
  }

  async function poll(runId: string, mine: number): Promise<void> {
    try {
      progress = await send<EvaluationView>({ kind: "analyze.status", runId });
    } catch (e) {
      if (mine === epoch) fail(e);
      return;
    }
    if (mine !== epoch) return;
    if (!progress.done) {
      draw();
      timer = setTimeout(() => void poll(runId, mine), pollMs);
      return;
    }
    // A gated run writes a report too; the reason it was filtered out is shown with it.
    const gated = progress.outcome === "gated" ? `Filtered out before scoring${progress.reason ? `: ${progress.reason.replaceAll("_", " ")}` : ""}.` : "";
    if (progress.report) {
      notice = gated;
      report = progress.report;
      stage = "scored";
    } else if (progress.outcome === "failed") {
      error = progress.explanation || `The evaluation did not finish (${progress.reason ?? "unknown reason"}).`;
      stage = "error";
    } else {
      notice = gated || `The evaluation ended: ${progress.outcome ?? "no outcome"}.`;
      stage = "captured";
    }
    draw();
  }

  async function addToTracker(): Promise<void> {
    if (!capture || tracking) return;
    tracking = true;
    draw();
    try {
      const c = report ? { ...capture, company: report.company, role: report.role } : capture;
      await send<LookupView>({ kind: "analyze.track", capture: c });
      stage = "actioned";
      draw();
    } catch (e) {
      fail(e);
    } finally {
      tracking = false;
    }
  }

  const act = (label: string, run: () => void, disabled = false) => h("button", { class: "act", type: "button", disabled, onclick: run }, label);

  function stepper(): Node {
    const at = Math.max(0, PHASES.findIndex(([id]) => id === progress?.phase));
    return h(
      "ol",
      { class: "steps", "aria-label": "Evaluation progress" },
      ...PHASES.map(([, label], i) => h("li", { "aria-current": i === at ? "step" : false, class: i < at ? "ok" : i === at ? "" : "muted" }, label)),
    );
  }

  function scoreView(): Node[] {
    if (!report) return [];
    const score = report.score === undefined ? "no score" : `Score ${report.score}`;
    return [
      h("p", { role: "status" }, h("strong", {}, `${score}`), `, recommendation: ${report.recommendation}`),
      h("p", { class: "muted" }, `${report.role} at ${report.company}`),
      h("a", { href: `${address}/reports?id=${report.number}`, target: "_blank", rel: "noopener" }, `Open report ${report.number}`),
    ];
  }

  function draw(): void {
    el.dataset.state = stage;
    const body: Node[] = [];
    if (connection !== "connected") {
      body.push(notConnected(connection, checked));
    } else if (stage === "idle" || stage === "capturing") {
      body.push(h("p", { role: "status" }, "Reading this page."));
    } else if (stage === "error") {
      body.push(h("p", { class: "bad", role: "alert" }, error), act("Try again", () => void begin()));
    } else if (stage === "starting") {
      body.push(h("p", { role: "status" }, "Starting the evaluation."));
    } else if (stage === "progress") {
      body.push(stepper());
    } else if (stage === "scored" || stage === "actioned") {
      if (notice) body.push(h("p", { class: "bad", role: "status" }, notice));
      body.push(...scoreView());
      if (stage === "actioned") body.push(h("p", { class: "ok", role: "status" }, "The posting is in the tracker."));
      if (confirming === "re-evaluate") {
        body.push(h("p", {}, "Run the evaluation again? It replaces nothing but costs another run."), act("Re-evaluate", () => void evaluate(true)), act("Cancel", () => ((confirming = ""), draw())));
      } else {
        body.push(act("Re-evaluate", () => ((confirming = "re-evaluate"), draw())));
        if (stage === "scored" && !found?.found) body.push(act("Add to tracker", () => void addToTracker(), tracking));
      }
    } else if (capture) {
      const d = capture.detection;
      body.push(h("p", { class: "muted" }, `${capture.title || capture.url}`), h("p", {}, `This page looks like ${d.label === "job_posting" ? "a job posting" : d.label === "likely_job_posting" ? "a likely job posting" : "something other than a job posting"} (${Math.round(d.confidence * 100)}% sure${d.signals.length ? `: ${d.signals.join(", ")}` : ""}).`));
      if (notice) body.push(h("p", { class: "bad", role: "status" }, notice));
      if (stage === "liveness") body.push(h("p", { role: "status" }, "Checking whether the posting is still open."));
      else if (confirming === "not-posting") {
        body.push(h("p", {}, "This page does not look like a job posting. Evaluate it anyway?"), act("Evaluate anyway", () => void evaluate(false)), act("Cancel", () => ((confirming = ""), draw())));
      } else if (stage === "awaiting-expiry") {
        body.push(h("p", { class: "bad", role: "alert" }, `This posting looks closed: "${capture.closed}".`), act("Evaluate anyway", () => ((closedOk = true), requestEvaluate())), act("Cancel", () => ((stage = "captured"), draw())));
      } else {
        body.push(act(d.label === "not_job_posting" ? "Evaluate anyway" : "Evaluate", requestEvaluate));
      }
    }
    redraw(el, body);
  }

  draw();
  return {
    setConnection(c, now = false) {
      checked = now;
      const first = connection !== "connected" && c === "connected" && stage === "idle";
      connection = c;
      if (first) void begin();
      else draw();
    },
    dispose() {
      epoch++;
      clearTimeout(timer);
    },
  };
}
