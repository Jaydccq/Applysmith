// Clean-room: docs/provenance.md#CR-ENGINE
// An embedded application system opens on an overview with no controls. The
// control that reveals the form is pressed once, never a submit control and
// never one that leaves the frame, and the form is given time to appear.

import { collapse, flatText, walkElements } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { clickLabel, isUnsafeLabel } from "./click-guard";
import { detectApplication } from "./detect";
import { pointerSequence } from "./gestures";
import { isRendered } from "./visibility";

/** Application systems whose form can sit behind an overview inside a frame. */
const EMBEDDED = new Set(["ashby", "greenhouse", "greenhouse-eu", "greenhouse-embed", "lever", "jobvite", "workable", "smartrecruiters", "rippling", "bamboohr"]);

export const isEmbeddedSite = (site: string | null): boolean => site !== null && EMBEDDED.has(site);

const OPENER = /^apply( now| for this (job|position|role))?$/i;

/** A button that would submit its form is a submit control whatever it says. */
function submitsForm(el: HTMLElement): boolean {
  if (el.localName !== "button") return false;
  const type = (el.getAttribute("type") ?? "").toLowerCase();
  return type === "submit" || (type === "" && el.closest("form") !== null);
}

function staysInFrame(el: HTMLElement): boolean {
  if (el.localName !== "a") return true;
  const href = el.getAttribute("href");
  if (!href || href.startsWith("#")) return true;
  try {
    return new URL(href, el.ownerDocument.baseURI).origin === el.ownerDocument.defaultView?.location.origin;
  } catch {
    return false;
  }
}

/** The one control that reveals the application form, or null. */
export function revealControl(doc: Document): HTMLElement | null {
  let tab: HTMLElement | null = null;
  let button: HTMLElement | null = null;
  walkElements(doc.body ?? doc, (el) => {
    const h = el as HTMLElement;
    // Only an unselected tab or a button can be the control: nothing else is measured or read.
    const isTab = !tab && h.getAttribute("role") === "tab" && h.getAttribute("aria-selected") === "false";
    // An anchor is a navigation, so only a real button opens the form.
    const isButton = !button && h.localName === "button";
    if ((!isTab && !isButton) || !isRendered(h)) return;
    const label = collapse(flatText(h));
    if (isTab && /^application$/i.test(label) && staysInFrame(h)) tab = h;
    if (isButton && OPENER.test(label) && !submitsForm(h)) button = h;
  });
  return tab ?? button;
}

/**
 * On an embedded system with no form yet: press the revealing control once, if
 * there is one, and wait up to the reveal window for the form to appear.
 * Returns whether the document now holds an application form.
 */
export async function revealApplication(ctx: WriteCtx, log: (event: string, data?: Record<string, unknown>) => void): Promise<boolean> {
  const control = revealControl(ctx.doc);
  if (control) {
    if (isUnsafeLabel(clickLabel(control))) {
      log("reveal", { result: "unsafe-click" });
    } else {
      pointerSequence(control);
      log("reveal", { result: "pressed" });
    }
  }
  const found = await waitFor(ctx, () => detectApplication(ctx.doc).application, ctx.timing.revealWaitMs, ctx.timing.revealPollMs);
  log("reveal", { result: found ? "form" : "no-fields" });
  return found === true;
}
