// Clean-room: docs/provenance.md#CR-ENGINE
// Is this document an application form, and what a frame holds.

import type { ProbeResult } from "./protocol";
import { resolveSelector } from "./selector";
import { ariaHiddenBlocks, isRendered, openDialogElsewhere } from "./visibility";
import { scanControls, type ControlInfo } from "./scan";

export type Detection =
  | { application: true; controls: number }
  | { application: false; reason: "no-form" | "blocked-by-dialog" };

function explicitlyRequired(info: ControlInfo): boolean {
  return info.members.some((m) => m.hasAttribute("required") || m.getAttribute("aria-required") === "true") || info.label.marked;
}

/** Workday's own container of one question. */
const WORKDAY_FIELD = '[data-automation-id^="formField"]';

/**
 * A data-entry control: a native field, an editable region or a combobox, or a listbox button the page marks
 * required or sets in Workday's own question container (Workday's questions, whose text twins are hidden); never
 * another button or clickable that opens a popup.
 */
function isDataEntry(info: ControlInfo): boolean {
  if (info.toggles.length) return false;
  if (info.driver === "date-segments") return true;
  if (info.kind === "custom-listbox" && (explicitlyRequired(info) || info.el.closest(WORKDAY_FIELD) !== null)) return true;
  const tag = info.el.localName;
  if (tag === "input" || tag === "textarea" || tag === "select") return true;
  return info.el.hasAttribute("contenteditable") || info.el.getAttribute("role") === "combobox" || info.el.getAttribute("role") === "textbox";
}

/** Visible, enabled data-entry controls and how many of them the page insists on. Writes nothing. */
export function probeControls(doc: Document): ProbeResult {
  const infos = doc.body ? scanControls(doc).filter(isDataEntry) : [];
  return { controls: infos.length, required: infos.filter(explicitlyRequired).length };
}

const FORM_FIELD = "input, textarea, select";
const NOT_DATA = new Set(["hidden", "submit", "reset", "button", "image", "search"]);

/** An application form has two data-entry controls, or one the page marks required. */
export function detectApplication(doc: Document): Detection {
  // Controls hidden from assistive tech behind an open dialog are a form the dialog is blocking.
  const behind = Array.from(doc.querySelectorAll<HTMLInputElement>(FORM_FIELD)).filter(
    (el) =>
      !NOT_DATA.has((el.getAttribute("type") ?? "").toLowerCase()) &&
      !el.disabled &&
      isRendered(el) &&
      ariaHiddenBlocks(el) &&
      openDialogElsewhere(doc, el),
  );
  if (behind.length >= 2 || behind.some((e) => e.required)) return { application: false, reason: "blocked-by-dialog" };
  const probe = probeControls(doc);
  if (probe.controls >= 2 || probe.required >= 1) return { application: true, controls: probe.controls };
  return { application: false, reason: "no-form" };
}

/**
 * A wizard step has advanced when none of the previous step's controls
 * resolves to anything on screen any more; a changed URL is not required, and
 * a changed URL with the same controls is not an advance.
 */
export function stepAdvanced(doc: Document, previous: string[]): boolean {
  if (!previous.length) return false;
  return previous.every((sel) => {
    const el = resolveSelector(doc, sel);
    return !el || !el.isConnected || !isRendered(el);
  });
}
