// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Question text, required flag and selector resolution; Scan cost and overlays.
import { describe, expect, test } from "vitest";
import { cleanQuestion, optionLabel } from "../src/labels";
import { scanControls, toQuestion } from "../src/scan";
import { resolveSelector } from "../src/selector";
import { ADA, byId, fakeHost, infoOf, makeEngine, mount, runFill, summaryOf, write } from "./helpers";

describe("Question text", () => {
  test("Slotted label: a label inside a component's shadow root that holds only * and a slot projecting Gender", () => {
    mount(`<field-box id="fb"><span slot="name">Gender</span></field-box>`);
    const root = (byId("fb") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<label for="inner"><span>*</span><slot name="name"></slot></label><input id="inner">`;
    const info = scanControls(document).find((i) => i.selector.includes(">>>"));
    expect(info?.label.text).toBe("Gender");
    expect(info?.required).toBe(true);
  });

  test("A label element that is itself a live region still states its question (ADP draws every label with aria-live)", () => {
    mount(`<div class="mdf-label"><label for="PersonalAddress_city" aria-live="polite">City<span class="mdf-required-indicator">*</span></label></div><input id="PersonalAddress_city" aria-required="true" class="vdl-textbox" type="text">
      <label for="m" aria-live="polite">Middle Name</label><input id="m" type="text">
      <input id="g" type="radio" name="g"><label for="g" aria-live="polite">Yes</label>`);
    expect(infoOf("PersonalAddress_city").label).toMatchObject({ text: "City", source: "label", marked: true });
    expect(infoOf("m").label).toMatchObject({ text: "Middle Name", source: "label" });
    expect(optionLabel(byId("g"))).toBe("Yes");
  });

  test("A label whose for names nothing but whose words are the question hands its asterisk to the custom group (ADP's screening questions)", () => {
    const radios = (n: number, q: string) => `<sdf-radio-group label="${q}" hide-label="true" value=""><span id="g${n}-label">${q}</span><div role="radiogroup" aria-labelledby="g${n}-label">
      <sdf-radio-button label="Yes" value="Yes" id="qYes_${n}" role="radio" aria-checked="false" tabindex="0"></sdf-radio-button><sdf-radio-button label="No" value="No" id="qNo_${n}" role="radio" aria-checked="false" tabindex="-1"></sdf-radio-button></div></sdf-radio-group>`;
    mount(`<div><label class="qLabel" for="question_0">Are you legally authorized to work in the country you are applying to?<span class="mdf-required-indicator">*</span></label>${radios(0, "Are you legally authorized to work in the country you are applying to?")}</div>
      <div><label class="qLabel" for="question_1">Did you notify your current manager that you are applying for this position?</label>${radios(1, "Did you notify your current manager that you are applying for this position?")}</div>`);
    expect(infoOf("qYes_0")).toMatchObject({ required: true, label: { marked: true } });
    expect(infoOf("qYes_1")).toMatchObject({ required: false, label: { marked: false } });
  });

  test("A ranked slot's bare label takes the question its fieldset's legend asks (Citadel's top two coding languages)", () => {
    const langs = `<option value="">-- Select --</option><option>C++</option><option>Python</option><option>Java</option>`;
    mount(`<fieldset><legend><p>Please select your top two preferred coding languages for interviews:</p></legend>
      <label for="code_preference_1">Preference 1</label><select id="code_preference_1" required>${langs}</select>
      <label for="code_preference_2">Preference 2</label><select id="code_preference_2" required>${langs}</select></fieldset>
      <fieldset><legend>Education</legend><label for="school">School</label><input id="school"></fieldset>`);
    expect(infoOf("code_preference_2").label.text).toBe("Please select your top two preferred coding languages for interviews: Preference 2");
    expect(infoOf("school").label.text).toBe("School");
  });

  test("A typeahead's clear glyph beside the box is no title: the label above it names the box though its for is the box's name (Citadel)", () => {
    mount(`<div class="form-group"><label for="profile[current_employer]">Current or Most Recent Employer</label><div class="typeahead__container"><div class="typeahead__field"><span class="typeahead__query">
      <span class="typeahead__cancel-button">\u00d7</span><span class="typeahead__cancel-button">\u00d7</span><input type="text" name="profile[current_employer]" id="profile_current_employer" placeholder=" " autocomplete="off"></span></div></div></div>`);
    expect(infoOf("profile_current_employer").label).toMatchObject({ text: "Current or Most Recent Employer", direct: "Current or Most Recent Employer" });
  });

  test("Page chrome: a control without id, name or label under a page heading and footer is not answered", async () => {
    mount(`<h1>Join the Example Corp team</h1><input><footer>Copyright Example Corp</footer>`);
    expect(scanControls(document).filter((i) => toQuestion(i))).toHaveLength(0);
  });

  test("The nearest per-field container title wins and the climb stops at a container holding another control", () => {
    mount(`<div class="field"><div class="label">Preferred pronouns</div><input id="a"></div>
      <div class="field"><input id="b"><input id="c"></div>`);
    expect(infoOf("a").label).toMatchObject({ text: "Preferred pronouns", source: "container" });
    expect(infoOf("b").label.text).toBe("");
  });

  test("A description block between the heading and the control is appended and only that part is truncated", () => {
    const long = "d".repeat(900);
    mount(`<div class="field"><div class="label">Why us?</div><div class="help-text">${long}</div><textarea id="t"></textarea></div>`);
    const text = infoOf("t").label.text;
    expect(text.startsWith("Why us? ddd")).toBe(true);
    expect(text.length).toBe("Why us? ".length + 600);
  });

  test("A group's question comes from its own legend; a section legend is a section, never a question", () => {
    mount(`<fieldset><legend>Education</legend>
      <fieldset><legend>Highest level of education?</legend><label><input type="radio" name="a" id="a1"> Bachelor</label><label><input type="radio" name="a" id="a2"> Master</label></fieldset>
      <fieldset><legend>Were you enrolled full time?</legend><label><input type="radio" name="b" id="b1"> Yes</label><label><input type="radio" name="b" id="b2"> No</label></fieldset></fieldset>`);
    expect(infoOf("a1").label.text).toBe("Highest level of education?");
    expect(infoOf("b1").label.text).toBe("Were you enrolled full time?");
    expect(infoOf("a1").section).toEqual(["Education"]);
  });

  test("A labelled group, a host's label attribute or a listbox fieldset that holds two questions captions a section, never either question", () => {
    const yesNo = (name: string, q: string) => `<div><p>${q}</p><div><label><input type="radio" name="${name}" id="${name}1"> Yes</label><label><input type="radio" name="${name}" id="${name}2"> No</label></div></div>`;
    const both = yesNo("vet", "Are you a protected veteran?") + yesNo("dis", "Do you have a disability?");
    mount(`<h3 id="h">Voluntary disclosures</h3><div role="group" aria-labelledby="h">${both}</div>`);
    expect([infoOf("vet1").label.text, infoOf("dis1").label.text]).toEqual(["Are you a protected veteran?", "Do you have a disability?"]);
    mount(`<div role="group" aria-label="Voluntary disclosures">${both}</div>`);
    expect([infoOf("vet1").label.text, infoOf("dis1").label.text]).toEqual(["Are you a protected veteran?", "Do you have a disability?"]);
    mount(`<x-section label="Voluntary disclosures">${both}</x-section>`);
    expect([infoOf("vet1").label.text, infoOf("dis1").label.text]).toEqual(["Are you a protected veteran?", "Do you have a disability?"]);
    // One question in the group or the host: its caption is the question.
    mount(`<h3 id="h">Are you a protected veteran?</h3><div role="group" aria-labelledby="h"><label><input type="radio" name="vet" id="vet1"> Yes</label><label><input type="radio" name="vet"> No</label></div>`);
    expect(infoOf("vet1").label.text).toBe("Are you a protected veteran?");
    const trigger = (id: string, q: string) => `<div><p>${q}</p><button id="${id}" type="button" aria-haspopup="listbox">Select One</button></div>`;
    mount(`<fieldset><legend>Screening questions</legend>${trigger("q1", "Are you authorized to work here?")}${trigger("q2", "Will you need sponsorship?")}</fieldset>`);
    expect([infoOf("q1").label.text, infoOf("q2").label.text]).toEqual(["Are you authorized to work here?", "Will you need sponsorship?"]);
  });

  test("Eightfold's readonly radios under a section legend are asked by their own nested label", async () => {
    // Live markup (Eightfold): the radios carry readonly, the radiogroup's aria-labelledby id does not exist, the fieldset legend names the section.
    const field = (id: string, q: string) => `<div class="grid-module_col field-3Ez-x"><div class="grid-module_row"><div class="grid-module_col"><div class="grid-module_row"><label id="${id}_label" for="${id}" class="label-385LH required-3upJY"><span>${q}</span></label></div></div>
      <div class="grid-module_col"><div role="radiogroup" id="${id}" aria-labelledby="${id}_legend">${["Yes", "No"].map((o) => `<div class="radio-module_selector"><input aria-label="${o}, ${q}" id="${id}-${o}" name="${id}" type="radio" readonly="" aria-required="true" value="${o}"><label for="${id}-${o}"><span class="radio-module_radio-button"></span><span>${o}</span></label></div>`).join("")}</div></div></div></div>`;
    mount(`<fieldset data-form-section-id="My Information"><legend class="sr-only">My Information</legend><div class="grid-module_row">${field("My_Information_candidateIsEmployee", "Are you a current Employee or Intern of Example Co?")}${field("My_Information_candidateIsPreviousWorker", "Have you previously worked as Employee or Intern for Example Co?")}</div></fieldset>`);
    expect(infoOf("My_Information_candidateIsEmployee-Yes").label.text).toBe("Are you a current Employee or Intern of Example Co?");
    expect(await write("My_Information_candidateIsEmployee-Yes", "No")).toMatchObject({ outcome: "filled" });
    expect(byId("My_Information_candidateIsEmployee-No").checked).toBe(true);
  });

  test("A choice group with no label takes the stem before its wrapper: a long directive or question, never a section blob or another question's text", () => {
    // Live Rippling SMS opt-in: no fieldset, legend or aria name; the stem is the previous sibling of the group's grandparent.
    const page = (stem: string) => `<form><div><h4>Application: Software Engineer I</h4>
      <div><div><p>First name</p></div><input id="fn" aria-labelledby="fn-lbl"><span id="fn-lbl">First name</span></div>
      <div class="paddingX--16"><p>${stem}</p></div>
      <div><div><div id="field-130" role="radiogroup">
        <div role="radio" aria-checked="false"><input type="radio" style="display:none" value="true" name="sms" id="sms-y" aria-labelledby="y"><div id="y"><p>Yes - I consent to receiving text messages</p></div></div>
        <div role="radio" aria-checked="false"><input type="radio" style="display:none" value="false" name="sms" id="sms-n" aria-labelledby="n"><div id="n"><p>No - I do not consent to receiving text messages</p></div></div>
      </div></div></div></div></form>`;
    const stem = "Check Yes or No to indicate your agreement to receive text message updates from Example Co regarding your job application. Frequency may vary. Message and data rates may apply. Reply HELP for assistance. Reply STOP to opt out of future messaging.";
    mount(page(stem));
    expect(infoOf("sms-y").label.text).toBe(stem);
    mount(page("Do you consent to receiving text messages?"));
    expect(infoOf("sms-y").label.text).toBe("Do you consent to receiving text messages?");
    const blob = "Individuals seeking employment at our company are considered without regards to race, color, religion, national origin, age, sex, marital status, ancestry, physical or mental disability, veteran status, gender identity, or sexual orientation. If you believe you belong to any of the categories of protected veterans listed below, please indicate by making the appropriate selection. As a government contractor subject to the Vietnam Era Veterans Readjustment Assistance Act, we request this information in order to measure the effectiveness of the outreach and positive recruitment efforts we undertake pursuant to that Act.";
    mount(page(blob));
    expect(infoOf("sms-y").label.text).not.toContain("Individuals seeking employment");
    // A sibling that holds another question's control ends the search.
    mount(`<div><div><label>Previous question<input type="text" name="prev"></label></div><fieldset><legend></legend><label>Yes<input type="radio" name="q" id="q1" value="Y"></label><label>No<input type="radio" name="q" value="N"></label></fieldset></div>`);
    expect(infoOf("q1").label.text).not.toContain("Previous question");
  });

  test("A long legend whose question is followed by a note of examples is asked by the question", () => {
    const note = "(**Examples of past and current subsidiaries and joint ventures include but are not limited to: " + "Example Financial, Example Components Holdings, Example Defense, ".repeat(5) + "and Example Ultium.)*";
    mount(`<fieldset><legend><label><span>Are you currently working or have you previously worked as an employee, contractor or supplier for Example Motors or any of its subsidiaries*, joint ventures*, or Dealers?

${note}<abbr aria-hidden="true">*</abbr></span></label></legend><label><input type="radio" name="w" id="w1"> Yes</label><label><input type="radio" name="w" id="w2"> No</label></fieldset>`);
    expect(infoOf("w1").label.text).toMatch(/^Are you currently working or have you previously worked .* or Dealers\?/);
  });

  test("A long legend's closing question keeps an abbreviation such as U.S. whole (RTX)", () => {
    mount(`<fieldset><legend><div data-automation-id="richText"><p>A U.S. Person is a lawful permanent resident as defined in 8 U.S.C. 1101(a)(20) or who is a protected individual as defined by 8 U.S.C. 1324b(a)(3).&nbsp; <b>U.S. citizens, U.S. nationals, U.S. permanent residents, or individuals granted refugee or asylee status in the U.S. are considered U.S. persons.</b></p>
      <p><i><b>NOTE</b>:&nbsp;Having an employment authorization document (EAD) to work in the United States (for example, an H1-B visa) <b>does NOT qualify you as a U.S. Person in most cases</b>.&nbsp;For a complete definition of a U.S. Person&nbsp;</i><i><a href="https://example.com/def">click here</a></i><i>.</i></p>
      <p><b>Are you a U.S. Person?<abbr title="required">*</abbr></b></p></div></legend>
      <div><button aria-haspopup="listbox" type="button" value="" aria-label=" Select One Required" id="usp">Select One</button><input type="text" value=""></div></fieldset>`);
    expect(infoOf("usp").label.text).toMatch(/^Are you a U\.S\. Person\?/);
  });

  test("A directive that points at a list below keeps the list after it (HP's export-control countries)", () => {
    mount(`<fieldset><legend><div data-automation-id="richText"><p>In order to comply with the U.S. Export Control regulations, HP must conduct specific, pre-interview screenings of candidates globally. In order to conduct these screenings, we must obtain information about citizenship/permanent residency. This Information will be used for the sole purpose of US Export Control screenings and will not be considered in any way<br>during the candidate selection process.<br><br>Please indicate if you are a citizen or permanent resident of ONE OR MORE of the countries listed below, by answering 'yes'. If you are not a citizen/permanent resident of one of the countries listed below, please answer 'no'.<br><br>The countries are: The countries are: Armenia, Azerbaijan, Belarus, Cambodia, China (People's Republic of China), Cuba, Georgia, Hong Kong, Iran, Iraq, Kazakhstan, Kyrgyzstan, Laos, Libya, Macao (Macau), Moldova, Mongolia, Myanmar (Burma), Nicaragua, North Korea (Democratic People’s Republic of Korea), North Sudan (Khartoum), Russia, Syria, Tajikistan, Turkmenistan, Ukraine, Uzbekistan, Venezuela, Vietnam, Yemen.<abbr title="required">*</abbr></p></div></legend>
      <div><button aria-haspopup="listbox" type="button" value="" aria-label=" Select One Required" id="exp">Select One</button><input type="text" value=""></div></fieldset>`);
    const text = infoOf("exp").label.text;
    expect(text).toMatch(/^Please indicate if you are a citizen or permanent resident of ONE OR MORE of the countries listed below, by answering 'yes'\. The countries are: .*Armenia, .*Yemen\./);
    expect(text).not.toContain("In order to comply");
  });

  test("A long legend's required mark after its last stop is no sentence: the closing sentence is the question (HP)", () => {
    mount(`<fieldset><legend><div data-automation-id="richText"><p><b>Conflict of Interest Question 3: <br><br>Certain Legal and ethical restrictions may apply with respect to relationships with government officials.&nbsp; In order to avoid even the appearance of a conflict of interest, please advise:<br>Whether a family member or anyone with whom you have a close personal relationship is a government official with regulatory authority over Example Corp or authority to make or influence a decision to obtain or retain Example Corp business.&nbsp;</b><abbr title="required">*</abbr></p></div></legend>
      <div><button aria-haspopup="listbox" type="button" value="" aria-label=" Select One Required" id="coi">Select One</button><input type="text" value=""></div></fieldset>`);
    expect(infoOf("coi").label.text).toMatch(/^In order to avoid even the appearance of a conflict of interest, please advise: Whether a family member .* business\.$/);
  });

  test("A checkbox group's legendless fieldset inside the fieldset that asks it reads the outer legend", () => {
    const box = (id: string, label: string) => `<div role="row"><div role="cell"><input id="${id}" type="checkbox" aria-required="true"><label for="${id}">${label}</label></div></div>`;
    mount(`<div role="group" aria-labelledby="pic"><h4 id="pic">Personal Info Collection Instructions</h4><p>We invite you to voluntarily self-identify.</p>
      <div data-automation-id="formField-ethnicityMulti"><fieldset><legend><label id="label83"><span>What is / are your race(s) / ethnicity(ies)?<abbr aria-hidden="true">*</abbr></span></label></legend>
      <fieldset data-automation-id="ethnicityMulti-CheckboxGroup" id="personalInfoUS--ethnicityMulti" aria-required="true"><div role="grid">${box("r1", "Asian (United States of America)")}${box("r2", "White (United States of America)")}</div></fieldset></fieldset></div></div>`);
    expect(infoOf("r1").label.text).toBe("What is / are your race(s) / ethnicity(ies)?");
  });

  test("Cleaning: title chrome is stripped by position, a company name inside the question stays, ids and repeats go", () => {
    mount(
      `<label for="a">Software Engineer Why do you want to work at Example Corp?</label><input id="a">
       <label for="b">Preferred Name* input-yJ791fwK</label><input id="b" name="input-yJ791fwK">
       <label for="c">Veteran status Veteran status</label><input id="c">
       <label for="d">/hour</label><input id="d" aria-label="Hourly rate">`,
      "Software Engineer - Example Corp",
    );
    expect(infoOf("a").label.text).toBe("Why do you want to work at Example Corp?");
    expect(infoOf("b").label.text).toBe("Preferred Name");
    expect(infoOf("c").label.text).toBe("Veteran status");
    expect(infoOf("d").label.text).toBe("Hourly rate");
  });

  test("Furniture such as 'Select One Required' is never a question", () => {
    mount(`<label for="a">Select One Required</label><select id="a"><option value="">Select</option><option>x</option></select>`);
    expect(infoOf("a").label.text).toBe("");
    expect(toQuestion(infoOf("a"))).toBeNull();
  });

  test("An aria-labelledby naming a missing element is ignored and aria-label is the last rung", () => {
    mount(`<input id="a" aria-labelledby="nope" aria-label="Portfolio link">`);
    expect(infoOf("a").label).toMatchObject({ text: "Portfolio link", source: "aria-label" });
  });

  test("A group with no label of its own abstains: no question is built", () => {
    mount(`<div><label><input type="checkbox" id="a1"> Accept</label><label><input type="checkbox" id="a2"> Decline</label></div>`);
    expect(infoOf("a1").label.text).toBe("");
    expect(toQuestion(infoOf("a1"))).toBeNull();
  });

  test("Spam trap label: a visible input labelled 'Please leave this field blank' stays empty", async () => {
    mount(`<label for="t">Please leave this field blank</label><input id="t"><label for="fn">First name</label><input id="fn">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(byId("t").value).toBe("");
    expect(s.rows.some((r) => r.label.includes("leave"))).toBe(false);
  });
});

describe("A plain-text editor is a control", () => {
  test("what a person typed into a plaintext-only editor is never the question of the box beside it", () => {
    mount(`<div class="field"><div><div class="title">Salary</div></div><div><div contenteditable="plaintext-only">words typed here</div><input id="n"></div></div>`);
    expect(infoOf("n").label.text).not.toContain("words typed here");
  });
});

/** Ashby's live acknowledgement field: the title's `for` names no control, and only a required question's title carries `_required_<hash>_<n>`. */
const ashbyAck = (title: string) =>
  `<div data-field-path="f1"><fieldset class="_container_1258i_28 _fieldEntry_1e3gg_28 ashby-application-form-input-checkbox-group"><label class="${title} ashby-application-form-question-title" for="f1">I understand that offers of employment are conditional on satisfactory completion of a background check.</label>` +
  `<div class="_option_1258i_34 ashby-application-form-input-checkbox-group-option"><span class="ashby-application-form-input-checkbox-group-option-checkbox _container_1danv_28"><input type="checkbox" id="a1" name="I understand"></span><label for="a1" class="_label_1258i_42 ashby-application-form-input-checkbox-group-option-label">I understand</label></div></fieldset></div>`;

describe("Long legends", () => {
  test("A long legend with no question is read at the line that asks for the answer and the pledge it confirms (Expedia's AI pledge)", () => {
    const legend = "We value the authenticity and originality of responses during our assessment and interview process. Therefore, we ask that you not use Artificial Intelligence (AI) to answer questions. We believe that genuine, personal insights are crucial for a meaningful evaluation. By proceeding, you acknowledge and agree not to utilize any AI tools during the assessment and/or interview process. Note that If selected to interview, you may request a disability accommodation and usage of tools may be considered. Please enter \u201cYes\u201d if you acknowledge. If you enter \u201cNo\u201d we may not consider your application.";
    mount(`<fieldset><legend><div><p><span>${legend}<abbr title="required">*</abbr></span></p></div></legend><div><button aria-haspopup="listbox" type="button" aria-label=" Select One Required" id="ai">Select One</button></div></fieldset>`);
    expect(infoOf("ai").label).toMatchObject({
      text: "By proceeding, you acknowledge and agree not to utilize any AI tools during the assessment and/or interview process. Please enter \u201cYes\u201d if you acknowledge.",
      source: "legend",
    });
    expect(infoOf("ai").markedRequired).toBe(true);
  });

  test("A directive that says how to answer after a verb of its own is the ask (Capital One's Code of Conduct)", () => {
    const legend = "All Capital One associates must follow the Code of Conduct. <b>The Code requires that you inform your manager and submit a Conflict of Interest Disclosure Form to the Ethics Office and receive approval before engaging in any secondary employment or outside business activity.</b> The Ethics Office will assess any conflict of interest, which may result in <b>required conditions, or the cessation of the outside activity.</b></p><br><p>To proceed with your application, please acknowledge receipt and confirm your understanding of your obligation to disclose directly to and receive pre-approval from the Ethics Office if you join Capital One. This is not an endorsement of your outside activity, an offer of employment, or a guarantee of further application steps. Please reach out to the Ethics Office with questions about this requirement and/or process. <b>Please acknowledge your understanding of this requirement by responding \"Yes.\" If you respond \"No\" the Ethics Office will reach out to you with more information and/or to answer questions about our policy if you make it to a final interview.";
    mount(`<fieldset><legend><div><p>${legend}<abbr title="required">*</abbr></b></p></div></legend><div><button aria-haspopup="listbox" type="button" aria-label=" Select One Required" id="coc">Select One</button></div></fieldset>`);
    expect(infoOf("coc").label).toMatchObject({
      text: "To proceed with your application, please acknowledge receipt and confirm your understanding of your obligation to disclose directly to and receive pre-approval from the Ethics Office if you join Capital One. Please acknowledge your understanding of this requirement by responding \"Yes.\"",
      source: "legend",
    });
  });
});

describe("Required flag", () => {
  test.each([
    // The page's own mark, which a consent box needs before the legal-declarations setting may tick it; the broader default is true either way.
    { title: "a legend's hashed class", html: `<fieldset><legend class="_required_ab12">Pick one</legend><label><input type="radio" name="a" id="a1"> One</label><label><input type="radio" name="a" id="a2"> Two</label></fieldset>`, marked: true },
    { title: "an Ashby title's hashed and indexed class", html: ashbyAck("_heading_f7cvd_52 _required_f7cvd_91 _label_1e3gg_42"), marked: true },
    { title: "an Ashby title without it", html: ashbyAck("_heading_f7cvd_52 _label_1e3gg_42"), marked: false },
  ])("Class-only required marker: $title", ({ html, marked }) => {
    mount(html);
    expect(infoOf("a1").markedRequired).toBe(marked);
  });

  test("Required comes from the attribute, aria-required, a trailing asterisk or the default; file slots default to optional", () => {
    mount(`<label for="a">A</label><input id="a" required><label for="b">B</label><input id="b" aria-required="true">
      <label for="c">C *</label><input id="c"><label for="e">E</label><input id="e" aria-required="false">
      <label for="f">F (optional)</label><input id="f"><label for="u">Resume</label><input id="u" type="file">`);
    expect(["a", "b", "c", "e", "f", "u"].map((id) => infoOf(id).required)).toEqual([true, true, true, false, false, false]);
    mount(`<label for="d">D</label><input id="d"><label for="u">Resume</label><input id="u" type="file">`);
    expect([infoOf("d").required, infoOf("u").required]).toEqual([true, false]);
  });

  test("A label that ends by saying when it is needed is not a required marker", () => {
    // evlo: "Desired Compensation (used only if required)" carries no asterisk on a page that marks required questions with one.
    mount(`<label for="a">Start date *</label><input id="a"><label for="b">Desired Compensation (used only if required)</label><input id="b">
      <label for="c">Salary (required)</label><input id="c"><label for="d">Visa details, if required</label><input id="d">`);
    expect(["a", "b", "c", "d"].map((id) => infoOf(id).required)).toEqual([true, false, true, false]);
    expect(infoOf("b").label.text).toBe("Desired Compensation (used only if required)");
    expect(infoOf("c").label.text).toBe("Salary");
  });

  test("A page whose convention is a trailing asterisk marks an unmarked label optional", () => {
    mount(`<label for="a">A *</label><input id="a"><label for="b">B</label><input id="b">`);
    expect(infoOf("a").required).toBe(true);
    expect(infoOf("b").required).toBe(false);
  });

  test("A form drawn inside a shadow root follows its own convention: an unmarked label there is optional, by asterisk or by class", () => {
    const shadowForm = (html: string) => {
      mount(`<x-form id="host"></x-form>`);
      byId("host").attachShadow({ mode: "open" }).innerHTML = html;
      return scanControls(document).map((i) => i.required);
    };
    expect(shadowForm(`<label for="a">A *</label><input id="a"><label for="b">B</label><input id="b">`)).toEqual([true, false]);
    expect(shadowForm(`<label class="field-required" for="a">A</label><input id="a"><label class="field" for="b">B</label><input id="b">`)).toEqual([true, false]);
  });

  test("A page whose labels carry a required class draws its asterisk: an unmarked label is optional (Boeing's Taleo)", () => {
    mount(`<label class="oracletaleocwsv2-required form-label" for="a">Last Name</label><input id="a" class="form-control" required>
      <label class="form-label" for="b">Middle</label><input id="b" class="form-control">
      <label class="oracletaleocwsv2-required form-label" for="c">Internal Candidate</label><select id="c"><option value="-1">Please select</option><option>No</option><option>Yes</option></select>
      <label class="not-required form-label" for="d">Referred By</label><input id="d">
      <div class="checkbox oracletaleocwsv2-custom-checkbox"><i class="fa" tabindex="0"></i><input type="checkbox" name="notificationCheckBox" id="e"></div><span>I agree to <a href="#" class="statementLink">Boeing Applicant Privacy Policy</a></span>`);
    expect(["a", "b", "c", "d"].map((id) => infoOf(id).required)).toEqual([true, false, true, false]);
    expect(infoOf("c").markedRequired).toBe(true);
    // The sign-up's agreement has a caption, not a label: nothing on it could carry the class, so the default holds.
    expect(infoOf("e")).toMatchObject({ required: true, label: { text: "I agree to Boeing Applicant Privacy Policy" } });
  });
});

describe("Selectors", () => {
  test("Duplicate id: a block with id=Country rendered three times gives each control a distinct path selector", async () => {
    mount(`${[1, 2, 3].map((n) => `<div id="Country"><label>Country ${n}<select><option value="">Select</option><option>Canada</option></select></label></div>`).join("")}<label for="x">First name</label><input id="x">`);
    const infos = scanControls(document).filter((i) => i.kind === "select");
    expect(new Set(infos.map((i) => i.selector)).size).toBe(3);
    for (const i of infos) expect(resolveSelector(document, i.selector)).toBe(i.el);
  });

  test("Workday's résumé box is found again after a line is drawn above it", () => {
    mount(`<div><div><div><div><div><div><p><span>Upload a file (5MB max)</span></p><div><div><button type="button" data-automation-id="select-files">Select files</button></div><input data-automation-id="file-upload-input-ref" type="file"></div></div></div></div></div></div></div>`);
    const info = scanControls(document).find((i) => i.kind === "file");
    const input = document.querySelector("input[type=file]") as HTMLElement;
    const holder = input.parentElement as HTMLElement;
    holder.insertBefore(document.createElement("div"), holder.firstChild);
    holder.parentElement?.insertBefore(document.createElement("p"), holder);
    expect(resolveSelector(document, info?.selector as string)).toBe(input);
  });

  test("A selector resolves a control that is not a CSS identifier and one inside a shadow root", () => {
    mount(`<input id="1abc:def" aria-label="Weird id"><div id="h"></div>`);
    const root = (byId("h") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<input aria-label="Inside"><input aria-label="Also inside">`;
    for (const info of scanControls(document)) expect(resolveSelector(document, info.selector)).toBe(info.el);
    expect(scanControls(document).map((i) => i.label.text)).toEqual(["Weird id", "Inside", "Also inside"]);
  });

  test("A name selector is used when the name is unique, an id when the id is", () => {
    mount(`<input name="email" aria-label="Email"><input id="z" aria-label="Z">`);
    expect(scanControls(document).map((i) => i.selector)).toEqual(['input[name="email"]', '[id="z"]']);
  });
});

describe("Scan cost", () => {
  test("Many controls: 400 controls scan quickly and each yields one entry", () => {
    mount(Array.from({ length: 400 }, (_, i) => `<label for="c${i}">Question ${i}</label><input id="c${i}">`).join(""));
    // Processor time, not the wall clock: a loaded machine stretches the clock but not the work.
    const t0 = process.cpuUsage();
    expect(scanControls(document)).toHaveLength(400);
    const used = process.cpuUsage(t0);
    expect((used.user + used.system) / 1000).toBeLessThan(3000);
  });
});

describe("Disabled controls", () => {
  test("A control inside a disabled fieldset, and a disabled sibling checkbox, are never asked", () => {
    mount(`<fieldset disabled><label for="a">Locked</label><input id="a"></fieldset><label for="b">Open</label><input id="b">
      <fieldset><legend>Pick</legend><label><input type="checkbox" id="c1" name="p"> One</label><label><input type="checkbox" id="c2" name="p" disabled> Two</label></fieldset>`);
    expect(scanControls(document).map((i) => i.el.id)).toEqual(["b", "c1"]);
    expect(infoOf("c1").members).toHaveLength(1);
  });
});

describe("Own control tokens", () => {
  test("Only whole tokens are removed from the question text", () => {
    expect(cleanQuestion("Are you authorized to work here? auth", { tokens: ["auth"] })).toBe("Are you authorized to work here?");
    expect(cleanQuestion("Do you require visa sponsorship?", { tokens: ["visa"] })).toBe("Do you require visa sponsorship?");
    expect(cleanQuestion("Phone number phone", { tokens: ["phone"] })).toBe("Phone number");
    expect(cleanQuestion("What is your firstName name", { tokens: ["firstName"] })).toBe("What is your name");
    expect(cleanQuestion("Do you require visa_sponsorship sponsorship?", { tokens: ["visa_sponsorship"] })).toBe("Do you require sponsorship?");
  });
});

describe("Labels the page draws without a label element", () => {
  test("A placeholder alone is the question, and the profile lane then claims the box (Breezy's direct apply form)", async () => {
    mount(`<form><input required placeholder="Full Name" type="text" name="cName"><input required placeholder="Email Address" type="email" name="cEmail"><input placeholder="Phone Number" type="text" name="cPhoneNumber"></form>`);
    const name = scanControls(document).find((i) => i.el.getAttribute("name") === "cName");
    expect(name?.label).toMatchObject({ text: "Full Name", direct: "Full Name", source: "placeholder" });
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));
    expect(s.rows.map((r) => [r.label, r.outcome])).toEqual([["Full Name", "filled"], ["Email Address", "filled"], ["Phone Number", "filled"]]);
    expect(Array.from(document.querySelectorAll("input")).map((i) => i.value)).toEqual(["Ada Example", "ada@example.com", "+1 555 0100"]);
  });

  test("The climb to a custom form's title passes the hidden autosize twin of an MUI text area (builtin.com)", () => {
    mount(`<div class="MuiBox-root css-1qm1lh"><div class="css-l5c1s3">
      <div class="typography__BaseType-iAsBsJ styles__FormLabel-drdTQT gnUfex" style="margin-bottom: 0px;">Open custral.com/activity. Tell us one thing you would change about it, and why. *</div>
      <div class="MuiFormControl-root MuiFormControl-fullWidth MuiTextField-root css-feqhe6"><div class="MuiInputBase-root MuiOutlinedInput-root MuiInputBase-multiline css-p224s8">
        <textarea aria-invalid="false" id=":rc:" name="f128c377-c4db-4376-866f-a68f4c9d71fe" required="" class="MuiInputBase-input" style="height: 20px; overflow: hidden;"></textarea>
        <textarea aria-hidden="true" class="MuiInputBase-input" readonly="" tabindex="-1" style="visibility: hidden; position: absolute; overflow: hidden; height: 0px;"></textarea>
        <fieldset aria-hidden="true" class="MuiOutlinedInput-notchedOutline css-igs3ac"><legend class="css-hdw1oc"><span class="notranslate">&#8203;</span></legend></fieldset>
      </div></div></div></div>`);
    expect(infoOf(":rc:").label.text).toContain("Tell us one thing you would change about it, and why.");
  });

  test("JazzHR's address placeholders name City, State/Province and Postal", () => {
    mount(`<input type="text" id="c" placeholder="City"><input type="text" id="s" placeholder="State/Province"><input type="text" id="z" placeholder="Postal">`);
    expect(["c", "s", "z"].map((id) => infoOf(id).label.text)).toEqual(["City", "State/Province", "Postal"]);
  });

  test("A sample value, an instruction, a menu prompt or a combobox hint is no question", () => {
    mount(`<input id="a" placeholder="e.g. 555-0100"><input id="b" placeholder="john@example.com"><input id="c" placeholder="MM/DD/YYYY"><input id="d" placeholder="--Select One--">
      <input id="e" placeholder="Start typing your answer"><input id="f" role="combobox" placeholder="Choose a city">`);
    for (const id of ["a", "b", "c", "d", "e", "f"]) expect(infoOf(id).label).toMatchObject({ text: "", source: "none" });
  });

  test("Every other source outranks the placeholder, and a faded box with only a placeholder is still a decoy", () => {
    mount(`<label for="l">Nickname</label><input id="l" placeholder="Full Name"><input id="a" aria-label="Portfolio" placeholder="Website">
      <input id="h" style="opacity:0" placeholder="Email Address"><input id="v" placeholder="Phone Number">`);
    expect(infoOf("l").label).toMatchObject({ text: "Nickname", source: "label" });
    expect(infoOf("a").label).toMatchObject({ text: "Portfolio", source: "aria-label" });
    expect(scanControls(document).map((i) => i.el.id)).toEqual(["l", "a", "v"]);
  });
});

describe("A status named beside the label is not the label", () => {
  test("A wrapping label named by a screen-reader count and the field title reads the title (Rippling's upload)", () => {
    mount(`<div data-testid="field"><span id="field-8-label">Résumé</span><span>*</span>
      <div data-testid="screen-reader-only" id="file-input-10">Total 0 file selected</div>
      <label data-testid="resume" aria-labelledby="file-input-10 field-8-label"><input title="" accept=".doc,.docx,.pdf" type="File" id="field-8"><button type="button"><span>Drop or select (.doc / .docx / .pdf)</span></button></label></div>`);
    expect(infoOf("field-8").label).toMatchObject({ text: "Résumé", direct: "Résumé", source: "aria-labelledby" });
  });

  test("A file input whose label only names its button reads the upload group's title (Greenhouse's Attach)", () => {
    mount(`<div role="group" aria-labelledby="ul-1" aria-required="true" class="file-upload"><div id="ul-1" class="label upload-label">Undergraduate Transcript<span class="required">*</span></div>
      <div class="file-upload__wrapper"><div class="button-container"><div><button type="button">Attach</button><label class="visually-hidden" for="q1">Attach</label><input id="q1" class="visually-hidden" type="file"></div>
      <button type="button">Dropbox</button></div></div></div><label for="q2">Attach</label><input id="q2" type="file">`);
    expect(infoOf("q1").label).toMatchObject({ text: "Undergraduate Transcript", source: "aria-labelledby" });
    expect(infoOf("q2").label.text).toBe("Attach");
  });

  test("A live region or a status role among the named ids is left out when another id says something; a lone one is still the name", () => {
    mount(`<span id="t">Phone</span><span id="s" role="status">Saved</span><span id="p" aria-live="polite">Typing</span>
      <input id="a" aria-labelledby="s t p"><span id="q" class="sr-only">Search jobs</span><input id="b" aria-labelledby="q">`);
    expect(infoOf("a").label).toMatchObject({ text: "Phone", source: "aria-labelledby" });
    expect(infoOf("b").label.text).toBe("Search jobs");
  });
});

describe("A lone checkbox states its own label", () => {
  test("The wrapping label is the question when nothing above the box asks one (Oracle's terms box, JazzHR's acknowledgement)", () => {
    mount(`<div><label><input type="checkbox" id="t" name="terms"> I agree with the terms and conditions</label></div>
      <div><label for="o"><input type="checkbox" id="o" name="optin">I acknowledge</label></div>`);
    expect(infoOf("t")).toMatchObject({ kind: "checkbox", admitted: true, label: { text: "I agree with the terms and conditions", source: "label" } });
    expect(infoOf("o").label).toMatchObject({ text: "I acknowledge", direct: "I acknowledge" });
  });

  test("The title above the box keeps the question; a title with a marker beside the box marks it required", () => {
    mount(`<div class="field"><div class="label">Are you over 18?</div><label><input type="checkbox" id="y"> Yes</label></div>
      <div class="form-group"><label for="h">Please check the box to acknowledge.<i>*</i></label><input type="hidden" id="h"><label class="checkbox" for="k"><input type="checkbox" id="k"> I acknowledge</label></div>`);
    expect(infoOf("y").label).toMatchObject({ text: "Are you over 18?", source: "container" });
    expect(infoOf("k")).toMatchObject({ label: { text: "I acknowledge", marked: true }, markedRequired: true });
  });

  test("Several boxes of one question stay a group and read the legend, not their own words", () => {
    mount(`<fieldset><legend>Which apply?</legend><label><input type="checkbox" name="p" id="a"> One</label><label><input type="checkbox" name="p" id="b"> Two</label></fieldset>`);
    expect(infoOf("a").label).toMatchObject({ text: "Which apply?", source: "legend" });
  });
});

describe("A custom question's title sits above styling wrappers and the list its box has open", () => {
  /** Rippling's custom question: the title is a block ahead of the field, and the field wraps its combobox in single-child divs. */
  const question = (open: boolean) => `<div class="marginY--36">
    <div class="paddingX--16"><div class="marginBottom--4"><div class="css-i4dt0z">Are you a US person? <div class="css-18yxg8r"></div></div></div></div>
    <div class="css-2a15f5" data-testid="field"><div class="css-1fttcpj"><div class="css-k5dvsw"><div>
      <div data-testid="select-controller" class="etc2niq4"><div class="etc2niq7"><div id="field-62" role="combobox" aria-autocomplete="list" aria-haspopup="listbox" aria-expanded="${open}" aria-label="Select" aria-required="true" tabindex="0" aria-controls="field-62-list"><p>Select</p></div></div><div class="etc2niq0"><span aria-hidden="true"></span></div></div>
      ${open ? `<div data-testid="popper" role="dialog" aria-label="Select"><div><ul id="field-62-list" role="listbox" aria-required="true"><li id="field-62-list-option-0" role="option">Yes</li><li id="field-62-list-option-1" role="option">No</li></ul></div></div>` : ""}
    </div></div><div id="form-field-tip-61" class="sr-only"></div></div></div>
  </div>`;

  test("The title is the question whether the list is open or closed, and the box stays required", () => {
    for (const open of [false, true]) {
      mount(question(open));
      expect(infoOf("field-62"), `open=${open}`).toMatchObject({ kind: "custom-listbox", label: { text: "Are you a US person?", source: "container" }, required: true });
    }
  });
});

describe("A select's shown value is its answer, not its question", () => {
  /** Bending Spoons: a title row holds the question's label; the react-select below it already shows a value. */
  const field = (shown: string) => `<div class="relative"><div class="flex items-end"><label class="label"><span class="label--title">How did you learn about the job?</span></label><label class="label label--requirements"></label></div>
    <div class="react-select" id="How-did-you-learn-about-the-job"><div class="react-select__control"><div class="react-select__value-container">${shown}<input id="rs" role="combobox" aria-expanded="false" aria-haspopup="true" aria-readonly="true" inputmode="none" value=""></div><div class="react-select__indicators"></div></div></div></div>`;

  test("The title row names the box whether it shows a value or its placeholder, even one that is no stock wording", () => {
    for (const shown of [`<div class="react-select__single-value css-qc6sy-singleValue">Job post on LinkedIn</div>`, `<div class="css-qc6sy-singleValue">Job post on LinkedIn</div>`, `<div class="react-select__placeholder">Select...</div>`, `<div class="react-select__placeholder css-14el2xx-placeholder">Search...</div>`]) {
      mount(field(shown));
      expect(infoOf("rs").label.text, shown).toBe("How did you learn about the job?");
    }
  });
});

describe("A lone box in a wrapper of its own is captioned beside the wrapper", () => {
  /** Taleo Business Edition's sign-up: an icon and the input in a styled div, the caption after it. */
  const box = (id: string, wrapAttrs: string, caption: string) =>
    `<div class="oracletaleocwsv2-form-group-full"><div style="display: inline-block" class="checkbox oracletaleocwsv2-custom-checkbox"${wrapAttrs}><i class="fa"></i><input type="checkbox" id="${id}"></div>${caption}</div>`;

  test("The wrapper's aria-labelledby, else the span after it, names the box, not the paragraph above", () => {
    mount(`<p>If you are a returning applicant, please sign in or reset your password using the Login button.</p>
      ${box("guest", ` role="checkbox" aria-labelledby="label-guest"`, `<span id="label-guest">Continue as a guest</span><p></p>`)}
      <h2>Applicant Privacy Policy</h2><p>This notice pertains to the collection and processing of the personal information of applicants.</p>
      ${box("agree", "", `<span>I agree to <a href="#" class="statementLink">Boeing Applicant Privacy Policy</a></span>`)}`);
    expect(infoOf("guest").label.text).toBe("Continue as a guest");
    expect(infoOf("agree").label).toMatchObject({ text: "I agree to Boeing Applicant Privacy Policy", source: "label" });
  });

  test("A hidden box drawn by the icon in its wrapper is scanned under its caption; one with nothing drawn is not", () => {
    mount(`${box("drawn", "", `<span>I agree to <a href="#">Boeing Applicant Privacy Policy</a></span>`).replace('<input type="checkbox" id="drawn">', '<input type="checkbox" id="drawn" style="display:none">')}
      <div><input type="checkbox" id="bare" style="display:none"></div><span>Send me job alerts</span>`);
    expect(infoOf("drawn").label.text).toBe("I agree to Boeing Applicant Privacy Policy");
    expect(scanControls(document).some((i) => i.selector.includes("bare"))).toBe(false);
  });

  test("A label that only draws the box captions nothing; the text after it names the box", () => {
    mount(`<div class="loginAgreement"><label class="atsx-checkbox-wrapper"><span class="atsx-checkbox"><input type="checkbox" id="agree" class="atsx-checkbox-input"><span class="atsx-checkbox-inner"></span></span></label>I have read and agree to the <a>User Agreement</a> and <a>Applicant Privacy Policy</a></div>
      <p>Phone</p><div><label><span><input type="checkbox" id="field"></span></label><div>Extension <input id="ext"></div></div>`);
    expect(infoOf("agree").label).toMatchObject({ text: "I have read and agree to the User Agreement and Applicant Privacy Policy", source: "label" });
    expect(infoOf("field").label.text).not.toContain("Extension");
  });

  test("A wrapper that holds the caption itself, or a field after it, is no such wrapper", () => {
    mount(`<p>Stay in touch</p><div><label><input type="checkbox" id="own"> Send me job alerts</label></div>
      <p>Phone</p><div><i></i><input type="checkbox" id="field"></div><span>Extension <input id="ext"></span>`);
    expect(infoOf("own").label.text).toBe("Send me job alerts");
    expect(infoOf("field").label.text).not.toContain("Extension");
  });
});

describe("A label for the wrapper of a group's boxes names the group", () => {
  const box = (o: string, i: number) =>
    `<div class="checkbox"><label><span><input aria-label="${o}" type="checkbox" id="dq_${i}" value="${i}"><span class="checkmark"></span><span>${o}</span></span></label></div>`;
  test("Phenom's demographic follow-up, in a fieldset that draws the whole section, is asked by the label pointing at its checkboxes; a label for another wrapper is not", () => {
    mount(`<form><fieldset><div class="form-group"><label for="race">Race? (select only one)</label><select id="race"><option>East Asian</option></select></div>
      <div class="form-group field field-array"><label class="control-label" for="dq">If you did not select, “Two or More Race/Ethnicities”, please answer “Does not apply”.<span class="required">*</span></label>
      <div class="checkboxes" id="dq">${["East Asian", "Does not apply"].map(box).join("")}</div></div></fieldset>
      <div class="form-group"><label for="elsewhere">Pick your offices</label><div id="elsewhere"></div><div class="checkboxes">${["Austin", "Remote"].map((o, i) => box(o, i + 5)).join("")}</div></div></form>`);
    const group = scanControls(document).find((i) => i.members.some((m) => m.id === "dq_0"))!;
    expect(group.members).toHaveLength(2);
    expect(group.label.text).toBe("If you did not select, “Two or More Race/Ethnicities”, please answer “Does not apply”.");
    expect(group.required).toBe(true);
    const other = scanControls(document).find((i) => i.members.some((m) => m.id === "dq_5"))!;
    expect(other.label.text).not.toContain("Pick your offices");
  });
});
