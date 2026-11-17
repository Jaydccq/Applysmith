import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mapFailure, signInLabelAllowed, startSignIn, type SignIn, type SignInDeps, type SignInOutcome } from "../src/widgets/signin";
import { watchPage } from "../src/widgets/main";

const PASSWORD = "Example-Pass-123";
const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
let widget: SignIn | undefined;
let outcomes: SignInOutcome[];
let resolve: ReturnType<typeof vi.fn>;
let settings: ReturnType<typeof vi.fn>;
let onClickSite: (btn: HTMLElement) => void;

const button = () => roots.filter((r) => r.host.isConnected).map((r) => r.querySelector<HTMLButtonElement>(".btn")).find(Boolean)!;
const pw = () => document.getElementById("pw") as HTMLInputElement;
const email = () => document.getElementById("email") as HTMLInputElement;

const SIGNIN = `<form id="f"><input id="email" type="email" name="email"><input id="pw" type="password" name="password"><button id="go" type="submit">Sign in</button></form>`;

function mount(html: string, over: Partial<SignInDeps> = {}) {
  document.body.innerHTML = html;
  document.getElementById("go")?.addEventListener("click", (e) => {
    e.preventDefault();
    onClickSite(e.currentTarget as HTMLElement);
  });
  widget = startSignIn({
    doc: document,
    changes: watchPage(document),
    win: window,
    resolve: resolve as SignInDeps["resolve"],
    settings: settings as SignInDeps["settings"],
    onOutcome: (o) => outcomes.push(o),
    verdictMs: 1000,
    pollMs: 100,
    ...over,
  });
}

async function press() {
  button().click();
  await vi.advanceTimersByTimeAsync(4000);
}

beforeEach(() => {
  vi.useFakeTimers();
  roots.length = 0;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  outcomes = [];
  resolve = vi.fn(async () => ({ email: "alex.example@example.com", password: PASSWORD, source: "settings-default" }));
  settings = vi.fn(async () => ({ acceptLegalDeclarations: false }));
  onClickSite = (btn) => btn.closest("form")!.remove();
});
afterEach(() => {
  widget?.destroy();
  widget = undefined;
  Element.prototype.attachShadow = realAttach;
  vi.useRealTimers();
});

describe("sign-in button", () => {
  it("is inert until clicked: nothing is requested, typed or pressed on load", async () => {
    mount(SIGNIN);
    await vi.advanceTimersByTimeAsync(5000);
    expect(button().textContent).toBe("Sign in with Applysmith");
    expect(resolve).not.toHaveBeenCalled();
    expect(email().value).toBe("");
    expect(outcomes).toEqual([]);
  });

  it("types the credential, presses the sign-in control and reports signed-in once when the form goes away", async () => {
    mount(SIGNIN);
    await press();
    expect(outcomes).toEqual(["signed-in"]);
    expect(resolve).toHaveBeenCalledTimes(1);
  });

  it("reports signin-rejected for an error alert and never a success", async () => {
    onClickSite = () => {
      const a = document.createElement("div");
      a.setAttribute("role", "alert");
      a.textContent = "Incorrect password";
      document.body.append(a);
    };
    mount(SIGNIN);
    await press();
    expect(outcomes).toEqual(["signin-rejected"]);
    expect(resolve).toHaveBeenCalledTimes(1);
  });

  it("does not start a registration when the site refuses the sign-in and offers to create an account", async () => {
    onClickSite = () => {
      const a = document.createElement("div");
      a.setAttribute("role", "alert");
      a.textContent = "Account not found. Create account";
      document.body.append(a);
    };
    mount(SIGNIN + `<a href="/register" id="reg">Create account</a>`);
    await press();
    expect(outcomes).toEqual(["signin-rejected"]);
    expect(settings).not.toHaveBeenCalled();
  });

  it("refuses to press a submit-application control and reports filled-no-control", async () => {
    const pressed = vi.fn();
    mount(`<form><input id="email" type="email"><input id="pw" type="password"><button id="x" type="submit">Submit application</button></form>`);
    document.getElementById("x")!.addEventListener("click", pressed);
    await press();
    expect(outcomes).toEqual(["filled-no-control"]);
    expect(pressed).not.toHaveBeenCalled();
  });

  it("never presses a social sign-in control", async () => {
    const pressed = vi.fn();
    mount(`<form><input id="email" type="email"><input id="pw" type="password"><button id="g" type="button">Sign in with Google</button></form>`);
    document.getElementById("g")!.addEventListener("click", pressed);
    await press();
    expect(outcomes).toEqual(["filled-no-control"]);
    expect(pressed).not.toHaveBeenCalled();
  });

  it("tries the credential once and does not retry after a failure", async () => {
    onClickSite = () => {};
    mount(SIGNIN);
    await press();
    expect(outcomes).toEqual(["failed"]);
    expect(resolve).toHaveBeenCalledTimes(1);
  });

  it.each([
    [{ code: "not_found", reason: "secret_not_found" }, "not-found"],
    [{ code: "forbidden", reason: "keychain_denied" }, "vault-denied"],
    [{ code: "unavailable", reason: "keychain_unavailable" }, "vault-unavailable"],
    [{ code: "offline" }, "vault-unavailable"],
    [{ code: "unavailable", reason: "keychain_denied" }, "vault-denied"],
    [{ code: "conflict", reason: "signin_held" }, "signin-held"],
  ])("maps the application's answer %o to %s and types nothing", async (failure, outcome) => {
    resolve = vi.fn(async () => {
      throw { failure };
    });
    mount(SIGNIN);
    await press();
    expect(outcomes).toEqual([outcome]);
    expect(email().value).toBe("");
    expect(mapFailure(failure)).toBe(outcome);
    // A sentence that ends with what was done, never the bare outcome code.
    const status = roots.filter((r) => r.host.isConnected).map((r) => r.querySelector<HTMLElement>(".status")).find(Boolean)!;
    expect(status.textContent).toMatch(/Nothing was typed\.$/);
  });

  it("tells the person why a held site was not signed in to, and types nothing", async () => {
    const message = "Signing in here is on hold for today: the stored passwords for this site ran out earlier today.";
    resolve = vi.fn(async () => {
      throw { failure: { code: "conflict", message, reason: "signin_held" } };
    });
    mount(SIGNIN);
    await press();
    expect(outcomes).toEqual(["signin-held"]);
    const status = roots.filter((r) => r.host.isConnected).map((r) => r.querySelector<HTMLElement>(".status")).find(Boolean)!;
    expect(status.textContent).toBe(`${message} Nothing was typed.`);
    expect(email().value).toBe("");
    expect(pw().value).toBe("");
    expect(resolve).toHaveBeenCalledTimes(1);
  });

  it("holds the password only for the run: not in storage, the console, the document or a later run", async () => {
    const spy = vi.spyOn(console, "log");
    const err = vi.spyOn(console, "error");
    mount(SIGNIN);
    await press();
    expect(JSON.stringify([spy.mock.calls, err.mock.calls])).not.toContain(PASSWORD);
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain(PASSWORD);
    expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain(PASSWORD);
    expect(roots.map((r) => r.host.outerHTML + r.innerHTML).join("")).not.toContain(PASSWORD);
    expect(document.body.innerHTML).not.toContain(PASSWORD);
  });
});

describe("registration", () => {
  const REGISTER = (box: string) => `<form id="f"><input id="email" type="email"><input id="pw" type="password"><input id="pw2" type="password">${box}<button id="go" type="submit">Create account</button></form>`;
  const agree = `<label><input id="terms" type="checkbox" required> I agree to the terms and privacy policy</label>`;

  it("with the legal-declaration setting off, ticks nothing, types nothing and reports consent-required", async () => {
    mount(REGISTER(agree));
    await press();
    expect(outcomes).toEqual(["consent-required"]);
    expect((document.getElementById("terms") as HTMLInputElement).checked).toBe(false);
    expect(email().value).toBe("");
    expect(resolve).not.toHaveBeenCalled();
  });

  it("with the setting on, fills identifier, password and confirm, ticks the required box by name and reports account-created", async () => {
    settings = vi.fn(async () => ({ acceptLegalDeclarations: true }));
    mount(REGISTER(agree + `<label><input id="news" type="checkbox"> Send me job alerts and newsletters (optional)</label>`));
    let seen: { email: string; pw: string; pw2: string; terms: boolean; news: boolean } | undefined;
    onClickSite = (btn) => {
      seen = { email: email().value, pw: pw().value, pw2: (document.getElementById("pw2") as HTMLInputElement).value, terms: (document.getElementById("terms") as HTMLInputElement).checked, news: (document.getElementById("news") as HTMLInputElement).checked };
      btn.closest("form")!.remove();
    };
    button().click();
    await vi.advanceTimersByTimeAsync(4000);
    expect(seen).toEqual({ email: "alex.example@example.com", pw: PASSWORD, pw2: PASSWORD, terms: true, news: false });
    expect(outcomes).toEqual(["account-created"]);
    expect(document.querySelector("body")!.textContent).not.toContain(PASSWORD);
  });
});

describe("press guard", () => {
  it("allows sign-in words and, on registration, account-creation words, and refuses the rest", () => {
    for (const ok of ["Sign in", "Log in", "Continue", "Next"]) expect(signInLabelAllowed(ok, false)).toBe(true);
    expect(signInLabelAllowed("Create account", false)).toBe(false);
    expect(signInLabelAllowed("Create account", true)).toBe(true);
    for (const no of ["Submit application", "Apply now", "Withdraw application", "Delete account", "Sign in with Google", "Forgot password"]) expect(signInLabelAllowed(no, true)).toBe(false);
  });
});
