import { useEffect, useRef, useState, type FormEvent } from "react";
import { get, put } from "../../api/client";
import { llmKeyStatus, llmRouting } from "../../api/decoders";
import { asApiError, useResource } from "../../hooks";
import { Chip, ErrorNote, ExternalLink } from "../../ui/parts";
import { useAction, when } from "../action";
import { startChatGPTSignIn, useChatGPT } from "../settings/ChatGPT";
import { SUBSCRIPTION_TEXT } from "../settings/modelsShared";
import { ApplyOutcome, useApply } from "./apply";
import type { ScreenProps } from "./model";

const OFFLINE = "offline";

// Llm picks how the app reaches a language model. A key goes through a
// write-only field to the key store before the choice is applied; the ChatGPT
// plan takes no key, only Sign in with ChatGPT. The apply answer carries the
// result of the test call, which is shown as it comes.
export function Llm({ step, state, reload }: ScreenProps) {
  const [routing] = useResource(() => get("/api/llm/routing", llmRouting), [], ["setup", "configuration"]);
  const apply = useApply(step.id, reload);
  const enumOf = (name: string) => step.fields.find((f) => f.name === name)?.enum ?? [];
  const apis = enumOf("preset");
  const clis = enumOf("cli");
  const limits = step.fields.find((f) => f.name === "acknowledge_cli_limits")?.doc ?? "";
  const [choice, setChoice] = useState("");
  const [understood, setUnderstood] = useState(false);
  const [filled, setFilled] = useState(false);
  const [keyError, setKeyError] = useState<unknown>(null);
  const [working, setWorking] = useState(false);
  // Uncontrolled: React never mirrors the typed key into an attribute, and the
  // field is emptied as soon as it is sent.
  const key = useRef<HTMLInputElement>(null);

  const presets = routing.status === "ready" ? routing.data.presets : [];
  // The stored choice starts selected, so a test that failed can be run again
  // with one press. A step never configured starts with none.
  const stored = routing.status === "ready" && step.status !== "missing" ? (routing.data.offline ? OFFLINE : routing.data.preset) : "";
  useEffect(() => {
    if (stored) setChoice((c) => c || stored);
  }, [stored]);
  const meta = (name: string) => presets.find((p) => p.name === name);
  const label = (name: string) => meta(name)?.label ?? name;
  const isApi = apis.includes(choice);
  const isCli = clis.includes(choice);
  const isPlan = isApi && meta(choice)?.kind === "chatgpt";
  const isKey = isApi && !isPlan;
  const chatgpt = useChatGPT(isPlan);
  const signing = useAction();
  const signedIn = !!chatgpt.status?.signedIn && !!chatgpt.status.planUsage;
  const saved = isKey && (meta(choice)?.keyConfigured ?? state.secrets.some((s) => s.present && s.name.includes(choice)));
  const ready = choice === OFFLINE || (isPlan && signedIn) || (isKey && (filled || saved)) || (isCli && understood);

  const signIn = async () => {
    if (routing.status !== "ready") return;
    const at = routing.data;
    await signing.run(() => startChatGPTSignIn(at));
    chatgpt.refresh();
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setKeyError(null);
    setWorking(true);
    try {
      if (isApi) {
        const typed = isKey ? (key.current?.value ?? "") : "";
        if (typed) {
          const sent = put(`/api/llm/keys/${encodeURIComponent(choice)}`, llmKeyStatus, { key: typed });
          if (key.current) key.current.value = "";
          setFilled(false);
          try {
            await sent;
          } catch (err) {
            setKeyError(asApiError(err));
            return;
          }
        }
        await apply.send({ preset: choice });
      } else if (isCli) {
        await apply.send({ cli: choice, acknowledge_cli_limits: true });
      } else {
        await apply.send({ offline: true });
      }
    } finally {
      setWorking(false);
    }
  };

  const options = [...apis, ...clis, OFFLINE];
  // Only the key of the provider chosen now: a key stored for another one says
  // nothing about this choice.
  const keyed = isKey ? state.secrets.filter((s) => s.name === `api-key-${choice}`) : [];
  return (
    <form onSubmit={(e) => void submit(e)} className="form-grid">
      <fieldset>
        <legend>How should the app reach a model?</legend>
        {options.map((o) => (
          <label key={o} className="check">
            <input type="radio" name="llm-choice" checked={choice === o} onChange={() => { setChoice(o); setUnderstood(false); setKeyError(null); }} />
            {o === OFFLINE ? "Offline: no key yet" : clis.includes(o) ? `${label(o)} (command-line tool)` : label(o)}
          </label>
        ))}
      </fieldset>

      {isPlan && (
        <div className="key-row">
          <p>{SUBSCRIPTION_TEXT.chatgpt.blurb} Sign in in the browser page that opens, then come back here.</p>
          <p>
            <button type="button" disabled={signing.busy || routing.status !== "ready"} onClick={() => void signIn()}>
              {signedIn ? "Sign in again" : "Sign in with ChatGPT"}
            </button>{" "}
            <Chip tone={signedIn ? "good" : "warn"}>
              {signedIn ? "signed in" : chatgpt.status?.pending ? "waiting for the browser" : chatgpt.status?.signedIn ? "plan usage not allowed" : "not signed in"}
            </Chip>
            {signedIn && chatgpt.status?.account && <span className="muted"> {chatgpt.status.account}</span>}
          </p>
          {chatgpt.status?.message && <p className="note error compact">{chatgpt.status.message}</p>}
          {signing.error && <ErrorNote error={signing.error} />}
        </div>
      )}
      {isKey && (
        <div className="key-row">
          <p>
            {meta(choice)?.keyUrl ? (
              <>
                Create a key for {label(choice)} at <ExternalLink href={meta(choice)?.keyUrl ?? ""}>{meta(choice)?.keyUrl}</ExternalLink>.
              </>
            ) : (
              <>Create an API key in your {label(choice)} account.</>
            )}
          </p>
          <label>
            API key <Chip tone={saved ? "good" : "warn"}>{saved ? "saved" : "not saved"}</Chip>
            <input ref={key} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => setFilled(e.target.value !== "")} placeholder={saved ? "Enter a new key to replace it" : "Paste the key"} />
          </label>
        </div>
      )}
      {isCli && (
        <div>
          <p role="note">{limits.replace(/^Required with cli: you have read that /, "")}</p>
          <label className="check">
            <input type="checkbox" checked={understood} onChange={(e) => setUnderstood(e.target.checked)} />
            I have read this and want to continue
          </label>
        </div>
      )}
      {/* Once offline is stored, the step's own warning above says what it means. */}
      {choice === OFFLINE && stored !== OFFLINE && <p>Nothing is sent to a model: evaluations and written answers are simulated, and reports say so. Real evaluation needs a model, so choose a provider when you have a key. Setup goes on either way.</p>}

      {keyed.length > 0 && (
        <ul className="plain stack" aria-label="Stored keys">
          {keyed.map((s) => (
            <li key={s.name}>
              {s.name}: <Chip tone={s.present ? "good" : "warn"}>{s.present ? "present" : "absent"}</Chip>
              {s.savedAt && <span className="muted"> saved {when(s.savedAt)}</span>}
            </li>
          ))}
        </ul>
      )}
      {keyError != null && <ErrorNote error={keyError} />}
      <div className="actions">
        <button type="submit" disabled={!ready || working || apply.busy}>
          {working || apply.busy ? "Testing the connection" : "Save and test"}
        </button>
      </div>
      <ApplyOutcome apply={apply} />
    </form>
  );
}
