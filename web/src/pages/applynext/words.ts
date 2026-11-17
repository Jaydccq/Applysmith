// What an apply state, a stop and its owner say in words. Shared by Apply Next
// and Runs so a row and its run never describe the same stop differently.
import type { ApplyOutcome } from "../../wire";
import { plainDetail, stageDoing } from "../runs/format";

type Tone = "good" | "warn" | "bad" | "neutral";

export const STATE_ORDER = ["running", "queued", "needs-user", "failed", "submitted", "skipped", "idle"] as const;

const STATES: Record<string, { label: string; tone: Tone }> = {
  idle: { label: "Not started", tone: "neutral" },
  queued: { label: "Queued", tone: "neutral" },
  running: { label: "Running", tone: "neutral" },
  "needs-user": { label: "Needs you", tone: "warn" },
  submitted: { label: "Submitted", tone: "good" },
  skipped: { label: "Skipped", tone: "neutral" },
  failed: { label: "Failed", tone: "bad" },
};

export const stateWords = (state: string) => STATES[state] ?? { label: state, tone: "neutral" as Tone };

// A posting under the reserved .example domain, as the demo's samples are, can
// never load, so neither Apply Next nor its report offers to autofill it.
export const sampleHost = (host = "") => /\.example$/.test(host);
export const SAMPLE = "A sample posting: its site does not exist, so autofill cannot open it.";

// Who has the next step of a stop, in words the user can act on. A stop the
// user owns says nothing here: the stop itself names what to do.
const OWNERS: Record<string, string> = {
  user: "",
  posting: "The posting is the problem (closed or unavailable); there is nothing to fix on your side.",
  environment: "The machine or the network was not ready; a retry may work.",
  defect: "Applysmith did something wrong; this is a defect to fix, not a step for you.",
};

export const ownerWords = (owner: string | undefined) => (owner ? (OWNERS[owner] ?? owner) : "");

// The heading of a group of stopped rows that share an owner.
const OWNER_TITLES: Record<string, string> = { user: "Waiting for you", posting: "Posting problems", environment: "Environment", defect: "Defects" };

export const ownerTitle = (owner: string) => OWNER_TITLES[owner] ?? owner;

const BATCH_ROW: Record<string, string> = { pending: "Waiting its turn", started: "Running", ended: "Finished" };

export const batchRowWords = (state: string) => BATCH_ROW[state] ?? state;

const CLASSES: Record<string, string> = {
  environment: "environment",
  unavailable: "site unavailable",
  credential: "sign-in",
  posting: "posting",
  "answer-gap": "unanswered question",
  verification: "verification code",
  defect: "defect",
};

// outcomeLine is the one-line result of a run: what it did, where it stopped
// and, unless the caller shows it apart, why in words (plainDetail). The
// reason never holds a form value.
export function outcomeLine(o: ApplyOutcome, withReason = true): string {
  if (o.kind === "submitted") return o.confirmed ? "Submitted and confirmed." : "Submitted, receipt not confirmed yet.";
  if (o.kind === "filled") return "Filled, nothing submitted.";
  if (o.kind !== "stopped") return o.kind === "running" ? "Running." : o.kind;
  const doing = stageDoing(o.stage);
  const where = doing ? ` while ${doing}` : "";
  const cls = o.class ? ` (${CLASSES[o.class] ?? o.class})` : "";
  const why = withReason ? plainDetail(o.reason)?.line : "";
  return `Stopped${where}${cls}.${why ? ` ${why}` : ""}`;
}

// durationMs is a stage length for a timeline: under a second in ms, then s,
// then m.
export function durationMs(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)} s`;
  return `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`;
}
