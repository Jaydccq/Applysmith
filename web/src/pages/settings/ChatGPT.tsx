import { useCallback, useEffect, useId, useState } from "react";
import { get, post } from "../../api/client";
import { chatgptModels, chatgptSignInStarted, chatgptSignOutResult, chatgptStatus } from "../../api/decoders_system";
import { useInterval } from "../../hooks";
import { Chip, ErrorNote, safeHref } from "../../ui/parts";
import type { ChatGPTStatus, LLMRouting } from "../../wire";
import { useAction } from "../action";
import { ensureProvider, RemoveAccount, SUBSCRIPTION_TEXT, statusOf, TestLine, useServiceTest } from "./modelsShared";

const POLL_MS = 1000;

// useChatGPT follows the Sign in with ChatGPT state of the server, which owns
// the tokens, and the models the signed-in account lists. It polls while a
// sign-in waits for the browser.
export function useChatGPT(active: boolean) {
  const [status, setStatus] = useState<ChatGPTStatus | null>(null);
  const [models, setModels] = useState<string[]>([]);
  const refresh = useCallback(() => {
    get("/api/llm/chatgpt", chatgptStatus).then(setStatus, () => {});
  }, []);
  useEffect(() => {
    if (active) refresh();
  }, [active, refresh]);
  useInterval(refresh, POLL_MS, !!status?.pending);
  const usable = !!status?.signedIn && status.planUsage;
  useEffect(() => {
    if (!usable) return setModels([]);
    get("/api/llm/chatgpt/models", chatgptModels).then((m) => setModels(m.models.map((x) => x.slug)), () => setModels([]));
  }, [usable]);
  return { status, models, refresh };
}

export type ChatGPTState = ReturnType<typeof useChatGPT>;

// startChatGPTSignIn adds the chatgpt provider when it is missing, then opens
// the sign-in page the server started in the browser; useChatGPT follows the
// outcome.
export async function startChatGPTSignIn(routing: LLMRouting): Promise<void> {
  await ensureProvider(routing, "chatgpt");
  const started = await post("/api/llm/chatgpt/signin", chatgptSignInStarted);
  const href = safeHref(started.url);
  if (href) window.open(href, "_blank", "noopener,noreferrer");
}

// ChatGPTCard is the sign-in of the chatgpt account: the user's ChatGPT plan,
// used through Sign in with ChatGPT. The card shows the account, never a token.
export function ChatGPTCard({ routing, status, models, refresh, reload }: { routing: LLMRouting; status: ChatGPTStatus | null; models: string[]; refresh: () => void; reload: () => void }) {
  const act = useAction();
  const test = useServiceTest(routing, "chatgpt", reload);
  const [unconfirmed, setUnconfirmed] = useState(false);
  const [model, setModel] = useState("");
  const list = useId();
  const added = routing.providers.some((p) => p.id === "chatgpt");
  const usable = !!status?.signedIn && status.planUsage;
  const st = statusOf(routing, "chatgpt", usable, status?.pending ? "Waiting for the browser" : status?.signedIn ? "Plan usage not allowed" : "Not signed in", "Signed in, not tested");
  const signIn = async () => {
    setUnconfirmed(false);
    await act.run(() => startChatGPTSignIn(routing));
    refresh();
    reload();
  };
  const signOut = async () => {
    const res = await act.run(() => post("/api/llm/chatgpt/signout", chatgptSignOutResult, {}));
    if (res) setUnconfirmed(!res.revoked);
    refresh();
    reload();
  };
  return (
    <li className="svc">
      <div className="svc-head">
        <span className="svc-title">
          <strong>{SUBSCRIPTION_TEXT.chatgpt.name}</strong>
          <Chip tone={st.tone}>{st.word}</Chip>
          {usable && <span className="muted">{status?.account || "your account"}</span>}
        </span>
        <span className="svc-actions">
          <button type="button" disabled={act.busy} onClick={() => void signIn()}>
            {usable ? "Sign in again" : "Sign in with ChatGPT"}
          </button>
          {usable && (
            <button type="button" disabled={test.busy} aria-label={`Test ${SUBSCRIPTION_TEXT.chatgpt.name}`} onClick={() => void test.run()}>
              Test
            </button>
          )}
          {(status?.signedIn || status?.pending) && (
            <button type="button" disabled={act.busy} onClick={() => void signOut()}>
              Sign out
            </button>
          )}
        </span>
      </div>
      <p className="muted">{SUBSCRIPTION_TEXT.chatgpt.blurb}</p>
      {status?.message && <p className="note error compact">{status.message}</p>}
      {unconfirmed && <p className="note compact">OpenAI did not confirm the revocation. The tokens are gone from this computer; to be sure, disconnect Applysmith in ChatGPT settings.</p>}
      {act.error && <ErrorNote error={act.error} />}
      {test.error && <ErrorNote error={test.error} />}
      {usable && models.length > 0 && <p className="muted">Models on your plan: {models.join(", ")}</p>}
      <TestLine routing={routing} id="chatgpt" />
      {usable && (
        <details className="svc-more">
          <summary>Test another model</summary>
          <div className="svc-try">
            <input list={list} value={model} placeholder="Model name" aria-label="Model to test on ChatGPT plan" spellCheck={false} onChange={(e) => setModel(e.target.value)} />
            <datalist id={list}>
              {models.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
            <button type="button" disabled={test.busy || !model.trim()} onClick={() => void test.run(model)}>
              Test this model
            </button>
          </div>
        </details>
      )}
      {added && !usable && !status?.pending && <RemoveAccount id="chatgpt" name={SUBSCRIPTION_TEXT.chatgpt.name} reload={reload} />}
    </li>
  );
}
