import { patch } from "../../../api/client";
import { configSaved } from "../../../api/decoders_pages";
import type { ConfigDocument } from "../../../wire";
import { toYaml } from "../../../api/yaml";
import { rowsOf, setOp, text, unsetOp, valueAt, type Op } from "../../profile/shared";

export type Entry = { [key: string]: unknown };

export const companiesOf = (doc: ConfigDocument): Entry[] => rowsOf(valueAt(doc.values, "companies"));
export const sourcesOf = (doc: ConfigDocument): { [source: string]: Entry } => (valueAt(doc.values, "sources") ?? {}) as { [source: string]: Entry };

// savePortals writes through the revision-checked patch of portals.yml. The
// server validates the whole file after the change and refuses an invalid one.
export async function savePortals(doc: ConfigDocument, ops: Op[]): Promise<true> {
  await patch("/api/config/portals", configSaved, { expectedRevision: doc.revision, ops });
  return true;
}

// keyOps writes each changed key path on its own (undefined unsets it), so the
// rest of the file keeps its bytes. A key path cannot enter a map that is
// missing, empty or written {}, so such a map is written whole with the keys
// going into it; later paths see what earlier ones wrote.
export function keyOps(values: Entry | undefined, changes: [string, unknown][]): Op[] {
  const cur = JSON.parse(JSON.stringify(values ?? {})) as Entry;
  return changes.flatMap(([path, v]) => {
    const segs = path.split(".");
    let at = cur;
    for (let i = 0; i < segs.length - 1; i++) {
      const next = at[segs[i]];
      if (next === null || typeof next !== "object" || Array.isArray(next) || Object.keys(next).length === 0) {
        if (v === undefined) return [];
        at[segs[i]] = segs.slice(i + 1).reduceRight((inner, k) => ({ [k]: inner }), v);
        return [setOp(segs.slice(0, i + 1).join("."), at[segs[i]])];
      }
      at = next as Entry;
    }
    const last = segs[segs.length - 1];
    if (v === undefined) return delete at[last], [unsetOp(path)];
    at[last] = v;
    return [setOp(path, v)];
  });
}

// Change is one edit of a list in portals.yml (companies, or a source's
// queries) as the user meant it: add an item, remove one, or set a key of one.
// It names the item by its whole value as the page read it, not by position,
// so it can be worked out again on a file that changed meanwhile.
export type Change = { list: string; item: unknown; label: string } & ({ kind: "add" } | { kind: "remove" } | { kind: "set"; key: string; value: unknown });

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
const nameOf = (x: unknown) => (x !== null && typeof x === "object" && "name" in x ? text((x as Entry).name).toLowerCase() : undefined);

// opsFor turns a change into operations on one list item of doc, so every
// other entry keeps its bytes and comments. When the item the change names is
// no longer in doc as it was read (removed, or edited meanwhile), or an added
// one already is, the answer is a sentence saying so, and nothing is written.
export function opsFor(doc: ConfigDocument, c: Change): Op[] | string {
  const raw = valueAt(doc.values, c.list);
  const list: unknown[] = Array.isArray(raw) ? raw : [];
  const at = list.findIndex((x) => same(x, c.item));
  const named = nameOf(c.item) !== undefined && list.some((x) => nameOf(x) === nameOf(c.item));
  if (c.kind === "add") {
    if (at >= 0 || named) return `${c.label} is already in portals.yml.`;
    return [{ op: "append", path: c.list, value: toYaml(c.item) }];
  }
  if (at < 0) return named ? `${c.label} was changed in portals.yml meanwhile.` : `${c.label} is no longer in portals.yml.`;
  if (c.kind === "remove") return [{ op: "remove", path: c.list, index: at }];
  return [setOp(`${c.list}[${at}].${c.key}`, c.value)];
}

// queryText is how a saved search reads in a list: its search string, then any
// other parameter.
export function queryText(q: unknown): string {
  if (q === null || typeof q !== "object") return text(q);
  const rec = q as Entry;
  const key = ["search", "query", "q", "keyword"].find((k) => k in rec);
  const rest = Object.entries(rec).filter(([k]) => k !== key);
  return [key ? text(rec[key]) : "", ...rest.map(([k, v]) => `${k}: ${text(v)}`)].filter(Boolean).join(", ");
}
