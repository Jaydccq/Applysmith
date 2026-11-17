import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../testing/api";
import { button, click, mount, q, qa, text, type, waitFor } from "../testing/dom";
import { documentList, documentRun, documentView } from "../testing/fixtures";
import type { DocumentProposal, DocumentRefinement } from "../wire";
import { DocumentsDrawer } from "./Documents";

const proposal = (over: Partial<DocumentProposal> = {}): DocumentProposal => ({
  id: 5, documentId: 11, kind: "resume", instruction: "Say dependable", text: "# Alex Example\n",
  diff: [
    { op: "same", text: "# Alex Example" },
    { op: "del", text: "Backend engineer focused on reliable data platforms." },
    { op: "add", text: "Backend engineer focused on dependable data platforms." },
  ],
  createdAt: "2026-10-12T09:00:00Z", ...over,
});
const refinement = (over: Partial<DocumentRefinement> = {}): DocumentRefinement => ({ turns: [], ...over });
const run = documentRun;
const box = () => q<HTMLTextAreaElement>("textarea")!;

async function open(docs = [documentView()]) {
  const api = fakeApi({ "GET /api/documents": () => ok(documentList(...docs)) });
  await mount(<DocumentsDrawer candidacyId={1} label="Northwind Example, Staff Engineer" onClose={() => {}} />);
  await waitFor(() => q("section"));
  return api;
}

async function openRevisions() {
  await click(button("Revise with an instruction"));
  await waitFor(() => qa("section[aria-label='Revise with an instruction']")[0]);
}

describe("revising a version with an instruction", () => {
  it("sends the instruction with the earlier turns and follows the run to its proposal", async () => {
    const api = await open();
    let pending: DocumentProposal | undefined;
    api.on("GET /api/documents/refine/11", () =>
      ok(refinement({ pending, turns: [{ kind: "instruction", text: "Shorter" }, { kind: "decision", decision: "rejected" }, { kind: "error", text: "no" }] })),
    );
    api.on("POST /api/documents/11/refine", () => {
      pending = proposal();
      return { data: { runId: "run-1" }, status: 202 };
    });
    api.on("GET /api/documents/runs/run-1", () => ok(run()));
    await openRevisions();
    await type(box(), "  Say dependable ");
    await click(button("Ask for a revision"));
    await waitFor(() => button("Accept revision"));
    const post = api.callsTo("POST /api/documents/11/refine")[0];
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    // Error turns are shown but not sent.
    expect(post.body).toEqual({ instruction: "Say dependable", turns: [{ kind: "instruction", text: "Shorter" }, { kind: "decision", decision: "rejected" }] });
    expect(text()).toContain("You discarded the revision.");
    expect(text()).toContain("The revision failed: no");
  });

  it("accepts through its own run and lists the new version when it ends", async () => {
    const api = await open([documentView({ refinePending: true })]);
    api.on("GET /api/documents/refine/11", () => ok(refinement({ pending: proposal() })));
    api.on("POST /api/documents/11/refine/accept", () => ({ data: { runId: "run-1" }, status: 202 }));
    api.on("GET /api/documents/runs/run-1", () => ok(run({ documents: [12] })));
    api.on("GET /api/documents", () => ok(documentList(documentView({ id: 12, version: 3, current: true }), documentView({ current: false }))));
    await openRevisions();
    await waitFor(() => button("Accept revision"));
    await click(button("Accept revision"));
    await waitFor(() => qa("section h3").length === 2 && qa("section h3")[0].textContent === "Résumé, version 3");
    expect(api.callsTo("POST /api/documents/11/refine/accept")[0].body).toEqual({ proposalId: 5 });
  });

  it("discards without a run and offers the box again", async () => {
    const api = await open([documentView({ refinePending: true })]);
    const decided = refinement({ turns: [{ kind: "instruction", text: "Say dependable" }, { kind: "decision", decision: "rejected" }] });
    let pending: DocumentProposal | undefined = proposal();
    api.on("GET /api/documents/refine/11", () => ok(pending ? refinement({ pending }) : decided));
    api.on("POST /api/documents/11/refine/discard", () => {
      pending = undefined;
      return ok(decided);
    });
    await openRevisions();
    await waitFor(() => button("Discard revision"));
    await click(button("Discard revision"));
    await waitFor(() => q("form textarea"));
    expect(api.callsTo("POST /api/documents/11/refine/discard")[0].body).toEqual({ proposalId: 5 });
    expect(text()).toContain("You discarded the revision.");
    expect(api.callsTo("GET /api/documents/runs/run-1")).toHaveLength(0);
  });

  it("does not offer a revision of a submitted version or an older one", async () => {
    await open([documentView({ frozen: true })]);
    expect(button("Revise with an instruction")!.disabled).toBe(true);
    expect(text()).toContain("cannot be revised");
  });
});
