import type { IntakeItem, IntakeSelection, IntakeSource } from "../../wire";

export const KINDS = [
  { id: "cv", label: "CV version", signal: false },
  { id: "profile-export", label: "Exported profile", signal: false },
  { id: "education-record", label: "Education record", signal: false },
  { id: "project", label: "Project description (suggestions only)", signal: true },
  { id: "reference", label: "Reference (suggestions only)", signal: true },
  { id: "course", label: "Course outline (suggestions only)", signal: true },
  { id: "repository", label: "Repository README or skills (suggestions only)", signal: true },
] as const;

export const MAX_DOCUMENTS = 20;
// The request carries every document's text, and the route accepts 4 MiB.
export const MAX_BYTES = 4 * 1024 * 1024;

/** What the person has decided about one item. */
export interface Pick {
  on: boolean;
  /** conflict: field to the chosen value, or "keep" for the profile's own. */
  choices: Record<string, string>;
  months: Record<string, string>;
}

export const emptyPick: Pick = { on: false, choices: {}, months: {} };

const bareYear = (v: string | undefined): boolean => /^\d{4}$/.test(v ?? "");
const validMonth = (v: string, year: string): boolean => /^\d{4}-(0[1-9]|1[0-2])$/.test(v) && v.startsWith(year + "-");

export function sourceText(s: IntakeSource): string {
  return `${s.label}, ${s.locator} (${s.origin})`;
}

/** The value a date field will take if the item is applied as picked. */
function valueOf(item: IntakeItem, field: string, pick: Pick): string | undefined {
  const choice = item.fields.find((f) => f.field === field);
  if (!choice) return item.entry[field];
  const c = pick.choices[field];
  return c === "keep" ? choice.profile : c;
}

/** The date fields that the documents give as a bare year, with the year. */
export function monthFields(item: IntakeItem, pick: Pick): { field: string; year: string }[] {
  const dates = new Set([...item.needsMonth, ...item.fields.map((f) => f.field).filter((f) => f === "start" || f === "end")]);
  const out: { field: string; year: string }[] = [];
  for (const field of dates) {
    const v = valueOf(item, field, pick);
    if (v !== undefined && bareYear(v)) out.push({ field, year: v });
  }
  return out;
}

/** The date fields of an item that still wait for a month. */
export function missingMonths(item: IntakeItem, pick: Pick): string[] {
  return monthFields(item, pick).filter((m) => !validMonth(pick.months[m.field] ?? "", m.year)).map((m) => m.field);
}

/** A conflict is applied only once every one of its fields has a side. */
export function undecided(item: IntakeItem, pick: Pick): string[] {
  return item.type === "conflict" ? item.fields.map((f) => f.field).filter((f) => !pick.choices[f]) : [];
}

export function ready(item: IntakeItem, pick: Pick): boolean {
  return pick.on && undecided(item, pick).length === 0 && missingMonths(item, pick).length === 0;
}

/** The selections to send: only the items the person turned on and finished. */
export function selectionsOf(items: readonly IntakeItem[], picks: Record<string, Pick>): IntakeSelection[] {
  const out: IntakeSelection[] = [];
  for (const it of items) {
    const p = picks[it.id] ?? emptyPick;
    if (it.type === "match" || !ready(it, p)) continue;
    const months = Object.fromEntries(Object.entries(p.months).filter(([, v]) => v !== ""));
    if (it.type === "signal") out.push({ id: it.id, confirm: true });
    else if (it.type === "conflict") out.push({ id: it.id, choices: p.choices, ...(Object.keys(months).length ? { months } : {}) });
    else out.push({ id: it.id, ...(Object.keys(months).length ? { months } : {}) });
  }
  return out;
}
