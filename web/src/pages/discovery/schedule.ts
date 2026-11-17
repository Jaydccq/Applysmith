import type { Schedule, SchedulePreset } from "../../wire";

export const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;
const LABEL: Record<string, string> = { mon: "Monday", tue: "Tuesday", wed: "Wednesday", thu: "Thursday", fri: "Friday", sat: "Saturday", sun: "Sunday" };
const RULE_DAY: Record<string, string> = { MO: "mon", TU: "tue", WE: "wed", TH: "thu", FR: "fri", SA: "sat", SU: "sun" };
const RULE_PARTS = new Set(["FREQ", "INTERVAL", "BYDAY", "BYMONTHDAY", "BYHOUR", "BYMINUTE", "BYSECOND"]);
export const POLICIES = [
  { id: "skip", label: "Skip what was missed" },
  { id: "fire-once", label: "Run once when back" },
  { id: "fire-all", label: "Run every missed time" },
] as const;

export const localZone = (): string => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
// ZONES are the zones this browser knows, offered as suggestions to a time
// zone field; the field still takes any name the server accepts.
export const ZONES = Intl.supportedValuesOf("timeZone");
export const dayLabel = (d: string): string => LABEL[d] ?? d;
export const shortDay = (d: string): string => dayLabel(d).slice(0, 3);

const pad = (n: number) => String(n).padStart(2, "0");

// untilLabel says how far off a fire time is, so a bare date and time reads as
// "in 9 h 30 min" at a glance.
export function untilLabel(iso: string, now: number = Date.now()): string {
  const min = Math.floor((Date.parse(iso) - now) / 60_000);
  if (Number.isNaN(min)) return "";
  if (min < 1) return "due now";
  if (min < 60) return `in ${min} min`;
  const h = Math.floor(min / 60);
  return h < 48 ? `in ${h} h${min % 60 && h < 10 ? ` ${min % 60} min` : ""}` : `in ${Math.floor(h / 24)} days`;
}
const every = (n: number, unit: string) => (n > 1 ? `Every ${n} ${unit}s` : `Every ${unit}`);

// describeRule puts the recurrence rules the scheduler writes (a frequency, an
// interval, days and times of day) in words. A rule with any other part is
// shown as written, so nothing it says is left out.
function describeRule(rule: string): string {
  const parts = new Map(rule.split(";").map((p) => p.split("=") as [string, string]));
  const list = (k: string) => (parts.get(k) ?? "").split(",").filter(Boolean);
  const n = Number(parts.get("INTERVAL") ?? "1");
  if ([...parts.keys()].some((k) => !RULE_PARTS.has(k)) || list("BYSECOND").some((s) => s !== "0") || list("BYDAY").some((d) => !RULE_DAY[d]) || !Number.isInteger(n) || n < 1) return rule;
  const hours = list("BYHOUR").map(Number);
  const minutes = list("BYMINUTE").map(Number);
  const at = hours.length > 0 ? ` at ${hours.flatMap((h) => (minutes.length ? minutes : [0]).map((m) => `${pad(h)}:${pad(m)}`)).join(", ")}` : "";
  const days = list("BYDAY").map((d) => LABEL[RULE_DAY[d]]);
  const monthDays = list("BYMONTHDAY");
  switch (parts.get("FREQ")) {
    case "MINUTELY":
      return every(n, "minute");
    case "HOURLY":
      return every(n, "hour") + (hours.length === 0 && minutes.length === 1 ? ` at minute ${minutes[0]}` : "");
    case "DAILY":
      return every(n, "day") + at;
    case "WEEKLY":
      return every(n, "week") + (days.length ? ` on ${days.join(", ")}` : "") + at;
    case "MONTHLY":
      return monthDays.length ? `${every(n, "month")} on day ${monthDays.join(", ")}${at}` : rule;
    default:
      return rule;
  }
}

// describePreset says when a schedule runs. The time zone is named only when
// it is not this computer's, the zone every other time on the page is in.
export function describePreset(p: SchedulePreset): string {
  const zone = p.timezone && p.timezone !== localZone() ? ` (${p.timezone})` : "";
  if (p.kind === "daily") return `Every day at ${p.time}${zone}`;
  if (p.kind === "weekly") return `${(p.weekdays ?? []).map(dayLabel).join(", ")} at ${p.time}${zone}`;
  return `${describeRule(p.rrule ?? "")}${zone}`;
}

const localTime = (iso: string) => {
  const d = new Date(iso);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
};
const startOfDay = (t: number) => new Date(t).setHours(0, 0, 0, 0);
// Monday first: getDay() counts Sunday as 0.
const weekday = (t: number) => WEEKDAYS[(new Date(t).getDay() + 6) % 7];

interface TimetableCell {
  schedule: Schedule;
  time: string;
}

interface TimetableDay {
  day: string;
  today: boolean;
  cells: TimetableCell[];
}

// timetable lays the enabled schedules out over the next seven days, today
// first, at the time of day on this computer. A daily schedule fills every
// day; a weekly one in this computer's time zone fills its weekdays; any other
// recurrence sits on the days its upcoming fires fall within the seven, so a
// monthly job shows only in the week it runs. A schedule that fires more than
// once in a day (every 10 minutes) would fill the table, so it is returned in
// `often` and named once instead.
export function timetable(schedules: Schedule[], now = Date.now()): { days: TimetableDay[]; often: Schedule[] } {
  const first = startOfDay(now);
  const days: TimetableDay[] = Array.from({ length: 7 }, (_, i) => {
    const d = new Date(first);
    d.setDate(d.getDate() + i);
    return { day: weekday(d.getTime()), today: i === 0, cells: [] };
  });
  const dayOf = (iso: string) => Math.round((startOfDay(Date.parse(iso)) - first) / 86_400_000);
  const often: Schedule[] = [];
  for (const s of schedules) {
    if (!s.enabled || s.next.length === 0) continue;
    if (s.preset.kind === "daily") {
      for (const d of days) d.cells.push({ schedule: s, time: localTime(s.next[0]) });
      continue;
    }
    if (s.preset.kind === "weekly" && s.preset.timezone === localZone() && s.preset.time) {
      for (const d of days) if (s.preset.weekdays?.includes(d.day)) d.cells.push({ schedule: s, time: s.preset.time });
      continue;
    }
    const fires = s.next.map((at) => ({ i: dayOf(at), time: localTime(at) })).filter((f) => f.i >= 0 && f.i < 7);
    if (new Set(fires.map((f) => f.i)).size < fires.length) {
      often.push(s);
      continue;
    }
    for (const f of fires) days[f.i].cells.push({ schedule: s, time: f.time });
  }
  for (const d of days) d.cells.sort((a, b) => a.time.localeCompare(b.time));
  return { days, often };
}
