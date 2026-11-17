// Clean-room: docs/provenance.md#CR-ENGINE
// SuccessFactors: every section starts collapsed, and the page's own uploader
// overwrites a file assigned from outside, so the résumé goes in through the
// main-world helper after the page's own trigger has been pressed.

import { collapse, flatText } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { probeControls } from "./detect";
import { pointerSequence } from "./gestures";
import type { ResumePayload } from "./protocol";
import { selectorFor } from "./selector";
import { isRendered } from "./visibility";

/**
 * The system's own uploader inputs: a fileData name, the fileUpload class, or the form of the uploader. That
 * form exists for the file alone; an application form that happens to be named form_something is not it.
 */
export function isSfUploader(input: Element): boolean {
  if (input.localName !== "input" || (input as HTMLInputElement).type !== "file") return false;
  const form = (input as HTMLInputElement).form;
  const uploaderForm = form !== null && /^(form_|fil)/.test(form.id) && Array.from(form.elements).every((e) => e === input || (e as HTMLInputElement).type === "hidden");
  return /^fileData/.test(input.getAttribute("name") ?? "") || input.classList.contains("fileUpload") || uploaderForm || input.closest(".fileUploaderForm") !== null;
}

/**
 * Presses the page's single "Expand all sections" anchor once, never the
 * collapse one, and waits for the visible field count to grow. A no-op on a
 * page without the anchor.
 */
export async function expandAllSections(ctx: WriteCtx): Promise<boolean> {
  const anchor = Array.from(ctx.doc.querySelectorAll<HTMLElement>('a[id$="_expandAllSections"]')).find((a) => isRendered(a));
  if (!anchor) return false;
  const before = probeControls(ctx.doc).controls;
  if (pointerSequence(anchor) === "unsafe-click") return false;
  const grew = await waitFor(ctx, () => probeControls(ctx.doc).controls > before, ctx.timing.sfExpandWaitMs, ctx.timing.sfPollMs);
  ctx.log("sf-expand", { grew: grew === true });
  return grew === true;
}

const ATTACHED = '[id$="_trashIcon"], [class~="trash"], [class*="trashIcon"]';

function trigger(doc: Document): HTMLElement | null {
  const icons = Array.from(doc.querySelectorAll<HTMLElement>('[id$="_attachIcon"]')).filter((e) => isRendered(e) && !e.matches(ATTACHED));
  if (icons.length) return icons[0] as HTMLElement;
  return Array.from(doc.querySelectorAll<HTMLElement>("button, a")).find((e) => isRendered(e) && /^upload (a )?(resume|cv)\b/i.test(collapse(flatText(e)))) ?? null;
}

export type SfUpload = "attached" | "already" | "no-trigger" | "unavailable" | "not-landed";

/**
 * Stages the document in the helper, presses the empty-state trigger (found
 * again after the async load, because a re-hydrated section detaches the
 * first reference), and puts the file on the uploader. A trash icon means a
 * file is already attached and nothing is replaced. "Attached" is claimed
 * only when the helper confirms a file landed, and however the upload ends
 * the helper is told to drop the staged document.
 */
export async function attachSfResume(ctx: WriteCtx, input: HTMLInputElement, payload: ResumePayload): Promise<SfUpload> {
  if (!ctx.main) return "unavailable";
  if (Array.from(ctx.doc.querySelectorAll<HTMLElement>(ATTACHED)).some((e) => isRendered(e))) return "already";
  const first = await waitFor(ctx, () => trigger(ctx.doc), ctx.timing.sfExpandWaitMs, ctx.timing.sfPollMs);
  if (!first) return "no-trigger";
  try {
    await ctx.main.request("sf.stage", { name: payload.fileName, type: payload.mediaType, base64: payload.contentBase64, ttlMs: 60_000 });
  } catch {
    return "unavailable";
  }
  // The section may have re-rendered while the document was being staged.
  const live = trigger(ctx.doc) ?? first;
  // The helper keeps the document only until this upload ends, so a later click on an uploader never gets it.
  const dropped = <T extends SfUpload>(out: T): T => {
    ctx.main?.send("sf.clear");
    return out;
  };
  if (pointerSequence(live) === "unsafe-click") return dropped("no-trigger");
  const landed = await waitFor(ctx, () => (input.files?.length ?? 0) > 0, ctx.timing.sfPollMs * 5, ctx.timing.sfPollMs);
  if (landed) return dropped("attached");
  try {
    const out = await ctx.main.request<{ attached: boolean }>("sf.attach", { selector: selectorFor(input) });
    return dropped(out.attached ? "attached" : "not-landed");
  } catch {
    return "unavailable";
  }
}
