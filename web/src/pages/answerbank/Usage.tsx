import { Search } from "lucide-react";
import { useState } from "react";
import { Link, useSearchParams } from "../../router";
import { FilterChips } from "../../ui/FilterChips";
import { Empty, formatDate } from "../../ui/parts";
import type { AnswerBankEntry, AnswerBankUsage } from "../../wire";

const DAY_MS = 86_400_000;

// lastUsed is the later of the last match and the last apply; empty when the
// entry never had either.
const lastUsed = (r: AnswerBankUsage["rows"][number]) => (r.lastMatchedAt > r.lastAppliedAt ? r.lastMatchedAt : r.lastAppliedAt);

// A zero is drawn quieter than a count, so the entries in use stand out.
const Count = ({ label, n }: { label: string; n: number }) => (
  <td data-label={label} className={n === 0 ? "num zero" : "num"}>
    {n}
  </td>
);

// The bank holds a few hundred entries and most are never asked, so the table
// opens on the ones used in the window, most matched first; the rest are one
// chip away.
const VIEWS = [
  { id: "used", label: "In use" },
  { id: "unused", label: "Not used lately" },
  { id: "all", label: "All" },
] as const;
type View = (typeof VIEWS)[number]["id"];

export function Usage({ usage, entries, now = Date.now() }: { usage: AnswerBankUsage; entries: AnswerBankEntry[]; now?: number }) {
  const [params] = useSearchParams();
  const view: View = VIEWS.find((v) => v.id === params.get("view"))?.id ?? "used";
  const [query, setQuery] = useState("");
  const names = new Map(entries.map((e) => [e.id, e.description || e.id]));
  const all = usage.rows
    .map((r) => {
      const used = lastUsed(r);
      return { r, used, name: names.get(r.entryId) ?? r.entryId, recent: used !== "" && now - Date.parse(used) <= usage.windowDays * DAY_MS };
    })
    .sort((a, b) => b.r.matches - a.r.matches || b.used.localeCompare(a.used) || a.name.localeCompare(b.name));
  const inUse = all.filter((x) => x.recent).length;
  const q = query.trim().toLowerCase();
  const rows = all.filter((x) => (view === "all" || x.recent === (view === "used")) && (q === "" || `${x.name} ${x.r.entryId}`.toLowerCase().includes(q)));
  const counts: Record<View, number> = { used: inUse, unused: all.length - inUse, all: all.length };
  return (
    <div>
      <p>
        <strong>{inUse}</strong> of {all.length} entries were used in the last {usage.windowDays} days.
      </p>
      <FilterChips
        label="Entries by use"
        items={VIEWS.map((v) => ({ label: v.label, count: counts[v.id], to: v.id === "used" ? "/answer-bank?tab=usage" : `/answer-bank?tab=usage&view=${v.id}`, current: v.id === view }))}
      />
      <label className="field entries-search">
        <span className="sr-only">Search usage</span>
        <span className="search-box">
          <Search aria-hidden="true" />
          <input type="search" data-page-search value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search an entry or an id" />
        </span>
      </label>
      <p className="help">
        Counts cover the last {usage.windowDays} days. Last used shows the latest use on record; the server keeps events for 180 days and then deletes them. Reading this page changes nothing.
      </p>
      {rows.length === 0 ? (
        <Empty>
          {q !== "" ? (
            "No entry matches the search."
          ) : view === "unused" ? (
            `Every entry was used in the last ${usage.windowDays} days.`
          ) : (
            <>
              No entry was used in the last {usage.windowDays} days. An entry counts as used when an autofill run matches a question to it.{" "}
              <Link to="/answer-bank?tab=usage&view=all">Show every entry</Link>
            </>
          )}
        </Empty>
      ) : (
        <table className="cards">
          <caption className="sr-only">Usage by entry</caption>
          <thead>
            <tr>
              <th scope="col">Entry</th>
              <th scope="col" className="num">Matched</th>
              <th scope="col" className="num">Applied</th>
              <th scope="col" className="num">Left for you</th>
              <th scope="col" className="num">Corrected</th>
              <th scope="col" className="num">Last used</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ r, used, name }) => (
              <tr key={r.entryId}>
                <td data-label="Entry" className="key">
                  <Link to={`/answer-bank?entry=${encodeURIComponent(r.entryId)}`}>{name}</Link>
                </td>
                <Count label="Matched" n={r.matches} />
                <Count label="Applied" n={r.applies} />
                <Count label="Left for you" n={r.abstains} />
                <Count label="Corrected" n={r.corrections} />
                <td data-label="Last used" className={used === "" ? "num zero" : "num"}>{used === "" ? "never" : formatDate(used)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
