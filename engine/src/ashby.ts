// Clean-room: docs/provenance.md#CR-ENGINE
// Ashby: the page saves every answer with its own whole-form request and
// loses answers when saves overlap. The helper in the page's world sends the
// saves one at a time; this side waits for the queue around each write and
// checks the server's echo of the field.

import { safeClick } from "./click-guard";
import { scanControls, type ControlInfo } from "./scan";
import { toggleOn } from "./toggles";
import type { WriteCtx } from "./env";
import type { MainWorld } from "./mainworld-client";
import type { EchoState } from "./mainworld/ashby-queue";

/** The save mutations the page sends; the queue owns exactly these and never the submission. */
export const ASHBY_OPS = "ApiSetFormValue|ApiAutofillWithResume|ApiSetFileValue";

export const ashbyActive = (ctx: WriteCtx): boolean => ctx.site === "ashby" && !!ctx.main;

export async function enableAshbyQueue(main: MainWorld): Promise<boolean> {
  try {
    await main.request("queue.enable", { ops: ASHBY_OPS });
    return true;
  } catch {
    return false;
  }
}

/** The queue is a fill's tool: it ends with the fill, so a later request of the page is never held. */
export async function disableAshbyQueue(main: MainWorld): Promise<void> {
  await main.request("queue.disable").catch(() => undefined);
}

/**
 * The texting consent under the phone box (UiPath's smsConsent and whatsAppConsent, Abridge's communicationConsent): the page
 * saves it with a request of its own, never in the form's copy. A question's own controls are named by its field path.
 */
const TEXTING_CONSENT = /^[a-z]+Consent$/i;

/** The field path the saved copy knows a control by; none for a control the saved copy does not hold. */
export function ashbyPath(el: Element): string {
  if (TEXTING_CONSENT.test(el.getAttribute("name") ?? "")) return "";
  return el.closest("[data-field-path]")?.getAttribute("data-field-path") ?? el.getAttribute("name") ?? el.getAttribute("id") ?? "";
}

async function drained(ctx: WriteCtx, grace: boolean): Promise<void> {
  if (grace) await ctx.sleep(ctx.timing.ashbyGraceMs);
  try {
    await (ctx.main as MainWorld).request("queue.drain", { timeoutMs: ctx.timing.ashbyDrainMs }, ctx.timing.ashbyDrainMs + 1500);
  } catch {
    /* a silent helper is not a reason to stall the write */
  }
}

/** Wait for the saves of earlier writes before the next one starts. */
export async function ashbyBefore(ctx: WriteCtx): Promise<void> {
  if (ashbyActive(ctx)) await drained(ctx, false);
}

/**
 * After a write: a pending debounced text save is flushed by leaving the
 * field, the queue is drained, and the echo must hold the answer. True when
 * the saved copy has it (or no echo has arrived to say otherwise).
 */
export async function ashbyHolds(ctx: WriteCtx, el: Element): Promise<boolean> {
  const textual = el.localName === "input" || el.localName === "textarea";
  const active = ctx.doc.activeElement as HTMLElement | null;
  if (active && el.contains(active) && active.localName !== "button") active.blur();
  else if (textual) {
    // A value written without focus is saved after the page's own debounce; a blur ends it at once.
    (el as HTMLElement).focus({ preventScroll: true });
    (el as HTMLElement).blur();
  }
  await drained(ctx, true);
  const path = ashbyPath(el);
  if (!path) return true;
  const held = async (): Promise<boolean> => {
    try {
      const echo = await (ctx.main as MainWorld).request<EchoState>("queue.echo");
      return !echo.seen || echo.held[path] === true;
    } catch {
      return true;
    }
  };
  if (await held()) return true;
  if (!textual || !(el as HTMLInputElement).value) return false;
  // A page whose blur does not send the save still sends it once its debounce ends. The wait is that long by the
  // clock: a drain held by a stuck save takes far longer than one poll.
  const until = Date.now() + ctx.timing.ashbyDebounceMs;
  while (Date.now() < until) {
    await ctx.checkpoint();
    await ctx.sleep(ctx.timing.pollMs);
    await drained(ctx, false);
    if (await held()) return true;
  }
  return false;
}

/** Required questions the saved copy lacks, as the page labels them. */
export function gapsFrom(doc: Document, echo: EchoState): string[] {
  if (!echo.seen) return [];
  const labels = new Map<string, string>();
  for (const info of scanControls(doc)) labels.set(ashbyPath(info.el), info.label.text);
  return Object.keys(echo.required)
    .filter((p) => echo.held[p] !== true)
    .map((p) => labels.get(p) ?? p);
}

/** A two-option Yes/No of an Ashby form. */
export function isAshbyYesNo(info: ControlInfo): boolean {
  return info.toggles.length === 2 && info.el.closest('[class*="_yesno_"], [data-field-path]') !== null;
}

/**
 * Sends again a Yes/No the page shows pressed but the server lacks: a write
 * never re-presses a pressed option, since one press only clears it. The
 * selected option is pressed (the page clears it), the clear is waited for and
 * its save answered, then the same option is pressed again. The other option
 * is never pressed. True when the option ends pressed.
 */
export async function recommitYesNo(ctx: WriteCtx, info: ControlInfo): Promise<boolean> {
  const selected = info.toggles.find(toggleOn);
  if (!selected || safeClick(selected) === "unsafe-click") return false;
  for (let i = 0; i < 20 && toggleOn(selected); i++) await ctx.sleep(ctx.timing.pollMs);
  await drained(ctx, true);
  if (toggleOn(selected) || safeClick(selected) !== "ok") return false;
  await drained(ctx, true);
  return true;
}

/** The submit-repair round: every pressed Yes/No the server lacks is sent again. */
export async function repairYesNo(ctx: WriteCtx): Promise<number> {
  if (!ashbyActive(ctx)) return 0;
  const echo = await (ctx.main as MainWorld).request<EchoState>("queue.echo").catch(() => null);
  if (!echo?.seen) return 0;
  let repaired = 0;
  for (const info of scanControls(ctx.doc)) {
    if (!isAshbyYesNo(info)) continue;
    const path = ashbyPath(info.el);
    if (!path || echo.held[path] === true) continue;
    if (await recommitYesNo(ctx, info)) repaired++;
  }
  return repaired;
}
