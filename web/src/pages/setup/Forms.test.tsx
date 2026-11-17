import { describe, expect, it, vi } from "vitest";
import { fail, ok } from "../../testing/api";
import { button, byText, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { applied, CLI_LIMITS, EDGAR_DOC, llmRouting, profileDoc, setupApi, setupState, setupStep } from "../../testing/fixtures_setup";
import Setup from "../Setup";

const CANARY = ["sk", "or", "canary", "0123456789abcdef"].join("-");
const field = (name: string) => q(`[data-field="${name}"]`)!;
const input = (name: string) => field(name).querySelector<HTMLInputElement>("input, select, textarea")!;

async function openStep(id: string, patch: Parameters<typeof setupState>[0] = {}, routes: Parameters<typeof setupApi>[1] = {}) {
  const api = setupApi(setupState(patch), routes);
  window.history.replaceState(null, "", `/setup?step=${id}`);
  await mount(<Setup />);
  await waitFor(() => q(`article[data-step="${id}"]`));
  return api;
}

describe("Setup: schema-driven forms", () => {
  it("sends only the fields that were filled in, never an empty one, and the ask-me marker for a field left to the person", async () => {
    const api = await openStep("identity", { identity: { status: "missing" } });
    await waitFor(() => input("full_name"));
    await type(input("full_name"), "  Riley Sample ");
    await type(input("phone"), "");
    await click(field("street_address").querySelector<HTMLInputElement>("input[type=checkbox]")!);
    expect(input("street_address").disabled).toBe(true);
    expect(field("street_address").textContent).not.toContain("@ask-me");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/identity/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/identity/apply")[0].body).toEqual({ input: { full_name: "Riley Sample", street_address: "@ask-me" } });
  });

  it("turns a list field into an array and a number into a number", async () => {
    const api = await openStep("identity", { identity: { status: "missing" } });
    await waitFor(() => input("other_links"));
    await type(input("other_links"), "https://example.com/a, https://example.com/b");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/identity/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/identity/apply")[0].body).toEqual({ input: { other_links: ["https://example.com/a", "https://example.com/b"] } });
  });

  it("starts from the stored profile and does not send a value that was not changed", async () => {
    const api = await openStep(
      "identity",
      { identity: { status: "partial" } },
      { "GET /api/config/profile": () => ok(profileDoc({ candidate: { full_name: "Riley Sample", email: "riley@mail.test", address: { city: "Springfield" } }, links: { other: ["https://example.com/x"] } })) },
    );
    await waitFor(() => input("email").value === "riley@mail.test");
    expect(input("full_name").value).toBe("Riley Sample");
    expect(input("city").value).toBe("Springfield");
    expect(input("other_links").value).toBe("https://example.com/x");
    expect(button("Save identity")?.disabled).toBe(true);
    await type(input("city"), "Shelbyville");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/identity/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/identity/apply")[0].body).toEqual({ input: { city: "Shelbyville" } });
  });

  it("offers what the saved CV says for the empty fields only, and stores nothing until the form is saved", async () => {
    // The server reads cv.md (cvparse TestIdentityReadsTheNameAndContactLine).
    const identity = { full_name: "Riley Sample", email: "riley.sample@mailbox.test", phone: "+14155550142", city: "Leeds", region: "West Yorkshire", linkedin: "https://www.linkedin.com/in/riley" };
    const api = await openStep(
      "identity",
      { identity: { status: "partial" } },
      {
        "GET /api/config/profile": () => ok(profileDoc({ candidate: { email: "riley@mail.test" } })),
        "GET /api/config/cv/draft": () => ok({ revision: "r", employment: [], education: [], skills: [], identity, dismissed: [] }),
      },
    );
    const found = await waitFor(() => q("[aria-labelledby=from-cv]"));
    expect(found.textContent).not.toContain("riley.sample@mailbox.test");
    await click(button(/^Use all/));
    expect(api.callsTo("POST /api/setup/steps/identity/apply")).toHaveLength(0);
    expect(input("email").value).toBe("riley@mail.test");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/identity/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/identity/apply")[0].body).toEqual({
      input: { full_name: "Riley Sample", phone: "+14155550142", city: "Leeds", region: "West Yorkshire", linkedin: "https://www.linkedin.com/in/riley" },
    });
  });

  it("shows the writer's per-field errors beside the fields they belong to", async () => {
    await openStep(
      "identity",
      { identity: { status: "missing" } },
      {
        "POST /api/setup/steps/identity/apply": () =>
          fail("validation_failed", "input rejected: email: must be a real address; phone: use international form", {
            details: { fields: { email: "must be a real address", "phone.number": "use international form" } },
          }),
      },
    );
    await waitFor(() => input("email"));
    await type(input("email"), "riley@example.com");
    await submit(q("form"));
    await waitFor(() => field("email").textContent?.includes("must be a real address"));
    expect(field("phone").textContent).toContain("Number: use international form");
    expect(field("full_name").querySelector("[role=alert]")).toBeNull();
    expect(input("email").getAttribute("aria-invalid")).toBe("true");
    expect(text()).toContain("Some answers need another look.");
  });

  it("falls back to a validated JSON box for a shape without a purpose-built editor", async () => {
    const state = setupState();
    state.steps = [{ ...setupStep("identity"), id: "extra", title: "Extra", status: "partial", fields: [{ name: "settings", type: "object", doc: "Free-form settings." }, { name: "limit", type: "int", doc: "A limit." }] }];
    state.next = "extra";
    const api = setupApi(state);
    await mount(<Setup />);
    await waitFor(() => input("settings"));
    await type(input("settings"), "{not json");
    await submit(q("form"));
    await waitFor(() => field("settings").textContent?.includes("This is not valid JSON."));
    expect(api.callsTo("POST /api/setup/steps/extra/apply")).toHaveLength(0);
    await type(input("settings"), '{"a": 1}');
    await type(input("limit"), "7");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/extra/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/extra/apply")[0].body).toEqual({ input: { settings: { a: 1 }, limit: 7 } });
  });
});

describe("Setup: work authorization", () => {
  it("leaves both sponsorship questions unanswered until the person answers", async () => {
    const api = await openStep("work-authorization", { "work-authorization": { status: "partial" } });
    await waitFor(() => field("requires_sponsorship_now"));
    expect(qa<HTMLInputElement>("input[type=radio]").every((r) => !r.checked)).toBe(true);
    expect(field("requires_sponsorship_now").textContent).toContain("Not answered yet");
    await click(field("requires_sponsorship_future").querySelectorAll("input[type=radio]")[1]);
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/work-authorization/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/work-authorization/apply")[0].body).toEqual({ input: { requires_sponsorship_future: false } });
  });
});

describe("Setup: targets", () => {
  it("edits roles and compensation with their own controls", async () => {
    const api = await openStep("targets", { targets: { status: "missing" } });
    await click(await waitFor(() => button("Add a role")));
    const role = q("[data-role='0']")!;
    await type(role.querySelector("input"), "Staff Engineer");
    await type(role.querySelectorAll("select")[0] as HTMLSelectElement, "staff");
    const comp = q('[role=group][aria-label="Compensation"]')!;
    await type(comp.querySelectorAll("input")[0], "usd");
    await type(comp.querySelector("select"), "year");
    await type(comp.querySelectorAll("input")[1], "150,000");
    await type(comp.querySelectorAll("input")[2], "180000");
    await type(field("locations").querySelector("textarea"), "Austin, TX\nanywhere or remote");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/targets/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/targets/apply")[0].body).toEqual({
      input: {
        roles: [{ title: "Staff Engineer", seniority: "staff" }],
        locations: ["Austin, TX", "anywhere or remote"],
        compensation: { currency: "USD", period: "year", minimum: 150000, target: 180000 },
      },
    });
  });

  // Pay is written "$120,000" or "150k"; text that is no amount waits beside its hint.
  it("reads pay written with a currency sign or k for thousands, and holds a save of text that is no amount", async () => {
    const api = await openStep("targets", { targets: { status: "missing" } });
    const comp = await waitFor(() => q('[role=group][aria-label="Compensation"]'));
    const [, min, tgt] = Array.from(comp.querySelectorAll("input"));
    await type(min, "about 120");
    expect(comp.textContent).toContain("Enter a whole amount, such as 120,000 or 150k.");
    expect(button("Save targets")!.disabled).toBe(true);
    await submit(q("form"));
    expect(api.callsTo("POST /api/setup/steps/targets/apply")).toHaveLength(0);
    await type(min, "$120,000");
    await type(tgt, "150k");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/targets/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/targets/apply")[0].body).toEqual({ input: { compensation: { minimum: 120000, target: 150000 } } });
  });

  it("sends an edited skip list, and an empty one when the person skips no employer", async () => {
    const api = await openStep("targets", {
      targets: { status: "partial", reasons: [{ field: "batch_skip_companies", text: "the batch skip list (Northwind, Contoso) needs review: confirm it or edit it" }] },
    });
    await waitFor(() => input("batch_skip_companies"));
    await type(input("batch_skip_companies"), "Northwind");
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/targets/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/targets/apply")[0].body).toEqual({ input: { batch_skip_companies: ["Northwind"] } });
    await click(byText("label", "Skip no employer")!.querySelector("input"));
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/targets/apply").length === 2);
    expect(api.callsTo("POST /api/setup/steps/targets/apply")[1].body).toEqual({ input: { batch_skip_companies: [] } });
  });
});

describe("Setup: language-model provider", () => {
  it("sends the key write-only, empties the field at once and never renders it", async () => {
    const api = await openStep(
      "llm",
      { llm: { status: "missing" } },
      {
        "PUT /api/llm/keys/openrouter": () => ok({ provider: "openrouter", keyConfigured: true }),
        "POST /api/setup/steps/llm/apply": () => ok(applied(setupStep("llm"), { notes: ["Test call to openrouter succeeded in 420 ms."] })),
      },
    );
    await click(await waitFor(() => qa<HTMLInputElement>("input[type=radio]").find((r) => r.parentElement?.textContent === "OpenRouter")!));
    expect(byText("a", "https://openrouter.ai/keys")?.getAttribute("href")).toBe("https://openrouter.ai/keys");
    const key = q<HTMLInputElement>("input[type=password]")!;
    expect(key.getAttribute("autocomplete")).toBe("new-password");
    await type(key, CANARY);
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/llm/apply").length === 1);
    expect(api.callsTo("PUT /api/llm/keys/openrouter")[0].body).toEqual({ key: CANARY });
    expect(api.callsTo("POST /api/setup/steps/llm/apply")[0].body).toEqual({ input: { preset: "openrouter" } });
    expect(key.value).toBe("");
    await waitFor(() => text().includes("Test call to openrouter succeeded in 420 ms."));
    expect(document.body.innerHTML).not.toContain(CANARY);
    expect(JSON.stringify(api.callsTo("POST /api/setup/steps/llm/apply"))).not.toContain(CANARY);
  });

  it("does not echo a key the server refused", async () => {
    await openStep("llm", { llm: { status: "missing" } }, { "PUT /api/llm/keys/openrouter": () => fail("validation_failed", "The key is not accepted.", { details: { field: "key" } }) });
    await click(await waitFor(() => qa<HTMLInputElement>("input[type=radio]").find((r) => r.parentElement?.textContent === "OpenRouter")!));
    const key = q<HTMLInputElement>("input[type=password]")!;
    await type(key, CANARY);
    await submit(q("form"));
    await waitFor(() => text().includes("The key is not accepted."));
    expect(key.value).toBe("");
    expect(document.body.innerHTML).not.toContain(CANARY);
  });

  it("needs the limits of a command-line preset to be accepted before it is saved", async () => {
    const api = await openStep("llm", { llm: { status: "missing" } });
    await click(await waitFor(() => qa<HTMLInputElement>("input[type=radio]").find((r) => r.parentElement?.textContent?.startsWith("Codex")))!);
    expect(text()).toContain(CLI_LIMITS);
    const save = button("Save and test")!;
    expect(save.disabled).toBe(true);
    await click(byText("label", "I have read this and want to continue")!.querySelector("input"));
    expect(save.disabled).toBe(false);
    await click(save);
    await waitFor(() => api.callsTo("POST /api/setup/steps/llm/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/llm/apply")[0].body).toEqual({ input: { cli: "codex", acknowledge_cli_limits: true } });
  });

  it("signs in with the ChatGPT plan instead of taking a key, and saves it once the plan is usable", async () => {
    let status = { signedIn: false, planUsage: false, pending: false };
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const api = await openStep("llm", { llm: { status: "missing" } }, {
      "GET /api/llm/chatgpt": () => ok(status),
      "PUT /api/llm/providers/chatgpt": () => ok(llmRouting),
      "POST /api/llm/chatgpt/signin": () => ok({ url: "https://auth.openai.com/x" }),
    });
    await click(await waitFor(() => qa<HTMLInputElement>("input[type=radio]").find((r) => r.parentElement?.textContent === "ChatGPT plan (sign in)")!));
    expect(q("input[type=password]")).toBeNull();
    expect(button("Save and test")!.disabled).toBe(true);
    status = { signedIn: true, planUsage: true, pending: false };
    await click(await waitFor(() => button("Sign in with ChatGPT")));
    await waitFor(() => api.callsTo("POST /api/llm/chatgpt/signin").length === 1);
    expect(open).toHaveBeenCalledWith("https://auth.openai.com/x", "_blank", "noopener,noreferrer");
    const save = await waitFor(() => button("Save and test")?.disabled === false && button("Save and test"));
    await click(save);
    await waitFor(() => api.callsTo("POST /api/setup/steps/llm/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/llm/apply")[0].body).toEqual({ input: { preset: "chatgpt" } });
    open.mockRestore();
  });
});

describe("Setup: discovery and the steps a person finishes", () => {
  it("shows the EDGAR disclosure before it can be read, then sends both flags with the pack", async () => {
    const api = await openStep("discovery", { discovery: { status: "missing" } });
    await click(await waitFor(() => qa<HTMLInputElement>("input[type=radio]").find((r) => r.parentElement?.textContent === "daily")!));
    expect(text()).not.toContain(EDGAR_DOC);
    expect(byText("label", "I have read this")).toBeNull();
    await click(byText("button", "Read what it sends"));
    expect(text()).toContain(EDGAR_DOC);
    expect(text().split(EDGAR_DOC)).toHaveLength(2);
    const box = byText("label", "I have read this")!.querySelector("input")!;
    expect(box.checked).toBe(false);
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/discovery/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/discovery/apply")[0].body).toEqual({ input: { pack: "daily" } });
    await click(box);
    await submit(q("form"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/discovery/apply").length === 2);
    expect(api.callsTo("POST /api/setup/steps/discovery/apply")[1].body).toEqual({ input: { enable_edgar: true, acknowledge_edgar_disclosure: true } });
  });
});
