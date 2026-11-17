import { useState, type FormEvent } from "react";
import { put } from "../../api/client";
import { llmRouting } from "../../api/decoders";
import { ErrorNote } from "../../ui/parts";
import type { LLMRouting } from "../../wire";
import { useAction } from "../action";

// Adding an account does not apply a preset: existing task routes stay put.
export function ProviderEditor({ routing, reload }: { routing: LLMRouting; reload: () => void }) {
  const act = useAction();
  const [open, setOpen] = useState(false);
  const [preset, setPreset] = useState("");
  const [id, setId] = useState("");
  const [kind, setKind] = useState("openai-compatible");
  const [url, setURL] = useState("");
  const [structured, setStructured] = useState(false);
  const [embeddings, setEmbeddings] = useState(false);
  const [saved, setSaved] = useState("");
  const chosen = routing.presets.find((p) => p.name === preset);
  const native = chosen?.kind === "cli" || chosen?.kind === "chatgpt";
  const duplicate = routing.providers.some((p) => p.id === id.trim());
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (duplicate) return;
    const name = native ? preset : id.trim();
    const body = preset ? { preset } : { kind, baseUrl: url.trim(), structuredOutput: structured, embeddings };
    if (await act.run(() => put(`/api/llm/providers/${encodeURIComponent(name)}`, llmRouting, body))) {
      setSaved(name);
      setOpen(false);
      setId("");
      reload();
    }
  };
  return (
    <div className="stack">
      <h4>Another account or your own endpoint</h4>
      <p className="muted">A second key for a service you already use, or any other service that speaks the OpenAI or Anthropic API. Add it here, save its key on its row, then choose it when you change a job.</p>
      {saved && <p role="status" className="note ok">Added {saved}. No job changed.</p>}
      <p><button type="button" aria-expanded={open} onClick={() => { setOpen(!open); act.clear(); }}>{open ? "Cancel" : "Add another account"}</button></p>
      {open && (
        <form className="stack" onSubmit={(e) => void submit(e)}>
          <label>Service
            <select value={preset} onChange={(e) => { setPreset(e.target.value); setId(e.target.value); }}>
              <option value="">Custom API endpoint</option>
              {routing.presets.filter((p) => p.kind !== "fake").map((p) => <option key={p.name} value={p.name}>{p.label}</option>)}
            </select>
          </label>
          <label>Name for this account
            <input value={native ? preset : id} disabled={native} required pattern="[a-z0-9][a-z0-9_-]{0,39}" maxLength={40} spellCheck={false} autoComplete="off" placeholder="minimax-personal" onChange={(e) => setId(e.target.value)} />
          </label>
          {duplicate && <p className="note error">That name is already taken. Use a different name for another account.</p>}
          {native && <p className="muted">Uses this Mac's existing sign-in. Separate login profiles are not managed here.</p>}
          {!preset && <>
            <label>Kind of API<select value={kind} onChange={(e) => { setKind(e.target.value); if (e.target.value === "anthropic") setEmbeddings(false); }}><option value="openai-compatible">OpenAI compatible</option><option value="anthropic">Anthropic</option></select></label>
            <label>Address of the API<input type="url" required value={url} placeholder="https://api.example.com/v1" spellCheck={false} onChange={(e) => setURL(e.target.value)} /></label>
            {kind === "openai-compatible" && <>
              <label><input type="checkbox" checked={structured} onChange={(e) => setStructured(e.target.checked)} /> Supports JSON schema output (structured replies)</label>
              <label><input type="checkbox" checked={embeddings} onChange={(e) => setEmbeddings(e.target.checked)} /> Supports embeddings</label>
            </>}
          </>}
          {act.error && <ErrorNote error={act.error} />}
          <p><button type="submit" disabled={act.busy || duplicate || !id.trim()}>{act.busy ? "Adding…" : "Add account"}</button></p>
        </form>
      )}
    </div>
  );
}
