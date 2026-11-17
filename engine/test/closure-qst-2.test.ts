// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Question pass (text scan, question text, section path, re-answering a declined control).
import { describe, expect, test } from "vitest";
import { scanControls, toQuestion } from "../src/scan";
import { PAD, byId, fakeHost, makeEngine, mount, runFill } from "./helpers";

const infos = () => scanControls(document);
const infoById = (id: string) => {
  const found = infos().find((i) => i.el.id === id);
  if (!found) throw new Error(`no control ${id}`);
  return found;
};

describe("Text and textarea scan", () => {
  test("textarea with bare \"Please specify\" after a select is typed text", () => {
    mount(`<label for="src">How did you hear about us?</label><select id="src"><option value="">Select</option><option>Other</option></select>
      <textarea id="spec" aria-label="Please specify"></textarea>
      <label for="why">Why do you want this role?</label><textarea id="why" maxlength="800"></textarea>
      <label for="ro">Read only note</label><input id="ro" readonly><label for="dis">Disabled note</label><input id="dis" disabled>`);
    const spec = toQuestion(infoById("spec"));
    expect(spec).toMatchObject({ kind: "text", inputType: "text", text: "How did you hear about us? Please specify" });
    const why = toQuestion(infoById("why"));
    expect(why).toMatchObject({ kind: "text", inputType: "long_text", maxLength: 800 });
    expect(infos().map((i) => i.el.id)).not.toContain("ro");
    expect(infos().map((i) => i.el.id)).not.toContain("dis");
    // With nothing before it, the stem stays bare.
    mount(`<textarea id="alone" aria-label="Please specify"></textarea>`);
    expect(infoById("alone").label.text).toBe("Please specify");
  });

  test("filled current-employer box is re-sent once, then skipped", async () => {
    mount(`<label for="co">Current employer</label><input id="co" value="Wrong Parse Inc">
      <label for="sal">Desired salary</label><input id="sal" value="90000">
      <label for="c">Which country do you live in?</label><select id="c"><option value="">Select</option><option>Canada</option></select><div id="slot"></div>${PAD}`);
    byId("c").addEventListener("change", () => {
      byId("slot").innerHTML = `<label for="st">Which province?</label><input id="st">`;
    });
    const host = fakeHost({
      "current employer": { outcome: "abstained", reason: "no-entry" },
      "which country do you live in?": { outcome: "filled", value: "Canada" },
      "which province?": { outcome: "filled", value: "Ontario" },
    });
    await runFill(makeEngine(host));
    const texts = host.matchRequests.flatMap((r) => r.questions.map((q) => q.text));
    // Machine-populated fields are asked although they hold a value, but only once however many passes the run takes; other filled boxes are never asked.
    expect(host.matchRequests.length).toBeGreaterThanOrEqual(2);
    expect(texts.filter((t) => t === "Current employer")).toHaveLength(1);
    expect(texts).not.toContain("Desired salary");
    expect(byId("co").value).toBe("Wrong Parse Inc");
  });

  // Ashby's Yes/No pills and a plain radio group hold their question in the legend of their own fieldset, which is the group, not a section.
  test("a stem after a group whose question is its own fieldset's legend takes that group's question, not an earlier box's", () => {
    mount(`<div class="_fieldEntry_x1y2z"><label for="li">LinkedIn Profile</label><input id="li"></div>
      <div class="_fieldEntry_x1y2z"><fieldset><legend>Have you ever been convicted of a felony? *</legend><div class="_yesno_a1b2c"><button type="button" aria-pressed="false">Yes</button><button type="button" aria-pressed="false">No</button></div></fieldset></div>
      <div class="_fieldEntry_x1y2z"><label for="why">If yes, please provide details *</label><input id="why"></div>
      <section><fieldset><legend>Do you hold a licence?</legend><label><input type="radio" name="lic" value="Yes">Yes</label><label><input type="radio" name="lic" value="No">No</label></fieldset>
      <label for="which">If yes, please specify</label><input id="which"></section>`);
    expect(infoById("why").label.text).toBe("Have you ever been convicted of a felony? If yes, please provide details");
    expect(infoById("which").label.text).toBe("Do you hold a licence? If yes, please specify");
  });
});

describe("Question text", () => {
  test("react-select description id is appended to a one-word label", () => {
    mount(`<label for="react-select-3-input">Role</label><input id="react-select-3-input" role="combobox" aria-haspopup="true"><div id="react-select-3-input-description">What role interests you most?</div>
      <label for="why">Why?</label><input id="why" aria-describedby="why-desc"><div id="why-desc">Why do you want to work here?</div>
      <label for="dup">Why do you want to work here?</label><input id="dup" aria-describedby="dup-desc"><div id="dup-desc">Why do you want to work here?</div>
      <label for="note">Note</label><input id="note" aria-describedby="note-desc"><div id="note-desc">Enter a short note, max 40 characters</div>
      <label for="where">From where do you intend to work?</label><input id="where" aria-describedby="where-desc"><div id="where-desc"><p>Please list city and state. (ie: San Francisco, CA)</p></div>
      <label for="long">Long</label><input id="long" aria-describedby="long-desc"><div id="long-desc">${"Is this a very long description? ".repeat(15)}</div>
      <label for="cert">I certify that my answers are true.</label><input id="cert" aria-describedby="cert-desc"><div id="cert-desc"><p>Please sign and date below.</p></div>
      <label for="esig">I certify that the facts set forth in this application are true.</label><input id="esig" aria-describedby="esig-desc"><div id="esig-desc"><p>Please enter your full name and today's date to signify your electronic signature for this statement.</p></div>
      <label for="pass">Legal name</label><input id="pass" aria-describedby="pass-desc"><div id="pass-desc"><p>Please enter your full name as it appears on your passport.</p></div>
      <label for="design">Design notes</label><input id="design" aria-describedby="design-desc"><div id="design-desc">Signal processing background preferred.</div>
      <div class="_fieldEntry_ab12"><label class="_heading_ab" for="ae1">Why you?</label><div class="_description_ab">Tell us what draws you to this team and role?</div><input id="ae1"></div>
      <div class="_fieldEntry_ab12"><label class="_heading_ab" for="ae2">Empty block</label><div class="_description_ab"></div><input id="ae2"></div>
      <div class="_fieldEntry_ab12"><label class="_heading_ab" for="ae3">After the control</label><input id="ae3"><div class="_description_ab">This follows the control?</div></div>
      <div class="_fieldEntry_ab12"><label class="_heading_ab" for="ae4">Other entry</label><input id="ae4"></div><div class="_fieldEntry_ab12"><div class="_description_ab">Belongs to the entry below?</div><input id="ae5" aria-label="Fifth"></div>`);
    expect(infoById("react-select-3-input").label.text).toBe("Role What role interests you most?");
    expect(infoById("why").label.text).toBe("Why? Why do you want to work here?");
    // A description already contained in the label, one that is not question-shaped and one past 360 characters are left out.
    expect(infoById("dup").label.text).toBe("Why do you want to work here?");
    expect(infoById("note").label.text).toBe("Note");
    // A hint naming the parts of a place to list is what the field asks, so it is appended.
    expect(infoById("where").label.text).toBe("From where do you intend to work? Please list city and state. (ie: San Francisco, CA)");
    expect(infoById("long").label.text).toBe("Long");
    // Abacus (Greenhouse, live): a hint asking the box to be signed is what it takes; a word that only opens with "sign" is not one.
    expect(infoById("cert").label.text).toBe("I certify that my answers are true. Please sign and date below.");
    // Lyft (Greenhouse, queue164): a hint to enter the name and date as a signature is one too; a name hint alone is not.
    expect(infoById("esig").label.text).toBe(
      "I certify that the facts set forth in this application are true. Please enter your full name and today's date to signify your electronic signature for this statement.",
    );
    expect(infoById("pass").label.text).toBe("Legal name");
    expect(infoById("design").label.text).toBe("Design notes");
    // An Ashby field entry's description block for the same control is appended; an empty block, a block after the control and a block of another entry are not.
    expect(infoById("ae1").label.text).toBe("Why you? Tell us what draws you to this team and role?");
    expect(infoById("ae2").label.text).toBe("Empty block");
    expect(infoById("ae3").label.text).toBe("After the control");
    expect(infoById("ae4").label.text).toBe("Other entry");
    // Anthropic: a description past 360 characters whose last sentence names the Yes that gives the acknowledgement.
    mount(`<div class="field-wrapper"><div class="select"><div class="select__container"><label id="ai-label" for="ai" class="label select__label">AI Policy for Application<span aria-hidden="true">*</span></label>
      <div class="select-shell"><div><div class="select__control"><div class="select__value-container"><div class="select__placeholder" id="react-select-ai-placeholder">Select...</div>
      <div class="select__input-container" data-value=""><input class="select__input" autocomplete="off" id="ai" tabindex="0" type="text" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-labelledby="ai-label" aria-required="true" role="combobox" aria-describedby="react-select-ai-placeholder ai-error" value=""></div></div>
      <div class="select__indicators"><button type="button" aria-label="Toggle flyout" tabindex="-1"></button></div></div></div><input required="" tabindex="-1" aria-hidden="true" value=""></div></div></div>
      <div class="body body__secondary"><div id="ai-description" class="question-description"><p>${"We believe that AI will have a transformative impact on the world. ".repeat(5)}We invite you to review our <a href="#">AI partnership guidelines for candidates</a> and confirm your understanding by selecting “Yes.”</p></div></div></div>`);
    expect(infoById("ai").label.text).toBe("AI Policy for Application We invite you to review our AI partnership guidelines for candidates and confirm your understanding by selecting “Yes.”");
  });

  // Zapier (Ashby, live): a description past 600 characters keeps the closing sentence that names the Yes giving the acknowledgement.
  test("an Ashby description held to 600 characters keeps the sentence that names the Yes", () => {
    mount(`<div class="_fieldEntry_1e3gg_28 ashby-application-form-field-entry" data-field-path="f9"><label class="_heading_f7cvd_52 _required_f7cvd_91 _label_1e3gg_42 ashby-application-form-question-title" for="f9">At Zapier, we don't just embrace automation and AI - our mission is to help others do the same. We expect the smart use of these tools when you join us, so we understand if you used AI to shape your application.</label>` +
      `<div class="_description_1e3gg_48 ashby-application-form-question-description"><p>That said, while AI can be a helpful assistant, your application should still reflect you. You have the opportunity to stand out by being specific, reflective and thoughtful about your own real experiences.</p><p>We value transparency and trust — and that starts from day one. We expect you to complete live interviews on your own. In our skills assessments, AI tools may be encouraged — we'll be interested in how you use them.</p><p>If we find that you have misrepresented yourself at any stage of the process, we'll remove you from consideration for this and future roles at Zapier.</p><p><strong>Please select "Yes" to confirm your understanding and agreement.</strong></p></div>` +
      `<div class="_container_1svni_28 _yesno_1e3gg_148 ashby-application-form-input-yesno"><button class="_option_1svni_32 ashby-application-form-input-yesno-option" aria-pressed="false" data-option="yes">Yes</button><button class="_option_1svni_32 ashby-application-form-input-yesno-option" aria-pressed="false" data-option="no">No</button><input type="checkbox" class="_input_1svni_78" tabindex="-1" name="f9"></div></div>`);
    const ack = infos().find((i) => i.label.text.startsWith("At Zapier"));
    expect(ack?.label.text.endsWith('Please select "Yes" to confirm your understanding and agreement.')).toBe(true);
    expect(ack?.label.text.startsWith("At Zapier, we don't just embrace automation and AI - our mission is to help others do the same. We expect the smart use of these tools when you join us, so we understand if you used AI to shape your application. That said,")).toBe(true);
  });

  test("a description the text does not take travels with the question", () => {
    // Oklo (Greenhouse, live): the options are A, B and C, and what each letter stands for is only in the description.
    const defs = "Please identify yourself as one of the following A, B, or C as described below: ____ A . U.S. Person, which is defined as a U.S. citizen ____ B . Not a U.S. Person, and a citizen of only countries listed in List I ____ C. Not a U.S. Person, and not a citizen of only countries listed in List I";
    mount(`<label for="exp">Export Control Question:</label><select id="exp"><option value="">Select</option><option>A</option><option>B</option><option>C</option></select><div id="exp-description"><p>${defs}</p></div>
      <label for="why">Why?</label><input id="why" aria-describedby="why-desc"><div id="why-desc">Why do you want to work here?</div>
      <label for="plain">Plain</label><input id="plain">`);
    const exp = toQuestion(infoById("exp"));
    expect(exp).toMatchObject({ text: "Export Control Question:", options: ["A", "B", "C"], description: defs });
    // A description the text took, and a control without one, carry none.
    expect(toQuestion(infoById("why"))?.description).toBeUndefined();
    expect(toQuestion(infoById("plain"))?.description).toBeUndefined();
  });
});

describe("Section path", () => {
  test("control inside an Education fieldset carries [\"Education\"] and the label is unchanged", () => {
    mount(`<fieldset><legend>Education</legend><label for="s">School</label><input id="s"></fieldset>
      <section aria-labelledby="h"><h2 id="h">Work Experience</h2><div class="entry"><h4>Entry #2</h4><label for="e">Employer</label><input id="e"></div></section>
      <oc-experience><label for="t">Job title</label><input id="t"></oc-experience>
      <div><label for="plain">Favourite colour</label><input id="plain"></div>
      <fieldset><legend>Pick a pet</legend><label for="pet">Pet</label><input id="pet"></fieldset>
      <div><input id="row" name="$$employerState_1_x" aria-label="State"></div>
      <div><input id="top" name="employer" aria-label="Employer name"></div>
      <section aria-labelledby="long"><h2 id="long">${"Additional information about the position and the company ".repeat(3)}</h2><input id="deep" aria-label="Deep"></section>`);
    expect(infoById("s").section).toEqual(["Education"]);
    expect(infoById("s").label.text).toBe("School");
    expect(infoById("e").section).toEqual(["Work Experience", "Entry #2"]);
    expect(infoById("t").section).toEqual(["Experience"]);
    // Only captions in the section vocabulary count, and a caption past 80 characters does not.
    expect(infoById("plain").section).toEqual([]);
    expect(infoById("pet").section).toEqual([]);
    expect(infoById("deep").section).toEqual([]);
    // With nothing structural, the control's own name says which row it belongs to; a bare employer name does not.
    expect(infoById("row").section).toEqual(["Employment"]);
    expect(infoById("top").section).toEqual([]);
    expect(toQuestion(infoById("s"))?.section).toEqual(["Education"]);
    expect(toQuestion(infoById("plain"))?.section).toBeUndefined();
  });

  test("Greenhouse's education block, titled by a paragraph, is the Education section; a paragraph elsewhere names none", () => {
    // HeyGen (Greenhouse, live): the review read the school's "Start date year" as the applicant's start date.
    mount(`<div class="education--container"><div class="education--form"><hr><div class="education--header"><p class="body body--medium">Education</p></div>
      <label for="start-year--0">Start date year</label><input id="start-year--0"></div></div>
      <div class="questions"><div class="header"><p>Education</p></div><label for="when">Start date year</label><input id="when"></div>`);
    expect(infoById("start-year--0").section).toEqual(["Education"]);
    expect(infoById("when").section).toEqual([]);
  });
});
