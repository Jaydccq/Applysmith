import { X } from "lucide-react";
import { Link } from "../router";
import { Chip, today } from "./parts";

interface FilterChip {
  label: string;
  /** Left out for a preset view, which holds no fixed count. */
  count?: number;
  to: string;
  current: boolean;
  /** A tracker status draws its coloured dot before the label. */
  status?: string;
}

// FilterChips is the row of counted filters above a list: each chip is a link
// that applies one filter, says how many rows it holds, and marks the one in
// force. Every list with counts by status or kind uses it, so they read alike.
//
//   <FilterChips label="Leads by category" items={[{ label: "Posting", count: 2, to: "/leads?category=posting", current: false }]} />
export function FilterChips({ label, items }: { label: string; items: FilterChip[] }) {
  return (
    <ul className="chips plain filter-chips" aria-label={label}>
      {items.map((it) => (
        <li key={it.to}>
          <Link to={it.to} aria-current={it.current ? "true" : undefined}>
            {it.status && <span className="dot" data-status={it.status} aria-hidden="true" />}
            {it.label}
            {it.count !== undefined && <> <Chip>{it.count}</Chip></>}
          </Link>
        </li>
      ))}
    </ul>
  );
}

// isDay is a calendar day as a link carries it, YYYY-MM-DD.
export const isDay = (s: string) => /^\d{4}-\d{2}-\d{2}$/.test(s);

// sinceWords names a window that starts on a local day.
export const sinceWords = (day: string) => (day === today() ? "today" : `since ${day}`);

// ClearChip shows a filter a link put in force that no control above the list
// shows, such as the rows a Home or funnel count counts; pressing it clears
// the filter. Every list a count opens that way uses it, so they read alike.
export function ClearChip({ label, to }: { label: string; to: string }) {
  return (
    <ul className="chips plain filter-chips" aria-label="Active filter">
      <li>
        <Link to={to} aria-current="true" aria-label={`Clear the filter: ${label}`}>
          {label} <X size={14} aria-hidden="true" />
        </Link>
      </li>
    </ul>
  );
}
