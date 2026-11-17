import { useEffect, useState } from "react";
import { ApiError, get, post } from "../api/client";
import { autofillTabResult, runAccepted } from "../api/decoders_runs";
import { asApiError, useInterval, useResource } from "../hooks";
import { Chip, Empty, ErrorNote } from "../ui/parts";
import { CookieImport } from "./browser/CookieImport";
import { LanesPanel, showInPanel, useBrowserStatus } from "./browser/Lanes";
import { SessionSync, loadSessions } from "./browser/Sessions";
import { SignIns } from "./browser/SignIns";
import { ReasonNotAnApp } from "../wire";
import type { AutofillControl, AutofillTabResult, BrowserStatus } from "../wire";
import { useSearchParams } from "../router";
import { PageHeader } from "../ui/PageHeader";

const POLL_MS = 1000;

function Group({ title, items }: { title: string; items: AutofillControl[] }) {
  return (
    <section aria-label={title}>
      <h3>
        {title} <Chip>{items.length}</Chip>
      </h3>
      {items.length === 0 ? (
        <Empty>None.</Empty>
      ) : (
        <ul className="plain">
          {items.map((c, i) => (
            <li key={i}>
              {c.label}
              {c.reason && <span className="muted"> ({c.reason})</span>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Result({ runId }: { runId: string }) {
  const [res, retry] = useResource(() => get(`/api/autofill/active-tab/${runId}`, autofillTabResult), [runId], []);
  const running = res.data?.status === "running" || res.status === "loading";
  useInterval(retry, POLL_MS, running);
  if (res.status === "error" && !res.data) return <ErrorNote error={res.error} onRetry={retry} />;
  if (!res.data || res.data.status === "running") return <p role="status">Filling the tab</p>;
  const r: AutofillTabResult = res.data;
  if (r.status !== "succeeded") {
    return (
      <div role="alert" className="note error">
        <p>The fill {r.status}{r.reason ? `: ${r.reason}` : "."}</p>
      </div>
    );
  }
  return (
    <div aria-live="polite">
      <p>
        Filled {r.filled.length}, review {r.review.length}, missing {r.missing.length}, skipped {r.skipped.length}
        {r.url ? ` on ${r.url}` : ""}.
      </p>
      <p className="muted">
        Drafts awaiting review: {r.drafts}. Flagged values: {r.flagged}. Learning events accepted: {r.events}. Nothing was submitted; the tab and its form
        are yours.
      </p>
      <div className="grid">
        <Group title="Missing" items={r.missing} />
        <Group title="Review" items={r.review} />
        <Group title="Skipped" items={r.skipped} />
        <Group title="Filled" items={r.filled} />
      </div>
    </div>
  );
}

// AutofillTab fills the tab the apply window shows, once. The server refuses
// a page that is not an application and a window an application run holds, and
// says so in its own words. A window read to have no tab offers no press.
function AutofillTab({ status }: { status?: BrowserStatus }) {
  const noTab = status && (status.state !== "running" || status.apply.tabs.length === 0) ? "The apply window has no tab. Open the application form in it first." : undefined;
  const [runId, setRunId] = useState<string | null>(null);
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  async function press() {
    setBusy(true);
    setFailure(null);
    setRunId(null);
    try {
      setRunId((await post("/api/autofill/active-tab", runAccepted)).runId);
    } catch (e) {
      setFailure(asApiError(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section aria-labelledby="browser-autofill" className="panel">
      <header className="panel-head">
        <h2 id="browser-autofill">Autofill this tab</h2>
      </header>
      <p className="muted">
        Fills the tab the apply window shows, once. It never navigates, presses Next, Continue, Review or Submit, or closes the tab.
      </p>
      <div className="actions">
        <button type="button" disabled={busy || noTab !== undefined} title={noTab} onClick={() => void press()}>
          Autofill this tab
        </button>
      </div>
      {noTab && <p className="muted">{noTab}</p>}
      {failure && (
        <div role="alert" className="note error">
          <p className="sentence">{failure.message}</p>
          {failure.details?.reason === ReasonNotAnApp && <p className="muted">Open the application form in the apply window, then try again.</p>}
          {failure.requestId && <p className="muted">Request {failure.requestId}</p>}
        </div>
      )}
      {runId && <Result runId={runId} />}
    </section>
  );
}

export default function Browser() {
  const [sessions, reloadSessions] = useResource(loadSessions, [], ["browser"]);
  const [status, reloadStatus] = useBrowserStatus();
  // /browser?target=<tab id> opens the browser panel on that tab.
  const [params, setParams] = useSearchParams();
  const target = params.get("target");
  useEffect(() => {
    if (!target) return;
    showInPanel(target);
    setParams({ target: undefined }, { replace: true });
  }, [target, setParams]);
  return (
    <>
      <PageHeader path="/browser" />
      <LanesPanel res={status} reload={reloadStatus} />
      <SignIns sessions={sessions} onChecked={reloadSessions} />
      <CookieImport onImported={reloadSessions} />
      <SessionSync res={sessions} reload={reloadSessions} />
      <AutofillTab status={status.data} />
    </>
  );
}
