// Clean-room: docs/provenance.md#CR-ENGINE
// LinkedIn's job and feed pages hold many combobox-shaped widgets that a scan
// of the whole document would open and stall on, so an Easy Apply step is
// scanned inside its dialog only. The step loop (Next, Review, Submit) is not
// the engine's.

import { collapse, flatText } from "./dom";
import { siteOf } from "./sites";
import { isRendered } from "./visibility";

const DIALOG = "[role=dialog], [role=alertdialog], dialog[open]";

/** The Easy Apply dialog, when one is open. */
export function easyApplyDialog(doc: Document): Element | null {
  for (const d of Array.from(doc.querySelectorAll(DIALOG))) {
    if (!isRendered(d)) continue;
    const marks = `${d.getAttribute("class") ?? ""} ${d.getAttribute("aria-label") ?? ""} ${collapse(flatText(d.querySelector("h1, h2, h3, [role=heading]") ?? d)).slice(0, 120)}`;
    if (/easy[- ]?apply/i.test(marks) || /\bapply to\b/i.test(marks)) return d;
  }
  return null;
}

const onLinkedIn = (doc: Document): boolean => siteOf(doc.defaultView?.location?.href ?? "") === "linkedin";

/** LinkedIn shows no Easy Apply dialog: there is nothing for the engine to scan or touch. */
export function noEasyApplyDialog(doc: Document): boolean {
  return onLinkedIn(doc) && easyApplyDialog(doc) === null;
}

/**
 * Where a scan looks: inside the Easy Apply dialog on LinkedIn, and nowhere
 * (an empty fragment) when LinkedIn shows no dialog, because its feed and job
 * pages are not the application. Every other site scans the document.
 */
export function scanRoot(doc: Document): ParentNode | null {
  if (!onLinkedIn(doc)) return null;
  return easyApplyDialog(doc) ?? doc.createDocumentFragment();
}
