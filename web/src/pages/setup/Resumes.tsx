import { useRef, useState } from "react";
import { ApiError, get } from "../../api/client";
import { configDocument } from "../../api/decoders_pages";
import { resumeList } from "../../api/decoders_setup";
import { asApiError, useResource } from "../../hooks";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import { MAX_BYTES, slug, storeUnderFreeName } from "../profile/Resumes";
import { listOf, rowsOf, text, valueAt } from "../profile/shared";
import { ApplyOutcome, useApply } from "./apply";
import { FieldErrors } from "./Form";
import type { ScreenProps } from "./model";
import { Toolchain } from "./Toolchain";

interface Row {
  id: string;
  label?: string;
  file: string;
  tags?: string[];
  default: boolean;
  description?: string;
  instructions?: string;
}

// rosterOf reads the roster as the profile keeps it, which is the shape the
// writer replaces.
function rosterOf(values: { [key: string]: unknown } | undefined): Row[] {
  return rowsOf(valueAt(values, "resumes")).map((r) => ({
    id: text(r.id), ...(text(r.label) ? { label: text(r.label) } : {}), file: text(r.file), ...(listOf(r.tags).length ? { tags: listOf(r.tags) } : {}),
    default: r.default === true, ...(text(r.description) ? { description: text(r.description) } : {}),
    ...(text(r.instructions) ? { instructions: text(r.instructions) } : {}),
  }));
}

const sizeLabel = (bytes: number) => (bytes >= 1048576 ? `${(bytes / 1048576).toFixed(1)} MiB` : `${Math.max(1, Math.round(bytes / 1024))} KiB`);

// Roster shows the registered résumés and registers an uploaded PDF. The
// writer replaces the whole roster, so every call sends the entries already
// there plus the change, with exactly one default.
function Roster({ step, reload }: ScreenProps) {
  const [list, retry] = useResource(() => get("/api/resumes", resumeList), [], ["setup", "configuration"]);
  const [profile] = useResource(() => get("/api/config/profile", configDocument), [], ["configuration"]);
  const apply = useApply(step.id, reload);
  const picker = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const known = profile.status === "ready" ? rosterOf(profile.data.values) : null;

  const add = async (file: File | undefined) => {
    if (!file || !known) return;
    if (!/\.pdf$/i.test(file.name) && file.type !== "application/pdf") return void setError(new ApiError("validation_failed", "Only PDF files can be uploaded.", { requestId: "" }));
    if (file.size === 0 || file.size > MAX_BYTES) return void setError(new ApiError("validation_failed", "A résumé PDF must be between 1 byte and 10 MiB.", { requestId: "" }));
    setBusy(true);
    setError(null);
    try {
      const { id, stored } = await storeUnderFreeName(file, slug(file.name), new Set(known.map((r) => r.id)));
      const first = !known.some((r) => r.default);
      await apply.send({ roster: [...known, { id, label: file.name.replace(/\.pdf$/i, ""), file: stored.file, default: first }] });
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setBusy(false);
    }
  };
  const makeDefault = (id: string) => void apply.send({ roster: (known ?? []).map((r) => ({ ...r, default: r.id === id })) });

  return (
    <section aria-labelledby="setup-roster" className="panel">
      <h3 id="setup-roster">Your résumés</h3>
      <div role="group" aria-label="Upload a résumé">
        <p>Choose a PDF of at most 10 MiB. It is added to the roster; the first one becomes the default.</p>
        <button type="button" disabled={busy || apply.busy || !known} onClick={() => picker.current?.click()}>
          Upload a résumé PDF
        </button>
        <input ref={picker} type="file" accept="application/pdf,.pdf" className="sr-only" tabIndex={-1} aria-label="Résumé PDF" onChange={(e) => { void add(e.target.files?.[0]); e.target.value = ""; }} />
      </div>
      {error != null && <ErrorNote error={error} />}
      <FieldErrors id="roster-errors" errors={apply.byField.roster} />
      <ApplyOutcome apply={apply} />
      <Panel title="Roster" id="setup-roster-list" res={list} retry={retry}>
        {(l) =>
          l.resumes.length === 0 ? (
            <Empty>No résumé is on the roster yet.</Empty>
          ) : (
            <ul className="plain stack">
              {l.resumes.map((r) => (
                <li key={r.id} className="row">
                  <span>
                    <strong>{r.label || r.id}</strong> <span className="muted">{r.fileName}, {sizeLabel(r.size)}</span> {r.default && <Chip tone="good">default</Chip>}
                  </span>
                  {!r.default && (
                    <button type="button" disabled={busy || apply.busy || !known} onClick={() => makeDefault(r.id)} aria-label={`Make ${r.label || r.id} the default`}>
                      Make default
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )
        }
      </Panel>
    </section>
  );
}

// Resumes keeps two statuses apart: the roster, which the step needs, and the
// rendering check, which only says whether new PDFs can be made.
export function Resumes(props: ScreenProps) {
  return (
    <div>
      <Roster {...props} />
      <Toolchain />
    </div>
  );
}
