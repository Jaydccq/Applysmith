import { Chip } from "../../ui/parts";
import { ApplyButton } from "./apply";
import { SchemaForm, type Control, type Custom } from "./Form";
import { text, listFromText, type ScreenProps, type Values } from "./model";
import { WithProfile } from "./Prefill";
import { compOf, compUnreadable, CompensationEditor, RolesEditor, roleOf, type Comp, type Role } from "./Roles";
import { listOf, rowsOf, valueAt } from "../profile/shared";

const SKIP_FIELD = "batch_skip_companies";
export const TARGET_LABELS = { [SKIP_FIELD]: "Batch skip list" };

// The profile holds the list only after it was edited. Until then the detector's
// reason carries the shipped default, which is what the person is asked to
// confirm.
function skipListOf(profile: { [key: string]: unknown }, reasons: ScreenProps["step"]["reasons"]): string[] {
  const stored = valueAt(profile, SKIP_FIELD);
  if (Array.isArray(stored)) return listOf(stored);
  const m = /skip list \((.*)\) needs review/.exec(reasons.find((r) => r.field === SKIP_FIELD)?.text ?? "");
  return m ? listFromText(m[1]) : [];
}

function targetValues(profile: { [key: string]: unknown }, skip: string[]): Values {
  return {
    roles: rowsOf(valueAt(profile, "targets.roles")).map(roleOf),
    locations: listOf(valueAt(profile, "targets.locations")).join("\n"),
    compensation: compOf((valueAt(profile, "targets.compensation") as { [key: string]: unknown } | undefined) ?? {}),
    [SKIP_FIELD]: skip.join(", "),
  };
}

function SkipList({ c }: { c: Control }) {
  const cleared = Array.isArray(c.value) && c.value.length === 0;
  const names = Array.isArray(c.value) ? [] : listFromText(text(c.value));
  return (
    <div>
      <p className="chips" aria-label="Batch skip list">
        {names.length === 0 ? <span className="muted">{cleared ? "No employer is skipped." : "The list is empty."}</span> : names.map((n) => <Chip key={n}>{n}</Chip>)}
      </p>
      <input id={c.id} type="text" aria-describedby={c.describedBy} aria-invalid={c.invalid || undefined} disabled={cleared} value={cleared ? "" : text(c.value)} onChange={(e) => c.set(e.target.value)} />
      <label className="check">
        <input type="checkbox" checked={cleared} onChange={(e) => c.set(e.target.checked ? [] : "")} />
        Skip no employer
      </label>
    </div>
  );
}

// Targets edits the roles, locations and compensation, and asks for the batch
// skip list to be confirmed or edited: it is the employers that batch applying
// leaves to the person.
export function Targets({ step, reload }: ScreenProps) {
  const needsReview = step.reasons.some((r) => r.field === SKIP_FIELD);
  return (
    <WithProfile>
      {(profile) => {
        const custom: Custom = {
          roles: (c) => <RolesEditor id={c.id} rows={(c.value as Role[] | undefined) ?? []} onChange={c.set} />,
          locations: (c) => <textarea id={c.id} className="editor" aria-describedby={c.describedBy} placeholder="One place per line, or: anywhere or remote" value={text(c.value)} onChange={(e) => c.set(e.target.value)} />,
          compensation: (c) => <CompensationEditor id={c.id} value={(c.value as Comp | undefined) ?? {}} onChange={c.set} />,
          [SKIP_FIELD]: (c) => <SkipList c={c} />,
        };
        return (
          <>
            <SchemaForm step={step} reload={reload} initial={targetValues(profile, skipListOf(profile, step.reasons))} custom={custom} visible={(n) => n !== "skip_list_confirmed"} disabled={(form) => compUnreadable(form.values.compensation)} submit="Save targets" labels={TARGET_LABELS} />
            <h3>Batch skip list</h3>
            {needsReview ? (
              <>
                <p>Batch applying leaves these employers to you. Keep the list as it is, or edit it above and save.</p>
                <ApplyButton step={step} reload={reload} label="Keep this list" input={{ skip_list_confirmed: true }} />
              </>
            ) : (
              <p className="muted">The skip list is confirmed.</p>
            )}
          </>
        );
      }}
    </WithProfile>
  );
}
