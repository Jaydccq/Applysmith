import { useEffect, useState, type MouseEvent } from "react";
import { useResource } from "../hooks";
import { navigate, useSearchParams } from "../router";
import { Markdown } from "../ui/Markdown";
import { Modal } from "../ui/Modal";
import { PageHeader } from "../ui/PageHeader";
import { ErrorNote, Loading } from "../ui/parts";
import { Tabs } from "../ui/Tabs";

// The user guide ships in the bundle, one chunk per document and one asset per
// screenshot, both fetched only when a document is opened. The files stay in
// docs/user-guide; nothing is copied.
const ROOT = "../../../docs/user-guide";
const DOCS = import.meta.glob<string>("../../../docs/user-guide/*.md", { query: "?raw", import: "default" });
const PICTURES = import.meta.glob<string>("../../../docs/user-guide/images/*.webp", { query: "?url", import: "default" });

const TABS = [
  { id: "README", label: "Daily loop" },
  { id: "tutorial", label: "Demo tutorial" },
  { id: "find-and-evaluate", label: "Find and evaluate" },
  { id: "apply", label: "Apply" },
  { id: "track", label: "Track outcomes" },
  { id: "ask-and-notifications", label: "Ask and notifications" },
];

// docLinks points a link to another repo document at the page that shows it
// in the app, and leaves only the label of one the app does not carry. dir is
// the folder of the document the text came from.
export function docLinks(md: string, dir: "user-guide" | "setup"): string {
  return md.replace(/(!?)\[([^\]]+)\]\((?![a-z]+:)([^)\s]+)\)/g, (all, bang: string, label: string, href: string) => {
    if (bang) return all;
    const [, folder, name] = /^\/docs\/([^/]+)\/([^/]+)\.md$/.exec(new URL(href, `http://doc/docs/${dir}/`).pathname) ?? [];
    let to: string | undefined;
    if (folder === "user-guide" && `${ROOT}/${name}.md` in DOCS) to = name === "README" ? "/guide" : `/guide?doc=${name}`;
    else if (folder === "setup") to = name === "README" ? "/setup" : /^\d\d-./.test(name) ? `/setup?step=${name.slice(3)}` : undefined;
    return to ? `[${label}](${to})` : label;
  });
}

// readDoc loads one document with its screenshots resolved to bundled assets.
async function readDoc(id: string): Promise<string> {
  const md = await DOCS[`${ROOT}/${id}.md`]();
  const names = [...new Set([...md.matchAll(/\]\(images\/([^)\s]+)\)/g)].map((m) => m[1]))];
  const urls = await Promise.all(names.map((n) => PICTURES[`${ROOT}/images/${n}`]?.()));
  return docLinks(
    names.reduce((text, n, i) => (urls[i] ? text.replaceAll(`](images/${n})`, `](${urls[i]})`) : text), md),
    "user-guide",
  );
}

// follow opens a link to another page of the app without reloading it.
function follow(e: MouseEvent<HTMLElement>) {
  const to = (e.target as HTMLElement).closest("a")?.getAttribute("href");
  if (!to?.startsWith("/") || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  navigate(to);
}

// DocText shows a repo document whose links already went through docLinks. A
// screenshot opens at its full size in a dialog that scrolls, because the
// 1280 px pictures cannot be read at phone width; the link underneath is the
// same picture, so a modified click still reaches it.
export function DocText({ source }: { source: string }) {
  const [shot, setShot] = useState<HTMLImageElement | null>(null);
  const pick = (e: MouseEvent<HTMLElement>) => {
    const img = (e.target as HTMLElement).closest("a.md-shot")?.querySelector("img");
    if (img && e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey) {
      e.preventDefault();
      setShot(img);
    } else follow(e);
  };
  return (
    <div onClick={pick}>
      <Markdown source={source} />
      {shot && (
        <Modal title={shot.alt || "Screenshot"} onClose={() => setShot(null)} variant="wide">
          {/* Focusable, so the keyboard scrolls a picture wider than the dialog. */}
          <div className="shot-scroll" tabIndex={0} role="group" aria-label="Full-size screenshot">
            <img src={shot.getAttribute("src") ?? ""} alt={shot.alt} />
          </div>
          <div className="actions">
            <button type="button" onClick={() => setShot(null)}>
              Close
            </button>
          </div>
        </Modal>
      )}
    </div>
  );
}

function DocView({ id }: { id: string }) {
  const [res, retry] = useResource(() => readDoc(id), [id]);
  // The braces matter: Chrome's scrollTo returns a promise, which an effect would take for its cleanup.
  useEffect(() => {
    window.scrollTo?.(0, 0);
  }, [id]);
  if (res.status === "error") return <ErrorNote error={res.error} what="this part of the guide" onRetry={retry} />;
  return res.data === undefined ? <Loading label="Loading the guide" /> : <DocText source={res.data} />;
}

// Guide is the user guide, read in the app. The open document lives in the
// address, so a reload or a shared link opens the same one.
export default function Guide() {
  const [params, setParams] = useSearchParams();
  const id = TABS.find((t) => t.id === params.get("doc"))?.id ?? "README";
  return (
    <>
      <PageHeader path="/guide" />
      <Tabs label="User guide" tabs={TABS} active={id} onSelect={(next) => setParams({ doc: next === "README" ? undefined : next })}>
        <DocView key={id} id={id} />
      </Tabs>
    </>
  );
}
