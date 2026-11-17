// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for HR-suite application systems (iCIMS labels).
import { describe, expect, test } from "vitest";
import { PAD, fakeHost, makeEngine, mount, runFill, summaryOf } from "./helpers";

describe("iCIMS labels", () => {
  test("A question is the control's own label, not the section heading plus the raw field id, and a radio option's text after its wrapper span is recovered", async () => {
    mount(`<div class="iCIMS_Forms"><h3>Education</h3>
      <div class="iCIMS_TableRow"><label for="f1_OtherMajor" class="iCIMS_InfoMsg">Other Major/Area of Study</label><input id="f1_OtherMajor" name="OtherMajor"></div>
      <fieldset><legend>Ethnicity</legend>
        <label><input type="radio" name="eth" value="1"><span>Hispanic</span> or Latino</label>
        <label><input type="radio" name="eth" value="2"><span>Not</span> Hispanic</label></fieldset></div>${PAD}`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host)));
    const asked = host.matchRequests.flatMap((r) => r.questions);
    expect(asked.map((q) => q.text)).toEqual(["Other Major/Area of Study", "Ethnicity"]);
    expect(asked[1]?.options).toEqual(["Hispanic or Latino", "Not Hispanic"]);
    expect(s.rows.some((r) => /OtherMajo|Education Other/.test(r.label))).toBe(false);
  });
});
