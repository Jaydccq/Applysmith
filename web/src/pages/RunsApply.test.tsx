import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { button, byLabel, byText, click, mount, press, q, qa, text, type, waitFor } from "../testing/dom";
import { candidacy } from "../testing/fixtures";
import Runs from "./Runs";

const stamp = "2026-10-12T09:00:00.000Z";

function entry(over: Record<string, unknown> = {}) {
  return {
    id: "run_a", kind: "apply", origin: "user", title: "Apply to Northwind Example", state: "needs-you", reason: "sign-in", link: "/runs/run_a", revision: 1,
    startedAt: stamp, updatedAt: stamp, stateAt: stamp, finishedAt: stamp, stage: "account", detail: "the saved password was refused", ...over,
  };
}

const record = (over: Record<string, unknown> = {}) => ({
  runId: "run_a", mode: "submit", caller: "dashboard", company: "Northwind Example", role: "Staff Engineer", host: "jobs.example.com", ats: "greenhouse",
  startedAt: stamp, finishedAt: stamp, pages: 2, keptTab: true, reportNumber: 7, candidacyId: 3,
  outcome: { kind: "stopped", stage: "account", class: "credential", reason: "the saved password was refused", owner: "user" },
  stages: [
    { stage: "preflight", status: "ok", detail: "browser ready", startedAt: stamp, ms: 420 },
    { stage: "fill", status: "ok", detail: "page 1 filled", startedAt: stamp, ms: 12400 },
    { stage: "account", status: "failed", detail: "sign-in refused", startedAt: stamp, ms: 75000 },
  ],
  ...over,
});

function server(rec: Record<string, unknown> = record(), extra: Record<string, () => ReturnType<typeof ok>> = {}, e = entry()) {
  window.history.pushState(null, "", "/runs/run_a");
  return fakeApi({
    "GET /api/runs": () => ok({ entries: [e] }),
    "GET /api/needs-you": () => ok({ items: [], unseen: 0 }),
    "GET /api/runs/run_a": () => ok({ runId: "run_a", kind: "apply", status: "failed", createdAt: stamp, updatedAt: stamp, entry: e }),
    "GET /api/apply/runs/run_a": () => ok(rec),
    ...extra,
  });
}

describe("Run detail of an application run", () => {
  it("opens the conversation of a run the Ask Agent started", async () => {
    const e = entry({ origin: "agent" });
    server(record(), { "GET /api/runs/run_a": () => ok({ runId: "run_a", kind: "apply", status: "failed", createdAt: stamp, updatedAt: stamp, entry: e, conversation: "ses_7" }) }, e);
    await mount(<Runs />);
    await waitFor(() => text().includes("The saved password was refused."));
    expect(q("dialog")!.textContent).toContain("Started byAsk Agent");
    const link = qa<HTMLAnchorElement>("dialog a").find((a) => a.textContent === "Open the conversation");
    expect(link?.getAttribute("href")).toBe("/ask?conversation=ses_7");
  });
});

describe("Needs you: the fix one click away", () => {
  const fix = { to: "/settings#settings-applications", label: "See the cooldown" };
  const gate = entry({ detail: "no sign-in is saved for careers.example.test; account creation is refused: a cooldown on careers.example.test is active", fix });
  const scan = {
    id: "scan-sign-in-linkedin", kind: "task", origin: "schedule", title: "Sign in for the linkedin scan", state: "needs-you", reason: "sign-in", link: "/browser?sync=linkedin.com",
    revision: 1, startedAt: stamp, updatedAt: stamp, stateAt: stamp, detail: "LinkedIn shows its sign-in page",
    fix: { to: "/browser?sync=linkedin.com", label: "Sync the sign-in from your Chrome", scan: "linkedin" },
  };

  it("links a stop the account creation gate refused to its setting, and a scan's sign-in to the session sync with the scan run again", async () => {
    window.history.pushState(null, "", "/runs");
    const api = fakeApi({
      "GET /api/runs": () => ok({ entries: [gate, scan] }),
      "GET /api/needs-you": () => ok({ items: [{ entry: gate, waitingSeconds: 60, action: "open-run" }, { entry: scan, waitingSeconds: 30, action: "open-run" }], unseen: 0 }),
      "POST /api/scans": () => ({ data: { runId: "run_s" }, status: 202 }),
    });
    await mount(<Runs />);
    const settings = await waitFor(() => byText<HTMLAnchorElement>("li a", fix.label));
    expect(settings.getAttribute("href")).toBe(fix.to);
    expect(byText("li a", "Resolve")).toBeNull();
    expect(byText("li a", scan.fix.label)?.getAttribute("href")).toBe(scan.fix.to);
    await click(button("Run the scan again"));
    await waitFor(() => api.callsTo("POST /api/scans").length === 1);
    expect(api.callsTo("POST /api/scans")[0].body).toEqual({ source: "linkedin" });
    expect(api.callsTo("POST /api/scans")[0].headers.get("Idempotency-Key")).not.toBeNull();
  });
});

describe("Run detail: retry", () => {
  const accepted = { runId: "run_b" };

  it("asks first, names the company, role and host, and retries without submitting", async () => {
    const api = server(record(), { "POST /api/apply/runs/run_a/retry": () => ok(accepted) });
    await mount(<Runs />);
    await click(await waitFor(() => byText("button", "Retry (fill only)")));
    const dialog = qa("dialog").at(-1)!;
    expect(dialog.querySelector("h2")?.textContent).toBe("Retry the application to Northwind Example");
    expect(dialog.textContent).toContain("fill the form for Staff Engineer at Northwind Example on jobs.example.com again. Nothing is submitted.");
    expect(document.activeElement?.textContent).toBe("Cancel");
    expect(api.callsTo("POST /api/apply/runs/run_a/retry")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Retry")!);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_a/retry").length === 1);
    expect(api.callsTo("POST /api/apply/runs/run_a/retry")[0].body).toEqual({ submit: false, confirmed: true });
    await waitFor(() => window.location.pathname === "/runs/run_b");
  });

  it("uses the submit-class dialog for a retry that submits, with the skip-list status", async () => {
    const api = server(record(), {
      "POST /api/apply/runs/run_a/retry": () => ok(accepted),
      "GET /api/apply/skip-status": () => ok({ company: "Northwind Example", listed: true, entry: "Northwind" }),
    });
    await mount(<Runs />);
    await click(await waitFor(() => byText("button", "Retry and submit")));
    const dialog = qa("dialog").at(-1)!;
    expect(dialog.querySelector("h2")?.textContent).toBe("Submit the application to Northwind Example again");
    expect(dialog.textContent).toContain("Staff Engineer at Northwind Example on jobs.example.com and press its final Submit button");
    expect(dialog.textContent).toContain("sends a real application and cannot be undone");
    await waitFor(() => dialog.textContent!.includes("is on your batch skip list (as “Northwind”)"));
    expect(api.callsTo("GET /api/apply/skip-status")[0].query.get("company")).toBe("Northwind Example");
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Retry and submit" && b.className === "danger")!);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_a/retry").length === 1);
    expect(api.callsTo("POST /api/apply/runs/run_a/retry")[0].body).toEqual({ submit: true, confirmed: true });
  });

  it("offers no retry that submits in a demo workspace, and says why", async () => {
    server(record({ mode: "fill" }), { "GET /api/setup/status": () => ok({ demo: true }) });
    await mount(<Runs />);
    await waitFor(() => text().includes("A demo workspace never presses Submit, so a retry here fills only."));
    expect(byText("button", "Retry (fill only)")).not.toBeNull();
    expect(button("Retry and submit")).toBeNull();
  });

  it("retries nothing when the dialog is closed with Escape", async () => {
    const api = server(record(), { "POST /api/apply/runs/run_a/retry": () => ok(accepted) });
    await mount(<Runs />);
    await click(await waitFor(() => byText("button", "Retry and submit")));
    await press("Escape", {}, qa("dialog").at(-1)!);
    await waitFor(() => qa("dialog").length === 1);
    // Escape closed the innermost dialog only; the run's drawer is still open.
    expect(window.location.pathname).toBe("/runs/run_a");
    expect(api.callsTo("POST /api/apply/runs/run_a/retry")).toHaveLength(0);
  });

  it("shows the server's refusal", async () => {
    server(record(), { "POST /api/apply/runs/run_a/retry": () => fail("conflict", "another application run holds the apply window") });
    await mount(<Runs />);
    await click(await waitFor(() => byText("button", "Retry (fill only)")));
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Retry")!);
    await waitFor(() => text().includes("another application run holds the apply window"));
  });

  it("offers no retry for a submitted application or a run still working", async () => {
    server(record({ outcome: { kind: "submitted", confirmed: true } }), {}, entry({ state: "done", outcome: "ok" }));
    await mount(<Runs />);
    await waitFor(() => text().includes("Submitted and confirmed."));
    expect(byText("button", "Retry (fill only)")).toBeNull();
    expect(button("Retry and submit")).toBeNull();
  });
});

describe("Run detail: batch", () => {
  const batch = {
    batchId: "batch_1", state: "closed", mode: "submit", caller: "agent", createdAt: stamp,
    rows: [
      { index: 1, company: "Northwind Example", role: "Staff Engineer", host: "jobs.example.com", state: "ended", runId: "run_a", mirrors: 1, outcome: { kind: "stopped", stage: "account", class: "credential", reason: "refused", owner: "user" } },
      { index: 2, company: "Contoso Example", role: "Backend Engineer", host: "jobs.example.org", state: "pending", note: "left for you: skip list", mirrors: 2 },
    ],
    owners: [{ owner: "user", companies: ["Northwind Example"] }, { owner: "defect", companies: ["Fabrikam Example", "Tailspin Example"] }],
    defects: [{ stage: "fill", class: "defect", reason: "select not found", companies: ["Fabrikam Example"] }],
  };

  it("shows the rows with state and note, the owner groups and the defects", async () => {
    const api = server(record({ batchId: "batch_1" }), { "GET /api/apply/batches/batch_1": () => ok(batch) });
    await mount(<Runs />);
    await waitFor(() => q("dialog")?.textContent?.includes("Who has the next step"));
    expect(api.callsTo("GET /api/apply/batches/batch_1")).toHaveLength(1);
    const rows = qa("dialog table").at(-1)!.querySelectorAll("tbody tr");
    expect(Array.from(rows[0].querySelectorAll("td")).map((td) => td.textContent)).toEqual(["Northwind Example", "Staff Engineer (+1 mirror)", "Finished", "Stopped while signing in (sign-in). Refused."]);
    expect(Array.from(rows[1].querySelectorAll("td")).map((td) => td.textContent)).toEqual(["Contoso Example", "Backend Engineer (+2 mirrors)", "Waiting its turn", "left for you: skip list"]);
    expect(qa("dialog a").find((a) => a.textContent === "Northwind Example")?.getAttribute("href")).toBe("/runs/run_a");
    const owners = qa("dialog ul[aria-label='Stopped rows by owner'] li").map((li) => li.textContent);
    expect(owners[0]).toContain("Waiting for you: Northwind Example");
    expect(owners[1]).toContain("Defects: Fabrikam Example, Tailspin Example");
    expect(q("dialog ul[aria-label='Defects of the batch']")?.textContent).toContain("fill (defect): select not found");
  });
});

describe("Run detail: correct this application", () => {
  const message = "Correct the application for Northwind Example: fill it in again with the right answers and submit it. https://jobs.example.com/apply/42";
  const submitted = { outcome: { kind: "submitted", confirmed: true } };

  it("offers the exact message the server accepts, to copy and send in Ask Agent, and sends nothing itself", async () => {
    const api = server(record({ ...submitted, correction: { message } }), {}, entry({ state: "done", outcome: "ok" }));
    await mount(<Runs />);
    const group = await waitFor(() => q('dialog [role="group"][aria-labelledby="correct-application"]'));
    expect(group.textContent).toContain("Correct this application");
    expect(group.querySelector("pre")?.textContent).toBe(message);
    const link = Array.from(group.querySelectorAll("a")).find((a) => a.textContent === "Open Ask Agent");
    expect(link?.getAttribute("href")).toBe("/ask");
    expect(byLabel(/^Copy the correction message$/)).not.toBeNull();
    expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
  });
});

describe("Run detail: Submit this form", () => {
  const filled = record({ mode: "dry-run", outcome: { kind: "filled", reason: "it was started to fill only" } });
  const waiting = entry({ reason: "confirmation", detail: "filled; Submit waits for your confirmation in the dashboard.", canFocusTab: true, focusTab: "tab-7", canSubmit: true });

  it("asks with the Autofill and submit facts, then submits the filled form from its kept tab", async () => {
    const api = server(filled, {
      "POST /api/apply/runs/run_a/submit": () => ok({ runId: "run_a" }),
      "GET /api/apply/skip-status": () => ok({ company: "Northwind Example", listed: false }),
    }, waiting);
    await mount(<Runs />);
    await click(await waitFor(() => byText("button", "Submit this form\u2026")));
    const dialog = qa("dialog").at(-1)!;
    expect(dialog.querySelector("h2")?.textContent).toBe("Submit the application to Northwind Example");
    expect(dialog.textContent).toContain("Staff Engineer at Northwind Example on jobs.example.com in the tab it kept, run every check again and press its final Submit button");
    expect(dialog.textContent).toContain("sends a real application and cannot be undone");
    await waitFor(() => dialog.textContent!.includes("Northwind Example is not on your batch skip list."));
    expect(api.callsTo("POST /api/apply/runs/run_a/submit")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Submit this form" && b.className === "danger")!);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_a/submit").length === 1);
    expect(api.callsTo("POST /api/apply/runs/run_a/submit")[0].body).toEqual({ confirmed: true });
    await waitFor(() => text().includes("The run checks the filled form in its kept tab again, then presses Submit."));
  });

  it("is not offered when the board entry does not offer it", async () => {
    server(filled, {}, entry({ reason: "confirmation", canFocusTab: true, focusTab: "tab-7" }));
    await mount(<Runs />);
    await waitFor(() => q("dialog") && byText("button", "Show tab"));
    expect(button("Submit this form\u2026")).toBeNull();
  });
});

describe("Run detail: answer the questions", () => {
  const outcome = { kind: "stopped", stage: "check", class: "answer-gap", reason: "2 required answers are missing", owner: "user" };
  const schools = Array.from({ length: 8 }, (_, i) => `School ${i + 1}`);
  // everywhere is the server's starting choice: every site unless the question
  // names the company (F-4).
  const questions = [
    { question: "Which office would you join?", kind: "radio-group", options: ["Remote", "On-site"], everywhere: true },
    { question: "Where did you study?", kind: "select", options: schools, everywhere: true },
    { question: "Why Northwind?", kind: "textarea", everywhere: false },
  ];
  const stoppedOn = (qs: object[], over: Record<string, unknown> = {}) => record({ outcome, questions: qs, ...over });

  it("offers each question with the form's options, saves an answer as the user's own entry, then retries in the run's mode when no tab is kept", async () => {
    let answered = false;
    const gone = { keptTab: false };
    const api = server(stoppedOn(questions, gone), {
      "GET /api/apply/runs/run_a": () => ok(stoppedOn(answered ? [{ ...questions[0], answeredBy: "custom-office-1", answer: "Remote" }, ...questions.slice(1)] : questions, gone)),
      "POST /api/apply/runs/run_a/answers": () => {
        answered = true;
        return ok({ entryId: "custom-office-1" });
      },
      "GET /api/apply/skip-status": () => ok({ company: "Northwind Example", listed: false }),
    });
    await mount(<Runs />);
    const section = await waitFor(() => q("dialog section[aria-labelledby='run-answers']"));
    expect(section.querySelectorAll("input[type='radio']")).toHaveLength(2);
    expect(Array.from(section.querySelectorAll("select option")).map((o) => o.textContent)).toEqual(["Choose an option", ...schools]);
    expect(section.textContent).toContain("3 of 3 still open.");
    const save = () => Array.from(section.querySelectorAll<HTMLButtonElement>("button[type='submit']"));
    expect(save().every((b) => b.disabled)).toBe(true);
    await click(section.querySelector("input[type='radio']"));
    expect(section.textContent).toContain("Otherwise it answers this question on jobs.example.com only.");
    await click(save()[0]);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_a/answers").length === 1);
    expect(api.callsTo("POST /api/apply/runs/run_a/answers")[0].body).toEqual({ question: "Which office would you join?", answer: "Remote", everywhere: true });
    await waitFor(() => section.textContent!.includes("Your Answer Bank answers this now: Remote."));
    expect(section.textContent).toContain("2 of 3 still open.");
    // The retry keeps the run's mode: this run submits, so the submit dialog asks.
    await click(byText("button", "Retry with these answers"));
    expect(qa("dialog").at(-1)!.querySelector("h2")?.textContent).toBe("Submit the application to Northwind Example again");
  });

  it("offers to keep a value you changed by hand: one press makes it the entry's answer, and the offer goes", async () => {
    let kept = false;
    const edit = { fillId: 41, entryId: "home-city", host: "jobs.example.com", question: "Town you live in", before: "Durham", after: "Raleigh" };
    const route = "POST /api/answer-bank/manual-fills/41/keep";
    const api = server(record(), {
      "GET /api/apply/runs/run_a": () => ok(record(kept ? {} : { edits: [edit] })),
      [route]: () => {
        kept = true;
        return ok({ revision: "r2", changed: true });
      },
    });
    await mount(<Runs />);
    await waitFor(() => text().includes("You changed Town you live in from Durham to Raleigh. Use Raleigh from now on?"));
    await click(byText("dialog button", "Use Raleigh"));
    await waitFor(() => text().includes("Town you live in is answered Raleigh from now on."));
    expect(api.callsTo(route)).toHaveLength(1);
    expect(text()).not.toContain("from Durham to Raleigh");
  });

  describe("Draft with evidence", () => {
    const essay = { ...questions[2], draftable: true };
    const box = () => q<HTMLTextAreaElement>("dialog section[aria-labelledby='run-answers'] textarea")!;

    it("puts the draft in the answer box with the facts it rests on, rewrites what is in the box, and saves nothing", async () => {
      let n = 0;
      const api = server(stoppedOn([essay]), {
        "POST /api/answers/compose": () => ok(n++ === 0 ? { outcome: "filled-model", answer: "I build tools for teams.", sources: ["cv.md#Experience", "profile#employment-2", "profile.yml#skills", "answer-bank#e7", "knowledge/projects/reef-survey.md#Reef survey", "x#y"] } : { outcome: "filled-model", answer: "I build tools.", sources: ["cv.md#Experience"] }),
      });
      await mount(<Runs />);
      await waitFor(() => box());
      await click(byText("button", "Draft with evidence"));
      await waitFor(() => box().value === "I build tools for teams.");
      expect(text()).toContain("Written from: your CV (Experience), your profile (Employment), your profile (Skills), your Answer Bank, your notes (Reef survey), x#y.");
      expect(api.callsTo("POST /api/answers/compose")[0].body).toEqual({ question: "Why Northwind?", host: "jobs.example.com", posting: { company: "Northwind Example", role: "Staff Engineer" } });
      await type(box(), "I build tools for teams. Edited.");
      await type(q<HTMLInputElement>("input[aria-label='How to change the draft']"), "shorter");
      await click(byText("button", "Rewrite"));
      await waitFor(() => box().value === "I build tools.");
      expect(api.callsTo("POST /api/answers/compose")[1].body).toMatchObject({ current: "I build tools for teams. Edited.", instruction: "shorter" });
      expect(api.callsTo("POST /api/apply/runs/run_a/answers")).toHaveLength(0);
    });
  });

  // The refused answer is still not saved, so closing the record asks first (useUnsaved).
  it("types a free answer, shows the server's refusal beside the question, and asks before the answer is left", async () => {
    const api = server(stoppedOn([questions[2]]), {
      "POST /api/apply/runs/run_a/answers": () => fail("conflict", "entry custom-team already answers this question"),
    });
    await mount(<Runs />);
    const box = await waitFor(() => q<HTMLTextAreaElement>("dialog section[aria-labelledby='run-answers'] textarea"));
    await type(box, "  I like the mission  ");
    await click(byText("button", "Save to Answer Bank"));
    await waitFor(() => text().includes("entry custom-team already answers this question"));
    expect(api.callsTo("POST /api/apply/runs/run_a/answers")[0].body).toEqual({ question: "Why Northwind?", answer: "I like the mission", everywhere: false });
    await click(q("dialog button[aria-label='Close']"));
    await waitFor(() => text().includes("Leave without saving?"));
  });
});

describe("Run detail: files the form asks for", () => {
  const accepted = { runId: "run_b" };
  const outcome = { kind: "stopped", stage: "check", class: "answer-gap", reason: "1 required answer is missing", owner: "user" };
  const stopped = (over: Record<string, unknown>) => record({ outcome, mode: "dry-run", uploads: ["Cover letter", "Transcript"], ...over });
  const retryButton = () => qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Retry")!;

  it("names the upload slots no question carries and, below the floor, records the override before the retry", async () => {
    const order: string[] = [];
    const api = server(stopped({ letterOverride: true }), {
      "POST /api/candidacies/3/documents-override": () => (order.push("override"), ok(candidacy({ id: 3, reportNumber: 7 }))),
      "POST /api/apply/runs/run_a/retry": () => (order.push("retry"), ok(accepted)),
    });
    await mount(<Runs />);
    const section = await waitFor(() => q("dialog section[aria-labelledby='run-uploads']"));
    expect(Array.from(section.querySelectorAll("li")).map((l) => l.textContent)).toEqual(["Cover letter", "Transcript"]);
    expect(section.textContent).toContain("A file is not an Answer Bank answer, so add it in the tab.");
    await click(byText("button", "Allow a tailored cover letter and retry"));
    const dialog = qa("dialog").at(-1)!;
    expect(dialog.querySelector("h2")?.textContent).toBe("Retry the application to Northwind Example");
    expect(dialog.textContent).toContain("Your override of the 4.5 floor is recorded for this row");
    expect(order).toEqual([]);
    await click(retryButton());
    await waitFor(() => order.length === 2);
    expect(order).toEqual(["override", "retry"]);
    expect(api.callsTo("POST /api/apply/runs/run_a/retry")[0].body).toEqual({ submit: false, confirmed: true });
  });

  it("offers no override when the server does not, and a cancelled one is not carried into a plain retry", async () => {
    const api = server(stopped({ letterOverride: false }), { "POST /api/apply/runs/run_a/retry": () => ok(accepted) });
    await mount(<Runs />);
    await waitFor(() => q("dialog section[aria-labelledby='run-uploads']"));
    expect(byText("button", "Allow a tailored cover letter and retry")).toBeNull();
    api.on("GET /api/apply/runs/run_a", () => ok(stopped({ letterOverride: true })));
    await mount(<Runs />);
    await click(await waitFor(() => byText("button", "Allow a tailored cover letter and retry")));
    await press("Escape", {}, qa("dialog").at(-1)!);
    await waitFor(() => qa("dialog").length === 1);
    await click(byText("button", "Retry (fill only)"));
    expect(qa("dialog").at(-1)!.textContent).not.toContain("Your override");
    await click(retryButton());
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_a/retry").length === 1);
    expect(api.callsTo("POST /api/candidacies/3/documents-override")).toHaveLength(0);
  });
});
