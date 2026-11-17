import { useId } from "react";
import { get, patch } from "../../../api/client";
import { portalTargetRoles } from "../../../api/decoders_discovery";
import { configSaved } from "../../../api/decoders_pages";
import { asApiError, useResource } from "../../../hooks";
import { ErrorNote } from "../../../ui/parts";
import { listOf, rowsOf, SaveForm, text, TextField, useDraft, valueAt } from "../../profile/shared";
import type { ConfigDocument } from "../../../wire";
import { useAction } from "../../action";
import { keyOps, savePortals } from "./edit";

const terms = (t: string) => t.split("\n").map((x) => x.trim()).filter(Boolean);
// A weight line is "term: weight"; one without a whole-number weight is sent
// without it, so the server's refusal names the line's weight.
const boosts = (t: string) =>
  terms(t).map((l) => {
    const m = /^(.*):\s*([+-]?\d+)$/.exec(l);
    return m ? { term: m[1].trim(), weight: Number(m[2]) } : { term: l };
  });

// The filter and speed keys of portals.yml, each a list of terms one per line,
// except the seniority weights and the two concurrency numbers.
const LISTS: [string, string, string, string?][] = [
  ["filters.title.positive", "Title must contain one of", "Blank: any title", "A term of several words matches when the title has each word, in any order."],
  ["filters.title.negative", "Title must contain none of", "Blank: none"],
  ["filters.title.seniority_boost", "Seniority weights", "senior: -2", "One per line as term: weight, -10 to 10. They rank postings, never reject them."],
  ["filters.skip_companies", "Companies to skip", "Blank: none", "Dropped before any posting is read."],
  ["filters.locations.include", "Location must contain one of", "Blank: any location"],
  ["filters.locations.exclude", "Location must contain none of", "Blank: none"],
];
const SPEED = [
  ["concurrency.list", "Requests at once to one site", "4"],
  ["concurrency.detail", "Postings read at once", "6"],
];
const BOOST = "filters.title.seniority_boost";

function Lines({ label, value, onChange, placeholder, hint }: { label: string; value: string; onChange: (v: string) => void; placeholder: string; hint?: string }) {
  const id = useId();
  return (
    <div>
      <label htmlFor={id}>{label}</label>
      <textarea id={id} rows={3} spellCheck={false} value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      {hint && <span className="muted">{hint}</span>}
    </div>
  );
}

// Filters edits the scan's filters and speed in portals.yml. Each changed key
// is written on its own, so the file's comments and other keys keep their
// bytes; a blank number leaves its key out and the default it shows applies.
// A company's own title filter, shown in the table, replaces the title lists
// for that company. The server validates the result and names a refused key.
export function Filters({ doc, reloadDoc }: { doc: ConfigDocument; reloadDoc: () => void }) {
  const start: { [path: string]: string } = {};
  for (const [p] of LISTS) start[p] = p === BOOST ? rowsOf(valueAt(doc.values, p)).map((b) => `${text(b.term)}: ${text(b.weight)}`).join("\n") : listOf(valueAt(doc.values, p)).join("\n");
  for (const [p] of SPEED) start[p] = text(valueAt(doc.values, p));
  const [f, setF] = useDraft(start);
  const put = (p: string) => (v: string) => setF({ ...f, [p]: v });
  const save = async () => {
    const changed = Object.keys(start).filter((p) => f[p] !== start[p]);
    await savePortals(doc, keyOps(doc.values, changed.map((p) => [p, p.startsWith("concurrency") ? (f[p].trim() ? Number(f[p]) : undefined) : p === BOOST ? boosts(f[p]) : terms(f[p])])));
    reloadDoc();
  };
  return (
    <section aria-label="Filters and speed">
      <h3>Filters and speed</h3>
      <p className="muted">Matching ignores case and goes by whole words.</p>
      <SaveForm onSubmit={save} reload={reloadDoc} onDiscard={() => setF(start)} dirty={JSON.stringify(f) !== JSON.stringify(start)} label="Save filters" columns>
        {LISTS.map(([p, label, placeholder, hint]) => <Lines key={p} label={label} value={f[p]} onChange={put(p)} placeholder={placeholder} hint={hint} />)}
        {SPEED.map(([p, label, d]) => <TextField key={p} type="number" label={label} value={f[p]} onChange={put(p)} placeholder={`${d} (default)`} hint="1 to 16." />)}
      </SaveForm>
    </section>
  );
}

// useTargetRoles is the server's plan of "Use my target roles": the edits of
// portals.yml that put the profile's target roles in the title filter and in
// the saved search of each enabled board that has none.
export const useTargetRoles = () => useResource(() => get("/api/portals/target-roles", portalTargetRoles), [], ["configuration"]);

export const TargetRoleLines = ({ lines }: { lines: string[] }) => (
  <ul aria-label="What it writes">
    {lines.map((l) => (
      <li key={l}>{l}</li>
    ))}
  </ul>
);

// TargetRoles offers the plan while it would change something, and writes
// exactly what it lists through the revision-checked patch. When the file
// changed meanwhile, the plan is worked out again on the new file at once.
export function TargetRoles({ reloadDoc }: { reloadDoc: () => void }) {
  const [plan, reloadPlan] = useTargetRoles();
  const act = useAction();
  const p = plan.data;
  if (!p?.lines.length) return null;
  const use = async () => {
    const stale = (e: unknown) => {
      if (asApiError(e).kind === "revision_conflict") reloadPlan();
      throw e;
    };
    if (await act.run(() => patch("/api/config/portals", configSaved, { expectedRevision: p.revision, ops: p.ops }).catch(stale))) {
      reloadPlan();
      reloadDoc();
    }
  };
  return (
    <section aria-label="Use my target roles">
      <h3>Use my target roles</h3>
      <p className="muted">Your profile's target roles are not in these filters yet. This writes:</p>
      <TargetRoleLines lines={p.lines} />
      {act.error?.kind === "revision_conflict" ? (
        <p role="alert" className="note error">portals.yml changed, so nothing was written. The plan above is now worked out on the new file.</p>
      ) : (
        act.error && <ErrorNote error={act.error} />
      )}
      <div className="actions">
        <button type="button" disabled={act.busy} onClick={() => void use()}>
          Use my target roles
        </button>
      </div>
    </section>
  );
}
