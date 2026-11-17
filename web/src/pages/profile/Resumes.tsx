import { FileText, Upload } from "lucide-react";
import { useRef, useState, type DragEvent } from "react";
import { ApiError, upload } from "../../api/client";
import { resumeUploaded } from "../../api/decoders_pages";
import { asApiError } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote } from "../../ui/parts";
import { PdfPreview } from "../documents/Preview";
import { ProfilePanel, rowsOf, saveProfile, setOp, text, listOf, valueAt } from "./shared";

export const MAX_BYTES = 10 * 1024 * 1024;

interface Resume {
  id: string;
  label: string;
  file: string;
  tags: string[];
  default: boolean;
  description: string;
  instructions: string;
}

function rosterOf(values: { [key: string]: unknown } | undefined): Resume[] {
  return rowsOf(valueAt(values, "resumes")).map((r) => ({
    id: text(r.id), label: text(r.label), file: text(r.file), tags: listOf(r.tags), default: r.default === true,
    description: text(r.description), instructions: text(r.instructions),
  }));
}

// rosterValue is the roster as the file keeps it: empty optional keys are left out.
function rosterValue(list: Resume[]) {
  return list.map((r) => ({
    id: r.id, ...(r.label ? { label: r.label } : {}), file: r.file, ...(r.tags.length ? { tags: r.tags } : {}), default: r.default,
    ...(r.description ? { description: r.description } : {}), ...(r.instructions ? { instructions: r.instructions } : {}),
  }));
}

export const slug = (name: string) =>
  name
    .replace(/\.pdf$/i, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 36);

function uniqueId(base: string, taken: Set<string>): string {
  const stem = base || "resume";
  let id = stem;
  for (let n = 2; taken.has(id); n++) id = `${stem}-${n}`;
  return id;
}

// A name the roster does not use can still belong to a PDF left on disk after
// a roster delete. The server then answers file_exists and the page tries the
// next suffixed name, at most this many times in all.
const MAX_NAME_TRIES = 20;

export async function storeUnderFreeName(file: File, stem: string, taken: Set<string>) {
  let id = uniqueId(stem, taken);
  for (let attempt = 1; ; attempt++) {
    try {
      return { id, stored: await upload("/api/resumes", resumeUploaded, file, { name: id }) };
    } catch (e) {
      const exists = e instanceof ApiError && e.kind === "conflict" && e.details?.reason === "file_exists";
      if (!exists) throw e;
      if (attempt >= MAX_NAME_TRIES) throw new Error(`No free file name was found for ${file.name} after ${MAX_NAME_TRIES} tries. Remove old copies from your resumes folder or rename the file.`);
      taken.add(id);
      id = uniqueId(stem, taken);
    }
  }
}

function Row({ r, onChange, onDefault, onDelete, busy }: { r: Resume; onChange: (next: Resume) => Promise<void>; onDefault: () => void; onDelete: () => void; busy: boolean }) {
  const [label, setLabel] = useState(r.label);
  const [tags, setTags] = useState(r.tags.join(", "));
  const parsed = tags.split(",").map((t) => t.trim()).filter(Boolean);
  const dirty = label !== r.label || parsed.join("|") !== r.tags.join("|");
  // The PDF is shown in the page: a link to the route could not send the session key.
  const [viewing, setViewing] = useState(false);
  return (
    <li className="resume-card">
      <div className="resume-head">
        <span className="resume-icon" aria-hidden="true">
          <FileText />
        </span>
        <span className="resume-title">
          <strong>{r.label || r.id}</strong>
          <span className="muted">{r.file}</span>
        </span>
        {r.default && <Chip tone="good">Default</Chip>}
      </div>
      <div className="form-grid two">
        <label>
          Label
          <input value={label} onChange={(e) => setLabel(e.target.value)} />
        </label>
        <label>
          Tags, separated by commas
          <input value={tags} onChange={(e) => setTags(e.target.value)} />
        </label>
      </div>
      <div className="actions resume-actions">
        <label className="check">
          <input type="radio" name="default-resume" checked={r.default} onChange={onDefault} disabled={busy} />
          Use as the default résumé
        </label>
        <button type="button" aria-expanded={viewing} aria-label={`${viewing ? "Hide" : "View"} the PDF of ${r.label || r.id}`} onClick={() => setViewing(!viewing)}>
          {viewing ? "Hide PDF" : "View PDF"}
        </button>
        <button type="button" className="primary" disabled={busy || !dirty} onClick={() => void onChange({ ...r, label: label.trim(), tags: parsed })}>
          Save label and tags
        </button>
        <button type="button" className="danger-outline" disabled={busy} onClick={onDelete} aria-label={`Delete ${r.label || r.id}`}>
          Delete
        </button>
      </div>
      {viewing && <PdfPreview key={r.id} path={`/api/resumes/${encodeURIComponent(r.id)}/file`} title={`PDF of ${r.label || r.id}`} />}
    </li>
  );
}

function Body({ roster, revision, reload }: { roster: Resume[]; revision: string; reload: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [said, setSaid] = useState("");
  const [doomed, setDoomed] = useState<Resume | null>(null);
  const [over, setOver] = useState(false);
  const picker = useRef<HTMLInputElement>(null);

  const run = async (go: () => Promise<string>) => {
    setBusy(true);
    setError(null);
    try {
      setSaid(await go());
    } catch (e) {
      setSaid("");
      setError(e);
    } finally {
      setBusy(false);
      // After a failure too: a save refused for its revision is sent again
      // against the one on file.
      reload();
    }
  };
  const writeRoster = (list: Resume[]) => saveProfile(revision, [setOp("resumes", rosterValue(list))]);

  const add = (file: File | undefined) => {
    if (!file || busy) return;
    if (!/\.pdf$/i.test(file.name) && file.type !== "application/pdf") return void setError(new ApiError("validation_failed", "Only PDF files can be uploaded.", { requestId: "" }));
    if (file.size === 0 || file.size > MAX_BYTES) return void setError(new ApiError("validation_failed", "A résumé PDF must be between 1 byte and 10 MiB.", { requestId: "" }));
    void run(async () => {
      const { id, stored } = await storeUnderFreeName(file, slug(file.name), new Set(roster.map((r) => r.id)));
      try {
        await writeRoster([...roster, { id, label: file.name.replace(/\.pdf$/i, ""), file: stored.file, tags: [], default: roster.length === 0, description: "", instructions: "" }]);
      } catch (e) {
        throw new Error(`The file was stored as ${stored.file}, but the roster was not updated: ${asApiError(e).message}`);
      }
      return `Added ${file.name}.`;
    });
  };
  const drop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    add(e.dataTransfer.files[0]);
  };

  return (
    <div>
      <div
        role="group"
        aria-label="Upload a résumé"
        aria-busy={busy}
        className="dropzone"
        data-over={over || undefined}
        onDragOver={(e) => {
          e.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={drop}
      >
        <Upload aria-hidden="true" />
        <p>Drop a PDF here, or choose one. A résumé PDF is at most 10 MiB.</p>
        <button type="button" disabled={busy} onClick={() => picker.current?.click()}>
          Choose PDF
        </button>
        <input ref={picker} type="file" accept="application/pdf,.pdf" className="sr-only" tabIndex={-1} aria-label="Résumé PDF" onChange={(e) => { add(e.target.files?.[0]); e.target.value = ""; }} />
      </div>
      <div aria-live="polite">
        {said && <p className="note ok">{said}</p>}
        {error != null && <ErrorNote error={error} />}
      </div>
      {roster.length === 0 ? (
        <Empty>No résumé is on the roster yet. The first one you add becomes the default.</Empty>
      ) : (
        <ul className="plain resume-list">
          {roster.map((r) => (
            <Row
              key={r.id + r.label + r.tags.join()}
              r={r}
              busy={busy}
              onChange={async (next) => void run(async () => (await writeRoster(roster.map((x) => (x.id === r.id ? next : x))), `Saved ${next.label || next.id}.`))}
              onDefault={() => void run(async () => (await writeRoster(roster.map((x) => ({ ...x, default: x.id === r.id }))), `${r.label || r.id} is the default résumé.`))}
              onDelete={() => setDoomed(r)}
            />
          ))}
        </ul>
      )}
      {doomed && (
        <ConfirmDialog
          title={`Delete ${doomed.label || doomed.id}`}
          consequence={`${doomed.file} leaves the roster, so no application uses it. The PDF itself stays in your resumes folder.`}
          verb="Delete from roster"
          busy={busy}
          onCancel={() => setDoomed(null)}
          onConfirm={() => {
            const gone = doomed;
            setDoomed(null);
            const rest = roster.filter((x) => x.id !== gone.id);
            if (gone.default && rest.length > 0) rest[0] = { ...rest[0], default: true };
            void run(async () => (await writeRoster(rest), `Removed ${gone.label || gone.id} from the roster.`));
          }}
        />
      )}
    </div>
  );
}

export function Resumes() {
  return <ProfilePanel title="Résumé PDFs" id="profile-resumes">{(doc, reload) => <Body roster={rosterOf(doc.values)} revision={doc.revision} reload={reload} />}</ProfilePanel>;
}
