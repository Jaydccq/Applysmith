import { useEffect, useId, useState, type FormEvent } from "react";
import { ApiError, get, patch } from "../../api/client";
import { llmRouting } from "../../api/decoders";
import { providerAccountList } from "../../api/decoders_provider_accounts";
import { asApiError, useResource } from "../../hooks";
import { Link } from "../../router";
import { SaveBar } from "../../ui/SaveBar";
import { usd } from "../../ui/parts";
import type { LLMCostRow, LLMRouting } from "../../wire";
import { Accounts } from "./Accounts";
import { useChatGPT } from "./ChatGPT";
import { Jobs } from "./Jobs";
import { Feedback, serviceName, type Action } from "./modelsShared";

// Spending is the cap on the day's spend, on a save bar.
function Spending({ routing, onChanged }: { routing: LLMRouting; onChanged: () => void }) {
  const [value, setValue] = useState(String(routing.dailyUsdCap));
  const [result, setResult] = useState<Action>(null);
  const [busy, setBusy] = useState(false);
  const [savedAt, setSavedAt] = useState("");
  const field = useId();
  useEffect(() => setValue(String(routing.dailyUsdCap)), [routing.dailyUsdCap]);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const n = Number(value);
    if (!Number.isFinite(n) || n < 0) {
      setResult({ fail: new ApiError("validation_failed", "The daily cap must be zero or more.", { requestId: "" }) });
      return;
    }
    setBusy(true);
    setSavedAt("");
    try {
      await patch("/api/llm/routing", llmRouting, { dailyUsdCap: n });
      setResult(null);
      setSavedAt(new Date().toLocaleTimeString());
      onChanged();
    } catch (err) {
      setResult({ fail: asApiError(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} className="inline-form">
      <label htmlFor={field}>Daily cap, in US dollars</label>
      <input id={field} type="number" inputMode="decimal" min={0} step={0.01} value={value} onChange={(e) => setValue(e.target.value)} />
      <p>
        Spent today: {usd(routing.capSpentUsd)}. Over the cap, calls move to the cheapest model and unattended evaluation waits until midnight. Plan subscriptions cost nothing per call and are not counted. A single job can have its own daily budget under Change, Advanced. <Link to="/usage">Usage</Link>
      </p>
      <Feedback result={result} />
      <SaveBar dirty={value !== String(routing.dailyUsdCap)} savedAt={savedAt}>
        <button type="submit" disabled={busy}>
          Save cap
        </button>
      </SaveBar>
    </form>
  );
}

// ModelsBody is the whole section: the accounts that can be connected, what
// each job uses, and what it may spend.
export function ModelsBody({ routing, byTask, reload, onNames }: { routing: LLMRouting; byTask?: LLMCostRow[]; reload: () => void; onNames: (names: string[]) => void }) {
  const hasChatGPT = routing.presets.some((p) => p.kind === "chatgpt") || routing.providers.some((p) => p.kind === "chatgpt");
  const chatgpt = useChatGPT(hasChatGPT);
  const [accounts, reloadAccounts] = useResource(() => get("/api/llm/accounts", providerAccountList), [], [], { keep: "settings.accounts" });
  const signedIn = !!chatgpt.status?.signedIn && chatgpt.status.planUsage;

  // The services are found by name, as the page draws them.
  const names = [...routing.presets.map(serviceName), ...routing.providers.map((p) => p.id)];
  useEffect(() => onNames(names), [onNames, names.join("\n")]);

  const unusable = routing.tasks.filter((t) => t.unrouted || t.broken).length;
  // An opt-in job left off has no model and no reason it lacks one: it is not a job to route.
  const on = routing.tasks.filter((t) => t.provider || t.unrouted || t.broken).length;
  return (
    <>
      {unusable > 0 && (
        <div role="status" className="banner">
          {unusable} of {on} jobs {unusable === 1 ? "has" : "have"} no usable model. Connect an account below, then <a href="#settings-default">change the default</a> to route {unusable === 1 ? "it" : "them"}.
        </div>
      )}
      <Accounts routing={routing} reload={reload} accounts={accounts} reloadAccounts={reloadAccounts} chatgpt={chatgpt} />
      <Jobs routing={routing} cost={byTask} reload={reload} signedIn={signedIn} extraModels={chatgpt.models.map((model) => ({ provider: "chatgpt", model }))} />
      <h3 id="settings-spending">Spending</h3>
      <Spending routing={routing} onChanged={reload} />
    </>
  );
}
