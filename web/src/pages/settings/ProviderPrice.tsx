import { useId, useState, type FormEvent } from "react";
import { put } from "../../api/client";
import { llmRouting } from "../../api/decoders";
import { ErrorNote } from "../../ui/parts";
import { useAction } from "../action";

export function ProviderPrice({ provider, models, reload }: { provider: string; models: string[]; reload: () => void }) {
  const act = useAction();
  const list = useId();
  const [model, setModel] = useState("");
  const [input, setInput] = useState("");
  const [cached, setCached] = useState("");
  const [output, setOutput] = useState("");
  const [saved, setSaved] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!model.trim() || !input || !cached || !output) return;
    if (await act.run(() => put(`/api/llm/prices/${encodeURIComponent(provider)}`, llmRouting, { model: model.trim(), inputPerM: Number(input), cachedInputPerM: Number(cached), outputPerM: Number(output) }))) {
      setSaved(true);
      reload();
    }
  };
  return <details>
    <summary>Prices</summary>
    <p className="muted">Prices let Applysmith count what this account costs, in US dollars per million tokens. MiniMax China and BigModel need your prices before calls can run. Set every model you use, backups included.</p>
    <form className="stack" onSubmit={(e) => void submit(e)} onChange={() => setSaved(false)}>
      <label>Priced model on {provider}<input value={model} required list={list} spellCheck={false} onChange={(e) => setModel(e.target.value)} /></label>
      <datalist id={list}>{models.map((m) => <option key={m} value={m} />)}</datalist>
      <label>Input USD per million on {provider}<input type="number" min="0" step="any" required value={input} onChange={(e) => setInput(e.target.value)} /></label>
      <label>Cached input USD per million on {provider}<input type="number" min="0" step="any" required value={cached} onChange={(e) => setCached(e.target.value)} /></label>
      <label>Output USD per million on {provider}<input type="number" min="0" step="any" required value={output} onChange={(e) => setOutput(e.target.value)} /></label>
      <p><button type="submit" disabled={act.busy}>Save model price</button></p>
      {saved && <p role="status">Saved the USD price for {provider}/{model}.</p>}
      {act.error && <ErrorNote error={act.error} />}
    </form>
  </details>;
}
