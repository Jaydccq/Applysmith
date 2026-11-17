// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Question pass (scan): scanner arbitration, groups, answered-group claims, admission gates.
import { describe, expect, test } from "vitest";
import { scanControls, toQuestion } from "../src/scan";
import { PAD, mount } from "./helpers";

const infosOf = () => scanControls(document);
const questions = () => infosOf().flatMap((i) => {
  const q = toQuestion(i);
  return q ? [q] : [];
});

describe("Scanner arbitration", () => {
  test("a wrapper and its inner input emit one question, output in document order", () => {
    mount(`<label for="first">First thing</label><input id="first">
      <div class="select-container"><label for="rs">Second thing</label><div class="select__control"><div class="select__value-container"><div class="select__placeholder">Select...</div><div class="select__input-container"><input id="rs" role="combobox" aria-haspopup="true" aria-autocomplete="list" aria-expanded="false"></div></div></div></div>
      <div role="combobox" aria-haspopup="listbox" id="wrap" aria-labelledby="wl"><span id="wl">Third thing</span><input id="inner"></div>
      <fieldset><legend>Fourth thing</legend><div><button type="button" id="yes">Yes</button><button type="button" id="no">No</button><input id="proxy" tabindex="-1" style="opacity:0"></div></fieldset>
      <label for="gate">Fifth thing</label><select id="gate"><option value="">Select</option><option>Yes</option><option>No</option></select>
      <label for="follow">If yes, please explain</label><textarea id="follow"></textarea>`);
    // Closed listboxes are asked after their options are read, so the order is checked on what the scan admits.
    const asked = infosOf().filter((i) => i.owner === "matcher" && i.admitted && i.label.text);
    expect(asked.map((i) => i.label.text)).toEqual(["First thing", "Second thing", "Third thing", "Fourth thing", "Fifth thing", "Fifth thing If yes, please explain"]);
    // The follow-up sits after the gate it depends on, and no control is asked twice.
    expect(new Set(asked.map((i) => i.selector)).size).toBe(asked.length);
    const covered = (id: string) => infosOf().filter((i) => i.el.id === id || i.members.some((m) => m.id === id) || i.el.contains(document.getElementById(id))).length;
    // The inner input of the wrapper and the react-select input are each one control; the pill pair's hidden proxy is none of its own.
    expect([covered("rs"), covered("wrap"), covered("inner"), covered("proxy")]).toEqual([1, 1, 1, 0]);
  });

  test("popup search combobox is dropped in favour of the slot trigger button", () => {
    // Neither box names itself a search; the popup's own box shares the trigger's aria-controls id, and the trigger wins by kind whatever the order.
    for (const triggerFirst of [true, false]) {
      const trigger = `<button type="button" id="trig" slot="trigger" aria-haspopup="listbox" aria-controls="pop" aria-expanded="true" aria-labelledby="tl"><span id="tl">Country of residence</span></button>`;
      const popup = `<div id="pop"><input id="filter" role="combobox" aria-controls="pop" aria-label="Filter the list"></div>`;
      mount(`<div>${triggerFirst ? trigger + popup : popup + trigger}</div>${PAD}`);
      const ids = infosOf().map((i) => i.el.id);
      expect(ids, `trigger first: ${triggerFirst}`).toContain("trig");
      expect(ids, `trigger first: ${triggerFirst}`).not.toContain("filter");
    }
    // Two plain inputs that tie on score are both kept.
    mount(`<div id="pop"><input id="a" role="combobox" aria-controls="pop" aria-label="One"><input id="b" role="combobox" aria-controls="pop" aria-label="Two"></div>`);
    expect(infosOf().map((i) => i.el.id)).toEqual(["a", "b"]);
  });
});

describe("Radio and checkbox groups", () => {
  test("two name-less radios under one stem emit one radio-group question", () => {
    mount(`<div class="field"><div class="stem">Do you agree to the schedule?</div><label><input type="radio" id="a"> Yes</label><label><input type="radio" id="b"> No</label></div>
      <fieldset><legend>Preferred contact</legend><label><input type="radio" name="n" id="n1"> Email</label><label><input type="radio" name="n" id="n2"> Phone</label><label><input type="radio" name="n" id="n3"> </label></fieldset>`);
    const qs = questions();
    expect(qs.map((q) => [q.text, q.kind, q.options])).toEqual([
      ["Do you agree to the schedule?", "radio-group", ["Yes", "No"]],
      ["Preferred contact", "radio-group", ["Email", "Phone"]],
    ]);
  });

  test("slotted role=radio options are collected into one question", () => {
    mount(`<spl-radio-group id="g" label="Work authorization"><span role="radio" aria-checked="false" id="r1">Yes</span><span role="radio" aria-checked="false" id="r2">No</span></spl-radio-group>`);
    (document.getElementById("g") as HTMLElement).attachShadow({ mode: "open" }).innerHTML = `<div class="wrap"><slot></slot></div>`;
    const qs = questions();
    expect(qs).toHaveLength(1);
    expect(qs[0]).toMatchObject({ kind: "radio-group", text: "Work authorization", options: ["Yes", "No"] });
  });

  test("display:none radio inside visible role=radio wrapper is scanned", () => {
    mount(`<fieldset><legend>Contact by</legend><div role="radio" aria-checked="false" id="w1"><input type="radio" id="hr1" name="k" value="e" style="display:none">Email</div><div role="radio" aria-checked="false" id="w2"><input type="radio" id="hr2" name="k" value="p" style="display:none">Phone</div></fieldset>
      <input type="checkbox" id="lonely" style="display:none">`);
    const qs = questions();
    expect(qs.map((q) => [q.text, q.options])).toEqual([["Contact by", ["Email", "Phone"]]]);
    // A hidden box with no visible stand-in of any kind is not a control a person can reach.
    expect(infosOf().some((i) => i.members.some((m) => m.id === "lonely") || i.el.id === "lonely")).toBe(false);
  });
});

describe("Checkbox groups", () => {
  test("thirty same-name country boxes under one legend emit one multi_select question", () => {
    const boxes = Array.from({ length: 30 }, (_, i) => `<label><input type="checkbox" name="countries[]" id="c${i}" value="${i}" required> Country ${i}</label>`).join("");
    mount(`<fieldset><legend>Which countries have you worked in?</legend>${boxes}<label><input type="checkbox" name="countries[]" id="gone" value="x" disabled> Disabled country</label></fieldset>
      <fieldset><legend>Hobbies</legend><label><input type="checkbox" name="h" id="h1"> Chess</label><label><input type="checkbox" name="h" id="h2"> Go</label></fieldset>`);
    const qs = questions();
    expect(qs).toHaveLength(1);
    expect(qs[0]).toMatchObject({ kind: "checkbox", inputType: "multi_select", text: "Which countries have you worked in?" });
    expect(qs[0]?.options).toHaveLength(30);
    expect(qs[0]?.options).not.toContain("Disabled country");
    // An optional pair with no instruction and no shared requirement is claimed, not emitted per option.
    expect(infosOf().find((i) => i.el.id === "h1")?.admitted).toBe(false);
  });

  test("a Lever multiple-select list of several boxes is one multi_select question; a list of one box stays a lone box", () => {
    // Valkyrie Trading (Lever, live): "What type of work environment are you seeking?" over In-person, Hybrid and Fully remote.
    const list = (n: string, label: string, values: string[]) => `<li class="application-question custom-question"><div><div class="application-label full-width multiple-select"><div class="text">${label}<span class="required">✱</span></div></div>
      <div class="application-field full-width required-field"><ul data-qa="checkboxes">${values.map((v) => `<li><label><input type="checkbox" name="cards[c][${n}]" value="${v}" required><span class="application-answer-alternative">${v}</span></label></li>`).join("")}</ul></div></div></li>`;
    mount(`<form><ul>${list("field2", "What type of work environment are you seeking?", ["In-person", "Hybrid", "Fully remote"])}${list("field4", "I certify that my answers are true.", ["I affirm and agree"])}</ul></form>`);
    const qs = questions();
    expect(qs.find((q) => q.text.startsWith("What type"))).toMatchObject({ kind: "checkbox", inputType: "multi_select", options: ["In-person", "Hybrid", "Fully remote"] });
    expect(qs.find((q) => q.text.startsWith("I certify"))).toMatchObject({ kind: "checkbox", inputType: "yes_no" });
  });

  test("a Workday checkbox group of several boxes is one multi_select question", () => {
    // The Clearing House (Workday, live): "Shift Available" over Day and Night.
    mount(`<fieldset data-automation-id="416f-CheckboxGroup" aria-required="true"><legend>Shift Available<abbr>*</abbr></legend>
      <div><input type="checkbox" id="d" aria-required="true"><label for="d">Day</label></div><div><input type="checkbox" id="n"><label for="n">Night</label></div></fieldset>`);
    expect(questions().find((q) => q.text.startsWith("Shift Available"))).toMatchObject({ kind: "checkbox", inputType: "multi_select", options: ["Day", "Night"] });
  });

  test("un-required \"I agree to the privacy policy\" box is sent, a stray optional box is not", () => {
    mount(`<fieldset><legend>I agree to the privacy policy</legend><label><input type="checkbox" id="priv"> Agree</label></fieldset>
      <fieldset><legend>Favourite colour</legend><label><input type="checkbox" id="stray"> Blue</label></fieldset>
      <fieldset><legend>Declaration</legend><label><input type="checkbox" id="done" checked> I agree to the terms</label></fieldset>
      <fieldset><legend>I certify the information is true</legend><label><input type="checkbox" id="off" disabled> Certify</label></fieldset>`);
    // A box that is checked already is not asked again; a disabled one is not offered.
    const asked = infosOf().filter((i) => !i.answered).flatMap((i) => toQuestion(i) ?? []);
    expect(asked.map((q) => q.text)).toEqual(["I agree to the privacy policy"]);
    expect(asked[0]).toMatchObject({ kind: "checkbox", options: ["Agree"], required: true });
  });

  test("instruction-only label opening a Lever card adopts the card's heading; one further down the card does not", () => {
    // Hive (Lever, live): the card's h4 "How did you hear about us?" is the question its first field only says how to answer.
    const card = (head: string, lead: string) => `<div class="section application-form"><h4 data-qa="card-name">${head}</h4><input type="hidden" name="cards[c][baseTemplate]" value="{}"><ul>${lead}
      <li class="application-question"><div><div class="application-label"><div class="text">Please check all that apply:</div></div>
      <div class="application-field"><ul data-qa="checkboxes"><li><label><input type="checkbox" name="cards[${head.length}][field0]" value="Friend"><span>Friend</span></label></li>
      <li><label><input type="checkbox" name="cards[${head.length}][field0]" value="LinkedIn"><span>LinkedIn</span></label></li></ul></div></div></li></ul></div>`;
    const before = `<li class="application-question"><div><div class="application-label"><div class="text">What is/was your GPA?</div></div><div class="application-field"><input type="text" name="gpa"></div></div></li>`;
    mount(`<h1>Apply to Example Corp</h1><form>${card("How did you hear about us?", "")}${card("A few more questions", before)}</form>`);
    const qs = questions();
    expect(qs.map((q) => q.text)).toEqual(["How did you hear about us? Please check all that apply:", "What is/was your GPA?", "Please check all that apply:"]);
  });
});
