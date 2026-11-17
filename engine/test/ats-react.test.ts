// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for HR-suite application systems (Paylocity); Drivers for widget libraries (react-widgets).
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { installHelper } from "../src/mainworld/helper";
import { pickRwRow } from "../src/reactwidgets";
import type { Win } from "../src/dom";
import { readInfo } from "../src/scan";
import { ADA, fakeHost, infoOf, makeEngine, mount, PAD, runFill, setUrl, write, uninstall } from "./helpers";
import { mountControlledText, mountPctySelect, mountRwDropdown } from "./ats-fixtures";
import { ctx } from "./helpers";
import { MainWorld } from "../src/mainworld-client";
import { applyValue } from "../src/apply";
import { UserEdits } from "../src/restore";

const win = window as unknown as Win;
const NONCE = "rw-nonce";
beforeEach(uninstall);
afterEach(uninstall);

/** write() with the channel to the helper in the context. */
async function writeWithHelper(id: string, value: string) {
  installHelper(win, NONCE, "h1");
  const main = new MainWorld(win, NONCE);
  const c = { ...ctx(), main, site: "paylocity" };
  const r = await applyValue(infoOf(id), { value, replaceDefault: false }, { ctx: c, written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: new UserEdits(document) });
  main.dispose();
  return r;
}

describe("react-widgets", () => {
  test("A fresh row has no list until it is opened: the root's own handler opens it, the option's React onClick commits it and the widget's rendered text verifies it", async () => {
    mount(PAD);
    const d = mountRwDropdown({ id: "country", label: "Country", options: ["United States of America", "Canada"] });
    const r = await writeWithHelper("country", "United States of America");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "United States of America" });
    expect(d.shown()).toBe("United States of America");
    expect(d.opens()).toBe(1);
    expect(d.domClicks()).toBe(0);
    expect(readInfo(infoOf("country"))).toBe("United States of America");
  });

  test("The live dropdown (a div with a bare aria-haspopup, a label with no for, a listbox that exists only once opened) is filled through the helper and unavailable without it", async () => {
    const live = `${PAD}<div class="form-group form-required"><label>Have you worked with us before?<span><em> (required)</em></span></label><div style="max-width: 100px;"><div data-for="Have you worked with us before?" id="info.haveYouWorkedWithUsBefore" role="combobox" aria-owns="info.haveYouWorkedWithUsBefore__listbox" aria-expanded="false" aria-haspopup="true" aria-busy="false" aria-live="polite" aria-autocomplete="list" aria-disabled="false" aria-readonly="false" tabindex="0" class="rw-dropdownlist rw-widget"><span tabindex="-1" title="open dropdown" aria-label="open dropdown" class="rw-dropdownlist-picker rw-select rw-btn"><span aria-hidden="true" class="rw-i rw-i-caret-down"></span></span><div class="rw-input">--</div></div></div></div>`;
    const id = "info.haveYouWorkedWithUsBefore";
    mount(live);
    expect(await write(id, "No")).toMatchObject({ outcome: "failed", reason: "binding-unavailable" });
    mount(live);
    const root = document.getElementById(id) as HTMLElement;
    const props = (el: Element, p: Record<string, unknown>) => ((el as unknown as Record<string, unknown>)["__reactProps$live"] = p);
    props(root, {
      onClick: () => {
        if (document.getElementById(`${id}__listbox`)) return;
        const pop = document.createElement("div");
        pop.innerHTML = `<ul id="${id}__listbox" class="rw-list" role="listbox"><li class="rw-list-option" role="option">Yes</li><li class="rw-list-option" role="option">No</li></ul>`;
        pop.querySelectorAll("li").forEach((li) =>
          props(li, {
            onClick: () => {
              (root.querySelector(".rw-input") as HTMLElement).textContent = li.textContent ?? "";
              pop.remove();
            },
          }),
        );
        root.parentElement?.append(pop);
      },
    });
    expect(await writeWithHelper(id, "No")).toMatchObject({ outcome: "filled", optionLabel: "No" });
    expect(readInfo(infoOf(id))).toBe("No");
  });

  test("Label match: exact, then ignoring a trailing required marker, then starts-with either way; ambiguity commits nothing", () => {
    const row = (label: string) => ({ el: document.createElement("li"), label, value: "", selected: false, disabled: false });
    expect(pickRwRow([row("Bachelor's *"), row("Master's *")], "Bachelor's")?.label).toBe("Bachelor's *");
    expect(pickRwRow([row("Doctorate of Philosophy"), row("Associate")], "Doctorate")?.label).toBe("Doctorate of Philosophy");
    expect(pickRwRow([row("Bachelor of Arts"), row("Bachelor of Science")], "Bachelor")).toBeNull();
    // Starts-with ends on a word: "No" opens neither "None" nor "North Carolina", and a one-letter row is not the start of a longer answer.
    expect(pickRwRow([row("None"), row("Yes")], "No")).toBeNull();
    expect(pickRwRow([row("North Carolina"), row("Texas")], "No")).toBeNull();
    expect(pickRwRow([row("M"), row("F")], "Mexico")).toBeNull();
    expect(pickRwRow([row("No, I have not"), row("Yes, I have")], "No")?.label).toBe("No, I have not");
  });

  test("Paylocity text reaches the component through onChange and onBlur with an event that has persist(), and is verified after a settle", async () => {
    mount(PAD);
    const t = mountControlledText({ id: "workHistory.0.company", label: "Company" });
    const r = await writeWithHelper("workHistory.0.company", "Example Labs");
    expect(r).toMatchObject({ outcome: "filled" });
    expect(t.state()).toBe("Example Labs");
    expect(t.input.value).toBe("Example Labs");
    expect(t.calls).toEqual(["change", "blur"]);
  });

  test("Education: Did you graduate is committed first and the degree widget that mounts afterwards is written in the same run", async () => {
    mount(PAD);
    let degreeMounted = false;
    const grad = mountRwDropdown({
      id: "educationHistory.0.didYouGraduate",
      label: "Did you graduate",
      options: ["Yes", "No"],
      onCommit: (label) => {
        if (label === "Yes" && !degreeMounted) {
          degreeMounted = true;
          setTimeout(() => mountRwDropdown({ id: "educationHistory.0.degreeId", label: "Degree", options: ["Bachelor of Science", "Master of Science"] }), 5);
        }
      },
    });
    mountControlledText({ id: "educationHistory.0.name", label: "School" });
    installHelper(win, NONCE, "h1");
    setUrl("https://recruiting.paylocity.com/recruiting/jobs/Apply/1/example");
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    const applicant = { candidate: ADA.candidate, history: { education: [{ school: "Example University", degree: "Bachelor of Science", startDate: "2013-09-01", endDate: "2017-05-01" }] } };
    await runFill(engine, { applicant });
    expect(grad.shown()).toBe("Yes");
    const degree = document.getElementById("educationHistory.0.degreeId");
    expect(degree?.querySelector(".rw-dropdownlist-value")?.textContent).toBe("Bachelor of Science");
  });

  test("Paylocity select: ArrowDown opens the menu, the leaf option's mouse-down handler commits it, and the display that renders after blur verifies it", async () => {
    mount(PAD);
    const d = mountPctySelect({ id: "workHistory.0.state", label: "State", options: ["California", "Colorado", "Texas"] });
    const r = await writeWithHelper("workHistory.0.state", "Texas");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Texas" });
    expect(d.shown()).toBe("Texas");
    expect(d.clicks()).toBe(0);
  });

  test("Paylocity select: an answer that names no option commits nothing and abstains", async () => {
    mount(PAD);
    const d = mountPctySelect({ id: "workHistory.0.state", label: "State", options: ["California", "Texas"] });
    const r = await writeWithHelper("workHistory.0.state", "Narnia");
    expect(r.outcome).not.toBe("filled");
    expect(d.shown()).toBe("");
  });
});
