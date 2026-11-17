// The step walker for LinkedIn Easy Apply and for hosted applications that go
// from step to step inside one document. For each step it has the engine fill,
// then presses only the step's progress control. It presses the final submit
// control only when the person started this walk with the submit option, which
// is off by default and is not remembered. Before that press it must hold a
// press claim from the application, which runs the duplicate guard: anything
// but an explicit grant, an unreachable application included, means no press.
// It never presses Withdraw.
import { clickLabel } from "../../../engine/src/click-guard";
import { pointerSequence } from "../../../engine/src/gestures";
import { isNeverLabel } from "../../../engine/src/guard-text";
import { siteOf } from "../../../engine/src/sites";
import { isRendered } from "../../../engine/src/visibility";
import type { PressDecision } from "../shared/messages";
import { blockingError, CONTROLS, isProfileSummaryStep, pagerAdvance, PROGRESS, stepRoot, SUBMIT } from "./easyapply-find";

export const MAX_ITERATIONS = 8;
export const SETTLE_MS = 700;
/** How often a hosted flow is checked for its next step while the budget runs. */
export const SETTLE_POLL_MS = 400;
/** How long a hosted single-document flow may take to render its next step. */
export const HOSTED_SETTLE_BUDGET_MS = 8000;

/** LinkedIn swaps its modal in place and keeps the single wait; every other site gets the hosted budget. */
export function settleBudgetFor(href: string): number {
  return siteOf(href) === "linkedin" ? 0 : HOSTED_SETTLE_BUDGET_MS;
}

/**
 * The engine's never-press rule for a label that may say submit: the word
 * submit itself is taken out, and anything else the rule refuses (withdraw,
 * discard, delete, a terminal apply, look-alike letters) still refuses it.
 */
const neverPress = (label: string): boolean => isNeverLabel(label.replace(/\bsubmit\b/gi, " "));

export type WalkEnd =
  | "submitted"
  | "stopped-before-submit"
  | "cap"
  | "no-advance"
  | "validation"
  | "no-control"
  | "draft-pending"
  | "claim-refused"
  | "fill-failed"
  | "user-stop";

export interface WalkState {
  phase: "filling" | "claiming" | "pressing" | "waiting" | "paused" | "ended";
  iteration: number;
  label?: string;
  end?: WalkEnd;
  message?: string;
}

export interface WalkDeps {
  doc: Document;
  /**
   * Has the engine fill the current step; resolves when it has ended, with the
   * fields it filled when known and the free-text drafts it composed and left
   * unwritten.
   */
  fill(): Promise<{ ok: boolean; message?: string; filled?: number; drafts?: number }>;
  pendingDrafts(): number;
  /** Asks the application for permission to press the final control; a rejection counts as a refusal. */
  claim(): Promise<PressDecision>;
  /** Tells the application the final control was pressed under the claim. */
  pressed(claimId: string): Promise<void>;
  report(state: WalkState): void;
  submit: boolean;
  /**
   * Hosted flows render a step hundreds of milliseconds to seconds after the
   * route changes: with a budget the walker checks for the next step every
   * SETTLE_POLL_MS until a step other than the pressed one shows a control, or
   * the budget ends. Without one it
   * waits SETTLE_MS once, as LinkedIn's in-place modal needs.
   */
  settleBudgetMs?: number;
  sleep?: (ms: number) => Promise<void>;
}

export interface Walk {
  pause(): void;
  resume(): void;
  stop(): void;
  done: Promise<WalkEnd>;
}

function rendered(root: Element): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(CONTROLS)].filter((c) => !(c as HTMLButtonElement).disabled && c.getAttribute("aria-disabled") !== "true" && isRendered(c) && !c.closest("[data-applysmith]"));
}

/** What the step looks like, to tell whether a press moved the form on. */
function signature(root: Element): string {
  return `${rendered(root).map(clickLabel).join("|")}#${(root.textContent ?? "").replace(/\s+/g, " ").slice(0, 400)}`;
}

/** The only click the walker makes: a progress control, or the final submit when allowed. */
export function pressAllowed(label: string, allowSubmit: boolean): "progress" | "submit" | null {
  if (neverPress(label)) return null;
  // The walker is the one that presses Next, Continue and Review, so only the never-press words stop a progress label here.
  if (PROGRESS.test(label) && !isNeverLabel(label)) return "progress";
  return allowSubmit && SUBMIT.test(label) ? "submit" : null;
}

export interface StepControls {
  progress?: HTMLElement;
  /** Set when the progress control is a textless pager, which a bare click does not move. */
  pager?: HTMLElement;
  submit?: HTMLElement;
}

/** The step's progress and submit controls; the panel's Next step button looks here too, so it can press nothing the walker would not. */
export function controlsOf(root: Element): StepControls {
  const all = rendered(root);
  const text = all.find((c) => pressAllowed(clickLabel(c), false) === "progress");
  const submit = all.find((c) => SUBMIT.test(clickLabel(c)) && !neverPress(clickLabel(c)));
  // A structural control is looked for only where no text names the step's end, so it is never taken for a way past a Submit.
  const pager = text || submit ? undefined : (pagerAdvance(root) ?? undefined);
  return { progress: text ?? pager, pager, submit };
}

export function startWalk(deps: WalkDeps): Walk {
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  let stopped = false;
  let paused = false;
  let wake: (() => void) | undefined;
  let iteration = 0;
  let filledTotal = 0;
  // Drafts the fills composed and did not write: their fields are still empty, so Submit waits as for a pending draft.
  let unwritten = 0;

  const say = (s: Omit<WalkState, "iteration">) => deps.report({ ...s, iteration });
  const end = (e: WalkEnd, message: string): WalkEnd => {
    say({ phase: "ended", end: e, message });
    return e;
  };
  async function checkpoint(): Promise<boolean> {
    while (paused && !stopped) {
      say({ phase: "paused" });
      await new Promise<void>((r) => (wake = r));
    }
    return stopped;
  }

  const noControlMessage = (filledNow: number | undefined): string =>
    filledNow === 0
      ? `Nothing to fill on this step, and it has no Continue, Next or Review control. Filled ${filledTotal} field(s) across ${iteration} step(s).`
      : filledNow === undefined
        ? "No Continue, Next or Review control is on this step."
        : `Step filled (${filledNow}). No Continue, Next or Review control was found. Review it and move on yourself.`;

  /** Waits for the pressed step to give way to the next one: another step on screen that shows a control. */
  async function settle(before: string): Promise<void> {
    const budget = deps.settleBudgetMs ?? 0;
    if (budget <= 0) return sleep(SETTLE_MS);
    const poll = Math.min(SETTLE_MS, SETTLE_POLL_MS);
    let waited = 0;
    // At least one interval always passes, so the route change has started before the page is read.
    do {
      await sleep(poll);
      waited += poll;
      if (stopped) return;
    } while (waited < budget && !nextStepShown(before));
  }

  const nextStepShown = (before: string): boolean => {
    const root = stepRoot(deps.doc);
    if (signature(root) === before) return false;
    const c = controlsOf(root);
    return Boolean(c.progress || c.submit);
  };

  async function loop(): Promise<WalkEnd> {
    for (;;) {
      if (await checkpoint()) return end("user-stop", "Stopped.");
      say({ phase: "filling" });
      // A step the host filled in from the person's own profile there is advanced, never edited.
      let filledNow: number | undefined = 0;
      if (!isProfileSummaryStep(stepRoot(deps.doc))) {
        const filled = await deps.fill();
        if (!filled.ok) return end("fill-failed", filled.message ?? "Autofill could not fill this step.");
        filledNow = filled.filled;
        filledTotal += filledNow ?? 0;
        unwritten += filled.drafts ?? 0;
      }
      if (await checkpoint()) return end("user-stop", "Stopped.");
      const root = stepRoot(deps.doc);
      const blocked = blockingError(root);
      if (blocked) return end("validation", `This step shows an error: "${blocked}". Fix it, then start the walk again.`);
      const { progress, pager, submit } = controlsOf(root);
      if (!progress) {
        if (!submit) return end("no-control", noControlMessage(filledNow));
        if (!deps.submit) return end("stopped-before-submit", `Stopped at "${clickLabel(submit)}". Review the form and press it yourself.`);
        if (deps.pendingDrafts() + unwritten > 0) return end("draft-pending", "A composed draft is not filled in yet, so Submit was not pressed.");
        if (pressAllowed(clickLabel(submit), true) !== "submit") return end("no-control", "The submit control was not safe to press.");
        say({ phase: "claiming", label: clickLabel(submit) });
        const decision = await deps.claim().catch((): PressDecision => ({ granted: false, message: "The press could not be cleared. Submit was not pressed." }));
        if (decision.granted !== true || !decision.claimId) return end("claim-refused", decision.granted === false ? decision.message : "The application did not grant the press. Submit was not pressed.");
        // The claim took time: the person may have stopped, typed a draft in, or the page may have moved on.
        if (stopped) return end("user-stop", "Stopped before Submit was pressed.");
        if (!submit.isConnected || !isRendered(submit) || (submit as HTMLButtonElement).disabled || pressAllowed(clickLabel(submit), true) !== "submit" || deps.pendingDrafts() > 0) {
          return end("no-control", "The page changed while Submit was being cleared, so it was not pressed.");
        }
        say({ phase: "pressing", label: clickLabel(submit) });
        submit.click();
        await deps.pressed(decision.claimId).catch(() => {});
        return end("submitted", "Submit was pressed once.");
      }
      if (iteration >= MAX_ITERATIONS) return end("cap", `Stopped after ${MAX_ITERATIONS} steps.`);
      const before = signature(root);
      iteration++;
      say({ phase: "pressing", label: pager ? "Next" : clickLabel(progress) });
      if (pager) {
        if (pointerSequence(pager) !== "ok") return end("no-control", "The step's advance control was not safe to press.");
      } else progress.click();
      say({ phase: "waiting" });
      await settle(before);
      if (stopped) return end("user-stop", "Stopped.");
      if (signature(stepRoot(deps.doc)) === before) return end("no-advance", "The form did not move to the next step.");
    }
  }

  const done = loop();
  return {
    pause: () => void (paused = true),
    resume: () => {
      paused = false;
      wake?.();
    },
    stop: () => {
      stopped = true;
      wake?.();
    },
    done,
  };
}
