import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../../testing/api";
import { button, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { SkillGaps } from "./Gaps";

const source = (over = {}) => ({ report: 3, company: "Contoso Example", role: "Engineer", requirement: "Rust", quote: "", class: "required", evidence: "", unsupported: false, ...over });
const gap = (over = {}) => ({ skill: "rust", label: "Rust", state: "not-evidenced", required: 2, preferred: 0, sources: [source()], ...over });

type Body = Record<string, unknown> & { gaps: Record<string, unknown>[] };

function analysis(over: Record<string, unknown> = {}): Body {
  return {
    id: "run_1", generatedAt: "2026-10-12T10:00:00Z", selected: [3, 4, 5], sample: [{ report: 3, createdAt: "2026-10-01T00:00:00Z", contentHash: "h" }],
    excluded: [{ report: 5, reason: "duplicate-posting", duplicateOf: 3 }, { report: 4, reason: "no-requirement-evidence", duplicateOf: 0 }],
    gaps: [gap(), gap({ skill: "terraform", label: "Terraform", state: "needs-review", required: 0, preferred: 1, sources: [source({ requirement: "Terraform", class: "preferred", evidence: "CV: some exposure" })] })],
    inferred: [{ report: 3, company: "Contoso Example", role: "Engineer", label: "Team leadership", evidence: "" }], plan: null, done: [], ...over,
  };
}

function server(initial: Body = analysis()): FakeApi {
  let current: Body = initial;
  const api = fakeApi({
    "GET /api/evaluations/gaps": () => ok({ analyses: [{ id: "run_1", generatedAt: "2026-10-12T10:00:00Z", sample: 1, gaps: 2 }] }),
    "POST /api/evaluations/gaps": () => ({ data: { runId: "run_1" }, status: 202 }),
    "GET /api/evaluations/gaps/run_1": () => ok(current),
    "POST /api/evaluations/gaps/run_1/confirm": (c) => {
      const body = c.body as { skill: string; confirmed: boolean };
      current = { ...current, gaps: current.gaps.map((g) => (g.skill === body.skill ? { ...g, state: body.confirmed ? "confirmed-gap" : "not-evidenced" } : g)) };
      return ok(current);
    },
  });
  return api;
}

async function open() {
  await mount(<SkillGaps shown={[3, 4, 5]} />);
  await click(button("Show"));
}

describe("SkillGaps", () => {
  it("starts an analysis under an idempotency key and shows the sample, the exclusions and the gaps without a fit score", async () => {
    const api = server();
    await open();
    await click(button("Use the reports listed above"));
    expect(q<HTMLInputElement>("input[placeholder]")!.value).toBe("3, 4, 5");
    await submit(q("form[aria-label='Analyze skill gaps']"));
    await waitFor(() => text().includes("1 posting analyzed from 3 selected"));
    const post = api.callsTo("POST /api/evaluations/gaps")[0];
    expect(post.body).toEqual({ reports: [3, 4, 5] });
    expect(post.headers.get("Idempotency-Key")).toMatch(/^web-/);
    expect(text()).toContain("no fit score");
    expect(text()).toContain("2 excluded");
    expect(text()).toContain("Not evidenced");
    expect(text()).toContain("Needs review");
    expect(text()).toContain("1 inferred, not counted");
    expect(text()).not.toMatch(/Score \d/);
  });

  it("keeps a not-evidenced gap out of the plan until the user confirms it", async () => {
    const api = server();
    await open();
    await click(qa<HTMLButtonElement>("ul[aria-label='Earlier analyses'] button")[0] ?? (await waitFor(() => qa<HTMLButtonElement>("ul[aria-label='Earlier analyses'] button")[0])));
    await waitFor(() => text().includes("Gaps, most demanded first"));
    expect(q<HTMLInputElement>("input[aria-label='Plan Rust']")!.disabled).toBe(true);
    await click(button("I lack this"));
    await waitFor(() => text().includes("Confirmed gap"));
    expect(api.callsTo("POST /api/evaluations/gaps/run_1/confirm")[0].body).toEqual({ skill: "rust", confirmed: true });
    expect(q<HTMLInputElement>("input[aria-label='Plan Rust']")!.disabled).toBe(false);
  });

  it("shows resource lookup as unavailable and offers progress that touches only the plan", async () => {
    const api = server(analysis({ gaps: [gap({ state: "confirmed-gap" })] }));
    const plan = {
      generatedAt: "2026-10-12T11:00:00Z", lookup: { status: "unavailable", reason: "no research source is configured, so no learning resources are listed" },
      items: [{ skill: "rust", label: "Rust", exercise: "Build a small project.", criterion: "It runs from a clean checkout.", effort: "Estimate: 15 to 25 hours of focused practice. This is a guess, not a measured figure.", resources: [], done: false }],
    };
    let current = analysis({ gaps: [gap({ state: "confirmed-gap" })] });
    api.on("GET /api/evaluations/gaps/run_1", () => ok(current));
    api.on("POST /api/evaluations/gaps/run_1/plan", () => {
      current = { ...current, plan };
      return ok(current);
    });
    api.on("POST /api/evaluations/gaps/run_1/progress", (c) => {
      const body = c.body as { done: boolean };
      current = { ...current, plan: { ...plan, items: [{ ...plan.items[0], done: body.done }] }, done: body.done ? ["rust"] : [] };
      return ok(current);
    });
    await open();
    await click(await waitFor(() => qa<HTMLButtonElement>("ul[aria-label='Earlier analyses'] button")[0]));
    await waitFor(() => q("input[aria-label='Plan Rust']"));
    await click(q("input[aria-label='Plan Rust']"));
    await click(q("label.check input[type=checkbox]"));
    await click(button(/Build a plan for 1 gap/));
    await waitFor(() => text().includes("Resource lookup unavailable"));
    expect(api.callsTo("POST /api/evaluations/gaps/run_1/plan")[0].body).toEqual({ skills: ["rust"], lookupResources: true });
    expect(text()).toContain("Estimate: 15 to 25 hours");
    expect(q("ul[aria-label='Resources for Rust']")).toBeNull();
    await click(q("section[aria-label='Learning plan'] input[type=checkbox]"));
    await waitFor(() => (q<HTMLInputElement>("section[aria-label='Learning plan'] input[type=checkbox]")?.checked ?? false) === true);
    expect(text()).toContain("adds no experience or skill to your profile");
    expect(api.callsTo("POST /api/evaluations/gaps/run_1/progress")[0].body).toEqual({ skill: "rust", done: true });
    expect(text()).toContain("Confirmed gap");
  });

  it("shows a refused analysis with its message", async () => {
    const api = server();
    api.on("POST /api/evaluations/gaps", () => fail("validation_failed", "select 1 to 100 reports, got 0"));
    await open();
    await type(q<HTMLInputElement>("input[placeholder]"), "3");
    await submit(q("form[aria-label='Analyze skill gaps']"));
    await waitFor(() => text().includes("select 1 to 100 reports"));
  });
});
