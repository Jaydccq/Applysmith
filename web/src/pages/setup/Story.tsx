import { useState } from "react";
import { get } from "../../api/client";
import { configDocument } from "../../api/decoders_pages";
import { asApiError, useResource } from "../../hooks";
import { ErrorNote, Loading } from "../../ui/parts";
import type { ConfigDocument } from "../../wire";
import { decide } from "./apply";
import { SchemaForm, type Control } from "./Form";
import { text, type ScreenProps } from "./model";

const PROMPTS = [
  "A one-line headline: who you are professionally.",
  "Strengths, each with proof: a result, a number or a project.",
  "What you want next.",
  "What you will not accept.",
  "How you prefer to be described.",
];

// area counts characters as the step does (trimmed, by character) so the
// minimum is reached while typing rather than learned after a save.
const area = (c: Control, min: number, disabled = false) => {
  const n = [...text(c.value).trim()].length;
  return (
    <>
      <textarea id={c.id} className="editor tall" disabled={disabled} aria-describedby={c.describedBy} aria-invalid={c.invalid || undefined} value={text(c.value)} onChange={(e) => c.set(e.target.value)} />
      {n > 0 && <span className="muted">{n < min ? `${n} of at least ${min} characters` : `${n} characters`}</span>}
    </>
  );
};

// own is a stored document the person wrote: the seed text and a missing file
// start the box empty.
const own = (d: ConfigDocument) => (d.exists && !d.template ? d.content : "");

// Story collects the narrative that drafts draw on and, apart from it, optional
// samples of the person's own writing. The voice part is skipped by itself:
// the narrative is still required.
export function Story({ step, reload }: ScreenProps) {
  const [skipped, setSkipped] = useState(step.warnings.some((w) => /voice/i.test(w) && /skipped/i.test(w)));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  // The boxes start from what is stored, so a narrative saved short is
  // finished rather than typed again.
  const [docs] = useResource(() => Promise.all([get("/api/config/narrative", configDocument), get("/api/config/voice", configDocument)]), []);

  const voice = async (kind: "skip" | "unskip") => {
    setBusy(true);
    setError(null);
    try {
      await decide("story.voice", kind);
      setSkipped(kind === "skip");
      reload();
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h3>Write your narrative</h3>
      <p>Answer these five prompts in your own words. They become one markdown text of at least 300 characters.</p>
      <ol>
        {PROMPTS.map((p) => (
          <li key={p}>{p}</li>
        ))}
      </ol>
      {docs.status === "loading" ? (
        <Loading label="Loading stored values" />
      ) : (
        <>
          {docs.status === "error" && <p className="muted">The stored text could not be read, so the boxes start empty.</p>}
          <SchemaForm
            step={step}
            reload={reload}
            fields={step.fields.filter((f) => f.name === "narrative" || f.name === "voice")}
            initial={docs.data ? { narrative: own(docs.data[0]), voice: own(docs.data[1]) } : {}}
            submit="Save story"
            labels={{ narrative: "Your narrative", voice: "Samples of your own writing (optional)" }}
            custom={{ narrative: (c) => area(c, 300), voice: (c) => area(c, 500, skipped) }}
          />
        </>
      )}
      <h3>Voice samples</h3>
      <p>{skipped ? "The voice part is skipped, so drafts are not checked against your own style." : "Samples of 500 characters or more let drafts be checked against your own style. You can skip this part."}</p>
      <div className="actions">
        {skipped ? (
          <button type="button" disabled={busy} onClick={() => void voice("unskip")}>
            Use voice samples again
          </button>
        ) : (
          <button type="button" disabled={busy} onClick={() => void voice("skip")}>
            Skip voice
          </button>
        )}
      </div>
      {error != null && <ErrorNote error={error} />}
    </div>
  );
}
