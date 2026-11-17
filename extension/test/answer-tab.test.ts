import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mountAnswer } from "../src/panel/answer";
import { clearPending, pendingCount } from "../src/widgets/drafts";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({
  send: (r: unknown) => send(r),
  failureOf: (e: { failure?: unknown }) => e.failure ?? { code: "internal", message: "Something went wrong." },
}));

const POSTING = `Backend Engineer. Responsibilities: build services. Qualifications: Go. Salary and benefits. Full-time, remote. Apply now. ${"Details. ".repeat(50)}`;
let el: HTMLElement;
let reply: (req: { kind: string; [k: string]: unknown }) => unknown;

const click = (label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === label)!.click();
const button = (label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === label) as HTMLButtonElement | undefined;
const why = () => document.getElementById("why") as HTMLTextAreaElement;
const composeCalls = () => send.mock.calls.map((c) => c[0] as { kind: string; [k: string]: unknown }).filter((r) => r.kind === "compose.run");
const settle = () => vi.waitFor(() => expect(el.querySelector("[data-status=requesting]")).toBeNull());

async function open() {
  const tab = mountAnswer(el);
  tab.setConnection("connected");
  await vi.waitFor(() => expect(el.querySelectorAll(".q").length).toBeGreaterThan(0));
  return tab;
}

beforeEach(() => {
  clearPending();
  send.mockReset();
  document.body.innerHTML = `<div id="t"></div><main>${POSTING}
    <form>
      <label for="first">First name</label><input id="first" type="text" value="Alex">
      <label for="visa">Will you need sponsorship?</label><input id="visa" type="text">
      <label for="why">Why do you want this role?</label><textarea id="why" rows="6" maxlength="500"></textarea>
    </form></main>`;
  el = document.getElementById("t")!;
  reply = (req) => {
    if (req.kind === "fill.status") return { phase: "done", frames: [], rows: [{ label: "First name", lane: "bank", outcome: "filled", required: true, review: false }] };
    if (req.kind === "compose.run") return { outcome: "filled-model", answer: "I like reliable systems. I have built them for years." };
    return undefined;
  };
  send.mockImplementation(async (req) => reply(req));
});
afterEach(() => clearPending());

describe("Answer tab", () => {
  it("settles the draft of a question that left the page, and every draft of the tab when it is disposed", async () => {
    const draftWhy = () => [...el.querySelectorAll(".card")].find((c) => c.querySelector("strong")!.textContent === "Why do you want this role?")?.querySelector("button");
    const tab = await open();
    draftWhy()!.click();
    await settle();
    expect(pendingCount()).toBe(1);
    why().remove();
    click("Read the page again");
    // The tab here is not in the panel's closed shadow root, so its own fields are read as questions too.
    await vi.waitFor(() => expect(draftWhy()).toBeUndefined());
    expect(pendingCount()).toBe(0);
    document.querySelector("form")!.insertAdjacentHTML("beforeend", `<textarea id="why" rows="6"></textarea>`);
    click("Read the page again");
    await vi.waitFor(() => expect(draftWhy()).toBeDefined());
    draftWhy()!.click();
    await settle();
    expect(pendingCount()).toBe(1);
    tab.dispose();
    expect(pendingCount()).toBe(0);
  });

  it("starts no draft by itself, drafts into a preview on Draft and leaves the page's field unchanged", async () => {
    await open();
    await new Promise((r) => setTimeout(r, 60));
    expect(composeCalls()).toHaveLength(0);
    click("Draft");
    await settle();
    expect(el.querySelector<HTMLTextAreaElement>(".preview")!.value).toContain("I like reliable systems.");
    expect(why().value).toBe("");
    expect(pendingCount()).toBe(1);
    expect(el.querySelector("[data-unreviewed]")!.textContent).toBe("1 unreviewed draft");
  });

  it("sends the question, the limit and the posting context with a draft request", async () => {
    await open();
    click("Draft");
    await settle();
    expect(composeCalls()[0]).toMatchObject({ question: "Why do you want this role?", maxLength: 500, key: expect.any(String) });
    expect((composeCalls()[0]!.posting as { description: string }).description).toContain("Responsibilities");
  });

  it("writes the text and reads it back only when Fill is pressed", async () => {
    await open();
    click("Draft");
    await settle();
    click("Fill");
    await vi.waitFor(() => expect(el.querySelector("[data-status=filled]")).not.toBeNull());
    expect(why().value).toBe("I like reliable systems. I have built them for years.");
    expect(pendingCount()).toBe(0);
  });

  it("says there is no grounded answer and writes nothing when the API refuses", async () => {
    reply = (req) => (req.kind === "compose.run" ? { outcome: "abstained", reason: "model-abstain" } : req.kind === "fill.status" ? { phase: "idle", frames: [] } : undefined);
    await open();
    click("Draft");
    await settle();
    expect(el.textContent).toMatch(/No grounded answer/);
    expect(why().value).toBe("");
    expect(pendingCount()).toBe(0);
    expect(button("Fill")!.disabled).toBe(true);
  });

  it("flags a draft over the control's limit and fills no more than the limit, ending at a sentence", async () => {
    const sentence = "I have built reliable services for years. ";
    reply = (req) => (req.kind === "compose.run" ? { outcome: "filled-model", answer: sentence.repeat(20).trim() } : req.kind === "fill.status" ? { phase: "idle", frames: [] } : undefined);
    await open();
    click("Draft");
    await settle();
    expect(el.textContent).toMatch(/Over the limit by \d+ characters/);
    expect(el.textContent).toMatch(/of 500 characters/);
    click("Fill");
    await vi.waitFor(() => expect(why().value).not.toBe(""));
    expect(why().value.length).toBeLessThanOrEqual(500);
    expect(why().value.endsWith(".")).toBe(true);
  });

  it("fills the draft as the person edited it, and keeps their place while another card redraws the tab", async () => {
    document.body.innerHTML = `<div id="t"></div><main>${POSTING}<form>
      <label for="why">Why do you want this role?</label><textarea id="why" rows="6"></textarea>
      <label for="more">Anything else?</label><textarea id="more" rows="6"></textarea></form></main>`;
    el = document.getElementById("t")!;
    let release: (v: unknown) => void = () => {};
    await open();
    click("Draft");
    await settle();
    const editor = () => el.querySelector<HTMLTextAreaElement>("textarea")!;
    editor().focus();
    editor().value = "I like reliable systems.";
    editor().setSelectionRange(2, 6);
    editor().dispatchEvent(new Event("input"));
    reply = (req) => (req.kind === "compose.run" ? new Promise((r) => (release = r)) : undefined);
    el.querySelectorAll<HTMLElement>(".card")[1]!.querySelector<HTMLButtonElement>("button")!.click();
    await vi.waitFor(() => expect(composeCalls()).toHaveLength(2));
    expect(document.activeElement).toBe(editor());
    release({ outcome: "filled-model", answer: "Nothing else." });
    await vi.waitFor(() => expect(el.querySelectorAll("textarea")).toHaveLength(2));
    expect(document.activeElement).toBe(editor());
    expect([editor().selectionStart, editor().selectionEnd]).toEqual([2, 6]);
    expect(el.textContent).toContain("4 words, 24 characters");
    click("Fill");
    await vi.waitFor(() => expect(why().value).toBe("I like reliable systems."));
  });

});
