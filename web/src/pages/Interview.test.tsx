import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../testing/api";
import { button, click, mount, q, qa, text, type, waitFor } from "../testing/dom";
import { candidacy, detail, documentRun } from "../testing/fixtures";
import type { DocumentRunView } from "../wire";
import { ics, InterviewDrawer } from "./Interview";

const T = "2026-10-12T09:00:00.000Z";

const stage = (over: Record<string, unknown> = {}) => ({ id: "tech-1", type: "technical", format: "video", interviewers: ["Sam Sample"], userConfirmed: true, revision: 3, recordedAt: T, ...over });

const workspace = (over: Record<string, unknown> = {}) => ({
  candidacyId: 1, stages: [], cancelled: [], feedback: [], mockTurns: [], packs: [], degraded: false, nextActions: [{ text: "Add an interview stage to start preparing.", kind: "stage" }], ...over,
});

const pack = (over: Record<string, unknown> = {}) => ({
  candidacyId: 1, stage: "tech-1", revision: 1, generation: "deterministic", createdAt: T, roleBrief: "Backend Engineer at Example Corp.", companyBrief: "Example Corp builds scheduling software.",
  hooks: [{ text: "Example Corp builds scheduling software.", sources: ["knowledge/companies/example.com.md"] }],
  claims: ["Led the platform migration and cut incidents by 40%"],
  questions: [
    { category: "depth", prompt: "Explain in depth a technical area you know best.", storyIds: [], sources: [] },
    { category: "requirement", prompt: "Describe your experience with: Go services", storyIds: ["alerts-ownership"], sources: ["story:alerts-ownership"] },
  ],
  priorFeedback: [], gaps: [{ text: "No story in your story bank covers the requirement \"PostgreSQL\". Mine a story for it.", kind: "story", target: "PostgreSQL" }],
  nextActions: [{ text: "Practice the likely questions with mock turns.", kind: "practice", target: "tech-1" }],
  sources: [
    { kind: "company-brief", ref: "knowledge/companies/example.com.md", label: "Company brief: example.com", state: "frozen" },
    { kind: "story", ref: "story:alerts-ownership", label: "Cutting alert noise", state: "user-confirmed" },
    { kind: "dossier", ref: "documents/x/resume.md", label: "Submitted résumé, version 1", state: "current" },
  ],
  warnings: [], ...over,
});

// ended answers a run's event stream with its final event.
const ended = (runId: string, final: object) => new Response(`id: e-1\nevent: final\ndata: ${JSON.stringify({ runId, ...final })}\n\n`);

const run = (over: Partial<DocumentRunView> = {}) => documentRun({ pack: { candidacyId: 1, stage: "tech-1", revision: 1 }, ...over });

async function open() {
  await mount(<InterviewDrawer candidacyId={1} label="Example Corp, Backend Engineer" onClose={() => {}} />);
  await waitFor(() => !text().includes("Loading"));
}

function server(ws: () => unknown) {
  return fakeApi({
    "GET /api/candidacies/1/interview": () => ok(ws()),
    "GET /api/kb/documents": () => ok({ entries: [{ id: "alerts-ownership", kind: "snippet", path: "knowledge/behavioral/alerts-ownership.md", category: "behavioral", title: "Cutting alert noise", description: "", modifiedAt: T, fileHash: "h", tags: [], principles: ["ownership"], sourceType: "work", sourceUrl: "", lint: [] }], uses: [] }),
  });
}

describe("InterviewDrawer", () => {
  it("adds a stage with the id, type and confirmation it was given", async () => {
    const api = server(() => workspace());
    api.on("POST /api/candidacies/1/interview/stages", () => ({ data: stage(), status: 201 }));
    await open();
    const form = q("form[aria-label='Add a stage']")!;
    const input = (label: string) => qa<HTMLInputElement | HTMLSelectElement>("input, select").filter((e) => form.contains(e)).find((e) => e.labels?.[0]?.textContent?.startsWith(label))!;
    await type(input("Stage name"), "!");
    expect(button("Add stage")?.disabled).toBe(true);
    await type(input("Stage name"), "Tech 1");
    await click(q<HTMLInputElement>("form[aria-label='Add a stage'] input[type=checkbox]")!);
    await type(input("Interviewers"), "Sam Sample, Pat Placeholder");
    await click(button("Add stage"));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/interview/stages").length === 1);
    expect(api.callsTo("POST /api/candidacies/1/interview/stages")[0].body).toEqual({
      id: "tech-1", type: "technical", format: "video", userConfirmed: true, expectedRevision: 0, interviewers: ["Sam Sample", "Pat Placeholder"],
    });
    expect(document.activeElement?.textContent).toBe("Add a stage"); // the form folds away once a stage exists
  });

  // The stage service refuses an id that is not 2 to 64 lowercase letters,
  // digits and hyphens (interview/service.go), so the type's own value
  // (system_design) cannot be the name of an unnamed stage.
  it("adds a stage left unnamed under an id the stage service accepts", async () => {
    const api = server(() => workspace());
    api.on("POST /api/candidacies/1/interview/stages", () => ({ data: stage(), status: 201 }));
    await open();
    await type(q<HTMLSelectElement>("form[aria-label='Add a stage'] select"), "system_design");
    await click(button("Add stage"));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/interview/stages").length === 1);
    expect((api.callsTo("POST /api/candidacies/1/interview/stages")[0].body as { id: string }).id).toMatch(/^[a-z0-9-]{2,64}$/);
  });

  // A stage proposed from a mail stays unconfirmed until the user confirms it
  // (specs/application-documents, Interview stages); recording the same id
  // again is the confirmation (RecordStage). The time must come back as the
  // same instant through the local date-and-time field.
  it("confirms a proposed stage by recording its details again", async () => {
    const at = "2026-10-23T14:30:00.000Z";
    const api = server(() => workspace({ stages: [stage({ userConfirmed: false, at })], nextActions: [] }));
    api.on("POST /api/candidacies/1/interview/stages", () => ({ data: stage({ at }), status: 201 }));
    await open();
    await click(button("Confirm details"));
    await click(button("Save details"));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/interview/stages").length === 1);
    expect(api.callsTo("POST /api/candidacies/1/interview/stages")[0].body).toEqual({
      id: "tech-1", type: "technical", format: "video", userConfirmed: true, expectedRevision: 3, at, interviewers: ["Sam Sample"],
    });
  });

  // The API refuses a stage recorded since the revision quoted.
  it("cancels a stage against the revision it read and takes the cancel back", async () => {
    let cancelled = false;
    const api = server(() => (cancelled ? workspace({ cancelled: [stage({ cancelled, revision: 4 })] }) : workspace({ stages: [stage()], nextActions: [] })));
    api.on("POST /api/candidacies/1/interview/stages", (req) => {
      cancelled = (req.body as { cancelled: boolean }).cancelled;
      return { data: stage({ cancelled, revision: cancelled ? 4 : 5 }), status: 201 };
    });
    await open();
    await click(button("Cancel stage"));
    await waitFor(() => text().includes("Cancelled stages (1)"));
    expect(q("section[aria-label='Stage tech-1']")).toBeNull();
    await click(button("Undo"));
    await waitFor(() => q("section[aria-label='Stage tech-1']") !== null);
    const sent = api.callsTo("POST /api/candidacies/1/interview/stages").map((c) => c.body as { cancelled: boolean; expectedRevision: number });
    expect(sent.map((b) => [b.cancelled, b.expectedRevision])).toEqual([[true, 3], [false, 4]]);
  });

  // RFC 5545: TEXT escapes (3.3.11), CRLF ends and 75-octet folds (3.1).
  it("writes a calendar event a calendar reads back unchanged", () => {
    const long = "Room 4, floor 2; ask at the desk\n" + "\u00e9".repeat(60);
    const out = ics(1, stage({ at: "2026-10-23T14:30:00.000Z" }), "Example Corp, Backend Engineer: Technical", long, "https://example.test/jobs/1", new Date(T));
    expect(out.endsWith("\r\n") && !/[^\r]\n|\r[^\n]/.test(out)).toBe(true);
    for (const line of out.split("\r\n")) expect(new TextEncoder().encode(line).length).toBeLessThanOrEqual(75);
    const lines = out.replace(/\r\n[ \t]/g, "").split("\r\n");
    expect(lines).toContain("SUMMARY:Example Corp\\, Backend Engineer: Technical");
    expect(lines).toContain("DESCRIPTION:Room 4\\, floor 2\\; ask at the desk\\n" + "\u00e9".repeat(60));
    expect(lines).toContain("DTSTART:20261023T143000Z");
    const uid = (v: string) => v.split("\r\n").find((l) => l.startsWith("UID:"));
    expect(uid(ics(1, stage({ at: T }), "", "", "", new Date(0)))).toBe(uid(out));
  });

  // A status change quotes the row version read (StatusChangeRequest), so a
  // change made elsewhere since is refused instead of overwritten.
  it("moves an applied application to Interview against the version it read", async () => {
    const api = server(() => workspace({ stages: [stage()], nextActions: [] }));
    api.on("GET /api/candidacies/1", () => ok(detail(candidacy({ status: "applied", version: 4 }))));
    api.on("PATCH /api/candidacies/1", () => ok({ entry: candidacy({ status: "interview", version: 5 }), changed: true, correction: false, inferred: false }));
    await open();
    await click(await waitFor(() => button("Move it to Interview")));
    await waitFor(() => api.callsTo("PATCH /api/candidacies/1").length === 1);
    expect(api.callsTo("PATCH /api/candidacies/1")[0].body).toEqual({ status: "interview", expectedVersion: 4 });
  });

  it("asks before Close drops a debrief not recorded yet", async () => {
    let closed = 0;
    server(() => workspace({ stages: [stage()], nextActions: [] }));
    await mount(<InterviewDrawer candidacyId={1} label="Example Corp, Backend Engineer" onClose={() => closed++} />);
    const form = await waitFor(() => q("form[aria-label='Record feedback']"));
    await type(form.querySelector("textarea"), "They asked about queues.");
    const closeButtons = () => qa<HTMLButtonElement>("button").filter((b) => b.textContent === "Close");
    await click(closeButtons()[0]);
    expect(closed).toBe(0);
    await click(await waitFor(() => button("Leave")));
    expect(closed).toBe(1);
  });

  it("asks before Cancel stage drops a debrief not recorded yet", async () => {
    const api = server(() => workspace({ stages: [stage({ at: "2999-01-01T09:00:00.000Z" }), stage({ id: "onsite-1", at: "2999-02-01T09:00:00.000Z" })], nextActions: [] }));
    api.on("POST /api/candidacies/1/interview/stages", () => ({ data: stage({ cancelled: true, revision: 4 }), status: 201 }));
    await open();
    const debrief = () => q<HTMLTextAreaElement>("form[aria-label='Record feedback'] textarea")!;
    await type(debrief(), "They asked about queues.");
    await click(button("Cancel stage"));
    await waitFor(() => button("Leave"));
    expect(api.callsTo("POST /api/candidacies/1/interview/stages")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").filter((b) => b.textContent === "Cancel").at(-1)!);
    expect(debrief().value).toBe("They asked about queues.");
    expect(api.callsTo("POST /api/candidacies/1/interview/stages")).toHaveLength(0);
    await click(button("Cancel stage"));
    await click(await waitFor(() => button("Leave")));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/interview/stages").length === 1);
  });

  it("builds a pack: starts the run with an idempotency key, follows it and shows the pack with its sources", async () => {
    let built = false;
    const api = server(() => workspace({ stages: [stage()], packs: built ? [{ stage: "tech-1", revision: 1, generation: "deterministic", createdAt: T }] : [] }));
    api.on("POST /api/candidacies/1/interview/stages/tech-1/pack", () => {
      built = true;
      return { data: { runId: "run-1" }, status: 202 };
    });
    api.on("GET /api/documents/runs/run-1", () => ok(run()));
    api.on("GET /api/candidacies/1/interview/stages/tech-1/pack", () => ok(pack()));
    await open();
    await click(button("Generate prep pack"));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/interview/stages/tech-1/pack").length === 1);
    expect(api.callsTo("POST /api/candidacies/1/interview/stages/tech-1/pack")[0].headers.get("Idempotency-Key")).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
    await waitFor(() => text().includes("Likely questions"));
    expect(text()).toContain("Backend Engineer at Example Corp.");
    expect(text()).toContain("Explain in depth a technical area you know best.");
    expect(text()).toContain("Your stories: Cutting alert noise");
    expect(text()).toContain("Mine a story for it.");
    expect(text()).toContain("Frozen at submission");
    expect(text()).toContain("Confirmed by you");
    expect(text()).toContain("Current version");
    expect(button("Generate a new revision")).not.toBeNull();
  });

  it("says why a pack run failed", async () => {
    const api = server(() => workspace({ stages: [stage()] }));
    api.on("POST /api/candidacies/1/interview/stages/tech-1/pack", () => ({ data: { runId: "run-2" }, status: 202 }));
    api.on("GET /api/documents/runs/run-2", () => ok(run({ runId: "run-2", status: "failed", reason: "timeout", pack: undefined })));
    await open();
    await click(button("Generate prep pack"));
    await waitFor(() => text().includes("The pack was not built (it took too long). Earlier revisions are unchanged."));
  });

  it("records feedback for the stage", async () => {
    const api = server(() => workspace({ stages: [stage()], packs: [{ stage: "tech-1", revision: 1, generation: "deterministic", createdAt: T }] }));
    api.on("GET /api/candidacies/1/interview/stages/tech-1/pack", () => ok(pack()));
    api.on("POST /api/candidacies/1/interview/stages/tech-1/feedback", () => ({ data: { id: 5, stage: "tech-1", from: "interviewer", text: "Strong on migrations.", recordedAt: T }, status: 201 }));
    await open();
    const form = await waitFor(() => q("form[aria-label='Record feedback']"));
    expect(button("Record feedback")?.disabled).toBe(true);
    await type(qa<HTMLSelectElement>("select").find((s) => form.contains(s))!, "interviewer");
    await type(form.querySelector("textarea"), "Strong on migrations.");
    await click(button("Record feedback"));
    await waitFor(() => api.callsTo("POST /api/candidacies/1/interview/stages/tech-1/feedback").length === 1);
    expect(api.callsTo("POST /api/candidacies/1/interview/stages/tech-1/feedback")[0].body).toEqual({ from: "interviewer", text: "Strong on migrations." });
  });

  it("practices an answer and shows the coaching it was given", async () => {
    const turn = { id: 9, stage: "tech-1", question: "Explain in depth a technical area you know best.", answer: "I led 40 engineers.", coaching: "- No story of yours backs the number 40, and your submitted résumé does not state it.", recordedAt: T };
    let coached = false;
    const api = server(() => workspace({ stages: [stage()], mockTurns: coached ? [turn] : [], packs: [{ stage: "tech-1", revision: 1, generation: "deterministic", createdAt: T }] }));
    api.on("GET /api/candidacies/1/interview/stages/tech-1/pack", () => ok(pack()));
    api.on("POST /api/candidacies/1/interview/stages/tech-1/mock-turns", () => ({ data: { runId: "run-c" }, status: 202 }));
    api.on("GET /api/runs/run-c/events", () => ((coached = true), ended("run-c", { status: "succeeded", outcome: "" })));
    await open();
    const form = await waitFor(() => q("form[aria-label='Practice an answer form']"));
    expect(button("Get coaching")?.disabled).toBe(true);
    await type(form.querySelector("input"), "Explain in depth a technical area you know best.");
    await type(form.querySelector("textarea"), "I led 40 engineers.");
    await click(button("Get coaching"));
    await waitFor(() => q("[aria-label='Coaching']"));
    expect(q("[aria-label='Coaching']")!.textContent).toContain("No story of yours backs the number 40");
    expect(api.callsTo("POST /api/candidacies/1/interview/stages/tech-1/mock-turns")[0].body).toEqual({
      question: "Explain in depth a technical area you know best.", answer: "I led 40 engineers.",
    });
    // The practised answer can start a story interview (KBMiningStart: a quadrant and notes).
    api.on("POST /api/kb/stories/mining", () => ok({ id: "m1", quadrant: "project", state: "collecting", fields: {}, log: [], createdAt: T, updatedAt: T }));
    await click(button("Turn this answer into a story"));
    await waitFor(() => api.callsTo("POST /api/kb/stories/mining").length === 1);
    const start = api.callsTo("POST /api/kb/stories/mining")[0].body as { quadrant: string; notes: string };
    expect(start.quadrant).toBe("project");
    expect(start.notes).toContain("I led 40 engineers.");
  });

  // Coaching runs on when the drawer closes: reopened, the drawer follows the
  // run again with its answer, and keeps the answer when the run fails.
  it("follows a coaching still working with its answer, and keeps the answer when it fails", async () => {
    const api = server(() => workspace({ stages: [stage(), stage({ id: "final-1", at: "2999-01-01" })], coachRun: { runId: "run-c", stage: "tech-1", question: "Why us?", answer: "Because of the product." } }));
    api.on("GET /api/runs/run-c/events", () => ended("run-c", { status: "failed", reason: "interrupted" }));
    await open();
    await waitFor(() => text().includes("The answer was not coached"));
    const form = q("form[aria-label='Practice an answer form']")!;
    expect(form.querySelector("input")!.value).toBe("Why us?");
    expect(form.querySelector("textarea")!.value).toBe("Because of the product.");
    expect(button("Get coaching")?.disabled).toBe(false);
  });
});
