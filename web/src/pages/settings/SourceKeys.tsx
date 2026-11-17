import { useId, useState, type FormEvent } from "react";
import { del, get, put } from "../../api/client";
import { secretList, secretStatus } from "../../api/decoders_secrets";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import type { ScanSource } from "../../wire";
import { useAction } from "../action";
import { usePasswordField } from "./passwordField";

// SourceKeys stores the API key of each scan source that reads one, write-only
// like a password, so a source Discovery marks "needs key" is put right here
// instead of at a terminal, where the command refuses while the app holds the
// workspace.
export function SourceKeys({ sources }: { sources: readonly ScanSource[] }) {
  const keyed = sources.filter((s) => s.keyName);
  const [res, reload] = useResource(() => get("/api/secrets", secretList), []);
  const head = useId();
  if (keyed.length === 0) return null;
  return (
    <section aria-labelledby={head}>
      <h3 id={head}>Scan source API keys</h3>
      <p className="muted">These sources search through a service of their own and read its key from this computer's keychain. Discovery lists one as ready once its key is saved.</p>
      {res.data ? (
        keyed.map((s) => <KeyRow key={s.name} source={s} saved={res.data!.entries.some((e) => e.name === s.keyName && e.present)} onChanged={reload} />)
      ) : res.status === "error" ? (
        <ErrorNote error={res.error} onRetry={reload} what="the saved keys" />
      ) : (
        <Loading lines={1} />
      )}
    </section>
  );
}

function KeyRow({ source, saved, onChanged }: { source: ScanSource; saved: boolean; onChanged: () => void }) {
  const id = useId();
  const key = usePasswordField();
  const act = useAction();
  const [removing, setRemoving] = useState(false);
  const path = `/api/secrets/${encodeURIComponent(source.keyName!)}`;
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const value = key.take();
    if (value && (await act.run(() => put(path, secretStatus, { value })))) onChanged();
  };
  const remove = async () => {
    setRemoving(false);
    if (await act.run(() => del(path, secretStatus))) onChanged();
  };
  return (
    <>
      <form onSubmit={save} className="key-row">
        <label htmlFor={id}>
          {source.title} API key <Chip tone={saved ? "good" : "warn"}>{saved ? "saved" : "not set"}</Chip>
        </label>
        <input id={id} ref={key.ref} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => key.onInput(e.target.value)} placeholder={saved ? "Enter a new key to replace it" : "Paste the key"} />
        <div className="actions">
          <button type="submit" disabled={key.text === "" || act.busy}>
            Save key
          </button>
          {saved && (
            <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRemoving(true)}>
              Remove key
            </button>
          )}
        </div>
      </form>
      {act.error && <ErrorNote error={act.error} />}
      {removing && (
        <ConfirmDialog
          title={`Remove the ${source.title} key`}
          consequence={`The key is deleted from this computer, and ${source.title} scans wait for a new one.`}
          verb="Remove key"
          onCancel={() => setRemoving(false)}
          onConfirm={() => void remove()}
        />
      )}
    </>
  );
}
