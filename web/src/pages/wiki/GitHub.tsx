import { useCallback, useEffect, useId, useState } from "react";
import { del, get, post } from "../../api/client";
import { kbGitHubAccount, kbGitHubRepos, kbGitHubSignIn } from "../../api/decoders_pages";
import { asApiError, useInterval } from "../../hooks";
import { Chip, ErrorNote, ageLabel, safeHref } from "../../ui/parts";
import type { KBGitHubAccount, KBGitHubRepo } from "../../wire";
import { useAction } from "../action";

const POLL_MS = 2000;

// useGitHubAccount follows the GitHub account of the server, which owns the
// sign-in and the token. It polls while a sign-in waits for GitHub.
export function useGitHubAccount() {
  const [status, setStatus] = useState<KBGitHubAccount | null>(null);
  const [error, setError] = useState<unknown>(null);
  const refresh = useCallback(() => {
    get("/api/kb/github", kbGitHubAccount).then((s) => (setError(null), setStatus(s)), (e) => setError(asApiError(e)));
  }, []);
  useEffect(refresh, [refresh]);
  useInterval(refresh, POLL_MS, status?.state === "pending");
  return { status, error, refresh };
}

type GitHubAccountState = ReturnType<typeof useGitHubAccount>;

// verificationLabel words the address the code is entered at, as "github.com/login/device".
function verificationLabel(uri: string): string {
  try {
    const u = new URL(uri);
    return u.host + u.pathname.replace(/\/$/, "");
  } catch {
    return uri;
  }
}

// GitHubSignIn is the account of the repository sources: signed out it starts
// the device flow, pending it shows the code to enter on GitHub, signed in it
// names the account. The page never holds the token.
export function GitHubSignIn({ account }: { account: GitHubAccountState }) {
  const { status, error, refresh } = account;
  const act = useAction();
  const [copied, setCopied] = useState("");
  const signIn = async () => {
    setCopied("");
    await act.run(() => post("/api/kb/github/signin", kbGitHubSignIn));
    refresh();
  };
  const signOut = async () => {
    setCopied("");
    await act.run(() => del("/api/kb/github", kbGitHubAccount));
    refresh();
  };
  const copy = async (code: string) => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied("Code copied.");
    } catch {
      setCopied("Copying is not available in this browser. Type the code in.");
    }
  };
  if (!status) return error ? <ErrorNote error={error} onRetry={refresh} what="the GitHub account" /> : null;
  const href = safeHref(status.verificationUri ?? "");
  return <div className="form-grid" role="group" aria-label="GitHub account">
    <h3>GitHub account <Chip tone={status.state === "signed-in" ? "good" : status.state === "pending" ? "info" : status.state === "error" ? "bad" : "neutral"}>{status.state === "signed-in" ? "Signed in" : status.state === "pending" ? "Waiting for GitHub" : status.state === "error" ? "Needs attention" : "Signed out"}</Chip></h3>
    {status.state === "signed-out" && <p className="muted">Sign in to list your repositories, private ones included, and read the ones you pick into the wiki. Applysmith asks GitHub for read access to your repositories and keeps the token on this computer only.</p>}
    {status.state === "pending" && <>
      <p>Enter this code on GitHub:</p>
      <p><code className="big-code" aria-label="Sign-in code">{status.userCode}</code></p>
      <div className="actions">
        <button type="button" onClick={() => void copy(status.userCode ?? "")}>Copy code</button>
        {href && <a className="button primary" href={href} target="_blank" rel="noopener noreferrer">Open {verificationLabel(status.verificationUri ?? "")}</a>}
        <button type="button" className="danger-outline" disabled={act.busy} onClick={() => void signOut()}>Cancel</button>
      </div>
      <p className="muted" role="status">Waiting for you to approve it on GitHub. This page updates by itself.{copied && ` ${copied}`}</p>
    </>}
    {status.state === "signed-in" && <>
      <p>Signed in as <strong>{status.login}</strong> ({status.source === "gh" ? "via the gh command" : "via the app"}).</p>
      {status.source === "gh" && <p className="muted">This is the sign-in of the gh command on this computer, and Applysmith cannot sign it out; run <code>gh auth logout</code> for that. Signing in with GitHub here stores a token of its own for the app.</p>}
    </>}
    {status.state === "error" && <p className="note error" role="alert">{status.error}</p>}
    {status.state === "signed-in" && status.error && <p className="note" role="status">{status.error}</p>}
    {status.state !== "pending" && <div className="actions">
      <button type="button" className={status.state === "signed-in" ? undefined : "primary"} disabled={act.busy} onClick={() => void signIn()}>{status.state === "signed-in" ? "Sign in again" : "Sign in with GitHub"}</button>
      {status.state === "signed-in" && status.source === "vault" && <button type="button" className="danger-outline" disabled={act.busy} onClick={() => void signOut()}>Sign out</button>}
    </div>}
    <div aria-live="polite">{act.error && <ErrorNote error={act.error} />}{error != null && <ErrorNote error={error} onRetry={refresh} what="the GitHub account" />}</div>
  </div>;
}

// RepoPicker lists the repositories of the signed-in account, newest push
// first, to read into the wiki. added are the "owner/name" of the sources
// already added (lower case); busy holds the buttons while a pick is under way.
export function RepoPicker({ added, busy, onRead }: { added: ReadonlySet<string>; busy: boolean; onRead: (fullName: string) => void }) {
  const [repos, setRepos] = useState<KBGitHubRepo[]>([]);
  const [page, setPage] = useState(0);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [query, setQuery] = useState("");
  const field = useId();
  const load = useCallback((next: number) => {
    setLoading(true);
    setError(null);
    get("/api/kb/github/repos", kbGitHubRepos, { page: next }).then(
      (r) => (setRepos((prev) => (next === 1 ? r.repos : [...prev, ...r.repos])), setPage(next), setMore(r.more), setLoading(false)),
      (e) => (setError(asApiError(e)), setLoading(false)),
    );
  }, []);
  useEffect(() => load(1), [load]);
  const q = query.trim().toLowerCase();
  const shown = repos.filter((r) => q === "" || [r.fullName, r.description, r.language].some((s) => s.toLowerCase().includes(q)));
  return <div role="group" aria-label="Your GitHub repositories">
    <h3>Your repositories</h3>
    <label className="search-field" htmlFor={field}>Search your repositories by name, description or language</label>
    <input id={field} type="search" value={query} onChange={(e) => setQuery(e.target.value)} />
    {error != null && <ErrorNote error={error} onRetry={() => load(page + 1)} what="your repositories" />}
    {repos.length === 0 && !loading && error == null && <p className="muted">This account lists no repository.</p>}
    {loading && repos.length === 0 && <p className="muted" role="status">Loading your repositories...</p>}
    {repos.length > 0 && shown.length === 0 && <p className="muted">No loaded repository matches the search.{more && " Load more to search further back."}</p>}
    <ul className="plain stack">
      {shown.map((r) => {
        const isAdded = r.alreadyAdded || added.has(r.fullName.toLowerCase());
        return <li key={r.fullName} className="panel">
          <div className="row">
            <strong>{r.fullName}</strong>
            {r.private && <Chip>Private</Chip>}
            {r.language && <Chip raw>{r.language}</Chip>}
            {isAdded && <Chip tone="good">Added</Chip>}
          </div>
          {r.description && <p className="muted clamp">{r.description}</p>}
          {r.pushedAt && <p className="muted">Last pushed {ageLabel(r.pushedAt)}</p>}
          <div className="actions">
            <button type="button" className="small primary" disabled={busy || isAdded} onClick={() => onRead(r.fullName)} aria-label={`Read ${r.fullName} into the wiki`}>{isAdded ? "Already added" : "Read into the wiki"}</button>
          </div>
        </li>;
      })}
    </ul>
    {more && <div className="actions"><button type="button" disabled={loading} onClick={() => load(page + 1)}>{loading ? "Loading..." : "Load more"}</button></div>}
  </div>;
}
