import { pageFor } from "../../pages";
import type { BoardEntry, EntryFix, NeedsYouItem } from "../../wire";

export const KINDS = ["apply", "scan", "evaluate", "documents", "agent", "task"] as const;

export const STATUSES: Record<string, string> = {
  working: "Running",
  "needs-you": "Needs you",
  blocked: "Blocked",
  ok: "Succeeded",
  failed: "Failed",
  cancelled: "Cancelled",
};

// statusOf is the filter value an entry answers to.
export function statusOf(e: BoardEntry): string {
  if (e.state !== "done") return e.state;
  if (e.outcome === "ok") return "ok";
  if (e.outcome === "cancelled") return "cancelled";
  return "failed";
}

export function stateLabel(e: BoardEntry): string {
  if (e.state === "done") {
    switch (e.outcome) {
      case "ok":
        return "Succeeded";
      case "cancelled":
        return "Cancelled";
      case "interrupted":
        return "Interrupted";
      default:
        return "Failed";
    }
  }
  return STATUSES[e.state] ?? e.state;
}

export function tone(e: BoardEntry): "good" | "warn" | "bad" | "neutral" {
  switch (statusOf(e)) {
    case "ok":
      return "good";
    case "needs-you":
      return "warn";
    case "blocked":
    case "failed":
      return "bad";
  }
  return "neutral";
}

export function durationLabel(fromIso: string, toIso: string | undefined, now: number = Date.now()): string {
  const ms = Math.max(0, (toIso ? Date.parse(toIso) : now) - Date.parse(fromIso));
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

// waitedLabel is how long something has waited, rounded the way a person
// says it: "just now", "12 min", "4 h", "3 days".
export function waitedLabel(seconds: number): string {
  if (seconds < 90) return "just now";
  const m = Math.round(seconds / 60);
  if (m < 90) return `${m} min`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h`;
  return `${Math.round(h / 24)} days`;
}

export const WEEK_MS = 7 * 24 * 3600 * 1000;

// dayGroup names the day a finished run belongs to on the board: today,
// yesterday, or earlier in the 7-day window, by the local calendar.
export function dayGroup(iso: string, now: number = Date.now()): string {
  const day = (t: number) => {
    const d = new Date(t);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  };
  const days = Math.round((day(now) - day(Date.parse(iso))) / (24 * 3600 * 1000));
  if (days <= 0) return "Today";
  if (days === 1) return "Yesterday";
  return "Earlier this week";
}

const pad = (n: number) => String(n).padStart(2, "0");
const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// clockIn is a time inside a day group that already names the day: the time
// alone under Today and Yesterday, the weekday and time under Earlier this week.
export function clockIn(iso: string, group: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return group === "Today" || group === "Yesterday" ? hm : `${WEEKDAYS[d.getDay()]} ${hm}`;
}

// Stage words: each run's stages as the user would say them; an unknown
// stage is its own word. The tracker record and the learning step
// run after the outcome is decided, so a run that stopped or finished never
// names them as where it stands.
const STAGES: Record<string, { name: string; doing: string }> = {
  preflight: { name: "Setup check", doing: "checking the setup" },
  resolve: { name: "Find the form", doing: "opening the posting" },
  account: { name: "Sign-in", doing: "signing in" },
  fill: { name: "Fill", doing: "filling the form" },
  check: { name: "Check", doing: "checking the form" },
  fix: { name: "Fix", doing: "correcting answers" },
  submit: { name: "Submit", doing: "submitting" },
  confirm: { name: "Confirmation", doing: "reading the confirmation" },
  record: { name: "Tracker", doing: "updating the tracker" },
  learn: { name: "Learning", doing: "saving what it learned" },
  // An evaluation's phases (internal/evaluate Phase*) and a document run's
  // stages (internal/documents Stage*); "queued" is any start that waits.
  queued: { name: "Queued", doing: "waiting to start" },
  reading: { name: "Read the posting", doing: "reading the posting" },
  gating: { name: "Filters", doing: "checking your filters" },
  screening: { name: "Quick screen", doing: "screening the posting" },
  evaluating: { name: "Evaluate", doing: "evaluating the posting" },
  writing: { name: "Report", doing: "writing the report" },
  retrieve: { name: "Evidence", doing: "gathering your evidence" },
  profile: { name: "Profile", doing: "reading your profile" },
  manifest: { name: "Plan", doing: "planning the documents" },
  draft: { name: "Draft", doing: "drafting" },
  review: { name: "Review", doing: "reviewing the draft" },
  revise: { name: "Revise", doing: "revising the draft" },
  compile: { name: "PDF", doing: "making the PDF" },
  verify: { name: "Verify", doing: "checking the PDF" },
  publish: { name: "Save", doing: "saving the documents" },
};
const AFTER_OUTCOME = new Set(["record", "learn"]);

// stageName is a stage's name in a stage timeline.
export const stageName = (stage: string) => STAGES[stage]?.name ?? stage.replace(/[-_]+/g, " ");

// stageDoing is what a run was doing at its stage ("filling the form"), or ""
// when the stage says nothing to the user.
export function stageDoing(stage: string | undefined, working = false): string {
  if (!stage || (!working && AFTER_OUTCOME.has(stage))) return "";
  return STAGES[stage]?.doing ?? stage.replace(/[-_]+/g, " ").toLowerCase();
}

// summaryLine is the drawer's first sentence: how long the run worked, where
// it stopped, and how long it has waited since.
export function summaryLine(e: BoardEntry, now: number = Date.now()): string {
  if (e.state === "working") {
    const doing = stageDoing(e.stage, true);
    return `Running for ${durationLabel(e.startedAt, undefined, now)}${doing ? `, ${doing}` : ""}.`;
  }
  const doing = stageDoing(e.stage);
  const where = doing ? ` while ${doing}` : "";
  if (e.state === "needs-you" || e.state === "blocked") {
    const worked = durationLabel(e.startedAt, e.stateAt, now);
    const since = durationLabel(e.stateAt, undefined, now);
    return `Worked for ${worked}, then stopped${where}. ${e.state === "blocked" ? "Blocked" : "Waiting for you"} for ${since}.`;
  }
  const worked = durationLabel(e.startedAt, e.finishedAt, now);
  return `${stateLabel(e)} after ${worked}${where && e.outcome !== "ok" ? `, stopped${where}` : ""}.`;
}

// Plain is a run's machine detail said in words: one line for a list, the
// groups of questions or answers behind it for the drawer, and the original
// text ("" when nothing was rewritten) for a disclosure.
interface PlainGroup {
  heading: string;
  items: string[];
}
export interface Plain {
  line: string;
  groups: PlainGroup[];
  raw: string;
}

const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;

// The failure words of the submit readiness check (internal/gates/review.go,
// failureWords), each with how the user is told about it.
const CHECKS: { words: string; heading: (n: number) => string; line: (n: number) => string }[] = [
  { words: "required and not answered", heading: (n) => `${count(n, "question")} need${n === 1 ? "s" : ""} your answer`, line: (n) => `Answer ${count(n, "question")}` },
  { words: "does not match your profile", heading: (n) => `${count(n, "answer")} ${n === 1 ? "doesn't" : "don't"} match your profile`, line: (n) => `Check ${count(n, "answer")} against your profile` },
  { words: "the page does not hold the value that was written", heading: (n) => `${count(n, "answer")} didn't stay on the page`, line: (n) => `Check ${count(n, "answer")} the page didn't keep` },
  { words: "looks wrong", heading: (n) => `${count(n, "answer")} look${n === 1 ? "s" : ""} wrong`, line: (n) => `Check ${count(n, "answer")} that look${n === 1 ? "s" : ""} wrong` },
];

// unquote reads a Go %q string body.
function unquote(s: string): string {
  return s.replace(/\\(u[0-9a-fA-F]{4}|x[0-9a-fA-F]{2}|.)/g, (_, c: string) => {
    if (c.length > 1) return String.fromCharCode(parseInt(c.slice(1), 16));
    return c === "n" || c === "t" ? " " : c;
  });
}

const QUOTED = /"((?:[^"\\]|\\.)*)"/g;
// A failure's words may be followed by a note in brackets (the opposite answer, a value the site filled in itself, a flag's reason).
const FAILURE = /page \d+ "((?:[^"\\]|\\.)*)": ([a-z ]+?)(?= \([^)]*\)|;|$)/g;

// clipLabel shortens a question for a one-line list, at a word where one is
// near the limit.
function clipLabel(s: string, max = 40): string {
  const t = s.trim();
  if (t.length <= max) return t;
  const cut = t.slice(0, max + 1);
  const space = cut.lastIndexOf(" ");
  return `${(space > max / 2 ? cut.slice(0, space) : t.slice(0, max)).replace(/[\s,.;:?!]+$/, "")}…`;
}

// preview names the first two items of a list and counts the rest.
function preview(items: string[], total = items.length): string {
  const shown = items.slice(0, 2).map((i) => clipLabel(i));
  const rest = total - shown.length;
  return rest > 0 ? `${shown.join(", ")} and ${rest} more` : shown.join(", ");
}

// Error chains as one sentence each, first match wins. A chain names its
// outermost failure first and its cause last, so the most specific shapes come
// first.
const ERRORS: [RegExp, string][] = [
  [/tab creation timed out/, "The browser didn't open the tab in time."],
  [/operation-timeout/, "The browser didn't answer in time."],
  [/network-offline/, "The computer is offline."],
  [/lane-tab-limit/, "The apply browser has too many tabs open."],
  [/tab-closed/, "The tab was closed while the run was using it."],
  [/no apply window is attached/, "The apply browser isn't open."],
  [/the apply tab could not be opened/, "The browser couldn't open the apply tab."],
  [/^posting closed/, "The posting is closed."],
  [/^stale link/, "The link now shows a different role than the one evaluated."],
  [/the site is (not answering|down for a while)/, "The site isn't answering right now."],
  [/the page loaded blank/, "The page loaded blank."],
  [/the posting could not be opened/, "The posting couldn't be opened."],
  [/verification challenge/, "The site showed a verification check."],
  [/^preflight blocked/, "The setup check stopped the run before it started."],
  [/^cancelled/, "The run was cancelled."],
  [/the browser failed during the walk/, "The browser failed while filling the form."],
  [/^check: not an application/, "The page wasn't an application form."],
  [/no control was read on any page/, "Nothing on the pages could be read back, so nothing is verified."],
  [/the page could not be re-read/, "A page couldn't be read back, so nothing on it is verified."],
  [/the page names a validation problem/, "The site refused the form with a validation problem."],
  [/submit clicked; no verdict could be read/, "Submit was pressed, but the result couldn't be read."],
  [/^pressed submit; the confirmation is not read yet/, "Submitted; the confirmation hasn't been read yet."],
  [/^filled; submit waits for your confirmation/, "Filled the form; Submit waits for your confirmation."],
  [/context deadline exceeded/, "The run ran out of time."],
  [/context canceled/, "The run was stopped before it finished."],
];

// What a board reason means when the detail says nothing more.
const REASONS: Record<string, string> = {
  answer: "Some questions need your answer.",
  "sign-in": "The site needs you to sign in.",
  challenge: "The site asks for a verification check.",
  review: "The filled form waits for your review.",
  "screen-review": "The quick screen could not decide on this posting.",
  "one-time-code": "The site sent a one-time code to enter.",
  "may-have-submitted": "The application may already have gone through.",
  confirmation: "Submit waits for your confirmation.",
  "credential-refused": "The site refused the saved password.",
  lockout: "The site locked the account.",
  "lane-unavailable": "The apply browser wasn't available.",
};

// What the user does next for each board reason.
const NEXT: Record<string, string> = {
  answer: "Answer these questions in the kept tab, or save the answers in the Answer Bank and retry.",
  "sign-in": "Sign in to the site in the kept tab, then retry.",
  challenge: "Finish the site's verification check in the kept tab.",
  "one-time-code": "Enter the code the site emailed you in the kept tab.",
  review: "Review the filled form in the kept tab.",
  "screen-review": "Open the report, then evaluate the posting in full or skip it.",
  "may-have-submitted": "Check the site or your email for a receipt before you retry.",
  "credential-refused": "Update the saved password for this site, then retry.",
  lockout: "Wait for the site to unlock the account, or reset the password on the site.",
  "lane-unavailable": "Retry the run as it is; it asks the app's browser for a new tab.",
};

// A review an evaluation waits on is a quick screen's, not a filled form's.
const reasonKey = (reason: string, kind?: string) => (kind === "evaluate" && reason === "review" ? "screen-review" : reason);

// The steps name an application's kept tab, so only an application run, and a
// quick screen's review, is told one; another kind's item leads to its page.
export function nextStep(reason: string | undefined, kind: string): string {
  const key = reasonKey(reason ?? "", kind);
  return kind === "apply" || key === "screen-review" ? (NEXT[key] ?? "") : "";
}

// fixOf is where a waiting entry is resolved: the fix the server names, else
// the page the entry links to when that is not its own run (a Gmail reconnect,
// a schedule's need, a hiring signal). linked says it came from the link.
export function fixOf(e: BoardEntry): (EntryFix & { linked?: boolean }) | undefined {
  if (e.fix || e.link.startsWith("/runs")) return e.fix;
  const page = pageFor(e.link.split(/[?#]/)[0]);
  return page && { to: e.link, label: `Open ${page.title}`, linked: true };
}

// dedupeChain drops a run of ": "-separated parts that repeats the run just
// before it ("a: b: a: b: c" reads "a: b: c").
function dedupeChain(text: string): string {
  const parts = text.split(": ");
  for (let k = Math.floor(parts.length / 2); k >= 1; k--) {
    for (let i = 0; i + 2 * k <= parts.length; ) {
      if (parts.slice(i, i + k).join(": ") === parts.slice(i + k, i + 2 * k).join(": ")) parts.splice(i + k, k);
      else i++;
    }
  }
  return parts.join(": ");
}

function sentence(text: string): string {
  const t = text.trim().replace(/^(check|fill): /, "");
  if (!t) return t;
  const s = t[0].toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

// plainDetail says a run's detail in words. It reads the shapes the
// application chain writes (internal/apply: the readiness check's failures,
// the unanswered rows of a disabled Submit, browser error chains) and says any
// other text as one sentence with its repeats removed. The reason fills in
// when there is no detail.
export function plainDetail(detail: string | undefined, reason?: string, kind?: string): Plain | null {
  const text = (detail ?? "").trim();
  if (!text) {
    const said = reason ? REASONS[reasonKey(reason, kind)] : "";
    return said ? { line: said, groups: [], raw: "" } : null;
  }
  const groups: (PlainGroup & { line: string; total: number })[] = [];
  const disabled = /the page keeps Submit disabled and these rows stand unanswered: (.*?)(?: \(and (\d+) more\))?(?:; the walk ended|$)/.exec(text);
  if (disabled) {
    const items = [...disabled[1].matchAll(QUOTED)].map((m) => unquote(m[1]));
    const total = items.length + Number(disabled[2] ?? 0);
    if (items.length > 0) groups.push({ heading: CHECKS[0].heading(total), line: CHECKS[0].line(total), items, total });
  } else {
    const found: { label: string; words: string }[] = [...text.matchAll(FAILURE)].map((m) => ({ label: unquote(m[1]).trim(), words: m[2] }));
    // The board keeps 300 characters of a detail and ends a cut one with "…",
    // so the last failure may lack its words or the end of its label. It joins
    // the group its words begin, else the one before it.
    const cut = text.endsWith("…") ? /page \d+ "((?:[^"\\]|\\.)*)(?:"(?:: ([a-z ]*))?)?…$/.exec(text) : null;
    if (cut && found.length > 0 && !found.some((f) => f.label === unquote(cut[1]).trim())) {
      const words = CHECKS.find((c) => cut[2] && c.words.startsWith(cut[2].trim()))?.words ?? found[found.length - 1].words;
      found.push({ label: `${unquote(cut[1]).trim()}${cut[0].includes('"…') || cut[2] !== undefined ? "" : "…"}`, words });
    }
    for (const c of CHECKS) {
      const items = [...new Set(found.filter((f) => f.words === c.words).map((f) => f.label))];
      if (items.length > 0) groups.push({ heading: c.heading(items.length), line: c.line(items.length), items, total: items.length });
    }
  }
  if (groups.length > 0) {
    const [top] = groups;
    return { line: `${top.line}: ${preview(top.items, top.total)}`, groups: groups.map(({ heading, items }) => ({ heading, items })), raw: text };
  }
  // The sign-in stop's last clause names the gate, not the user's next move.
  const chain = dedupeChain(text).replace(/; account creation needs a (?:submitting )?run(?: you started)?/, ". An account is only created in a run you start yourself");
  const lower = chain.toLowerCase();
  const known = ERRORS.find(([re]) => re.test(lower));
  const line = known ? known[1] : sentence(chain.split("; the walk ended")[0]);
  // Text that only gained a capital and a full stop needs no disclosure.
  const same = (t: string) => t.toLowerCase().replace(/\.$/, "");
  return { line, groups: [], raw: same(line) === same(text) ? "" : text };
}

// repeatKey is what repeats of one stop share; a running run repeats nothing.
const repeatKey = (e: BoardEntry) => (e.state === "working" ? e.id : `${e.kind}|${e.title}|${e.state}|${e.reason ?? ""}`);

// groupRepeats folds the runs of one posting stopped for one reason into one
// group, newest first, keeping the order in which the groups first appear: the
// inbox, Home and Apply Next show each group as one row.
export function groupRepeats<T>(items: T[], entryOf: (t: T) => BoardEntry): T[][] {
  const out = new Map<string, T[]>();
  for (const it of items) {
    const key = repeatKey(entryOf(it));
    out.set(key, [...(out.get(key) ?? []), it]);
  }
  return [...out.values()].map((g) => [...g].sort((a, b) => Date.parse(entryOf(b).stateAt) - Date.parse(entryOf(a).stateAt)));
}

// WaitingGroup is one row of the inbox: the repeats of one waiting run.
export interface WaitingGroup {
  key: string;
  items: NeedsYouItem[];
}

export const groupWaiting = (items: NeedsYouItem[]): WaitingGroup[] => groupRepeats(items, (i) => i.entry).map((g) => ({ key: repeatKey(g[0].entry), items: g }));
