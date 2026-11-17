import { useState } from "react";
import { post } from "../../api/client";
import { signInChecks } from "../../api/decoders_runs";
import { browserNewTab } from "../../api/decoders_browser";
import type { Resource } from "../../hooks";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import type { SessionSiteView, SessionsView, SignInCheck } from "../../wire";
import { useAction, when } from "../action";
import { showInPanel } from "./Lanes";

const NEXT_WORDS: Record<string, string> = {
  proceed: "ready",
  "retry-later": "try again later",
  "needs-user": "you need to sign in",
  "blocked-by-safety": "a challenge is in the way; it is left to you",
};

const STATE_WORDS: Record<string, string> = { signed_in: "signed in", signed_out: "signed out", unknown: "unknown" };

const stateWord = (s: string) => STATE_WORDS[s] ?? s.replace("_", " ");

// nextOf is what a check found, in words: the state and what to do about it,
// or why the site could not be checked.
const nextOf = (c: SignInCheck) => (c.error ? `not checked (${c.error})` : `${stateWord(c.state)} (${NEXT_WORDS[c.next] ?? c.next}${c.checkedAt ? `, checked ${when(c.checkedAt)}` : ""})`);

// SiteRow is one job site: its sign-in state and last check, what the check
// just pressed found, and the button that opens its sign-in page in a new tab
// of the apply window, shown in the browser panel to sign in there.
function SiteRow({ site, check }: { site: SessionSiteView; check?: SignInCheck }) {
  const open = useAction();
  const go = async () => {
    const url = site.signInUrl;
    const got = url && (await open.run(() => post("/api/browser/lanes/apply/tabs", browserNewTab, { url })));
    if (got) showInPanel(got.tabId);
  };
  return (
    <li className="row hold">
      <span>
        <strong>{site.label}</strong>{" "}
        <Chip tone={site.signIn === "signed_in" ? "good" : site.signIn === "unknown" ? "neutral" : "warn"}>{stateWord(site.signIn)}</Chip>{" "}
        <span className="muted">{site.signInCheckedAt ? `checked ${when(site.signInCheckedAt)}` : "never checked"}</span>
        {check && <span className="sub muted">This check: {nextOf(check)}</span>}
        {open.error && <ErrorNote error={open.error} />}
      </span>
      <button type="button" className={site.signIn === "signed_in" || site.signIn === "unknown" ? undefined : "primary"} disabled={open.busy} aria-label={`Open ${site.label} sign-in`} onClick={() => void go()}>
        Open sign-in
      </button>
    </li>
  );
}

// SignIns opens a job site's sign-in page in the browser panel and shows what
// the last check read there. A check only loads and reads a page; it never
// signs in or answers a challenge.
export function SignIns({ sessions, onChecked }: { sessions: Resource<SessionsView>; onChecked: () => void }) {
  const [checks, setChecks] = useState<SignInCheck[] | null>(null);
  const check = useAction();
  const press = async () => {
    const done = await check.run(() => post("/api/browser/sessions/check", signInChecks));
    if (done) {
      setChecks(done.checks);
      onChecked();
    }
  };
  // The sites the check reads: the job boards the scanners read through the browser.
  const sites = (sessions.data?.sites ?? []).filter((s) => s.signInUrl);
  const signedIn = checks?.filter((c) => !c.error && c.state === "signed_in").length ?? 0;
  const needYou = checks?.filter((c) => !c.error && (c.next === "needs-user" || c.next === "blocked-by-safety")).length ?? 0;
  const unread = checks?.filter((c) => c.error).length ?? 0;
  return (
    <section aria-labelledby="browser-signins" className="panel">
      <header className="panel-head">
        <h2 id="browser-signins">Sign-ins</h2>
      </header>
      <p className="muted">
        Loads each job site's account page in the scan window and reports whether the browser is signed in. It only loads and reads the page. A site
        found signed in or rate-limited in the last five minutes is answered from that result; one you just signed in to is read again.
      </p>
      {sessions.status === "loading" && <Loading label="Loading the sign-ins" lines={2} />}
      {sessions.status === "error" && <ErrorNote error={sessions.error} onRetry={onChecked} what="the sign-ins" />}
      {sites.length > 0 && (
        <ul className="plain stack">
          {sites.map((s) => (
            <SiteRow key={s.domain} site={s} check={checks?.find((c) => c.domain === s.domain)} />
          ))}
        </ul>
      )}
      <div className="actions">
        <button type="button" disabled={check.busy} onClick={() => void press()}>
          {check.busy ? "Checking sign-ins" : "Check sign-ins"}
        </button>
      </div>
      {check.error && (
        <div role="alert" className="note error">
          <p className="sentence">{check.error.message}</p>
          {check.error.requestId && <p className="muted">Request {check.error.requestId}</p>}
        </div>
      )}
      {checks && (
        <div aria-live="polite">
          <p>
            Checked {checks.length === 1 ? "1 site" : `${checks.length} sites`}: {signedIn} signed in, {needYou} need you
            {unread > 0 ? `, ${unread} could not be read` : ""}.
          </p>
          <ul className="plain">
            {checks
              .filter((c) => !sites.some((s) => s.domain === c.domain))
              .map((c) => (
                <li key={c.domain}>
                  {c.label}: {nextOf(c)}
                </li>
              ))}
          </ul>
        </div>
      )}
    </section>
  );
}
