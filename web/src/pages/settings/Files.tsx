import { useId, useState } from "react";
import { ApiError, get, put } from "../../api/client";
import { configDocument, configSaved } from "../../api/decoders_pages";
import { systemInfo } from "../../api/decoders_system";
import { useResource } from "../../hooks";
import { hasShell } from "../../desktop";
import { CopyButton } from "../../ui/CopyButton";
import { ErrorNote, Panel, Loading } from "../../ui/parts";
import { Changed, SaveBar } from "../../ui/SaveBar";
import { leave } from "../../router";
import type { ConfigDocument } from "../../wire";
import { useAction } from "../action";
import { DesktopRows } from "./Desktop";
import { profileSaved, useProfileSaves, useRevisedDraft } from "../profile/shared";

const still = () => {};

const FILES = [
  { file: "profile", name: "profile.yml" },
  { file: "portals", name: "portals.yml" },
  { file: "policy", name: "policy.yml" },
  { file: "cv", name: "cv.md" },
] as const;

// RawEditor edits one file as text, in Settings and on Discovery's Raw text
// tab alike. The server validates on save and answers a refusal that names
// every finding. Text with no edit follows the file; an edit is kept and saved
// at the revision it was made at, so a file that changed since is a revision
// conflict, never overwritten (useRevisedDraft).
export function RawEditor({ file, name }: { file: string; name: string }) {
  const [doc, reloadDoc] = useResource(() => get(`/api/config/${file}`, configDocument), [file], ["configuration"], { keep: "settings.file" });
  const form = useRevisedDraft(doc.data?.revision ?? "", doc.data?.content ?? "");
  const save = useAction();
  const [savedAt, setSavedAt] = useState("");
  const area = useId();
  const { draft: text, dirty } = form;
  // A form's save of profile.yml shows here at once.
  useProfileSaves(file === "profile" ? reloadDoc : still);

  if (doc.status === "loading") return <Loading />;
  if (doc.status === "error" && !doc.data) return <ErrorNote error={doc.error} onRetry={reloadDoc} what={name} />;
  const d: ConfigDocument = doc.data!;
  const conflict = save.error instanceof ApiError && save.error.kind === "revision_conflict";

  const press = async () => {
    setSavedAt("");
    const done = await save.run(() => put(`/api/config/${file}`, configSaved, { content: text, expectedRevision: form.revision }));
    if (done) {
      form.sent(text);
      if (file === "profile") profileSaved();
      setSavedAt(new Date().toLocaleTimeString());
      reloadDoc();
    }
  };
  const discard = () => {
    form.reset();
    save.clear();
    reloadDoc();
  };
  // Keep my text reads the file as it is now and puts the text over it.
  const keep = async () => {
    const now = await save.run(() => get(`/api/config/${file}`, configDocument));
    if (now) (form.rebase(now.revision, now.content), reloadDoc());
  };

  return (
    <div>
      {!d.exists && <p className="muted">{name} does not exist yet. Saving creates it.</p>}
      {d.template && <p className="muted">{name} still holds the template.</p>}
      <label htmlFor={area}>Contents of {name}</label>
      <textarea id={area} className="raw-editor" rows={18} spellCheck={false} value={text} onChange={(e) => form.setDraft(e.target.value)} />
      <SaveBar dirty={dirty} savedAt={savedAt}>
        {dirty && (
          <button type="button" className="ghost" disabled={save.busy} onClick={discard}>
            Discard changes
          </button>
        )}
        <button type="button" className="primary" disabled={!dirty || save.busy} onClick={() => void press()}>
          Save {name}
        </button>
      </SaveBar>
      {conflict && <Changed onKeep={() => void keep()} onLoad={discard} />}
      {save.error && !conflict && <ErrorNote error={save.error} />}
      {d.issues.length > 0 && (
        <div className="note error" aria-live="polite">
          <p>
            {name} as saved has {d.issues.length} {d.issues.length === 1 ? "problem" : "problems"}:
          </p>
          <ul className="plain">
            {d.issues.map((i, n) => (
              <li key={n}>
                {i.path ? `${i.path}: ` : ""}
                {i.message}
                {i.line ? ` (line ${i.line})` : ""}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function Body({ path }: { path: string }) {
  const [file, setFile] = useState<(typeof FILES)[number]["file"]>("profile");
  const current = FILES.find((f) => f.file === file)!;
  return (
    <>
      <p>
        Workspace folder: <code>{path}</code> <CopyButton text={path} label="Copy the workspace path" />
      </p>
      {hasShell() && <DesktopRows workspacePath={path} />}
      <nav aria-label="Files" className="chips">
        {FILES.map((f) => (
          <button key={f.file} type="button" aria-pressed={f.file === file} onClick={() => leave(() => setFile(f.file))}>
            {f.name}
          </button>
        ))}
      </nav>
      <RawEditor key={file} file={current.file} name={current.name} />
    </>
  );
}

export function FilesSection() {
  const [info, reload] = useResource(() => get("/api/system/info", systemInfo), [], [], { keep: "settings.system" });
  return (
    <Panel title="Files" id="settings-files" res={info} retry={reload} what="the workspace folder">
      {(i) => <Body path={i.workspacePath} />}
    </Panel>
  );
}
