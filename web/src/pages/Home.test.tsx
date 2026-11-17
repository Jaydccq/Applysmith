import { act } from "react";
import { describe, expect, it } from "vitest";
import { BoardContext } from "../hooks";
import { fail, fakeApi, manualBoard, ok } from "../testing/api";
import { byText, click, mount, q, qa, submit, text, type, waitFor } from "../testing/dom";
import { applyList, routing, summary } from "../testing/fixtures";
import { doctorClean, setupState } from "../testing/fixtures_setup";
import Home from "./Home";

const health = { status: "ok", apiVersion: 1, version: "dev", buildId: "b1", workspaceId: "w1", startedAt: "2026-10-01T09:00:00.000Z" };

function healthy() {
  return fakeApi({
    "GET /api/tracker/summary": () => ok(summary),
    "GET /api/apply/list": () => ok(applyList),
    "GET /api/llm/routing": () => ok(routing),
    "GET /api/health": () => ok(health),
    "GET /api/setup/state": () => ok(setupState()),
    "POST /api/setup/doctor": () => ok(doctorClean),
    "GET /api/needs-you": () => ok({ items: [], unseen: 0 }),
    "GET /api/needs-you/counts": () => ok(noCounts),
    "GET /api/inbox": () => ok({ rows: [], page: 1, pageSize: 1, total: 0, counts: {} }),
    "GET /api/runs": () => ok({ entries: [] }),
    "GET /api/runs/summary": () => ok({ working: 0, needsYou: 0, blocked: 0, doneUnacknowledged: 0, unseen: 0 }),
    "GET /api/agent/confirmations": () => ok({ cards: [] }),
    "GET /api/agent/tasks": () => ok({ tasks: [] }),
    "GET /api/day": () => ok(day()),
    "GET /api/gmail/inbox": () => ok({ threads: [], total: 0 }),
    "GET /api/tracker/followups": () => ok({ suggestions: [] }),
    "GET /api/interviews/soon": () => ok({ items: [] }),
    "GET /api/discovery/funnel/week": () => ok({ since: "2026-10-16", current: stages, previous: stages }),
  });
}

const noCounts = { emailConfirmations: 0, wikiDrafts: 0, heldSignals: 0, suggestedAnswers: 0, answerEdits: 0 };

const stages = { found: 0, evaluated: 0, applied: 0, responded: 0, interview: 0, offer: 0 };

const day = (over: Record<string, unknown> = {}) => ({ date: "2026-10-01", did: [], left: [], employers: [], counts: { found: 0, evaluated: 0, ready: 0, appliedByUser: 0, appliedByAgent: 0, waiting: 0 }, ...over });

const HOUR = 3600 * 1000;
const at = (msAgo: number) => new Date(Date.now() - msAgo).toISOString();

describe("Home", () => {
  it("lists what needs the user from the data it has, and omits setup progress once setup is done", async () => {
    healthy();
    await mount(<Home />);
    await waitFor(() => text().includes("ready to apply"));
    expect(text()).toContain("1 posting is ready to apply");
    expect(text()).toContain("1 model task has no usable route");
    expect(text().toLowerCase()).not.toContain("setup progress");
  });

  it("hands an item over to the agent in one press, opens the task already on one, and lists a task's question", async () => {
    const api = healthy();
    api.on("GET /api/inbox", () => ok({ rows: [], page: 1, pageSize: 1, total: 4, counts: {} }));
    api.on("GET /api/tracker/followups", () => ok({ suggestions: [{ id: 7, company: "Hooli", role: "Engineer", version: 1, kind: "follow_up", due: "2026-10-01" }] }));
    const task = (over: Record<string, unknown>) => ({ id: "task_1", sessionId: "ses_1", title: "Draft the follow-ups that are due", mandate: "Draft them.", state: "working", steps: [], turns: 1, maxTurns: 20, createdAt: at(HOUR), updatedAt: at(HOUR), ...over });
    api.on("GET /api/agent/tasks", () => ok({ tasks: [
      task({ idea: "draft_followups" }),
      task({ id: "task_2", sessionId: "ses_2", title: "Find roles in Austin", state: "waiting_you", question: "Remote roles too?" }),
      task({ id: "task_3", sessionId: "ses_3", idea: "evaluate_inbox", state: "done" }),
    ] }));
    api.on("POST /api/agent/ideas/evaluate_inbox", () => ({ data: { sessionId: "ses_9", taskId: "task_9" }, status: 202 }));
    await mount(<Home />);
    const item = (t: string) => waitFor(() => qa("li").find((li) => li.textContent?.includes(t))!);
    // A task that stopped on a question asks from Needs you.
    const asks = await item("A task the agent works on asks you: Find roles in Austin");
    expect(asks.textContent).toContain("Remote roles too?");
    expect(asks.querySelector("a")?.getAttribute("href")).toBe("/ask?conversation=ses_2");
    // An open task on the follow-ups is opened, not started again; a done one
    // on the inbox leaves it to hand over anew.
    expect((await item("A follow-up to Hooli is due")).querySelector("button")?.textContent).toBe("The agent is on it");
    await click((await item("4 postings wait in the inbox")).querySelector("button")!);
    await waitFor(() => window.location.search === "?conversation=ses_9");
    expect(window.location.pathname).toBe("/ask");
    expect(api.callsTo("POST /api/agent/ideas/evaluate_inbox")).toHaveLength(1);
  });

  it("fails one panel at a time", async () => {
    const api = healthy();
    api.on("GET /api/tracker/summary", () => fail("internal", "The tracker is unavailable."));
    await mount(<Home />);
    await waitFor(() => text().includes("The tracker is unavailable."));
    expect(text()).toContain("Request ");
    await waitFor(() => text().includes("Apply now"));
    expect(byText("button", "Try again")).not.toBeNull();
  });

  it("evaluates a pasted posting URL and follows the run to its report", async () => {
    const api = healthy();
    let view: Record<string, unknown> = { runId: "run_5", status: "running", review: false };
    api.on("POST /api/evaluations", () => ok({ runId: "run_5" }));
    api.on("GET /api/evaluations/run_5", () => ok(view));
    const board = manualBoard();
    await mount(
      <BoardContext.Provider value={board}>
        <Home />
      </BoardContext.Provider>,
    );
    await act(async () => board.set({ runs: 1 }));
    const form = q("form[aria-label='Evaluate a posting']")!;
    const go = () => byText("button", "Evaluate") as HTMLButtonElement;
    expect(go().disabled).toBe(true);
    await type(form.querySelector("textarea"), "  https://jobs.example.com/northwind/1 ");
    await submit(form);
    const call = api.callsTo("POST /api/evaluations")[0];
    expect(call.body).toEqual({ url: "https://jobs.example.com/northwind/1" });
    expect(call.headers.get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => text().includes("Evaluation of the posting at jobs.example.com started."));
    expect(byText("a", "Follow the run")?.getAttribute("href")).toBe("/runs/run_5");
    expect(form.querySelector("textarea")!.value).toBe("");
    view = { runId: "run_5", status: "succeeded", outcome: "reported", reportNumber: 42, review: false };
    await act(async () => board.set({ runs: 2 }));
    await waitFor(() => byText("a", "Report 42")?.getAttribute("href") === "/reports?id=42");
  });

  it("sends pasted posting text as text and shows a refusal", async () => {
    const api = healthy();
    api.on("POST /api/evaluations", () => fail("validation_failed", "too long"));
    await mount(<Home />);
    const form = q("form[aria-label='Evaluate a posting']")!;
    await type(form.querySelector("textarea"), "Staff Engineer.\nhttps://jobs.example.com/1");
    await submit(form);
    expect(api.callsTo("POST /api/evaluations")[0].body).toEqual({ text: "Staff Engineer.\nhttps://jobs.example.com/1" });
    await waitFor(() => text().includes("too long"));
  });
});
