// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Large option lists answered by exact match; Option and label normalisation.
// Synthetic copies of the markup Lever rendered in the second live dry run with
// the demo identity: a school picker of several thousand names that ends with
// an "Other (School Not Listed)" choice, and a years-of-experience menu.
import { describe, expect, test } from "vitest";
import { ADA, byId, fakeHost, makeEngine, mount, PAD, rowFor, runFill, summaryOf, write } from "./helpers";

const SCHOOL_QUESTION = `Which university are you currently attending or did you last attend? Please select "Other (School Not Listed)" if your school is not listed.`;

function schools(count: number, tail: string[]): string[] {
  return [...Array.from({ length: count }, (_, i) => `Sample College ${String(i).padStart(4, "0")}`), ...tail];
}

function schoolPicker(options: string[]): HTMLSelectElement {
  const list = options.map((o) => `<option value="${o}">${o}</option>`).join("");
  mount(`<ul><li class="application-question custom-question"><div><div class="application-label full-width dropdown"><div class="text">${SCHOOL_QUESTION.replace(/"/g, "&quot;")}<span class="required">✱</span></div></div>
<div class="application-field full-width required-field"><div class="application-dropdown"><select id="school" name="cards[1][field0]" required><option value="">Click Here (If you encounter an issue, make sure your browser is updated)</option>${list}</select></div></div></div></li></ul>${PAD}`);
  return document.getElementById("school") as HTMLSelectElement;
}

describe("Large option lists answered by exact match: a school the list lacks", () => {
  test("A long school list without the answer takes its one 'Other (School Not Listed)' choice", async () => {
    // 600 names stand for the live list's thousands: every long-list rule starts past 100 options, and the fill's cost grows with the count.
    const sel = schoolPicker(schools(600, ["Did not attend university", "Other - School Not Listed"]));
    const host = fakeHost({ [SCHOOL_QUESTION.toLowerCase()]: { outcome: "filled", value: "Example University" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    const asked = host.matchRequests.flatMap((r) => r.questions).find((q) => q.text.startsWith("Which university"));
    expect(asked?.options).toEqual([]);
    expect(rowFor(s, "Which university")).toMatchObject({ outcome: "filled", optionLabel: "Other - School Not Listed" });
    expect(sel.value).toBe("Other - School Not Listed");
  });

  test("A school the list holds is chosen by its own name, never the Other choice", async () => {
    const sel = schoolPicker(schools(150, ["Example University", "Other (School Not Listed)"]));
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Example University" });
    expect(sel.value).toBe("Example University");
  });

  test("Without an Other choice that says not listed the control is left with option-not-found", async () => {
    const sel = schoolPicker(schools(150, ["Did not attend university", "Other"]));
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(sel.value).toBe("");
  });

  test("Two choices that say not listed are never guessed between", async () => {
    const sel = schoolPicker(schools(150, ["Other (School Not Listed)", "Other (Not Listed)"]));
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(sel.value).toBe("");
  });

  test("A not-listed choice on a list that is neither a school nor an employer list stands in for nothing", async () => {
    mount(`<label for="where">Favourite colour</label><select id="where"><option value="">Select</option><option>Red</option><option>Blue</option><option>Other (Not Listed)</option></select>${PAD}`);
    const r = await write("where", "Green");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect((document.getElementById("where") as HTMLSelectElement).value).toBe("");
  });

  test("An employer list takes its not-listed choice the same way", async () => {
    mount(`<label for="emp">Who is your current employer?</label><select id="emp"><option value="">Select</option><option>Acme Corp</option><option>Globex</option><option>Other (Employer Not Listed)</option></select>${PAD}`);
    const r = await write("emp", "Northwind Labs");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Other (Employer Not Listed)" });
  });
});

const ALEX = {
  ...ADA,
  links: { linkedin: "https://linkedin.example.org/in/alex-example", github: "https://github.example.org/alex-example", portfolio: "https://alex.example.org" },
};

/** Greenhouse's link rows: the label wraps nothing, the control carries the same words as its aria-label. */
function greenhouseLinks(): void {
  mount(`<form id="application-form"><div class="application--questions">
<div class="field-wrapper"><div class="text-input-wrapper"><div class="input-wrapper"><label id="question_1-label" for="question_1" class="label label">LinkedIn Profile</label><input id="question_1" class="input input__single-line" aria-label="LinkedIn Profile" aria-describedby="question_1-description question_1-error question_1-help" aria-invalid="false" aria-errormessage="question_1-error" aria-required="false" type="text" maxlength="255" value=""></div></div></div>
<div class="field-wrapper"><div class="text-input-wrapper"><div class="input-wrapper"><label id="question_2-label" for="question_2" class="label label">Website</label><input id="question_2" class="input input__single-line" aria-label="Website" aria-describedby="question_2-description question_2-error question_2-help" aria-invalid="false" aria-errormessage="question_2-error" aria-required="false" type="text" maxlength="255" value=""></div></div></div>
</div></form>`);
}

describe("Profile links: boxes labelled LinkedIn Profile and Website", () => {
  test("A bare Website box the matcher leaves blank stays blank and does not hold back the other rows", async () => {
    greenhouseLinks();
    const host = fakeHost({ website: { outcome: "abstained", reason: "input-mismatch" } });
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ALEX }));
    expect(rowFor(s, "Website")).toMatchObject({ outcome: "abstained" });
    expect(byId("question_2").value).toBe("");
    // The profile pass still reached the LinkedIn box beside it.
    expect(rowFor(s, "LinkedIn Profile")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

});

describe("ADP personal-information page: every label is a live region", () => {
  function adpAddress(): void {
    const field = (id: string, text: string, required: boolean, extra = "") =>
      `<div class="mdf-validated-field "><div class="mdf-label mb-2 flex items-center"><label for="${id}" aria-live="polite">${text}${required ? '<span class="mdf-required-indicator">*</span>' : ""}</label></div><input id="${id}" ${required ? 'aria-required="true"' : ""} class="vdl-textbox ${extra}" type="text" value=""></div>`;
    mount(`<div class="personal-info">${field("personalInfomationMiddleName", "Middle Name", false)}
<label class="consent"><input type="checkbox" name="consentCheckBox"><span>I agree to receive text messages about future opportunities</span></label>
${field("PersonalAddress_address_line1", "Address Line 1", true, "pac-target-input")}
${field("PersonalAddress_city", "City", true)}
${field("PersonalAddress_state", "State / Territory", true)}
${field("PersonalAddress_postalCode", "Postal Code", true)}</div><div class="pac-container"></div>`);
  }

  test("The profile fills the address boxes by their labels, and the optional middle name and text-message consent stay untouched", async () => {
    adpAddress();
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(rowFor(s, "City")).toMatchObject({ outcome: "filled" });
    expect(rowFor(s, "Postal Code")).toMatchObject({ outcome: "filled" });
    expect(rowFor(s, "Address Line 1")).toMatchObject({ outcome: "filled" });
    expect(rowFor(s, "State / Territory")).toMatchObject({ outcome: "filled" });
    expect(byId("PersonalAddress_city").value).toBe("Durham");
    expect(byId("PersonalAddress_postalCode").value).toBe("27701");
    expect(byId("PersonalAddress_state").value).toBe("North Carolina");
    expect(byId("personalInfomationMiddleName").value).toBe("");
    expect((document.querySelector("input[name=consentCheckBox]") as HTMLInputElement).checked).toBe(false);
  });

  test("The address box never takes focus in the task of another box's blur: the page's own focus handler throws then and unmounts the form", async () => {
    adpAddress();
    let settling = false;
    for (const id of ["PersonalAddress_city", "PersonalAddress_state", "PersonalAddress_postalCode"]) {
      byId(id).addEventListener("blur", () => {
        settling = true;
        setTimeout(() => {
          settling = false;
        }, 0);
      });
    }
    byId("PersonalAddress_address_line1").addEventListener("focus", () => {
      if (settling) document.body.innerHTML = "";
    });
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(document.getElementById("PersonalAddress_address_line1")).not.toBeNull();
    expect(rowFor(s, "Address Line 1")).toMatchObject({ outcome: "filled" });
    expect(byId("PersonalAddress_postalCode").value).toBe("27701");
  });
});

describe("ADP Workforce Now application page: dijit text boxes named by a sibling label", () => {
  function wfnAddress(): void {
    const box = (name: string, text: string, required: boolean, id: string) =>
      `<div class="element"><label class="${name}Label${required ? " required" : ""}">${text}</label><div class="dijit dijitTextBox dijitValidationTextBox" id="widget_${id}" role="presentation">` +
      `<div class="dijitReset dijitValidationContainer"><input class="dijitValidationIcon" value="Χ " type="text" tabindex="-1" readonly="readonly" role="presentation"></div>` +
      `<div class="dijitReset dijitInputField dijitInputContainer"><input class="dijitReset dijitInputInner" autocomplete="off" name="${name}" type="text" tabindex="0" id="${id}" value=""></div></div></div>`;
    mount(`<div class="address">${box("city", "City:", false, "uniqName_9_2")}${box("zip", "ZIP Code:", true, "uniqName_9_3")}</div>`);
  }

  test("The validation icon beside each box is no second control: the profile fills City and the required ZIP Code", async () => {
    wfnAddress();
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(rowFor(s, "ZIP Code")).toMatchObject({ outcome: "filled" });
    expect(rowFor(s, "City")).toMatchObject({ outcome: "filled" });
    expect(byId("uniqName_9_3").value).toBe("27701");
    expect(byId("uniqName_9_2").value).toBe("Durham");
  });
});

describe("ADP Workforce Now electronic-signature step: the agreement above a box labelled Reviewed", () => {
  function esign(): HTMLInputElement {
    mount(`<div class="appPanel widescreen"><div class="element"><label><span name="accept" data-dojo-props="required: true"></span> <span>I agree to the above</span></label></div>
<div class="element"><label><div class="dijit dijitCheckBox" role="presentation"><input name="accept" type="checkbox" role="checkbox" aria-checked="false" class="dijitCheckBoxInput" id="uniqName_13_0" value="true" required=""></div> <span class="required"><b>Reviewed</b></span></label>
<input type="hidden" id="eSignature" name="eSignature" value="Ada Example"><input type="hidden" id="esigStatus" name="esigStatus" value="true"></div></div>`);
    return document.getElementById("uniqName_13_0") as HTMLInputElement;
  }

  test("The hidden inputs beside the box are no second control: the agreement is the question and follows the legal setting", async () => {
    let box = esign();
    const on = summaryOf(await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true }));
    expect(rowFor(on, "I agree to the above")).toMatchObject({ outcome: "filled" });
    expect(box.checked).toBe(true);
    box = esign();
    await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: false });
    expect(box.checked).toBe(false);
  });
});

describe("ADP Workforce Now preliminary questions: the next question is drawn once a text answer is left", () => {
  const GPA = "If you have obtained your Bachelors degree in the last 5 years please include your gpa below:";
  const TRAVEL = "What percentage are you willing to travel?";

  test("A question drawn when the written text box loses focus is asked in the same fill", async () => {
    mount(`<div class="appPanel questionRow" style="display: block;"><div class="element"><label for="gpa">${GPA}</label><textarea id="gpa" name="gpa"></textarea></div></div>
<div id="travelRow" class="appPanel questionRow" style="display:none;"><div class="element"><fieldset><legend>${TRAVEL}</legend>
<label><input type="radio" name="travel" value="a"> 0% - 10%</label><label><input type="radio" name="travel" value="b"> 10% - 25%</label><label><input type="radio" name="travel" value="c"> 25% - 50%</label></fieldset></div></div>${PAD}`);
    byId("gpa").addEventListener("focusout", () => {
      if ((byId("gpa") as HTMLTextAreaElement).value) byId("travelRow").style.display = "block";
    });
    const host = fakeHost({
      [GPA.toLowerCase()]: { outcome: "filled", value: "3.8" },
      [TRAVEL.toLowerCase()]: { outcome: "filled", value: "10% - 25%" },
    });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(rowFor(s, "What percentage")).toMatchObject({ outcome: "filled" });
    expect((document.querySelector('input[name="travel"][value="b"]') as HTMLInputElement).checked).toBe(true);
  });
});

describe("ADP Workforce Now check-all-that-apply: every box is named after its own value", () => {
  const PLACES = "Which location(s) would you be willing to relocate to (check all that apply)?";
  const box = (city: string, value: string, i: number) =>
    `<label><div class="dijit dijitReset dijitInline dijitCheckBox" role="presentation"><input name="_ASSESS_62_100735913_0_${value}" type="checkbox" role="checkbox" aria-checked="false" class="dijitReset dijitCheckBoxInput" id="dijit_form_CheckBox_${i}" value="${value}"></div><span class="normal">${city}</span></label>`;

  test("The boxes are one multi-select question and the chosen places are ticked", async () => {
    mount(`<div class="appPanel questionRow" style="display: block;"><div class="element"><label class=""><div><span>${PLACES}</span></div></label>
${box("Atlanta, GA", "100759713", 0)}${box("Chicago, IL", "100759813", 1)}${box("Dallas, TX", "100760013", 2)}</div></div>${PAD}`);
    const host = fakeHost({ [PLACES.toLowerCase()]: { outcome: "filled", optionLabels: ["Atlanta, GA", "Chicago, IL"], multiSelectBinding: true } });
    const s = summaryOf(await runFill(makeEngine(host)));
    const asked = host.matchRequests.flatMap((r) => r.questions).filter((q) => q.kind === "checkbox");
    expect(asked).toHaveLength(1);
    expect(asked[0]).toMatchObject({ text: PLACES, inputType: "multi_select", options: ["Atlanta, GA", "Chicago, IL", "Dallas, TX"] });
    expect(rowFor(s, "Which location")).toMatchObject({ outcome: "filled" });
    expect([0, 1, 2].map((i) => (byId(`dijit_form_CheckBox_${i}`) as HTMLInputElement).checked)).toEqual([true, true, false]);
  });
});

describe("ADP Workforce Now application form: a work-authorization answer the site filled in is asked again", () => {
  const PROOF = "If hired, can you provide proof of your legal right to work in the United States?";

  test("The applicant's own answer replaces the site's", async () => {
    mount(`<label for="proof">${PROOF}</label><select id="proof" required><option value=" ">— Please Specify —</option><option value="Y">Yes</option><option value="N" selected>No</option></select>${PAD}`);
    const host = fakeHost({ [PROOF.toLowerCase()]: { outcome: "filled", value: "Yes", optionLabel: "Yes" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions).some((q) => q.text === PROOF)).toBe(true);
    expect(rowFor(s, "If hired")).toMatchObject({ value: "Yes" });
    expect((byId("proof") as HTMLSelectElement).value).toBe("Y");
  });
});

describe("ADP Workforce Now menus: a row's hidden expand marker and zero-width space are no part of its label", () => {
  test("Each dijit menu row reads as its own name", async () => {
    const { optionsOf } = await import("../src/popup");
    const row = (name: string) =>
      `<tr class="dijitReset dijitMenuItem" role="option"><td class="dijitMenuItemIconCell"><span class="dijitMenuItemIcon"></span></td><td class="dijitMenuItemLabel">​<span class="label">${name}&nbsp;</span></td><td class="dijitMenuItemAccelKey" style="display: none"></td><td class="dijitMenuArrowCell"><span style="visibility: hidden"><span class="dijitMenuExpand"></span><span class="dijitMenuExpandA11y">+</span></span></td></tr>`;
    mount(`<table id="menu" role="listbox"><tbody>${row("Aiken")}${row("Charleston")}${row("Yes")}</tbody></table>`);
    expect(optionsOf(byId("menu")).map((o) => o.label)).toEqual(["Aiken", "Charleston", "Yes"]);
  });
});

describe("Gem application form: captions in spans above their boxes, nameless Yes/No choices", () => {
  const AGE = "Are you 18 years of age or older?";
  const AUTH = "Are you legally authorized to work in the country or jurisdiction where the position to which you are applying is located?";
  const field = (caption: string, control: string) =>
    `<div class="flex-30" style="gap: 6px; flex-direction: column;"><div class="flex-30" style="gap: 4px; flex-direction: column;"><span class="bodyImportant-47 primary-51">${caption}<span class="requiredAsterisk-111"> *</span></span></div>${control}</div>`;
  const text = (caption: string) =>
    `<div class="flex-30" style="gap: 6px; flex-direction: column;"><span class="bodyImportant-47 primary-51">${caption}<span class="requiredAsterisk-76"> *</span></span><div class="textField-77"><div class="inputWrapper-80"><div class="inputElementAndIconWrapper-81"><input data-1p-ignore="true" class="input-84" type="text" value=""></div></div></div></div>`;
  const radio = (id: string, option: string) =>
    `<div><input class="radioInput-113" id="${id}" type="radio"><label for="${id}" class="label-114"><span class="outerCircle-116"><div class="innerCircle-117"></div></span><div class="body-48 primary-51">${option}</div></label><div class="helpTextContainer-123"></div></div>`;
  const box = (id: string, option: string) =>
    `<div class="checkboxContainer-124"><input class="checkboxInput-125" id="${id}" type="checkbox"><label for="${id}" class="label-126"><span class="box-128"></span><div class="body-48 primary-51">${option}</div></label><div class="helpTextContainer-133"></div></div>`;

  function form(): void {
    mount(`<div class="flex-30">${text("First name")}${text("Last name")}
${field(AGE, `<div><div class="flex-30"><div class="flex-30"><div class="flex-30">${radio("age-yes", "Yes")}${radio("age-no", "No")}</div></div></div></div>`)}
${field(AUTH, `<div><div class="flex-30"><div class="flex-30">${box("auth-yes", "Yes")}${box("auth-no", "No")}</div></div></div>`)}</div>${PAD}`);
  }

  test("A caption drawn in a span right above its box names it: First and Last name are the applicant's", async () => {
    form();
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(rowFor(s, "First name")).toMatchObject({ outcome: "filled", value: ADA.candidate.firstName });
    expect(rowFor(s, "Last name")).toMatchObject({ outcome: "filled", value: ADA.candidate.lastName });
  });

  test("A nameless radio pair nested under styling wrappers is asked with the caption above it", async () => {
    form();
    const host = fakeHost({ [AGE.toLowerCase()]: { outcome: "filled", value: "Yes", optionLabel: "Yes" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    const asked = host.matchRequests.flatMap((r) => r.questions);
    expect(asked.find((q) => q.kind === "radio-group")).toMatchObject({ text: AGE, inputType: "yes_no", options: ["Yes", "No"] });
    expect(asked.find((q) => q.kind === "checkbox")).toMatchObject({ text: AUTH, inputType: "yes_no" });
    expect(rowFor(s, AGE)).toMatchObject({ outcome: "filled" });
    expect(byId("age-yes").checked).toBe(true);
  });
});
