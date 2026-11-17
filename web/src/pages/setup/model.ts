import type { ApiError } from "../../api/client";
import type { DoctorResult, SetupField, SetupReason, SetupState, SetupStep } from "../../wire";

export type Tone = "good" | "warn" | "bad" | "neutral";

// The statuses a detector can return, with the tone each one is drawn in. A
// step not started is plain "To do", not an error: a new workspace has a dozen.
export const STATUS_TONE: Record<string, Tone> = { complete: "good", skipped: "neutral", partial: "warn", template: "warn", missing: "neutral" };
const STATUS_WORD: Record<string, string> = { complete: "Done", skipped: "Skipped", partial: "Partly done", template: "Example values", missing: "To do" };
export const statusWord = (status: string): string => STATUS_WORD[status] ?? status;

// The topics a setup screen depends on: the writers, the connections the
// needs-a-person steps wait for, and the data the detectors read.
export const SETUP_TOPICS = ["setup", "gmail", "configuration", "answerbank", "schedules", "notifications", "browser"] as const;

// What a step screen is handed. reload re-reads the state after a write.
export interface ScreenProps {
  step: SetupStep;
  state: SetupState;
  reload: () => void;
}

export type Values = Record<string, unknown>;

export const text = (v: unknown): string => (typeof v === "string" ? v : v === null || v === undefined ? "" : String(v));

// listFromText splits a typed list. A list with a line break is split on the
// breaks only, so a place such as "Austin, TX" stays whole.
export function listFromText(s: string): string[] {
  return (s.includes("\n") ? s.split(/\r?\n/) : s.split(",")).map((x) => x.trim()).filter(Boolean);
}

type Encoded = { skip: true } | { value: unknown } | { error: string };

// encodeField turns what a form holds into what the writer takes. An empty
// field is skipped, because the writer keeps a stored value unless a new one
// is supplied.
function encodeField(f: SetupField, v: unknown): Encoded {
  switch (f.type) {
    case "bool":
      return typeof v === "boolean" ? { value: v } : { skip: true };
    case "int": {
      const s = text(v).trim();
      if (s === "") return { skip: true };
      const n = Number(s);
      return Number.isInteger(n) ? { value: n } : { error: "Enter a whole number." };
    }
    case "string-list": {
      // An array is a deliberate answer, so an empty one is sent: it clears a list.
      if (Array.isArray(v)) return { value: v.map(text) };
      const list = listFromText(text(v));
      return list.length > 0 ? { value: list } : { skip: true };
    }
    case "object":
    case "object-list": {
      if (typeof v === "string") {
        const s = v.trim();
        if (s === "") return { skip: true };
        try {
          return { value: JSON.parse(s) as unknown };
        } catch {
          return { error: "This is not valid JSON." };
        }
      }
      if (Array.isArray(v)) return v.length > 0 ? { value: v } : { skip: true };
      if (v !== null && typeof v === "object") return Object.keys(v).length > 0 ? { value: v } : { skip: true };
      return { skip: true };
    }
    default: {
      const s = text(v).trim();
      return s === "" ? { skip: true } : { value: s };
    }
  }
}

const same = (a: unknown, b: unknown) => JSON.stringify(a ?? "") === JSON.stringify(b ?? "");

// encodeInput builds the writer input from the fields that changed since the
// form was loaded, so a stored value the person did not touch is never sent
// again.
export function encodeInput(fields: readonly SetupField[], values: Values, base: Values): { input: Record<string, unknown>; errors: Record<string, string[]> } {
  const input: Record<string, unknown> = {};
  const errors: Record<string, string[]> = {};
  for (const f of fields) {
    if (same(values[f.name], base[f.name])) continue;
    const e = encodeField(f, values[f.name]);
    if ("error" in e) errors[f.name] = [e.error];
    else if ("value" in e) input[f.name] = e.value;
  }
  return { input, errors };
}

// topField is the input field a server path such as roles[0].title belongs to.
const topField = (path: string): string => path.split(/[.[]/, 1)[0];

// subLabel reads the part of a path below its input field as words:
// compensation.minimum is "Minimum", roles[0].title is "Item 1, title".
const subLabel = (path: string, top: string): string => {
  const s = path
    .slice(top.length)
    .replace(/\[(\d+)\]/g, (_, n: string) => ` item ${Number(n) + 1},`)
    .replace(/\./g, " ")
    .replace(/,\s*$/, "")
    .trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
};

// rejection reads the per-field answer of a 422. A message with no field to put
// it beside is returned as general.
export function rejection(e: ApiError): { byField: Record<string, string[]>; general: string } {
  const byField: Record<string, string[]> = {};
  const put = (path: string, message: string) => {
    const top = topField(path);
    (byField[top] ??= []).push(path === top ? message : `${subLabel(path, top)}: ${message}`);
  };
  if (e.kind === "validation_failed") {
    for (const [path, message] of Object.entries(e.details?.fields ?? {})) put(path, message);
    if (Object.keys(byField).length === 0 && e.details?.field) put(e.details.field, e.message);
  }
  return { byField, general: Object.keys(byField).length > 0 ? "Some answers need another look." : e.message };
}

type ReasonGroup = { field: string; items: SetupReason[] } | { reason: SetupReason };

// groupReasons collects reasons that share a field, which is how a group of
// answer-bank entries arrives, and leaves a lone reason as it is.
export function groupReasons(reasons: readonly SetupReason[]): ReasonGroup[] {
  const count = new Map<string, number>();
  for (const r of reasons) if (r.field) count.set(r.field, (count.get(r.field) ?? 0) + 1);
  const out: ReasonGroup[] = [];
  const seen = new Map<string, { field: string; items: SetupReason[] }>();
  for (const r of reasons) {
    if (!r.field || (count.get(r.field) ?? 0) < 2) {
      out.push({ reason: r });
      continue;
    }
    let g = seen.get(r.field);
    if (!g) {
      g = { field: r.field, items: [] };
      seen.set(r.field, g);
      out.push(g);
    }
    g.items.push(r);
  }
  return out;
}

// blockingEntry reads "<description> (<entry-id>)": the id is the text inside
// the last parentheses and holds no space.
export function blockingEntry(r: SetupReason): { id: string; description: string } | null {
  const m = /^(.*?)\s*\(([^()\s]+)\)\s*$/.exec(r.text);
  return m && m[1] !== "" ? { id: m[2], description: m[1] } : null;
}

export const doctorGroup = (r: DoctorResult): string => r.id.split(".")[0];

export const phaseLabel = (phase: string): string => phase.charAt(0).toUpperCase() + phase.slice(1).replaceAll("-", " ");

export const SENIORITY = ["intern", "junior", "mid", "senior", "lead", "staff", "principal", "manager", "director", "vp", "executive"] as const;
