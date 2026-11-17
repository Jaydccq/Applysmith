// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Controls the engine never writes or presses (the one Save: an open repeatable-row editor's own);
// Drivers for HR-suite application systems (SmartRecruiters cards).
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { isEditorSave } from "../src/click-guard";
import type { Win } from "../src/dom";
import { pressEditorSave } from "../src/gestures";
import { installHelper } from "../src/mainworld/helper";
import type { ApplicantSnapshot } from "../src/protocol";
import { defineSplInput } from "./ats-fixtures";
import { byId, fakeHost, makeEngine, mount, runFill, setUrl, uninstall } from "./helpers";

const win = window as unknown as Win;
const NONCE = "sr-save-nonce";

const PROFILE: ApplicantSnapshot = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example", email: "alex@example.com" },
  history: { employment: [{ employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true }], education: [] },
};

/** An Experience section; the editor's own footer buttons are `editorButtons`, and a Save may also sit at section or page level. */
function mountEditor(opts: { editorButtons: string[]; sectionSave?: string; pageSave?: boolean }) {
  const presses: string[] = [];
  const btn = (label: string, cls = "") => `<button type="button" class="${cls}">${label}</button>`;
  mount(`<form><label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em" type="email">
    <section id="exp"><h3>Experience</h3><ul class="cards"></ul><div class="slot"></div><button type="button" class="add">Add experience</button>${opts.sectionSave ? btn(opts.sectionSave, "section-save") : ""}</section>
    ${opts.pageSave ? btn("Save", "page-save") : ""}</form>`);
  const slot = document.querySelector("#exp .slot") as HTMLElement;
  document.querySelector("#exp .add")!.addEventListener("click", () => {
    presses.push("add");
    if (slot.children.length) return;
    slot.innerHTML = `<div class="editor"><spl-input label="Title"></spl-input><spl-input label="Company"></spl-input>${opts.editorButtons.map((l) => btn(l)).join("")}</div>`;
    slot.querySelectorAll("button").forEach((b) => b.addEventListener("click", () => presses.push(`press:${b.textContent}`)));
  });
  document.querySelector(".section-save")?.addEventListener("click", () => presses.push("press:section-save"));
  document.querySelector(".page-save")?.addEventListener("click", () => presses.push("press:page-save"));
  return presses;
}

async function fill() {
  installHelper(win, NONCE, "p1");
  const engine = makeEngine(fakeHost());
  engine.useHelper(NONCE);
  await runFill(engine, { applicant: PROFILE });
  engine.dispose();
}

beforeEach(() => {
  defineSplInput("spl-input");
  defineSplInput("spl-datepicker");
  uninstall();
  setUrl("https://jobs.smartrecruiters.com/Example/743999-engineer");
});
afterEach(() => {
  uninstall();
});

describe("SmartRecruiters Save", () => {
  test("an editor with a Save of its own is saved by that Save", async () => {
    const presses = mountEditor({ editorButtons: ["Save", "Cancel"] });
    await fill();
    expect(presses).toContain("press:Save");
  });

  test("SR-A: an editor that only offers Save and continue, Save draft or Save application is left for the person", async () => {
    const presses = mountEditor({ editorButtons: ["Save and continue", "Save draft", "Save application", "Cancel"] });
    await fill();
    expect(presses.filter((p) => /Save/.test(p))).toEqual([]);
  });

  test("SR-B: a section-level Save is never pressed for an editor that has none of its own", async () => {
    const presses = mountEditor({ editorButtons: ["Cancel"], sectionSave: "Save" });
    await fill();
    expect(presses).not.toContain("press:section-save");
    expect(presses.filter((p) => /Save/i.test(p))).toEqual([]);
  });

  test("SR-C: a page-level Save outside the section is never pressed, with or without an editor Save", async () => {
    const presses = mountEditor({ editorButtons: ["Cancel"], pageSave: true, sectionSave: "Save changes" });
    await fill();
    expect(presses.filter((p) => /save/i.test(p))).toEqual([]);
    // With a Save of its own the editor is saved by that one, never by the page's.
    const withSave = mountEditor({ editorButtons: ["Save", "Cancel"], pageSave: true });
    await fill();
    expect(withSave).toContain("press:Save");
    expect(withSave).not.toContain("press:page-save");
  });
});

describe("pressEditorSave and isEditorSave", () => {
  test("only a control named exactly Save, below the editor element itself, is the editor's Save", () => {
    mount(`<section id="sec"><div id="ed"><button id="a">Save</button><button id="b" aria-label="Save">Save</button><button id="c">Save draft</button>
      <button id="d" aria-label="Submit application">Save</button><button id="e">S\u0430ve</button><button id="f">Cancel</button></div><button id="g">Save</button></section>`);
    const ed = byId("ed");
    expect(["a", "b", "c", "d", "e", "f"].map((id) => isEditorSave(byId(id), ed))).toEqual([true, true, false, false, false, false]);
    expect(isEditorSave(byId("g"), ed)).toBe(false);
    expect(isEditorSave(ed, ed)).toBe(false);
    expect(isEditorSave(byId("a"), byId("sec"))).toBe(true);
  });

  test("the Save is pressed once, with the pointer sequence, and a hidden Save is skipped", () => {
    mount(`<div id="ed"><button id="h" style="display:none">Save</button><button id="s">Save</button></div>`);
    const pressed: string[] = [];
    for (const b of Array.from(document.querySelectorAll("button"))) b.addEventListener("click", () => pressed.push(b.id));
    expect(pressEditorSave(byId("ed"))).toBe("ok");
    expect(pressed).toEqual(["s"]);
  });
});
