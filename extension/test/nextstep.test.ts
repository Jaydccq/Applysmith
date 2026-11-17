import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mountAutofill } from "../src/panel/autofill";
import type { FillView } from "../src/shared/messages";
import { advanceThisFrame, embeddedFormPresent, hasProgressControl } from "../src/widgets/nextstep";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({
  send: (r: unknown) => send(r),
  failureOf: (e: { failure?: unknown }) => e.failure ?? { code: "internal", message: "Something went wrong." },
}));

const PAGER = `<div class="srccarPager"><div style="display:block"><div id="go" data-dojo-attach-point="thePagerSingleGo"></div></div></div>`;

describe("Next step: embedded form detection", () => {
  const frames = (...srcs: (string | null)[]) => {
    document.body.innerHTML = srcs.map((s) => (s === null ? "<iframe></iframe>" : `<iframe src="${s}"></iframe>`)).join("");
    return document;
  };
  it("counts an ATS iframe", () => expect(embeddedFormPresent(frames("https://jobs.jobvite.com/example/apply"))).toBe(true));
  it("ignores captcha, analytics and video frames", () => expect(embeddedFormPresent(frames("https://newassets.hcaptcha.com/captcha/v1", "https://www.youtube.com/embed/x", "https://challenges.cloudflare.com/cdn-cgi"))).toBe(false));
});

describe("Next step: the press", () => {
  const log = (el: Element) => {
    const seen: string[] = [];
    for (const t of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el.addEventListener(t, () => seen.push(t));
    return seen;
  };

  it("presses a Continue button once with a plain click", () => {
    document.body.innerHTML = `<button id="b">Continue</button>`;
    const seen = log(document.getElementById("b")!);
    expect(hasProgressControl(document)).toBe(true);
    expect(advanceThisFrame(document)).toEqual({ advanced: true, label: "Continue" });
    expect(seen).toEqual(["click"]);
  });

  it("presses a textless Dojo pager with the full pointer gesture", () => {
    document.body.innerHTML = PAGER;
    const seen = log(document.getElementById("go")!);
    expect(hasProgressControl(document)).toBe(true);
    expect(advanceThisFrame(document)).toMatchObject({ advanced: true });
    expect(seen).toEqual(["pointerdown", "mousedown", "pointerup", "mouseup", "click"]);
  });

  it.each(["Submit application", "Withdraw application", "Discard", "Delete draft"])("never presses %s", (label) => {
    document.body.innerHTML = `<button id="b">${label}</button>`;
    const seen = log(document.getElementById("b")!);
    expect(hasProgressControl(document)).toBe(false);
    expect(advanceThisFrame(document)).toEqual({ advanced: false, label: null });
    expect(seen).toEqual([]);
  });

  it("presses the progress control and not the Submit beside it", () => {
    document.body.innerHTML = `<button id="s">Submit application</button><button id="n">Next</button>`;
    const submit = log(document.getElementById("s")!);
    const next = log(document.getElementById("n")!);
    advanceThisFrame(document);
    expect(submit).toEqual([]);
    expect(next).toEqual(["click"]);
  });

  it("leaves a pager alone while the step shows a Submit control", () => {
    document.body.innerHTML = `${PAGER}<button id="s">Submit application</button>`;
    const seen = log(document.getElementById("go")!);
    expect(advanceThisFrame(document).advanced).toBe(false);
    expect(seen).toEqual([]);
  });

  it("does not widen to an unrecognized textless control or a disabled one", () => {
    document.body.innerHTML = `<div class="srccarPager"><div data-dojo-attach-point="thePagerSingleFinish" id="x"></div></div><button disabled>Next</button>`;
    const seen = log(document.getElementById("x")!);
    expect(advanceThisFrame(document).advanced).toBe(false);
    expect(seen).toEqual([]);
  });
});

describe("Next step: the panel button", () => {
  let el: HTMLElement;
  const idle: FillView = { phase: "idle", frames: [] };
  beforeEach(() => {
    vi.useFakeTimers();
    send.mockReset();
    send.mockResolvedValue(idle);
    // The real panel sits in a closed shadow root; the marker keeps its own button out of the page scan.
    document.body.innerHTML = '<div data-applysmith><div id="t"></div></div><main id="page"></main>';
    el = document.getElementById("t")!;
  });
  afterEach(() => vi.useRealTimers());

  const mount = async () => {
    const tab = mountAutofill(el);
    tab.setConnection("connected");
    await vi.advanceTimersByTimeAsync(0);
    return tab;
  };
  const button = () => [...el.querySelectorAll("button")].find((b) => b.textContent === "Next step")!;
  const page = (html: string) => (document.getElementById("page")!.innerHTML = html);
  const stepCalls = () => send.mock.calls.filter(([r]) => (r as { kind: string }).kind === "step.advance").length;

  it("is visible for an embedded application form and sends the press to the frame that owns the control", async () => {
    page('<iframe src="https://jobs.jobvite.com/example/apply"></iframe>');
    send.mockImplementation(async (r: { kind: string }) => (r.kind === "step.advance" ? { advanced: true, label: "Next" } : idle));
    await mount();
    expect(button().hidden).toBe(false);
    button().click();
    await vi.advanceTimersByTimeAsync(0);
    expect(stepCalls()).toBe(1);
    expect(el.textContent).toContain('Pressed "Next".');
    await vi.advanceTimersByTimeAsync(1500);
  });

  it("presses the top frame's own control without asking the worker", async () => {
    page('<button id="n">Continue</button><iframe src="https://jobs.jobvite.com/example/apply"></iframe>');
    const seen: string[] = [];
    document.getElementById("n")!.addEventListener("click", () => seen.push("click"));
    await mount();
    button().click();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen).toEqual(["click"]);
    expect(stepCalls()).toBe(0);
    await vi.advanceTimersByTimeAsync(1500);
  });

  it("presses once for a double click, then can be pressed again", async () => {
    page('<button id="n">Next</button>');
    const seen: string[] = [];
    document.getElementById("n")!.addEventListener("click", () => seen.push("click"));
    await mount();
    button().click();
    button().click();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen).toEqual(["click"]);
    expect(button().disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(1500);
    expect(button().disabled).toBe(false);
    button().click();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen).toEqual(["click", "click"]);
    await vi.advanceTimersByTimeAsync(1500);
  });

  it("is disabled while a fill is in flight", async () => {
    page('<button id="n">Next</button>');
    send.mockResolvedValue({ phase: "running", frames: [], elapsedMs: 1000 } satisfies FillView);
    await mount();
    send.mockClear();
    const seen: string[] = [];
    document.getElementById("n")!.addEventListener("click", () => seen.push("click"));
    // The status read at mount reports the fill running.
    expect(button().disabled).toBe(true);
    button().click();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen).toEqual([]);
  });

  it("never presses Submit from the button", async () => {
    page('<button id="s">Submit application</button><iframe src="about:blank"></iframe>');
    const seen: string[] = [];
    document.getElementById("s")!.addEventListener("click", () => seen.push("click"));
    await mount();
    expect(button().hidden).toBe(true);
    button().click();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen).toEqual([]);
    expect(stepCalls()).toBe(0);
  });
});
