import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import type { ConfigDocument } from "../../wire";
import { CaptchaSection, SignInSection, generatePassword, rulesMet, siteKey } from "./SignIn";

const STRONG = ["Strong", "Canary", "9!x"].join("-");
const SITE_PW = ["site", "canary", "1"].join("-");

const registry = [{ kind: "signin_default", name: "signin-default", description: "Shared default", env: "APPLYSMITH_SECRET_<NAME>", required: false }];

const entry = (over: Record<string, unknown>) => ({ kind: "signin_default", present: true, ...over });

function server(entries: Record<string, unknown>[] = [entry({ name: "signin-default", email: "alex@example.com" })]) {
  let list = entries;
  let bank = { llmFallbackEnabled: false, groundingMode: "strict", autoAcceptLegalDeclarations: false, allowGoogleSignIn: false, watchScans: false, knowledgeSemanticRetrieval: false };
  const api = fakeApi({
    "GET /api/secrets": () => ok({ registry, entries: list }),
    "GET /api/answer-bank/settings": () => ok(bank),
    "PATCH /api/answer-bank/settings": (c) => ok((bank = { ...bank, ...(c.body as object) })),
    "PUT /api/secrets/signin-default": () => ok({ name: "signin-default", present: true, source: "keychain" }),
    "PUT /api/secrets/signin-site-careers.example.test": () => ok({ name: "signin-site-careers.example.test", present: true, source: "keychain" }),
    "DELETE /api/secrets/signin-site-careers.example.test": () => ok({ name: "signin-site-careers.example.test", present: false }),
    "POST /api/secrets/alternates": () => ok({ id: "0123456789ab", name: "signin-alternate-0123456789ab" }),
    "PUT /api/secrets/alternates/order": () => ok({ registry, entries: list }),
    "DELETE /api/secrets/alternates/0123456789ab": () => ok({ registry, entries: (list = list.filter((e) => e.name !== "signin-alternate-0123456789ab")) }),
  });
  return api;
}

const alternate = (id: string, label: string, position: number) => entry({ name: `signin-alternate-${id}`, kind: "signin_alternate", label, position });
const labelled = (name: string) => qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === name) ?? null;
const passwordField = (label: RegExp) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent && label.test(i.labels[0].textContent))!;

const box = (label: RegExp) => qa<HTMLInputElement>("input[type=checkbox]").find((i) => label.test(i.closest("label")?.textContent ?? ""))!;

describe("Settings: sign-in credentials", () => {
  it("keeps Google sign-in off until selected and writes both directions", async () => {
    const api = server();
    await mount(<SignInSection />);
    const google = /Automatically sign in, authorize and register with Google/;
    expect((await waitFor(() => box(google))).checked).toBe(false);
    await click(box(google));
    expect(api.callsTo("PATCH /api/answer-bank/settings")[0].body).toEqual({ allowGoogleSignIn: true });
    await waitFor(() => box(google).checked);
    await click(box(google));
    expect(api.callsTo("PATCH /api/answer-bank/settings")[1].body).toEqual({ allowGoogleSignIn: false });
    await waitFor(() => !box(google).checked);
    expect(text()).toContain("Sign in to Google once in the apply window");
  });

  it("shows presence only and never a stored value", async () => {
    server([
      entry({ name: "signin-default", email: "alex@example.com" }),
      entry({ name: "signin-site-careers.example.test", kind: "signin_site", host: "careers.example.test", lastSigninAt: "2026-10-01T09:00:00.000Z" }),
      alternate("0123456789ab", "Old laptop", 1),
    ]);
    await mount(<SignInSection />);
    await waitFor(() => text().includes("careers.example.test"));
    expect(text()).toContain("Old laptop");
    expect(text()).toContain("never displayed");
    for (const input of qa<HTMLInputElement>("input[type=password]")) expect(input.value).toBe("");
  });

  it("evaluates the five rules live and gates the save on all of them", async () => {
    server();
    await mount(<SignInSection />);
    const field = await waitFor(() => passwordField(/Shared default password/));
    expect(button("Save password")?.disabled).toBe(true);
    await type(field, "password1");
    expect(button("Save password")?.disabled).toBe(true);
    const needed = qa("ul[aria-label='Password rules'] li").filter((li) => li.textContent?.startsWith("needed")).map((li) => li.textContent);
    expect(needed.join(" ")).toContain("at least 12 characters");
    expect(needed.join(" ")).toContain("an uppercase letter");
    expect(needed.join(" ")).toContain("a symbol");
    await type(field, STRONG);
    expect(button("Save password")?.disabled).toBe(false);
  });

  it("sends only the password on update, empties the field and never renders it", async () => {
    const api = server();
    await mount(<SignInSection />);
    const field = await waitFor(() => passwordField(/Shared default password/));
    await type(field, STRONG);
    await submit(field.closest("form"));
    await waitFor(() => api.callsTo("PUT /api/secrets/signin-default").length === 1);
    expect(api.callsTo("PUT /api/secrets/signin-default")[0].body).toEqual({ value: STRONG });
    expect(field.value).toBe("");
    expect(document.body.innerHTML).not.toContain(STRONG);
  });

  it("offers a generator whose result satisfies every rule", async () => {
    server();
    await mount(<SignInSection />);
    const field = await waitFor(() => passwordField(/Shared default password/));
    await click(button("Generate a password"));
    expect(field.value.length).toBeGreaterThanOrEqual(12);
    expect(rulesMet(field.value).every(Boolean)).toBe(true);
    expect(button("Save password")?.disabled).toBe(false);
    for (let i = 0; i < 300; i++) {
      const pw = generatePassword();
      expect(rulesMet(pw).every(Boolean)).toBe(true);
      expect(/(.)\1\1/.test(pw)).toBe(false);
    }
  });

  it("normalizes a pasted job address into the site key", async () => {
    const api = server();
    await mount(<SignInSection />);
    const site = await waitFor(() => passwordField(/Site \(paste/));
    await type(site, "https://Careers.Example.TEST:8443/jobs/42?utm=x#top");
    expect(text()).toContain("Stored for careers.example.test");
    await type(passwordField(/Password for this site/), SITE_PW);
    await submit(site.closest("form"));
    await waitFor(() => api.callsTo("PUT /api/secrets/signin-site-careers.example.test").length === 1);
    expect(api.callsTo("PUT /api/secrets/signin-site-careers.example.test")[0].body).toEqual({ value: SITE_PW });
    expect(document.body.innerHTML).not.toContain(SITE_PW);
    expect(siteKey("ftp://example.test/x")).toBeNull();
    expect(siteKey("https://user:pw@example.test/")).toBeNull();
    expect(siteKey("Jobs.Example.Test")).toBe("jobs.example.test");
  });

  it("lists alternates by label and removes one by id after a confirmation", async () => {
    const api = server([entry({ name: "signin-default", email: "alex@example.com" }), alternate("0123456789ab", "Old laptop", 1), alternate("ba9876543210", "Work", 2)]);
    await mount(<SignInSection />);
    await click(await waitFor(() => labelled("Remove Old laptop")));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/secrets/alternates/0123456789ab")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Remove")!);
    await waitFor(() => api.callsTo("DELETE /api/secrets/alternates/0123456789ab").length === 1);
  });

  it("moves an alternate by sending the whole new order", async () => {
    const api = server([entry({ name: "signin-default", email: "alex@example.com" }), alternate("0123456789ab", "Old laptop", 1), alternate("ba9876543210", "Work", 2)]);
    await mount(<SignInSection />);
    await click(await waitFor(() => labelled("Move Work up")));
    await waitFor(() => api.callsTo("PUT /api/secrets/alternates/order").length === 1);
    expect(api.callsTo("PUT /api/secrets/alternates/order")[0].body).toEqual({ ids: ["ba9876543210", "0123456789ab"] });
  });
});

// The setup step's dashboard instruction sends the user here (flow round 7):
// the key is stored under the name the apply chain's solver reads, and like a
// password it is never kept in the field or drawn on the page.
describe("Settings: captcha solver", () => {
  it("stores the 2Captcha key under the solver's secret name, empties the field and shows it saved", async () => {
    const KEY = ["captcha", "canary", "7"].join("-");
    let present = false;
    const api = fakeApi({
      "GET /api/secrets": () => ok({ registry: [], entries: [{ name: "twocaptcha-key", kind: "token", present }] }),
      "PUT /api/secrets/twocaptcha-key": () => {
        present = true;
        return ok({ name: "twocaptcha-key", present: true, source: "keychain" });
      },
    });
    await mount(<CaptchaSection />);
    const field = await waitFor(() => passwordField(/2Captcha API key/));
    expect(field.labels?.[0]?.textContent).toContain("not set");
    await type(field, KEY);
    await submit(field.closest("form"));
    await waitFor(() => api.callsTo("PUT /api/secrets/twocaptcha-key").length === 1);
    expect(api.callsTo("PUT /api/secrets/twocaptcha-key")[0].body).toEqual({ value: KEY });
    expect(field.value).toBe("");
    await waitFor(() => passwordField(/2Captcha API key/).labels?.[0]?.textContent?.includes("saved") === true);
    expect(document.body.innerHTML).not.toContain(KEY);
  });

  // Deleting a stored secret is destructive, so it happens only after the
  // dialog is confirmed, and only to the solver's own secret name.
  it("removes the saved key only after the dialog is confirmed", async () => {
    let present = true;
    const api = fakeApi({
      "GET /api/secrets": () => ok({ registry: [], entries: [{ name: "twocaptcha-key", kind: "token", present }] }),
      "DELETE /api/secrets/twocaptcha-key": () => {
        present = false;
        return ok({ name: "twocaptcha-key", present: false });
      },
    });
    await mount(<CaptchaSection />);
    await click(await waitFor(() => button("Remove key")));
    expect(api.callsTo("DELETE /api/secrets/twocaptcha-key")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Remove key")!);
    await waitFor(() => api.callsTo("DELETE /api/secrets/twocaptcha-key").length === 1);
    await waitFor(() => passwordField(/2Captcha API key/).labels?.[0]?.textContent?.includes("not set") === true);
  });

  // The key test is one balance read on the user's press, never on load, and
  // the balance shows as dollars and cents ($X.XX).
  it("tests the saved key only when asked and shows the balance", async () => {
    const api = fakeApi({
      "GET /api/secrets": () => ok({ registry: [], entries: [{ name: "twocaptcha-key", kind: "token", present: true }] }),
      "POST /api/secrets/twocaptcha-key/test": () => ok({ name: "twocaptcha-key", present: true, live: "ok", balanceCents: 42, belowFloor: true }),
    });
    await mount(<CaptchaSection />);
    await click(await waitFor(() => button("Test key")));
    await waitFor(() => text().includes("$0.42"));
    expect(api.callsTo("POST /api/secrets/twocaptcha-key/test")).toHaveLength(1);
  });

  // The policy read fills an absent captcha_solver with its default, on, and a
  // policy.yml with errors has no values and reads off (apply-safety-gates:
  // "An unreadable or invalid policy.yml SHALL read as off"). A press writes
  // the key through the policy editor's route, again at the file's current
  // revision after a conflict (flow round 12, S-1).
  it("shows the solver switch as the policy holds it and turns it off through the policy route, keeping focus on the switch", async () => {
    const policy = (revision: string, over: Partial<ConfigDocument> = {}): ConfigDocument => ({ file: "policy", exists: true, template: false, content: "", revision, issues: [], values: { captcha_solver: "on" }, ...over });
    const solver = () => qa<HTMLInputElement>("input[type=checkbox]").find((i) => i.labels?.[0]?.textContent?.trim() === "Use the solver") ?? null;
    const secrets = () => ok({ registry: [], entries: [] });
    for (const [doc, checked, disabled] of [
      [policy("p1"), true, false],
      [policy("p1", { values: { captcha_solver: "off" } }), false, false],
      [policy("p1", { issues: [{ code: "not_allowed", message: '"maybe" is not an allowed value', path: "captcha_solver", line: 2 }], values: undefined }), false, true],
    ] as const) {
      fakeApi({ "GET /api/secrets": secrets, "GET /api/config/policy": () => ok(doc) });
      await mount(<CaptchaSection />);
      const sw = await waitFor(solver);
      expect([sw.checked, sw.disabled]).toEqual([checked, disabled]);
    }

    let doc = policy("p1");
    const api = fakeApi({
      "GET /api/secrets": secrets,
      "GET /api/config/policy": () => ok(doc),
      "PATCH /api/config/policy": (c) => {
        if ((c.body as { expectedRevision: string }).expectedRevision !== "p2") return fail("revision_conflict", "policy.yml changed on disk; reload it and apply the change again");
        doc = policy("p3", { values: { captcha_solver: "off" } });
        return ok({ revision: "p3", changed: true });
      },
    });
    await mount(<CaptchaSection />);
    const sw = await waitFor(solver);
    doc = policy("p2"); // the file changes after the page read it
    sw.focus();
    await click(sw);
    await waitFor(() => !sw.checked);
    const off = [{ op: "set", path: "captcha_solver", value: "off" }];
    expect(api.callsTo("PATCH /api/config/policy").map((c) => c.body)).toEqual([{ expectedRevision: "p1", ops: off }, { expectedRevision: "p2", ops: off }]);
    expect(document.activeElement).toBe(sw);
  });
});
