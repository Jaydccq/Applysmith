// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for HR-suite application systems (Dayforce, ADP WorkforceNow).
import { describe, expect, test } from "vitest";
import { readInfo, scanControls } from "../src/scan";
import { ADA, fakeHost, infoOf, makeEngine, mount, runFill, summaryOf, write } from "./helpers";
import { mountAntdSelect } from "./ats-fixtures";

describe("Dayforce", () => {
  test("A combobox that re-renders as it opens is retried once after the re-render and the committed value is read from the selection item", async () => {
    mount(`<label for="pad">Referral</label><input id="pad" value="x">`);
    const a = mountAntdSelect({ id: "state", label: "State", options: ["North Carolina", "Ohio"], remountOnFirstOpen: true });
    const r = await write("state", "Ohio");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Ohio" });
    expect(a.shown()).toBe("Ohio");
    expect(a.mounts()).toBeGreaterThanOrEqual(2);
    expect(readInfo(infoOf("state"))).toBe("Ohio");
  });

});

describe("ADP WorkforceNow", () => {
  test("A required Mobile and a required Home Phone, both labelled only Phone Number, both take the applicant's phone, and the +1 seed is replaced", async () => {
    mount(`<label for="m">Phone Number *</label><input id="m" type="tel" required><label for="h">Phone Number *</label><input id="h" type="tel" required value="+1">
      <label for="e">Employer phone</label><input id="e" type="tel">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));
    expect((document.getElementById("m") as HTMLInputElement).value).toMatch(/555/);
    expect((document.getElementById("h") as HTMLInputElement).value).toMatch(/555/);
    expect((document.getElementById("e") as HTMLInputElement).value).toBe("");
    expect(s.rows.filter((r) => r.outcome === "filled" && r.label === "Phone Number")).toHaveLength(2);
  });

  // The Voluntary Self-ID page: a react-select combobox whose placeholder div sits in the same value container
  // before the input, the question in a label with no `for` and in the input's aria-label.
  const VSID = `<div class="padding-top"><label class="vsid-title"><text>Gender </text><div class="contents"><sdf-floating-pane><div class="mdf-simple-help"><div>This field allows you to indicate your gender. Self-identification is optional.</div></div></sdf-floating-pane></div></label>
    <div><div class="vsid-item"><span id="live-region" class="a11yText"></span><span aria-live="polite" role="log" class="a11yText"></span><div class="MDFSelectBox__control"><div class="MDFSelectBox__value-container"><div class="MDFSelectBox__placeholder" id="ph-1">--Select One--</div><div class="MDFSelectBox__input-container"><input class="MDFSelectBox__input" id="vsidGender" type="text" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-label="Gender" role="combobox" aria-describedby="ph-1" value=""></div></div></div></div></div></div>
    <div><div class="hr-div"></div></div><div><h4 id="vsidEthRace">Ethnicity &amp; Race</h4></div>
    <div class="padding-top"><label class="upperCaseTextLable">Ethnicity</label><div><div><div class="vsid-item"><div class="MDFSelectBox__control"><div class="MDFSelectBox__value-container"><div class="MDFSelectBox__placeholder" id="ph-2">--Select One--</div><div class="MDFSelectBox__input-container"><input class="MDFSelectBox__input" id="vsidEthinicity" type="text" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-label="Ethnicity" role="combobox" aria-describedby="ph-2" value=""></div></div></div></div></div></div></div>`;

  test("A react-select combobox states its question, never its '--Select One--' placeholder", () => {
    mount(VSID);
    for (const [id, question] of [["vsidGender", "Gender"], ["vsidEthinicity", "Ethnicity"]]) {
      const info = infoOf(id!);
      expect(info.kind).toBe("custom-listbox");
      expect(info.label.text).toBe(question);
    }
  });

  test("A control whose own label has no asterisk on a page whose other labels have one is optional, and a sibling sdf-select counts as another field", () => {
    mount(`<div><label for="a">First name *</label><input id="a"></div>
      <div><label for="x">Select currency type *</label><sdf-select-simple id="cur"></sdf-select-simple><label for="n">Notes</label><input id="n"></div>`);
    const infos = scanControls(document);
    const notes = infos.find((i) => i.el.id === "n")!;
    expect(notes.label.text).toBe("Notes");
    expect(notes.required).toBe(false);
    expect(infos.some((i) => i.el.id === "cur")).toBe(true);
  });
});
