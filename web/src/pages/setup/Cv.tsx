import { CVFileInput } from "../profile/MarkdownTab";
import { useEffect, useRef, useState } from "react";
import { get } from "../../api/client";
import { configDocument } from "../../api/decoders_pages";
import { useResource } from "../../hooks";
import { Loading } from "../../ui/parts";
import { Changed } from "../../ui/SaveBar";
import type { SetupDraft } from "../../wire";
import { useApply } from "./apply";
import { DraftReview } from "./CvDraft";
import { SchemaForm } from "./Form";
import { EvidenceImport } from "../intake/EvidenceImport";
import { text, type ScreenProps } from "./model";

// Cv takes the CV as markdown, starting from the saved one so a return to the
// step shows it. What the parser finds in it comes back as a draft to review;
// nothing of it is stored until it is confirmed. A save quotes the revision
// the text was loaded at, so a CV saved meanwhile in another window is never
// replaced unasked. A CV saved elsewhere replaces the text here when the window
// comes back into view and nothing here is typed over the loaded text.
export function Cv({ step, reload }: ScreenProps) {
  const [importing, setImporting] = useState(false);
  const [draft, setDraft] = useState<SetupDraft | null>(null);
  // A confirmed draft clears the save's notes, which said nothing was stored
  // yet; the form starts again from what was saved.
  const [saves, setSaves] = useState(0);
  const saved = useRef<Record<string, unknown>>({});
  const revision = useRef<string | undefined>(undefined);
  const typed = useRef("");
  const ask = useApply(step.id, reload);
  const markdown = step.fields.filter((f) => f.name === "markdown");
  const awaiting = step.reasons.some((r) => /draft|review/i.test(r.text));
  const [stored, refetch] = useResource(() => get("/api/config/cv", configDocument), [], ["configuration"]);
  useEffect(() => {
    const on = () => document.visibilityState !== "hidden" && refetch();
    window.addEventListener("focus", on);
    document.addEventListener("visibilitychange", on);
    return () => (window.removeEventListener("focus", on), document.removeEventListener("visibilitychange", on));
  }, [refetch]);
  useEffect(() => {
    const doc = stored.data;
    if (!doc || revision.current === undefined || doc.revision === revision.current || text(typed.current) !== text(saved.current.markdown)) return;
    revision.current = doc.revision;
    saved.current = doc.exists && !doc.template ? { markdown: doc.content } : {};
    setSaves((n) => n + 1);
  }, [stored.data]);
  if (stored.status === "loading") return <Loading label="Loading your CV" />;
  if (revision.current === undefined && stored.data) revision.current = stored.data.revision;
  if (saved.current.markdown === undefined && stored.data?.exists && !stored.data.template) saved.current = { markdown: stored.data.content };
  // The revision of the file as read now: a save quotes it from here on, and
  // with load the form starts again from that text.
  const reread = async (load: boolean) => {
    const doc = await get("/api/config/cv", configDocument);
    revision.current = doc.revision;
    if (load) {
      saved.current = { markdown: doc.content };
      setSaves((n) => n + 1);
    }
    return doc;
  };
  return (
    <div>
      <SchemaForm
        key={saves}
        initial={saved.current}
        step={step}
        reload={reload}
        fields={markdown}
        submit="Save CV"
        disabled={importing}
        extra={() => (revision.current ? { revision: revision.current } : {})}
        onApplied={(a) => {
          if (a.draft) setDraft(a.draft);
          // The file's new revision is the next save's base only while the
          // file holds the text just saved; otherwise the old one stays, so
          // that save is refused too.
          const [text, before] = [typed.current.trim(), revision.current];
          void reread(false).then((doc) => doc.content === text || (revision.current = before), () => (revision.current = before));
        }}
        conflict={(_, clear) => <Changed onKeep={() => void reread(false).then(clear, clear)} onLoad={() => void reread(true).catch(clear)} />}
        custom={(form, saving) => {
          saved.current = form.base;
          typed.current = typeof form.values.markdown === "string" ? form.values.markdown : "";
          return {
          markdown: (c) => (
            <>
            <CVFileInput disabled={saving} unsaved={text(c.value) !== text(form.base.markdown)} onText={c.set} onBusy={setImporting} />
            <textarea disabled={importing || saving} id={c.id} className="editor tall" spellCheck={false} aria-describedby={c.describedBy} aria-invalid={c.invalid || undefined} value={typeof c.value === "string" ? c.value : ""} onChange={(e) => c.set(e.target.value)} />
            </>
          ),
          };
        }}
        labels={{ markdown: "Your CV as markdown" }}
      />
      {!draft && awaiting && (
        <div className="actions">
          <button type="button" disabled={ask.busy} onClick={() => void ask.send({}).then((a) => a?.draft && setDraft(a.draft))}>
            Review extracted history
          </button>
        </div>
      )}
      {draft && <DraftReview step={step} draft={draft} reload={reload} onConfirmed={() => setSaves((n) => n + 1)} />}
      <EvidenceImport onApplied={reload} />
    </div>
  );
}
