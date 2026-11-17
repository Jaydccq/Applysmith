// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Legal and declaration checkboxes follow the user's setting; Optional marketing opt-ins are never ticked
// (the rule holds for every control kind a marketing question can be answered on, not only tick boxes).
import { describe, expect, test } from "vitest";
import { PAD, byId, fakeHost, makeEngine, mount, runFill, until, write } from "./helpers";
import { mountMenu } from "./widgets-fixtures";

const QUESTIONS = ["Would you like to receive job alerts?", "May we contact you about future roles?"];

interface Fixture {
  /** Id of an element the write is addressed to. */
  id: string;
  /** What the control shows now; empty when nothing is chosen. */
  shown: () => string;
}

const kinds: Record<string, (question: string, required: boolean) => Fixture> = {
  select: (question, required) => {
    mount(`<label for="s">${question}</label><select id="s"${required ? " required" : ""}><option value="">Select</option><option>Yes</option><option>No</option></select>`);
    return { id: "s", shown: () => (byId("s") as HTMLSelectElement).value };
  },
  "custom listbox": (question, required) => {
    document.body.innerHTML = "";
    const menu = mountMenu({ id: "lb", label: question, options: ["Yes", "No"], openOn: "click" });
    if (required) menu.trigger.setAttribute("aria-required", "true");
    return { id: "lb", shown: () => (menu.shown() === "Select one" ? "" : menu.shown()) };
  },
  "Yes/No button group": (question, required) => {
    mount(
      `<div role="group" aria-labelledby="q"${required ? ' aria-required="true"' : ""}><span id="q">${question}</span><button type="button" id="yes" aria-pressed="false">Yes</button><button type="button" id="no" aria-pressed="false">No</button></div>`,
    );
    for (const id of ["yes", "no"]) {
      byId(id).addEventListener("click", () => {
        for (const other of ["yes", "no"]) byId(other).setAttribute("aria-pressed", String(other === id));
      });
    }
    return { id: "yes", shown: () => (byId("yes").getAttribute("aria-pressed") === "true" ? "Yes" : byId("no").getAttribute("aria-pressed") === "true" ? "No" : "") };
  },
  "radio group": (question, required) => {
    mount(
      `<fieldset><legend>${question}</legend><label><input type="radio" name="r" id="ry"${required ? " required" : ""}> Yes</label><label><input type="radio" name="r" id="rn"> No</label></fieldset>`,
    );
    return { id: "ry", shown: () => (byId("ry") as HTMLInputElement).checked ? "Yes" : (byId("rn") as HTMLInputElement).checked ? "No" : "" };
  },
};

describe("Marketing opt-in on a control that is not a tick box", () => {
  for (const [kind, build] of Object.entries(kinds)) {
    // One wording per rule: the two wordings take the same branch, so each rule reads one of them.
    test(`${kind}, optional: a Yes to "${QUESTIONS[0]}" is refused and nothing is chosen, with the setting on or off`, async () => {
      for (const accept of [false, true]) {
        const f = build(QUESTIONS[0] as string, false);
        expect(await write(f.id, "Yes", {}, "replace", new Map(), accept)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
        expect(f.shown()).toBe("");
      }
    });

    test(`${kind}, required by the page: a Yes to "${QUESTIONS[1]}" follows the setting`, async () => {
      const off = build(QUESTIONS[1] as string, true);
      expect(await write(off.id, "Yes", {}, "replace", new Map(), false)).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off" });
      expect(off.shown()).toBe("");
      const on = build(QUESTIONS[1] as string, true);
      expect((await write(on.id, "Yes", {}, "replace", new Map(), true)).outcome).toBe("filled");
      expect(on.shown()).toBe("Yes");
    });

    test(`${kind}: a No the matcher chose is written whatever the setting and the marking`, async () => {
      for (const required of [false, true]) {
        for (const accept of [false, true]) {
          const f = build(QUESTIONS[0] as string, required);
          expect((await write(f.id, "No", {}, "replace", new Map(), accept)).outcome).toBe("filled");
          expect(f.shown()).toBe("No");
        }
      }
    });
  }

  test("A sentence option that opts in is refused on a custom listbox, an explicit No option is never picked instead", async () => {
    document.body.innerHTML = "";
    const menu = mountMenu({ id: "lb", label: "Stay in touch", options: ["Yes, send me job alerts", "No thanks"], openOn: "click" });
    expect(await write("lb", "Yes, send me job alerts", {}, "replace", new Map(), true)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(menu.clicked).toEqual([]);
  });

  test("A question that is not about marketing is answered Yes on a select as before", async () => {
    kinds.select?.("Are you 18 or older?", false);
    expect((await write("s", "Yes")).outcome).toBe("filled");
  });
});

describe("Acknowledgement sweep and user edits", () => {
  test("A required terms box that rendered late and that the user then touched is not ticked by the late sweep", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><div id="late"></div>${PAD}`);
    // The refill stage settles long enough for the box to render between the fill pass and the late sweep.
    const engine = makeEngine(fakeHost(), { refillSettleMs: 150 });
    const watch = setInterval(() => {
      if (!engine.log().some((l) => l.includes("cascade end"))) return;
      clearInterval(watch);
      byId("late").innerHTML = `<label><input type="checkbox" id="t" required> I have read and accept the terms and conditions</label>`;
      // A person's key on the box; a box's own input event is trusted for a script's click() too, so it is no person's.
      const ev = new Event("keydown", { bubbles: true, composed: true });
      Object.defineProperty(ev, "isTrusted", { value: true });
      byId("t").dispatchEvent(ev);
    }, 1);
    await runFill(engine, { acceptLegalDeclarations: true });
    clearInterval(watch);
    await until(() => engine.status().state === "done");
    expect((byId("t") as HTMLInputElement).checked).toBe(false);
  });

  test("A required statement that the information given is accurate is ticked, though a later clause says the site takes no responsibility (evlo)", async () => {
    const said = (text: string) => {
      mount(`<label><input type="checkbox" id="t" required> ${text}</label>${PAD}`);
      return byId("t") as HTMLInputElement;
    };
    let box = said("The information I have provided here and in my resume is complete and accurate. I understand that evlo.ai takes no responsibility for the accuracy or completeness of the application it submits to partnered Employers/Recruiters on my behalf.");
    let engine = makeEngine(fakeHost());
    await runFill(engine, { acceptLegalDeclarations: true });
    await until(() => engine.status().state === "done");
    expect(box.checked).toBe(true);
    // A statement that the information is not accurate is never ticked.
    box = said("The information I have provided is not complete and accurate.");
    engine = makeEngine(fakeHost());
    await runFill(engine, { acceptLegalDeclarations: true });
    await until(() => engine.status().state === "done");
    expect(box.checked).toBe(false);
  });
});
