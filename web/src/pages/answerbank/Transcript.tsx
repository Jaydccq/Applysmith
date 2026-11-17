import { FileText, Upload } from "lucide-react";
import { useRef, useState, type DragEvent } from "react";
import { ApiError, del, get, upload } from "../../api/client";
import { answerBankTranscript } from "../../api/decoders_bank";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Empty, ErrorNote, Panel } from "../../ui/parts";
import type { AnswerBankTranscript } from "../../wire";
import { MAX_BYTES, slug } from "../profile/Resumes";

// TranscriptPanel is the Answer Bank's place for the academic transcript: the
// one PDF that application runs attach to an upload the form labels as a
// transcript. A new upload replaces it.
export function TranscriptPanel() {
  const [transcript, reload] = useResource(() => get("/api/answer-bank/transcript", answerBankTranscript), []);
  return (
    <Panel title="Academic transcript" id="ab-transcript" res={transcript} retry={reload}>
      {(t) => <Transcript transcript={t} reload={reload} />}
    </Panel>
  );
}

function Transcript({ transcript, reload }: { transcript: AnswerBankTranscript; reload: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [said, setSaid] = useState("");
  const [removing, setRemoving] = useState(false);
  const [over, setOver] = useState(false);
  const picker = useRef<HTMLInputElement>(null);
  const stored = transcript.fileName;

  const run = async (go: () => Promise<string>) => {
    setBusy(true);
    setError(null);
    try {
      setSaid(await go());
      reload();
    } catch (e) {
      setSaid("");
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const add = (file: File | undefined) => {
    if (!file || busy) return;
    if (!/\.pdf$/i.test(file.name) && file.type !== "application/pdf") return void setError(new ApiError("validation_failed", "Only PDF files can be uploaded.", { requestId: "" }));
    if (file.size === 0 || file.size > MAX_BYTES) return void setError(new ApiError("validation_failed", "A transcript PDF must be between 1 byte and 10 MiB.", { requestId: "" }));
    void run(async () => {
      const saved = await upload("/api/answer-bank/transcript", answerBankTranscript, file, { name: slug(file.name) || "transcript" });
      return stored ? `Replaced ${stored} with ${saved.fileName}.` : `Added ${saved.fileName}.`;
    });
  };
  const drop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    add(e.dataTransfer.files[0]);
  };

  return (
    <div>
      <p className="muted">Application forms that ask for a transcript (&ldquo;Transcript&rdquo;, &ldquo;Unofficial transcript&rdquo;, &ldquo;Academic transcript&rdquo;) get this PDF. Without one, such an upload is left for you.</p>
      <div
        role="group"
        aria-label={stored ? "Replace the transcript" : "Upload a transcript"}
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
        <p>{stored ? "Drop a PDF here to replace it, or choose one." : "Drop a PDF here, or choose one."} A transcript PDF is at most 10 MiB.</p>
        <button type="button" disabled={busy} onClick={() => picker.current?.click()}>
          {stored ? "Replace PDF" : "Choose PDF"}
        </button>
        <input ref={picker} type="file" accept="application/pdf,.pdf" className="sr-only" tabIndex={-1} aria-label="Transcript PDF" onChange={(e) => { add(e.target.files?.[0]); e.target.value = ""; }} />
      </div>
      <div aria-live="polite">
        {said && <p className="note ok">{said}</p>}
        {error != null && <ErrorNote error={error} />}
      </div>
      {stored ? (
        <ul className="plain resume-list">
          <li className="resume-card">
            <div className="resume-head">
              <span className="resume-icon" aria-hidden="true">
                <FileText />
              </span>
              <span className="resume-title">
                <strong>{stored}</strong>
                <span className="muted">transcript/{stored}</span>
              </span>
              <button type="button" className="danger-outline" disabled={busy} onClick={() => setRemoving(true)} aria-label={`Remove ${stored}`}>
                Remove
              </button>
            </div>
          </li>
        </ul>
      ) : (
        <Empty>No transcript yet.</Empty>
      )}
      {removing && (
        <ConfirmDialog
          title={`Remove ${stored}`}
          consequence="The PDF is deleted from your workspace, and application forms that ask for a transcript are left for you."
          verb="Remove"
          busy={busy}
          onCancel={() => setRemoving(false)}
          onConfirm={() => {
            setRemoving(false);
            void run(async () => (await del("/api/answer-bank/transcript", answerBankTranscript), `Removed ${stored}.`));
          }}
        />
      )}
    </div>
  );
}
