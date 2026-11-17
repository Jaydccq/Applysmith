import { useEffect, useRef, useState, type MouseEvent } from "react";
import { get } from "../../api/client";
import { kbDocument } from "../../api/decoders_pages";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { Markdown } from "../../ui/Markdown";
import { Chip, Panel, humanize } from "../../ui/parts";
import { askTo } from "../answerbank/model";

// A link to another page in a page's text is bundle-relative: "/projects/x.md".
const isPagePath = (href: string) => href.startsWith("/") && /\.md$/i.test(href.split("#")[0]);

// PageView reads one wiki page as a page of a wiki: its Markdown, with the
// links to other pages followed inside the Wiki, and the pages that link to it.
export function PageView({ id, onOpen, onBack }: { id: string; onOpen: (id: string) => void; onBack: () => void }) {
  const [res, reload] = useResource(() => get(`/api/kb/documents/${encodeURIComponent(id)}`, kbDocument), [id]);
  const [missing, setMissing] = useState("");
  const heading = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setMissing("");
    heading.current?.focus?.();
  }, [id]);
  const follow = (e: MouseEvent) => {
    const href = (e.target as Element).closest?.("a")?.getAttribute("href") ?? "";
    if (!isPagePath(href) || !res.data) return;
    e.preventDefault();
    const path = href.split("#")[0];
    const link = res.data.links.find((l) => l.path === path);
    if (link?.id) onOpen(link.id);
    else setMissing(path);
  };
  const doc = res.data;
  const title = doc ? doc.entry.title || doc.entry.id : "Page";
  return (
    <Panel title={title} id="kb-page" res={res} retry={reload} what="this page" actions={<button type="button" onClick={onBack}>All pages</button>}>
      {(d) => (
        <div>
          <div ref={heading} tabIndex={-1} className="row">
            {d.entry.category && <Chip>{humanize(d.entry.category)}</Chip>}
            {d.entry.tags.map((t) => <Chip key={t} raw>{t}</Chip>)}
          </div>
          {d.entry.description && <p className="muted">{d.entry.description}</p>}
          <div onClick={follow}>
            <Markdown source={d.body} />
          </div>
          <div aria-live="polite">{missing && <p className="note" role="status">There is no page at {missing}.</p>}</div>
          <h3>Linked from</h3>
          {d.linkedFrom.length === 0
            ? <p className="muted">No other page links to this one.</p>
            : <ul className="plain">{d.linkedFrom.map((p) => <li key={p.id}><Link to={`/wiki?page=${encodeURIComponent(p.id)}`}>{p.title || p.id}</Link></li>)}</ul>}
          <p className="muted">To change this page, <Link to={askTo(`About my wiki page "${title}": `)}>tell the Ask Agent</Link>.</p>
        </div>
      )}
    </Panel>
  );
}
