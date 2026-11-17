// Clean-room: docs/provenance.md#CR-ENGINE
// Date parsing and the shapes date-bearing controls accept.

const MONTHS = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];

export interface YMD {
  y: number;
  m: number;
  d: number | null;
}

const pad = (n: number, w = 2) => String(n).padStart(w, "0");

/** A two-digit year is this century's unless that lies more than ten years ahead: "98" is 1998, not 2098. */
function year(raw: string): number {
  const n = parseInt(raw, 10);
  if (raw.length > 2) return n;
  const now = new Date().getFullYear();
  const y = now - (now % 100) + n;
  return y > now + 10 ? y - 100 : y;
}

/** Dates written with month names, and the numeric forms. */
export function parseDate(text: string): YMD | null {
  const s = text.trim();
  let m = /^(\d{4})-(\d{1,2})(?:-(\d{1,2}))?$/.exec(s);
  if (m) return mk(+(m[1] as string), +(m[2] as string), m[3] ? +m[3] : null);
  m = /^(\d{1,2})[/.-](\d{1,2})[/.-](\d{2}|\d{4})$/.exec(s);
  if (m) {
    const a = +(m[1] as string);
    const b = +(m[2] as string);
    const y = year(m[3] as string);
    // US order unless the first number cannot be a month.
    return a <= 12 ? mk(y, a, b) : mk(y, b, a);
  }
  m = /^(\d{1,2})[/.-](\d{4})$/.exec(s);
  if (m) return mk(+(m[2] as string), +(m[1] as string), null);
  m = /^([A-Za-z]{3,9})\.?\s+(?:(\d{1,2}),?\s+)?(\d{4})$/.exec(s);
  if (m) {
    const idx = MONTHS.indexOf((m[1] as string).slice(0, 3).toLowerCase());
    if (idx >= 0) return mk(+(m[3] as string), idx + 1, m[2] ? +m[2] : null);
  }
  return null;
}

function mk(y: number, m: number, d: number | null): YMD | null {
  if (m < 1 || m > 12 || y < 1000 || y > 9999) return null;
  if (d !== null && (d < 1 || d > 31)) return null;
  return { y, m, d };
}

/** ISO date for a native date input, or null when the text is not a full date. */
export function isoDate(text: string): string | null {
  const p = parseDate(text);
  if (!p || p.d === null) return null;
  return `${pad(p.y, 4)}-${pad(p.m)}-${pad(p.d)}`;
}

const RANGE_SPLIT = /\s+(?:-|to|–|—)\s+|\s*(?:–|—)\s*/;

/** The edge of a date range the control's wording names: the end for graduation or end wording, else the start. */
export function rangeEdge(text: string, wording: string): string {
  const parts = text.split(RANGE_SPLIT).map((s) => s.trim()).filter(Boolean);
  let pick = parts[0] ?? "";
  if (parts.length > 1) {
    const end = /\b(end|graduation|graduate|completion|completed|finish|to)\b/i.test(wording);
    const start = /\b(start|begin|beginning|from)\b/i.test(wording);
    pick = end && !start ? (parts[parts.length - 1] as string) : (parts[0] as string);
  }
  return pick;
}

/** YYYY-MM for a month input; a range is cut at the edge the control's wording names. */
export function monthValue(text: string, wording: string): string | null {
  const p = parseDate(rangeEdge(text, wording));
  return p ? `${pad(p.y, 4)}-${pad(p.m)}` : null;
}

export type DateLayout = "us" | "day-first" | "iso" | "month-year" | "year-month";

/** The layout a text date control declares through its placeholder, label, pattern or mask. */
export function layoutHint(hint: string): DateLayout | null {
  const h = hint.toLowerCase();
  if (/yyyy[-/.]mm[-/.]dd/.test(h)) return "iso";
  if (/(?<![a-z])mm[-/.]yyyy(?![-/.]?dd)/.test(h) && !/dd/.test(h)) return "month-year";
  if (/yyyy[-/.]mm(?![-/.]?dd)/.test(h) && !/dd/.test(h)) return "year-month";
  if (/dd[-/.]mm[-/.](yyyy|yy)|d[-/.]m[-/.]yy|day\s*\/\s*month\s*\/\s*year/.test(h)) return "day-first";
  if (/mm[-/.]dd[-/.](yyyy|yy)|month\s*\/\s*day\s*\/\s*year/.test(h)) return "us";
  return null;
}

export function formatDate(p: YMD, layout: DateLayout, sep = "/"): string {
  const d = p.d ?? 1;
  switch (layout) {
    case "iso":
      return `${pad(p.y, 4)}-${pad(p.m)}-${pad(d)}`;
    case "month-year":
      return `${pad(p.m)}${sep}${pad(p.y, 4)}`;
    case "year-month":
      return `${pad(p.y, 4)}${sep}${pad(p.m)}`;
    case "day-first":
      return `${pad(d)}${sep}${pad(p.m)}${sep}${pad(p.y, 4)}`;
    default:
      return `${pad(p.m)}${sep}${pad(d)}${sep}${pad(p.y, 4)}`;
  }
}

/**
 * The separator a hint writes between date parts ("DD.MM.YYYY", or the short "d.m.yy" layoutHint reads); a slash when
 * it names none: the "..." of "Pick date...", the dash of a pattern's "[0-9]" and the dots of an example date are no separator.
 */
export function datePartSeparator(hint: string): string {
  return (/(?:yyyy|yy|mm|dd)\s*([-./])\s*(?:yyyy|yy|mm|dd)/i.exec(hint) ?? /(?<![a-z])d([-./])m[-./]yy/i.exec(hint))?.[1] ?? "/";
}

/** Today in the browser's local time, shaped for the control. */
export function todayFor(now: Date, type: string, hint: string): string {
  const p: YMD = { y: now.getFullYear(), m: now.getMonth() + 1, d: now.getDate() };
  if (type === "date") return formatDate(p, "iso");
  return formatDate(p, layoutHint(hint) ?? "us", datePartSeparator(hint));
}
