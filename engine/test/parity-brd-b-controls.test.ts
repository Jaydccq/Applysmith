// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Control writers and commit verification; Widget drivers for custom controls; Checkbox groups,
// consent boxes and single-answer choice groups; Controls the engine never writes or presses.
import { describe, expect, test } from "vitest";
import { isUnsafeLabel } from "../src/click-guard";
import { scanControls } from "../src/scan";
import { PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, until, write } from "./helpers";
import { mountMenu } from "./widgets-fixtures";

describe("Value outside the option set", () => {
  test("A year offered to a Yes, No and Maybe radio group selects nothing and the row is failed", async () => {
    mount(`<fieldset><legend>Open to relocation</legend><label><input type="radio" name="y" id="y1" value="a"> Yes</label><label><input type="radio" name="y" id="y2" value="b"> No</label><label><input type="radio" name="y" id="y3" value="c"> Maybe</label></fieldset>${PAD}`);
    const r = await write("y1", "2019");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(document.querySelectorAll("input:checked")).toHaveLength(0);
  });
});

describe("Options that answer to mouse down", () => {
  test("A react-select shaped option that responds to mouse down only still commits", async () => {
    mount("");
    const m = mountMenu({ id: "q", label: "Pick one", options: ["Alpha", "Beta"], clickable: false });
    m.list.querySelectorAll("li").forEach((li) =>
      li.addEventListener("mousedown", () => {
        m.trigger.textContent = li.textContent;
        m.list.hidden = true;
      }),
    );
    const r = await write("q", "Beta");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Beta" });
    expect(m.shown()).toBe("Beta");
  });

  test("An option that is already selected is never clicked again, because a second click would clear it", async () => {
    mount("");
    const m = mountMenu({ id: "q", label: "Pick one", options: ["Alpha", "Beta"] });
    m.trigger.textContent = "Beta";
    m.list.querySelectorAll("li")[1]?.setAttribute("aria-selected", "true");
    const r = await write("q", "Beta");
    expect(r.outcome).toBe("prefilled");
    expect(m.clicked).toEqual([]);
  });
});

describe("LinkedIn numeric box", () => {
  const attempt = async (value: string) => {
    mount(`<label for="a-numeric">Years of experience</label><input id="a-numeric" type="text">${PAD}`);
    return write("a-numeric", value);
  };

  test.each(["Yes"])("%s is refused as a failed write and nothing is typed", async (value) => {
    expect(await attempt(value)).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
    expect(byId("a-numeric").value).toBe("");
  });

  test("One plain number is typed with its thousands separators dropped", async () => {
    expect(await attempt("1,200")).toMatchObject({ outcome: "filled", value: "1200" });
    expect(byId("a-numeric").value).toBe("1200");
  });
});

describe("Self-identification radios that re-render", () => {
  test("The ids churn after the write and the intended option is confirmed checked by its text", async () => {
    mount(`<fieldset id="fs"><legend>Gender</legend><label><input type="radio" name="g" id="g1"> Male</label><label><input type="radio" name="g" id="g2"> Female</label></fieldset>${PAD}`);
    let churn = 0;
    const rerender = () => {
      const fs = byId<HTMLElement>("fs");
      const picked = fs.querySelector<HTMLInputElement>("input:checked")?.parentElement?.textContent?.trim();
      churn++;
      fs.innerHTML = `<legend>Gender</legend><label><input type="radio" name="g" id="c${churn}a"${picked === "Male" ? " checked" : ""}> Male</label><label><input type="radio" name="g" id="c${churn}b"${picked === "Female" ? " checked" : ""}> Female</label>`;
    };
    for (const id of ["g1", "g2"]) byId(id).addEventListener("change", () => setTimeout(rerender, 1));
    const r = await write("g1", "Female");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Female" });
    await until(() => churn > 0);
    const checked = Array.from(document.querySelectorAll<HTMLInputElement>("#fs input:checked")).map((i) => i.parentElement?.textContent?.trim());
    expect(checked).toEqual(["Female"]);
  });
});

describe("Bare Confirmed acknowledgements", () => {
  const ticked = async (legend: string, withResume = false) => {
    mount(`${PAD}${withResume ? '<label for="up">Resume</label><input type="file" id="up">' : ""}<fieldset><legend>${legend}</legend><label><input type="checkbox" required> Confirmed</label></fieldset>`);
    await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true });
    return document.querySelector<HTMLInputElement>("fieldset input")?.checked;
  };

  test("The at-will acknowledgement is ticked", async () => {
    expect(await ticked("I understand that employment with Example Corp is at will.")).toBe(true);
  });

  test("A factual claim or a disclosure behind a bare Confirmed is never ticked", async () => {
    expect(await ticked("I am a veteran of the armed forces.")).toBe(false);
    expect(await ticked("Have you ever been terminated from a job?")).toBe(false);
  });

  test("The resume requirement is ticked only when a resume goes along", async () => {
    expect(await ticked("A resume is required to apply.")).toBe(false);
    expect(await ticked("A resume is required to apply.", true)).toBe(true);
  });

  test("A box that is not an acknowledgement is left alone", async () => {
    expect(await ticked("Select your preferred shift")).toBe(false);
  });
});

describe("Terminal apply phrases", () => {
  test("Apply and save and Apply without saving are refused while other controls stay allowed", () => {
    for (const label of ["Apply and save", "Apply & save", "Apply without saving"]) expect(isUnsafeLabel(label), label).toBe(true);
    for (const label of ["Add another", "Done"]) expect(isUnsafeLabel(label), label).toBe(false);
    for (const label of ["Apply", "Apply now", "Easy Apply", "Apply for this job"]) expect(isUnsafeLabel(label), label).toBe(true);
  });
});

describe("BambooHR Fabric select", () => {

  test("The live toggle (a bare aria-haspopup, no role) is found, named by the label that points at its hidden select, and filled through its menu", async () => {
    mount(`<div><label for="fab-select364">State<span aria-hidden="true"> *</span></label><div class="fab-Select" data-fabric-component="Select"><div style="display: inline-block;"><div class="fab-SelectToggle__container"><button data-menu-id="fab-menu66" aria-expanded="false" aria-haspopup="true" aria-label="State –Select–" tabindex="0" aria-disabled="false" class="fab-SelectToggle" type="button"><div class="fab-SelectToggle__guts"><div class="fab-SelectToggle__placeholder">–Select–</div></div></button></div></div><select aria-hidden="true" class="chzn-ignore" id="fab-select364" name="state.value" readonly="" required="" tabindex="-1" style="height: 0px; opacity: 0; overflow: hidden; position: absolute; width: 0px;"><option value=""></option></select></div></div>
      ${PAD}<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em" type="email">`);
    const btn = document.querySelector("button") as HTMLElement;
    // Floating UI ignores a click whose detail is 0; the portal menu mounts on the page body.
    btn.addEventListener("click", (e) => {
      if ((e as MouseEvent).detail < 1 || document.getElementById("fab-menu66")) return;
      const menu = document.createElement("div");
      menu.id = "fab-menu66";
      menu.setAttribute("role", "menu");
      for (const name of ["Alabama", "North Carolina", "South Carolina"]) {
        const item = document.createElement("div");
        item.setAttribute("role", "menuitem");
        item.textContent = name;
        item.addEventListener("click", (ev) => {
          if ((ev as MouseEvent).detail < 1) return;
          (btn.querySelector(".fab-SelectToggle__placeholder") as HTMLElement).textContent = name;
          menu.remove();
        });
        menu.append(item);
      }
      document.body.append(menu);
    });
    const info = scanControls(document).find((i) => i.el === btn);
    expect(info).toMatchObject({ driver: "listbox", label: { text: "State" }, answered: false });
    const s = summaryOf(await runFill(makeEngine(fakeHost({ state: { outcome: "filled", value: "North Carolina" } }))));
    expect(rowFor(s, "State")).toMatchObject({ outcome: "filled", value: "North Carolina" });
    expect(btn.textContent).toBe("North Carolina");
  });
});

describe("Duolingo's grouped listbox", () => {
  // The label points at a role=group box; the toggle is an empty button drawn over the span that shows the choice,
  // and the open list mounts inside the group.
  const field = (id: string, q: string, options: string[]) => {
    const wrap = document.createElement("div");
    wrap.innerHTML = `<label for="${id}" id="${id}--label">${q}<span>*</span></label><div aria-required="true" id="${id}" role="group"><div class="ykGYX"><div class="FUCuR"><span class="GfJwj">Select...</span><button aria-controls="${id}-menu" aria-haspopup="listbox" type="button"></button></div></div></div>`;
    const btn = wrap.querySelector("button") as HTMLElement;
    const shown = wrap.querySelector("span.GfJwj") as HTMLElement;
    btn.addEventListener("click", () => {
      if (document.getElementById(`${id}-menu`)) return;
      const ul = document.createElement("ul");
      ul.id = `${id}-menu`;
      ul.setAttribute("role", "listbox");
      for (const o of options) {
        const li = document.createElement("li");
        li.setAttribute("role", "option");
        li.setAttribute("aria-selected", "false");
        li.textContent = o;
        li.addEventListener("click", () => {
          shown.textContent = o;
          ul.remove();
        });
        ul.append(li);
      }
      wrap.querySelector(".ykGYX")?.append(ul);
    });
    return wrap;
  };

  test("An empty toggle inside the group a label points at is named by that label and reads the choice its box shows", async () => {
    mount(PAD);
    document.body.prepend(field("question_1", "What is your expected graduation timeline?", ["Anything before Fall/Winter 2026", "Fall/Winter 2026"]),
      field("question_2", "Do you plan to have a PhD by August 2027?", ["Yes", "No"]));
    const toggles = () => scanControls(document).filter((i) => i.el.localName === "button");
    expect(toggles().map((i) => [i.label.text, i.answered])).toEqual([
      ["What is your expected graduation timeline?", false],
      ["Do you plan to have a PhD by August 2027?", false],
    ]);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "what is your expected graduation timeline?": { outcome: "filled", value: "Fall/Winter 2026" } }))));
    expect(rowFor(s, "What is your expected graduation timeline?")).toMatchObject({ outcome: "filled", value: "Fall/Winter 2026" });
    expect(toggles().map((i) => [i.label.text, i.answered])).toEqual([
      ["What is your expected graduation timeline?", true],
      ["Do you plan to have a PhD by August 2027?", false],
    ]);
  });
});
