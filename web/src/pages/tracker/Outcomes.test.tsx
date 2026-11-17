import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { OutcomeReview } from "./Outcomes";

const stages = (open: number) => [
  { stage: "responded", numerator: 3, denominator: 8, open, unknown: 2 },
  { stage: "interview", numerator: 2, denominator: 8, open, unknown: 2 },
  { stage: "offer", numerator: 0, denominator: 8, open, unknown: 2 },
  { stage: "rejected", numerator: 4, denominator: 8, open, unknown: 2 },
];

const outcomes = {
  from: "2026-09-01", to: "", asOf: "2026-10-12", folded: 1, noApplicationDate: 2,
  overall: { key: "", applications: 10, known: 8, unknown: 2, open: 3, closed: 5, stages: stages(3), insufficient: ["fewer than 10 applications with event data"] },
  bySource: [{ key: "referral", applications: 4, known: 4, unknown: 0, open: 1, closed: 3, stages: stages(1), insufficient: [] }, { key: "", applications: 6, known: 4, unknown: 2, open: 2, closed: 2, stages: stages(2), insufficient: ["fewer than 10 applications with event data"] }],
  byRoleFamily: [],
};

describe("OutcomeReview", () => {
  it("sends the cohort dates and the cutoff", async () => {
    const api = fakeApi({ "GET /api/tracker/outcomes": () => ok(outcomes) });
    await mount(<OutcomeReview />);
    await click(button("Show"));
    await waitFor(() => text().includes("read as of"));
    const dates = qa<HTMLInputElement>("input[type=date]");
    await type(dates[0], "2026-09-01");
    await type(dates[1], "2026-09-30");
    await type(dates[2], "2026-10-05");
    await submit(q("form[aria-label=Cohort]"));
    await waitFor(() => api.callsTo("GET /api/tracker/outcomes").some((c) => c.query.get("asOf") === "2026-10-05"));
    const last = api.callsTo("GET /api/tracker/outcomes").at(-1)!;
    expect([last.query.get("from"), last.query.get("to")]).toEqual(["2026-09-01", "2026-09-30"]);
  });

  it("shows a failed summary with its message", async () => {
    fakeApi({ "GET /api/tracker/outcomes": () => fail("validation_failed", "from is after to") });
    await mount(<OutcomeReview />);
    await click(button("Show"));
    await waitFor(() => text().includes("from is after to"));
  });
});
