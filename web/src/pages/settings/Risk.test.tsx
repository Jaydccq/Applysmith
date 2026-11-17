import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../../testing/api";
import { mount, qa, text, waitFor } from "../../testing/dom";
import type { RiskView } from "../../wire";
import { RiskSection } from "./Risk";

const view: RiskView = {
  days: 30,
  baseline: [
    { source: "indeed", runs: 12, visits: 765, challenges: 3, rateLimited: 0 },
    { source: "ziprecruiter", runs: 4, visits: 0, challenges: 0, rateLimited: 1 },
  ],
  signals: [
    { system: "workday", signal: "verification_required", at: "2027-03-09T10:00:00Z", endsAt: "2027-03-12T10:00:00Z", runId: "run_a" },
    { system: "lever", signal: "rate_limited", at: "2027-03-08T10:00:00Z" },
  ],
  cooldowns: [
    { kind: "apply", system: "workday", signal: "verification_required", until: "2027-03-12T10:00:00Z" },
    { kind: "apply", system: "generic", host: "careers.example.test", signal: "captcha", until: "2027-03-15T10:00:00Z" },
    { kind: "scan", system: "glassdoor", signal: "blocked scan", until: "2027-03-10T15:00:00Z" },
  ],
};

describe("Settings: detection and cooldowns", () => {
  it("shows the active cooldowns, the challenge rate per source and the recent signals, and offers nothing to change", async () => {
    const api = fakeApi({ "GET /api/policy/risk": () => ok(view) });
    await mount(<RiskSection />);
    await waitFor(() => text().includes("Cooling down now"));
    expect(text()).toContain("workday");
    expect(text()).toContain("account creation");
    expect(text()).toContain("glassdoor");
    // A site the generic flow serves is held alone, by its host.
    expect(qa("li").some((l) => l.textContent?.includes("careers.example.test") && l.textContent.includes("account creation"))).toBe(true);
    expect(text()).toContain("verification required");
    const rows = qa("table")[0].querySelectorAll("tbody tr");
    expect(rows[0].textContent).toContain("indeed");
    expect(rows[0].textContent).toContain("0.4 per 100");
    expect(rows[1].textContent).toContain("none");
    expect(qa("tbody tr").some((r) => r.textContent?.includes("lever") && r.textContent.includes("none"))).toBe(true);
    expect(qa<HTMLAnchorElement>("a").map((a) => a.getAttribute("href"))).toContain("/runs/run_a");
    expect(qa("button, input, select")).toHaveLength(0);
    expect(api.calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });
});
