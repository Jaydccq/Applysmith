import { act } from "react";
import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { button, byLabel, click, mount, press, q, qa, settle, submit, text, type, waitFor } from "../testing/dom";
import { approval, card, chatStream, goal, message, session } from "../testing/fixtures_agent";
import AskAgent from "./AskAgent";
import { navigate } from "../router";

function server(over: { cards?: ReturnType<typeof card>[]; approvals?: ReturnType<typeof approval>[]; goals?: ReturnType<typeof goal>[]; sessions?: ReturnType<typeof session>[]; messageText?: Record<string, string> } = {}) {
  let cards = over.cards ?? [];
  let approvals = over.approvals ?? [];
  let goals = over.goals ?? [];
  const sessions = over.sessions ?? [];
  const api = fakeApi({
    "GET /api/agent/confirmations": () => ok({ cards }),
    "GET /api/agent/approvals": () => ok({ approvals }),
    "GET /api/agent/goals": () => ok({ goals }),
    // The server matches titles and message text; the fake matches the title and
    // the words a session is said to contain.
    "GET /api/apply/skip-status": (c) => {
      const company = c.query.get("company") ?? "";
      return company === "Northwind Example" ? ok({ company, listed: false }) : ok({ company, listed: true, entry: "Alphabet" });
    },
    "GET /api/chat/sessions": (c) => {
      const needle = c.query.get("q")?.toLowerCase();
      const found = needle ? sessions.filter((s) => `${s.title} ${over.messageText?.[s.id] ?? ""}`.toLowerCase().includes(needle)) : sessions;
      return ok({ sessions: found, total: found.length });
    },
  });
  const decide = (verb: string) => (c: { path: string }) => {
    const id = c.path.split("/")[4];
    const done = cards.find((x) => x.id === id)!;
    cards = cards.filter((x) => x.id !== id);
    return ok({ card: { ...done, state: verb }, result: verb === "approved" ? "Evaluation queued." : undefined });
  };
  api.on("POST /api/agent/confirmations/card-1/approve", decide("approved"));
  api.on("POST /api/agent/confirmations/card-1/approve-standing", decide("approved"));
  api.on("POST /api/agent/confirmations/card-1/decline", decide("declined"));
  api.on("DELETE /api/agent/approvals/ap-1", () => {
    approvals = [];
    return ok({ id: "ap-1" });
  });
  const setGoal = () => (c: { path: string }) => ok(goals.find((g) => c.path.includes(g.id)));
  api.on("POST /api/agent/goals/g1/proposals/p1/accept", () => {
    goals = goals.map((g) => ({ ...g, proposal: undefined, version: 2, steps: [{ text: "Shortlist companies", status: "done" }, { text: "Send five applications" }] }));
    return ok(goals[0]);
  });
  api.on("POST /api/agent/goals/g1/proposals/p1/dismiss", () => {
    goals = goals.map((g) => ({ ...g, proposal: undefined }));
    return ok(goals[0]);
  });
  api.on("PATCH /api/agent/goals/g1", setGoal());
  api.on("DELETE /api/agent/goals/g1", () => {
    goals = [];
    return ok({ id: "g1" });
  });
  api.on("POST /api/agent/goals", () => ok(goal({ id: "g2", title: "New" }), ));
  return api;
}

const openTab = (id: string) => window.history.replaceState(null, "", `/ask?tab=${id}`);

describe("Ask Agent: confirmation cards", () => {

  it("approves once without a body and removes the card", async () => {
    const api = server({ cards: [card()] });
    await mount(<AskAgent />);
    await click(await waitFor(() => button("Approve once")));
    await waitFor(() => api.callsTo("POST /api/agent/confirmations/card-1/approve").length === 1);
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve")[0].body).toBeUndefined();
    await waitFor(() => text().includes("Nothing is waiting for you."));
    expect(text()).toContain("evaluate_job: approved. Evaluation queued.");
  });

  it("approves for today through the standing route with the scope", async () => {
    const api = server({ cards: [card()] });
    await mount(<AskAgent />);
    await click(await waitFor(() => button("Approve for today")));
    await waitFor(() => api.callsTo("POST /api/agent/confirmations/card-1/approve-standing").length === 1);
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve-standing")[0].body).toEqual({ scope: "today" });
  });

  it("declines a card", async () => {
    const api = server({ cards: [card()] });
    await mount(<AskAgent />);
    await click(await waitFor(() => button("Decline")));
    await waitFor(() => api.callsTo("POST /api/agent/confirmations/card-1/decline").length === 1);
    await waitFor(() => text().includes("evaluate_job: declined."));
  });

  it("asks for confirmation naming company, role and host before a submit is approved", async () => {
    const api = server({ cards: [card({ tier: 4, scopes: ["once"], tool: "autofill_application" })] });
    await mount(<AskAgent />);
    await click(await waitFor(() => button("Approve once")));
    await waitFor(() => q("dialog"));
    expect(q("dialog")?.textContent).toContain("submits the application to Northwind Example for Staff Engineer on boards.example.test");
    await waitFor(() => q("dialog")?.textContent?.includes("Northwind Example is not on your batch skip list."));
    expect(api.callsTo("GET /api/apply/skip-status")[0].query.get("company")).toBe("Northwind Example");
    expect(document.activeElement?.textContent).toBe("Cancel");
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve")).toHaveLength(0);
    await press("Escape", {}, q("dialog")!);
    await waitFor(() => !q("dialog"));
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve")).toHaveLength(0);
    await click(button("Approve once"));
    await waitFor(() => q("dialog"));
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Approve and submit")!);
    await waitFor(() => api.callsTo("POST /api/agent/confirmations/card-1/approve").length === 1);
  });

  it("offers a submit no standing scope in the list, whatever scopes its card names", async () => {
    const api = server({ cards: [card({ tier: 4, scopes: ["once", "conversation", "today"], tool: "autofill_application" })] });
    await mount(<AskAgent />);
    await waitFor(() => button("Approve once"));
    expect(button("Approve for this conversation")).toBeNull();
    expect(button("Approve for today")).toBeNull();
    await click(button("Approve once"));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve")).toHaveLength(0);
  });

  it("shows the refusal with its request id when the card has expired, and reloads the list", async () => {
    const api = server({ cards: [card()] });
    api.on("POST /api/agent/confirmations/card-1/approve", () => fail("conflict", "that card expired or was already handled"));
    await mount(<AskAgent />);
    await click(await waitFor(() => button("Approve once")));
    await waitFor(() => text().includes("that card expired or was already handled"));
    expect(q("[role=alert]")?.textContent).toContain("Request ");
    expect(api.callsTo("GET /api/agent/confirmations").length).toBeGreaterThan(1);
  });

});

describe("Ask Agent: standing approvals", () => {

  it("revokes only after a confirmation, then lists none", async () => {
    const api = server({ approvals: [approval()] });
    openTab("approvals");
    await mount(<AskAgent />);
    await click(await waitFor(() => byLabel(/Revoke evaluate_job on boards.example.test/)));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/agent/approvals/ap-1")).toHaveLength(0);
    expect(q("dialog")?.textContent).toContain("held for your decision again");
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Revoke")!);
    await waitFor(() => api.callsTo("DELETE /api/agent/approvals/ap-1").length === 1);
    await waitFor(() => text().includes("No standing approval is active."));
  });

});

describe("Ask Agent: goals", () => {
  const proposed = goal({ proposal: { id: "p1", reason: "Two roles closed this week.", steps: [{ text: "Send five applications" }], createdAt: "2026-10-12T09:00:00Z" } });

  it("accepts a proposal and shows the new steps", async () => {
    const api = server({ goals: [proposed] });
    openTab("goals");
    await mount(<AskAgent />);
    await click(await waitFor(() => button("Accept proposal")));
    await waitFor(() => api.callsTo("POST /api/agent/goals/g1/proposals/p1/accept").length === 1);
    await waitFor(() => text().includes("Send five applications") && !button("Accept proposal"));
  });

  it("deletes a goal only after a confirmation", async () => {
    const api = server({ goals: [goal()] });
    openTab("goals");
    await mount(<AskAgent />);
    await click(await waitFor(() => byLabel(/^Delete /)));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/agent/goals/g1")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Delete goal")!);
    await waitFor(() => api.callsTo("DELETE /api/agent/goals/g1").length === 1);
    await waitFor(() => text().includes("No goal yet."));
  });

  it("creates a goal from a title, a due date and steps", async () => {
    const api = server();
    openTab("goals");
    await mount(<AskAgent />);
    await waitFor(() => text().includes("New goal"));
    const form = qa("form").find((f) => f.textContent?.includes("Add goal"))!;
    await type(form.querySelector("input:not([type=date])"), "Ten applications");
    await type(form.querySelector("input[type=date]"), "2026-10-31");
    await type(form.querySelector("textarea"), "Shortlist\nTailor the CV\n");
    await submit(form);
    await waitFor(() => api.callsTo("POST /api/agent/goals").length === 1);
    expect(api.callsTo("POST /api/agent/goals")[0].body).toEqual({ title: "Ten applications", dueDate: "2026-10-31", steps: [{ text: "Shortlist" }, { text: "Tailor the CV" }] });
  });

  it("keeps the status of a step whose text is unchanged when the steps are edited", async () => {
    const api = server({ goals: [goal()] });
    openTab("goals");
    await mount(<AskAgent />);
    await click(await waitFor(() => byLabel(/^Edit /)));
    const area = q<HTMLTextAreaElement>("textarea")!;
    await type(area, "Shortlist companies\nSend five applications");
    await submit(area.closest("form"));
    await waitFor(() => api.callsTo("PATCH /api/agent/goals/g1").length === 1);
    expect(api.callsTo("PATCH /api/agent/goals/g1")[0].body).toEqual({
      title: "Ten targeted applications this month",
      dueDate: "2026-10-31",
      steps: [{ text: "Shortlist companies", status: "done" }, { text: "Send five applications" }],
    });
  });
});

describe("Ask Agent: chat", () => {
  const detail = (extra: Record<string, unknown> = {}) => ({
    session: session(),
    messages: [
      message({ id: 1, role: "user", text: "How is my search going?" }),
      message({ id: 2, role: "tool_call", text: '{"days":7}', toolName: "list_applications", class: "read", toolCallId: "c1" }),
      message({ id: 3, role: "tool_result", text: "3 applications", toolName: "list_applications", toolCallId: "c1" }),
      message({ id: 4, role: "assistant", text: "You have **three** open applications." }),
    ],
    turns: [],
    ...extra,
  });

  it("offers to let the agent finish a run the conversation started under the latest note about it, and not while a turn runs", async () => {
    const at = "2026-10-21T17:00:00.000Z";
    const waiting = {
      id: "run_7", kind: "apply", origin: "agent", title: "Apply to Northwind Example", state: "needs-you", reason: "answer", link: "/runs/run_7", revision: 1,
      startedAt: at, updatedAt: at, stateAt: at, canFocusTab: true, canDone: true,
    };
    const messages = [
      message({ id: 1, role: "user", text: "Fill in the application at https://jobs.example.test/p/7 but don't submit it." }),
      message({ id: 2, role: "assistant", text: "Started filling it." }),
      message({ id: 3, role: "note", text: 'The run "Apply to Northwind Example" needs an answer. Earlier.', runId: "run_7", systemMade: true }),
      message({ id: 4, role: "note", text: 'The run "Apply to Northwind Example" needs an answer. Latest.', runId: "run_7", systemMade: true }),
    ];
    let running = false;
    const api = server({ sessions: [session()] });
    api.on("GET /api/chat/sessions/s1", () => ok(detail({ messages, ...(running ? { activeTurnId: "t2" } : {}) })));
    api.on("GET /api/chat/sessions/s1/events", () => chatStream().response());
    api.on("GET /api/runs/run_7", () => ok({ runId: "run_7", kind: "apply", status: "running", createdAt: at, updatedAt: at, entry: waiting }));
    api.on("POST /api/apply/runs/run_7/takeover", () => ok({ sessionId: "s1" }));
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    const finish = await waitFor(() => button("Let the agent finish"));
    expect(qa("button").filter((b) => b.textContent === "Let the agent finish")).toHaveLength(1);
    expect(finish.closest(".run-note")?.textContent).toContain("Latest.");
    await click(finish);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_7/takeover").length === 1);
    expect(window.location.search).toContain("conversation=s1");

    running = true;
    await mount(<AskAgent />);
    await waitFor(() => button("Send guidance") && api.callsTo("GET /api/runs/run_7").length >= 2);
    await settle();
    expect(button("Let the agent finish")).toBeNull();
  });

  it("draws a researched answer's web sources as links that open in a new tab, and any other address as text", async () => {
    const api = server({ sessions: [session()] });
    api.on("GET /api/chat/sessions/s1", () =>
      ok(
        detail({
          messages: [
            message({ id: 1, role: "user", text: "Research the latest Go release." }),
            message({
              id: 2, role: "assistant", text: "Go 1.27.1 is the latest stable release.",
              citations: [
                { label: "Release History - The Go Programming Language", url: "https://go.dev/doc/devel/release" },
                { label: "Injected", url: "javascript:alert(1)" },
                { label: "profile#employment" },
              ],
              limitations: ["Rests only on the web search results listed as sources; nothing else was checked."],
            }),
          ],
        }),
      ),
    );
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    await waitFor(() => text().includes("Go 1.27.1 is the latest stable release."));
    const links = qa<HTMLAnchorElement>(".sources a");
    expect(links).toHaveLength(1);
    expect(links[0].textContent).toBe("Release History - The Go Programming Language");
    expect(links[0].getAttribute("href")).toBe("https://go.dev/doc/devel/release");
    expect(links[0].target).toBe("_blank");
    expect(links[0].rel).toBe("noopener noreferrer");
    const sources = q(".sources")!;
    expect(sources.textContent).toContain("https://go.dev/doc/devel/release");
    expect(sources.textContent).toContain("Injected");
    expect(sources.textContent).toContain("profile#employment");
    expect(sources.innerHTML).not.toContain("javascript:");
    expect(sources.textContent).toContain("Limits");
    expect(sources.textContent).toContain("Rests only on the web search results");
  });

  it("starts a conversation with the first message and streams the reply", async () => {
    const api = server();
    const stream = chatStream();
    api.on("POST /api/chat/sessions/new/messages", () => ok({ sessionId: "s9", turnId: "t9" }));
    let running = true;
    api.on("GET /api/chat/sessions/s9", () =>
      ok({ session: session({ id: "s9", title: "Hello" }), messages: [message({ id: 1, role: "user", text: "Hello agent" }), ...(running ? [] : [message({ id: 2, role: "assistant", text: "Hi there" })])], turns: [], ...(running ? { activeTurnId: "t9" } : {}) }),
    );
    api.on("GET /api/chat/sessions/s9/events", () => stream.response());
    await mount(<AskAgent />);
    await waitFor(() => q("textarea"));
    await type(q("textarea"), "Hello agent");
    await submit(q("form.composer"));
    await waitFor(() => api.callsTo("POST /api/chat/sessions/new/messages").length === 1);
    expect(api.callsTo("POST /api/chat/sessions/new/messages")[0].body).toEqual({ text: "Hello agent" });
    await waitFor(() => api.callsTo("GET /api/chat/sessions/s9/events").length === 1);
    stream.push({ type: "state", activeTurnId: "t9" });
    stream.push({ type: "tool.call", toolCallId: "c1", tool: "list_applications", class: "read", phrase: "Reading your tracker" });
    stream.push({ type: "token", text: "Hi " });
    stream.push({ type: "token", text: "there" });
    await waitFor(() => text().includes("Reading your tracker") && text().includes("Hi there"));
    expect(q("[aria-busy=true] .chip")?.textContent).toBe("read");
    expect(q("[aria-busy=true]")).not.toBeNull();
    running = false;
    stream.push({ type: "final", text: "Hi there" });
    await waitFor(() => !q("[aria-busy=true]") && qa(".turn.assistant").length === 1);
    expect(text()).toContain("Hi there");
  });

  it("names a web research while it runs and keeps the sources in the stored answer", async () => {
    const api = server();
    const stream = chatStream();
    api.on("POST /api/chat/sessions/new/messages", () => ok({ sessionId: "s8", turnId: "t8" }));
    let running = true;
    const cite = { label: "All releases - The Go Programming Language", url: "https://go.dev/dl/" };
    api.on("GET /api/chat/sessions/s8", () =>
      ok({
        session: session({ id: "s8", title: "Go" }),
        messages: [message({ id: 1, role: "user", text: "Research the latest Go release" }), ...(running ? [] : [message({ id: 2, role: "assistant", text: "Go 1.27.1 is out.", citations: [cite] })])],
        turns: [],
        ...(running ? { activeTurnId: "t8" } : {}),
      }),
    );
    api.on("GET /api/chat/sessions/s8/events", () => stream.response());
    await mount(<AskAgent />);
    await waitFor(() => q("textarea"));
    await type(q("textarea"), "Research the latest Go release");
    await submit(q("form.composer"));
    await waitFor(() => api.callsTo("GET /api/chat/sessions/s8/events").length === 1);
    stream.push({ type: "state", activeTurnId: "t8" });
    stream.push({ type: "tool.call", toolCallId: "c1", tool: "research_question", class: "draft", phrase: "Researching on the web" });
    await waitFor(() => text().includes("Researching on the web"));
    expect(q("[aria-busy=true]")?.textContent).toContain("draft");
    running = false;
    stream.push({ type: "final", text: "Go 1.27.1 is out.", citations: [cite], limitations: ["Rests only on the web search results listed as sources; nothing else was checked."] });
    await waitFor(() => !q("[aria-busy=true]") && qa(".sources a").length === 1);
    expect(q<HTMLAnchorElement>(".sources a")?.href).toBe("https://go.dev/dl/");
  });

  // An unsent message holds the page: a link and another conversation ask
  // first, a failed send keeps holding, a sent message lets the page go.
  it("asks before an unsent message is dropped, until it is sent", async () => {
    const api = server({ sessions: [session()] });
    let refuse = true;
    api.on("POST /api/chat/sessions/new/messages", () => (refuse ? fail("internal", "The server could not start the turn.") : ok({ sessionId: "s9", turnId: "t9" })));
    api.on("GET /api/chat/sessions/s9", () => ok({ session: session({ id: "s9" }), messages: [], turns: [] }));
    api.on("GET /api/chat/sessions/s9/events", () => new Response("", { status: 200 }));
    window.history.replaceState(null, "", "/ask");
    await mount(<AskAgent />);
    await type(await waitFor(() => q("textarea")), "Hello agent");
    await click(await waitFor(() => q<HTMLButtonElement>(".rail-session")));
    await click(await waitFor(() => button("Cancel")));
    await submit(q("form.composer"));
    await waitFor(() => api.callsTo("POST /api/chat/sessions/new/messages").length === 1 && !q("[aria-busy=true]"));
    await act(async () => navigate("/tracker"));
    await click(await waitFor(() => button("Cancel")));
    expect(window.location.pathname + window.location.search).toBe("/ask");
    refuse = false;
    await submit(q("form.composer"));
    await waitFor(() => window.location.search === "?conversation=s9" && q<HTMLTextAreaElement>("textarea")?.value === "");
    await act(async () => navigate("/tracker"));
    expect(window.location.pathname).toBe("/tracker");
    expect(q("dialog")).toBeNull();
  });

  // The Answer Bank hands the user here with the start of a message.
  it("opens a new conversation with a message another page handed over, sent only by the user, and takes it out of the address", async () => {
    const api = server({ sessions: [session()] });
    window.history.replaceState(null, "", `/ask?draft=${encodeURIComponent("Add to my answer bank: ")}`);
    await mount(<AskAgent />);
    await waitFor(() => q<HTMLTextAreaElement>("textarea")?.value === "Add to my answer bank: ");
    expect(window.location.search).toBe("");
    expect(api.callsTo("POST /api/chat/sessions/new/messages")).toHaveLength(0);
    await click(await waitFor(() => q<HTMLButtonElement>(".rail-session")));
    await click(await waitFor(() => button("Leave")));
    await waitFor(() => window.location.search === "?conversation=s1");
    expect(q<HTMLTextAreaElement>("textarea")?.value).toBe("");
  });

  it("sends guidance as steering while a turn runs and offers Cancel turn", async () => {
    const api = server({ sessions: [session()] });
    const stream = chatStream();
    api.on("GET /api/chat/sessions/s1", () => ok(detail({ activeTurnId: "t1" })));
    api.on("GET /api/chat/sessions/s1/events", () => stream.response());
    api.on("POST /api/chat/sessions/s1/steer", () => ok({ turnId: "t1", queued: 1 }));
    api.on("POST /api/chat/sessions/s1/cancel", () => ok({ turnId: "t1", runs: [], note: "The turn was cancelled." }));
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    await waitFor(() => button("Send guidance"));
    await waitFor(() => api.callsTo("GET /api/chat/sessions/s1/events").length === 1);
    await type(q("textarea"), "Focus on remote roles");
    await submit(q("form.composer"));
    await waitFor(() => api.callsTo("POST /api/chat/sessions/s1/steer").length === 1);
    expect(api.callsTo("POST /api/chat/sessions/s1/steer")[0].body).toEqual({ text: "Focus on remote roles" });
    expect(api.callsTo("POST /api/chat/sessions/s1/messages")).toHaveLength(0);
    await waitFor(() => text().includes("Guidance queued. 1 waiting"));
    await click(button("Cancel turn"));
    await waitFor(() => api.callsTo("POST /api/chat/sessions/s1/cancel").length === 1);
    await waitFor(() => text().includes("The turn was cancelled."));
  });

  it("sends a message once when Enter is pressed again before the server answers, and holds Send meanwhile", async () => {
    const api = server({ sessions: [session()] });
    let answer: () => void = () => {};
    api.on("GET /api/chat/sessions/s1", () => ok(detail()));
    api.on("POST /api/chat/sessions/s1/messages", () => new Promise((r) => (answer = () => r(ok({ sessionId: "s1", turnId: "t2" })))));
    api.on("GET /api/chat/sessions/s1/events", () => new Response("", { status: 200 }));
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    const area = await waitFor(() => q<HTMLTextAreaElement>("textarea"));
    await type(area, "Next question");
    await act(async () => {
      area.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      area.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    await press("Enter", {}, area);
    expect(button("Send")?.disabled).toBe(true);
    answer();
    await waitFor(() => button("Send")?.disabled === true && area.value === "");
    expect(api.callsTo("POST /api/chat/sessions/s1/messages")).toHaveLength(1);
  });

  it("renames a conversation and deletes one only after a confirmation", async () => {
    const api = server({ sessions: [session()] });
    api.on("GET /api/chat/sessions/s1", () => ok(detail()));
    api.on("PATCH /api/chat/sessions/s1", () => ok(session({ title: "Renamed" })));
    api.on("DELETE /api/chat/sessions/s1", () => ok({ id: "s1" }));
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    await click(await waitFor(() => button("Rename")));
    await type(q("#" + CSS.escape(q<HTMLInputElement>("input[maxlength='120']")!.id)), "Renamed");
    await submit(q("form.inline-form"));
    await waitFor(() => api.callsTo("PATCH /api/chat/sessions/s1").length === 1);
    expect(api.callsTo("PATCH /api/chat/sessions/s1")[0].body).toEqual({ title: "Renamed" });
    await click(await waitFor(() => button("Delete")));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/chat/sessions/s1")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Delete conversation")!);
    await waitFor(() => api.callsTo("DELETE /api/chat/sessions/s1").length === 1);
    await waitFor(() => !window.location.search.includes("conversation=s1"));
  });

  it("shows a held tool call as waiting for the user and reloads the cards", async () => {
    const api = server({ sessions: [session()] });
    const stream = chatStream();
    api.on("GET /api/chat/sessions/s1", () => ok(detail({ activeTurnId: "t1" })));
    api.on("GET /api/chat/sessions/s1/events", () => stream.response());
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    await waitFor(() => api.callsTo("GET /api/chat/sessions/s1/events").length === 1);
    const before = api.callsTo("GET /api/agent/confirmations").length;
    stream.push({ type: "tool.call", toolCallId: "c2", tool: "autofill_application", class: "external-action", phrase: "Filling the application" });
    stream.push({ type: "tool.held", toolCallId: "c2", tool: "autofill_application", code: "submit_unnamed", reason: "The posting was not named.", confirmationId: "card-1" });
    await waitFor(() => text().includes("waiting for you"));
    await waitFor(() => qa("[aria-busy=true] .chip").length === 2);
    expect(qa("[aria-busy=true] .chip").map((c) => c.textContent)).toEqual(["external-action", "waiting for you"]);
    await waitFor(() => api.callsTo("GET /api/agent/confirmations").length > before);
  });
});

describe("Ask Agent: the open conversation's request panel", () => {
  const detail = (extra: Record<string, unknown> = {}) => ({ session: session(), messages: [message({ id: 1, role: "user", text: "Evaluate the posting" })], turns: [], ...extra });
  const options = () => qa<HTMLButtonElement>("section.request button.request-option");
  const open = async (cards: ReturnType<typeof card>[], extra: Record<string, unknown> = {}) => {
    const api = server({ sessions: [session()], cards });
    api.on("GET /api/chat/sessions/s1", () => ok(detail(extra)));
    api.on("POST /api/chat/sessions/s1/messages", () => ok({ sessionId: "s1", turnId: "t2" }));
    api.on("POST /api/chat/sessions/s1/steer", () => ok({ turnId: "t1", queued: 1 }));
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    await waitFor(() => q("section.request"));
    return api;
  };

  it("moves with Up and Down and chooses the highlighted answer with Enter", async () => {
    const api = await open([card()]);
    await press("ArrowDown", {}, document.activeElement!);
    await press("ArrowDown", {}, document.activeElement!);
    await press("ArrowUp", {}, document.activeElement!);
    expect(document.activeElement).toBe(options()[1]);
    expect(options()[1].hasAttribute("data-active")).toBe(true);
    await press("Enter", {}, document.activeElement!);
    await waitFor(() => api.callsTo("POST /api/agent/confirmations/card-1/approve-standing").length === 1);
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve-standing")[0].body).toEqual({ scope: "conversation" });
    await waitFor(() => !q("section.request") && q("form.composer")?.hidden === false);
    expect(text()).toContain("evaluate_job: approved.");
  });

  it("declines and sends what to do instead as a new message when no turn runs", async () => {
    const api = await open([card()]);
    await press("5", {}, document.activeElement!);
    const field = q<HTMLInputElement>("section.request input")!;
    expect(document.activeElement).toBe(field);
    await type(field, "Evaluate the second posting instead");
    await press("Enter", {}, field);
    await waitFor(() => api.callsTo("POST /api/chat/sessions/s1/messages").length === 1);
    expect(api.callsTo("POST /api/agent/confirmations/card-1/decline")).toHaveLength(1);
    expect(api.callsTo("POST /api/chat/sessions/s1/messages")[0].body).toEqual({ text: "Evaluate the second posting instead" });
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve")).toHaveLength(0);
  });

  it("never approves a submit with Enter or a number: both only open the submit confirmation", async () => {
    const api = await open([card({ tier: 4, scopes: ["once", "conversation"], tool: "autofill_application", code: "submit_unnamed" })]);
    expect(q("section.request h3")?.textContent).toBe("Allow the agent to submit this application?");
    // A submit offers no standing scope, whatever the card lists.
    expect(options().map((b) => b.querySelector(".request-label")?.textContent)).toEqual(["Yes, review the submit", "No"]);
    await press("Enter", {}, options()[0]);
    await waitFor(() => q("dialog"));
    expect(q("dialog")?.textContent).toContain("submits the application to Northwind Example");
    expect(document.activeElement?.textContent).toBe("Cancel");
    // Enter again lands on Cancel, and Esc closes the dialog, not the panel.
    await press("Enter", {}, document.activeElement!);
    await press("Escape", {}, q("dialog")!);
    await waitFor(() => !q("dialog"));
    expect(q("section.request")).not.toBeNull();
    await press("1", {}, options()[0]);
    await waitFor(() => q("dialog"));
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve")).toHaveLength(0);
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve-standing")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Approve and submit")!);
    await waitFor(() => api.callsTo("POST /api/agent/confirmations/card-1/approve").length === 1);
  });

  it("does not take the keys from a message being typed when a card arrives", async () => {
    let cards: ReturnType<typeof card>[] = [];
    const api = server({ sessions: [session()] });
    const stream = chatStream();
    api.on("GET /api/agent/confirmations", () => ok({ cards }));
    api.on("GET /api/chat/sessions/s1", () => ok(detail({ activeTurnId: "t1" })));
    api.on("GET /api/chat/sessions/s1/events", () => stream.response());
    window.history.replaceState(null, "", "/ask?conversation=s1");
    await mount(<AskAgent />);
    const area = await waitFor(() => q<HTMLTextAreaElement>("textarea"));
    await waitFor(() => api.callsTo("GET /api/chat/sessions/s1/events").length === 1);
    area.focus();
    await type(area, "Also check the salary");
    cards = [card()];
    stream.push({ type: "tool.call", toolCallId: "c1", tool: "evaluate_job", class: "external-action", phrase: "Starting an evaluation" });
    stream.push({ type: "tool.held", toolCallId: "c1", tool: "evaluate_job", code: "taint", reason: "Outside content was read in this turn.", confirmationId: "card-1" });
    await waitFor(() => q("section.request"));
    expect(q("section.request")?.contains(document.activeElement)).toBe(false);
    expect(document.activeElement).not.toBe(area);
    await press("1", {}, document.activeElement ?? window);
    await press("Enter", {}, document.activeElement ?? window);
    await settle();
    expect(api.callsTo("POST /api/agent/confirmations/card-1/approve")).toHaveLength(0);
    // The draft is kept for when the panel goes.
    expect(q<HTMLTextAreaElement>("textarea")?.value).toBe("Also check the salary");
  });

});
