import { useEffect, useId, useState, type FormEvent } from "react";
import { ApiError, del, get, patch, post, put } from "../../api/client";
import { answerBankSettings } from "../../api/decoders_bank";
import { configDocument } from "../../api/decoders_pages";
import { alternateAdded, secretList, secretStatus, secretTest } from "../../api/decoders_secrets";
import { useResource } from "../../hooks";
import { useSearchParams } from "../../router";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote, Loading, Panel } from "../../ui/parts";
import type { SecretEntry, SecretTest } from "../../wire";
import { useAction, when } from "../action";
import { usePasswordField } from "./passwordField";
import { savePolicy, type PolicyOp } from "./Policy";

const SERVER_RULES = ["at least 12 characters", "an uppercase letter", "a lowercase letter", "a number", "a symbol"] as const;

// rulesMet mirrors the server's five rules for the shared default. The server
// checks again; this only tells the user what is still missing while typing.
export function rulesMet(pw: string): boolean[] {
  return [
    [...pw].length >= 12,
    /\p{Lu}/u.test(pw),
    /\p{Ll}/u.test(pw),
    /\p{Nd}/u.test(pw),
    /[^\p{L}\p{Nd}\s]/u.test(pw),
  ];
}

// siteKey is the lowercase host name an override is stored under: the host of
// an http or https address, or a bare host name. Never the path, never the
// registrable domain. null when the text is neither.
export function siteKey(text: string): string | null {
  const t = text.trim();
  if (!t) return null;
  try {
    const u = new URL(t.includes("://") ? t : `https://${t}`);
    if ((u.protocol !== "http:" && u.protocol !== "https:") || u.username || u.password || !u.hostname) return null;
    return u.hostname.toLowerCase();
  } catch {
    return null;
  }
}

const LOWER = "abcdefghijkmnopqrstuvwxyz";
const UPPER = "ABCDEFGHJKLMNPQRSTUVWXYZ";
const DIGITS = "23456789";
const SYMBOLS = "!#$%&*+-=?@^_";

function randomIndex(n: number): number {
  const limit = Math.floor(2 ** 32 / n) * n;
  const buf = new Uint32Array(1);
  for (;;) {
    crypto.getRandomValues(buf);
    if (buf[0] < limit) return buf[0] % n;
  }
}

// generatePassword draws 24 characters from the system random source with at
// least one of each class and never the same character three times in a row.
export function generatePassword(): string {
  const all = LOWER + UPPER + DIGITS + SYMBOLS;
  for (;;) {
    const chars = Array.from({ length: 24 }, (_, i) => {
      const set = i < 4 ? [LOWER, UPPER, DIGITS, SYMBOLS][i] : all;
      return set[randomIndex(set.length)];
    });
    for (let i = chars.length - 1; i > 0; i--) {
      const j = randomIndex(i + 1);
      [chars[i], chars[j]] = [chars[j], chars[i]];
    }
    if (!chars.some((c, i) => i >= 2 && c === chars[i - 1] && c === chars[i - 2])) return chars.join("");
  }
}

function trouble(entries: SecretEntry[]): string | null {
  const codes = new Set(entries.filter((e) => e.problem).map((e) => e.problem));
  if (codes.has("keychain_denied")) return "The operating system refused access to the keychain. Unlock it or allow access when asked, then reload this page. The stored passwords are not lost.";
  if (codes.has("keychain_unavailable")) return "No keychain is available on this computer. Set the passwords through APPLYSMITH_SECRET_<NAME> environment variables and restart the server; the setup guide lists the names.";
  return null;
}

function DefaultForm({ entry, onChanged }: { entry?: SecretEntry; onChanged: () => void }) {
  const emailId = useId();
  const pwId = useId();
  const [email, setEmail] = useState(entry?.email ?? "");
  const [shown, setShown] = useState(false);
  const pw = usePasswordField();
  const act = useAction();
  const met = rulesMet(pw.text);
  const all = met.every(Boolean);

  const saveEmail = async (e: FormEvent) => {
    e.preventDefault();
    if (await act.run(() => put("/api/secrets/signin-default", secretStatus, { value: "", email: email.trim() }))) onChanged();
  };
  const savePassword = async (e: FormEvent) => {
    e.preventDefault();
    const value = pw.take();
    setShown(false);
    if (value && (await act.run(() => put("/api/secrets/signin-default", secretStatus, { value })))) onChanged();
  };
  return (
    <div>
      <form onSubmit={saveEmail} className="key-row">
        <label htmlFor={emailId}>
          Registration email <Chip tone={entry?.email ? "good" : "warn"}>{entry?.email ? "saved" : "not set"}</Chip>
        </label>
        <input id={emailId} type="email" autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
        <button type="submit" disabled={act.busy || email.trim() === "" || email.trim() === (entry?.email ?? "")}>
          Save email
        </button>
      </form>
      <form onSubmit={savePassword} className="key-row">
        <label htmlFor={pwId}>
          Shared default password <Chip tone={entry?.present ? "good" : "warn"}>{entry?.present ? "saved" : "not set"}</Chip>
        </label>
        <input id={pwId} ref={pw.ref} type={shown ? "text" : "password"} autoComplete="new-password" spellCheck={false} onChange={(e) => pw.onInput(e.target.value)} placeholder={entry?.present ? "Enter a new password to replace it" : "Choose a password"} />
        <ul aria-label="Password rules" className="rules">
          {SERVER_RULES.map((rule, i) => (
            <li key={rule}>
              <Chip tone={met[i] ? "good" : "neutral"}>{met[i] ? "met" : "needed"}</Chip> {rule}
            </li>
          ))}
        </ul>
        <div className="actions">
          <button type="button" disabled={act.busy} onClick={() => { pw.set(generatePassword()); setShown(true); }}>
            Generate a password
          </button>
          <button type="submit" disabled={!all || act.busy}>
            Save password
          </button>
        </div>
        {shown && <p className="muted">This is the only time the password is shown. Copy it if you need it elsewhere.</p>}
      </form>
      {act.error && <ErrorNote error={act.error} />}
      <p className="muted">A stored password is never displayed again. Setting a new one replaces it.</p>
    </div>
  );
}

// SiteForm starts on the host a run's fix names (`?site=`): a stop for a
// refused password links here.
function SiteForm({ hasEmail, onChanged }: { hasEmail: boolean; onChanged: () => void }) {
  const siteId = useId();
  const pwId = useId();
  const named = useSearchParams()[0].get("site") ?? "";
  const [site, setSite] = useState(named);
  useEffect(() => {
    if (named) setSite(named);
  }, [named]);
  const pw = usePasswordField();
  const act = useAction();
  const key = siteKey(site);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const value = pw.take();
    if (key && value && (await act.run(() => put(`/api/secrets/signin-site-${encodeURIComponent(key)}`, secretStatus, { value })))) {
      setSite("");
      onChanged();
    }
  };
  return (
    <form onSubmit={save} className="key-row">
      <label htmlFor={siteId}>Site (paste a job address or a host name)</label>
      <input id={siteId} type="text" autoComplete="off" spellCheck={false} value={site} onChange={(e) => setSite(e.target.value)} />
      <p className="muted" aria-live="polite">
        {site.trim() === "" ? "" : key ? `Stored for ${key}` : "That is not an http or https address."}
      </p>
      <label htmlFor={pwId}>Password for this site (any value the site accepts)</label>
      <input id={pwId} ref={pw.ref} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => pw.onInput(e.target.value)} />
      <button type="submit" disabled={!key || pw.text === "" || !hasEmail || act.busy}>
        Save override
      </button>
      {!hasEmail && <p className="muted">Save the registration email first.</p>}
      {act.error && <ErrorNote error={act.error} />}
    </form>
  );
}

function AlternateForm({ hasEmail, onChanged }: { hasEmail: boolean; onChanged: () => void }) {
  const labelId = useId();
  const pwId = useId();
  const [label, setLabel] = useState("");
  const pw = usePasswordField();
  const act = useAction();
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const value = pw.take();
    if (value && (await act.run(() => post("/api/secrets/alternates", alternateAdded, { value, label: label.trim() })))) {
      setLabel("");
      onChanged();
    }
  };
  return (
    <form onSubmit={save} className="key-row">
      <label htmlFor={labelId}>Label (at most 40 characters)</label>
      <input id={labelId} type="text" maxLength={40} autoComplete="off" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Alternate 1" />
      <label htmlFor={pwId}>Alternate password</label>
      <input id={pwId} ref={pw.ref} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => pw.onInput(e.target.value)} />
      <button type="submit" disabled={pw.text === "" || !hasEmail || act.busy}>
        Add alternate
      </button>
      {act.error && <ErrorNote error={act.error} />}
    </form>
  );
}

// The secret the apply chain's captcha solver reads (internal/vault registry).
const CAPTCHA_KEY = "twocaptcha-key";

// The policy.yml switch the solver reads at the start of a run (internal/apply
// SolverFactory): on only when the file validates and holds on. The policy
// read fills an absent key with its default, on; a file that is missing or has
// errors has no values and reads off (gates.CaptchaSolve).
const SOLVER = "captcha_solver";

// SolverSwitch writes the switch on the owner's press through the policy
// editor's save. The press sets one key, so after a revision conflict it is
// sent again at the revision the file has now. It stays enabled while saving,
// so focus is not dropped; useAction ignores a second press meanwhile.
function SolverSwitch() {
  const [res, reload] = useResource(() => get("/api/config/policy", configDocument), [], ["configuration"], { keep: "settings.captcha-policy" });
  const act = useAction();
  const id = useId();
  if (res.status === "loading") return <Loading label="Loading the solver switch" lines={1} />;
  if (!res.data) return <ErrorNote error={res.error} onRetry={reload} what="the solver switch" />;
  const doc = res.data;
  const readable = doc.exists && doc.issues.length === 0;
  const turn = async (on: boolean) => {
    const ops: PolicyOp[] = [{ op: "set", path: SOLVER, value: on ? "on" : "off" }];
    const done = await act.run(async () => {
      try {
        await savePolicy(ops, doc.revision);
      } catch (e) {
        if (!(e instanceof ApiError) || e.kind !== "revision_conflict") throw e;
        await savePolicy(ops);
      }
      return true;
    });
    if (done) reload();
  };
  return (
    <div>
      <label htmlFor={id}>
        <input id={id} type="checkbox" checked={doc.values?.[SOLVER] === "on"} disabled={!readable} aria-describedby={`${id}-note`} onChange={(e) => void turn(e.target.checked)} /> Use the solver
      </label>
      <p id={`${id}-note`} className="muted">
        {readable ? "On by default; a change applies from the next apply run. Off leaves every captcha to you and keeps the key stored." : "Off: policy.yml is missing or has errors, so the solver stays off until the file is fixed in Settings, Files."}
      </p>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// testVerdict says what the key test found: the balance read is the test.
function testVerdict(t: SecretTest): string {
  switch (t.live) {
    case "ok": {
      const works = `Key works, balance $${((t.balanceCents ?? 0) / 100).toFixed(2)}.`;
      return t.belowFloor ? `${works} That is under the solver's USD 0.50 floor, so it leaves every captcha to you until the account is topped up.` : works;
    }
    case "rejected":
      return "2Captcha does not know this key. Copy it again from your 2Captcha account and save it.";
    case "not_found":
      return "No key is saved.";
  }
  return `2Captcha did not answer the test (${t.detail ?? t.live}). Try again later.`;
}

// CAPTCHA_TERMS is this section's entry in the Settings search index; it
// lives here, beside the words it repeats, as the only Settings file that may
// name the provider.
export const CAPTCHA_TERMS = ["2Captcha API key", "Clears supported challenges", "at most 2 challenges per run", "batch skip list"];

// CaptchaSection stores the 2Captcha key, write-only like a password, so the
// setup step can send an app user here instead of to a terminal, where the
// command refuses while the app holds the workspace.
export function CaptchaSection() {
  const [res, reload] = useResource(() => get("/api/secrets", secretList), []);
  const id = useId();
  const key = usePasswordField();
  const act = useAction();
  const [removing, setRemoving] = useState(false);
  const [verdict, setVerdict] = useState<SecretTest | undefined>();
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const value = key.take();
    setVerdict(undefined);
    if (value && (await act.run(() => put(`/api/secrets/${CAPTCHA_KEY}`, secretStatus, { value })))) reload();
  };
  const remove = async () => {
    setRemoving(false);
    setVerdict(undefined);
    if (await act.run(() => del(`/api/secrets/${CAPTCHA_KEY}`, secretStatus))) reload();
  };
  // One balance read, on the user's press only.
  const test = async () => setVerdict(await act.run(() => post(`/api/secrets/${CAPTCHA_KEY}/test`, secretTest)));
  return (
    <Panel title="Captcha solver" id="settings-captcha" res={res} retry={reload} what="the captcha solver key">
      {(data) => {
        const saved = data.entries.some((e) => e.name === CAPTCHA_KEY && e.present);
        return (
          <>
            <p>
              Clears supported challenges through your own 2Captcha account. The solver runs only inside an explicit apply run that submits, solves at most 2 challenges per run, stops under USD 0.50 of balance and never runs for an employer on the batch skip list.
            </p>
            <SolverSwitch />
            <form onSubmit={save} className="key-row">
              <label htmlFor={id}>
                2Captcha API key <Chip tone={saved ? "good" : "warn"}>{saved ? "saved" : "not set"}</Chip>
              </label>
              <input id={id} ref={key.ref} type="password" autoComplete="new-password" spellCheck={false} onChange={(e) => key.onInput(e.target.value)} placeholder={saved ? "Enter a new key to replace it" : "Paste the key"} />
              <div className="actions">
                <button type="submit" disabled={key.text === "" || act.busy}>
                  Save key
                </button>
                {saved && (
                  <>
                    <button type="button" disabled={act.busy} onClick={() => void test()}>
                      Test key
                    </button>
                    <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRemoving(true)}>
                      Remove key
                    </button>
                  </>
                )}
              </div>
            </form>
            {verdict && <p role="status">{testVerdict(verdict)}</p>}
            {act.error && <ErrorNote error={act.error} />}
            {removing && <ConfirmDialog title="Remove the 2Captcha key" consequence="The key is deleted from this computer and every captcha is left to you until a new key is saved." verb="Remove key" onCancel={() => setRemoving(false)} onConfirm={() => void remove()} />}
          </>
        );
      }}
    </Panel>
  );
}

// GoogleSignIn is the switch that lets a run press a site's Sign in with
// Google: setup step 13 counts it as a way to sign in, beside the shared default.
function GoogleSignIn() {
  const [res, reload] = useResource(() => get("/api/answer-bank/settings", answerBankSettings), [], ["answerbank"]);
  const act = useAction();
  const id = useId();
  if (res.status !== "ready") return null;
  const write = async (on: boolean) => {
    if (await act.run(() => patch("/api/answer-bank/settings", answerBankSettings, { allowGoogleSignIn: on }))) reload();
  };
  return (
    <div>
      <label htmlFor={id}>
        <input id={id} type="checkbox" checked={res.data.allowGoogleSignIn} disabled={act.busy} onChange={(e) => void write(e.target.checked)} /> Automatically sign in, authorize and register with Google
      </label>
      <p className="muted">Off by default. Sign in to Google once in the apply window (the Browser page opens it); a run then presses a site's Sign in with Google, uses that session, selects your profile email, and approves verified basic name, email and profile access. It can also register a site account, subject to your account creation limits. Extra permissions, a new Google login and verification still need you. Google cookies are never copied from another browser.</p>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

export function SignInSection() {
  const [res, reload] = useResource(() => get("/api/secrets", secretList), [], [], { keep: "settings.secrets" });
  const act = useAction();
  const [removing, setRemoving] = useState<SecretEntry | null>(null);

  const remove = async (entry: SecretEntry) => {
    setRemoving(null);
    const go = async (): Promise<true> => {
      if (entry.kind === "signin_alternate") await del(`/api/secrets/alternates/${encodeURIComponent(entry.name.replace("signin-alternate-", ""))}`, secretList);
      else await del(`/api/secrets/${encodeURIComponent(entry.name)}`, secretStatus);
      return true;
    };
    if (await act.run(go)) reload();
  };
  const move = async (alternates: SecretEntry[], from: number, to: number) => {
    const ids = alternates.map((a) => a.name.replace("signin-alternate-", ""));
    ids.splice(to, 0, ...ids.splice(from, 1));
    if (await act.run(() => put("/api/secrets/alternates/order", secretList, { ids }))) reload();
  };

  return (
    <Panel title="Sign-in credentials" id="settings-signin" res={res} retry={reload} what="the sign-in credentials">
      {(data) => {
        const signin = data.entries.filter((e) => e.kind.startsWith("signin_"));
        const def = signin.find((e) => e.kind === "signin_default");
        const sites = signin.filter((e) => e.kind === "signin_site" && e.present);
        const alternates = signin.filter((e) => e.kind === "signin_alternate").sort((a, b) => (a.position ?? 0) - (b.position ?? 0));
        const problem = trouble(signin);
        return (
          <>
            <p>Passwords for application sites. Only presence is shown here; a stored password is never displayed.</p>
            {problem && <p role="alert" className="note">{problem}</p>}
            <h3>Sign in with Google</h3>
            <GoogleSignIn />
            <h3>Shared default</h3>
            <DefaultForm entry={def} onChanged={reload} />
            <h3>Site overrides</h3>
            {sites.length === 0 ? (
              <p className="muted">No site has its own password.</p>
            ) : (
              <ul>
                {sites.map((s) => (
                  <li key={s.name} className="row">
                    <span>
                      <strong>{s.host}</strong> <Chip tone="good">saved</Chip>
                      {s.lastSigninAt && <span className="muted"> signed in {when(s.lastSigninAt)}</span>}
                    </span>
                    <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRemoving(s)}>
                      Remove
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <SiteForm hasEmail={!!def?.email} onChanged={reload} />
            <h3>Alternate passwords</h3>
            <p className="muted">Tried in this order after the site's own password and the shared default.</p>
            {alternates.length === 0 ? (
              <p className="muted">No alternate passwords.</p>
            ) : (
              <ol>
                {alternates.map((a, i) => (
                  <li key={a.name} className="row">
                    <span>
                      <strong>{a.label}</strong> <Chip tone={a.present ? "good" : "warn"}>{a.present ? "saved" : "missing"}</Chip>
                    </span>
                    <span className="actions">
                      <button type="button" aria-label={`Move ${a.label} up`} disabled={act.busy || i === 0} onClick={() => void move(alternates, i, i - 1)}>
                        Up
                      </button>
                      <button type="button" aria-label={`Move ${a.label} down`} disabled={act.busy || i === alternates.length - 1} onClick={() => void move(alternates, i, i + 1)}>
                        Down
                      </button>
                      <button type="button" className="danger-outline" aria-label={`Remove ${a.label}`} disabled={act.busy} onClick={() => setRemoving(a)}>
                        Remove
                      </button>
                    </span>
                  </li>
                ))}
              </ol>
            )}
            <AlternateForm hasEmail={!!def?.email} onChanged={reload} />
            {act.error && <ErrorNote error={act.error} />}
            {removing && (
              <ConfirmDialog
                title={`Remove ${removing.kind === "signin_site" ? removing.host : removing.label}`}
                consequence="The stored password is deleted. A site that was already signed in keeps its history."
                verb="Remove"
                onCancel={() => setRemoving(null)}
                onConfirm={() => void remove(removing)}
              />
            )}
          </>
        );
      }}
    </Panel>
  );
}
