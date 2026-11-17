// Clean-room: docs/provenance.md#CR-ENGINE
// A draft an earlier run left behind: a prose box that holds exactly one bare
// profile value its question does not ask for (a degree in "Summary") is
// cleared. One-line inputs and Yes/No questions are never touched, and neither
// is a box the person wrote in.

import { normalize } from "./dom";
import { clearText } from "./gestures";
import { readInfo, scanControls } from "./scan";
import type { Run } from "./run";

/** Profile fields as the words a question would use to ask for them. */
const FIELD_WORDS: [string, RegExp][] = [
  ["fullName", /\bname\b/],
  ["firstName", /\bname\b/],
  ["lastName", /\bname\b/],
  ["email", /\be ?mail\b/],
  ["phone", /\b(phone|mobile|cell|telephone)\b/],
  ["line1", /\b(address|street)\b/],
  ["city", /\b(city|town|location)\b/],
  ["state", /\b(state|province|region)\b/],
  ["postalCode", /\b(zip|postal)\b/],
  ["school", /\b(school|universit\w*|college|education|institution)\b/],
  ["degree", /\b(degree|education|qualification)\b/],
  ["major", /\b(major|field|study|discipline)\b/],
  ["company", /\b(company|employer|organi[sz]ation|experience|employment)\b/],
  ["title", /\b(title|position|role|experience)\b/],
];

function profileValues(run: Run): Map<string, string[]> {
  const a = run.applicant;
  const out = new Map<string, string[]>();
  const put = (key: string, v: unknown) => {
    if (typeof v !== "string" || !v.trim()) return;
    const n = normalize(v);
    out.set(n, [...(out.get(n) ?? []), key]);
  };
  if (!a) return out;
  const c = a.candidate;
  for (const [k, v] of Object.entries({ fullName: c.fullName, firstName: c.firstName, lastName: c.lastName, email: c.email, phone: c.phone, line1: c.address?.line1, city: c.address?.city, state: c.address?.state, postalCode: c.address?.postalCode })) put(k, v);
  for (const e of a.history?.education ?? []) for (const [k, v] of Object.entries({ school: e.school, degree: e.degree, major: e.field })) put(k, v);
  for (const e of a.history?.employment ?? []) for (const [k, v] of Object.entries({ company: e.employer, title: e.title })) put(k, v);
  return out;
}

/** Clears the prose boxes that hold one stale bare profile value; returns how many. */
export function clearStaleProse(run: Run): number {
  const known = profileValues(run);
  let cleared = 0;
  for (const info of scanControls(run.doc)) {
    if (info.kind !== "textarea" || run.edits.edited(info)) continue;
    const value = readInfo(info);
    if (value === "" || value.includes("\n")) continue;
    const keys = known.get(normalize(value));
    if (!keys) continue;
    const asked = normalize(info.label.text);
    // The question names none of that field's words: the value is a leftover, not an answer.
    if (keys.some((k) => FIELD_WORDS.find(([f]) => f === k)?.[1].test(asked))) continue;
    clearText(info.el as HTMLTextAreaElement);
    run.log.add("stale", "cleared", { selector: info.selector });
    cleared++;
  }
  return cleared;
}
