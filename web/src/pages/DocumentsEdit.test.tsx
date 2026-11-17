import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { button, click, mount, press, q, qa, text, type, waitFor } from "../testing/dom";
import { candidacy, documentList, documentRun, documentSource, documentView } from "../testing/fixtures";
import type { DocumentRunView } from "../wire";
import { DocumentsDrawer } from "./Documents";

const run = (over: Partial<DocumentRunView> = {}) => documentRun({ documents: [13], ...over });
const field = (label: string) => qa("label").find((l) => l.textContent?.startsWith(label))?.querySelector("textarea") ?? null;
// inConfirm is a button of the "Leave without saving?" dialog.
const inConfirm = (label: string) => [...qa("dialog").at(-1)!.querySelectorAll("button")].find((b) => b.textContent === label)!;
const headings = () => qa("section h3").map((h) => h.textContent);

async function open() {
  await mount(<DocumentsDrawer candidacyId={1} label="Northwind Example, Staff Engineer" onClose={() => {}} />);
  await waitFor(() => q("section"));
}

// A server whose list grows when a run adds a version, as the real one does.
function server(initial = [documentView()]) {
  let docs = initial;
  const api = fakeApi({ "GET /api/documents": () => ok(documentList(...docs)) });
  return { api, add: (...d: ReturnType<typeof documentView>[]) => void (docs = [...d, ...docs]) };
}

const draft = (over: Partial<ReturnType<typeof documentView>> = {}) =>
  documentView({ id: 13, version: 3, pdfState: "none", pdfPath: undefined, pdfSha256: undefined, pages: undefined, ready: false, verification: undefined, warnings: ["Edited from version 2."], ...over });

async function editing(api: ReturnType<typeof server>["api"]) {
  api.on("GET /api/documents/source/11", () => ok(documentSource()));
  await open();
  await click(button("Show text"));
  await waitFor(() => q("pre"));
}

describe("document text", () => {
  it("does not let a version attached to a submitted application be edited", async () => {
    const { api } = server([documentView({ frozen: true })]);
    api.on("GET /api/documents/source/11", () => ok(documentSource({ frozen: true })));
    await open();
    await click(button("Show text"));
    await waitFor(() => q("pre"));
    expect(button("Edit")!.disabled).toBe(true);
    expect(text()).toContain("cannot be edited");
  });

  it("saves an edit as a new draft revision whose checks are pending and leaves the edited version as it was", async () => {
    const { api, add } = server();
    api.on("POST /api/documents/11/edit", () => {
      add(draft());
      return { data: { runId: "run-1" }, status: 202 };
    });
    api.on("GET /api/documents/runs/run-1", () => ok(run()));
    await editing(api);
    await click(button("Edit"));
    expect(button("Save as new draft")!.disabled).toBe(true);
    await type(field("Experience 1, bullet 2"), "Mentored four engineers through code review");
    await click(button("Save as new draft"));
    await waitFor(() => headings().length === 2);

    const post = api.callsTo("POST /api/documents/11/edit")[0];
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
    const sent = (post.body as { resume: { experience: { bullets: { text: string; evidence: string[] }[] }[] } }).resume;
    expect(sent.experience[0].bullets[1]).toEqual({ text: "Mentored four engineers through code review", evidence: ["E4"] });
    expect(sent.experience[0].bullets[0]).toEqual({ text: "Led the platform migration", evidence: ["E3"] });

    expect(headings()).toEqual(["Résumé, version 3", "Résumé, version 2"]);
    const [fresh, old] = qa("section");
    expect(fresh.textContent).toContain("No PDF yet");
    expect(fresh.textContent).toContain("Checks are pending");
    expect(fresh.textContent).toContain("Edited from version 2.");
    expect(fresh.textContent).not.toContain("Checks passed");
    expect(old.textContent).toContain("Checks passed");
  });

  it("shows the refusal of an edit the sources do not support and stores nothing", async () => {
    const { api } = server();
    api.on("POST /api/documents/11/edit", () => fail("validation_failed", "documents: no draft passed the grounding checks: unsupported-figure"));
    await editing(api);
    await click(button("Edit"));
    await type(field("Experience 1, bullet 1"), "Led the platform migration and cut incidents by 90%");
    await click(button("Save as new draft"));
    await waitFor(() => text().includes("unsupported-figure"));
    expect(api.callsTo("POST /api/documents/11/edit")).toHaveLength(1);
    expect(api.calls.filter((c) => c.path.startsWith("/api/documents/runs/"))).toEqual([]);
    expect(headings()).toEqual(["Résumé, version 2"]);
    // No run is followed: the edit can be sent again at once.
    expect(button("Save as new draft")!.disabled).toBe(false);
  });

  // A run can end without a version after the request was accepted (the stored
  // text changed under it); the typed text must survive that.
  it("keeps the typed text in the editor when the run ends without a new version, and Cancel closes it", async () => {
    const { api } = server();
    api.on("POST /api/documents/11/edit", () => ({ data: { runId: "run-1" }, status: 202 }));
    api.on("GET /api/documents/runs/run-1", () => ok(run({ status: "failed", reason: "stale-source", documents: [] })));
    await editing(api);
    await click(button("Edit"));
    await type(field("Experience 1, bullet 2"), "Mentored four engineers through code review");
    await click(button("Save as new draft"));
    await waitFor(() => text().includes("The edit was not saved"));
    expect(field("Experience 1, bullet 2")!.value).toBe("Mentored four engineers through code review");
    expect(button("Save as new draft")!.disabled).toBe(false);
    await click(button("Cancel"));
    await click(inConfirm("Leave"));
    expect(field("Experience 1, bullet 2")).toBeNull();
    expect(q("pre")).not.toBeNull();
  });

  it("asks before Escape or Close drops unsaved text, and keeps it when the user stays", async () => {
    const { api } = server();
    api.on("GET /api/documents/source/11", () => ok(documentSource()));
    const onClose = vi.fn();
    await mount(<DocumentsDrawer candidacyId={1} label="Northwind Example, Staff Engineer" onClose={onClose} />);
    await click(await waitFor(() => button("Show text")));
    await click(await waitFor(() => button("Edit")));
    await type(field("Profile"), "Changed.");
    await press("Escape", {}, field("Profile")!);
    await click(inConfirm("Cancel"));
    expect(onClose).not.toHaveBeenCalled();
    expect(field("Profile")!.value).toBe("Changed.");
    await click(qa<HTMLButtonElement>("button").filter((b) => b.textContent?.trim() === "Close").at(-1)!);
    await click(inConfirm("Leave"));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("regenerating and making the PDF", () => {
  it("asks for the pair of the application with both PDFs and shows the new version when the run succeeds", async () => {
    const { api, add } = server();
    api.on("POST /api/documents/resume", () => {
      add(draft({ warnings: [] }));
      return { data: { runId: "run-1" }, status: 202 };
    });
    api.on("GET /api/documents/runs/run-1", () => ok(run()));
    await open();
    await click(button("Regenerate résumé and cover letter"));
    await waitFor(() => headings().length === 2);
    const post = api.callsTo("POST /api/documents/resume")[0];
    expect(post.body).toEqual({ candidacyId: 1, includeCoverLetter: true, pdf: true });
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(text()).not.toContain("was not made");
  });

  it("offers Allow a PDF anyway when the pair is refused below the score floor, then asks again", async () => {
    const { api } = server();
    let refused = true;
    api.on("POST /api/documents/resume", () =>
      refused ? fail("validation_failed", "score 3.9 is below the 4.5 floor", { details: { reason: "below-score-floor" } }) : { data: { runId: "run-1" }, status: 202 },
    );
    api.on("POST /api/candidacies/1/documents-override", () => ((refused = false), ok(candidacy({ reportNumber: 7 }))));
    api.on("GET /api/documents/runs/run-1", () => ok(run({ status: "running", stage: "draft", documents: [] })));
    await open();
    expect(button("Allow a PDF anyway")).toBeNull();
    await click(button("Regenerate résumé and cover letter"));
    await click(await waitFor(() => button("Allow a PDF anyway")));
    await waitFor(() => api.callsTo("POST /api/documents/resume").length === 2);
    expect(api.callsTo("POST /api/candidacies/1/documents-override")).toHaveLength(1);
    expect(api.callsTo("POST /api/documents/resume")[1].body).toEqual({ candidacyId: 1, includeCoverLetter: true, pdf: true });
    await waitFor(() => text().includes("Working on the new résumé and cover letter"));
    expect(button("Allow a PDF anyway")).toBeNull();
  });

  it("keeps the previous version and says so when a run fails", async () => {
    const { api } = server();
    api.on("POST /api/documents/resume", () => ({ data: { runId: "run-1" }, status: 202 }));
    api.on("GET /api/documents/runs/run-1", () => ok(run({ status: "failed", reason: "no-valid-draft", documents: [] })));
    await open();
    await click(button("Regenerate résumé and cover letter"));
    await waitFor(() => text().includes("was not made"));
    expect(text()).toContain("The new résumé and cover letter was not made (the text did not pass the grounding checks). Your earlier versions are unchanged.");
    expect(headings()).toEqual(["Résumé, version 2"]);
    expect(q("section")!.textContent).toContain("Checks passed");
    expect(button("Regenerate résumé and cover letter")!.disabled).toBe(false);
  });

  it("refuses a second run while one is working", async () => {
    const { api } = server();
    api.on("POST /api/documents/resume", () => ({ data: { runId: "run-1" }, status: 202 }));
    api.on("GET /api/documents/runs/run-1", () => ok(run({ status: "running", stage: "draft", remainingSeconds: 150, documents: [] })));
    await open();
    await click(button("Regenerate résumé and cover letter"));
    await waitFor(() => text().includes("Working on the new résumé and cover letter: writing the text."));
    expect(text()).toMatch(/\d+ s so far, at most 3 min left\./);
    expect(button("Regenerate résumé and cover letter")!.disabled).toBe(true);
  });

  it("makes the PDF of a draft, which stores it as a new version under documents/", async () => {
    const { api, add } = server([draft()]);
    api.on("POST /api/documents/13/pdf", () => {
      add(documentView({ id: 15, version: 4 }));
      return { data: { runId: "run-1" }, status: 202 };
    });
    api.on("GET /api/documents/runs/run-1", () => ok(run({ documents: [15] })));
    await open();
    expect(text()).toContain("saved under documents/");
    await click(button("Make PDF"));
    await waitFor(() => headings().length === 2);
    expect(api.callsTo("POST /api/documents/13/pdf")[0].body).toEqual({});
    expect(text()).toContain("PDF saved at documents/northwind-example-staff-engineer-2026-10-01/2/resume.pdf.");
  });
});
