import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, click, mount, q, qa, text, type, unmount, waitFor } from "../testing/dom";
import { candidacy, detail } from "../testing/fixtures";
import { confirmation, digest, entry, health, linkItem, localTime, thread } from "../testing/fixtures_gmail";
import Gmail from "./Gmail";

interface Opts {
  health?: ReturnType<typeof health>;
  threads?: ReturnType<typeof thread>[];
  digests?: ReturnType<typeof digest>[];
  links?: { review: ReturnType<typeof linkItem>[]; unlinked: ReturnType<typeof linkItem>[] };
  confirmations?: ReturnType<typeof confirmation>[];
  verdicts?: Record<string, string>;
  // The reply draft the server keeps for th1; none by default after a request.
  draft?: string;
  kind?: string;
}

function server(o: Opts = {}): FakeApi {
  let confirmations = o.confirmations ?? [];
  const queued = o.links ?? { review: [], unlinked: [] };
  let links = queued;
  const threads = o.threads ?? [thread()];
  let storedDraft = "draft" in o ? o.draft : "Hello,\nThursday at 10 works for me.";
  const api = fakeApi({
    "GET /api/gmail/health": () => ok(o.health ?? health()),
    "GET /api/gmail/inbox": () => ok({ threads, total: threads.length }),
    "GET /api/gmail/inbox/th1": () => ok({ thread: thread(), timeline: [entry(), entry({ messageId: "m0", type: "interview_scheduled", direction: "outgoing", subject: "Re: call", snippet: "", reviewReasons: ["sender_unverified"] })] }),
    "GET /api/gmail/threads/th1/summary": () => ok({ text: "The recruiter asks for a call on Thursday.", generatedAt: "2026-10-12T09:05:00Z", method: "deterministic", cached: true }),
    "GET /api/gmail/threads/th1/draft": () => ok({ kind: o.kind ?? "reply", draft: storedDraft ? { text: storedDraft, kind: "reply", generatedAt: "2026-10-12T09:10:00Z", method: "model" } : undefined }),
    "GET /api/gmail/digests": () => ok({ digests: o.digests ?? [digest()] }),
    "GET /api/gmail/digests/feedback": () => ok({ verdicts: o.verdicts ?? {} }),
    "GET /api/gmail/links": (c) => ok({ items: c.query.get("decision") === "review" ? links.review : links.unlinked }),
    "GET /api/gmail/confirmations": () => ok({ confirmations }),
    "GET /api/gmail/settings": () => ok({ alerts: true, scheduledDigests: false }),
    "GET /api/candidacies/12": () => ok(detail(candidacy({ id: 12, status: "interview" }))),
  });
  api.on("POST /api/gmail/threads/th1/draft", () => {
    storedDraft = storedDraft ? "A newer draft; Thursday at 10 works." : "Thank you; Thursday at 10 works for me.";
    return ok({ kind: "reply", draft: { text: storedDraft, kind: "reply", generatedAt: "2026-10-12T09:20:00Z", method: "model" } });
  });
  api.on("POST /api/gmail/confirmations/3/confirm", () => {
    confirmations = [];
    return ok({ id: 3, state: "confirmed" });
  });
  api.on("POST /api/gmail/confirmations/3/dismiss", () => {
    confirmations = [];
    return ok({ id: 3, state: "dismissed" });
  });
  api.on("POST /api/gmail/links/sig1/confirm", (c) => {
    links = { review: links.review.filter((i) => i.signalId !== "sig1"), unlinked: links.unlinked.filter((i) => i.signalId !== "sig1") };
    return ok({ signalId: "sig1", threadId: "th1", candidacyId: (c.body as { candidacyId: number }).candidacyId, outcome: { result: "changed", fromStatus: "applied", toStatus: "rejected" } });
  });
  api.on("POST /api/gmail/links/sig1/reject", () => {
    links = { review: [], unlinked: [] };
    return ok({ signalId: "sig1" });
  });
  api.on("POST /api/gmail/links/sig1/restore", () => {
    links = queued;
    return ok({ signalId: "sig1" });
  });
  return api;
}

const open = (search = "") => window.history.replaceState(null, "", "/gmail" + search);

describe("Gmail: connection health", () => {

  it("connects again with a new app password, keeps nothing of it on the page and refreshes the status", async () => {
    const api = server({ health: health({ login: "refused", sync: "degraded" }) });
    api.on("PUT /api/gmail/account", () => ok({ health: health() }));
    api.on("POST /api/gmail/sync", () => ({ data: { started: true }, status: 202 }));
    open();
    await mount(<Gmail />);
    const password = await waitFor(() => q<HTMLInputElement>('input[type="password"]'));
    expect(q<HTMLInputElement>('input[type="email"]')?.value).toBe("alex@example.com");
    expect(button("Connect")?.disabled).toBe(true);
    await type(password, "abcd efgh ijkl mnop");
    api.on("GET /api/gmail/health", () => ok(health()));
    await click(button("Connect"));
    await waitFor(() => text().includes("Gmail is connected as alex@example.com. A sync has started."));
    expect(api.callsTo("PUT /api/gmail/account")[0].body).toEqual({ address: "alex@example.com", password: "abcd efgh ijkl mnop" });
    expect(password.value).toBe("");
    await waitFor(() => q("dl.facts dd")?.textContent === "Connected as alex@example.com");
    // The outcome stays readable after the banner it answered has gone.
    expect(q('[role="alert"]')).toBeNull();
    expect(text()).toContain("Gmail is connected as alex@example.com.");
  });

  it("shows Gmail's refusal of a new password on its field and stores nothing", async () => {
    const api = server({ health: health({ connected: false, login: "", sync: "never-run", latestRun: undefined, mailbox: undefined, cursorAgeSeconds: undefined }) });
    api.on("PUT /api/gmail/account", () => fail("validation_failed", "Gmail refused the sign-in (Invalid credentials (Failure)): check the address", { details: { field: "password" } }));
    await mount(<Gmail />);
    await type(await waitFor(() => q<HTMLInputElement>('input[type="email"]')), "alex@example.com");
    await type(q<HTMLInputElement>('input[type="password"]'), "abcdefghijklmnop");
    await click(button("Connect"));
    await waitFor(() => text().includes("Gmail refused the sign-in"));
    expect(text()).not.toContain("abcdefghijklmnop");
    expect(api.callsTo("POST /api/gmail/sync")).toHaveLength(0);
  });

});

describe("Gmail: sync now", () => {
  it("starts a sync in the background and says so, then refreshes the status", async () => {
    const api = server();
    api.on("POST /api/gmail/sync", () => ({ data: { started: true }, status: 202 }));
    await mount(<Gmail />);
    await click(await waitFor(() => button("Sync now")));
    await waitFor(() => text().includes("A sync has started."));
    expect(api.callsTo("POST /api/gmail/sync")).toHaveLength(1);
    expect(api.callsTo("GET /api/gmail/health").length).toBeGreaterThan(1);
  });

  it("shows the refusal of a second sync", async () => {
    const api = server();
    api.on("POST /api/gmail/sync", () => fail("conflict", "Gmail sync is already running"));
    await mount(<Gmail />);
    await click(await waitFor(() => button("Sync now")));
    await waitFor(() => text().includes("Gmail sync is already running"));
    expect(text()).not.toContain("A sync has started.");
  });
});

describe("Gmail: action inbox", () => {
  // The lane, owner and link read in a job seeker's words, never the wire's
  // ids ("Owner: user", "link auto"; round-58 audit).
  it("lists a thread with its lane, attention, owner, link and linked application", async () => {
    server({ threads: [thread(), thread({ threadId: "r", employer: "Globex", subject: "Thank you for applying", state: "rejected", kind: "rejection", lane: "closed", owner: "none", attention: "urgent" })] });
    await mount(<Gmail />);
    await waitFor(() => text().includes("Can we schedule a call?"));
    expect(text()).toContain("Northwind Example: Staff Engineer");
    expect(text()).toContain("Yours to do");
    // A rejection sorts first as urgent, but with nothing to do it shows no urgency.
    const chips = (employer: string) => [...(qa("li.thread-row").find((li) => li.querySelector("h3")?.textContent?.startsWith(employer))?.querySelectorAll(".chip") ?? [])].map((c) => c.textContent);
    expect(chips("Northwind Example")).toContain("Urgent");
    expect(chips("Globex")).toEqual(["Closed", "Matched"]);
    expect(text()).not.toMatch(/Owner: user|link auto|do now/);
    expect(text()).toContain("2 days waiting");
    expect(text()).toContain("Linked to application 7");
    expect(qa<HTMLAnchorElement>("a").find((a) => a.textContent === "application 7")?.getAttribute("href")).toBe("/tracker?row=7");
  });

});

describe("Gmail: thread detail", () => {

  it("copies the draft to the clipboard and never calls a send route", async () => {
    const api = server();
    const write = vi.fn(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: write } });
    open("?thread=th1");
    await mount(<Gmail />);
    await click(await waitFor(() => qa<HTMLButtonElement>("dialog button").find((b) => b.getAttribute("aria-label") === "Copy the reply draft")));
    expect(write).toHaveBeenCalledWith("Hello,\nThursday at 10 works for me.");
    expect(api.calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });

  // I replied also marks the thread done, as Done does, so it leaves Do now at
  // once instead of at the next sync; Undo reopens it with the rest.
  it("records I replied on a follow-up with the row's version, marks the thread done, and Undo takes back all", async () => {
    const api = server({ kind: "follow_up" });
    const row = (version: number) => candidacy({ id: 7, status: "applied", version });
    api.on("GET /api/candidacies/7", () => ok(detail(row(3))));
    api.on("POST /api/candidacies/7/notes", (c) => ok(row("undo" in (c.body as object) ? 7 : 4)));
    api.on("POST /api/candidacies/7/followup", (c) => ok(row("undo" in (c.body as object) ? 6 : 5)));
    api.on("POST /api/gmail/inbox/th1/done", () => ok({ threadId: "th1" }));
    api.on("POST /api/gmail/inbox/th1/reopen", () => ok({ threadId: "th1" }));
    open("?thread=th1");
    await mount(<Gmail />);
    await click(await waitFor(() => button("I replied")));
    await waitFor(() => api.callsTo("POST /api/gmail/inbox/th1/done").length === 1);
    await click(await waitFor(() => button("Undo")));
    await waitFor(() => api.callsTo("POST /api/candidacies/7/notes").length === 2);
    expect(api.callsTo("POST /api/gmail/inbox/th1/reopen")).toHaveLength(1);
    expect(api.callsTo("POST /api/candidacies/7/followup").map((c) => c.body)).toEqual([{ expectedVersion: 4 }, { undo: true, expectedVersion: 5 }]);
    expect(api.callsTo("POST /api/candidacies/7/notes").map((c) => c.body)).toEqual([expect.objectContaining({ expectedVersion: 3 }), { undo: true, expectedVersion: 6 }]);
  });

  // A mail that moved its application (a misread rejection) is put back from
  // the thread as the user's correction (PATCH /api/candidacies/{id},
  // StatusChangeRequest), only while the server says the move is the row's
  // latest status change (putBack): a row the user set back to the same
  // status since has that status but is not offered back.
  it("puts the row back only while the server says the mail's move is the latest", async () => {
    for (const putBack of [true, false]) {
      const api = server();
      const moved = entry({ type: "rejection", outcome: { result: "changed", fromStatus: "applied", toStatus: "rejected", candidacyId: 7 }, rowStatus: "rejected", putBack });
      api.on("GET /api/gmail/inbox/th1", () => ok({ thread: thread(), timeline: [moved] }));
      api.on("GET /api/candidacies/7", () => ok(detail(candidacy({ id: 7, status: "rejected", version: 4 }))));
      api.on("PATCH /api/candidacies/7", () => ok({ entry: candidacy({ id: 7, status: "applied", version: 5 }), changed: true, correction: true, inferred: false }));
      open("?thread=th1");
      await mount(<Gmail />);
      await waitFor(() => q("dialog")?.textContent?.includes("Tracker status moved from applied to rejected."));
      const back = qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Put it back to applied");
      if (putBack) {
        await click(back!);
        await waitFor(() => api.callsTo("PATCH /api/candidacies/7").length === 1);
        expect(api.callsTo("PATCH /api/candidacies/7")[0].body).toEqual({ status: "applied", expectedVersion: 4 });
      } else {
        expect(back).toBeUndefined();
      }
      unmount();
    }
  });

});

describe("Gmail: review tab", () => {
  // The link names the tracker row by its id (GmailLinkConfirm); the user
  // picks the row by its company and role, among the employer's applications.
  it("confirms a link to the application picked by name when the queue has no candidate", async () => {
    const api = server({ links: { review: [], unlinked: [linkItem({ decision: "unlinked", candidacyId: undefined })] } });
    api.on("GET /api/candidacies", (c) => ok({ entries: c.query.get("company") === "Northwind Example" ? [candidacy({ id: 12, status: "applied" }), candidacy({ id: 13, role: "Data Engineer" })] : [], page: 1, pageSize: 20, total: 26 }));
    open("?tab=review");
    await mount(<Gmail />);
    const confirmLink = await waitFor(() => byLabel(/^Confirm the link/));
    expect(confirmLink.disabled).toBe(true);
    const pick = await waitFor(() => qa<HTMLSelectElement>("select").find((s) => s.labels?.[0]?.textContent?.startsWith("Application")));
    await type(pick, [...pick.options].find((o) => o.textContent?.startsWith("Northwind Example: Staff Engineer"))!.value);
    await click(byLabel(/^Confirm the link/));
    await waitFor(() => api.callsTo("POST /api/gmail/links/sig1/confirm").length === 1);
    expect(api.callsTo("POST /api/gmail/links/sig1/confirm")[0].body).toEqual({ candidacyId: 12 });
    await waitFor(() => text().includes("No link needs your decision."));
  });

  it("rejects a signal as not job related, and Undo puts it back", async () => {
    const api = server({ links: { review: [linkItem()], unlinked: [] } });
    open("?tab=review");
    await mount(<Gmail />);
    await click(await waitFor(() => byLabel(/^Not job related/)));
    await waitFor(() => api.callsTo("POST /api/gmail/links/sig1/reject").length === 1);
    await waitFor(() => text().includes("No link needs your decision."));
    await click(await waitFor(() => button("Undo")));
    await waitFor(() => api.callsTo("POST /api/gmail/links/sig1/restore").length === 1);
    await waitFor(() => byLabel(/^Not job related/));
    expect(button("Undo")).toBeNull();
  });

  it("names a refused tracker change after a confirmation", async () => {
    const api = server({ links: { review: [linkItem()], unlinked: [] } });
    api.on("POST /api/gmail/links/sig1/confirm", () => ok({ signalId: "sig1", threadId: "th1", candidacyId: 7, outcome: { result: "no_change", reason: "the status cannot move backwards" } }));
    open("?tab=review");
    await mount(<Gmail />);
    await click(await waitFor(() => byLabel(/^Confirm the link/)));
    await waitFor(() => text().includes("the status cannot move backwards"));
  });

  it("lists an offer confirmation, which changes nothing until confirmed, and confirms it", async () => {
    const api = server({ confirmations: [confirmation({ company: "Example Corp", role: "Backend Engineer" })] });
    open("?tab=review");
    await mount(<Gmail />);
    await waitFor(() => text().includes("suggests the status"));
    expect(text()).toContain("changes only if you confirm");
    // The link to the tracker row names the row by the company and role the wire carries.
    expect(q('a[href="/tracker?row=12"]')?.textContent).toBe("Example Corp \u2014 Backend Engineer");
    expect(api.callsTo("POST /api/gmail/confirmations/3/confirm")).toHaveLength(0);
    await click(byLabel(/^Confirm offer for Example Corp/));
    await waitFor(() => text().includes("Nothing waits for your confirmation."));
    expect(api.callsTo("POST /api/gmail/confirmations/3/confirm")).toHaveLength(1);
  });

  // Undo of a confirmed offer is one server call that puts the row back and the
  // offer waiting (POST .../restore, one transaction), never a status write of
  // its own that can land without the restore.
  it("undoes a confirmed offer in one call and names the status the row is back to", async () => {
    const api = server({ confirmations: [confirmation()] });
    api.on("POST /api/gmail/confirmations/3/restore", () => ok({ id: 3, state: "pending", rowStatus: "interview" }));
    open("?tab=review");
    await mount(<Gmail />);
    await click(await waitFor(() => byLabel(/^Confirm offer/)));
    await click(await waitFor(() => button("Undo")));
    await waitFor(() => text().includes("is back to interview"));
    expect(api.callsTo("POST /api/gmail/confirmations/3/restore")).toHaveLength(1);
    expect(api.calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
  });

});

describe("Gmail: briefs", () => {
  const morning = digest({ id: 5, createdAt: localTime(12, 8) });

  it("rates a thread thumbs-up, and a second press clears the rating", async () => {
    let verdicts: Record<string, string> = {};
    const api = server({ digests: [morning] });
    api.on("GET /api/gmail/digests/feedback", () => ok({ verdicts }));
    api.on("POST /api/gmail/digests/5/feedback", (c) => {
      const b = c.body as { threadKey: string; verdict: string };
      verdicts = b.verdict === "clear" ? {} : { [b.threadKey]: b.verdict };
      return ok({ digestId: 5, threadKey: b.threadKey, verdict: b.verdict });
    });
    open("?tab=briefs");
    await mount(<Gmail />);
    await click(await waitFor(() => byLabel(/^Thumbs up for Northwind/)));
    await waitFor(() => byLabel(/^Thumbs up for Northwind/)?.getAttribute("aria-pressed") === "true");
    expect(api.callsTo("POST /api/gmail/digests/5/feedback")[0].body).toEqual({ threadKey: "th1", verdict: "up" });
    await click(byLabel(/^Thumbs up for Northwind/));
    await waitFor(() => byLabel(/^Thumbs up for Northwind/)?.getAttribute("aria-pressed") === "false");
    expect(api.callsTo("POST /api/gmail/digests/5/feedback")[1].body).toEqual({ threadKey: "th1", verdict: "clear" });
  });

  it("makes a brief on demand and opens it", async () => {
    const made = digest({ id: 9, headline: "Made by hand", trigger: "manual", createdAt: localTime(12, 14) });
    let list = [morning];
    const api = server({ digests: [morning] });
    api.on("GET /api/gmail/digests", () => ok({ digests: list }));
    api.on("POST /api/gmail/digests/run", () => {
      list = [made, morning];
      return ok({ runId: "run_digest" });
    });
    api.on("GET /api/runs/run_digest/events", () => new Response(`id: e-1\nevent: final\ndata: ${JSON.stringify({ runId: "run_digest", status: "succeeded", outcome: "9" })}\n\n`));
    open("?tab=briefs");
    await mount(<Gmail />);
    await click(await waitFor(() => button("Make a brief now")));
    await waitFor(() => text().includes("Made by hand"));
    expect(window.location.search).toContain("brief=9");
  });

  it("says a brief is already being made when the server reports a conflict", async () => {
    const api = server({ digests: [morning] });
    api.on("POST /api/gmail/digests/run", () => fail("conflict", "a digest is already being produced"));
    open("?tab=briefs");
    await mount(<Gmail />);
    await click(await waitFor(() => button("Make a brief now")));
    await waitFor(() => text().includes("already being produced"));
  });

});

describe("Gmail: deciding a thread", () => {
  const th2 = () => thread({ threadId: "th2", subject: "Complete the assessment", kind: "online_assessment", state: "assessment" });
  // The server hides a decided thread from the lane it was decided in.
  function decisions(opts: { signals?: string[] } = {}) {
    const gone = new Set<string>();
    const api = fakeApi({
      "GET /api/gmail/health": () => ok(health()),
      "GET /api/gmail/inbox": () => ok({ threads: [thread(), th2()].filter((t) => !gone.has(t.threadId)), total: 2 - gone.size }),
      "GET /api/gmail/inbox/th1": () => ok({ thread: thread({ done: gone.has("th1") }), timeline: [entry()] }),
      "GET /api/gmail/threads/th1/summary": () => ok({ text: "The recruiter asks for a call on Thursday.", generatedAt: "2026-10-12T09:05:00Z", method: "deterministic", cached: true }),
      "GET /api/gmail/digests": () => ok({ digests: [] }),
      "GET /api/gmail/links": () => ok({ items: [] }),
      "POST /api/gmail/inbox/th1/done": () => (gone.add("th1"), ok({ threadId: "th1" })),
      "POST /api/gmail/inbox/th1/dismiss": () => (gone.add("th1"), ok({ threadId: "th1", signalIds: opts.signals ?? ["m1"] })),
      "POST /api/gmail/inbox/th1/reopen": () => (gone.delete("th1"), ok({ threadId: "th1" })),
      "POST /api/gmail/links/m1/restore": () => (gone.delete("th1"), ok({ signalId: "m1" })),
      "POST /api/gmail/links/m2/restore": () => ok({ signalId: "m2" }),
    });
    return api;
  }
  const opens = () => document.activeElement?.getAttribute("aria-label");

  it("marks a row done, moves focus to the next row, and undoes it", async () => {
    const api = decisions();
    await mount(<Gmail />);
    await click(await waitFor(() => byLabel(/^Mark done: Can we schedule/)));
    await waitFor(() => text().includes("Marked done: Can we schedule a call?"));
    expect(api.callsTo("POST /api/gmail/inbox/th1/done")).toHaveLength(1);
    await waitFor(() => !byLabel(/^Open the thread: Can we schedule/));
    expect(opens()).toBe("Open the thread: Complete the assessment");
    await click(button("Undo"));
    await waitFor(() => byLabel(/^Open the thread: Can we schedule/));
    // Focus follows the restored row, not the tab the vanished Undo button leaves it on.
    await waitFor(() => opens() === "Open the thread: Can we schedule a call?");
    expect(api.callsTo("POST /api/gmail/inbox/th1/reopen")).toHaveLength(1);
    expect(text()).not.toContain("Marked done");
  });

  it("dismisses a row and the undo restores exactly the signals the server marked", async () => {
    const api = decisions({ signals: ["m1", "m2"] });
    await mount(<Gmail />);
    await click(await waitFor(() => byLabel(/^Dismiss the thread: Can we schedule/)));
    await waitFor(() => text().includes("Dismissed: Can we schedule a call?"));
    await click(button("Undo"));
    await waitFor(() => byLabel(/^Open the thread: Can we schedule/));
    expect(api.callsTo("POST /api/gmail/links/m1/restore")).toHaveLength(1);
    expect(api.callsTo("POST /api/gmail/links/m2/restore")).toHaveLength(1);
  });

});
