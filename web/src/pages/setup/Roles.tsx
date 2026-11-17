import { labelOf } from "./Form";
import { SENIORITY, text } from "./model";
import { payHint, payNumber } from "../profile/shared";

export interface Role {
  title?: string;
  seniority?: string;
  priority?: string;
}

// roleOf keeps the keys in one order and drops empty ones, so an unchanged
// editor compares equal to what was loaded.
export const roleOf = (r: { [key: string]: unknown }): Role => {
  const out: Role = {};
  if (text(r.title)) out.title = text(r.title);
  if (text(r.seniority)) out.seniority = text(r.seniority);
  if (text(r.priority)) out.priority = text(r.priority);
  return out;
};

// RolesEditor edits the target roles one row at a time: a title, a seniority
// from the list the profile accepts, and a priority.
export function RolesEditor({ id, rows, onChange }: { id: string; rows: Role[]; onChange: (rows: Role[]) => void }) {
  const edit = (i: number, key: keyof Role, value: string) => onChange(rows.map((r, j) => (j === i ? roleOf({ ...r, [key]: value }) : r)));
  return (
    <div id={id} role="group" aria-label="Target roles">
      <ul className="plain stack">
        {rows.map((r, i) => (
          <li key={i} className="panel" data-role={i}>
            <div className="form-grid">
              <label>
                Role title
                <input type="text" value={r.title ?? ""} onChange={(e) => edit(i, "title", e.target.value)} />
              </label>
              <label>
                Seniority
                <select value={r.seniority ?? ""} onChange={(e) => edit(i, "seniority", e.target.value)}>
                  <option value="">Choose</option>
                  {SENIORITY.map((s) => (
                    <option key={s} value={s}>
                      {labelOf(s)}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Priority
                <select value={r.priority ?? ""} onChange={(e) => edit(i, "priority", e.target.value)}>
                  <option value="">Primary (default)</option>
                  <option value="primary">Primary</option>
                  <option value="secondary">Secondary</option>
                </select>
              </label>
            </div>
            <div className="actions">
              <button type="button" onClick={() => onChange(rows.filter((_, j) => j !== i))} aria-label={`Remove role ${i + 1}`}>
                Remove role
              </button>
            </div>
          </li>
        ))}
      </ul>
      <button type="button" onClick={() => onChange([...rows, {}])}>
        Add a role
      </button>
    </div>
  );
}

export interface Comp {
  currency?: string;
  period?: string;
  minimum?: number | string;
  target?: number | string;
}

// compOf keeps a number a number and leaves unreadable text for the writer to
// reject beside the field.
export function compOf(c: { [key: string]: unknown }): Comp {
  const out: Comp = {};
  if (text(c.currency)) out.currency = text(c.currency);
  if (text(c.period)) out.period = text(c.period);
  for (const k of ["minimum", "target"] as const) {
    const s = text(c[k]).trim();
    if (s !== "") out[k] = payNumber(s) ?? s;
  }
  return out;
}

// compUnreadable is true while an amount is text that is no number; the save
// waits, as the hint beside the field says.
export const compUnreadable = (c: unknown): boolean => ["minimum", "target"].some((k) => typeof (c as Comp | undefined)?.[k as keyof Comp] === "string");

export function CompensationEditor({ id, value, onChange }: { id: string; value: Comp; onChange: (c: Comp) => void }) {
  const edit = (key: keyof Comp, v: string) => onChange(compOf({ ...value, [key]: v }));
  return (
    <div id={id} role="group" aria-label="Compensation" className="form-grid">
      <label>
        Currency (three letters)
        <input type="text" value={value.currency ?? ""} maxLength={3} onChange={(e) => edit("currency", e.target.value.toUpperCase())} />
      </label>
      <label>
        Pay period
        <select value={value.period ?? ""} onChange={(e) => edit("period", e.target.value)}>
          <option value="">Choose</option>
          <option value="year">Year</option>
          <option value="hour">Hour</option>
        </select>
      </label>
      {(["minimum", "target"] as const).map((k) => {
        const hint = payHint(text(value[k]));
        return (
          <label key={k}>
            {k === "minimum" ? "Minimum" : "Target"}
            <input type="text" inputMode="numeric" value={text(value[k])} aria-invalid={hint ? true : undefined} aria-describedby={hint ? `${id}-${k}-hint` : undefined} onChange={(e) => edit(k, e.target.value)} />
            {hint && (
              <span id={`${id}-${k}-hint`} className="muted">
                {hint}
              </span>
            )}
          </label>
        );
      })}
    </div>
  );
}
