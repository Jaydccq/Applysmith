import { act } from "react";
import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, byText, click, mount, q, qa, text, type, waitFor } from "../testing/dom";
import { coverage, docEntry, kbDoc, theme } from "../testing/fixtures_pages";
import { KBDocBrief, type KBDocEntry, type KBDraft, type KBGitHubAccount, type KBIngestReport, type KBRepo } from "../wire";

const draft = (over: Partial<KBDraft>): KBDraft => ({ id: 1, revision: 1, status: "draft", kind: "snippet", origin: "import", category: "", snippetId: "notes", markdown: "Notes", body: "Notes", facts: [], ...over });
import Wiki from "./Wiki";

function visit(tab: string) {
  window.history.replaceState(null, "", tab === "pages" ? "/wiki" : `/wiki?tab=${tab}`);
}

const brief = docEntry({ id: "example.com", kind: KBDocBrief, category: "", title: "Example Corp", path: "knowledge/companies/example.com.md", modifiedAt: "2026-09-20T09:00:00.000Z", tags: [], sourceUrl: "https://www.example.com/about" });
const story = docEntry({ id: "quieting-the-pager", category: "behavioral", title: "Quieting the pager", tags: [], principles: ["ownership"], sourceType: "work", path: "knowledge/behavioral/quieting-the-pager.md" });

const report = (over: Partial<KBIngestReport> = {}): KBIngestReport => ({ status: "ingested", commit: "", branch: "main", listing: "api", considered: 0, read: 0, reused: 0, fetched: 0, skipped: [], redacted: [], drafts: [], notes: [], ...over });
const repo = (over: Partial<KBRepo> = {}): KBRepo => ({ id: 1, owner: "acme", name: "pager", branch: "", commitSha: "", status: "added", report: report({ status: "" }), createdAt: "2026-10-01T09:00:00.000Z", ingestedAt: "", ...over });

const github = (over: Partial<KBGitHubAccount> = {}): KBGitHubAccount => ({ state: "signed-out", login: "", source: "", ...over });

// ended answers a run's event stream with its final event.
const ended = (runId: string, final: object) => new Response(`id: e-1\nevent: final\ndata: ${JSON.stringify({ runId, ...final })}\n\n`);
// hung is a run that goes on.
const hung = () => new Response(new ReadableStream<Uint8Array>({ start() {} }));

// proposalsRun makes the proposal request a run that ends having found these.
function proposalsRun(api: FakeApi, proposals: unknown[]) {
  let done = false;
  api.on("GET /api/kb/stories/proposals", () => ok({ proposals: done ? proposals : [] }));
  api.on("POST /api/kb/stories/proposals", () => ok({ runId: "run_p" }));
  api.on("GET /api/runs/run_p/events", () => ((done = true), ended("run_p", { status: "succeeded", outcome: `${proposals.length} proposed` })));
}

function server(entries: KBDocEntry[]): FakeApi {
  return fakeApi({
    "GET /api/kb/sources": () => ok({ repos: [] }),
    "GET /api/kb/github": () => ok(github()),
    "GET /api/secrets": () => ok({ registry: [], entries: [{ name: "github-token", kind: "token", present: false }] }),
    "GET /api/kb/documents": (call) => ok({ entries: call.query.has("q") ? [] : entries, uses: [] }),
    "GET /api/kb/drafts": () => ok({ drafts: [] }),
    "GET /api/kb/stories/proposals": () => ok({ proposals: [] }),
    "GET /api/kb/stories/coverage": () => ok(coverage(theme(), theme({ id: "disagreement", name: "Disagreement", grade: "red", stories: [], suggest: ["team-conflict", "leadership"] }))),
  });
}

describe("Wiki: pages", () => {

  it("creates a snippet with its tags", async () => {
    const api = server([]);
    api.on("POST /api/kb/documents", () => ok(kbDoc(docEntry())));
    visit("pages");
    await mount(<Wiki />);
    await waitFor(() => q("form[aria-label='New page']"));
    const form = q("form[aria-label='New page']")!;
    const by = (label: string) => qa<HTMLInputElement>("input, textarea, select").filter((e) => form.contains(e)).find((e) => e.labels?.[0]?.textContent === label)!;
    expect(button("Add page")?.disabled).toBe(true);
    await type(by("Title"), "Reef survey");
    await type(by("Tags, separated by commas"), "data, field");
    await type(by("Text"), "I led a small survey team.");
    await click(button("Add page"));
    await waitFor(() => api.callsTo("POST /api/kb/documents").length === 1);
    expect(api.callsTo("POST /api/kb/documents")[0].body).toEqual({ title: "Reef survey", tags: ["data", "field"], body: "I led a small survey team." });
  });

  it("imports a PDF as a draft that waits in the review queue, published only from there", async () => {
    const api = server([]);
    let queue: KBDraft[] = [];
    api.on("GET /api/kb/drafts", () => ok({ drafts: queue }));
    const markdown = "---\nid: harbor\nallowed_facts: []\n---\nHarbor allocation notes";
    api.on("POST /api/kb/imports", () => ok((queue = [draft({ id: 7, snippetId: "harbor", markdown, body: "Harbor allocation notes" })])[0]));
    api.on("POST /api/kb/drafts/7/publish", () => ok(kbDoc(docEntry())));
    visit("pages"); await mount(<Wiki />);
    const input = await waitFor(() => q<HTMLInputElement>("input[type=file]"));
    Object.defineProperty(input, "files", {value: [new File(["%PDF-1.4"], "harbor.pdf", {type:"application/pdf"})], configurable: true});
    await act(async () => input.dispatchEvent(new Event("change", {bubbles:true})));
    // The draft reads as its text; the frontmatter shows only in the editor.
    const preview = await waitFor(() => q("[role=region][aria-label='Text of harbor']"));
    expect([preview.textContent, q("textarea[aria-label='Text of harbor']"), api.callsTo("POST /api/kb/imports")[0].query.get("name")]).toEqual(["Harbor allocation notes", null, "harbor.pdf"]);
    expect(api.callsTo("POST /api/kb/drafts/7/publish")).toHaveLength(0);
    await click(button("Edit"));
    expect(q<HTMLTextAreaElement>("textarea[aria-label='Text of harbor']")?.value).toBe(markdown);
    await click(button("Cancel"));
    await click(button("Publish"));
    await waitFor(() => api.callsTo("POST /api/kb/drafts/7/publish").length === 1);
  });

  it("confirms a draft's statements, edits its text and rejects it after asking", async () => {
    const api = server([]);
    const story = draft({ id: 9, kind: "story", snippetId: "pager", facts: ["Fired all night.", "Cut 200 alerts."].map((text) => ({ text, origin: "user", pending: true })) });
    api.on("GET /api/kb/drafts", () => ok({ drafts: [story] }));
    for (const r of ["POST /api/kb/drafts/9/confirm", "PATCH /api/kb/drafts/9"]) api.on(r, () => ok(story));
    api.on("DELETE /api/kb/drafts/9", () => ok({ undo: "undo_d" }));
    visit("pages"); await mount(<Wiki />);
    await waitFor(() => text().includes("2 to confirm"));
    await click(byLabel(/^Confirm: Cut 200/));
    await waitFor(() => api.callsTo("POST /api/kb/drafts/9/confirm").length === 1);
    expect(api.callsTo("POST /api/kb/drafts/9/confirm")[0].body).toEqual({ facts: [1] });
    await click(button("Edit"));
    expect(button("Save draft")?.disabled).toBe(true);
    await type(q<HTMLTextAreaElement>("textarea[aria-label='Text of pager']")!, "Revised");
    await click(button("Save draft"));
    await waitFor(() => api.callsTo("PATCH /api/kb/drafts/9").length === 1);
    expect(api.callsTo("PATCH /api/kb/drafts/9")[0].body).toEqual({ markdown: "Revised" });
    await click(byLabel(/^Reject pager$/));
    expect(api.callsTo("DELETE /api/kb/drafts/9")).toHaveLength(0);
    await click(button("Reject draft"));
    await waitFor(() => api.callsTo("DELETE /api/kb/drafts/9").length === 1);
  });

  it("edits a snippet against the hash it was opened with", async () => {
    const api = server([docEntry()]);
    api.on("GET /api/kb/documents/reef-survey", () => ok(kbDoc(docEntry())));
    api.on("PATCH /api/kb/documents/reef-survey", () => ok(kbDoc(docEntry({ fileHash: "hash-2" }))));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Edit")));
    const body = await waitFor(() => qa<HTMLTextAreaElement>("textarea.tall")[0]);
    expect(body.value).toBe("I led a small survey team.");
    expect(q("ul.stack > li")!.contains(body)).toBe(true);
    await type(body, "I led a survey team of four.");
    await click(button("Save page"));
    await waitFor(() => api.callsTo("PATCH /api/kb/documents/reef-survey").length === 1);
    expect(api.callsTo("PATCH /api/kb/documents/reef-survey")[0].body).toEqual({ expectedHash: "hash-1", title: "Reef survey", tags: ["data"], body: "I led a survey team of four." });
  });

  // A stale hash is a revision conflict (KBDocumentRevise); leaving unsaved text asks (SaveBar).
  it("keeps the typed text over a conflict and saves it over the new version only when asked", async () => {
    const api = server([docEntry()]);
    let hash = "hash-1";
    api.on("GET /api/kb/documents/reef-survey", () => ok(kbDoc(docEntry({ fileHash: hash }))));
    api.on("PATCH /api/kb/documents/reef-survey", () => fail("revision_conflict", "changed since it was opened"));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Edit")));
    const body = await waitFor(() => qa<HTMLTextAreaElement>("textarea.tall")[0]);
    await type(body, "Mine.");
    hash = "hash-2";
    await click(button("Save page"));
    await click(await waitFor(() => button("Keep my text")));
    await waitFor(() => !button("Keep my text"));
    expect(body.value).toBe("Mine.");
    await click(qa("[role=tab]").find((t) => t.textContent === "Story bank")!);
    await click(await waitFor(() => button("Cancel")));
    expect(body.value).toBe("Mine.");
    await click(button("Save page"));
    await waitFor(() => api.callsTo("PATCH /api/kb/documents/reef-survey").length === 2);
    expect(api.callsTo("PATCH /api/kb/documents/reef-survey")[1].body).toMatchObject({ expectedHash: "hash-2", body: "Mine." });
  });

  // GET .../previous is the text the last save replaced (KBPrevious); it comes
  // back only by a save quoting the hash the editor opened with (KBDocumentRevise).
  it("brings back the text the last save replaced, written only when the user saves", async () => {
    const now = docEntry({ title: "Reef survey (bad)", tags: [], fileHash: "hash-2" });
    const api = server([now]);
    api.on("GET /api/kb/documents/reef-survey", () => ok({ ...kbDoc(now, "A bad save."), hasPrevious: true }));
    api.on("GET /api/kb/documents/reef-survey/previous", () => ok({ savedAt: "2026-10-01T09:00:00.000Z", title: "Reef survey", tags: ["data"], body: "I led a small survey team." }));
    api.on("PATCH /api/kb/documents/reef-survey", () => ok(kbDoc(docEntry({ fileHash: "hash-3" }))));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Edit")));
    const earlier = await waitFor(() => q<HTMLDetailsElement>("details.earlier"));
    earlier.open = true;
    await act(async () => earlier.dispatchEvent(new Event("toggle")));
    await click(await waitFor(() => button("Open in the editor")));
    expect(api.callsTo("PATCH /api/kb/documents/reef-survey")).toHaveLength(0);
    await click(button("Save page"));
    await waitFor(() => api.callsTo("PATCH /api/kb/documents/reef-survey").length === 1);
    expect(api.callsTo("PATCH /api/kb/documents/reef-survey")[0].body).toEqual({ expectedHash: "hash-2", title: "Reef survey", tags: ["data"], body: "I led a small survey team." });
  });

  // A delete answers the token that undoes it (KBUndo); Undo posts that token
  // to /api/kb/restore and the snippet is listed again.
  it("deletes a snippet only after a confirmation, and Undo brings it back", async () => {
    let entries = [docEntry()];
    const api = server([]);
    api.on("GET /api/kb/documents", () => ok({ entries, uses: [] }));
    api.on("DELETE /api/kb/documents/reef-survey", () => ((entries = []), ok({ undo: "undo_1" })));
    api.on("POST /api/kb/restore", () => ((entries = [docEntry()]), ok({ kind: "snippet", name: "reef-survey" })));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === "Delete Reef survey")!));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/kb/documents/reef-survey")).toHaveLength(0);
    expect(document.activeElement?.textContent).toBe("Cancel");
    await click(button("Delete page"));
    await waitFor(() => api.callsTo("DELETE /api/kb/documents/reef-survey").length === 1);
    await waitFor(() => qa("ul.stack > li").length === 0);
    await click(button("Undo"));
    expect(api.callsTo("POST /api/kb/restore")[0].body).toEqual({ undo: "undo_1" });
    await waitFor(() => qa("ul.stack > li").length === 1);
  });

});

describe("Wiki: GitHub repositories", () => {

  it("adds a repository as typed, trimmed, and shows the server's refusal of a bad one", async () => {
    const api = server([]);
    let n = 0;
    api.on("POST /api/kb/sources", () => (n++ === 0 ? fail("validation_failed", "\"nope\" is not a repository") : ok(repo({ owner: "acme", name: "pager" }))));
    visit("pages");
    await mount(<Wiki />);
    const form = await waitFor(() => q("form[aria-label='Add a repository']"));
    const field = form.querySelector<HTMLInputElement>("input")!;
    expect(button("Add repository")?.disabled).toBe(true);
    await type(field, "  nope ");
    await click(button("Add repository"));
    await waitFor(() => form.textContent?.includes("is not a repository"));
    await type(field, " acme/pager ");
    await click(button("Add repository"));
    await waitFor(() => api.callsTo("POST /api/kb/sources").length === 2);
    expect(api.callsTo("POST /api/kb/sources").map((c) => c.body)).toEqual([{ repo: "nope" }, { repo: "acme/pager" }]);
    await waitFor(() => api.callsTo("GET /api/kb/sources").length === 2);
    expect(document.activeElement).toBe(field); // the button is disabled again, so focus waits on the field
  });

  it("starts a read as a run and returns at once, follows it, and reads again by force only when asked", async () => {
    const api = server([]);
    api.on("GET /api/kb/sources", () => ok({ repos: [repo()] }));
    const final = (runId: string, outcome: string) => new Response(`id: e-1\nevent: final\ndata: ${JSON.stringify({ runId, status: "succeeded", outcome })}\n\n`);
    api.on("POST /api/kb/sources/1/ingest", (call) => ok({ runId: (call.body as { force: boolean }).force ? "run_two" : "run_one" }));
    api.on("GET /api/runs/run_one/events", () => final("run_one", "up-to-date"));
    api.on("GET /api/runs/run_two/events", () => final("run_two", "ingested"));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Ingest now")));
    await click(await waitFor(() => button("Read again anyway")));
    await waitFor(() => api.callsTo("POST /api/kb/sources/1/ingest").length === 2);
    const posts = api.callsTo("POST /api/kb/sources/1/ingest");
    expect(posts.map((c) => c.body)).toEqual([{ force: false }, { force: true }]);
    expect(posts.every((c) => c.headers.get("Idempotency-Key"))).toBe(true);
    await waitFor(() => api.callsTo("GET /api/runs/run_two/events").length === 1);
    await waitFor(() => api.callsTo("GET /api/kb/drafts").length >= 3);
    expect(button("Read again anyway")).toBeNull();
  });

  it("shows why a read failed", async () => {
    const api = server([]);
    api.on("GET /api/kb/sources", () => ok({ repos: [repo()] }));
    api.on("POST /api/kb/sources/1/ingest", () => ok({ runId: "run_bad" }));
    api.on("GET /api/runs/run_bad/events", () => new Response(`id: e-1\nevent: final\ndata: ${JSON.stringify({ runId: "run_bad", status: "failed", reason: "GitHub answered 404 for pager" })}\n\n`));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Ingest now")));
    await waitFor(() => text().includes("GitHub answered 404 for pager"));
    expect(button("Ingest now")?.disabled).toBe(false);
  });

  it("removes a repository only after a confirmation", async () => {
    const api = server([]);
    api.on("GET /api/kb/sources", () => ok({ repos: [repo()] }));
    api.on("DELETE /api/kb/sources/1", () => ok({ undo: "undo_r" }));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => byLabel(/^Remove acme\/pager$/)));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/kb/sources/1")).toHaveLength(0);
    await click(button("Remove repository"));
    await waitFor(() => api.callsTo("DELETE /api/kb/sources/1").length === 1);
  });

  it("signs in with GitHub: starts the flow, shows the code to enter on GitHub and where", async () => {
    const api = server([]);
    let started = false;
    api.on("GET /api/kb/github", () => ok(started ? github({ state: "pending", userCode: "WDJB-MJHT", verificationUri: "https://github.com/login/device", expiresAt: "2026-10-26T12:15:00.000Z" }) : github()));
    api.on("POST /api/kb/github/signin", () => ((started = true), ok({ userCode: "WDJB-MJHT", verificationUri: "https://github.com/login/device", expiresAt: "2026-10-26T12:15:00.000Z" })));
    visit("pages");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Sign in with GitHub")));
    await waitFor(() => text().includes("WDJB-MJHT"));
    expect(byText("a", /github.com\/login\/device/)?.getAttribute("href")).toBe("https://github.com/login/device");
    expect(text()).toContain("Waiting for you to approve it on GitHub");
    expect(button("Copy code")).not.toBeNull();
  });

  it("lets the app's own GitHub token be signed out", async () => {
    const api = server([]);
    api.on("GET /api/kb/github", () => ok(github({ state: "signed-in", login: "octo-cat", source: "vault" })));
    api.on("GET /api/kb/github/repos", () => ok({ repos: [], page: 1, more: false }));
    api.on("DELETE /api/kb/github", () => ok(github()));
    visit("pages");
    await mount(<Wiki />);
    await waitFor(() => text().includes("Signed in as octo-cat (via the app)"));
    await click(button("Sign out"));
    await waitFor(() => api.callsTo("DELETE /api/kb/github").length === 1);
  });

  it("stores a GitHub token without keeping it in the page", async () => {
    const api = server([]);
    api.on("PUT /api/secrets/github-token", () => ok({ name: "github-token", present: true, source: "keychain" }));
    visit("pages");
    await mount(<Wiki />);
    const form = await waitFor(() => q("form[aria-label='GitHub token']"));
    const field = form.querySelector<HTMLInputElement>("input[type=password]")!;
    expect(button("Save token")?.disabled).toBe(true);
    await type(field, "  ghp_exampletoken  ");
    await click(button("Save token"));
    await waitFor(() => api.callsTo("PUT /api/secrets/github-token").length === 1);
    expect(api.callsTo("PUT /api/secrets/github-token")[0].body).toEqual({ value: "ghp_exampletoken" });
    expect(field.value).toBe("");
  });
});

describe("Wiki: company briefs", () => {

  it("deletes a brief only after a confirmation, and Undo brings it back", async () => {
    let entries = [brief];
    const api = server([]);
    api.on("GET /api/kb/documents", () => ok({ entries, uses: [] }));
    api.on("DELETE /api/kb/documents/example.com", () => ((entries = []), ok({ undo: "undo_b" })));
    api.on("POST /api/kb/restore", () => ((entries = [brief]), ok({ kind: "brief", name: "example.com" })));
    visit("briefs");
    await mount(<Wiki />);
    await click(await waitFor(() => byLabel(/^Delete Example Corp$/)));
    expect(api.callsTo("DELETE /api/kb/documents/example.com")).toHaveLength(0);
    await click(await waitFor(() => button("Delete brief")));
    await waitFor(() => qa("tbody tr").length === 0);
    await click(button("Undo"));
    expect(api.callsTo("POST /api/kb/restore")[0].body).toEqual({ undo: "undo_b" });
    await waitFor(() => qa("tbody tr").length === 1);
  });

  it("fills the form from a brief to refresh it, and builds only when the user presses the button", async () => {
    const api = server([{ ...brief, title: "Example Corp company brief" }]);
    api.on("POST /api/kb/briefs", () => ok({ runId: "run_b" }));
    api.on("GET /api/runs/run_b/events", hung);
    visit("briefs");
    await mount(<Wiki />);
    await click(await waitFor(() => byLabel(/^Refresh Example Corp company brief$/)));
    const input = (label: string) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;
    expect([input("Company").value, input("Link to one of its postings").value]).toEqual(["Example Corp", "https://www.example.com/about"]);
    expect(document.activeElement).toBe(input("Company"));
    expect(api.callsTo("POST /api/kb/briefs")).toHaveLength(0);
    await click(button("Generate or refresh brief"));
    await waitFor(() => api.callsTo("POST /api/kb/briefs").length === 1);
  });

  it("starts a brief as a run, says which model task ran when it ends, and lists the new brief", async () => {
    const api = server([]);
    api.on("POST /api/kb/briefs", () => ok({ runId: "run_b" }));
    api.on("GET /api/runs/run_b/events", () => ended("run_b", { status: "succeeded", outcome: "Brief built with the research-search model task." }));
    visit("briefs");
    await mount(<Wiki />);
    const input = (label: string) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;
    await waitFor(() => q("form[aria-label='Generate a brief']"));
    expect(button("Generate or refresh brief")?.disabled).toBe(true);
    await type(input("Company"), "Example Corp");
    await type(input("Link to one of its postings"), "https://jobs.example.com/role");
    await click(button("Generate or refresh brief"));
    await waitFor(() => text().includes("Brief built with the research-search model task."));
    const post = api.callsTo("POST /api/kb/briefs")[0];
    expect(post.body).toEqual({ company: "Example Corp", jobUrl: "https://jobs.example.com/role", refresh: true });
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => api.callsTo("GET /api/kb/documents").length === 2);
  });

  it("explains a posting that names no company website", async () => {
    const api = server([]);
    api.on("POST /api/kb/briefs", () => ok({ runId: "run_bad" }));
    api.on("GET /api/runs/run_bad/events", () => ended("run_bad", { status: "failed", reason: "the posting declares no company website" }));
    visit("briefs");
    await mount(<Wiki />);
    const input = (label: string) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;
    await waitFor(() => q("form[aria-label='Generate a brief']"));
    await type(input("Company"), "Example Corp");
    await type(input("Link to one of its postings"), "https://jobs.example.com/role");
    await click(button("Generate or refresh brief"));
    await waitFor(() => text().includes("the posting declares no company website"));
    expect(text()).toContain("never guessed from its name");
    expect(button("Generate or refresh brief")?.disabled).toBe(false);
  });
});

describe("Wiki: story bank", () => {

  it("saves a story through the gates and lists what blocked it", async () => {
    const api = server([]);
    api.on("POST /api/kb/stories", () => ok({ draftId: 7, published: false, path: "", findings: [{ code: "no-metric", severity: "block", message: "The result needs a real figure." }] }));
    visit("stories");
    await mount(<Wiki />);
    await click(await waitFor(() => qa<HTMLButtonElement>("button").find((b) => b.textContent === "Add a story" && !b.getAttribute("aria-label"))!));
    const form = (await waitFor(() => q("form[aria-label='Add a story']")))!;
    const by = (label: string) => qa<HTMLInputElement>("input, textarea").filter((e) => form.contains(e)).find((e) => e.labels?.[0]?.textContent === label)!;
    expect(button("Save story")?.disabled).toBe(true);
    await type(by("Title"), "Quieting the pager");
    await type(by("Situation"), "The pager fired all night.");
    await type(by("Task"), "Cut the noise.");
    await type(by("Action"), "I grouped alerts by cause.");
    await type(by("Result"), "Fewer pages.");
    await click(button("Save story"));
    await waitFor(() => text().includes("Saved as a draft."));
    expect(text()).toContain("The result needs a real figure.");
    expect(api.callsTo("POST /api/kb/stories")[0].body).toEqual({ title: "Quieting the pager", sourceType: "work", principles: [], situation: "The pager fired all night.", task: "Cut the noise.", action: "I grouped alerts by cause.", result: "Fewer pages." });
  });

  it("Proposed story needs approval: none is saved until the user approves it", async () => {
    const api = server([]);
    const p = (title: string) => ({ title, principles: ["ownership"], situation: "s", task: "t", action: "a", result: "r" });
    proposalsRun(api, [p("Quieter pager"), p("Faster builds")]);
    api.on("POST /api/kb/stories", () => ok({ draftId: 3, published: true, path: "knowledge/behavioral/quieter-pager.md", findings: [] }));
    visit("stories");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Propose stories from my CV")));
    await waitFor(() => qa("ul[aria-label='Proposed stories'] > li").length === 2);
    expect(api.callsTo("POST /api/kb/stories")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("button").find((b) => b.textContent === "Approve and save")!);
    await waitFor(() => api.callsTo("POST /api/kb/stories").length === 1);
    expect((api.callsTo("POST /api/kb/stories")[0].body as { title: string }).title).toBe("Quieter pager");
    expect(text()).toContain("Saved to your story bank.");
    expect(qa("button").filter((b) => b.textContent === "Approve and save")).toHaveLength(1);
  });

  it("approving a proposal saves the whole story with the origin the user chose", async () => {
    const api = server([]);
    const proposal = { title: "Quieter pager", principles: ["ownership"], situation: "The pager fired all night.", task: "Cut the noise.", action: "I grouped alerts by cause.", result: "Fewer pages." };
    proposalsRun(api, [proposal, { ...proposal, title: "Faster builds" }]);
    api.on("POST /api/kb/stories", () => ok({ draftId: 3, published: true, path: "knowledge/behavioral/quieter-pager.md", findings: [] }));
    visit("stories");
    await mount(<Wiki />);
    await click(await waitFor(() => button("Propose stories from my CV")));
    await waitFor(() => qa("ul[aria-label='Proposed stories'] > li").length === 2);
    const origin = (title: string) => qa<HTMLSelectElement>("select").find((s) => s.getAttribute("aria-label") === `Origin of ${title}`)!;
    expect(origin("Quieter pager").value).toBe("work");
    expect([...origin("Quieter pager").options].map((o) => o.value)).toEqual(["project", "work", "team-conflict", "customer", "leadership", "failure-learning"]);
    await act(async () => {
      origin("Quieter pager").value = "failure-learning";
      origin("Quieter pager").dispatchEvent(new Event("change", { bubbles: true }));
    });
    const approve = (n: number) => qa<HTMLButtonElement>("button").filter((b) => b.textContent === "Approve and save")[n];
    await click(approve(0));
    await waitFor(() => api.callsTo("POST /api/kb/stories").length === 1);
    expect(api.callsTo("POST /api/kb/stories")[0].body).toEqual({ ...proposal, sourceType: "failure-learning" });
    await waitFor(() => text().includes("Saved to your story bank."));
    await click(approve(0));
    await waitFor(() => api.callsTo("POST /api/kb/stories").length === 2);
    expect(api.callsTo("POST /api/kb/stories")[1].body).toEqual({ ...proposal, title: "Faster builds", sourceType: "work" });
  });

  // kb.md, Story mining sessions: save, set themes, confirm all (none named), publish.
  it("continues an unfinished interview and banks the story with the two themes picked", async () => {
    const api = server([]);
    const at = "2026-10-20T09:00:00Z";
    const session = (over: object = {}) => ({ id: "s1", quadrant: "team", sourceType: "", state: "collecting", notesFrom: "", fields: {}, log: [], draftId: 0, createdAt: at, updatedAt: at, ignored: false, next: { field: "task", text: "What was your task?" }, ...over });
    api.on("GET /api/kb/stories/mining", () => ok({ sessions: [session()] }));
    api.on("GET /api/kb/stories/mining/suggestions", () => ok({ quadrants: [] }));
    api.on("POST /api/kb/stories/mining/s1/replies", () => ok(session({ state: "ready", next: undefined })));
    api.on("POST /api/kb/stories/mining/s1/save", () => ok(draft({ id: 4, kind: "story", origin: "mining" })));
    api.on("PATCH /api/kb/drafts/4", () => ok(draft({ id: 5, kind: "story" })));
    api.on("POST /api/kb/drafts/5/confirm", () => ok(draft({ id: 6, kind: "story" })));
    api.on("POST /api/kb/drafts/6/publish", () => ok(kbDoc(story)));
    visit("stories");
    await mount(<Wiki />);
    await waitFor(() => text().includes("What was your task?"));
    await type(q<HTMLTextAreaElement>("section[aria-label^='Interview'] textarea"), "Cut the noise.");
    await click(button("Answer"));
    expect(api.callsTo("POST /api/kb/stories/mining/s1/replies")[0].body).toEqual({ text: "Cut the noise.", expectedRevision: 0 });
    for (const box of await waitFor(() => qa<HTMLInputElement>("section[aria-label^='Interview'] input[type=checkbox]").slice(0, 2))) await click(box);
    await click(button("Save to the story bank"));
    await waitFor(() => api.callsTo("POST /api/kb/drafts/6/publish").length === 1);
    // The saved note is announced and the keyboard goes on to the next interview.
    await waitFor(() => document.activeElement === button("Start an interview"));
    expect(api.callsTo("PATCH /api/kb/drafts/4")[0].body).toEqual({ principles: ["ownership", "disagreement"] });
    expect(api.callsTo("POST /api/kb/drafts/5/confirm")[0].body).toEqual({});
  });

  // kb.md, Story mining sessions: an edit quotes the transcript length it read.
  it("edits an answered part against the session it read, and keeps the text over a newer one", async () => {
    const api = server([]);
    const at = "2026-10-20T09:00:00Z";
    const session = (n: number, situation: string) => ({ id: "s1", quadrant: "team", sourceType: "", state: "collecting", notesFrom: "", fields: { situation: { text: situation, source: "user" } }, log: Array(n).fill({ role: "reply", field: "situation", text: "", at }), draftId: 0, createdAt: at, updatedAt: at, ignored: false, next: { field: "task", text: "What?" } });
    let posts = 0;
    api.on("GET /api/kb/stories/mining", () => ok({ sessions: [session(2, "The pager fired.")] }));
    api.on("GET /api/kb/stories/mining/suggestions", () => ok({ quadrants: [] }));
    api.on("GET /api/kb/stories/mining/s1", () => ok(session(3, "Changed elsewhere.")));
    api.on("POST /api/kb/stories/mining/s1/replies", () => (posts++ === 0 ? fail("revision_conflict", "changed since it was opened") : ok(session(4, "The pager fired all night."))));
    visit("stories");
    await mount(<Wiki />);
    await click(await waitFor(() => byLabel(/^Edit Situation$/)));
    await type(q<HTMLTextAreaElement>("textarea[aria-label='Your answer: Situation']"), "The pager fired all night.");
    await click(button("Save answer"));
    await click(await waitFor(() => button("Keep my text")));
    await click(button("Save answer"));
    const edit = (expectedRevision: number) => ({ field: "situation", text: "The pager fired all night.", expectedRevision });
    expect(api.callsTo("POST /api/kb/stories/mining/s1/replies").map((c) => c.body)).toEqual([edit(2), edit(3)]);
    await waitFor(() => text().includes("Saved your new answer for Situation."));
    expect(document.activeElement).toBe(byLabel(/^Edit Situation$/));
  });

});
