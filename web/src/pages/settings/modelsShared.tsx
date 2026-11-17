// What the Models and providers section knows about services, in the words it
// shows them. Pure: the components read the routing and decide nothing else.
import { useId, useState, type FormEvent } from "react";
import { del, post, put } from "../../api/client";
import type { ApiError } from "../../api/client";
import { llmKeyStatus, llmRouting } from "../../api/decoders";
import { llmTestStarted } from "../../api/decoders_system";
import { asApiError } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { ErrorNote, type Tone } from "../../ui/parts";
import type { LLMFallback, LLMPreset, LLMRouting, LLMTaskRoute } from "../../wire";
import { useAction, when } from "../action";
import { usePasswordField } from "./passwordField";

// Provider kinds that are called with an API key. The server refuses the key
// routes for any other kind, so this only decides which rows show the field.
export const KEYED_KINDS = new Set(["openai-compatible", "anthropic"]);

// The services whose plan allowance has a usage monitor, by preset name; the
// value is the id of the monitor in GET /api/llm/accounts.
export const PLAN_MONITOR: Record<string, string> = { "opencode-go": "opencode-go", minimax: "minimax-global", "minimax-cn": "minimax-cn", zai: "glm-zai", bigmodel: "glm-cn" };

export const SUBSCRIPTION_TEXT: Record<string, { name: string; blurb: string }> = {
  chatgpt: { name: "ChatGPT plan", blurb: "Signs in with your ChatGPT account and uses your plan allowance, so a call costs nothing extra." },
  claude: { name: "Claude Pro/Max", blurb: "Works only by running the Claude Code program installed on this Mac, with your Claude login. Calls are slower than an API key because each one starts that program." },
  codex: { name: "Codex CLI", blurb: "Runs the Codex program installed on this Mac with your ChatGPT plan. Signing in with ChatGPT is faster than this." },
  "opencode-go": { name: "OpenCode Go", blurb: "Your OpenCode Go plan, called with its API key. Calls use plan allowance, not cash." },
  "kimi-code": { name: "Kimi Code", blurb: "Your Kimi Code membership, called with its own key. Calls use plan allowance, not cash." },
};

export function serviceName(p: LLMPreset): string {
  return SUBSCRIPTION_TEXT[p.name]?.name ?? p.label;
}

// providerName says which service a configured provider id is: its preset's
// name, with the id when it is a second account of the same service.
export function providerName(routing: LLMRouting, id: string): string {
  if (id === "group") return "Pool";
  const preset = routing.presets.find((p) => p.name === id);
  if (preset) return serviceName(preset);
  const of = routing.presets.find((p) => p.name === routing.providers.find((x) => x.id === id)?.preset);
  return of ? `${serviceName(of)} (${id})` : id;
}

// modelTag is a route's model in one line: "ChatGPT plan · gpt-6-luna". A
// command-line program that picks its own model has no name to add.
export function modelTag(routing: LLMRouting, c: LLMFallback): string {
  const who = c.provider === "group" ? `Pool ${c.model}` : providerName(routing, c.provider);
  return !c.model || c.model === "default" || c.provider === "group" ? who : `${who} · ${c.model}`;
}

// The jobs are shown by area, in this order; an area the server adds later is
// shown after them under its own name.
export const AREAS: { id: string; title: string }[] = [
  { id: "evaluation", title: "Evaluating jobs" },
  { id: "apply", title: "Filling applications" },
  { id: "documents", title: "Writing" },
  { id: "mail", title: "Email" },
  { id: "knowledge", title: "Research and knowledge" },
  { id: "agent", title: "Ask Agent" },
];

export function byArea(tasks: LLMTaskRoute[]): { id: string; title: string; tasks: LLMTaskRoute[] }[] {
  const ids = [...AREAS.map((a) => a.id), ...new Set(tasks.map((t) => t.group).filter((g) => !AREAS.some((a) => a.id === g)))];
  return ids
    .map((id) => ({ id, title: AREAS.find((a) => a.id === id)?.title ?? id[0].toUpperCase() + id.slice(1), tasks: tasks.filter((t) => t.group === id) }))
    .filter((a) => a.tasks.length > 0);
}

export const THINKING: [string, string][] = [
  ["", "Job default"],
  ["off", "Off"],
  ["minimal", "Minimal"],
  ["low", "Low"],
  ["medium", "Medium"],
  ["high", "High"],
  ["xhigh", "Highest"],
];

// ---- Shared pieces of the cards and the job editor ----

export type Action = { say: string } | { fail: ApiError } | null;

export function Feedback({ result }: { result: Action }) {
  return (
    <div aria-live="polite">
      {result && "say" in result && <p className="note ok">{result.say}</p>}
      {result && "fail" in result && <ErrorNote error={result.fail} />}
    </div>
  );
}

// ensureProvider makes sure an account exists for a service before something
// is done with it. Adding one never touches the default or any job's model.
export async function ensureProvider(routing: LLMRouting, id: string, preset = id): Promise<void> {
  if (routing.providers.some((p) => p.id === id)) return;
  await put(`/api/llm/providers/${encodeURIComponent(id)}`, llmRouting, { preset });
}

// A usable service is one a job could be sent to now: a plan signed in, a
// program added, or a key saved.
export function usable(p: LLMPreset, routing: LLMRouting, signedIn: boolean): boolean {
  if (p.kind === "chatgpt") return signedIn;
  if (p.kind === "cli") return routing.providers.some((x) => x.id === p.name);
  return p.keyConfigured;
}

// statusOf is how a service reads: only the newest test is stored, so a service
// that is not the one tested last reads as untested.
export function statusOf(routing: LLMRouting, id: string, ready: boolean, off: string, idle: string): { tone: Tone; word: string } {
  if (routing.testing === id) return { tone: "warn", word: "Testing" };
  if (!ready) return { tone: "neutral", word: off };
  const t = routing.lastTest;
  if (t && t.provider === id) return t.outcome === "ok" ? { tone: "good", word: "Connected" } : { tone: "bad", word: `Failing: ${t.errorClass ?? "other"}` };
  return { tone: "neutral", word: idle };
}

export function TestLine({ routing, id }: { routing: LLMRouting; id: string }) {
  const t = routing.lastTest?.provider === id ? routing.lastTest : null;
  if (!t) return null;
  return (
    <>
      <p className="muted">
        Last test{t.model && ` of ${t.model}`} {when(t.testedAt)}, {t.latencyMs} ms
      </p>
      {t.outcome !== "ok" && t.fix && <p className="note error compact">{t.fix}</p>}
    </>
  );
}

// useServiceTest runs the live test of a service. The call can take longer
// than a request may, so the server runs it and the page follows it; an empty
// model tests the usual one, a typed id tries another on the same account.
export function useServiceTest(routing: LLMRouting, id: string, reload: () => void, preset = id) {
  const act = useAction();
  const run = async (model = "") => {
    const body = model.trim() ? { model: model.trim() } : {};
    const started = await act.run(async () => {
      await ensureProvider(routing, id, preset);
      return post(`/api/llm/providers/${encodeURIComponent(id)}/test`, llmTestStarted, body);
    });
    if (started) reload();
  };
  return { run, busy: act.busy || !!routing.testing, error: act.error };
}

// RemoveAccount takes a service's account out of Applysmith, with its saved key.
export function RemoveAccount({ id, name, reload }: { id: string; name: string; reload: () => void }) {
  const act = useAction();
  const [ask, setAsk] = useState(false);
  return (
    <>
      <button type="button" className="danger-outline" disabled={act.busy} aria-label={`Remove ${name} account`} onClick={() => setAsk(true)}>
        Remove account
      </button>
      {act.error && <ErrorNote error={act.error} />}
      {ask && (
        <ConfirmDialog
          title={`Remove ${name}`}
          consequence="The account and its saved key are removed from Applysmith. An account that a job, a backup, a pool or the default still uses cannot be removed."
          verb="Remove account"
          busy={act.busy}
          onCancel={() => setAsk(false)}
          onConfirm={() => {
            void act.run(() => del(`/api/llm/providers/${encodeURIComponent(id)}`, llmRouting)).then((done) => {
              setAsk(false);
              if (done) reload();
            });
          }}
        />
      )}
    </>
  );
}

// KeyForm saves or clears one service's API key. The field is uncontrolled:
// React never writes the typed key back into an attribute, and it is emptied
// after every attempt, accepted or not. A saved key is never shown again.
export function KeyForm({ id, name, configured, onChanged, afterSave }: { id: string; name: string; configured: boolean; onChanged: () => void; afterSave?: () => Promise<void> }) {
  const field = useId();
  const input = usePasswordField();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Action>(null);
  const [confirm, setConfirm] = useState(false);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const key = input.take();
    if (!key) return;
    setBusy(true);
    try {
      await put(`/api/llm/keys/${encodeURIComponent(id)}`, llmKeyStatus, { key });
      await afterSave?.();
      setResult({ say: `Saved at ${new Date().toLocaleTimeString()}.` });
      onChanged();
    } catch (err) {
      setResult({ fail: asApiError(err) });
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    setConfirm(false);
    setBusy(true);
    try {
      await del(`/api/llm/keys/${encodeURIComponent(id)}`, llmKeyStatus);
      setResult({ say: "Key cleared." });
      onChanged();
    } catch (err) {
      setResult({ fail: asApiError(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="key-form">
      <form onSubmit={save}>
        <label htmlFor={field}>{name} API key</label>
        <input id={field} ref={input.ref} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => input.onInput(e.target.value)} placeholder={configured ? "Enter a new key to replace it" : "Paste the key"} />
        <span className="svc-actions">
          <button type="submit" disabled={input.text === "" || busy}>
            {configured ? "Replace key" : "Save key"}
          </button>
          {configured && (
            <button type="button" className="danger-outline" onClick={() => setConfirm(true)} disabled={busy}>
              Clear key
            </button>
          )}
        </span>
      </form>
      <Feedback result={result} />
      {confirm && <ConfirmDialog title={`Clear the ${name} key`} consequence={`The stored key for ${name} is deleted. Jobs that use ${name} fail until a new key is saved.`} verb="Clear key" onConfirm={clear} onCancel={() => setConfirm(false)} />}
    </div>
  );
}

const OTHER = "__other__";

// ModelPick chooses one model from the accounts that are set up, grouped by
// account, with a way to name any other model id on any account.
export function ModelPick({ label, value, onChange, routing, known, pools = false }: { label: string; value: LLMFallback; onChange: (v: LLMFallback) => void; routing: LLMRouting; known: LLMFallback[]; pools?: boolean }) {
  const [other, setOther] = useState(false);
  const list = useId();
  const providers = routing.providers.filter((p) => p.kind !== "fake");
  const models = (id: string) => [...new Set([...known.filter((k) => k.provider === id).map((k) => k.model), ...(value.provider === id && value.model ? [value.model] : [])])];
  const names = pools ? [...new Set([...Object.keys(routing.groups).sort(), ...(value.provider === "group" && value.model ? [value.model] : [])])] : [];
  const accounts = value.provider && !providers.some((p) => p.id === value.provider) && value.provider !== "group" ? [{ id: value.provider }, ...providers] : providers;
  const current = other ? OTHER : value.provider && value.model ? `${value.provider}|${value.model}` : "";
  const choose = (v: string) => {
    if (v === OTHER) return setOther(true);
    setOther(false);
    const at = v.indexOf("|");
    onChange(at < 0 ? { provider: "", model: "" } : { provider: v.slice(0, at), model: v.slice(at + 1) });
  };
  return (
    <div className="model-pick">
      <label>
        {label}
        <select value={current} onChange={(e) => choose(e.target.value)}>
          <option value="">Choose a model</option>
          {providers
            .filter((p) => models(p.id).length > 0)
            .map((p) => (
              <optgroup key={p.id} label={providerName(routing, p.id)}>
                {models(p.id).map((m) => (
                  <option key={m} value={`${p.id}|${m}`}>
                    {m === "default" ? "The program's own default" : m}
                  </option>
                ))}
              </optgroup>
            ))}
          {names.length > 0 && (
            <optgroup label="Model pools">
              {names.map((n) => (
                <option key={n} value={`group|${n}`}>
                  {n}
                </option>
              ))}
            </optgroup>
          )}
          <option value={OTHER}>Another model…</option>
        </select>
      </label>
      {other && (
        <div className="pick">
          <label>
            {label}: account
            <select value={value.provider} onChange={(e) => onChange({ provider: e.target.value, model: value.model })}>
              <option value="">Choose an account</option>
              {accounts.map((p) => (
                <option key={p.id} value={p.id}>
                  {providerName(routing, p.id)}
                </option>
              ))}
            </select>
          </label>
          <label>
            {label}: model name
            <input list={list} value={value.model} onChange={(e) => onChange({ provider: value.provider, model: e.target.value })} spellCheck={false} />
          </label>
          <datalist id={list}>
            {known.filter((k) => k.provider === value.provider).map((k) => (
              <option key={k.model} value={k.model} />
            ))}
          </datalist>
        </div>
      )}
    </div>
  );
}

// Jobs report their cost over this many days.
export const COST_DAYS = 30;
