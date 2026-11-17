import type { ScanRecord } from "../../wire";
import { humanize } from "../../ui/parts";

export type Tone = "good" | "warn" | "bad" | "neutral";

export const STATUSES = ["running", "completed", "skipped", "blocked", "needs-sign-in", "failed", "interrupted", "cancelled"] as const;

// SCOPES are the location scopes a scan reads (internal/discovery/scope.go),
// each with its name.
export const SCOPES: [string, string][] = [["us", "United States"], ["canada", "Canada"], ["uk", "United Kingdom"], ["europe", "Europe"], ["asia-pacific", "Asia-Pacific"], ["worldwide", "Worldwide"]];

// scanLabel is a scan status in words; "needs-sign-in" reads as a source's
// readiness does (internal/api discovery_views.go), "Needs sign-in".
export const scanLabel = (status: string) => (status === "needs-sign-in" ? "Needs sign-in" : humanize(status));

export function scanTone(status: string): Tone {
  switch (status) {
    case "completed":
      return "good";
    case "failed":
      return "bad";
    case "blocked":
    case "needs-sign-in":
    case "skipped":
    case "interrupted":
    case "cancelled":
      return "warn";
  }
  return "neutral";
}

export function readinessTone(readiness: string): Tone {
  return readiness === "ready" ? "good" : readiness === "off" ? "neutral" : "warn";
}

const sum = (m: Record<string, number>) => Object.values(m).reduce((a, b) => a + b, 0);

// countsLine is what became of the postings a scan fetched. Every one ends in
// exactly one of new, already known, filtered out or undated
// (discovery.Counts), so the parts add up to the fetched number.
export function countsLine(r: ScanRecord): string {
  const c = r.counts;
  if (c.discovered === 0) return r.status === "running" ? "Nothing fetched yet" : "Nothing fetched";
  const parts = [`${c.inserted} new${c.queued ? ` (${c.queued} sent to evaluation)` : ""}`];
  const known = sum(c.duplicates);
  const filtered = sum(c.filtered);
  if (known) parts.push(`${known} already known`);
  if (filtered) parts.push(`${filtered} filtered out`);
  if (c.undated) parts.push(`${c.undated} with no readable date`);
  return `${c.discovered} fetched: ${parts.join(", ")}`;
}

// The reason codes of filtered and known postings in internal/discovery, in
// plain words; an unknown code is shown humanized.
const DROPPED: Record<string, string> = {
  title: "by title",
  location: "by location",
  scope: "outside the location scope",
  "blocked-company": "blocked companies",
  "invalid-url": "bad links",
  "non-posting-url": "not posting links",
  "over-limit": "over the posting limit",
  sponsorship: "no visa sponsorship",
  stale: "too old",
  "dead-link": "dead links",
  "lead-gate": "stopped by the lead gate",
  "not-hiring": "not hiring posts",
  "no-hiring-word": "not hiring posts",
  inbox: "in the inbox",
  tracker: "in the tracker",
  report: "already reported",
  archive: "archived",
  history: "seen by an earlier scan",
  run: "repeated in this scan",
};

const listed = (m: Record<string, number>) =>
  Object.entries(m)
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${n} ${DROPPED[k] ?? humanize(k).toLowerCase()}`)
    .join(", ");

// droppedLine says why the postings that are not new were left out, by reason.
export function droppedLine(r: ScanRecord): string {
  const out = [listed(r.counts.filtered) && `Filtered out: ${listed(r.counts.filtered)}.`, listed(r.counts.duplicates) && `Already known: ${listed(r.counts.duplicates)}.`];
  return out.filter(Boolean).join(" ");
}

export function elapsed(r: ScanRecord, now: number = Date.now()): string {
  const ms = r.finishedAt ? r.durationMs : Math.max(0, now - Date.parse(r.startedAt));
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  return m < 60 ? `${m}m ${s % 60}s` : `${Math.floor(m / 60)}h ${m % 60}m`;
}

// REASONS are the scan end codes of internal/discovery in plain words.
const REASONS: Record<string, string> = {
  cooldown: "cooling down after a blocked run; wait, or tick Ignore the cooldown in its options",
  "host-pressure": "the computer was short of memory; close some apps and start it again",
  "host-offline": "the network was unreachable; start it again once you are online",
  "needs-sign-in": "needs a sign-in",
  "layout-changed": "the page layout changed",
  "no-cards": "the page listed no jobs",
  "empty-window": "nothing new in the time window",
  "empty-extraction": "the page loaded but nothing could be read",
  "budget-reached": "stopped at its time budget",
  "limit-reached": "stopped at its posting limit",
  "frontier-reached": "reached postings it had already seen",
  "no-filter": "the search needs a filter; set the search text or title in its options",
  overlay: "a dialog covered the page; close it on the Browser page and start again",
  interrupted: "interrupted",
  cancelled: "cancelled",
};

// why gives the reason a scan did not complete in words a person can act on.
// Exa's spend cap ends its run with the code of the time budget
// (discovery.Record.SpendCapped), so it is worded here.
export function why(r: ScanRecord): string {
  const reason = r.source === "exa" && r.reason === "budget-reached" ? "stopped at its spend cap; the searches left run first next time" : r.reason && (REASONS[r.reason] ?? r.reason);
  return [reason, r.error].filter(Boolean).join(": ");
}
