import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { button, click, mount, settle, text, waitFor } from "../testing/dom";
import Browser from "./Browser";

const running = { runId: "run_a", status: "running", filled: [], review: [], missing: [], skipped: [], drafts: 0, flagged: 0, events: 0 };
const done = {
  ...running, status: "succeeded", url: "https://jobs.example.test/apply/1", site: "example",
  filled: [{ label: "First name" }, { label: "Email" }],
  review: [{ label: "Phone" }],
  missing: [{ label: "Why us", reason: "abstained:no-answer" }, { label: "Referral code", reason: "no-answer" }],
  skipped: [{ label: "Pronouns", reason: "sensitive" }],
  drafts: 1, flagged: 0, events: 3,
};

describe("Check sign-ins", () => {
  const checks = {
    checks: [
      { domain: "linkedin.com", label: "LinkedIn", state: "signed_out", next: "needs-user", checkedAt: "2027-03-10T09:00:00Z", cached: false, error: "" },
      { domain: "indeed.com", label: "Indeed", state: "captcha", next: "blocked-by-safety", checkedAt: "2027-03-10T09:00:00Z", cached: true, error: "" },
      { domain: "jobright.ai", label: "Jobright", state: "unknown", next: "proceed", checkedAt: null, cached: false, error: "the site could not be checked" },
    ],
  };

  it("asks the server to probe on the press and lists each site with what to do next", async () => {
    const api = fakeApi({ "POST /api/browser/sessions/check": () => ok(checks) });
    await mount(<Browser />);
    expect(api.callsTo("POST /api/browser/sessions/check")).toHaveLength(0);
    await click(button("Check sign-ins"));
    await waitFor(() => text().includes("LinkedIn: signed out"));
    expect(api.callsTo("POST /api/browser/sessions/check")).toHaveLength(1);
    expect(text()).toContain("you need to sign in");
    expect(text()).toContain("Indeed: captcha");
    expect(text()).toContain("a challenge is in the way");
    expect(text()).toContain("Jobright: not checked (the site could not be checked)");
    expect(text()).toContain("Checked 3 sites: 0 signed in, 2 need you, 1 could not be read.");
    // A time reads on this computer's clock, never as the raw timestamp.
    expect(text()).toContain("checked 2027-03-10 01:00");
    expect(text()).not.toContain("09:00:00Z");
  });

  it("shows the refusal when the browser is not available", async () => {
    fakeApi({ "POST /api/browser/sessions/check": () => fail("unavailable", "the browser did not start", { details: { reason: "launch-timeout" } }) });
    await mount(<Browser />);
    await click(button("Check sign-ins"));
    await waitFor(() => text().includes("the browser did not start"));
  });
});

describe("Autofill this tab", () => {
  it("reports the counts and lists the leftovers, and says nothing was submitted", async () => {
    const api = fakeApi({
      "POST /api/autofill/active-tab": () => ({ data: { runId: "run_a" }, status: 202 }),
      "GET /api/autofill/active-tab/run_a": () => ok(done),
    });
    await mount(<Browser />);
    await click(button("Autofill this tab"));
    await waitFor(() => text().includes("Filled 2, review 1, missing 2, skipped 1"));
    expect(api.callsTo("POST /api/autofill/active-tab")).toHaveLength(1);
    expect(api.callsTo("POST /api/autofill/active-tab")[0].body).toBeUndefined();
    expect(text()).toContain("on https://jobs.example.test/apply/1");
    const missing = document.querySelector("section[aria-label='Missing']")!;
    expect(Array.from(missing.querySelectorAll("li")).map((l) => l.textContent)).toEqual(["Why us (abstained:no-answer)", "Referral code (no-answer)"]);
    expect(text()).toContain("Drafts awaiting review: 1");
    expect(text()).toContain("Learning events accepted: 3");
    expect(text()).toContain("Nothing was submitted");
  });

  it("shows the refusal for a page that is not an application and starts no fill", async () => {
    const api = fakeApi({
      "POST /api/autofill/active-tab": () => fail("validation_failed", "this page is not an application form: the page shows 0 data-entry controls", { details: { reason: "not-an-application" } }),
    });
    await mount(<Browser />);
    await click(button("Autofill this tab"));
    await waitFor(() => text().includes("this page is not an application form"));
    expect(text()).toContain("Open the application form in the apply window");
    expect(api.callsTo("GET /api/autofill/active-tab/run_a")).toHaveLength(0);
  });

  it("disables the button while the press is sent", async () => {
    let release: (v: unknown) => void = () => {};
    fakeApi({
      "POST /api/autofill/active-tab": () => new Promise((r) => (release = r)) as never,
    });
    await mount(<Browser />);
    const press = click(button("Autofill this tab"));
    await settle();
    expect(button("Autofill this tab")?.disabled).toBe(true);
    release({ data: { runId: "run_a" }, status: 202 });
    await press;
  });
});
