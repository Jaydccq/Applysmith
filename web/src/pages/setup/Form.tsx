import { useId, useState, type FormEvent, type ReactNode } from "react";
import { useUnsaved } from "../../ui/SaveBar";
import type { SetupApplied, SetupField, SetupStep } from "../../wire";
import { ApplyOutcome, useApply, type Apply } from "./apply";
import { ASK_ME } from "../answerbank/model";
import { encodeInput, listFromText, text, type Values } from "./model";
import { withCode } from "./Reasons";

// Names whose usual spelling is not the id's.
const SPELLED: Record<string, string> = { linkedin: "LinkedIn", github: "GitHub", vp: "VP" };

// labelOf turns a field name or an enum value into words: work_visa is "Work visa".
export const labelOf = (name: string): string => {
  if (SPELLED[name]) return SPELLED[name];
  const s = name.replaceAll("_", " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
};

export interface Form {
  values: Values;
  base: Values;
  set: (name: string, v: unknown) => void;
  markSaved: () => void;
}

// useForm holds what a person typed next to what was loaded, so the form can
// send only what changed.
function useForm(initial: Values): Form {
  const [base, setBase] = useState<Values>(initial);
  const [values, setValues] = useState<Values>(initial);
  return { values, base, set: (name, v) => setValues((p) => ({ ...p, [name]: v })), markSaved: () => setBase(values) };
}

export interface Control {
  id: string;
  value: unknown;
  set: (v: unknown) => void;
  invalid: boolean;
  describedBy: string;
}

function EnumList({ field, control }: { field: SetupField; control: Control }) {
  const chosen = listFromText(text(control.value));
  const toggle = (opt: string) => control.set((chosen.includes(opt) ? chosen.filter((c) => c !== opt) : [...chosen, opt]).join(", "));
  return (
    <div role="group" aria-labelledby={control.id + "-label"} className="chips">
      {(field.enum ?? []).map((opt) => (
        <label key={opt} className="check">
          <input type="checkbox" checked={chosen.includes(opt)} onChange={() => toggle(opt)} />
          {labelOf(opt)}
        </label>
      ))}
    </div>
  );
}

// FieldControl is the input the schema type asks for: text for a string,
// a checkbox for a bool, a number box for an int, a select for an enum, a
// comma separated box for a list, and a JSON box for a shape no purpose-built
// editor covers.
function FieldControl({ field, control }: { field: SetupField; control: Control }) {
  const common = { id: control.id, "aria-invalid": control.invalid || undefined, "aria-describedby": control.describedBy };
  if (field.type === "bool") {
    return <input {...common} type="checkbox" checked={control.value === true} onChange={(e) => control.set(e.target.checked)} />;
  }
  if (field.type === "string-list" && field.enum?.length) return <EnumList field={field} control={control} />;
  if (field.enum?.length) {
    return (
      <select {...common} value={text(control.value)} onChange={(e) => control.set(e.target.value)}>
        <option value="">Choose</option>
        {field.enum.map((opt) => (
          <option key={opt} value={opt}>
            {labelOf(opt)}
          </option>
        ))}
      </select>
    );
  }
  if (field.type === "object" || field.type === "object-list") {
    const shown = typeof control.value === "string" ? control.value : control.value === undefined ? "" : JSON.stringify(control.value, null, 2);
    return <textarea {...common} className="editor" spellCheck={false} value={shown} onChange={(e) => control.set(e.target.value)} />;
  }
  return <input {...common} type={field.type === "int" ? "number" : "text"} value={text(control.value)} onChange={(e) => control.set(e.target.value)} />;
}

// AskMeBox leaves a field to the person at each application by storing the
// ask-me marker, so nobody has to type it.
export function AskMeBox({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <label className="check">
      <input type="checkbox" checked={value === ASK_ME} onChange={(e) => onChange(e.target.checked ? ASK_ME : "")} />
      Ask me at each application
    </label>
  );
}

export function FieldErrors({ id, errors }: { id: string; errors: string[] | undefined }) {
  if (!errors?.length) return null;
  return (
    <div id={id} role="alert">
      {errors.map((m) => (
        <p key={m} className="note error compact">
          {m}
        </p>
      ))}
    </div>
  );
}

// FieldRow puts a label, the control, the schema's own explanation and the
// field's errors together, so a rejection appears beside what it rejects.
function FieldRow({ field, form, errors, custom, label, hint }: {
  field: SetupField;
  form: Form;
  errors: string[] | undefined;
  custom?: (c: Control) => ReactNode;
  label?: string;
  hint?: string;
}) {
  const id = useId();
  const control: Control = {
    id,
    value: form.values[field.name],
    set: (v) => form.set(field.name, v),
    invalid: !!errors?.length,
    describedBy: `${id}-doc ${id}-err`,
  };
  const name = label ?? labelOf(field.name);
  const mark = field.required ? <span className="muted"> (required)</span> : null;
  const inline = field.type === "bool" && !custom;
  return (
    <div className="field" data-field={field.name}>
      {inline ? (
        <label className="check" htmlFor={id}>
          <FieldControl field={field} control={control} />
          <span>
            {name}
            {mark}
          </span>
        </label>
      ) : (
        <>
          <label htmlFor={id} id={id + "-label"}>
            {name}
            {mark}
          </label>
          {custom ? custom(control) : <FieldControl field={field} control={control} />}
        </>
      )}
      <span className="muted" id={id + "-doc"}>
        {withCode(hint ?? field.doc)}
      </span>
      <FieldErrors id={id + "-err"} errors={errors} />
    </div>
  );
}

export type Custom = Record<string, (c: Control) => ReactNode>;

interface SchemaFormProps {
  step: SetupStep;
  reload: () => void;
  fields?: readonly SetupField[];
  initial?: Values;
  /** Replaces the input of a field with a purpose-built control. */
  custom?: Custom | ((form: Form, busy: boolean) => Custom);
  /** Hides a field whose answer does not apply yet. */
  visible?: (name: string, form: Form) => boolean;
  labels?: Record<string, string>;
  /** Replaces the schema's own explanation under a field; empty hides it. */
  hints?: Record<string, string>;
  submit?: string;
  /** Holds saving while a custom control is still loading its draft, or holds
   * text it cannot send. */
  disabled?: boolean | ((form: Form) => boolean);
  /** Lays short fields out two to a row where the width allows. */
  columns?: boolean;
  onApplied?: (applied: SetupApplied) => void;
  /** Rendered between the fields and the button. */
  children?: ReactNode | ((form: Form) => ReactNode);
  /** Rendered above the fields. */
  lead?: (form: Form) => ReactNode;
  /** Read at each save and sent beside the changed fields. */
  extra?: () => Values;
  /** Replaces the outcome when the save is refused as made from an older file. */
  conflict?: (form: Form, clear: () => void) => ReactNode;
}

// SchemaForm is the schema-driven form: one row per field of the step, sending
// only the fields that changed and showing the writer's answer per field.
// Another step or page asks first while a change is not saved.
export function SchemaForm({ step, reload, fields = step.fields, initial = {}, custom = {}, visible, labels = {}, hints = {}, submit = "Save", disabled = false, columns, onApplied, children, lead, extra, conflict }: SchemaFormProps) {
  const form = useForm(initial);
  const apply: Apply = useApply(step.id, reload);
  const controls = typeof custom === "function" ? custom(form, apply.busy) : custom;
  const held = typeof disabled === "function" ? disabled(form) : disabled;
  const [local, setLocal] = useState<Record<string, string[]>>({});
  const { input, errors: encodeErrors } = encodeInput(fields, form.values, form.base);
  const dirty = Object.keys(input).length > 0 || Object.keys(encodeErrors).length > 0;
  const ask = useUnsaved(dirty && !apply.busy);
  const errors = { ...apply.byField, ...local };
  // A rejection can name a field this form does not show; it is still told.
  const orphans = Object.entries(errors).flatMap(([k, ms]) => (fields.some((f) => f.name === k) ? [] : ms.map((m) => (m.startsWith(k) ? m : `${k}: ${m}`))));

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (held || apply.busy) return;
    setLocal(encodeErrors);
    if (Object.keys(encodeErrors).length > 0) return;
    const done = await apply.send({ ...extra?.(), ...input });
    if (done) {
      form.markSaved();
      onApplied?.(done);
    }
  };

  return (
    <form className={columns ? "form-grid form-cols" : "form-grid"} onSubmit={(e) => void onSubmit(e)} noValidate>
      {lead?.(form)}
      {fields.filter((f) => visible?.(f.name, form) ?? true).map((f) => (
        <FieldRow key={f.name} field={f} form={form} errors={errors[f.name]} custom={controls[f.name]} label={labels[f.name]} hint={hints[f.name]} />
      ))}
      {typeof children === "function" ? children(form) : children}
      <FieldErrors id="orphan-errors" errors={orphans} />
      <div className="actions">
        <button type="submit" disabled={held || apply.busy || !dirty}>
          {apply.busy ? "Saving" : submit}
        </button>
        {!dirty && <span className="muted">A field left empty keeps what is stored.</span>}
      </div>
      {conflict && apply.failure?.kind === "revision_conflict" ? conflict(form, apply.clear) : <ApplyOutcome apply={apply} />}
      {ask}
    </form>
  );
}
