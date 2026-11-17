import { useState, type ReactNode } from "react";
import { Empty, usd } from "../../ui/parts";

interface BarItem {
  key: string;
  label: string;
  costUsd: number;
}

// CostChart draws bars that carry their value as text, so colour never carries
// the number; the toggle swaps them for a table of the same numbers. With onPick a label is
// a button that hands its key back.
export function CostChart({ caption, items, table, empty, onPick }: { caption: string; items: BarItem[]; table: ReactNode; empty: string; onPick?: (key: string) => void }) {
  const [asTable, setAsTable] = useState(false);
  if (items.length === 0) return <Empty>{empty}</Empty>;
  const max = Math.max(...items.map((i) => i.costUsd), Number.EPSILON);
  return (
    <>
      <button type="button" aria-pressed={asTable} onClick={() => setAsTable((t) => !t)}>
        {asTable ? "Show as chart" : "Show as table"}
      </button>
      {asTable ? (
        table
      ) : (
        <ul className="plain bars" aria-label={caption}>
          {items.map((i) => (
            <li key={i.key} className="bar-row">
              {onPick ? (
                <button type="button" title={`Show the calls of ${i.label}`} onClick={() => onPick(i.key)}>
                  {i.label}
                </button>
              ) : (
                <span>{i.label}</span>
              )}
              <span className="bar" aria-hidden="true">
                <span style={{ width: `${(i.costUsd / max) * 100}%` }} />
              </span>
              <span>{usd(i.costUsd)}</span>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
