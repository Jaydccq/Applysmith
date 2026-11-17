// The panel's "Next step" button: pressing the page's progress control once,
// in this frame or in the embedded application form that owns it. The control
// is found and vetted by the step walker's own rules, so nothing the walker
// would refuse (Submit, Withdraw, any never-label) can be pressed from here.
import { clickLabel } from "../../../engine/src/click-guard";
import { pointerSequence } from "../../../engine/src/gestures";
import { controlsOf, pressAllowed } from "./easyapply";
import { stepRoot } from "./easyapply-find";

export interface NextStepState {
  visible: boolean;
  disabled: boolean;
  title: string;
}

export interface AdvanceResult {
  advanced: boolean;
  label: string | null;
}

export const NOT_ADVANCED: AdvanceResult = { advanced: false, label: null };

// Widgets that are never an application form: a challenge, analytics or video frame.
const NON_APPLICATION_HOSTS = ["hcaptcha.com", "recaptcha.net", "challenges.cloudflare.com", "googletagmanager.com", "google-analytics.com", "doubleclick.net", "facebook.com", "youtube.com", "vimeo.com"];

function embeddable(src: string | null): boolean {
  const value = (src ?? "").trim();
  if (!value || /^(about|javascript):/i.test(value)) return false;
  let host: string;
  try {
    host = new URL(value, "https://page.invalid").hostname;
  } catch {
    return false;
  }
  return !NON_APPLICATION_HOSTS.some((d) => host === d || host.endsWith(`.${d}`));
}

/** True when an iframe could hold the application form, whose own Next this frame cannot see. */
export function embeddedFormPresent(doc: Document): boolean {
  return [...doc.querySelectorAll("iframe")].some((f) => embeddable(f.getAttribute("src")));
}

export function nextStepState(input: { hasProgress: boolean; embedded: boolean; busy: boolean }): NextStepState {
  if (!input.hasProgress && !input.embedded) return { visible: false, disabled: true, title: "No next-page control found" };
  if (input.busy) return { visible: true, disabled: true, title: "Autofill is still running" };
  return { visible: true, disabled: false, title: input.hasProgress ? "Press the page's next-step button once" : "Press the embedded form's next-step button once" };
}

export function hasProgressControl(doc: Document): boolean {
  return controlsOf(stepRoot(doc)).progress !== undefined;
}

/**
 * Presses this document's progress control once. A textless Dojo pager gets the
 * full pointer gesture, which it needs; every other control a plain click.
 */
export function advanceThisFrame(doc: Document): AdvanceResult {
  const { progress, pager } = controlsOf(stepRoot(doc));
  if (!progress) return NOT_ADVANCED;
  if (pager) return pointerSequence(pager) === "ok" ? { advanced: true, label: "Next" } : NOT_ADVANCED;
  const label = clickLabel(progress);
  if (pressAllowed(label, false) !== "progress") return NOT_ADVANCED;
  progress.click();
  return { advanced: true, label: label.slice(0, 60) };
}
