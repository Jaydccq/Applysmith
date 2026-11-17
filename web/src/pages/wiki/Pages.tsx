import { useCallback, useEffect, useId, useRef, useState, type FormEvent } from "react";
import { del, get, newRequestId, patch, post, put, request, type ApiError } from "../../api/client";
import { kbCoverage, kbDocument, kbDocumentList, kbDraft, kbDraftList, kbPrevious, kbRepoAdded, kbRepoList, kbRestored, kbUndo } from "../../api/decoders_pages";
import { runAccepted } from "../../api/decoders_runs";
import { secretList, secretStatus } from "../../api/decoders_secrets";
import { useAction, when } from "../action";
import { useRunLines } from "../discovery/runLines";
import { Link, leave, useSearchParams } from "../../router";
import { asApiError, useResource, useTopicsSettled, type Resource } from "../../hooks";
import { Changed, SaveBar } from "../../ui/SaveBar";
import { ConfirmDialog } from "../../ui/Modal";
import { openerOf } from "../../ui/focus";
import { Chip, Empty, ErrorNote, Panel, UndoNote, ageLabel, humanize } from "../../ui/parts";
import { KBDocSnippet, type KBDocEntry, type KBDocument, type KBDraft, type KBDraftList, type KBIngestReport, type KBPrincipleGrade, type KBPrevious, type KBRepo, type RunFinal, type SecretList } from "../../wire";
import { EmptyState } from "../../ui/EmptyState";
import { Markdown } from "../../ui/Markdown";
import { GitBranch, NotebookPen, Upload } from "lucide-react";
import { askTo } from "../answerbank/model";
import { GitHubSignIn, RepoPicker, useGitHubAccount } from "./GitHub";

// ADD_PAGE_PROMPT starts a message that adds to the wiki.
const ADD_PAGE_PROMPT = "Add to my wiki: ";

const CATEGORIES = ["projects", "experience", "answer-patterns"] as const;
// A run that reads a repository or writes drafts announces "runs" on each
// step; the drafts and the repositories read once the notices settle.
const RUNS_SETTLE_MS = 1000;
const splitTags = (s: string) => s.split(",").map((t) => t.trim()).filter(Boolean);

// useTextMatches asks the server which documents hold every word of q in their
// text too (GET /api/kb/documents?q=), a moment after the typing stops; null
// until it answers for this q, and failed when it could not.
type TextMatches = { ids: Set<string>; failed: boolean } | null;
export function useTextMatches(q: string): TextMatches {
  const [hits, setHits] = useState<{ q: string; ids: Set<string>; failed: boolean }>();
  useEffect(() => {
    if (q === "") return;
    let live = true;
    const t = setTimeout(() => void get("/api/kb/documents", kbDocumentList, { q }).then((l) => ({ ids: new Set(l.entries.map((e) => e.id)), failed: false }), () => ({ ids: new Set<string>(), failed: true })).then((r) => live && setHits({ q, ...r })), 250);
    return () => ((live = false), clearTimeout(t));
  }, [q]);
  return hits?.q === q ? hits : null;
}

// TextUnsearched is the quiet note when the text search did not answer.
export function TextUnsearched({ matches }: { matches: TextMatches }) {
  return matches?.failed ? <p className="muted" role="status">The text could not be searched just now; only titles and tags were.</p> : null;
}

function Create({ onCreated }: { onCreated: (id: string) => void }) {
  const [f, setF] = useState({ title: "", category: "", tags: "", body: "" });
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState("");
  const ids = [useId(), useId(), useId(), useId()];
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setSaid("");
    try {
      const doc = await post("/api/kb/documents", kbDocument, { title: f.title.trim(), category: f.category || undefined, tags: splitTags(f.tags), body: f.body });
      setSaid(`Added ${f.title.trim()}. It is marked New in the list above.`);
      setF({ title: "", category: "", tags: "", body: "" });
      onCreated(doc.entry.id);
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} className="form-grid" aria-label="New page">
      <h3>New page</h3>
      <div>
        <label htmlFor={ids[0]}>Title</label>
        <input id={ids[0]} value={f.title} onChange={(e) => setF({ ...f, title: e.target.value })} />
      </div>
      <div>
        <label htmlFor={ids[1]}>Category</label>
        <select id={ids[1]} value={f.category} onChange={(e) => setF({ ...f, category: e.target.value })}>
          <option value="">None</option>
          {CATEGORIES.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
      </div>
      <div>
        <label htmlFor={ids[2]}>Tags, separated by commas</label>
        <input id={ids[2]} value={f.tags} onChange={(e) => setF({ ...f, tags: e.target.value })} />
      </div>
      <div>
        <label htmlFor={ids[3]}>Text</label>
        <textarea id={ids[3]} className="editor" value={f.body} onChange={(e) => setF({ ...f, body: e.target.value })} />
      </div>
      <SaveBar dirty={Object.values(f).some((v) => v.trim() !== "")} savedAt="">
        <button type="submit" disabled={busy || f.title.trim() === "" || f.body.trim() === ""}>
          Add page
        </button>
      </SaveBar>
      {error != null && <ErrorNote error={error} />}
      <div aria-live="polite">{said && <p className="note ok">{said}</p>}</div>
    </form>
  );
}

// ImportFile turns a dropped or chosen file into a draft at once; nothing is
// searched until the draft is published from Drafts to review.
function ImportFile({ onCreated }: { onCreated: () => void }) {
  const [said, setSaid] = useState("");
  const [over, setOver] = useState(false);
  const picker = useRef<HTMLInputElement>(null);
  const act = useAction();
  const add = async (file: File | undefined) => {
    if (!file || act.busy) return;
    setSaid("");
    const d = await act.run(async () => {
      if (file.size > 5 * 1024 * 1024) throw new Error("Choose a file smaller than 5 MiB.");
      return request("POST", "/api/kb/imports", kbDraft, { file, query: { name: file.name } });
    });
    if (d) (setSaid(`Imported ${d.snippetId} as a draft. It waits in Drafts to review above.`), onCreated());
  };
  return <section aria-label="Import knowledge" className="form-grid">
    <h3>Import knowledge</h3>
    <div role="group" aria-label="Import a file" aria-busy={act.busy} className="dropzone" data-over={over || undefined}
      onDragOver={(e) => (e.preventDefault(), setOver(true))} onDragLeave={() => setOver(false)} onDrop={(e) => (e.preventDefault(), setOver(false), void add(e.dataTransfer.files[0]))}>
      <Upload aria-hidden="true" />
      <p>Drop a PDF, Markdown or text file here, or choose one (up to 5 MiB). It becomes one draft to review; nothing is searched until you publish it.</p>
      <button type="button" disabled={act.busy} onClick={() => picker.current?.click()}>{act.busy ? "Importing..." : "Choose a file"}</button>
      <input ref={picker} type="file" accept=".pdf,.md,.markdown,.txt" className="sr-only" tabIndex={-1} aria-label="Knowledge file" onChange={(e) => { void add(e.target.files?.[0]); e.target.value = ""; }} />
    </div>
    <div aria-live="polite">{said && <p className="note ok">{said}</p>}{act.error && <ErrorNote error={act.error} />}</div>
  </section>;
}

const ORIGINS: Record<string, string> = { import: "Imported file", repo: "GitHub source", mining: "Story interview" };

// Themes picks a story's competency themes; publishing needs two.
export function Themes({ themes, picked, onChange, legend = "Competency themes", disabled }: { themes: KBPrincipleGrade[]; picked: string[]; onChange: (p: string[]) => void; legend?: string; disabled?: boolean }) {
  return (
    <fieldset disabled={disabled}>
      <legend>{legend}</legend>
      <div className="chips">
        {themes.map((t) => (
          <label key={t.id} className="check">
            <input type="checkbox" checked={picked.includes(t.id)} onChange={(e) => onChange(e.target.checked ? [...picked, t.id] : picked.filter((p) => p !== t.id))} />
            {t.name}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

// Draft is one draft in the review queue. Nothing in it is searched until it is
// published, and a pending statement reaches allowed_facts only once confirmed.
function Draft({ d, themes, go, busy, reject }: { d: KBDraft; themes: KBPrincipleGrade[]; go: (fn: () => Promise<unknown>, done: string) => Promise<boolean>; busy: boolean; reject: () => void }) {
  // base is the text the edit started from; a newer revision under it waits for the user's choice.
  const [edit, setEdit] = useState<{ text: string; base: string } | null>(null);
  const text = edit?.text ?? null;
  const stale = edit !== null && edit.base !== d.markdown;
  const name = d.snippetId;
  const pending = d.facts.filter((f) => f.pending).length;
  const confirm = (i: number) => go(() => post(`/api/kb/drafts/${d.id}/confirm`, kbDraft, { facts: [i] }), `Confirmed a statement of ${name}.`);
  // A story publishes with two themes or more (the story gates); a draft not
  // from the story interview picks them here instead of in its front matter.
  const story = d.kind === "story";
  const short = story ? 2 - new Set(d.principles).size : 0;
  return <li className="panel">
    <div className="row"><strong>{name}</strong><Chip>{ORIGINS[d.origin] ?? humanize(d.origin)}</Chip>{pending > 0 && <Chip tone="warn">{pending} to confirm</Chip>}</div>
    <p className="muted">Not searched until published.</p>
    {edit === null ? <div className="draft-text" role="region" aria-label={`Text of ${name}`} tabIndex={0}>{d.body ? <Markdown source={d.body} /> : <p className="muted">No text yet.</p>}</div>
      : <textarea aria-label={`Text of ${name}`} className="editor" rows={Math.min(18, d.markdown.split("\n").length + 1)} value={edit.text} onChange={(e) => setEdit({ ...edit, text: e.target.value })} />}
    {d.facts.length > 0 && <ul className="plain item-list" aria-label={`Statements of ${name}`}>{d.facts.map((f, i) => <li key={i}>
      {f.pending && <Chip tone="warn">Unconfirmed</Chip>}
      <span className="item-main">{f.text}<span className="item-detail">{humanize(f.origin)}</span></span>
      {f.pending && <button type="button" className="small" disabled={busy} onClick={() => void confirm(i)} aria-label={`Confirm: ${f.text}`}>Confirm</button>}
    </li>)}</ul>}
    {story && themes.length > 0 && <Themes themes={themes} picked={d.principles ?? []} legend={`Themes of ${name}, at least two`} disabled={edit !== null}
      onChange={(principles) => !busy && void go(() => patch(`/api/kb/drafts/${d.id}`, kbDraft, { principles }), `Saved the themes of ${name}.`)} />}
    {stale && <Changed onKeep={() => setEdit({ ...edit, base: d.markdown })} onLoad={() => setEdit(null)} />}
    {edit !== null ? <SaveBar dirty={edit.text !== edit.base} savedAt="">
      <button type="button" className="small" onClick={() => leave(() => setEdit(null))}>Cancel</button>
      <button type="button" className="small primary" disabled={busy || stale || text === d.markdown} onClick={async () => (await go(() => patch(`/api/kb/drafts/${d.id}`, kbDraft, { markdown: text }), `Saved ${name}.`)) && setEdit(null)}>Save draft</button>
    </SaveBar> : <div className="actions">
      <button type="button" className="small primary" disabled={busy || short > 0} onClick={() => void go(() => post(`/api/kb/drafts/${d.id}/publish`, kbDocument), `Published ${name}.`)}>Publish</button>
      {short > 0 && <span className="muted">Pick {short === 1 ? "one more theme" : "two themes"} to publish.</span>}
      <button type="button" className="small" onClick={() => setEdit({ text: d.markdown, base: d.markdown })}>Edit</button>
      <button type="button" className="small danger-outline" disabled={busy} onClick={reject} aria-label={`Reject ${name}`}>Reject</button>
    </div>}
  </li>;
}

// Gone is a knowledge delete the page offers to take back: the server holds
// what it removed for a while (kb.UndoWindow) under the token undo.
export type Gone = { undo: string; done: string; undone: string };

// UndoDelete is the note after a knowledge delete. Undo restores the item, and
// onBack reloads what shows it.
export function UndoDelete({ gone, onBack }: { gone: Gone; onBack: () => void }) {
  const act = useAction();
  const undo = async () => {
    const ok = (await act.run(() => post("/api/kb/restore", kbRestored, { undo: gone.undo }))) !== undefined;
    if (ok) onBack();
    return ok;
  };
  return <><UndoNote done={gone.done} undone={gone.undone} onUndo={undo} />{act.error && <ErrorNote error={act.error} />}</>;
}

// DraftQueue lists every unpublished draft: imports, GitHub sources, mined stories.
function DraftQueue({ res, reload, onChanged }: { res: Resource<KBDraftList>; reload: () => void; onChanged: () => void }) {
  const stories = !!res.data?.drafts.some((d) => d.kind === "story");
  const [cov] = useResource(() => (stories ? get("/api/kb/stories/coverage", kbCoverage) : Promise.resolve(null)), [stories]);
  const [doomed, setDoomed] = useState<KBDraft | null>(null);
  const [said, setSaid] = useState("");
  const [gone, setGone] = useState<Gone | null>(null);
  const act = useAction();
  const go = async (fn: () => Promise<unknown>, done: string) => {
    setSaid("");
    setGone(null);
    if ((await act.run(fn)) === undefined) return false;
    setSaid(done);
    reload();
    onChanged();
    return true;
  };
  const reject = async (d: KBDraft) => {
    setSaid("");
    const r = await act.run(() => del(`/api/kb/drafts/${d.id}`, kbUndo));
    if (!r) return;
    setGone({ undo: r.undo, done: `Rejected ${d.snippetId}.`, undone: `${d.snippetId} is back in the drafts.` });
    reload();
    onChanged();
  };
  return <Panel title="Drafts to review" id="kb-drafts" res={res} retry={reload} what="the drafts" actions={!!res.data?.drafts.length && <Chip tone="warn">{res.data.drafts.length}</Chip>}>
    {({ drafts }) => <>
      <div aria-live="polite">{said && <p className="note ok">{said}</p>}{gone && <UndoDelete key={gone.undo} gone={gone} onBack={() => (reload(), onChanged())} />}{act.error && <ErrorNote error={act.error} />}</div>
      {drafts.length === 0 ? <Empty>Nothing waits for review. Imports and GitHub sources land here as drafts.</Empty>
        : <ul className="plain stack">{drafts.map((d) => <Draft key={d.snippetId} d={d} themes={cov.data?.principles ?? []} go={go} busy={act.busy} reject={() => setDoomed(d)} />)}</ul>}
      {doomed && <ConfirmDialog title={`Reject ${doomed.snippetId}`} consequence="The draft and its revisions are deleted. You can undo it for a few minutes." verb="Reject draft"
        onCancel={() => setDoomed(null)} onConfirm={() => { const d = doomed; setDoomed(null); void reject(d); }} />}
    </>}
  </Panel>;
}

// Editor edits a published snippet or story in place: its title, tags and text.
export function Editor({ entry, onClose, onSaved, noun = "page" }: { entry: KBDocEntry; onClose: () => void; onSaved: () => void; noun?: string }) {
  const [res, reload] = useResource(() => get(`/api/kb/documents/${encodeURIComponent(entry.id)}`, kbDocument), [entry.id]);
  const [saved, setSaved] = useState("");
  // Close hands the keyboard back to the Edit button that opened the editor.
  const opener = useRef(openerOf());
  const close = () => {
    onClose();
    if (opener.current instanceof HTMLElement) opener.current.focus();
  };
  return (
    <Panel title={`Edit ${entry.title || entry.id}`} id={`kb-edit-${entry.id}`} res={res} retry={reload} actions={<button type="button" onClick={() => leave(close)}>Close</button>}>
      {(doc) => <EditForm key={doc.entry.fileHash} doc={doc} noun={noun} savedAt={saved} reload={reload} onSaved={() => (setSaved(new Date().toLocaleTimeString()), onSaved())} />}
    </Panel>
  );
}

// Previous shows the one text the last save replaced. Opening it puts it in
// the form as an unsaved edit, so bringing it back is a save like any other.
function Previous({ id, onOpen }: { id: string; onOpen: (p: KBPrevious) => void }) {
  const [open, setOpen] = useState(false);
  const [opened, setOpened] = useState(false);
  const [res, reload] = useResource(() => (open ? get(`/api/kb/documents/${encodeURIComponent(id)}/previous`, kbPrevious) : Promise.resolve(null)), [open, id]);
  return (
    <details className="earlier" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>Earlier version</summary>
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />}
      {res.data && (
        <>
          <p className="muted">Replaced {when(res.data.savedAt)}. Only the text before the last save is kept.</p>
          <strong>{res.data.title}</strong>
          <p className="pre">{res.data.body}</p>
          <button type="button" className="small" onClick={() => (onOpen(res.data!), setOpened(true))}>
            Open in the editor
          </button>
          {opened && <p className="muted" role="status">It is in the editor; save to bring it back.</p>}
        </>
      )}
    </details>
  );
}

function EditForm({ doc, noun, savedAt, reload, onSaved }: { doc: KBDocument; noun: string; savedAt: string; reload: () => void; onSaved: () => void }) {
  const [title, setTitle] = useState(doc.entry.title);
  const [tags, setTags] = useState(doc.entry.tags.join(", "));
  const [body, setBody] = useState(doc.body);
  // base is the version a save replaces: the one opened, or a newer one the user chose to save over.
  const [base, setBase] = useState(doc);
  const unchanged = title === base.entry.title && tags === base.entry.tags.join(", ") && body === base.body;
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const url = `/api/kb/documents/${encodeURIComponent(doc.entry.id)}`;
  const keep = () => get(url, kbDocument).then((d) => (setBase(d), setError(null)), (e) => setError(asApiError(e)));
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await patch(url, kbDocument, { expectedHash: base.entry.fileHash, title, tags: splitTags(tags), body });
      reload();
      onSaved();
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} className="form-grid">
      <label>
        Title
        <input value={title} onChange={(e) => setTitle(e.target.value)} />
      </label>
      <label>
        Tags, separated by commas
        <input value={tags} onChange={(e) => setTags(e.target.value)} />
      </label>
      <label>
        Text
        <textarea className="editor tall" value={body} onChange={(e) => setBody(e.target.value)} />
      </label>
      {doc.hasPrevious && <Previous id={doc.entry.id} onOpen={(p) => (setTitle(p.title), setTags(p.tags.join(", ")), setBody(p.body))} />}
      {error?.kind === "revision_conflict" ? <Changed onKeep={() => void keep()} onLoad={reload} /> : error && <ErrorNote error={error} />}
      <SaveBar dirty={!unchanged} savedAt={savedAt}>
        <button type="submit" disabled={busy || unchanged}>
          Save {noun}
        </button>
      </SaveBar>
    </form>
  );
}

const REPO_STATE: Record<string, { label: string; tone: "good" | "bad" | "neutral" }> = {
  ingested: { label: "Read", tone: "good" },
  failed: { label: "Failed", tone: "bad" },
  added: { label: "Not read yet", tone: "neutral" },
};
const LEFT_OUT_SHOWN = 50;

// LeftOut lists the files an ingestion did not use: secrets by path and kind
// only, then the files it skipped with the reason.
function LeftOut({ report }: { report: KBIngestReport }) {
  const rows = [...report.redacted.map((f) => ({ ...f, reason: `holds a secret (${f.reason})` })), ...report.skipped];
  if (rows.length === 0) return null;
  return <details className="earlier">
    <summary>{report.redacted.length > 0 ? `${rows.length} files left out, ${report.redacted.length} for secrets` : `${rows.length} files skipped`}</summary>
    <ul className="plain">{rows.slice(0, LEFT_OUT_SHOWN).map((f) => <li key={f.path} className="muted">{f.path}: {f.reason}</li>)}</ul>
    {rows.length > LEFT_OUT_SHOWN && <p className="muted">and {rows.length - LEFT_OUT_SHOWN} more</p>}
  </details>;
}

// Repository is one source with when it was last read, what that produced and
// the actions on it.
function Repository({ r, busy, run, final, ingest, remove }: { r: KBRepo; busy: boolean; run: string | null; final?: RunFinal; ingest: (force: boolean) => void; remove: () => void }) {
  const reading = run !== null && !final;
  const upToDate = final?.outcome === "up-to-date";
  const name = `${r.owner}/${r.name}`;
  const state = REPO_STATE[r.status] ?? { label: humanize(r.status), tone: "neutral" as const };
  const rep = r.report;
  return <li className="panel">
    <div className="row">
      <strong><a href={`https://github.com/${name}`} target="_blank" rel="noreferrer noopener">{name}</a></strong>
      <Chip tone={state.tone}>{state.label}</Chip>
    </div>
    {r.status === "ingested" && <p className="muted">Read {when(r.ingestedAt)} at {r.commitSha.slice(0, 7)}{r.branch && ` on ${r.branch}`}: {rep.read} {rep.read === 1 ? "file" : "files"} used ({rep.reused} unchanged), {rep.skipped.length} skipped, {rep.redacted.length} left out for secrets. It wrote {rep.drafts.length === 0 ? "no drafts" : <a href="#kb-drafts">{rep.drafts.length} {rep.drafts.length === 1 ? "draft" : "drafts"}</a>}.</p>}
    {r.status === "failed" && <p className="muted">The last attempt failed {when(r.ingestedAt)}.</p>}
    {r.status === "added" && <p className="muted">Nothing has been read from it yet.</p>}
    {r.status !== "added" && rep.notes.length > 0 && <p className="note">{rep.notes.join(" ")}</p>}
    {r.status === "ingested" && <LeftOut report={rep} />}
    <div aria-live="polite">
      {reading && <p className="muted" role="status">Reading {name} from GitHub. It goes on if you leave this page. <Link to={`/runs/${run}`}>Follow the run</Link></p>}
      {upToDate && <p className="note ok">Already up to date; nothing new was read.</p>}
      {final?.status === "failed" && final.reason !== rep.notes.join(" ") && <p className="note">{final.reason}</p>}
      {final?.status === "cancelled" && <p className="muted">The read was cancelled.</p>}
    </div>
    <div className="actions">
      <button type="button" className="small primary" disabled={busy || reading} onClick={() => ingest(false)}>{reading ? "Reading..." : "Ingest now"}</button>
      {upToDate && <button type="button" className="small" disabled={busy || reading} onClick={() => ingest(true)}>Read again anyway</button>}
      <button type="button" className="small danger-outline" disabled={busy || reading} onClick={remove} aria-label={`Remove ${name}`}>Remove</button>
    </div>
  </li>;
}

// GitHubToken stores the optional token ingestion sends with its requests. The
// value is written once and never shown again.
function GitHubToken({ res, reload }: { res: Resource<SecretList>; reload: () => void }) {
  const field = useId();
  const input = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [said, setSaid] = useState("");
  const act = useAction();
  const entry = res.data?.entries.find((e) => e.name === "github-token");
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const value = input.current?.value.trim() ?? "";
    if (!value) return;
    setSaid("");
    if (await act.run(() => put("/api/secrets/github-token", secretStatus, { value }))) {
      input.current!.value = "";
      input.current!.focus();
      setTyped(false);
      setSaid("Token saved.");
      reload();
    }
  };
  const remove = async () => {
    setRemoving(false);
    setSaid("");
    if (await act.run(() => del("/api/secrets/github-token", secretStatus))) (setSaid("Token removed."), input.current?.focus(), reload());
  };
  if (res.status === "error") return <ErrorNote error={res.error} onRetry={reload} what="the GitHub token setting" />;
  if (!entry) return null;
  return <form onSubmit={save} className="form-grid" aria-label="GitHub token">
    <h3>GitHub token <Chip tone={entry.present ? "good" : "neutral"}>{entry.present ? (entry.source === "env" ? "Set in the environment" : "Stored") : "Not stored"}</Chip></h3>
    <p className="muted">{entry.present ? "Sent with every request to GitHub, so private repositories can be read and the rate limit is higher." : "Optional. A public repository is read without one, within GitHub's low limit for anonymous requests. A private repository needs a token with read access, or the gh command signed in on this computer."} A stored token is never shown again.</p>
    <div>
      <label htmlFor={field}>{entry.present ? "New token" : "Token"}</label>
      <input id={field} ref={input} type="password" autoComplete="off" spellCheck={false} onChange={(e) => setTyped(e.target.value.trim() !== "")} />
    </div>
    <div className="actions">
      <button type="submit" disabled={act.busy || !typed}>{entry.present ? "Replace token" : "Save token"}</button>
      {entry.present && entry.source !== "env" && <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRemoving(true)}>Remove token</button>}
    </div>
    <div aria-live="polite">{said && <p className="note ok">{said}</p>}{act.error && <ErrorNote error={act.error} />}</div>
    {removing && <ConfirmDialog title="Remove the GitHub token" consequence="Private repositories cannot be read again until a token is stored, and public ones fall back to GitHub's limit for anonymous requests (unless the gh command is signed in). Repositories already read are not affected." verb="Remove token" onCancel={() => setRemoving(false)} onConfirm={() => void remove()} />}
  </form>;
}

// Sources lists the GitHub repositories drafts are made from. Ingesting starts
// a background run that goes on without this page; what it writes lands in
// Drafts to review, never in search.
function Sources({ onDrafts }: { onDrafts: () => void }) {
  const [res, reload] = useResource(() => get("/api/kb/sources", kbRepoList), [], [], { keep: "knowledge.sources" });
  useTopicsSettled(["runs"], RUNS_SETTLE_MS, reload);
  const [token, reloadToken] = useResource(() => get("/api/secrets", secretList), [], [], { keep: "knowledge.github-token" });
  const [repo, setRepo] = useState("");
  const box = useRef<HTMLInputElement>(null);
  const [doomed, setDoomed] = useState<KBRepo | null>(null);
  const [run, setRun] = useState<{ repo: number; id: string } | null>(null);
  const { final } = useRunLines(run?.id, () => (reload(), onDrafts()));
  // A read that began before this page opened is followed like one started here.
  const running = res.data?.repos.find((r) => r.readingRunId);
  useEffect(() => {
    if (running && !run) setRun({ repo: running.id, id: running.readingRunId! });
  }, [running?.readingRunId]);
  const [said, setSaid] = useState("");
  const field = useId();
  const act = useAction();
  const adding = useAction();
  const picking = useAction();
  const account = useGitHubAccount();
  // The account's repositories are a live GitHub read, so they are listed when
  // asked for, not on every visit to the page.
  const [listing, setListing] = useState(false);
  const add = async (e: FormEvent) => {
    e.preventDefault();
    setSaid("");
    const r = await adding.run(() => post("/api/kb/sources", kbRepoAdded, { repo: repo.trim() }));
    if (!r) return;
    setRepo("");
    box.current?.focus();
    setSaid(`Added ${r.owner}/${r.name}. Press "Ingest now" to read it.`);
    reload();
  };
  const ingest = async (r: KBRepo, force: boolean) => {
    setSaid("");
    setRun(null);
    const out = await act.run(() => request("POST", `/api/kb/sources/${r.id}/ingest`, runAccepted, { body: { force }, headers: { "Idempotency-Key": newRequestId() } }));
    if (out) setRun({ repo: r.id, id: out.runId });
  };
  // A repository picked from the account is added, then read, as one step.
  const readPicked = async (fullName: string) => {
    setSaid("");
    setRun(null);
    const r = await picking.run(() => post("/api/kb/sources", kbRepoAdded, { repo: fullName }));
    if (!r) return;
    reload();
    await ingest(r, false);
  };
  const [gone, setGone] = useState<Gone | null>(null);
  const remove = async (r: KBRepo) => {
    setSaid("");
    setGone(null);
    const out = await act.run(() => del(`/api/kb/sources/${r.id}`, kbUndo));
    if (out) {
      setGone({ undo: out.undo, done: `Removed ${r.owner}/${r.name}.`, undone: `${r.owner}/${r.name} is back, with its drafts.` });
      reload();
      onDrafts();
    }
  };
  return <Panel title="GitHub repositories" id="kb-sources" res={res} retry={reload} what="the repositories">
    {({ repos }) => <>
      <p className="muted">Ingesting reads a repository at its newest commit, once, and writes drafts from its README, docs and code. Nothing is searched until you publish a draft.</p>
      <div aria-live="polite">{said && <p className="note ok">{said}</p>}{gone && <UndoDelete key={gone.undo} gone={gone} onBack={() => (reload(), onDrafts())} />}{act.error && <ErrorNote error={act.error} />}</div>
      <GitHubSignIn account={account} />
      {account.status?.state === "signed-in" && (listing
        ? <RepoPicker key={`${account.status.source}:${account.status.login}`} added={new Set(repos.map((r) => `${r.owner}/${r.name}`))} busy={act.busy || picking.busy} onRead={(name) => void readPicked(name)} />
        : <div className="actions"><button type="button" onClick={() => setListing(true)}>Choose one of your repositories</button></div>)}
      <div aria-live="polite">{picking.error && <ErrorNote error={picking.error} />}</div>
      <hr />
      {repos.length === 0 ? <EmptyState icon={GitBranch} title="No repository yet.">Pick one above or add one below to turn what it holds into drafts of pages.</EmptyState>
        : <ul className="plain stack">{repos.map((r) => <Repository key={r.id} r={r} busy={act.busy} run={run?.repo === r.id ? run.id : null} final={run?.repo === r.id ? final : undefined} ingest={(force) => void ingest(r, force)} remove={() => setDoomed(r)} />)}</ul>}
      <hr />
      <form onSubmit={add} className="form-grid" aria-label="Add a repository">
        <h3>Add a repository</h3>
        <div>
          <label htmlFor={field}>Repository, as owner/repo or its GitHub address</label>
          <input id={field} ref={box} value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="owner/repo" autoCapitalize="none" spellCheck={false} />
        </div>
        <div className="actions"><button type="submit" disabled={adding.busy || repo.trim() === ""}>Add repository</button></div>
        {adding.error && <ErrorNote error={adding.error} />}
      </form>
      <hr />
      <GitHubToken res={token} reload={reloadToken} />
      {doomed && <ConfirmDialog title={`Remove ${doomed.owner}/${doomed.name}`} verb="Remove repository"
        consequence="The repository stops being a source. The file text read from it and its drafts that still wait for review are deleted. Pages you already published from it stay, and GitHub is not changed. You can undo it for a few minutes."
        onCancel={() => setDoomed(null)} onConfirm={() => { const gone = doomed; setDoomed(null); void remove(gone); }} />}
    </>}
  </Panel>;
}

// SnippetText shows a snippet's text to read, without opening the editor.
function SnippetText({ id }: { id: string }) {
  const [res, reload] = useResource(() => get(`/api/kb/documents/${encodeURIComponent(id)}`, kbDocument), [id]);
  return res.status === "error" ? <ErrorNote error={res.error} onRetry={reload} /> : res.data ? <Markdown source={res.data.body} /> : null;
}

export function Pages() {
  const [, setParams] = useSearchParams();
  const [res, reload] = useResource(() => get("/api/kb/documents", kbDocumentList), []);
  const [drafts, reloadDrafts] = useResource(() => get("/api/kb/drafts", kbDraftList), []);
  useTopicsSettled(["runs"], RUNS_SETTLE_MS, reloadDrafts);
  const [query, setQuery] = useState("");
  const inText = useTextMatches(query.trim());
  const [order, setOrder] = useState<"newest" | "title">("newest");
  // The snippet just added is marked and brought into view once it is listed.
  const [fresh, setFresh] = useState<string>();
  const toScroll = useRef(false);
  const freshRow = useCallback((el: HTMLLIElement | null) => {
    if (!el || !toScroll.current) return;
    toScroll.current = false;
    el.scrollIntoView?.({ block: "center" });
  }, []);
  const [editing, setEditing] = useState<KBDocEntry | null>(null);
  const [reading, setReading] = useState<string | null>(null);
  const [doomed, setDoomed] = useState<KBDocEntry | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const [said, setSaid] = useState("");
  const [gone, setGone] = useState<Gone | null>(null);
  const [error, setError] = useState<unknown>(null);

  const copy = async (e: KBDocEntry) => {
    setError(null);
    try {
      const doc = await get(`/api/kb/documents/${encodeURIComponent(e.id)}`, kbDocument);
      await navigator.clipboard.writeText(doc.body);
      setSaid(`Copied ${e.title || e.id}.`);
    } catch (err) {
      setSaid("");
      setError(err instanceof TypeError ? new Error("Copying is not available in this browser.") : err);
    }
  };
  const remove = async (e: KBDocEntry) => {
    setError(null);
    setRemoving(e.id);
    try {
      const { undo } = await del(`/api/kb/documents/${encodeURIComponent(e.id)}`, kbUndo);
      setSaid("");
      setGone({ undo, done: `Deleted ${e.title || e.id}.`, undone: `${e.title || e.id} is back.` });
      if (editing?.id === e.id) setEditing(null);
      reload();
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setRemoving(null);
    }
  };

  return (
    <>
    <DraftQueue res={drafts} reload={reloadDrafts} onChanged={reload} />
    <Panel title="Pages" id="kb-snippets" res={res} retry={reload} actions={<Link className="button" to={askTo(ADD_PAGE_PROMPT)} title="Tell the Ask Agent what to add; it finds the page it belongs on and shows you the new text before saving">Add with Ask</Link>}>
      {(list) => {
        const q = query.trim().toLowerCase();
        const snippets = list.entries.filter((e) => e.kind === KBDocSnippet && e.category !== "behavioral");
        const named = (e: KBDocEntry) => [e.title, e.id, e.description, ...e.tags].some((s) => s.toLowerCase().includes(q));
        const titleOf = (e: KBDocEntry) => e.title || e.id;
        const shown = snippets.filter((e) => q === "" || named(e) || inText?.ids.has(e.id))
          .sort((a, b) => order === "title" ? titleOf(a).localeCompare(titleOf(b)) : Date.parse(b.modifiedAt) - Date.parse(a.modifiedAt));
        return (
          <div>
            <div className="toolbar">
              <label className="search-field">
                Search pages by title, tag or text
                <input type="search" data-page-search value={query} onChange={(e) => setQuery(e.target.value)} />
              </label>
              <label>
                Sort
                <select value={order} onChange={(e) => setOrder(e.target.value as "newest" | "title")}>
                  <option value="newest">Newest first</option>
                  <option value="title">Title</option>
                </select>
              </label>
              <span className="count-line">
                {q === "" ? snippets.length : `${shown.length} of ${snippets.length}`} {snippets.length === 1 ? "page" : "pages"}
              </span>
            </div>
            <div aria-live="polite">
              {said && <p className="note ok">{said}</p>}
              {gone && <UndoDelete key={gone.undo} gone={gone} onBack={reload} />}
              {error != null && <ErrorNote error={error} />}
            </div>
            {q !== "" && <TextUnsearched matches={inText} />}
            {shown.length === 0 ? (
              <EmptyState icon={NotebookPen} title={snippets.length === 0 ? "No page yet." : inText === null ? "Searching the text of your pages..." : "No page matches the search."}>
                {snippets.length === 0 && "A page is a short write-up of a project or an experience that evaluations, cover letters and answers draw on. Import a file or write one below."}
              </EmptyState>
            ) : (
              <ul className="plain stack">
                {shown.map((e) => (
                  <li key={e.id} className="panel" ref={e.id === fresh ? freshRow : undefined} data-fresh={e.id === fresh || undefined}>
                    <div className="row">
                      <strong>{e.title || "Untitled page"}</strong>
                      {e.id === fresh && <Chip tone="good">New</Chip>}
                      {q !== "" && !named(e) && <Chip>Found in the text</Chip>}
                      {e.category && <Chip>{humanize(e.category)}</Chip>}
                    </div>
                    {e.description && <p className="muted clamp">{e.description}</p>}
                    <p className="muted">Edited {ageLabel(e.modifiedAt)}</p>
                    {e.tags.length > 0 && <p className="chips">{e.tags.map((t) => <Chip key={t} raw>{t}</Chip>)}</p>}
                    {e.lint.length > 0 && <ul className="note">{e.lint.map((l) => <li key={l.code + l.message}>{l.message}</li>)}</ul>}
                    <div className="actions">
                      <button type="button" className="small" onClick={() => setParams({ page: e.id })}>Open</button>
                      <button type="button" className="small" aria-expanded={reading === e.id} onClick={() => setReading(reading === e.id ? null : e.id)}>{reading === e.id ? "Hide the text" : "Show the text"}</button>
                      <button type="button" className="small" aria-expanded={editing?.id === e.id} onClick={() => leave(() => setEditing(editing?.id === e.id ? null : e))}>Edit</button>
                      <button type="button" className="small" onClick={() => void copy(e)}>Copy</button>
                      <button type="button" className="small danger-outline" disabled={removing === e.id} onClick={() => setDoomed(e)} aria-label={`Delete ${e.title || e.id}`}>Delete</button>
                    </div>
                    {reading === e.id && <SnippetText id={e.id} />}
                    {editing?.id === e.id && <Editor key={e.id} entry={e} onClose={() => setEditing(null)} onSaved={reload} />}
                  </li>
                ))}
              </ul>
            )}
            <hr />
            <ImportFile onCreated={reloadDrafts} />
            <hr />
            {/* A search that would hide the new page is cleared. */}
            <Create onCreated={(id) => ((toScroll.current = true), setFresh(id), setQuery(""), reload())} />
            {doomed && (
              <ConfirmDialog
                title={`Delete ${doomed.title || doomed.id}`}
                consequence="The page file and everything the search index holds from it are removed. You can undo it for a few minutes."
                verb="Delete page"
                onCancel={() => setDoomed(null)}
                onConfirm={() => {
                  const gone = doomed;
                  setDoomed(null);
                  void remove(gone);
                }}
              />
            )}
          </div>
        );
      }}
    </Panel>
    <Sources onDrafts={reloadDrafts} />
    </>
  );
}
