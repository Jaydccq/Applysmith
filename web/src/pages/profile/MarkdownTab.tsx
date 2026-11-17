import { ArrowDown, ArrowUp, Check, Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { ApiError, get, put, upload } from "../../api/client";
import { cvExtracted } from "../../api/decoders_setup";
import { configBackupList, configDocument, configSaved } from "../../api/decoders_pages";
import { asApiError, useResource } from "../../hooks";
import { Markdown } from "../../ui/Markdown";
import { ConfirmDialog } from "../../ui/Modal";
import { Changed, SaveBar } from "../../ui/SaveBar";
import { Panel, ErrorNote, formatDateTime } from "../../ui/parts";
import type { ConfigDocument } from "../../wire";
import { CvForm, CvSheet, formDrops, GrowArea } from "./CvSheet";
import { ExampleMarker } from "./shared";

const MAX_BYTES = 256 * 1024;

type Structured = "cv" | "sections";
type Mode = "structured" | "markdown";

// A Part is the text before the first level-two heading (head null) or one
// level-two heading with the lines under it. Joining the parts gives the
// document back byte for byte, so a part nobody edits never changes.
interface Part {
  id: number;
  head: string | null;
  lines: string[];
}

let lastPart = 0;

function splitParts(source: string): Part[] {
  const parts: Part[] = [{ id: ++lastPart, head: null, lines: [] }];
  let fence = false;
  for (const line of source.split("\n")) {
    if (/^\s*```/.test(line)) fence = !fence;
    if (!fence && /^##\s/.test(line)) parts.push({ id: ++lastPart, head: line, lines: [] });
    else parts[parts.length - 1].lines.push(line);
  }
  if (parts[0].lines.length === 0 && parts.length > 1) parts.shift();
  return parts;
}

const joinParts = (parts: Part[]) => parts.flatMap((p) => (p.head === null ? p.lines : [p.head, ...p.lines])).join("\n");

const headText = (head: string) => head.replace(/^##\s+/, "").replace(/\s+#+\s*$/, "").trim();
const withoutComments = (text: string) => text.replace(/<!--[\s\S]*?(-->|$)/g, "");

// A Chunk is a run of text or one Markdown table inside a part being edited.
type Chunk = { kind: "text"; text: string } | { kind: "table"; rows: string[][] };

const isTableLine = (l: string) => /^\s*\|.*\|\s*$/.test(l);
const rowCells = (l: string) => l.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
const isSepLine = (l: string) => l.includes("-") && rowCells(l).every((c) => /^:?-{3,}:?$/.test(c));

function chunksOf(lines: string[]): Chunk[] {
  const out: Chunk[] = [];
  let text: string[] = [];
  const flush = () => {
    if (text.length > 0) out.push({ kind: "text", text: text.join("\n") });
    text = [];
  };
  for (let i = 0; i < lines.length; i++) {
    if (isTableLine(lines[i]) && i + 1 < lines.length && isSepLine(lines[i + 1])) {
      flush();
      const rows = [rowCells(lines[i])];
      i += 2;
      while (i < lines.length && isTableLine(lines[i])) rows.push(rowCells(lines[i++]));
      i--;
      const width = Math.max(...rows.map((r) => r.length));
      out.push({ kind: "table", rows: rows.map((r) => [...r, ...Array<string>(width - r.length).fill("")]) });
    } else {
      text.push(lines[i]);
    }
  }
  flush();
  return out;
}

const chunkLines = (c: Chunk): string[] =>
  c.kind === "text" ? c.text.split("\n") : c.rows.flatMap((r, i) => {
    const line = `| ${r.map((x) => x.replace(/\|/g, "\\|")).join(" | ")} |`;
    return i === 0 ? [line, `|${r.map(() => "---").join("|")}|`] : [line];
  });

// TableGrid edits a Markdown table as a grid of fields: the first row is the
// header.
function TableGrid({ rows, onChange }: { rows: string[][]; onChange: (rows: string[][]) => void }) {
  const width = rows[0]?.length ?? 0;
  const set = (i: number, j: number, v: string) => onChange(rows.map((r, a) => (a === i ? r.map((x, b) => (b === j ? v : x)) : r)));
  return (
    <div className="doc-table">
      <table>
        <thead>
          <tr>
            {rows[0]?.map((h, j) => (
              <th key={j}>
                <input type="text" aria-label={`Column ${j + 1} heading`} value={h} onChange={(e) => set(0, j, e.target.value)} />
              </th>
            ))}
            <th aria-label="Row actions" />
          </tr>
        </thead>
        <tbody>
          {rows.slice(1).map((r, i) => (
            <tr key={i}>
              {r.map((c, j) => (
                <td key={j}>
                  <GrowArea aria-label={`Row ${i + 1}, ${rows[0][j] || `column ${j + 1}`}`} value={c} onChange={(e) => set(i + 1, j, e.target.value.replace(/\n/g, " "))} />
                </td>
              ))}
              <td>
                <button type="button" className="ghost small icon-only" onClick={() => onChange(rows.filter((_, a) => a !== i + 1))} aria-label={`Remove row ${i + 1}`} title="Remove row">
                  <Trash2 aria-hidden="true" />
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <span className="doc-table-actions">
        <button type="button" className="ghost small" onClick={() => onChange([...rows, Array<string>(width).fill("")])}>
          <Plus aria-hidden="true" />
          Add row
        </button>
        <button type="button" className="ghost small" onClick={() => onChange(rows.map((r, i) => [...r, i === 0 ? "New column" : ""]))}>
          <Plus aria-hidden="true" />
          Add column
        </button>
      </span>
    </div>
  );
}

// PartEditor edits one part: its heading, its text and its tables. It keeps
// its own chunks while open, so text being typed is never rewritten.
function PartEditor({ part, onChange }: { part: Part; onChange: (p: Part) => void }) {
  const id = useId();
  const [chunks, setChunks] = useState(() => chunksOf(part.lines));
  const setChunk = (i: number, c: Chunk) => {
    const next = chunks.map((x, j) => (j === i ? c : x));
    setChunks(next);
    onChange({ ...part, lines: next.flatMap(chunkLines) });
  };
  return (
    <div className="doc-edit">
      {part.head !== null && (
        <div>
          <label htmlFor={id}>Heading</label>
          <input id={id} type="text" value={headText(part.head)} onChange={(e) => onChange({ ...part, head: `## ${e.target.value}` })} />
        </div>
      )}
      {chunks.map((c, i) =>
        // Blank lines around a table stay in the file but get no box.
        c.kind === "text" && c.text.trim() === "" && chunks.length > 1 ? null : c.kind === "table" ? (
          <TableGrid key={i} rows={c.rows} onChange={(rows) => setChunk(i, { kind: "table", rows })} />
        ) : (
          <GrowArea key={i} className="doc-text" aria-label={chunks.length > 1 ? `Text ${i + 1}` : "Text"} value={c.text} onChange={(e) => setChunk(i, { kind: "text", text: e.target.value })} />
        ),
      )}
    </div>
  );
}

// DocSections shows a document as one card per section, each read as
// rendered text and edited in place on its own.
function DocSections({ source, onChange }: { source: string; onChange: (text: string) => void }) {
  const [parts, setParts] = useState(() => splitParts(source));
  const [open, setOpen] = useState<number | null>(null);
  const update = (next: Part[]) => {
    setParts(next);
    onChange(joinParts(next));
  };
  const moveTo = (from: number, to: number) => {
    const next = parts.slice();
    const [p] = next.splice(from, 1);
    next.splice(to, 0, p);
    update(next);
  };
  const first = parts.findIndex((p) => p.head !== null);
  return (
    <div className="doc-parts">
      {parts.map((p, i) => {
        const title = p.head === null ? "Introduction" : headText(p.head) || "Untitled";
        const shown = withoutComments(p.lines.join("\n")).trim();
        const editing = open === p.id;
        return (
          <section key={p.id} className="doc-part" data-open={editing || undefined} aria-label={title}>
            <div className="doc-part-head">
              <h3>{title}</h3>
              <span className="cv-order">
                {p.head !== null && (
                  <>
                    <button type="button" className="ghost small icon-only" disabled={i === first} onClick={() => moveTo(i, i - 1)} aria-label={`Move ${title} up`} title="Move up">
                      <ArrowUp aria-hidden="true" />
                    </button>
                    <button type="button" className="ghost small icon-only" disabled={i === parts.length - 1} onClick={() => moveTo(i, i + 1)} aria-label={`Move ${title} down`} title="Move down">
                      <ArrowDown aria-hidden="true" />
                    </button>
                    <button type="button" className="ghost small icon-only" onClick={() => update(parts.filter((x) => x.id !== p.id))} aria-label={`Remove ${title}`} title="Remove">
                      <Trash2 aria-hidden="true" />
                    </button>
                  </>
                )}
                <button type="button" className={editing ? "small primary" : "small"} onClick={() => setOpen(editing ? null : p.id)} aria-label={editing ? `Done editing ${title}` : `Edit ${title}`}>
                  {editing ? <Check aria-hidden="true" /> : <Pencil aria-hidden="true" />}
                  {editing ? "Done" : "Edit"}
                </button>
              </span>
            </div>
            {editing ? (
              <PartEditor part={p} onChange={(next) => update(parts.map((x) => (x.id === p.id ? next : x)))} />
            ) : shown ? (
              <div className="doc-read">
                <Markdown source={shown} />
              </div>
            ) : (
              <p className="muted">Nothing here yet.</p>
            )}
          </section>
        );
      })}
      <button
        type="button"
        onClick={() => {
          const p: Part = { id: ++lastPart, head: "## New section", lines: ["", ""] };
          const last = parts[parts.length - 1];
          const lines = last && last.lines[last.lines.length - 1] !== "" ? [...parts.slice(0, -1), { ...last, lines: [...last.lines, ""] }] : parts;
          update([...lines, p]);
          setOpen(p.id);
        }}
      >
        <Plus aria-hidden="true" />
        Add section
      </button>
    </div>
  );
}

// Earlier lists the texts the last saves replaced. Opening one puts it in the
// editor as an unsaved edit, so bringing it back is a save like any other.
// Saves in the same minute are told apart by their seconds.
function Earlier({ file, revision, onOpen }: { file: string; revision: string; onOpen: (text: string, at: string) => void }) {
  const [open, setOpen] = useState(false);
  const [res, reload] = useResource(() => (open ? get(`/api/config/${file}/backups`, configBackupList) : Promise.resolve(null)), [open, file, revision]);
  return (
    <details className="earlier" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>Earlier versions</summary>
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />}
      {res.data &&
        (res.data.backups.length === 0 ? (
          <p className="muted">None yet. Each save keeps the text it replaces; the last 10 are kept.</p>
        ) : (
          <ul className="plain item-list">
            {res.data.backups.map((b, _, all) => {
              const at = formatDateTime(b.savedAt, all.filter((o) => formatDateTime(o.savedAt) === formatDateTime(b.savedAt)).length > 1);
              return (
                <li key={b.savedAt}>
                  <span className="item-main">
                    Replaced {at}
                    <span className="item-detail">{` ${b.content.split("\n").length} lines`}</span>
                  </span>
                  <button type="button" className="small" onClick={() => onOpen(b.content, at)} aria-label={`Open the version replaced ${at}`}>
                    Open in the editor
                  </button>
                </li>
              );
            })}
          </ul>
        ))}
    </details>
  );
}

function Editor({ doc, label, step, importable, structured, reload }: { doc: ConfigDocument; label: string; step: string; importable: boolean; structured: Structured; reload: () => void }) {
  const [draft, setDraft] = useState(doc.content);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const [restored, setRestored] = useState("");
  const [mode, setMode] = useState<Mode>(() => (structured === "cv" && formDrops(doc.content) ? "markdown" : "structured"));
  // opened counts the texts that arrived from elsewhere; the structured
  // editors read the text again when it changes.
  const [opened, setOpenedCount] = useState(0);
  // A form drawn again starts with short text boxes, and WebKit clamps the
  // scroll to that short page: the page goes back to where it was.
  const scrollAt = useRef(0);
  const setOpened = (next: (n: number) => number) => {
    scrollAt.current = window.scrollY;
    setOpenedCount(next);
  };
  useLayoutEffect(() => {
    if (opened > 0 && window.scrollY !== scrollAt.current) window.scrollTo(0, scrollAt.current);
  }, [opened]);
  const area = useId();
  const discarding = useRef(false);
  const dirty = draft !== doc.content;

  // After "Load the new version", the editor takes the text that arrives.
  useEffect(() => {
    if (!discarding.current) return;
    discarding.current = false;
    setDraft(doc.content);
    setOpened((n) => n + 1);
    setError(null);
  }, [doc.revision]);
  const tooBig = new TextEncoder().encode(draft).length > MAX_BYTES;
  const edit = (text: string) => {
    setDraft(text);
    setSavedAt(null);
    setRestored("");
  };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await put(`/api/config/${doc.file}`, configSaved, { content: draft, expectedRevision: doc.revision });
      setSavedAt(new Date().toLocaleTimeString());
      setRestored("");
      reload();
    } catch (e) {
      setSavedAt(null);
      setError(asApiError(e));
    } finally {
      setBusy(false);
    }
  };

  const modes: [Mode, string][] = [["structured", structured === "cv" ? "Form" : "Sections"], ["markdown", "Markdown"]];
  return (
    <div>
      {doc.template && <ExampleMarker step={step} />}
      <div className="editor-modes">
        <span className="segmented" role="radiogroup" aria-label={`How to edit the ${label}`}>
          {modes.map(([m, name]) => (
            <label key={m}>
              <input type="radio" name={`${doc.file}-mode`} checked={mode === m} onChange={() => setMode(m)} />
              {name}
            </label>
          ))}
        </span>
        {/* Importing starts here, not in the save bar, where it took a fifth of a phone's screen. */}
        {importable && (
          <CVFileInput
            disabled={busy}
            unsaved={dirty}
            onBusy={setBusy}
            onText={(text) => {
              edit(text);
              setOpened((n) => n + 1);
              setError(null);
            }}
          />
        )}
        {mode === "structured" && structured === "cv" && formDrops(draft) && (
          <p className="note">This CV holds comments, tables, code or nested lists that the form does not keep. Editing a field writes the CV without them; use Markdown to keep them.</p>
        )}
      </div>
      {mode === "structured" && structured === "sections" ? (
        <DocSections key={opened} source={draft} onChange={edit} />
      ) : (
        <div className={mode === "structured" ? "split form-split" : "split"}>
          <div>
            {mode === "structured" ? (
              <CvForm key={opened} source={draft} onChange={edit} />
            ) : (
              <>
                <label htmlFor={area} className="sr-only">
                  {label}
                </label>
                <textarea id={area} className="editor tall" value={draft} onChange={(e) => edit(e.target.value)} spellCheck disabled={busy} />
              </>
            )}
          </div>
          <div aria-label="Preview" role="region" className="editor-preview" tabIndex={0}>
            <h3>Preview</h3>
            {structured === "cv" ? <CvSheet source={draft} /> : <Markdown source={draft} />}
          </div>
        </div>
      )}
      {tooBig && (
        <p role="status" className="note error">
          Over the 256 KiB limit.
        </p>
      )}
      {error?.kind === "revision_conflict" ? (
        <Changed
          onKeep={() => (setError(null), reload())}
          onLoad={() => {
            discarding.current = true;
            reload();
          }}
        />
      ) : (
        error && <ErrorNote error={error} />
      )}
      {doc.exists && (
        <Earlier
          file={doc.file}
          revision={doc.revision}
          onOpen={(text, at) => {
            edit(text);
            setOpened((n) => n + 1);
            setRestored(`The ${label} as it was until ${at} is in the editor. Save to bring it back, or discard the change.`);
          }}
        />
      )}
      <p role="status" className={restored ? "note" : "sr-only"}>{restored}</p>
      <SaveBar dirty={dirty} savedAt={savedAt ?? ""}>
        {dirty && (
          <button
            type="button"
            className="ghost"
            disabled={busy}
            onClick={() => {
              setDraft(doc.content);
              setOpened((n) => n + 1);
              setError(null);
              setRestored("");
            }}
          >
            Discard changes
          </button>
        )}
        <button type="button" className="primary" onClick={() => void save()} disabled={busy || !dirty || tooBig}>
          Save {label}
        </button>
      </SaveBar>
    </div>
  );
}

// MarkdownTab edits one Markdown document. The CV opens as a form beside its
// résumé preview, other documents as one card per section; both can switch to
// the plain Markdown with a live preview. A save quotes the revision it was
// loaded at, so a change made elsewhere is never lost.
export function MarkdownTab({ file, label, step, importable = false, structured }: { file: string; label: string; step: string; importable?: boolean; structured: Structured }) {
  const [res, reload] = useResource(() => get(`/api/config/${file}`, configDocument), [file]);
  return (
    <Panel title={label} id={`profile-${file}`} res={res} retry={reload}>
      {(doc) => <Editor doc={doc} label={label} step={step} importable={importable} structured={structured} reload={reload} />}
    </Panel>
  );
}

// CVFileInput serves both CV editors. Extraction is local to the server and
// returns only an editor value: importing never saves CV/profile facts. Over
// unsaved text it asks first, as the imported text replaces it.
export function CVFileInput({ onText, onBusy, disabled = false, unsaved = false }: { onText: (text: string) => void; onBusy: (busy: boolean) => void; disabled?: boolean; unsaved?: boolean }) {
  const picker = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const load = async (file: File | undefined) => {
    if (!file || busy || disabled) return;
    setError(null);
    if (file.size > 10 * 1024 * 1024) {
      setError(new ApiError("validation_failed", "CV files are limited to 10 MiB.", { requestId: "" }));
      return;
    }
    setBusy(true);
    onBusy(true);
    try {
      const result = await upload("/api/setup/cv/extract", cvExtracted, file, { name: file.name });
      onText(result.markdown);
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setBusy(false);
      onBusy(false);
      if (picker.current) picker.current.value = "";
    }
  };
  return <div>
    <button type="button" disabled={busy || disabled} onClick={() => (unsaved ? setAsking(true) : picker.current?.click())}>{busy ? "Extracting CV" : "Import from file"}</button>
    {asking && <ConfirmDialog title="Replace your unsaved text?" consequence="The text read from the file replaces what is in the editor now, including the changes you have not saved." verb="Choose a file" onCancel={() => setAsking(false)} onConfirm={() => (setAsking(false), picker.current?.click())} />}
    <input ref={picker} type="file" disabled={busy || disabled} accept=".pdf,.docx,.md,.markdown,.txt" className="sr-only" tabIndex={-1} aria-label="CV file" onChange={(e) => void load(e.target.files?.[0])} />
    <p className="muted">PDF, DOCX, Markdown or text, up to 10 MiB. Review the text before saving.</p>
    {error && <ErrorNote error={error} />}
  </div>;
}
