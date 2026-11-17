import { createContext, useContext, useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { ApiError, get, patch } from "../../api/client";
import { configDocument, configSaved } from "../../api/decoders_pages";
import { toYaml } from "../../api/yaml";
import { asApiError, useResource, type Resource } from "../../hooks";
import { Link } from "../../router";
import { Chip, ErrorNote, Panel } from "../../ui/parts";
import type { ConfigDocument } from "../../wire";
import { Changed, SaveBar } from "../../ui/SaveBar";
import { AskMeBox } from "../setup/Form";
import { ASK_ME } from "../answerbank/model";

// The shipped example persona and the reserved addresses. A value that still
// equals one is a placeholder, not the user's own fact. This follows the setup
// writers' rule (internal/onboarding/examples.go).
const PERSONA = new Set(["alex example", "example corp", "example university"]);
const RESERVED_HOSTS = new Set(["example.com", "example.org", "example.net", "localhost"]);
const RESERVED_SUFFIX = [".test", ".example", ".invalid", ".localhost"];

export function isExample(value: unknown): boolean {
  if (typeof value !== "string") return false;
  const v = value.trim().toLowerCase();
  if (v === "") return false;
  if (PERSONA.has(v)) return true;
  const at = v.lastIndexOf("@");
  if (at >= 0) {
    const domain = v.slice(at + 1);
    if (RESERVED_HOSTS.has(domain) || RESERVED_SUFFIX.some((s) => domain.endsWith(s))) return true;
  }
  return /^1?(\d{3})?55501\d\d$/.test(v.replace(/\D/g, ""));
}

export function ExampleMarker({ step }: { step: string }) {
  return (
    <span className="example-mark">
      <Chip tone="warn">Still an example</Chip>
      <Link to={`/setup?step=${step}`}>Replace it in setup</Link>
    </span>
  );
}

// FormSection is one titled part of a profile tab: its title and what it is
// for on the left, its fields on the right, stacked on a narrow screen.
export function FormSection({ title, about, children }: { title: string; about?: string; children: ReactNode }) {
  const id = useId();
  return (
    <section className="form-section" aria-labelledby={id}>
      <div className="form-section-head">
        <h3 id={id}>{title}</h3>
        {about && <p className="muted">{about}</p>}
      </div>
      <div className="form-section-body">{children}</div>
    </section>
  );
}

// valueAt reads a dotted path of the parsed profile.
export function valueAt(values: { [key: string]: unknown } | undefined, path: string): unknown {
  let cur: unknown = values;
  for (const part of path.split(".")) {
    if (cur === null || typeof cur !== "object") return undefined;
    cur = (cur as { [key: string]: unknown })[part];
  }
  return cur;
}

export const text = (v: unknown) => (v === null || v === undefined ? "" : String(v));
export const listOf = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : []);

// defaultSkillYears is what a skill saved with no Years counts as when a form
// asks for its years: one to two years, the owner's default (the answer bank's
// bind.defaultSkillYears).
export const defaultSkillYears = 1.5;
// wholeNumber reads an amount as people type it, with thousands separated by
// commas or spaces ("120,000"); anything else is not a whole number.
export const wholeNumber = (s: string): number | undefined => {
  const t = s.replace(/[\s,_]/g, "");
  return /^\d+$/.test(t) ? Number(t) : undefined;
};
// wholeHint names a typed amount the profile cannot hold before the save,
// which would otherwise store something else.
export const wholeHint = (v: string, eg: string) => (v.trim() === "" || wholeNumber(v) !== undefined ? undefined : `Enter a whole number, such as ${eg}.`);
// payNumber reads a pay amount as people write it, with a leading currency
// sign and a trailing k for thousands ("$150k", "£45", "120,000").
export const payNumber = (s: string): number | undefined => {
  const m = /^[$€£¥]?\s*(.*?)\s*([kK]?)$/.exec(s.trim());
  const n = m ? wholeNumber(m[1]) : undefined;
  return n !== undefined && m?.[2] ? n * 1000 : n;
};
export const payHint = (v: string) => (v.trim() === "" || payNumber(v) !== undefined ? undefined : "Enter a whole amount, such as 120,000 or 150k.");
// amount reads a count that may have a fraction ("1.5" years) as wholeNumber
// does; amountHint names text that is none.
export const amount = (s: string): number | undefined => wholeNumber(s) ?? (/^\d*\.\d+$/.test(s.trim()) ? Number(s.trim()) : undefined);
export const amountHint = (v: string) => (v.trim() === "" || amount(v) !== undefined ? undefined : "Enter a number, such as 3 or 1.5.");
export const rowsOf = (v: unknown): { [key: string]: unknown }[] => (Array.isArray(v) ? (v as { [key: string]: unknown }[]) : []);

export type Op = { op: "set" | "unset" | "append" | "remove"; path: string; value?: string; index?: number };
export const setOp = (path: string, value: unknown): Op => ({ op: "set", path, value: toYaml(value) });
export const unsetOp = (path: string): Op => ({ op: "unset", path });

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

// useDraft holds a form's values over the saved ones. When the saved values
// change under it (a reload after a conflict, a confirmed CV draft entry), a
// field the user has not edited takes its new value and an edited one stays,
// so a save sends only the user's changes and never undoes another write.
export function useDraft<T extends { [key: string]: unknown }>(start: T): [T, (f: T) => void] {
  const [s, setS] = useState({ base: start, f: start });
  let f = s.f;
  if (!same(start, s.base)) {
    f = { ...start };
    for (const k in start) if (!same(s.f[k], s.base[k])) f[k] = s.f[k];
    setS({ base: start, f });
  }
  return [f, (next) => setS({ base: start, f: next })];
}

// useRevisedDraft holds a form's draft over values read from a file with its
// revision. A draft with no edit follows the file. An edited one is kept:
// while the values under it are the ones it started from (another part of the
// file was written) it moves to the new revision, and once they differ it
// stays at the revision the edits were made at, so its save is refused as a
// conflict instead of writing old values over the change. `base` is what the
// edits were made over.
export function useRevisedDraft<T>(revision: string, saved: T) {
  const [s, setS] = useState({ revision, base: saved, draft: saved });
  let cur = s;
  if (revision !== s.revision) {
    if (same(s.draft, s.base) || same(s.draft, saved)) cur = { revision, base: saved, draft: saved };
    else if (same(saved, s.base)) cur = { ...s, revision };
    if (cur !== s) setS(cur);
  }
  const file = useRef({ revision, saved });
  file.current = { revision, saved };
  return {
    draft: cur.draft,
    base: cur.base,
    revision: cur.revision,
    dirty: !same(cur.draft, cur.base),
    setDraft: (draft: T) => setS((p) => ({ ...p, draft })),
    // sent records a draft the server took, so the file read next replaces it.
    sent: (draft: T) => setS((p) => ({ revision: file.current.revision, base: draft, draft: p.draft })),
    // reset drops the edits for what the file holds.
    reset: () => setS({ revision: file.current.revision, base: file.current.saved, draft: file.current.saved }),
    // rebase puts the edits over a newer read of the file, so the next save replaces it.
    rebase: (at: string, over: T) => setS((p) => ({ revision: at, base: over, draft: p.draft })),
  };
}

// A save of profile.yml from one form makes every other form of it on the
// page (Settings keeps its sections mounted) read the file again, so their
// next save quotes the new revision instead of being refused as a change made
// elsewhere. useDraft keeps their unsaved fields.
const profileSaves = new EventTarget();
export const profileSaved = () => void profileSaves.dispatchEvent(new Event("saved"));
export function useProfileSaves(reload: () => void): void {
  useEffect(() => {
    profileSaves.addEventListener("saved", reload);
    return () => profileSaves.removeEventListener("saved", reload);
  }, [reload]);
}

// useProfile loads the profile with its revision.
function useProfile(): [Resource<ConfigDocument>, () => void] {
  const res = useResource(() => get("/api/config/profile", configDocument), []);
  useProfileSaves(res[1]);
  return res;
}

export async function saveProfile(revision: string, ops: Op[]): Promise<void> {
  await patch("/api/config/profile", configSaved, { expectedRevision: revision, ops });
  profileSaved();
}

// ProfilePanel loads the profile and hands the tab its document.
export function ProfilePanel({ title, id, children }: { title: string; id: string; children: (doc: ConfigDocument, reload: () => void) => ReactNode }) {
  const [res, reload] = useProfile();
  return (
    <Panel title={title} id={id} res={res} retry={reload}>
      {(doc) =>
        doc.issues.length > 0 ? (
          <div role="alert" className="note error">
            <p>The profile file has errors, so it cannot be edited here yet:</p>
            <ul>{doc.issues.map((i) => <li key={`${i.path}${i.message}`}>{i.path ? `${i.path}: ` : ""}{i.message}</li>)}</ul>
          </div>
        ) : (
          children(doc, reload)
        )
      }
    </Panel>
  );
}

// A refused save's findings by profile path (details.fields). A control that
// names its path (data-path) owns the findings at that path and inside it.
const Issues = createContext<{ [path: string]: string }>({});
const owns = (field: string, issue: string) => issue === field || issue.startsWith(field + ".") || issue.startsWith(field + "[");

// useIssue gives a control the server's words about its path: the props that
// mark it invalid and tie the words to it, and the words to show beside it.
export function useIssue(path: string | undefined, id: string) {
  const issues = useContext(Issues);
  const said = path ? Object.entries(issues).filter(([p]) => owns(path, p)).map(([, m]) => m).join(" ") : "";
  return {
    props: { "data-path": path, ...(said ? { "aria-invalid": true, "aria-describedby": `${id}-issue` } : {}) },
    note: said && <p id={`${id}-issue`} className="note error compact">{said}</p>,
  };
}

// Issued gives a purpose-built control of a form the props of useIssue, and
// shows the words after it.
export function Issued({ path, children }: { path: string; children: (props: ReturnType<typeof useIssue>["props"]) => ReactNode }) {
  const issue = useIssue(path, useId());
  return (
    <>
      {children(issue.props)}
      {issue.note}
    </>
  );
}

// askMe adds the box that leaves the field to the person at each application.
export function TextField({ label, value, onChange, hint, type = "text", example, wide, placeholder, path, askMe }: { label: string; value: string; onChange: (v: string) => void; hint?: string; type?: string; example?: string; wide?: boolean; placeholder?: string; path?: string; askMe?: boolean }) {
  const id = useId();
  const issue = useIssue(path, id);
  const mine = askMe && value === ASK_ME;
  return (
    <div data-wide={wide || undefined}>
      <label htmlFor={id}>{label}</label>
      <input id={id} type={type} value={mine ? "" : value} disabled={mine} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} {...issue.props} />
      {askMe && <AskMeBox value={value} onChange={onChange} />}
      {example && isExample(value) && <ExampleMarker step={example} />}
      {hint && <span className="muted">{hint}</span>}
      {issue.note}
    </div>
  );
}

// SaveForm owns the busy, error and saved states of one edit.
// columns lays the fields out two to a row where there is room. onDiscard puts
// the saved values back; reload reads the latest file after a conflict, under
// the kept edits (useDraft) or, on "Load the new version", without them.
// blocked holds the save while the form says what is missing. A refusal that
// names fields is shown beside them, the first one focused, with a summary
// below and the findings no control owns; an edit of a field clears its own.
export function SaveForm({ onSubmit, onDiscard, reload, dirty, label = "Save", columns, blocked, children }: { onSubmit: () => Promise<void>; onDiscard: () => void; reload: () => void; dirty: boolean; label?: string; columns?: boolean; blocked?: boolean; children: ReactNode }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [issues, setIssues] = useState<{ [path: string]: string }>({});
  const [unowned, setUnowned] = useState<string[]>([]);
  const form = useRef<HTMLFormElement>(null);
  const named = error?.kind === "validation_failed" && error.details?.fields ? issues : null;
  useLayoutEffect(() => {
    if (!named) return;
    const paths = [...form.current!.querySelectorAll<HTMLElement>("[data-path]")].map((e) => e.dataset.path!);
    setUnowned(Object.entries(named).filter(([p]) => !paths.some((f) => owns(f, p))).map(([p, m]) => `${p}: ${m}`));
    form.current!.querySelector<HTMLElement>("[aria-invalid=true]")?.focus();
  }, [error]);
  const left = Object.keys(issues).length;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onSubmit();
      setSaved(new Date().toLocaleTimeString());
    } catch (err) {
      const e = asApiError(err);
      setSaved(null);
      setError(e);
      setIssues(e.details?.fields ?? {});
    } finally {
      setBusy(false);
    }
  };
  const edited = (e: FormEvent) => {
    const p = (e.target as HTMLElement).dataset?.path;
    if (p && named) setIssues(Object.fromEntries(Object.entries(issues).filter(([i]) => !owns(p, i))));
  };
  return (
    <form ref={form} onSubmit={submit} onChange={edited} className={columns ? "form-grid two" : "form-grid"}>
      <Issues.Provider value={named ?? {}}>{children}</Issues.Provider>
      <SaveBar dirty={dirty} savedAt={saved ?? ""}>
        {dirty && (
          <button type="button" className="ghost" disabled={busy} onClick={() => (onDiscard(), setError(null))}>
            Discard changes
          </button>
        )}
        <button type="submit" disabled={busy || !dirty || blocked}>
          {label}
        </button>
      </SaveBar>
      {error?.kind === "revision_conflict" ? (
        <Changed onKeep={() => (setError(null), reload())} onLoad={() => (onDiscard(), setError(null), reload())} />
      ) : named ? (
        left > 0 && (
          <div role="alert" className="note error">
            <p>Not saved: {left === 1 ? "one answer needs" : `${left} answers need`} another look.</p>
            {unowned.map((m) => <p key={m}>{m}</p>)}
          </div>
        )
      ) : (
        error && <ErrorNote error={error} />
      )}
    </form>
  );
}
