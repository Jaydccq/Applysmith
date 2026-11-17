import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startEditAI, type EditAI, type EditAIDeps } from "../src/widgets/editai";
import { watchPage } from "../src/widgets/main";

let ctl: EditAI | undefined;
let compose: ReturnType<typeof vi.fn>;
let cancel: ReturnType<typeof vi.fn>;
let flags: { off: boolean; editWithAI: boolean };

function deps(): EditAIDeps {
  return { doc: document, win: window, changes: watchPage(document), flags: async () => flags, posting: async () => ({ company: "Example Corp", role: "Engineer", description: "Build things" }), compose: compose as EditAIDeps["compose"], cancel: cancel as EditAIDeps["cancel"] };
}

const why = () => document.getElementById("why") as HTMLTextAreaElement;
/** The widget lives in a closed shadow root; the tests reach it through the one test hook the platform gives: attachShadow is patched below. */
const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
const sparks = () => roots.flatMap((r) => [...r.querySelectorAll<HTMLButtonElement>(".spark")]);
const pop = () => roots.map((r) => r.querySelector(".pop")).find(Boolean) as HTMLElement | undefined;
const popBtn = (label: string) => [...pop()!.querySelectorAll("button")].find((b) => b.textContent === label)!;

async function start() {
  ctl = startEditAI(deps());
  await vi.waitFor(() => expect(sparks().length).toBeGreaterThan(0));
}

beforeEach(() => {
  roots.length = 0;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL("https://example.com/careers/apply");
  document.body.innerHTML = `<form><label for="why">Why us?</label><textarea id="why" rows="6" maxlength="400"></textarea><input type="search" id="q"><textarea id="note" rows="1"></textarea></form>`;
  flags = { off: false, editWithAI: true };
  compose = vi.fn(async () => ({ outcome: "filled-model", answer: "Because I like reliable systems." }));
  cancel = vi.fn(async () => {});
});
afterEach(() => {
  ctl?.destroy();
  ctl = undefined;
  Element.prototype.attachShadow = realAttach;
});

describe("Edit with AI", () => {
  it("composes nothing until a button is pressed and Generate is chosen", async () => {
    await start();
    expect(compose).not.toHaveBeenCalled();
    sparks()[0]!.click();
    expect(compose).not.toHaveBeenCalled();
  });

  it("generates into the control for that request and Undo restores the earlier text", async () => {
    await start();
    why().value = "I like systems.";
    sparks()[0]!.click();
    const instruction = pop()!.querySelector("input")!;
    instruction.value = "make it formal";
    popBtn("Generate").click();
    await vi.waitFor(() => expect(why().value).toBe("Because I like reliable systems."));
    expect(compose).toHaveBeenCalledWith(expect.objectContaining({ question: "Why us?", maxLength: 400, current: "I like systems.", instruction: "make it formal", posting: expect.objectContaining({ company: "Example Corp" }) }));
    popBtn("Undo").click();
    await vi.waitFor(() => expect(why().value).toBe("I like systems."));
  });

  it("pressing the button again while a request runs keeps its popover live, and Close cancels the request and writes nothing", async () => {
    let answer: (v: unknown) => void = () => {};
    compose = vi.fn(() => new Promise((r) => (answer = r)));
    await start();
    sparks()[0]!.click();
    popBtn("Generate").click();
    await vi.waitFor(() => expect(compose).toHaveBeenCalledTimes(1));
    sparks()[0]!.click();
    answer({ outcome: "filled-model", answer: "Because I like reliable systems." });
    await vi.waitFor(() => expect(popBtn("Generate").disabled).toBe(false));
    expect(pop()!.textContent).toMatch(/Written to the field/);

    why().value = "";
    popBtn("Generate").click();
    await vi.waitFor(() => expect(compose).toHaveBeenCalledTimes(2));
    popBtn("Close").click();
    expect(cancel).toHaveBeenCalledWith('edit:[id="why"]');
    answer({ outcome: "filled-model", answer: "Late text." });
    await new Promise((r) => setTimeout(r, 20));
    expect(why().value).toBe("");
    sparks()[0]!.click();
    expect(popBtn("Generate").disabled).toBe(false);
  });

  it("shows an error in the popover, leaves the field alone and does not retry", async () => {
    compose = vi.fn(async () => {
      throw new Error("The application did not answer.");
    });
    await start();
    why().value = "keep me";
    sparks()[0]!.click();
    popBtn("Generate").click();
    await vi.waitFor(() => expect(pop()!.textContent).toMatch(/did not answer/));
    expect(compose).toHaveBeenCalledTimes(1);
    expect(why().value).toBe("keep me");
  });

  it("says there is no grounded answer and writes nothing when the API refuses", async () => {
    compose = vi.fn(async () => ({ outcome: "abstained", reason: "model-abstain" }));
    await start();
    sparks()[0]!.click();
    popBtn("Generate").click();
    await vi.waitFor(() => expect(pop()!.textContent).toMatch(/No grounded answer/));
    expect(why().value).toBe("");
  });

  it("shows no button at all while the master switch is off, and none on a host switched off", async () => {
    flags = { off: false, editWithAI: false };
    ctl = startEditAI(deps());
    await new Promise((r) => setTimeout(r, 30));
    expect(sparks()).toHaveLength(0);
    flags = { off: true, editWithAI: true };
    await ctl.refresh();
    expect(sparks()).toHaveLength(0);
  });
});
