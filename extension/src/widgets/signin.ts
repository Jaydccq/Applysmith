// The sign-in button. It is inert: nothing runs on load, on navigation or on a
// timer. The person's click on it is the authorization for this page. On click
// it asks the application for the credential the vault allows for this page,
// types it with the engine's writers, presses only a control whose label is a
// sign-in or account-creation word, and shows what the site did.
import { consentRefusal } from "../../../engine/src/consent";
import { clickLabel } from "../../../engine/src/click-guard";
import { resolveLabel } from "../../../engine/src/labels";
import { isRendered } from "../../../engine/src/visibility";
import { h } from "../shared/dom";
import type { Failure } from "../shared/messages";
import { createLayer, type Layer } from "./layer";
import type { PageChanges } from "./main";
import { writeControl } from "./write";

export type SignInOutcome =
  | "signed-in"
  | "account-created"
  | "signin-rejected"
  | "consent-required"
  | "filled-no-control"
  | "not-found"
  | "vault-unavailable"
  | "vault-denied"
  | "signin-held"
  | "failed";

/** What the widget says for an outcome that carries no detail of its own. */
const SAID: Partial<Record<SignInOutcome, string>> = {
  "signed-in": "Signed in.",
  "account-created": "Account created.",
  "not-found": "No password is saved for this site. Add one in the dashboard under Settings, Sign-in credentials. Nothing was typed.",
  "vault-unavailable": "The application could not read the password store. Nothing was typed.",
  "vault-denied": "The password store refused access. Nothing was typed.",
};

export const VERDICT_MS = 8000;
export const VERDICT_POLL_MS = 200;

const CSS = `.btn { position: fixed; z-index: 2147483644; } .status { position: fixed; z-index: 2147483644; max-width: 280px; margin: 0; padding: 4px 8px; background: var(--bg); border: 1px solid var(--line); border-radius: 6px; }`;

const SIGNIN_LABEL = /^(sign ?in|log ?in|login|continue|next)\b/i;
const REGISTER_LABEL = /^(create( an| your| my)? account|register|sign ?up)\b/i;
const REFUSED = /\b(submit|apply|withdraw|delete|discard|remove|cancel|forgot|reset)\b|\b(google|apple|facebook|linkedin|microsoft|github|sso|passkey|link|sms)\b/i;
const FAILURE_TEXT = /incorrect|invalid|wrong|failed|not (recogni[sz]ed|found|match)|doesn['’]?t match|try again|unable to|error|locked|denied|expired/i;
const ALERT = "[role=alert], [aria-live=assertive], .error, .errors, .alert-danger, [class*=error], [class*=Error]";
const CONTROL = "button, input[type=submit], input[type=button], [role=button]";

export interface SignInForm {
  kind: "signin" | "register";
  root: Element;
  identifier: HTMLInputElement | null;
  password: HTMLInputElement;
  confirm: HTMLInputElement | null;
}

const visibleInput = (el: HTMLInputElement) => !el.disabled && !el.readOnly && isRendered(el) && !el.closest("[data-applysmith]");

/** The sign-in or registration form on the page, when there is one. */
function findForm(doc: Document): SignInForm | null {
  const passwords = [...doc.querySelectorAll<HTMLInputElement>("input[type=password]")].filter(visibleInput);
  const first = passwords[0];
  if (!first) return null;
  const root = first.closest("form") ?? first.parentElement?.parentElement ?? doc.body;
  const inRoot = passwords.filter((p) => root.contains(p));
  const fields = [...root.querySelectorAll<HTMLInputElement>("input")].filter((i) => ["text", "email", "tel", ""].includes((i.getAttribute("type") ?? "").toLowerCase()) && visibleInput(i));
  const likely = (i: HTMLInputElement) => /user|email|login|account|identifier|id\b/i.test(`${i.name} ${i.id} ${i.autocomplete} ${i.placeholder}`);
  const identifier = fields.find(likely) ?? fields[0] ?? null;
  const confirm = inRoot[1] ?? null;
  return { kind: confirm ? "register" : "signin", root, identifier, password: first, confirm };
}

/** Whether pressing this control is something a sign-in may do. */
export function signInLabelAllowed(label: string, register: boolean): boolean {
  if (REFUSED.test(label)) return false;
  return SIGNIN_LABEL.test(label) || (register && REGISTER_LABEL.test(label));
}

function pressable(form: SignInForm): HTMLElement[] {
  let node: Element | null = form.root;
  for (let i = 0; node && i < 5; i++, node = node.parentElement) {
    const found = [...node.querySelectorAll<HTMLElement>(CONTROL)].filter((c) => !(c as HTMLButtonElement).disabled && isRendered(c) && !c.closest("[data-applysmith]"));
    if (found.length) return found;
  }
  return [];
}

function failureAlerts(doc: Document): Set<string> {
  const out = new Set<string>();
  for (const el of doc.querySelectorAll(ALERT)) {
    const text = (el.textContent ?? "").trim();
    if (text && isRendered(el) && FAILURE_TEXT.test(text)) out.add(text);
  }
  return out;
}

function agreementBoxes(root: Element): { el: HTMLInputElement; text: string; required: boolean }[] {
  return [...root.querySelectorAll<HTMLInputElement>("input[type=checkbox]")]
    .filter((b) => isRendered(b))
    .map((el) => ({ el, text: resolveLabel(el).text, required: el.required || el.getAttribute("aria-required") === "true" }));
}

export interface Credential {
  email: string;
  password: string;
}

export interface SignInDeps {
  doc: Document;
  win: Window;
  /** The widgets' shared page observer. */
  changes: PageChanges;
  /** Asks the application for this page's credential; rejects with the worker's failure. */
  resolve(): Promise<Credential>;
  settings(): Promise<{ acceptLegalDeclarations: boolean }>;
  /** Called when the person's click starts a run, before anything else happens. */
  onStart?(): void;
  /** Called exactly once for each run the person starts, when given; the page widgets give none, as no route receives it. */
  onOutcome?(o: SignInOutcome, detail?: string): void;
  verdictMs?: number;
  pollMs?: number;
}

export interface SignIn {
  refresh(): void;
  destroy(): void;
}

export function mapFailure(f: Partial<Failure> | undefined): SignInOutcome {
  if (f?.reason === "signin_held") return "signin-held";
  if (f?.reason === "secret_not_found" || (f?.code === "not_found" && !f.reason)) return "not-found";
  if (f?.reason === "keychain_denied") return "vault-denied";
  return "vault-unavailable";
}

export function startSignIn(deps: SignInDeps): SignIn {
  const { doc, win } = deps;
  /** The setting fails closed: anything but an explicit true, a failed read included, leaves every agreement box alone. */
  const legalAccepted = async (): Promise<boolean> => {
    try {
      return (await deps.settings()).acceptLegalDeclarations === true;
    } catch {
      return false;
    }
  };
  let layer: Layer | undefined;
  let form: SignInForm | null = null;
  let button: HTMLButtonElement | undefined;
  let status: HTMLElement | undefined;
  let running = false;

  const say = (text: string, tone = "") => {
    if (!status) return;
    status.textContent = text;
    status.className = `status ${tone}`;
    status.hidden = text === "";
  };

  function place(): void {
    if (!form || !button || !status) return;
    const r = form.password.getBoundingClientRect();
    button.style.left = `${Math.max(0, Math.round(r.left))}px`;
    button.style.top = `${Math.round(r.bottom + 6)}px`;
    status.style.left = `${Math.max(0, Math.round(r.left))}px`;
    status.style.top = `${Math.round(r.bottom + 40)}px`;
  }

  function refresh(): void {
    if (running) return;
    form = findForm(doc);
    if (!form) {
      layer?.destroy();
      layer = button = status = undefined;
      return;
    }
    if (layer && !layer.alive()) layer = undefined;
    if (!layer) {
      layer = createLayer(doc, CSS);
      button = h("button", { class: "btn", type: "button" });
      status = h("p", { class: "status", role: "status", hidden: true });
      button.addEventListener("click", () => void run());
      layer.box.append(button, status);
    }
    button!.textContent = form.kind === "register" ? "Create account with Applysmith" : "Sign in with Applysmith";
    place();
  }

  async function run(): Promise<void> {
    if (running || !form) return;
    running = true;
    button!.disabled = true;
    deps.onStart?.();
    const f = form;
    let reported = false;
    const finish = (o: SignInOutcome, detail?: string) => {
      if (reported) return;
      reported = true;
      running = false;
      button!.disabled = false;
      say(detail ?? SAID[o] ?? o.replaceAll("-", " "), o === "signed-in" || o === "account-created" ? "ok" : "bad");
      deps.onOutcome?.(o, detail);
    };
    try {
      say("Working.");
      const register = f.kind === "register";
      const ticked: string[] = [];
      let boxes: { el: HTMLInputElement; text: string; required: boolean }[] = [];
      if (register) {
        boxes = agreementBoxes(f.root);
        const accept = await legalAccepted();
        const gating = boxes.filter((b) => b.required && consentRefusal({ text: b.text, markedRequired: true, optional: false }, true) === null);
        if (gating.length > 0 && !accept) return finish("consent-required", "The form needs an agreement box ticked and your setting for legal declarations (Settings, Runtime settings) is off. Nothing was typed or ticked.");
      }
      let cred: Credential | undefined;
      try {
        cred = await deps.resolve();
      } catch (e) {
        const failure = (e as { failure?: Failure }).failure ?? (e as Partial<Failure>);
        const outcome = mapFailure(failure);
        return finish(outcome, outcome === "signin-held" ? `${failure?.message ?? "Signing in here is on hold for today."} Nothing was typed.` : undefined);
      }
      let wrote = (!f.identifier || (await writeControl(f.identifier, cred.email)).ok) && (await writeControl(f.password, cred.password)).ok;
      if (register && f.confirm) wrote = wrote && (await writeControl(f.confirm, cred.password)).ok;
      cred = undefined;
      if (!wrote) return finish("failed", "The page did not keep the typed values.");
      if (register) {
        const accept = await legalAccepted();
        for (const b of boxes) {
          if (b.el.checked || consentRefusal({ text: b.text, markedRequired: b.required, optional: false }, accept) !== null || !b.required) continue;
          b.el.click();
          ticked.push(b.text);
        }
      }
      const target = pressable(f).find((c) => signInLabelAllowed(clickLabel(c), register));
      if (!target) return finish("filled-no-control", "Typed, but no sign-in control is safe to press. Press it yourself.");
      const before = failureAlerts(doc);
      target.click();
      say(ticked.length ? `Ticked: ${ticked.join("; ")}. Waiting for the site.` : "Waiting for the site.");
      const verdict = await watch(f, before);
      if (verdict === "rejected") return finish("signin-rejected", "The site refused the sign-in.");
      if (verdict === "gone") return finish(register ? "account-created" : "signed-in", ticked.length ? `Ticked: ${ticked.join("; ")}.` : undefined);
      finish("failed", "The site did not answer in time.");
    } catch {
      finish("failed", "The sign-in did not finish.");
    }
  }

  function watch(f: SignInForm, before: Set<string>): Promise<"rejected" | "gone" | "timeout"> {
    const limit = deps.verdictMs ?? VERDICT_MS;
    const every = deps.pollMs ?? VERDICT_POLL_MS;
    return new Promise((resolve) => {
      let waited = 0;
      const tick = () => {
        for (const text of failureAlerts(doc)) if (!before.has(text)) return resolve("rejected");
        const stillAsking = doc.contains(f.password) && isRendered(f.password);
        if (!stillAsking && findForm(doc) === null) return resolve("gone");
        waited += every;
        if (waited >= limit) return resolve("timeout");
        setTimeout(tick, every);
      };
      setTimeout(tick, every);
    });
  }

  const stopWatching = deps.changes.on(refresh, 500);
  win.addEventListener("resize", place);
  win.addEventListener("scroll", place, true);
  refresh();

  return {
    refresh,
    destroy() {
      stopWatching();
      win.removeEventListener("resize", place);
      win.removeEventListener("scroll", place, true);
      layer?.destroy();
    },
  };
}
