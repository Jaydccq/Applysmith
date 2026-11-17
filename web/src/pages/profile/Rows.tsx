import { Plus, Trash2 } from "lucide-react";
import { useId } from "react";
import { Empty } from "../../ui/parts";
import { amount, amountHint, isExample, ExampleMarker, useIssue } from "./shared";

export interface Column {
  key: string;
  label: string;
  type?: "text" | "number" | "select";
  options?: readonly string[];
  /** Setup step to link when the value still equals an example. */
  example?: string;
  /** The form the value takes, shown while the cell is empty. */
  placeholder?: string;
  /** A long value takes the whole row. */
  wide?: boolean;
}

export type Row = Record<string, string>;

// rowsFrom turns the parsed list of a profile key into editable text rows.
export function rowsFrom(list: { [key: string]: unknown }[], columns: readonly Column[]): Row[] {
  return list.map((item) => Object.fromEntries(columns.map((c) => [c.key, item[c.key] === null || item[c.key] === undefined ? "" : String(item[c.key])])));
}

// valueFrom is the list as the file keeps it: blank cells are left out and a
// number column holds a number.
export function valueFrom(rows: Row[], columns: readonly Column[]) {
  return rows.map((r) =>
    Object.fromEntries(
      columns
        .filter((c) => (r[c.key] ?? "").trim() !== "")
        .map((c) => [c.key, (c.type === "number" ? amount(r[c.key]) : undefined) ?? r[c.key].trim()]),
    ),
  );
}

// held says a number cell holds text that is no number, which holds the save.
export const held = (rows: Row[], columns: readonly Column[]) => rows.some((r) => columns.some((c) => c.type === "number" && amountHint(r[c.key] ?? "")));

function Cell({ column, value, onChange, name, path }: { column: Column; value: string; onChange: (v: string) => void; name: string; path?: string }) {
  const id = useId();
  const issue = useIssue(path, id);
  return (
    <div data-wide={column.wide || undefined}>
      <label htmlFor={id}>
        {column.label}
        <span className="sr-only"> of {name}</span>
      </label>
      {column.type === "select" ? (
        <select id={id} value={value} onChange={(e) => onChange(e.target.value)} {...issue.props}>
          <option value="">Not set</option>
          {column.options?.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      ) : (
        <input id={id} type="text" inputMode={column.type === "number" ? "decimal" : undefined} value={value} placeholder={column.placeholder} onChange={(e) => onChange(e.target.value)} {...issue.props} />
      )}
      {column.type === "number" && amountHint(value) && <span className="muted">{amountHint(value)}</span>}
      {column.example && isExample(value) && <ExampleMarker step={column.example} />}
      {issue.note}
    </div>
  );
}

// RowsEditor edits a list of records, one card per record with its fields
// side by side where there is room. A record of a few short fields (a skill, a
// role) reads as a table row instead: on a wide screen the first row names the
// columns and the rest leave the names to screen readers. path is the list's
// profile path, so a refusal of a cell is shown beside it; from is the place
// of the first row in that list when other rows come before it.
export function RowsEditor({ title, noun, columns, rows, onChange, path, from = 0 }: { title: string; noun: string; columns: readonly Column[]; rows: Row[]; onChange: (rows: Row[]) => void; path?: string; from?: number }) {
  const compact = columns.length <= 4 && columns.every((c) => !c.wide);
  return (
    <fieldset className="rows">
      <legend>{title}</legend>
      {rows.length === 0 && <Empty>No {noun} yet.</Empty>}
      <ul className={compact ? "plain rows-list compact" : "plain rows-list"} data-cols={compact ? columns.length : undefined}>
        {rows.map((r, i) => {
          const name = `${noun} ${i + 1}`;
          return (
            <li key={i} className="rows-item">
              <div className="form-grid rows-fields">
                {columns.map((c) => (
                  <Cell key={c.key} column={c} name={name} path={path && `${path}[${from + i}].${c.key}`} value={r[c.key] ?? ""} onChange={(v) => onChange(rows.map((x, j) => (j === i ? { ...x, [c.key]: v } : x)))} />
                ))}
              </div>
              <button type="button" className="ghost small icon-only" onClick={() => onChange(rows.filter((_, j) => j !== i))} aria-label={`Remove ${name}`} title={`Remove ${name}`}>
                <Trash2 aria-hidden="true" />
              </button>
            </li>
          );
        })}
      </ul>
      <button type="button" className="ghost small" onClick={() => onChange([...rows, Object.fromEntries(columns.map((c) => [c.key, ""]))])}>
        <Plus aria-hidden="true" />
        Add {noun}
      </button>
    </fieldset>
  );
}
