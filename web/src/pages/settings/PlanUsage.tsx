import { useState, type FormEvent } from "react";
import { del, post, put } from "../../api/client";
import { providerAccount } from "../../api/decoders_provider_accounts";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote } from "../../ui/parts";
import type { ProviderAccount } from "../../wire";
import { useAction, when } from "../action";
import { usePasswordField } from "./passwordField";

function Credential({ account, kind, changed }: { account: ProviderAccount; kind: "key" | "cookie" | "workspace"; changed: () => void }) {
  const act = useAction();
  const input = usePasswordField();
  const [forget, setForget] = useState(false);
  const label = kind === "key" ? "Plan API key" : kind === "cookie" ? "Console Cookie header" : "Workspace ID";
  const configured = kind === "key" ? account.keyConfigured : kind === "cookie" ? account.cookieConfigured : account.workspaceConfigured;
  const path = `/api/llm/accounts/${encodeURIComponent(account.id)}/credentials`;
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const value = input.take();
    if (!value.trim()) return;
    if (await act.run(() => put(path, providerAccount, { kind, value }))) changed();
  };
  return <form className="stack" onSubmit={(e) => void save(e)}>
    <label>{label} for {account.name}
      <input ref={input.ref} type={kind === "workspace" ? "text" : "password"} autoComplete="off" spellCheck={false} placeholder={configured ? "Saved; enter a replacement" : kind === "workspace" ? "wrk_…" : "Not saved"} onChange={(e) => input.onInput(e.target.value)} />
    </label>
    <p className="row">
      <Chip tone={configured ? "good" : "neutral"}>{configured ? "saved" : "not saved"}</Chip>
      <button type="submit" disabled={input.text.trim() === "" || act.busy}>Save {kind}</button>
      {configured && <button type="button" disabled={act.busy} onClick={() => setForget(true)}>Forget {kind}</button>}
    </p>
    {act.error && <ErrorNote error={act.error} />}
    {forget && <ConfirmDialog title={`Forget ${label.toLowerCase()} for ${account.name}`} consequence="The stored credential will be removed from this Applysmith workspace." verb="Forget credential" busy={act.busy} onCancel={() => setForget(false)} onConfirm={() => { void act.run(() => del(`${path}/${kind}`, providerAccount)).then((result) => { setForget(false); if (result) changed(); }); }} />}
  </form>;
}

// UsageWindows shows what a plan has used, once the service has said. Unknown
// or expired usage is never drawn as zero.
export function UsageWindows({ account }: { account: ProviderAccount }) {
  const usage = account.usage;
  if (usage.status !== "ok" || usage.windows.length === 0) return null;
  return (
    <div className="plan-usage">
      <ul className="plain stack">
        {usage.windows.map((w, i) => (
          <li key={`${w.name}-${i}`}>
            <strong>{w.name}</strong>
            {w.usedPercent !== undefined && (
              <>
                {" "}
                · {w.usedPercent.toFixed(1)}% used <progress aria-label={`${account.name} ${w.name} usage`} value={w.usedPercent} max={100} />
              </>
            )}
            {w.used !== undefined && <> · used {w.used.toLocaleString()}</>}
            {w.limit !== undefined && <> of {w.limit.toLocaleString()}</>}
            {w.remaining !== undefined && <> · {w.remaining.toLocaleString()} remaining</>}
            {w.resetsAt && <> · resets {when(w.resetsAt)}</>}
          </li>
        ))}
      </ul>
      {usage.checkedAt && <p className="muted">Checked {when(usage.checkedAt)}</p>}
    </div>
  );
}

// UsageSetup is where a plan's usage is tracked: the credentials for its usage
// page, kept apart from the key that runs jobs, and the one control that asks
// the service. Only Refresh usage contacts it.
export function UsageSetup({ account, changed }: { account: ProviderAccount; changed: () => void }) {
  const refresh = useAction();
  const usage = account.usage;
  return (
    <div className="stack">
      <h4>Plan usage</h4>
      <p className="muted">Track how much of the plan you have used. These credentials are kept in this workspace&apos;s local vault, are separate from the key that runs jobs, and are sent only to the plan&apos;s own service when you press Refresh usage. {account.note}</p>
      <Credential account={account} kind="key" changed={changed} />
      {account.cookieSupported && (
        <>
          <p className="muted">Optional console Cookie header: sign in to the console, then copy the Cookie request header from DevTools, Network. A saved key takes priority.</p>
          <Credential account={account} kind="cookie" changed={changed} />
        </>
      )}
      {account.id === "opencode-go" && <Credential account={account} kind="workspace" changed={changed} />}
      <div aria-live="polite">
        <p>
          <Chip tone={usage.status === "ok" ? "good" : usage.status === "error" ? "warn" : "neutral"}>{usage.status === "ok" ? "usage available" : usage.status}</Chip> {usage.message}
        </p>
        {refresh.error && <ErrorNote error={refresh.error} />}
      </div>
      <p>
        <button
          type="button"
          aria-label={`Refresh usage for ${account.name}`}
          disabled={refresh.busy}
          onClick={() => {
            void refresh.run(() => post(`/api/llm/accounts/${encodeURIComponent(account.id)}/refresh`, providerAccount)).then((result) => {
              if (result) changed();
            });
          }}
        >
          {refresh.busy ? "Refreshing…" : "Refresh usage"}
        </button>
      </p>
    </div>
  );
}
