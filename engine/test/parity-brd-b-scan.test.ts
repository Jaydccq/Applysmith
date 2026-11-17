// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Question text, required flag and selector resolution; Profile-field pass; Drivers for application boards.
import { describe, expect, test } from "vitest";
import { scanControls, toQuestion } from "../src/scan";
import { PAD, byId, fakeHost, infoOf, makeEngine, mount, rowFor, runFill, summaryOf } from "./helpers";
import { mountRows } from "./ats-fixtures";

const questionsOf = () => scanControls(document).filter((i) => toQuestion(i));

describe("Rippling custom questions", () => {
  test("Rippling radio groups keep the real question text, a checkbox behind a visible ARIA proxy is captured and a box with no visible proxy is skipped", () => {
    mount(`<div class="field"><div class="q">Are you legally authorized to work in the United States?</div>
      <label><input type="radio" name="wa" id="wa1" value="y"> Yes</label><label><input type="radio" name="wa" id="wa2" value="n"> No</label></div>
      <label><input type="checkbox" id="px" style="display:none"><span role="checkbox" aria-checked="false" tabindex="0" id="proxy">I acknowledge the privacy notice</span></label>
      <label><input type="checkbox" id="hid" style="display:none"> I have a hidden box</label>
      <div><div>Tell us more</div><textarea id="ta"></textarea></div>`);
    expect(infoOf("wa1").label.text).toBe("Are you legally authorized to work in the United States?");
    expect(infoOf("wa1").options.map((o) => o.label)).toEqual(["Yes", "No"]);
    const asked = questionsOf().map((i) => i.el.id);
    expect(asked).toContain("proxy");
    expect(asked).not.toContain("px");
    expect(asked).not.toContain("hid");
    expect(infoOf("ta").label.text).toBe("Tell us more");
  });

  test("A box the page already ticked is not sent to the matcher again", () => {
    mount(`<label><input type="checkbox" id="done" checked> Already acknowledged</label>${PAD}`);
    expect(questionsOf().map((i) => i.el.id)).not.toContain("done");
  });
});

describe("Workable address and summary", () => {
  test("The visible address box is filled, the aria-hidden mirror inputs the widget owns stay empty and the Summary textarea is left alone", async () => {
    mount(`<label for="address">Address</label><input id="address" autocomplete="street-address">
      <div style="position: absolute; width: 1px; height: 1px; overflow: hidden;"><label><div data-role="illustrated-input"><input id="m1" aria-hidden="true" tabindex="-1" name="street" type="text" autocomplete="address-line1"></div></label></div><div style="position: absolute; width: 1px; height: 1px; overflow: hidden;"><label><div data-role="illustrated-input"><input id="m2" aria-hidden="true" tabindex="-1" name="city" type="text"></div></label></div><div style="position: absolute; width: 1px; height: 1px; overflow: hidden;"><label><div data-role="illustrated-input"><input id="m3" aria-hidden="true" tabindex="-1" name="zip" type="text" autocomplete="postal-code"></div></label></div>
      <label for="summary">Summary</label><textarea id="summary"></textarea><label for="em">Email</label><input id="em">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(byId("address").value).toBe("1 Example Street");
    expect(["m1", "m2", "m3"].map((id) => byId(id).value)).toEqual(["", "", ""]);
    expect(byId("summary").value).toBe("");
    expect(s.rows.map((r) => r.label)).not.toContain("street");
    expect(rowFor(s, "Summary").outcome).toBe("abstained");
  });
});

describe("Breezy rows and salary", () => {
  test("A date caption in a row's wrapper labels none of the row's other controls, and a currency span is never the salary question", () => {
    mount(`<div class="row"><span class="label start-date">Start date</span><input id="a1"><input id="a2"><textarea id="a3"></textarea></div>
      <div class="f"><span class="currency label">$</span><input id="sal" aria-label="Desired salary"></div>`);
    expect(["a1", "a2", "a3"].map((id) => infoOf(id).label.text)).toEqual(["", "", ""]);
    expect(infoOf("sal").label.text).toBe("Desired salary");
  });

  test("An application-level start date answer reaches its own box and never a repeated work row", async () => {
    mount(`<label for="early">Earliest start date</label><input id="early">`);
    mountRows({ entity: "employment", style: "workday", rows: [{}] });
    const host = fakeHost({ "earliest start date": { outcome: "filled", value: "2027-01-04" } });
    await runFill(makeEngine(host), { applicant: { candidate: { fullName: "Alex Example" }, history: { employment: [{ employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true }] } } });
    expect(byId("early").value).toBe("2027-01-04");
    const start = document.getElementById("workExperience-1--startDate") as HTMLInputElement;
    expect(start.value).toBe("2021-03-01");
  });
});

describe("Question bodies and neighbours", () => {
  test("A bot-check radio takes its heading plus the body block that holds the decisive prompt", () => {
    mount(`<div class="field"><div class="title">Verify you are human</div><div class="body">To confirm, select Lightning</div>
      <div class="choices"><label><input type="radio" name="b" id="b1"> Rain</label><label><input type="radio" name="b" id="b2"> Lightning</label></div></div>
      <div class="field"><div class="title">Short</div><div class="body">Describe the thing in detail please</div><textarea id="fq"></textarea></div>`);
    expect(infoOf("b1").label.text).toBe("Verify you are human To confirm, select Lightning");
    expect(infoOf("fq").label.text).toBe("Short Describe the thing in detail please");
  });

  test("A group's question never includes the neighbouring groups' text", () => {
    mount(`<div class="form-group"><label>Race</label><select id="race"><option>Asian</option><option>White</option></select></div>
      <div class="form-group"><label>Gender</label><label><input type="radio" name="g" id="g1"> Male</label><label><input type="radio" name="g" id="g2"> Female</label></div>
      <div class="form-group"><label>Disability status</label><label><input type="radio" name="d" id="d1"> Yes</label><label><input type="radio" name="d" id="d2"> No</label></div>`);
    expect(infoOf("g1").label.text).toBe("Gender");
    expect(infoOf("d1").label.text).toBe("Disability status");
    expect(infoOf("race").label.text).toBe("Race");
  });

  test("A choice group takes its question from the paragraph before it and two groups get different questions", () => {
    mount(`<div><p>Have you previously worked for Example Corp?</p><div class="choices"><label><input type="radio" name="p1" id="p1a"> Yes</label><label><input type="radio" name="p1" id="p1b"> No</label></div></div>
      <div><p>Are you willing to relocate?</p><div class="choices"><label><input type="radio" name="p2" id="p2a"> Yes</label><label><input type="radio" name="p2" id="p2b"> No</label></div></div>`);
    expect(infoOf("p1a").label.text).toBe("Have you previously worked for Example Corp?");
    expect(infoOf("p2a").label.text).toBe("Are you willing to relocate?");
  });

  test("Yes and No questions in a section fieldset keep their own titles and plain option labels, and the section legend is never the question", () => {
    mount(`<fieldset><legend>Application questions</legend>
      <div role="radiogroup" aria-labelledby="t1"><h4 id="t1">Are you eligible to work in the UK?</h4>
        <label id="y1"><input type="radio" name="e" id="e1" aria-labelledby="t1 y1"> Yes</label><label id="n1"><input type="radio" name="e" id="e2" aria-labelledby="t1 n1"> No</label></div>
      <div role="radiogroup" aria-labelledby="t2"><h4 id="t2">Do you need a visa?</h4>
        <label id="y2"><input type="radio" name="v" id="v1" aria-labelledby="t2 y2"> Yes</label><label id="n2"><input type="radio" name="v" id="v2" aria-labelledby="t2 n2"> No</label></div></fieldset>`);
    expect(infoOf("e1").label.text).toBe("Are you eligible to work in the UK?");
    expect(infoOf("v1").label.text).toBe("Do you need a visa?");
    expect(infoOf("e1").options.map((o) => o.label)).toEqual(["Yes", "No"]);
    expect(infoOf("e1").section).toEqual(["Application questions"]);
  });

});
