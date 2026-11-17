import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type Handler } from "../testing/api";
import { button, click, mount, q, settle, text, waitFor } from "../testing/dom";
import type { BoardEntry, ComputerUseStatus, DesktopEvent } from "../wire";
import AgentControl from "./AgentControl";
import Home from "./Home";
import { RunDetail } from "./runs/Detail";
import { canLetAgentFinish } from "./runs/Takeover";

// The shapes internal/api/wire/computeruse.go sends.
const status = (over: Partial<ComputerUseStatus> = {}): ComputerUseStatus => ({
  health: "ready", accessibility: true, screenRecording: true, servicePaused: false, backgroundOff: false, paused: false, imagesToModel: false, ...over,
});
const event = (over: Partial<DesktopEvent> & { id: string }): DesktopEvent => ({
  at: "2026-10-21T17:42:00.000Z", sessionId: "ses_1", turnId: "turn_1", tool: "desktop_click", app: "Google Chrome", window: "Apply - Northwind Example",
  role: "AXButton", control: "Next", verdict: "allow", ...over,
});

function server(s: ComputerUseStatus, events: DesktopEvent[] = [], extra: Record<string, Handler> = {}) {
  return fakeApi({ "GET /api/computer-use": () => ok(s), "GET /api/computer-use/activity": () => ok({ events }), ...extra });
}

// chatStream answers a conversation's event stream: its first event says
// whether a turn runs, and the stream of a running turn stays open.
function chatStream(activeTurnId?: string): Response {
  const first = new TextEncoder().encode(`id: e-1\nevent: chat\ndata: ${JSON.stringify(activeTurnId ? { type: "state", activeTurnId } : { type: "state" })}\n\n`);
  return new Response(new ReadableStream<Uint8Array>({ start(c) { c.enqueue(first); if (!activeTurnId) c.close(); } }), { headers: { "Content-Type": "text/event-stream" } });
}

const pause = () => q<HTMLInputElement>("input[role=switch]");

describe("Agent control", () => {
  it("posts Pause and shows the state the server answered, not the press", async () => {
    let kept = status();
    let refuse = true;
    const api = server(kept, [], {
      "GET /api/computer-use": () => ok(kept),
      "POST /api/computer-use/pause": (c) => {
        if (refuse) return fail("internal", "the pause could not be stored");
        kept = { ...kept, paused: (c.body as { paused: boolean }).paused };
        return ok(kept);
      },
    });
    await mount(<AgentControl />);
    await click(await waitFor(pause));
    await waitFor(() => text().includes("the pause could not be stored"));
    expect(api.callsTo("POST /api/computer-use/pause").map((c) => c.body)).toEqual([{ paused: true }]);
    expect(pause()?.checked).toBe(false);
    refuse = false;
    await click(pause());
    await waitFor(() => pause()?.checked);
    expect(text()).toContain("Pause is on: every desktop action is refused.");
  });

  it("offers Stop only while the newest event's conversation runs a turn, and cancels that turn", async () => {
    const events = [event({ id: "e2", sessionId: "ses_2", turnId: "turn_2" }), event({ id: "e1" })];
    const cancel = { "POST /api/chat/sessions/ses_2/cancel": () => ok({ turnId: "turn_2", runs: [], note: "The turn was cancelled." }) };
    const idle = server(status(), events, { "GET /api/chat/sessions/ses_2/events": () => chatStream(), ...cancel });
    await mount(<AgentControl />);
    await waitFor(() => idle.callsTo("GET /api/chat/sessions/ses_2/events").length === 1);
    await settle();
    expect(button("Stop the turn")).toBeNull();

    const busy = server(status(), events, { "GET /api/chat/sessions/ses_2/events": () => chatStream("turn_2"), ...cancel });
    await mount(<AgentControl />);
    await click(await waitFor(() => button("Stop the turn")));
    await waitFor(() => text().includes("The turn was cancelled."));
    expect(busy.callsTo("POST /api/chat/sessions/ses_2/cancel")).toHaveLength(1);
    expect(busy.calls.filter((c) => c.path.startsWith("/api/chat/sessions/ses_1"))).toHaveLength(0);
  });

  it("says which of the three pauses is on, and what turns each off", async () => {
    const rows: [Partial<ComputerUseStatus>, string, string][] = [
      [{ paused: true }, "Paused in Agent control", "turn Pause off to let the agent act again"],
      [{ backgroundOff: true }, "Background control off", "Turn on Allow background control"],
      [{ servicePaused: true, paused: true }, "Paused in PersonalCU", "Resume control from its menu bar icon"],
    ];
    for (const [over, label, help] of rows) {
      server(status({ health: "paused", ...over }));
      await mount(<AgentControl />);
      await waitFor(() => text().includes(label));
      expect(text()).toContain(help);
      if (over.paused && !over.servicePaused) expect(text()).not.toContain("Allow background control");
    }
  });

  it("lists a key on its caps and a scroll's direction, narrows to the refused steps, and outlines the control an action named", async () => {
    const png = () => new Response(new Blob(["png"], { type: "image/png" }));
    const api = server(
      status(),
      [
        event({ id: "e5", tool: "desktop_keys", role: undefined, control: undefined, detail: "shift+tab" }),
        event({ id: "e4", tool: "desktop_scroll", detail: "down, 2 pages" }),
        event({ id: "e3", verdict: "deny", reason: "the latest look is older than 30 seconds, so the agent must look again", role: undefined, control: undefined }),
        event({ id: "e2", shotId: "s1", target: { x: 0.5, y: 0.25, width: 0.1, height: 0.05 } }),
        event({ id: "e1", tool: "desktop_look", role: undefined, control: undefined, shotId: "s1" }),
      ],
      { "GET /api/computer-use/shots/s1": png },
    );
    await mount(<AgentControl />);
    await waitFor(() => document.querySelectorAll(".desk-shot img").length === 2);
    expect([...document.querySelectorAll("kbd")].map((k) => k.textContent)).toEqual(["Shift", "Tab"]);
    expect(text()).toContain("Scroll down, 2 pages in Google Chrome");
    // The look and the click decided on it show one picture, read once; only the click has the outline.
    expect(api.callsTo("GET /api/computer-use/shots/s1")).toHaveLength(1);
    const marks = [...document.querySelectorAll<HTMLElement>(".desk-mark")];
    expect(marks.map((m) => [m.style.left, m.style.top, m.style.width, m.style.height])).toEqual([["50%", "25%", "10%", "5%"]]);

    await click(button("Refused 1"));
    expect(document.querySelectorAll(".desk-event")).toHaveLength(1);
    expect(text()).toContain("1 step, newest first");
    await click(button("All 5"));
    expect(document.querySelectorAll(".desk-event")).toHaveLength(5);
  });
});

describe("Let the agent finish", () => {
  const at = "2026-10-21T17:00:00.000Z";
  // A board entry as internal/api/notify.go draws it: canFocusTab is a kept
  // tab and canDone the server's resume rule.
  const entry = (over: Partial<BoardEntry> = {}): BoardEntry => ({
    id: "run_7", kind: "apply", origin: "user", title: "Apply to Northwind Example", state: "needs-you", reason: "answer", link: "/runs/run_7", revision: 1,
    startedAt: at, updatedAt: at, stateAt: at, canFocusTab: true, canDone: true, ...over,
  });

  it("is offered only for a run waiting on answers in its kept tab that Done could resume", () => {
    const rows: [string, BoardEntry, boolean][] = [
      ["waits on answers in its kept tab", entry(), true],
      ["its tab is no longer kept", entry({ canFocusTab: undefined }), false],
      ["waits on a sign-in instead", entry({ reason: "sign-in" }), false],
      ["a schedule started it, so Done cannot resume it", entry({ origin: "schedule", canDone: undefined }), false],
    ];
    for (const [why, e, offered] of rows) expect(canLetAgentFinish(e), why).toBe(offered);
  });

  it("is offered on Home's Needs you and in the run drawer, and opens the conversation the server started or says why it refused", async () => {
    let refuse = true;
    const api = fakeApi({
      "GET /api/needs-you": () => ok({ items: [{ entry: entry(), waitingSeconds: 60, action: "focus-tab" }], unseen: 0 }),
      "GET /api/runs/run_7": () => ok({ runId: "run_7", kind: "apply", status: "running", createdAt: at, updatedAt: at, entry: entry() }),
      "POST /api/apply/runs/run_7/takeover": () => (refuse ? fail("conflict", "the run no longer waits for answers") : ok({ sessionId: "ses_9" })),
    });
    await mount(<RunDetail id="run_7" onClose={() => {}} onChange={() => {}} />);
    await waitFor(() => q("dialog") && button("Let the agent finish"));

    await mount(<Home />);
    await click(await waitFor(() => button("Let the agent finish")));
    await waitFor(() => text().includes("the run no longer waits for answers"));
    expect(window.location.pathname).toBe("/");
    refuse = false;
    await click(button("Let the agent finish"));
    await waitFor(() => window.location.pathname + window.location.search === "/ask?conversation=ses_9");
    expect(api.callsTo("POST /api/apply/runs/run_7/takeover")).toHaveLength(2);
  });
});
