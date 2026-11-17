import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";

// SortHeader is a column header the list can be sorted by. The sorted column
// carries aria-sort and an arrow; pressing it sorts by that column, or back to
// the list's default order when it already is the sort. A column sorted
// from A to Z passes ascending.
//
//   <SortHeader label="Score" sorted={sort === "score_desc"} onSort={() => setSort(sort === "score_desc" ? "" : "score_desc")} className="num" />
export function SortHeader({ label, sorted, onSort, className, ascending }: { label: string; sorted: boolean; onSort: () => void; className?: string; ascending?: boolean }) {
  return (
    <th scope="col" aria-sort={sorted ? (ascending ? "ascending" : "descending") : "none"} className={className}>
      <button type="button" className="sort" onClick={onSort}>
        {label}
        {sorted ? ascending ? <ArrowUp /> : <ArrowDown /> : <ChevronsUpDown />}
      </button>
    </th>
  );
}
