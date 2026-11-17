import { useId, useState } from "react";
import { useInterval, type Resource } from "../../hooks";
import { Chip, Empty, ErrorNote } from "../../ui/parts";
import type { LLMPreset, LLMProvider, LLMRouting, ProviderAccount, ProviderAccounts } from "../../wire";
import { useAction } from "../action";
import { ChatGPTCard, type ChatGPTState } from "./ChatGPT";
import { ensureProvider, KeyForm, KEYED_KINDS, PLAN_MONITOR, providerName, RemoveAccount, serviceName, statusOf, SUBSCRIPTION_TEXT, TestLine, useServiceTest } from "./modelsShared";
import { UsageSetup, UsageWindows } from "./PlanUsage";
import { ProviderEditor } from "./ProviderEditor";
import { ProviderPrice } from "./ProviderPrice";

const POLL_MS = 1000;

// A service whose key runs jobs: a plan's key or a pay-per-use API key. The
// card says whether the key is saved, tests it on request and keeps the key,
// the prices and the account itself behind Manage.
function KeyedCard({ routing, reload, id, name, blurb, preset, provider, subscription, monitor, reloadAccounts }: {
  routing: LLMRouting;
  reload: () => void;
  id: string;
  name: string;
  blurb?: string;
  preset?: LLMPreset;
  provider?: LLMProvider;
  subscription: boolean;
  monitor?: ProviderAccount;
  reloadAccounts: () => void;
}) {
  const test = useServiceTest(routing, id, reload, preset?.name);
  const [model, setModel] = useState("");
  const list = useId();
  const saved = provider?.keyConfigured ?? preset?.keyConfigured ?? false;
  const st = statusOf(routing, id, saved, "Not connected", "Key saved, not tested");
  const models = routing.knownModels.filter((k) => k.provider === id).map((k) => k.model);
  return (
    <li className={blurb ? "svc" : "svc compact"}>
      <div className="svc-head">
        <span className="svc-title">
          <strong>{name}</strong>
          <Chip tone={st.tone}>{st.word}</Chip>
        </span>
        {saved && (
          <button type="button" disabled={test.busy} aria-label={`Test ${name}`} onClick={() => void test.run()}>
            Test
          </button>
        )}
      </div>
      {blurb && <p className="muted">{blurb}</p>}
      {test.error && <ErrorNote error={test.error} />}
      <TestLine routing={routing} id={id} />
      {monitor && <UsageWindows account={monitor} />}
      <details className="svc-more">
        <summary>{saved ? "Manage" : "Add key"}</summary>
        <div className="stack">
          <KeyForm id={id} name={name} configured={saved} onChanged={reload} afterSave={preset ? () => ensureProvider(routing, id, preset.name) : undefined} />
          {saved && (
            <div className="svc-try">
              <input list={list} value={model} placeholder="Model name" aria-label={`Model to test on ${name}`} spellCheck={false} onChange={(e) => setModel(e.target.value)} />
              <datalist id={list}>
                {[...new Set(models)].map((m) => (
                  <option key={m} value={m} />
                ))}
              </datalist>
              <button type="button" disabled={test.busy || !model.trim()} onClick={() => void test.run(model)}>
                Test this model
              </button>
            </div>
          )}
          {provider && !subscription && KEYED_KINDS.has(provider.kind) && <ProviderPrice provider={id} models={models} reload={reload} />}
          {monitor && <UsageSetup account={monitor} changed={reloadAccounts} />}
          {provider && <RemoveAccount id={id} name={name} reload={reload} />}
        </div>
      </details>
    </li>
  );
}

// A subscription that works by running a program installed on this Mac. Adding
// it only makes the program available to pick; no job changes.
function ProgramCard({ routing, reload, preset, account }: { routing: LLMRouting; reload: () => void; preset: LLMPreset; account?: ProviderAccount }) {
  const act = useAction();
  const text = SUBSCRIPTION_TEXT[preset.name];
  const name = serviceName(preset);
  const added = routing.providers.some((p) => p.id === preset.name);
  const test = useServiceTest(routing, preset.name, reload);
  const st = statusOf(routing, preset.name, added, "Not connected", "Added, not tested");
  const program = preset.program ?? preset.name;
  return (
    <li className="svc">
      <div className="svc-head">
        <span className="svc-title">
          <strong>{name}</strong>
          <Chip tone={st.tone}>{st.word}</Chip>
        </span>
        <span className="svc-actions">
          {added ? (
            <button type="button" disabled={test.busy} aria-label={`Test ${name}`} onClick={() => void test.run()}>
              Test
            </button>
          ) : (
            <button type="button" disabled={act.busy || account?.installed === false} aria-label={`Connect ${name}`} onClick={() => void act.run(async () => (await ensureProvider(routing, preset.name), true)).then((done) => done && reload())}>
              Connect
            </button>
          )}
          {added && <RemoveAccount id={preset.name} name={name} reload={reload} />}
        </span>
      </div>
      <p className="muted">{text?.blurb ?? "Works by running a program installed on this Mac."}</p>
      {account && <p>{account.installed ? `The ${program} program is installed on this Mac.` : `The ${program} program is not installed on this Mac. Install it, then press Connect.`}</p>}
      {account?.loginCommand && (
        <p>
          Sign in once in Terminal: <code>{account.loginCommand}</code>
        </p>
      )}
      {account?.authFilePresent && <p className="muted">A login file is already there. Applysmith cannot tell if it is still valid; press Test to find out.</p>}
      {act.error && <ErrorNote error={act.error} />}
      {test.error && <ErrorNote error={test.error} />}
      <TestLine routing={routing} id={preset.name} />
    </li>
  );
}

export function Accounts({ routing, reload, accounts, reloadAccounts, chatgpt }: { routing: LLMRouting; reload: () => void; accounts: Resource<ProviderAccounts>; reloadAccounts: () => void; chatgpt: ChatGPTState }) {
  useInterval(reload, POLL_MS, !!routing.testing);
  const found = new Map((accounts.data?.accounts ?? []).map((a) => [a.id, a]));
  // The plan you sign in with comes first, then programs, then keyed plans.
  const rank = (p: LLMPreset) => (p.kind === "chatgpt" ? 0 : p.kind === "cli" ? 1 : 2);
  const presets = routing.presets.filter((p) => p.kind !== "fake").sort((a, b) => rank(a) - rank(b));
  const presetOf = (p: LLMProvider) => routing.presets.find((x) => x.name === p.preset);
  // Accounts that are not a service's first: a second key for it, or an endpoint of the user's own.
  const extras = routing.providers.filter((p) => p.kind !== "fake" && !routing.presets.some((x) => x.name === p.id));
  // Each service as a card, with whether it is connected: a key saved or an account added.
  const cards = (subscription: boolean) => [
    ...presets
      .filter((p) => p.subscription === subscription)
      .map((p) => {
        const provider = routing.providers.find((x) => x.id === p.name);
        const connected = !!provider || p.keyConfigured;
        if (p.kind === "chatgpt") return { connected, el: <ChatGPTCard key={p.name} routing={routing} status={chatgpt.status} models={chatgpt.models} refresh={chatgpt.refresh} reload={reload} /> };
        if (p.kind === "cli") return { connected, el: <ProgramCard key={p.name} routing={routing} reload={reload} preset={p} account={found.get(p.name)} /> };
        return {
          connected,
          el: <KeyedCard key={p.name} routing={routing} reload={reload} id={p.name} name={serviceName(p)} blurb={SUBSCRIPTION_TEXT[p.name]?.blurb} preset={p} provider={provider} subscription={subscription} monitor={found.get(PLAN_MONITOR[p.name] ?? "")} reloadAccounts={reloadAccounts} />,
        };
      }),
    ...extras
      .filter((p) => !!presetOf(p)?.subscription === subscription)
      .map((p) => ({ connected: true, el: <KeyedCard key={p.id} routing={routing} reload={reload} id={p.id} name={providerName(routing, p.id)} provider={p} subscription={subscription} reloadAccounts={reloadAccounts} /> })),
  ];
  const subscriptions = cards(true).map((c) => c.el);
  const paid = cards(false);
  const connected = paid.filter((c) => c.connected).map((c) => c.el);
  const offered = paid.filter((c) => !c.connected).map((c) => c.el);
  // Programs that cannot run a job are listed apart, so none reads as a model to pick.
  const others = [...found.values()].filter((a) => a.mode === "native" && !presets.some((p) => p.kind === "cli" && p.name === a.id));
  return (
    <>
      <h3 id="settings-accounts">Accounts</h3>
      <p>Connect the services you pay for or subscribe to. Connecting an account never changes which model a job uses; that is set under What each job uses.</p>
      <h4>Subscriptions</h4>
      <p className="muted">Use your plan&apos;s allowance, so a call costs nothing extra.</p>
      {subscriptions.length === 0 ? <Empty>No subscription service is available.</Empty> : <ul className="plain svc-list">{subscriptions}</ul>}
      <h4>Pay-per-use API keys</h4>
      <p className="muted">Billed by the service for what you use. Keys are stored on this computer and never shown again.</p>
      {connected.length > 0 ? <ul className="plain svc-list">{connected}</ul> : <p className="muted">No pay-per-use service is connected yet.</p>}
      {offered.length > 0 && (
        <details className="svc-more offered">
          <summary>Add a pay-per-use service ({offered.length})</summary>
          <ul className="plain svc-list">{offered}</ul>
        </details>
      )}
      <ProviderEditor routing={routing} reload={reload} />
      <details className="svc-more">
        <summary>Other AI apps on this Mac</summary>
        {accounts.status === "error" && !accounts.data ? (
          <p className="muted">Could not check this Mac for other AI apps.</p>
        ) : (
          <ul className="plain stack">
            {others.map((a) => (
              <li key={a.id}>
                <strong>{a.name}</strong> <span className="muted">{a.installed ? "Detected on this Mac. Applysmith cannot use it to run jobs." : "Not detected. Applysmith could not use it to run jobs anyway."}</span>
              </li>
            ))}
          </ul>
        )}
      </details>
    </>
  );
}
