import { useState } from "react";
import { TargetRoleLines, useTargetRoles } from "../discovery/portals/TitleFilters";
import { SchemaForm, type Control, type Form } from "./Form";
import { text, type ScreenProps } from "./model";

// PackChoice offers the schedule packs the schema lists. What each one creates
// is the field's own explanation, shown under the choices.
function PackChoice({ c, packs }: { c: Control; packs: readonly string[] }) {
  return (
    <div role="group" aria-labelledby={c.id + "-label"} aria-describedby={c.describedBy} className="chips">
      {packs.map((p) => (
        <label key={p} className="check">
          <input type="radio" name="discovery-pack" checked={text(c.value) === p} onChange={() => c.set(p)} />
          {p}
        </label>
      ))}
    </div>
  );
}

const EDGAR_TITLE = "Turn on the SEC EDGAR hiring-signal source";

// Edgar sends the profile email to the SEC, so the source is switched on in two
// steps: the disclosure is shown first, and only then can it be read and
// accepted. Both flags go to the writer together. The field's label names the
// source; the button says what it opens.
function Edgar({ doc, form }: { doc: string; form: Form }) {
  const [open, setOpen] = useState(form.values.enable_edgar === true);
  const read = form.values.enable_edgar === true && form.values.acknowledge_edgar_disclosure === true;
  const set = (on: boolean) => {
    form.set("enable_edgar", on);
    form.set("acknowledge_edgar_disclosure", on);
  };
  if (!open) {
    return (
      <button type="button" onClick={() => setOpen(true)} aria-expanded={false}>
        Read what it sends
      </button>
    );
  }
  return (
    <div>
      <p role="note">{doc}</p>
      <label className="check">
        <input type="checkbox" checked={read} onChange={(e) => set(e.target.checked)} />
        I have read this
      </label>
      {!read && (
        <button type="button" onClick={() => setOpen(false)}>
          Keep it off
        </button>
      )}
    </div>
  );
}

// TargetRolesPlan previews what "Use my target roles" writes into portals.yml.
function TargetRolesPlan() {
  const [plan] = useTargetRoles();
  if (!plan.data) return null;
  return plan.data.lines.length ? <TargetRoleLines lines={plan.data.lines} /> : <p className="muted">Nothing to write: portals.yml already uses the target roles, or the profile lists none.</p>;
}

export function Discovery({ step, reload }: ScreenProps) {
  const packs = step.fields.find((f) => f.name === "pack")?.enum ?? [];
  const edgar = step.fields.find((f) => f.name === "enable_edgar");
  return (
    <SchemaForm
      step={step}
      reload={reload}
      submit="Save choice"
      visible={(name) => name !== "acknowledge_edgar_disclosure"}
      custom={(form) => ({ pack: (c) => <PackChoice c={c} packs={packs} />, ...(edgar ? { enable_edgar: () => <Edgar doc={edgar.doc} form={form} /> } : {}) })}
      labels={{ pack: "Schedule pack", seed_registry: "Fetch the public board lists now", enable_edgar: EDGAR_TITLE, use_target_roles: "Use my target roles", enable_boards: "Turn on the job boards that need no sign-in" }}
      hints={{ enable_edgar: "" }}
    >
      {(form) => form.values.use_target_roles === true && <TargetRolesPlan />}
    </SchemaForm>
  );
}
