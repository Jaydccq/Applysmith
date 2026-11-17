// Clean-room: docs/provenance.md#CR-ENGINE
// Date pickers: a button that opens a day grid (react-day-picker style) and a
// date shown as separate month, day and year spinbuttons.

import { formatDate, isoDate, parseDate, type YMD } from "./dates";
import { collapse, flatText } from "./dom";
import { waitFor, type WriteCtx } from "./env";
import { focusEl, pointerSequence, typeText } from "./gestures";
import { classTokens } from "./popup";
import type { WriteOutcome } from "./writers";

const ISO = /^\d{4}-\d{2}-\d{2}$/;
const VERB_NEAR_DATE = /\b(select|choose|pick)\b.*\bdate\b|\bdate\b.*\b(select|choose|pick)\b/i;
const MONTHS = ["january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"];

/** A button with a dialog popup whose text is an unset date prompt, or an ISO date once picked. */
export function isCalendarTrigger(el: Element): boolean {
  if (el.localName !== "button" || el.getAttribute("aria-haspopup") !== "dialog") return false;
  const text = collapse(flatText(el));
  return ISO.test(text) || VERB_NEAR_DATE.test(text);
}

function panelFor(trigger: Element): HTMLElement | null {
  const root = trigger.getRootNode() as Document | ShadowRoot;
  const panels = Array.from(root.querySelectorAll<HTMLElement>('.rdp, [class*="rdp-root"], [role=dialog]')).filter((p) => p.querySelector("button[name=day], .rdp-day"));
  return panels[panels.length - 1] ?? null;
}

function shown(panel: HTMLElement): { y: number; m: number } | null {
  const label = panel.querySelector('[class*="caption_label"], [aria-live], [class*="caption"]');
  const m = /([A-Za-z]+)\s+(\d{4})/.exec(collapse(flatText(label ?? panel)));
  const word = m ? (m[1] as string).toLowerCase() : "";
  // A caption may abbreviate the month ("Nov 2026", "Sept 2026"): three letters name one month only.
  const idx = word.length >= 3 ? MONTHS.findIndex((name) => name.startsWith(word)) : -1;
  return m && idx >= 0 ? { y: +(m[2] as string), m: idx + 1 } : null;
}

function navButton(panel: HTMLElement, dir: "previous" | "next"): HTMLElement | null {
  const hit = Array.from(panel.querySelectorAll<HTMLElement>("button")).find((b) => {
    const mark = `${b.getAttribute("name") ?? ""} ${b.getAttribute("aria-label") ?? ""} ${b.getAttribute("class") ?? ""}`.toLowerCase();
    return mark.includes(dir) && !b.hasAttribute("disabled");
  });
  return hit ?? null;
}

/**
 * Opens the day grid, walks to the target month one click per tick (a second
 * click in the same tick advances one month only), clicks the enabled day only
 * once the caption reads as the target month and year, closes the panel by
 * clicking the trigger again, and reads the date back from the trigger's text.
 * Every exit past the open leaves the panel closed.
 */
export async function writeCalendar(trigger: HTMLElement, value: string, ctx: WriteCtx): Promise<WriteOutcome> {
  const iso = isoDate(value);
  if (!iso) return { ok: false, reason: "format-not-accepted" };
  if (collapse(flatText(trigger)) === iso) return { ok: true, optionLabel: iso };
  const [y, m, d] = iso.split("-").map(Number) as [number, number, number];
  if (pointerSequence(trigger) === "unsafe-click") return { ok: false, reason: "unsafe-click" };
  const panel = await waitFor(ctx, () => panelFor(trigger), ctx.timing.openWaitMs);
  if (!panel) return { ok: false, reason: "listbox-not-opened" };
  // Escape does not close this panel: the trigger is clicked again.
  const close = async (): Promise<void> => {
    if (panel.isConnected && panelFor(trigger)) pointerSequence(trigger);
    await ctx.sleep(ctx.timing.pollMs);
  };
  for (let i = 0; i < 60; i++) {
    const cur = shown(panel);
    if (!cur) break;
    const diff = (y - cur.y) * 12 + (m - cur.m);
    if (diff === 0) break;
    const step = navButton(panel, diff > 0 ? "next" : "previous");
    if (!step) break;
    if (pointerSequence(step) === "unsafe-click") {
      await close();
      return { ok: false, reason: "unsafe-click" };
    }
    await ctx.sleep(ctx.timing.toggleGapMs);
  }
  // A day is a date only in the month the grid shows: a caption that cannot be read, or a walk that stopped short, takes no day.
  const at = shown(panel);
  if (!at || at.y !== y || at.m !== m) {
    await close();
    return { ok: false, reason: "write-not-committed", detail: "stage=navigate" };
  }
  const day = Array.from(panel.querySelectorAll<HTMLElement>("button[name=day], .rdp-day")).find(
    (b) => collapse(flatText(b)) === String(d) && !b.hasAttribute("disabled") && !classTokens(b).some((t) => t.includes("outside")),
  );
  if (!day) {
    await close();
    return { ok: false, reason: "option-not-found" };
  }
  if (pointerSequence(day) === "unsafe-click") {
    await close();
    return { ok: false, reason: "unsafe-click" };
  }
  await ctx.sleep(ctx.timing.activateMs);
  await close();
  const read = collapse(flatText(trigger));
  return read === iso ? { ok: true, optionLabel: iso } : { ok: false, reason: "write-not-committed" };
}

const STRICT_FORMS = [/^\d{4}-\d{1,2}(?:-\d{1,2})?$/, /^\d{1,2}\/\d{1,2}\/\d{4}$/, /^(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?\s+\d{4}$/i];

/**
 * The date a segment writer accepts: ISO, a US slash date or a month and year
 * in strict mode, any parsable date otherwise. A month and year on segments
 * that ask for a day takes the first only when the question asks for the first
 * of the month.
 */
function segmentDate(parts: HTMLInputElement[], value: string, opts: SegmentOptions): YMD | null {
  const v = value.trim();
  if (opts.strict && !STRICT_FORMS.some((re) => re.test(v))) return null;
  const date = parseDate(v);
  if (date && date.d === null && opts.firstOfMonth && parts.some((p) => segmentOf(p) === "day")) return { ...date, d: 1 };
  return date;
}

/** What the segments read as once the value is written: ISO, or year-month when the date has no day input. */
export function segmentIso(parts: HTMLInputElement[], value: string, opts: SegmentOptions = {}): string | null {
  const date = segmentDate(parts, value, opts);
  if (!date) return null;
  const hasDay = parts.some((p) => segmentOf(p) === "day");
  if (hasDay) return date.d === null ? null : formatDate(date, "iso");
  return formatDate(date, "year-month", "-");
}

const SEGMENT_ID = /dateSection(Month|Day|Year)-input$/i;

/**
 * The part of the date an input holds, named by the end of its id. The row's
 * own name ahead of it ("lastDayOfWork", "birthday") says nothing about the part.
 */
function segmentOf(p: HTMLInputElement): "month" | "day" | "year" | null {
  const part = SEGMENT_ID.exec(p.id)?.[1]?.toLowerCase();
  return part === "month" || part === "day" || part === "year" ? part : null;
}

/** The expected text of each segment, in page order; null when a segment is not month, day or year. */
function expectedSegments(parts: HTMLInputElement[], p: YMD): string[] | null {
  const out: string[] = [];
  for (const part of parts) {
    const segment = segmentOf(part);
    if (segment === "month") out.push(String(p.m).padStart(2, "0"));
    else if (segment === "day") {
      if (p.d === null) return null;
      out.push(String(p.d).padStart(2, "0"));
    } else if (segment === "year") out.push(String(p.y).padStart(4, "0"));
    else return null;
  }
  return out;
}

const PICKER_OPTION = '[data-automation-id="monthPickerOption"], [data-automation-id^="datePicker"] [role="option"]';

async function typeSegments(parts: HTMLInputElement[], want: string[], ctx: WriteCtx): Promise<void> {
  for (const [i, part] of parts.entries()) {
    focusEl(part);
    await typeText(part, want[i] as string, ctx);
    part.dispatchEvent(new Event("change", { bubbles: true }));
    // The month input opens a picker whose option commits the month.
    if (segmentOf(part) === "month") {
      const opt = await waitFor(ctx, () => ctx.doc.querySelector<HTMLElement>(PICKER_OPTION), ctx.timing.pickerWaitMs, ctx.timing.pickerPollMs);
      if (opt) pointerSequence(opt);
    }
    part.blur();
  }
}

export interface SegmentOptions {
  /** A questionnaire answer: ISO, US slash or month-and-year form only. */
  strict?: boolean;
  /** The question asks for the first of the month. */
  firstOfMonth?: boolean;
}

const FIRST_OF_MONTH = /\b(?:first|1st)\s+(?:day\s+)?of\s+the\s+month\b/i;

/** How a segment writer reads the answer to this question. */
export function segmentOptions(question: string, matcher: boolean): SegmentOptions {
  return { strict: matcher, firstOfMonth: FIRST_OF_MONTH.test(question) };
}

/**
 * Month, day and year of a date typed into separate inputs, one key at a time,
 * in the order the page shows them, and read back per segment. The first
 * interaction on a fresh page can land before the widget hydrates, so a
 * commit that left every segment empty is repeated once.
 */
export async function writeDateSegments(parts: HTMLInputElement[], value: string, ctx: WriteCtx, opts: SegmentOptions = {}): Promise<WriteOutcome> {
  const date = segmentDate(parts, value, opts);
  if (!date) return { ok: false, reason: "format-not-accepted" };
  const want = expectedSegments(parts, date);
  if (!want) return { ok: false, reason: "format-not-accepted" };
  const held = () => parts.map((p) => p.value);
  // A tenant may show a month or day without its leading zero: "8" holds "08".
  const bare = (v: string) => v.replace(/^0+/, "");
  const matches = () => held().every((v, i) => bare(v) === bare(want[i] as string));
  await typeSegments(parts, want, ctx);
  await ctx.sleep(ctx.timing.settleMs);
  if (!matches() && held().every((v) => v === "")) {
    await ctx.sleep(ctx.timing.hydrateRetryMs);
    await typeSegments(parts, want, ctx);
    await ctx.sleep(ctx.timing.settleMs);
  }
  const iso = date.d === null ? formatDate(date, "year-month", "-") : formatDate(date, "iso");
  return matches() ? { ok: true, optionLabel: iso } : { ok: false, reason: "write-not-committed" };
}
