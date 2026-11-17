// Clean-room: docs/provenance.md#CR-ENGINE
// The stages of a fill, in order. Each is timed in the diagnostic log and ends
// at its next cancellation point when the run is stopped.

import { consentSweep } from "./consent-sweep";
import { enableAshbyQueue } from "./ashby";
import { noEasyApplyDialog } from "./easyapply";
import { expandAllSections } from "./successfactors";
import { detectApplication, type Detection } from "./detect";
import { isEmbeddedSite, revealApplication } from "./reveal";
import { fire, type Win } from "./dom";
import { blurKind, releaseFocus, tap } from "./gestures";
import { refillStage, TailGuard } from "./refill";
import { Restorer, type UserEdits } from "./restore";
import { cascadeCompletion } from "./stage-cascade";
import type { Host } from "./host";
import type { Summary } from "./protocol";
import { QuietWatcher } from "./quiet";
import type { Run } from "./run";
import { readInfo, scanControls } from "./scan";
import { resolveSelector } from "./selector";
import { questionPass } from "./stage-questions";
import { applyLoop, profilePass } from "./stage-profile";
import { resumeStage, resumeUploadStep, type ResumeDeps } from "./stage-resume";
import { offersEntryAdd, prepareSections, removeDuplicateRows, RowSweeper, snapshotRows, wroteIn } from "./sections";
import { lateParseSweep, reconcileSrCards, snapshotSrCards } from "./srcards";
import { clearStaleProse } from "./stale";
import { buildSummary, unclaimedRows, unknownControlRows } from "./summary";

export interface PipelineDeps {
  host: Host;
  win: Win;
  attached: ResumeDeps["attached"];
  /** Values written in this run, by selector; shared with later answer() calls. */
  written: Map<string, string>;
  /** The user's own edits, tracked across the run and the restore pass that outlives it. */
  edits: UserEdits;
  /** Runs once the fill is done and before the summary is returned (the engine refreshes the saved copy's gaps here). */
  afterSummary?: () => Promise<void>;
  /** The restore pass is handed to the engine so a new run or a stop can end it. */
  onRestorer: (r: Restorer) => void;
}

/**
 * Touch each control the run wrote once, so the page's own validators run: a
 * text control is focused and blurred; a closed-listbox button trigger is
 * blurred after a settle. Radios, checkboxes, file inputs, empty controls,
 * input-backed comboboxes and date sub-inputs are left alone.
 */
async function blurPass(run: Run): Promise<void> {
  const done = new Set<string>();
  for (const row of run.rows.values()) {
    if (row.outcome !== "filled" || done.has(row.selector)) continue;
    if (row.kind !== "text" && row.kind !== "textarea" && row.kind !== "custom-listbox") continue;
    done.add(row.selector);
    await run.checkpoint();
    const el = resolveSelector(run.doc, row.selector) as HTMLInputElement | null;
    const how = el ? blurKind(row.kind, el) : null;
    if (!el || !how || run.committed.get(el) === el.value) continue;
    if (how === "button") {
      await tap(el, run.ctx());
      continue;
    }
    await releaseFocus(el, run.ctx());
    el.focus();
    fire(el, "focusin");
    await run.sleep(run.timing.tapMs);
    fire(el, "focusout");
    el.blur();
  }
}

export async function runPipeline(run: Run, d: PipelineDeps): Promise<Summary> {
  const quiet = new QuietWatcher(run.doc, d.win);
  const written = d.written;
  try {
    const detection = await run.stage("detect", async () => {
      // Only an Easy Apply dialog is the application on LinkedIn; the page around it is never opened or scanned.
      if (noEasyApplyDialog(run.doc)) {
        run.log.add("detect", "linkedin-no-easy-apply-dialog");
        return { application: false, reason: "no-form" } as Detection;
      }
      // A collapsed application shows no fields until its sections are opened.
      await expandAllSections(run.ctx());
      for (const info of scanControls(run.doc)) if (readInfo(info) === "") run.emptyAtStart.add(info.selector);
      let found: Detection = detectApplication(run.doc);
      // An experience step (Workday's) has no input of its own until an entry is added: an experience or
      // education entry to add makes it one.
      if (!found.application && found.reason === "no-form" && offersEntryAdd(run.doc)) {
        run.log.add("detect", "experience-step");
        found = { application: true, controls: 0 };
      }
      if (!found.application && found.reason === "no-form" && resumeUploadStep(run.doc)) {
        run.log.add("detect", "resume-step");
        found = { application: true, controls: 0 };
      }
      // An embedded system may open on an overview: its own control reveals the form.
      if (!found.application && found.reason === "no-form" && isEmbeddedSite(run.site)) {
        if (await revealApplication(run.ctx(), (event, data) => run.log.add("detect", event, data))) found = detectApplication(run.doc);
      }
      return found;
    });
    if (!detection.application) {
      return buildSummary(run, d.win, detection.reason === "blocked-by-dialog" ? { blockedByDialog: true } : { notAnApplication: true });
    }
    if (run.site === "ashby" && run.main && !(await enableAshbyQueue(run.main))) run.log.add("detect", "ashby-queue-unavailable");
    await run.stage("legal-early", () => (run.acceptLegalDeclarations ? consentSweep(run, written) : Promise.resolve()));
    const start = snapshotRows(run);
    snapshotSrCards(run);
    await run.stage("resume", () => resumeStage(run, { host: d.host, win: d.win, attached: d.attached, written }));
    await run.stage("sections", async () => {
      // SmartRecruiters keeps its entries as cards, which have their own reconcile.
      if (run.site === "smartrecruiters") await reconcileSrCards(run);
      else await prepareSections(run, start);
      clearStaleProse(run);
    });
    // One fill pass; the refill stage runs it again when the page swapped the block it wrote into.
    const fillPass = async () => {
      await run.stage("question-pass", () => questionPass(run, { host: d.host, written, quiet }));
      const plan = await run.stage("profile-pass", async () => profilePass(run));
      await run.stage("apply-loop", () => applyLoop(run, plan, { written, quiet }));
      await run.stage("cascade", () => cascadeCompletion(run, { written, quiet }));
    };
    await fillPass();
    await run.stage("refill", () => refillStage(run, { written, edits: d.edits, pass: fillPass }));
    await run.stage("legal-late", () => consentSweep(run, written));
    await run.stage("blur", () => blurPass(run));
    await run.stage("late-parse", () => lateParseSweep(run));
    await run.stage("duplicates", () => removeDuplicateRows(run));
    const tail = run.applicant
      ? new TailGuard({
          doc: run.doc,
          log: run.log,
          edits: d.edits,
          written,
          claimed: run.claimed,
          applicant: run.applicant,
          acceptLegalDeclarations: run.acceptLegalDeclarations,
          capMs: run.timing.tailCapMs,
        })
      : undefined;
    const restorer = new Restorer({
      tail,
      doc: run.doc,
      win: d.win,
      timing: run.timing,
      log: run.log,
      edits: d.edits,
      acceptLegalDeclarations: run.acceptLegalDeclarations,
      rows: () => Array.from(run.rows.values()),
      driver: { site: run.site, main: run.main },
      sweep: run.applicant ? new RowSweeper({ applicant: run.applicant, site: run.site, edits: d.edits, log: run.log, timing: run.timing, start, wrote: wroteIn(run.rows) }) : undefined,
    });
    d.onRestorer(restorer);
    await run.stage("restore", () => restorer.first());
    await run.stage("summary", async () => {
      await unclaimedRows(run);
      unknownControlRows(run);
    });
    const summary = buildSummary(run, d.win);
    await d.afterSummary?.();
    if (restorer.restored.size) summary.restored = Array.from(restorer.restored);
    // Later repairs are listed on the same summary; the watch never delays it.
    restorer.listen((selector) => {
      const list = summary.restored ?? (summary.restored = []);
      if (!list.includes(selector)) list.push(selector);
    });
    void restorer.watch();
    return summary;
  } finally {
    quiet.stop();
  }
}
