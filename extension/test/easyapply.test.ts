import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PressDecision } from "../src/shared/messages";
import { MAX_ITERATIONS, SETTLE_MS, SETTLE_POLL_MS, HOSTED_SETTLE_BUDGET_MS, pressAllowed, startWalk, type WalkEnd, type WalkState } from "../src/widgets/easyapply";
import { findApplyModal } from "../src/widgets/easyapply-find";

let states: WalkState[];
let fills: number;
let pending: number;
let claims: number;
let reported: string[];
let decision: () => Promise<PressDecision>;

type Fill = () => Promise<{ ok: boolean; message?: string; filled?: number; drafts?: number }>;

function walk(over: { submit?: boolean; fill?: Fill; settleBudgetMs?: number; sleep?: (ms: number) => Promise<void> } = {}) {
  return startWalk({
    doc: document,
    submit: over.submit ?? false,
    ...(over.settleBudgetMs !== undefined ? { settleBudgetMs: over.settleBudgetMs } : {}),
    fill: over.fill ?? (async () => (fills++, { ok: true })),
    pendingDrafts: () => pending,
    claim: () => (claims++, decision()),
    pressed: async (id) => void reported.push(id),
    report: (s) => states.push(s),
    sleep: over.sleep ?? (async () => {}),
  });
}
const end = async (w: ReturnType<typeof walk>): Promise<WalkEnd> => w.done;

/** A three-step form in a dialog: each Continue shows the next step. */
function steps(labels: string[], final = "Submit application") {
  document.body.innerHTML = `<div role="dialog" id="d"><div id="step"></div></div>`;
  let i = 0;
  const show = () => {
    const last = i === labels.length;
    document.getElementById("step")!.innerHTML = `<p>Step ${i + 1}</p>` + (last ? `<button id="final">${final}</button>` : `<button id="next">${labels[i]}</button>`);
    document.getElementById("next")?.addEventListener("click", () => {
      i++;
      show();
    });
    document.getElementById("final")?.addEventListener("click", () => ((window as unknown as { __submitted: number }).__submitted++));
  };
  show();
}

beforeEach(() => {
  states = [];
  fills = 0;
  pending = 0;
  claims = 0;
  reported = [];
  decision = async () => ({ granted: true, claimId: "claim-1" });
  (window as unknown as { __submitted: number }).__submitted = 0;
});
const submitted = () => (window as unknown as { __submitted: number }).__submitted;

describe("step walker", () => {
  it("fills each step, presses Continue and Next, and stops before the final Submit when the option is off", async () => {
    steps(["Continue", "Next"]);
    expect(await end(walk())).toBe("stopped-before-submit");
    expect(fills).toBe(3);
    expect(submitted()).toBe(0);
    expect(states.at(-1)!.message).toMatch(/Submit application/);
  });

  it("presses the final submit control exactly once when the walk was started with the option", async () => {
    steps(["Continue", "Review"]);
    expect(await end(walk({ submit: true }))).toBe("submitted");
    expect(submitted()).toBe(1);
  });

  it("asks for the press claim only at the final control, never for a step", async () => {
    steps(["Continue", "Next"]);
    expect(await end(walk())).toBe("stopped-before-submit");
    expect(claims).toBe(0);
    steps(["Continue", "Next"]);
    expect(await end(walk({ submit: true }))).toBe("submitted");
    expect(claims).toBe(1);
  });

  it("reports the press with its claim id once, after pressing", async () => {
    steps(["Continue"]);
    expect(await end(walk({ submit: true }))).toBe("submitted");
    expect(submitted()).toBe(1);
    expect(reported).toEqual(["claim-1"]);
    expect(states.map((s) => s.phase)).toContain("claiming");
  });

  describe("the press claim", () => {
    const refused = async (why: () => Promise<PressDecision>, expectedMessage: RegExp) => {
      steps(["Continue"]);
      decision = why;
      expect(await end(walk({ submit: true }))).toBe("claim-refused");
      expect(submitted()).toBe(0);
      expect(reported).toEqual([]);
      expect(states.at(-1)!.message).toMatch(expectedMessage);
    };

    it("does not press when the application does not answer", () => refused(async () => { throw new Error("offline"); }, /not pressed/i));
    it("does not press a duplicate, and says why", () => refused(async () => ({ granted: false, message: "duplicate submit blocked: already submitted. Submit was not pressed." }), /already submitted/));
    it("does not press a grant without a claim id", () => refused(async () => ({ granted: true, claimId: "" }), /not pressed/i));
    it("does not press an answer that is not an explicit grant", () => refused(async () => ({}) as PressDecision, /not pressed/i));

    it("does not press when the person stops while the claim is out", async () => {
      steps(["Continue"]);
      let grant: (d: PressDecision) => void = () => {};
      decision = () => new Promise((r) => (grant = r));
      const w = walk({ submit: true });
      await vi.waitFor(() => expect(claims).toBe(1));
      w.stop();
      grant({ granted: true, claimId: "claim-1" });
      expect(await end(w)).toBe("user-stop");
      expect(submitted()).toBe(0);
      expect(reported).toEqual([]);
    });

    it("does not press when the page changed while the claim was out", async () => {
      steps(["Continue"]);
      let grant: (d: PressDecision) => void = () => {};
      decision = () => new Promise((r) => (grant = r));
      const w = walk({ submit: true });
      await vi.waitFor(() => expect(claims).toBe(1));
      document.getElementById("final")!.textContent = "Withdraw application";
      grant({ granted: true, claimId: "claim-1" });
      expect(await end(w)).toBe("no-control");
      expect(submitted()).toBe(0);
    });

    it("does not ask while a composed draft is pending", async () => {
      steps(["Continue"]);
      pending = 1;
      expect(await end(walk({ submit: true }))).toBe("draft-pending");
      expect(claims).toBe(0);
      expect(submitted()).toBe(0);
    });

    it("still ends submitted when reporting the press fails", async () => {
      steps(["Continue"]);
      const w = startWalk({
        doc: document, submit: true, fill: async () => ({ ok: true }), pendingDrafts: () => 0, claim: async () => ({ granted: true, claimId: "c" }),
        pressed: async () => { throw new Error("worker gone"); }, report: () => {}, sleep: async () => {},
      });
      expect(await end(w)).toBe("submitted");
      expect(submitted()).toBe(1);
    });
  });

  it("refuses to press submit when a step's fill left a composed draft unwritten", async () => {
    steps(["Continue"]);
    let n = 0;
    expect(await end(walk({ submit: true, fill: async () => ({ ok: true, drafts: n++ === 0 ? 1 : 0 }) }))).toBe("draft-pending");
    expect(claims).toBe(0);
    expect(submitted()).toBe(0);
  });

  it("never presses Withdraw, with or without the submit option", async () => {
    document.body.innerHTML = `<div role="dialog"><button id="w">Withdraw application</button></div>`;
    const pressed = vi.fn();
    document.getElementById("w")!.addEventListener("click", pressed);
    expect(await end(walk())).toBe("no-control");
    expect(await end(walk({ submit: true }))).toBe("no-control");
    expect(pressed).not.toHaveBeenCalled();
    expect(pressAllowed("Withdraw application", true)).toBeNull();
    expect(pressAllowed("Discard", true)).toBeNull();
    expect(pressAllowed("Apply and save", true)).toBeNull();
    expect(pressAllowed("Submit application", false)).toBeNull();
    expect(pressAllowed("Save and continue", false)).toBe("progress");
    // The engine's never-press rule, folded: the & form of a terminal apply, and a Submit spelled with a look-alike letter.
    expect(pressAllowed("Submit: apply & save", true)).toBeNull();
    expect(pressAllowed("Submit \u0430pplication", true)).toBeNull();
    expect(pressAllowed("Submit application", true)).toBe("submit");
  });

  it("never takes a date picker's pager or a sign-in choice for the step's progress control", async () => {
    for (const label of ["Next month", "Next year", "Continue with Google"]) expect(pressAllowed(label, false), label).toBeNull();
    document.body.innerHTML = `<div role="dialog"><button id="m">Next month</button><button id="c">Continue</button></div>`;
    const month = vi.fn();
    document.getElementById("m")!.addEventListener("click", month);
    await end(walk());
    expect(month).not.toHaveBeenCalled();
  });

  it("reads every name of a control: a Review button titled Submit application is the final control, not a step", async () => {
    document.body.innerHTML = `<div role="dialog"><button id="r" title="Submit application">Review</button></div>`;
    const pressed = vi.fn();
    document.getElementById("r")!.addEventListener("click", pressed);
    expect(await end(walk())).toBe("stopped-before-submit");
    expect(pressed).not.toHaveBeenCalled();
    document.body.innerHTML = `<span id="n">Submit application</span><div role="dialog"><button id="r" aria-labelledby="n">Continue</button></div>`;
    document.getElementById("r")!.addEventListener("click", pressed);
    expect(await end(walk())).toBe("stopped-before-submit");
    document.body.innerHTML = `<div role="dialog"><button id="r"><span aria-label="Withdraw application">Next</span></button></div>`;
    document.getElementById("r")!.addEventListener("click", pressed);
    expect(await end(walk({ submit: true }))).toBe("no-control");
    expect(pressed).not.toHaveBeenCalled();
  });

  it("skips a disabled progress control", async () => {
    document.body.innerHTML = `<div role="dialog"><button id="n" disabled>Next</button></div>`;
    expect(await end(walk())).toBe("no-control");
  });

  it("stops after 8 iterations and reports the cap", async () => {
    document.body.innerHTML = `<div role="dialog"><p id="n">Step 1</p><button id="next">Next</button></div>`;
    let n = 1;
    document.getElementById("next")!.addEventListener("click", () => (document.getElementById("n")!.textContent = `Step ${++n}`));
    expect(await end(walk())).toBe("cap");
    expect(n).toBe(MAX_ITERATIONS + 1);
    expect(states.at(-1)!.message).toMatch(/8/);
  });

  it("presses only the rendered Continue when a page stacks hidden steps", async () => {
    document.body.innerHTML = Array.from({ length: 6 }, (_, i) => `<section ${i === 2 ? "" : "hidden"}><button class="c" data-i="${i}">Continue</button></section>`).join("");
    const pressed: string[] = [];
    document.querySelectorAll<HTMLElement>(".c").forEach((b) => b.addEventListener("click", () => (pressed.push(b.dataset.i!), b.closest("section")!.hidden = true)));
    await end(walk());
    expect(pressed).toEqual(["2"]);
  });

  it("ignores a Continue inside a dialog hidden with display none", async () => {
    document.body.innerHTML = `<div role="dialog" style="display:none"><button id="region">Continue</button></div><div id="page"><p>Step</p><button id="real">Next</button></div>`;
    const pressed: string[] = [];
    for (const id of ["region", "real"]) document.getElementById(id)!.addEventListener("click", () => pressed.push(id));
    await end(walk());
    expect(pressed).toEqual(["real"]);
  });

  it("stops when the step shows blocking errors", async () => {
    document.body.innerHTML = `<div role="dialog"><input aria-invalid="true"><button>Next</button></div>`;
    expect(await end(walk())).toBe("validation");
  });

  it("stops when pressing does not move the form on", async () => {
    document.body.innerHTML = `<div role="dialog"><p>Same</p><button id="b">Continue</button></div>`;
    expect(await end(walk())).toBe("no-advance");
  });

  it("stops when the fill itself fails", async () => {
    steps(["Continue"]);
    expect(await end(walk({ fill: async () => ({ ok: false, message: "Autofill failed." }) }))).toBe("fill-failed");
  });

  it("waits 700 ms after each press and honours Pause, Resume and Stop", async () => {
    steps(["Continue", "Next", "Review"]);
    const sleeps: number[] = [];
    let release: (v: { ok: boolean }) => void = () => {};
    const w = walk({ fill: () => new Promise((r) => (release = r)), sleep: async (ms) => void sleeps.push(ms) });
    await vi.waitFor(() => expect(states.some((s) => s.phase === "filling")).toBe(true));
    w.pause();
    release({ ok: true });
    await vi.waitFor(() => expect(states.at(-1)!.phase).toBe("paused"));
    expect(sleeps).toEqual([]);
    // Resume carries on: Continue is pressed, then the walker waits 700 ms.
    w.resume();
    await vi.waitFor(() => expect(states.filter((s) => s.phase === "filling")).toHaveLength(2));
    expect(sleeps).toEqual([SETTLE_MS]);
    expect(SETTLE_MS).toBe(700);
    w.stop();
    release({ ok: true });
    expect(await end(w)).toBe("user-stop");
    expect(document.getElementById("final")).toBeNull();
  });
});

/** Counts the clicks on every button by its id, and names the one pressed. */
function pressedIds(): string[] {
  const out: string[] = [];
  document.querySelectorAll<HTMLElement>("button").forEach((b) => b.addEventListener("click", () => out.push(b.id)));
  return out;
}

describe("modal discovery ladder", () => {
  const decoy = `<button id="decoy">Continue</button>`;
  const rungs: Record<string, string> = {
    "the modal class": `${decoy}<div class="jobs-easy-apply-modal"><p>Contact info</p><button id="mine">Next</button></div>`,
    "a dialog labelled by an easy id": `${decoy}<div role="dialog" aria-labelledby="easy-apply-title"><h2 id="easy-apply-title">Contact info</h2><button id="mine">Next</button></div>`,
    "a labelled dialog whose text shows the apply wording": `${decoy}<section role="dialog" aria-labelledby="t"><h2 id="t">Apply to Example Co</h2><button id="back">Back</button><button id="mine">Next</button></section>`,
    "a dialog holding a form labelled apply": `${decoy}<div role="dialog"><form aria-label="Apply now"><button id="mine">Next</button></form></div>`,
    "a dialog that names Easy Apply": `${decoy}<div role="dialog"><p>Easy Apply</p><button id="mine">Next</button></div>`,
    "an Apply to heading with no dialog at all": `${decoy}<section><h3>Apply to Example Co</h3><div><button id="mine">Next</button></div></section>`,
    "an Apply to role heading": `${decoy}<section><div role="heading" aria-level="2">Apply to Example Co</div><div><button id="mine">Review</button></div></section>`,
    "the Application powered by footer": `${decoy}<section><div><button id="mine">Next</button><p>Application powered by Example</p></div></section>`,
  };
  for (const [name, html] of Object.entries(rungs)) {
    it(`finds the Easy Apply modal without its class or role: ${name}`, async () => {
      // A cookie dialog comes last, where a walker that only knows dialogs would look.
      document.body.innerHTML = `${html}<div role="dialog"><button id="cookie">Accept all</button></div>`;
      const pressed = pressedIds();
      await end(walk());
      expect(pressed).toEqual(["mine"]);
    });
  }

  it("finds the Easy Apply modal without its class or role: inside an open shadow root", async () => {
    document.body.innerHTML = `${decoy}<div id="host"></div>`;
    document.getElementById("host")!.attachShadow({ mode: "open" }).innerHTML = `<div class="jobs-easy-apply-modal"><button id="mine">Next</button></div>`;
    const pressed: string[] = [];
    document.getElementById("decoy")!.addEventListener("click", () => pressed.push("decoy"));
    document.getElementById("host")!.shadowRoot!.getElementById("mine")!.addEventListener("click", () => pressed.push("mine"));
    await end(walk());
    expect(pressed).toEqual(["mine"]);
  });

  it("takes a dialog holding a Submit application button for the step, and leaves the Continue outside it", async () => {
    document.body.innerHTML = `<button id="decoy">Continue</button><div role="dialog"><button id="final">Submit application</button></div><div role="dialog"><button id="other">Close</button></div>`;
    const pressed = pressedIds();
    expect(await end(walk())).toBe("stopped-before-submit");
    expect(pressed).toEqual([]);
    expect(findApplyModal(document)?.querySelector("#final")).not.toBeNull();
  });

  it("finds the shortest footer first, so the modal is not the whole page wrapper", () => {
    document.body.innerHTML = `<main id="page"><div id="wrap"><div id="modal"><p>Application powered by Example</p><button>Next</button></div></div><aside><button>Continue</button></aside></main>`;
    expect(findApplyModal(document)?.id).toBe("modal");
  });

  it("ignores an Apply to heading that has no progress control near it", () => {
    document.body.innerHTML = `<header><h2>Apply to Example Co</h2><p>Open roles</p></header><section><button>Continue</button></section>`;
    expect(findApplyModal(document)).toBeNull();
  });

  it("ignores a hidden modal and a heading in a hidden subtree", () => {
    document.body.innerHTML = `<div class="jobs-easy-apply-modal" hidden><button>Next</button></div><section hidden><h2>Apply to Example Co</h2><button>Next</button></section>`;
    expect(findApplyModal(document)).toBeNull();
  });
});

describe("terminal reasons of a step with no control", () => {
  it("refuses a progress control whose name holds a never-press word and reports no control", async () => {
    document.body.innerHTML = `<div role="dialog"><button id="c" aria-label="Continue and delete my profile">Continue</button></div>`;
    const pressed = pressedIds();
    expect(await end(walk())).toBe("no-control");
    expect(pressed).toEqual([]);
  });
});

describe("settle budget for slow hosted steps", () => {
  /** A hosted step whose next page renders only after a virtual 1200 ms; the walker's sleeps advance the clock. */
  function slowPage(blank = true) {
    document.body.innerHTML = `<form id="f"><p>Step 1</p><button id="go">Continue</button></form>`;
    let virtual = 0;
    let loadedAt = Infinity;
    document.getElementById("go")!.addEventListener("click", () => {
      // A blanking page shows a loader; the other keeps the pressed step on screen while it saves.
      if (blank) document.getElementById("f")!.innerHTML = `<span>Loading</span>`;
      loadedAt = virtual + 1200;
    });
    const sleeps: number[] = [];
    const sleep = async (ms: number) => {
      sleeps.push(ms);
      virtual += ms;
      if (virtual >= loadedAt) document.getElementById("f")!.innerHTML = `<p>Step 2</p><button id="final">Submit application</button>`;
    };
    return { sleeps, sleep };
  }

  it("polls for the next step of a slow hosted page within the budget", async () => {
    const { sleeps, sleep } = slowPage();
    expect(await end(walk({ settleBudgetMs: HOSTED_SETTLE_BUDGET_MS, sleep }))).toBe("stopped-before-submit");
    expect(sleeps.length).toBeGreaterThan(1);
    expect(sleeps.every((ms) => ms === SETTLE_POLL_MS)).toBe(true);
  });

  it("gives up when the page never renders a control, after no more than the budget", async () => {
    document.body.innerHTML = `<form id="f"><button id="go">Continue</button></form>`;
    document.getElementById("go")!.addEventListener("click", () => (document.getElementById("f")!.innerHTML = `<span>Loading</span>`));
    const sleeps: number[] = [];
    expect(await end(walk({ settleBudgetMs: 2000, sleep: async (ms) => void sleeps.push(ms) }))).toBe("no-control");
    expect(sleeps.reduce((a, b) => a + b, 0)).toBe(2000);
  });
});

describe("blocking error rules", () => {
  const stopsWith = async (html: string, message?: RegExp) => {
    document.body.innerHTML = html;
    expect(await end(walk())).toBe("validation");
    if (message) expect(states.at(-1)!.message).toMatch(message);
  };
  const passes = async (html: string) => {
    document.body.innerHTML = html;
    const pressed = pressedIds();
    expect(await end(walk())).not.toBe("validation");
    expect(pressed).toEqual(["next"]);
  };

  it("stops on a rendered alert with required wording", () => stopsWith(`<div role="alert">Phone number: This field is required</div><button id="next">Next</button>`, /This field is required/));
  it("stops on an assertive live region with error wording", () => stopsWith(`<div aria-live="assertive">Please enter a valid email</div><button id="next">Next</button>`, /valid email/));
  it("stops on a control marked invalid and shows the message it points at", () =>
    stopsWith(`<input aria-invalid="true" aria-describedby="m"><p id="m">Enter a valid phone number</p><button id="next">Next</button>`, /valid phone number/));
  it("stops on a control marked invalid with no message", () => stopsWith(`<input aria-invalid="true"><button id="next">Next</button>`, /A required field is invalid/));
  it("stops on a parsed item that lacks a required field, which is plain static text", () =>
    stopsWith(`<div><span>There are required details missing from this item. Edit this item to add required details.</span></div><button id="next">Next</button>`, /required details missing/));

  it("is not tripped by the instruction that starred fields are required", () => passes(`<span>Fields marked with an asterisk(*) are required.</span><button id="next">Next</button>`));
  it("is not tripped when that instruction sits in a live region", () => passes(`<div role="alert">Fields marked with an asterisk (*) are required.</div><button id="next">Next</button>`));
  it("is not tripped by prose that merely says required", () => passes(`<p>A valid work authorization is required for this role.</p><button id="next">Next</button>`));
  it("is not tripped by a hidden template", () =>
    passes(`<div role="alert" style="display:none">This field is required</div><section hidden><div aria-live="assertive">Please enter a valid email</div><input aria-invalid="true"></section><div hidden><span>Required details missing from this item</span></div><button id="next">Next</button>`));
  it("does not read a long block of text, or a container's gathered text, as the incomplete-item message", () =>
    passes(`<div><span>Education</span><span>${"x".repeat(200)} required details missing</span></div><button id="next">Next</button>`));
});

describe("textless Dojo pager", () => {
  /** The observed pager: the dual layout is hidden, the single layout shows one empty advance div. */
  const pagerPage = (single = "thePagerSingleGo") =>
    `<div class="srccarPager"><div style="display:none"><div id="back" data-dojo-attach-point="thePagerDualPrev"></div><div id="dual" data-dojo-attach-point="thePagerDualNext"></div></div><div style="display:block"><div id="go" data-dojo-attach-point="${single}"></div></div></div><section id="step"><p>Step 1</p></section>`;
  const watch = (id: string, log: string[]) => {
    const el = document.getElementById(id)!;
    for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el.addEventListener(type, () => log.push(type));
    return el;
  };

  it("presses the textless pager with the full pointer gesture, which a bare click does not satisfy", async () => {
    document.body.innerHTML = pagerPage();
    const log: string[] = [];
    const go = watch("go", log);
    const hidden: string[] = [];
    watch("dual", hidden);
    // Dojo listens to mouse up, not click.
    go.addEventListener("mouseup", () => (document.getElementById("step")!.innerHTML = `<p>Step 2</p><button id="final">Submit application</button>`));
    expect(await end(walk())).toBe("stopped-before-submit");
    expect(log).toEqual(["pointerdown", "mousedown", "pointerup", "mouseup", "click"]);
    expect(hidden).toEqual([]);
  });

  it("does not take an unrecognized textless control for the pager", async () => {
    document.body.innerHTML = pagerPage("thePagerSingleFinish");
    const log: string[] = [];
    watch("go", log);
    expect(await end(walk())).toBe("no-control");
    expect(log).toEqual([]);
  });

  it("leaves the pager alone while the step shows a Submit control", async () => {
    document.body.innerHTML = pagerPage() + `<button id="final">Submit application</button>`;
    const log: string[] = [];
    watch("go", log);
    expect(await end(walk())).toBe("stopped-before-submit");
    expect(log).toEqual([]);
  });
});

describe("profile summary steps", () => {
  const summary = (heading = "<h3>Work experience</h3>", counter = "<p>1 of 2</p>") =>
    `<div role="dialog">${heading}${counter}<div><button id="remove">Remove</button><button id="edit">Edit</button></div><button id="next">Next</button></div>`;

  it("advances a LinkedIn summary step without filling or editing it", async () => {
    document.body.innerHTML = summary();
    const pressed = pressedIds();
    await end(walk());
    expect(fills).toBe(0);
    expect(pressed).toEqual(["next"]);
  });

  it("fills a work experience heading that lacks the Remove and Edit controls of a summary", async () => {
    document.body.innerHTML = `<div role="dialog"><h3>Work experience</h3><p>1 of 2 resumes</p><button id="edit">Edit</button><button id="next">Next</button></div>`;
    await end(walk());
    expect(fills).toBe(1);
  });
});
