import { useState, type FormEvent } from "react";
import type { ConfigDocument, ScanSource } from "../../../wire";
import { usePortalsWrite } from "./WriteError";
import { queryText, sourcesOf } from "./edit";

// Searches edits the saved search queries of one source: a search string and
// an optional location. Add appends one query and Remove takes one out, so the
// rest of the file keeps its bytes.
export function Searches({ doc, sources, reloadDoc }: { doc: ConfigDocument; sources: ScanSource[]; reloadDoc: () => void }) {
  // The source list may arrive after this form; until one is picked the first
  // listed source is the one shown and edited.
  const [picked, setSource] = useState("");
  const source = picked || sources[0]?.name || "";
  const title = sources.find((s) => s.name === source)?.title ?? source;
  const [search, setSearch] = useState("");
  const [location, setLocation] = useState("");
  const { write, busy, notice } = usePortalsWrite(doc, reloadDoc);
  const all = sourcesOf(doc);
  const queries: unknown[] = Array.isArray(all[source]?.queries) ? (all[source].queries as unknown[]) : [];
  const list = `sources.${source}.queries`;

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const q = search.trim();
    const loc = location.trim();
    const item = loc ? { search: q, location: loc } : q;
    if (await write({ kind: "add", list, item, label: `The search ${queryText(item)}` })) {
      setSearch("");
      setLocation("");
    }
  };

  return (
    <section aria-label="Search queries">
      <h3>Search queries</h3>
      <p className="muted">Searches a source runs when it is started without options. A source with none is skipped.</p>
      <div className="form-grid">
        <label>
          Source
          <select value={source} onChange={(e) => setSource(e.target.value)}>
            {sources.map((s) => (
              <option key={s.name} value={s.name}>
                {s.title}
              </option>
            ))}
          </select>
        </label>
      </div>
      {queries.length === 0 ? (
        <p className="muted">{title} has no saved search.</p>
      ) : (
        <ul className="entry-list plain" aria-label={`Saved searches of ${title}`}>
          {queries.map((q, i) => (
            <li key={`${queryText(q)}-${i}`} className="row">
              <span>{queryText(q)}</span>
              <button type="button" className="danger-outline" aria-label={`Remove the search ${queryText(q)}`} disabled={busy} onClick={() => void write({ kind: "remove", list, item: q, label: `The search ${queryText(q)}` })}>
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
      <form onSubmit={add} className="form-grid" aria-label="Add a search">
        <label>
          Search
          <input type="text" required value={search} onChange={(e) => setSearch(e.target.value)} />
        </label>
        <label>
          Location (optional)
          <input type="text" value={location} onChange={(e) => setLocation(e.target.value)} />
        </label>
        <button type="submit" disabled={busy || search.trim() === "" || source === ""}>
          Add search
        </button>
      </form>
      {notice}
    </section>
  );
}
