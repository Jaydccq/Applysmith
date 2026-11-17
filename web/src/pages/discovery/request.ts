import type { ScanRequest } from "../../wire";

// Draft holds the form as text, so an empty field is "not set" and a bad
// number can be named before anything is sent.
export interface Draft {
  search: string; title: string; city: string; state: string; url: string; scope: string; postedWithin: string; limit: string; sort: string; mode: string;
  topN: string; budgetMinutes: string; pages: string; fn: string; seniority: string; employment: string;
  remote: boolean; easyApplyOnly: boolean; retryKnown: boolean; ignoreCooldown: boolean; force: boolean;
}

export const EMPTY: Draft = {
  search: "", title: "", city: "", state: "", url: "", scope: "", postedWithin: "", limit: "", sort: "", mode: "", topN: "", budgetMinutes: "", pages: "", fn: "", seniority: "",
  employment: "", remote: false, easyApplyOnly: false, retryKnown: false, ignoreCooldown: false, force: false,
};

const RANGES: { key: "limit" | "topN" | "pages" | "budgetMinutes"; label: string; max: number }[] = [
  { key: "limit", label: "Results per search", max: 1000 },
  { key: "topN", label: "Top N", max: 500 },
  { key: "pages", label: "Result pages", max: 50 },
  { key: "budgetMinutes", label: "Time budget", max: 360 },
];

// toRequest builds the body of POST /api/scans from the form, or says which
// field is wrong.
export function toRequest(source: string, d: Draft): { body?: ScanRequest; error?: string } {
  const body: ScanRequest = { source };
  const nums: Record<string, number> = {};
  for (const r of RANGES) {
    const raw = d[r.key].trim();
    if (raw === "") continue;
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 0 || n > r.max) return { error: `${r.label} must be a whole number from 0 to ${r.max}.` };
    nums[r.key] = n;
  }
  for (const k of ["search", "title", "city", "state", "url", "scope", "postedWithin", "sort", "mode", "fn", "seniority", "employment"] as const) {
    const v = d[k].trim();
    if (v) body[k] = v;
  }
  if (nums.limit !== undefined) body.limit = nums.limit;
  if (nums.topN !== undefined) body.topN = nums.topN;
  if (nums.pages !== undefined) body.pages = nums.pages;
  if (nums.budgetMinutes !== undefined) body.budgetSeconds = nums.budgetMinutes * 60;
  for (const k of ["remote", "easyApplyOnly", "retryKnown", "ignoreCooldown", "force"] as const) if (d[k]) body[k] = true;
  return { body };
}
