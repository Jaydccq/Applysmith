// The Autofill tab: start, pause, resume and stop a fill of this page, and the
// result. The worker runs the engine; this tab only asks and shows.
import { resolveSelector } from "../../../engine/src/selector";
import { h, redraw } from "../shared/dom";
import { postingFacts, readPosting } from "../shared/capture";
import { DOC_KEY, type Connection, type FillView, type JobContext, type PressDecision, type RowView } from "../shared/messages";
import { failureOf, send } from "../shared/rpc";
import { pendingCount } from "../widgets/drafts";
import { settleBudgetFor, startWalk, type Walk, type WalkState } from "../widgets/easyapply";
import { advanceThisFrame, embeddedFormPresent, hasProgressControl, nextStepState, type AdvanceResult } from "../widgets/nextstep";
import { createPicker } from "../widgets/picker";
import { submissionEntry, watchConfirmation, type ConfirmationWatch } from "../widgets/recorder";
import { notConnected, SHOWN } from "./shell";

const POLL_MS = 700;
/** How often the Next step button re-reads the page for a progress control. */
const NEXT_STEP_CHECK_MS = 1000;
const MAX_LISTED = 20;

const FRAME_OUTCOME: Record<string, string> = {
  "admitted-registry": "filled (known application system)",
  "admitted-probe": "filled (a form was found)",
  "skipped-zero-controls": "skipped (no form fields)",
  "skipped-denylist": "skipped (not an application frame)",
  "failed-probe": "could not be checked",
  "failed-inject": "could not be reached",
  "failed-no-listener": "did not answer",
};

/** Where the job the fill uses came from. */
const JOB_FROM: Record<JobContext["source"], string> = {
  pinned: "pinned by you",
  page: "this page",
  anchor: "the posting you opened before this form",
  board: "read from the job board",
  autofill: "the last fill in this tab",
  none: "",
};

const [NONE, OWN, BLANK, SELF, FIT] = ["nothing in your answer bank answers it", "the form asks for your own words", "left blank on purpose", "self-identification is yours to answer", "your answer does not fit this field"];
/** Why a field was left to the person, in words; the engine's and the matcher's reasons. */
const WHY: Record<string, string> = {
  "option-not-found": "none of its options matches your answer",
  "listbox-not-opened": "its menu did not open",
  "write-not-committed": "the page did not keep the value",
  "format-not-accepted": "the page refused the value's format",
  "control-detached": "the field changed while it was filled",
  "verify-mismatch": "the value read back differs",
  "ashby-save-lost": "the page lost the value",
  "unsafe-click": "it needs a click that is not safe to make",
  "needs-native-upload": "it needs a file you choose",
  "driver-unavailable": "this kind of field is not supported",
  "awaiting-review": "a draft waits for your review",
  "ask-me": "your answer bank says to ask you",
  timeout: "it took too long to fill",
  readonly: "the page does not let it be changed",
  "user-edited": "you changed it yourself, so your value stays",
  ambiguous: "more than one answer fits equally",
  "too-many-options": "it has too many options to choose from",
  unsupported: "nothing you have told it supports an answer",
  "no-match": NONE,
  "no-answer": NONE,
  "no-entry": NONE,
  "no-route": NONE,
  "no-lane-claimed": "nothing in your profile or answer bank answers it",
  "no-grounding": "nothing in your profile supports an answer",
  "low-confidence": "no answer was certain enough",
  "narrative-deferred": "it needs your own words",
  "ai-instruction": OWN,
  "destructive-option": "the only fitting answer would withdraw you",
  "input-mismatch": FIT,
  "response-mismatch": FIT,
  "model-abstain": "the model gave no answer",
  "upstream-error": "the model could not be reached",
  "budget-exhausted": "the run's model budget ran out",
  "legal-declaration-off": "a legal declaration: tick it yourself, or allow it in Settings, Runtime settings",
  "self-id-policy": SELF,
  "sensitive-category": SELF,
  "marketing-opt-in": "an optional opt-in, never ticked for you",
  "section-policy": BLANK,
  "context-policy": BLANK,
  "placeholder-empty": BLANK,
  "duplicate-link": "that link is already used in another field",
};
/** Reasons for a field the person does not have to fill, unless the page requires it; one they edited never needs them. */
const ON_PURPOSE = new Set(["section-policy", "context-policy", "placeholder-empty", "duplicate-link", "marketing-opt-in", "self-id-policy", "sensitive-category", "user-edited"]);

const why = (reason?: string) => (reason ? (WHY[reason] ?? reason.replaceAll("-", " ")) : "");

export interface AutofillTab {
  setConnection(c: Connection, checked?: boolean): void;
  dispose(): void;
}

const documentToken = (win: Window) => (win as unknown as Record<string, string>)[DOC_KEY] ?? "";

export function mountAutofill(el: HTMLElement, win: Window = window): AutofillTab {
  const picker = createPicker({
    doc: win.document,
    win,
    send: async (report) => {
      await send({ kind: "report.field", report });
    },
  });
  let walk: Walk | undefined;
  let walkState: WalkState | undefined;
  // The tab is drawn again on every change, so the section's open state is kept here.
  let walkOpen = false;
  let walkSubmit = false;
  let confirming: ConfirmationWatch | undefined;
  // A request that answers after dispose re-arms nothing.
  let disposed = false;
  let connection: Connection = "offline";
  let checked = false;
  let view: FillView = { phase: "idle", frames: [] };
  let error = "";
  let timer: ReturnType<typeof setTimeout> | undefined;
  let stepping = false;
  let steppingNote = "";
  let job: JobContext | undefined;

  /** The job the fill will use; pinning keeps it for this tab until it is unpinned. */
  const loadJob = async (pin?: boolean) => {
    try {
      const reading = readPosting(win.document);
      job = await send<JobContext>(pin === undefined ? { kind: "context.resolve", reading } : { kind: "context.pin", reading, pin });
    } catch (e) {
      if (pin !== undefined) error = failureOf(e).message;
    }
    draw();
  };

  function jobLine(): Node | false {
    if (!job?.source) return false;
    if (job.source === "none") return h("p", { class: "muted" }, "No job description was found for this page; answers use your bank alone.");
    const name = [job.title, job.company].filter(Boolean).join(" at ") || job.url;
    return h(
      "p",
      { class: "muted" },
      `Job: ${name} (${JOB_FROM[job.source]}). `,
      h("button", { class: "act", type: "button", onclick: () => void loadJob(job?.source !== "pinned") }, job.source === "pinned" ? "Unpin" : "Pin this job"),
    );
  }

  const call = async (kind: "fill.start" | "fill.pause" | "fill.resume" | "fill.stop" | "fill.status") => {
    try {
      view =
        kind === "fill.start"
          ? await send<FillView>({ kind, reading: readPosting(win.document), doc: documentToken(win) })
          : await send<FillView>({ kind } as { kind: "fill.status" });
      error = "";
    } catch (e) {
      const f = failureOf(e);
      error = f.code === "unauthenticated" ? "The extension is not paired. Open its toolbar popup to connect." : f.message;
    }
    draw();
    schedule();
  };

  const schedule = () => {
    clearTimeout(timer);
    if (disposed) return;
    if (view.phase === "starting" || view.phase === "running" || view.phase === "paused") timer = setTimeout(() => void call("fill.status"), POLL_MS);
  };

  const nextStepBtn = h("button", { class: "act", type: "button", onclick: () => void nextStep() }, "Next step");
  const nextStepNote = h("p", { class: "muted", role: "status" });
  const fillInFlight = () => stepping || view.phase === "starting" || view.phase === "running" || view.phase === "paused" || (walk !== undefined && walkState?.phase !== "ended");

  function refreshNextStep(): void {
    // The page is read only while this tab is shown in an open panel; showing the tab reads it at once.
    if (el.closest("[hidden]")) return;
    const state = nextStepState({ hasProgress: hasProgressControl(win.document), embedded: embeddedFormPresent(win.document), busy: fillInFlight() });
    nextStepBtn.hidden = !state.visible;
    nextStepBtn.disabled = state.disabled;
    nextStepBtn.title = state.title;
    nextStepNote.hidden = !state.visible || steppingNote === "";
    nextStepNote.textContent = steppingNote;
  }

  /** One press per click: the button is disabled before anything is awaited. */
  async function nextStep(): Promise<void> {
    if (nextStepBtn.disabled || stepping) return;
    stepping = true;
    steppingNote = "Pressing the next-step button...";
    refreshNextStep();
    let out: AdvanceResult = advanceThisFrame(win.document);
    if (!out.advanced && embeddedFormPresent(win.document)) {
      out = await send<AdvanceResult>({ kind: "step.advance" }).catch(() => out);
    }
    steppingNote = out.advanced ? `Pressed "${out.label ?? "Next"}".` : "No next-page control found here or in the embedded form.";
    refreshNextStep();
    await new Promise((r) => setTimeout(r, POLL_MS));
    stepping = false;
    refreshNextStep();
  }

  // Pause and Resume share a key, so focus stays on the one button that takes its place.
  const act = (label: string, kind: Parameters<typeof call>[0], disabled = false, primary = false) =>
    h("button", { class: primary ? "act primary" : "act", type: "button", disabled, "data-key": kind.replace("resume", "pause"), onclick: () => void call(kind) }, label);

  function draw(): void {
    const body: (Node | string | false)[] = [nextStepBtn, nextStepNote];
    // A walk runs its own fills, which this view does not show until a step ends: starting another would compete with it.
    const walking = walk !== undefined && walkState?.phase !== "ended";
    const ended = ["done", "stopped", "error"].includes(view.phase);
    if (connection !== "connected") {
      body.push(notConnected(connection, checked));
    } else if (view.phase === "idle") {
      body.push(h("p", { class: "muted" }, "Fills the fields of this page from your profile and answer bank. It never presses Submit."), act("Start", "fill.start", walking, true));
    } else if (ended) {
      if (view.message) body.push(h("p", view.phase === "error" ? { class: "bad", role: "alert" } : { role: "status" }, view.message));
      body.push(act("Fill again", "fill.start", walking, true), h("button", { class: "act", type: "button", disabled: walking, onclick: () => void dismiss() }, "Dismiss result"), ...result(view, win.document));
    } else {
      const secs = Math.round((view.elapsedMs ?? 0) / 1000);
      body.push(
        h("p", { role: "status" }, view.phase === "paused" ? `Paused after ${secs} s.` : `Filling the form, ${secs} s.`),
        view.phase === "paused" ? act("Resume", "fill.resume") : act("Pause", "fill.pause"),
        act("Stop", "fill.stop"),
      );
    }
    if (connection === "connected") body.push(jobLine());
    if (error) body.push(h("p", { class: "bad", role: "alert" }, error));
    if (connection === "connected" && (view.phase === "idle" || ended)) body.push(tools());
    // The tab is drawn again on every poll: a keyboard user's focus must survive it.
    redraw(el, body.filter((b): b is Node | string => Boolean(b)));
    refreshNextStep();
  }

  async function dismiss(): Promise<void> {
    try {
      await send({ kind: "fill.dismiss" });
      view = { phase: "idle", frames: [] };
      error = "";
    } catch (e) {
      error = failureOf(e).message;
    }
    draw();
  }

  /** Runs the engine on the current step and waits until it has ended. */
  async function fillStep(): Promise<{ ok: boolean; message?: string; filled?: number; drafts?: number }> {
    try {
      let v = await send<FillView>({ kind: "fill.start", reading: readPosting(win.document), doc: documentToken(win) });
      while (v.phase === "starting" || v.phase === "running" || v.phase === "paused") {
        await new Promise((r) => setTimeout(r, POLL_MS));
        v = await send<FillView>({ kind: "fill.status" });
      }
      view = v;
      return { ok: v.phase === "done", message: v.message, ...(v.counts ? { filled: v.counts.filled } : {}), ...(v.drafts ? { drafts: v.drafts } : {}) };
    } catch (e) {
      return { ok: false, message: failureOf(e).message };
    }
  }

  function tools(): Node {
    const walking = walk !== undefined && walkState?.phase !== "ended";
    const parts: (Node | false)[] = [
      h("button", { class: "act", type: "button", onclick: () => ((error = ""), picker.arm()) }, "Point at a field"),
      h("p", { class: "muted" }, "Click a field that autofill missed or got wrong. Nothing on the page changes."),
    ];
    const option = h("input", { type: "checkbox", id: "walk-submit" });
    option.checked = walkSubmit;
    option.addEventListener("change", () => (walkSubmit = option.checked));
    parts.push(
      h(
        "details",
        { open: walkOpen || walking, ontoggle: (e: Event) => (walkOpen = (e.target as HTMLDetailsElement).open) },
        h("summary", {}, "Step by step (Easy Apply and similar forms)"),
        h("p", { class: "muted" }, "Fills each step, then presses Continue, Next or Review. At most 8 steps."),
        h("label", {}, option, " Also press the final Submit control. Off unless you tick it, for this walk only."),
        walking
          ? h("span", {}, h("button", { class: "act", type: "button", onclick: () => walk?.pause() }, "Pause walk"), h("button", { class: "act", type: "button", onclick: () => walk?.resume() }, "Resume walk"), h("button", { class: "act", type: "button", onclick: () => walk?.stop() }, "Stop walk"))
          : h("button", { class: "act", type: "button", onclick: () => beginWalk() }, "Walk the steps"),
        walkState ? h("p", { role: "status" }, walkState.phase === "ended" ? (walkState.message ?? "") : `Step ${walkState.iteration + (walkState.phase === "filling" ? 1 : 0)}: ${walkState.phase}${walkState.label ? ` (${walkState.label})` : ""}`) : false,
      ),
    );
    return h("div", {}, ...parts.filter((p): p is Node => Boolean(p)));
  }

  function beginWalk(): void {
    walk = startWalk({
      doc: win.document,
      submit: walkSubmit,
      settleBudgetMs: settleBudgetFor(win.location.href),
      fill: fillStep,
      pendingDrafts: pendingCount,
      claim: () => {
        const facts = postingFacts(win.document);
        return send<PressDecision>({ kind: "press.claim", ...(facts.company ? { company: facts.company } : {}), ...(facts.role ? { role: facts.role } : {}) });
      },
      pressed: async (claimId) => {
        const entry = submissionEntry(win.document, win, Date.now());
        // The recorder ignores the walker's own click, so the press is watched for the page's confirmation here.
        confirming = watchConfirmation({ doc: win.document, win, record: (e) => send({ kind: "press.report", claimId, entry: e }).then(() => {}) }, entry);
        await send({ kind: "press.report", claimId, entry });
      },
      report: (st) => {
        walkState = st;
        draw();
      },
    });
    // The option applies to this walk only.
    walkSubmit = false;
  }

  draw();
  const watch = setInterval(refreshNextStep, NEXT_STEP_CHECK_MS);
  el.addEventListener(SHOWN, refreshNextStep);
  return {
    setConnection(c, now = false) {
      checked = now;
      const first = connection !== "connected" && c === "connected";
      connection = c;
      draw();
      // A fill that began before this panel was drawn, or before the worker restarted, shows up again.
      if (first) {
        void call("fill.status");
        void loadJob();
      }
    },
    dispose: () => {
      disposed = true;
      clearTimeout(timer);
      clearInterval(watch);
      picker.cancel();
      walk?.stop();
      confirming?.stop();
    },
  };
}

const highlighted = new WeakMap<HTMLElement, { outline: string; timer: ReturnType<typeof setTimeout> }>();

/** Brings a field the fill left to the person into view, puts the cursor in it and outlines it for a moment. */
export function reveal(doc: Document, selector: string): boolean {
  const el = resolveSelector(doc, selector) as HTMLElement | null;
  if (!el) return false;
  const still = doc.defaultView?.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  el.scrollIntoView({ block: "center", behavior: still ? "auto" : "smooth" });
  el.focus({ preventScroll: true });
  // A second press within the 2 s keeps the field's own outline and restarts the timer.
  const held = highlighted.get(el);
  if (held) clearTimeout(held.timer);
  const outline = held?.outline ?? el.style.outline;
  el.style.outline = "3px solid #1f5fbf";
  const timer = setTimeout(() => {
    el.style.outline = outline;
    highlighted.delete(el);
  }, 2000);
  highlighted.set(el, { outline, timer });
  return true;
}

/** Show for a field of the top frame (see reveal); a field gone from the page says so instead. */
export function showButton(doc: Document, selector: string, label: string): HTMLElement {
  const show = h("button", { class: "link", type: "button", "aria-label": `Show ${label} on the page` }, "Show");
  show.addEventListener("click", () => {
    if (!reveal(doc, selector)) show.replaceWith(h("span", { class: "muted" }, "no longer on the page"));
  });
  return show;
}

function rowList(title: string, rows: RowView[], doc: Document): Node | false {
  if (rows.length === 0) return false;
  const items = rows.slice(0, MAX_LISTED).map((r) => {
    const label = r.label || "(unlabeled)";
    const li = h("li", {}, `${label}${r.reason ? `: ${why(r.reason)}` : ""}${r.required ? " (required)" : ""}`);
    // A field of the top frame can be shown; one inside an embedded form cannot be reached from here.
    if (r.selector) li.append(" ", showButton(doc, r.selector, label));
    return li;
  });
  if (rows.length > MAX_LISTED) items.push(h("li", { class: "muted" }, `and ${rows.length - MAX_LISTED} more`));
  return h("div", {}, title && h("strong", {}, title), h("ul", { class: "list" }, ...items));
}

/** The counts, what needs the person, and which frames were filled. */
export function result(v: FillView, doc: Document = document): (Node | false)[] {
  const out: (Node | false)[] = [];
  const c = v.counts;
  if (c && (v.phase === "done" || v.phase === "stopped")) {
    if (c.requiredUnfilled > 0) {
      const one = c.requiredUnfilled === 1;
      out.push(h("p", { class: "bad" }, h("strong", {}, `${c.requiredUnfilled} required ${one ? "field is" : "fields are"} still empty.`), ` Fill ${one ? "it" : "them"} before you submit.`));
    }
    const counted = [`${c.filled} filled`, c.prefilled && `${c.prefilled} already filled`, c.review && `${c.review} to review`, c.skipped && `${c.skipped} skipped`];
    out.push(h("p", { role: "status" }, v.phase === "stopped" ? "Stopped. " : "", `${counted.filter(Boolean).join(", ")}.`));
    const rows = v.rows ?? [];
    const left = rows.filter((r) => ["skipped", "abstained", "failed"].includes(r.outcome));
    const onPurpose = (r: RowView) => r.reason === "user-edited" || (!r.required && ON_PURPOSE.has(r.reason ?? ""));
    out.push(
      // Every question the fill could not answer is the person's, required ones first; a blank
      // left on purpose is listed apart, unless the page requires it.
      rowList("Needs you", left.filter((r) => !onPurpose(r)).sort((a, b) => Number(b.required) - Number(a.required)), doc),
      rowList("Check these", rows.filter((r) => r.review), doc),
    );
    const blank = rowList("", left.filter(onPurpose), doc);
    if (blank) out.push(h("details", {}, h("summary", {}, `Not filled on purpose (${left.filter(onPurpose).length})`), blank));
  }
  if (v.notAnApplication) out.push(h("p", { class: "muted" }, "This page does not look like an application form."));
  if (v.matchUnavailable) out.push(h("p", { class: "bad" }, "The answer matcher did not answer, so only profile fields were filled."));
  if (v.stall) out.push(h("p", { class: "bad" }, v.stall));
  if (v.drafts) out.push(h("p", {}, `${v.drafts} free-text ${v.drafts === 1 ? "question was" : "questions were"} left for you: nothing was written. Answer ${v.drafts === 1 ? "it" : "them"} yourself, or draft an answer in the Answer tab.`));
  // Only a frame that failed counts as unreachable; a frame skipped on purpose never does.
  const failed = v.frames.filter((f) => f.outcome.startsWith("failed-"));
  if (v.unreachable && failed.length > 0) {
    out.push(
      h(
        "div",
        { class: "bad", role: "alert" },
        h("p", {}, `${failed.length} part${failed.length === 1 ? "" : "s"} of this page could not be reached, so the form may be only partly filled. Fill what is missing by hand.`),
        h("ul", { class: "list" }, ...failed.map((f) => h("li", {}, `${f.url}: ${FRAME_OUTCOME[f.outcome] ?? f.outcome}${f.reason ? ` (${f.reason})` : ""}`))),
      ),
    );
  }
  if (v.frames.length > 1) {
    out.push(
      h(
        "details",
        {},
        h("summary", {}, `Frames (${v.frames.length})`),
        h("ul", { class: "list" }, ...v.frames.map((f) => h("li", {}, `${f.url}: ${FRAME_OUTCOME[f.outcome] ?? f.outcome}${f.controls !== undefined ? `, ${f.controls} controls` : ""}`))),
      ),
    );
  }
  return out;
}
