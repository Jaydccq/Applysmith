// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Controls the engine never writes or presses; Legal and declaration checkboxes follow the user's setting; Optional marketing opt-ins are never ticked.
import { describe, expect, test } from "vitest";
import { consentKind, consentRefusal } from "../src/consent";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, runFill, summaryOf, write } from "./helpers";

const ANSWER_YES = { outcome: "filled", value: "Yes" } as const;

describe("Consent boxes at the writer", () => {
  test("An optional marketing opt-in is never ticked, even with an affirmative answer and the setting on", async () => {
    mount(`<label><input type="checkbox" id="m"> I agree to receive job alerts and newsletters</label>`);
    expect(await write("m", "Yes", {}, "replace", new Map(), true)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(byId("m").checked).toBe(false);
    expect(await write("m", "Yes")).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
  });

  test("A box the label calls optional stays optional whatever the required flag says", async () => {
    mount(`<label><input type="checkbox" id="m" required> (optional) keep me in the talent network</label>`);
    expect(await write("m", "Yes", {}, "replace", new Map(), true)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(byId("m").checked).toBe(false);
  });

  test("A required terms box is ticked only with the setting on and is a needs-user leftover when off", async () => {
    mount(`<label><input type="checkbox" id="t" required> I have read and accept the terms and conditions</label>`);
    expect(await write("t", "Yes")).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off" });
    expect(byId("t").checked).toBe(false);
    expect((await write("t", "Yes", {}, "replace", new Map(), true)).outcome).toBe("filled");
    expect(byId("t").checked).toBe(true);
  });

  test("An unticked box its form library holds invalid is required: it follows the setting", async () => {
    mount(`<div role="checkbox" id="pp" aria-checked="false" tabindex="0" class="ng-pristine ng-invalid" aria-label="I've read and accept the Employer Privacy Policy"></div>`);
    byId("pp").addEventListener("click", () => {
      byId("pp").setAttribute("aria-checked", "true");
      byId("pp").classList.replace("ng-invalid", "ng-valid");
    });
    expect(await write("pp", "Yes")).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off" });
    expect((await write("pp", "Yes", {}, "replace", new Map(), true)).outcome).toBe("filled");
    expect(byId("pp").getAttribute("aria-checked")).toBe("true");
  });

  test("Required consent to be contacted about future roles follows the setting", async () => {
    mount(`<label><input type="checkbox" id="c" required> I allow Example Corp to contact me about future opportunities</label>`);
    expect(await write("c", "Yes")).toMatchObject({ reason: "legal-declaration-off" });
    expect((await write("c", "Yes", {}, "replace", new Map(), true)).outcome).toBe("filled");
  });

  test("A Yes in a choice group answering a marketing question is refused as well", async () => {
    mount(`<fieldset><legend>Would you like to receive our newsletter?</legend><label><input type="radio" name="n" id="y"> Yes</label><label><input type="radio" name="n" id="no"> No</label></fieldset>`);
    expect(await write("y", "Yes", {}, "replace", new Map(), true)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(byId("y").checked).toBe(false);
  });

  test("A marketing switch is judged on the answer the writer will use, not on a blank value beside an option label", async () => {
    mount(`<label for="sw">Send me job alerts</label><button type="button" role="switch" id="sw" aria-checked="false"></button>`);
    byId("sw").addEventListener("click", () => byId("sw").setAttribute("aria-checked", "true"));
    const r = await write("sw", "", { optionLabel: "Yes" }, "replace", new Map(), true);
    expect(r).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(byId("sw").getAttribute("aria-checked")).toBe("false");
  });

  test("Declining is never blocked", async () => {
    mount(`<label><input type="checkbox" id="m" checked> Send me job alerts</label>`);
    expect((await write("m", "No")).outcome).toBe("filled");
    expect(byId("m").checked).toBe(false);
  });

  test("Words that merely contain terms or policy are not acknowledgements", () => {
    expect(consentKind("Do you hold a policyholder licence?")).toBeNull();
    expect(consentKind("Did you pass your midterms")).toBeNull();
    expect(consentKind("I understand the privacy policy")).toBe("declaration");
    expect(consentKind("I confirm I am legally authorized to work in the country")).toBeNull();
    expect(consentKind("I certify that false answers could disqualify me from future opportunities")).toBe("declaration");
    expect(consentRefusal({ text: "Policyholder", markedRequired: true, optional: false }, false)).toBeNull();
  });

  test("A statement that the information given is true is a declaration, without a certifying verb (evlo)", () => {
    expect(consentKind("The information I have provided here and in my resume is complete and accurate. I understand that evlo.ai takes no responsibility for the accuracy or completeness of the application it submits to partnered Employers/Recruiters on my behalf.")).toBe("declaration");
    expect(consentKind("All statements I have made in this application are true and correct")).toBe("declaration");
    expect(consentKind("Is the information on your resume up to date?")).toBeNull();
    expect(consentKind("Provide accurate information about your employment history")).toBeNull();
  });

  test("A required box labelled with a lookalike word is a plain control, not a consent box", async () => {
    mount(`<label><input type="checkbox" id="p" required> I am a policyholder</label>`);
    expect((await write("p", "Yes")).outcome).toBe("filled");
  });
});

describe("An answer the user wrote into their own bank", () => {
  // Spec (apply-safety-gates): the opt-in rule's "only exception is an answer the user wrote into their own answer bank
  // for that exact question, which applies as written", and the legal-declaration setting "SHALL NOT govern ... an answer
  // the user wrote into their own answer bank for the question, which applies as written".
  const DATA = `<label><input type="checkbox" id="d"> I consent to Example Corp retaining my data for 24 months</label>`;

  test("ticks an unmarked declaration box, while the same value from any other source is refused", async () => {
    mount(DATA);
    expect(await write("d", "Yes", {}, "replace", new Map(), true)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(byId("d").checked).toBe(false);
    expect((await write("d", "Yes", { ownAnswer: true }, "replace", new Map(), false)).outcome).toBe("filled");
    expect(byId("d").checked).toBe(true);
  });

  test("ticks a marketing opt-in as written", async () => {
    mount(`<label><input type="checkbox" id="m"> Send me job alerts</label>`);
    expect((await write("m", "Yes", { ownAnswer: true })).outcome).toBe("filled");
    expect(byId("m").checked).toBe(true);
  });

  test("applies through a run on an unmarked consent group, and a model-composed answer is still refused", async () => {
    const agree = "I consent to Example Corp retaining my data for 24 months";
    const html = `<fieldset><legend>Data retention</legend><label><input type="radio" name="r" id="y"> ${agree}</label><label><input type="radio" name="r" id="n"> I do not consent</label></fieldset>${PAD}`;
    mount(html);
    await runFill(makeEngine(fakeHost({ "data retention": { outcome: "filled", value: agree, ownAnswer: true } })), { applicant: ADA });
    expect(byId("y").checked).toBe(true);
    mount(html);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "data retention": { outcome: "filled-model", value: agree, ownAnswer: true } })), { applicant: ADA, acceptLegalDeclarations: true }));
    expect(byId("y").checked).toBe(false);
    expect(s.rows.find((r) => r.kind === "radio-group")).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
  });
});

describe("Consent boxes in a run", () => {
  test("The sweep leaves an unmarked consent box unticked with the setting on and ticks a marked one", async () => {
    mount(`<label><input type="checkbox" id="d"> I consent to Example Corp retaining my data for 24 months</label>
      <label><input type="checkbox" id="t" required> I agree to the privacy policy</label>${PAD}`);
    await runFill(makeEngine(fakeHost({})), { applicant: ADA, acceptLegalDeclarations: true });
    expect(byId("d").checked).toBe(false);
    expect(byId("t").checked).toBe(true);
  });

  test("A page-required marketing box follows the setting and an unmarked one is left alone", async () => {
    mount(`<fieldset><legend>Stay in touch</legend><input type="checkbox" id="r" required><label for="r">Email me about future roles</label></fieldset>${PAD}`);
    const host = fakeHost({ "stay in touch": ANSWER_YES });
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA }));
    expect(byId("r").checked).toBe(false);
    expect(s.rows.find((r) => r.kind === "checkbox")).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off" });
    expect(s.counts.requiredUnfilled).toBe(1);
  });

  test("A matcher answer cannot tick a marketing box and a required terms box follows the setting", async () => {
    const html = `<fieldset><legend>Alerts</legend><input type="checkbox" id="m"><label for="m">I agree to receive job alerts</label></fieldset>
      <fieldset><legend>Privacy</legend><input type="checkbox" id="t" required><label for="t">I agree to the privacy policy</label></fieldset>${PAD}`;
    for (const accept of [false, true]) {
      mount(html);
      const host = fakeHost({ alerts: ANSWER_YES, privacy: ANSWER_YES });
      const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA, acceptLegalDeclarations: accept }));
      expect(byId("m").checked).toBe(false);
      expect(byId("t").checked).toBe(accept);
      const [alerts, policy] = s.rows.filter((r) => r.kind === "checkbox");
      expect(alerts?.outcome).toBe("skipped");
      expect(host.matchRequests.flatMap((r) => r.questions.map((q) => q.text))).not.toContain("Alerts");
      if (accept) expect(policy).toMatchObject({ outcome: "filled" });
      else expect(policy).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off" });
    }
  });
});

describe("Consent carried by the option label", () => {
  const TERMS_RADIO = `<fieldset><legend>Terms of employment</legend><label><input type="radio" name="t" id="acc" required> I accept the terms and conditions</label><label><input type="radio" name="t" id="rej"> I do not accept</label></fieldset>`;

  test("A radio option that assents to terms follows the setting", async () => {
    mount(TERMS_RADIO);
    expect(await write("acc", "I accept the terms and conditions")).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off" });
    expect(byId("acc").checked).toBe(false);
    expect((await write("acc", "I accept the terms and conditions", {}, "replace", new Map(), true)).outcome).toBe("filled");
    expect(byId("acc").checked).toBe(true);
  });

  test("The same group the page does not mark required is never assented to, with the setting on", async () => {
    mount(TERMS_RADIO.replace(" required", ""));
    expect(await write("acc", "I accept the terms and conditions", {}, "replace", new Map(), true)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(byId("acc").checked).toBe(false);
  });

  test("A negative option is still allowed", async () => {
    mount(TERMS_RADIO);
    expect((await write("rej", "I do not accept")).outcome).toBe("filled");
    expect(byId("rej").checked).toBe(true);
  });

  test("A multi-checkbox option that opts in to marketing is never ticked", async () => {
    mount(`<fieldset><legend>Contact preferences</legend><label><input type="checkbox" name="c" id="sms"> Send me job alerts by SMS</label><label><input type="checkbox" name="c" id="mail"> Email newsletter</label></fieldset>`);
    const desired = { optionLabels: ["Send me job alerts by SMS"] };
    expect(await write("sms", "Send me job alerts by SMS", desired, "replace", new Map(), true)).toMatchObject({ outcome: "skipped", reason: "marketing-opt-in" });
    expect(byId("sms").checked).toBe(false);
  });

  test("The same opt-in option in a group the page marks required follows the setting", async () => {
    mount(`<fieldset aria-required="true"><legend>Contact preferences *</legend><label><input type="radio" name="c" id="sms"> Send me job alerts by SMS</label><label><input type="radio" name="c" id="no"> No thanks</label></fieldset>`);
    expect(await write("sms", "Send me job alerts by SMS")).toMatchObject({ reason: "legal-declaration-off" });
    expect((await write("sms", "Send me job alerts by SMS", {}, "replace", new Map(), true)).outcome).toBe("filled");
  });

  test("A select option that assents to terms follows the setting", async () => {
    mount(`<label for="s">Terms</label><select id="s" required><option value="">Select</option><option>I accept the terms and conditions</option><option>I do not accept</option></select>`);
    expect(await write("s", "I accept the terms and conditions")).toMatchObject({ reason: "legal-declaration-off" });
    expect((await write("s", "I do not accept")).outcome).toBe("filled");
  });

  test("A group that is not about consent is unaffected", async () => {
    mount(`<fieldset><legend>How did you hear about us?</legend><label><input type="radio" name="h" id="nl"> Email newsletter</label><label><input type="radio" name="h" id="fr"> A friend</label></fieldset>`);
    expect((await write("nl", "Email newsletter")).outcome).toBe("filled");
    expect(byId("nl").checked).toBe(true);
  });
});

describe("Deferred answers keep the run's legal-declarations setting", () => {
  const BOX = `<label><input type="checkbox" id="t" required> I have read and accept the terms and conditions</label>`;

  test("answer() ticks a required terms box when the run started with the setting on, and not otherwise", async () => {
    for (const accept of [false, true]) {
      mount(BOX + PAD);
      const engine = makeEngine(fakeHost());
      await runFill(engine, { applicant: ADA, acceptLegalDeclarations: accept });
      byId("t").checked = false;
      const reply = await engine.answer([{ selector: "#t", outcome: "filled", value: "Yes" }]);
      if (accept) expect(reply.results[0]).toMatchObject({ outcome: "filled" });
      else expect(reply.results[0]).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off" });
      expect(byId("t").checked).toBe(accept);
    }
  });
});

describe("Declaration wording", () => {
  test("A question that only mentions conditions or policy is not a declaration", () => {
    expect(consentKind("Can you work under these conditions?")).toBeNull();
    expect(consentKind("Are you comfortable with our hybrid policy?")).toBeNull();
    expect(consentKind("Do you have experience with privacy engineering?")).toBeNull();
  });

  test("Acknowledgement phrasing is still a declaration", () => {
    expect(consentKind("I agree to the terms and conditions")).toBe("declaration");
    expect(consentKind("By submitting you accept our privacy policy")).toBe("declaration");
    expect(consentKind("Please acknowledge the candidate privacy notice")).toBe("declaration");
    expect(consentKind("Please be informed that your application to this job offer will trigger some processing of your personal data by the recruiting company. For more information, please refer to the recruiting company's privacy notice.")).toBe("declaration");
    expect(consentKind("Terms & Conditions")).toBe("declaration");
    expect(consentKind("Privacy Policy")).toBe("declaration");
    expect(consentKind("I certify the information is true")).toBe("declaration");
  });
});

describe("A statement that the application is the applicant's own work", () => {
  // Only the user's own bank answer settles one, so no setting ticks it; a truth certification still follows the setting.
  const rows: [string, string | null, string | null][] = [
    ["Can you confirm that everything in this application is true and your own, including your experience and identity?", "context-policy", "context-policy"],
    ["I confirm that everything in this application is true and my own", "context-policy", "context-policy"],
    ["I certify that this application is my own work and was not generated by AI", "context-policy", "context-policy"],
    ["I confirm that my answers were written by me", "context-policy", "context-policy"],
    ["I confirm that I completed this application without the use of AI", "context-policy", "context-policy"],
    ["My responses are not AI-generated", "context-policy", "context-policy"],
    ["Is everything in this application your own? Yes", "context-policy", "context-policy"],
    ["I certify that the information I provided is true and complete", "legal-declaration-off", null],
    ["I confirm my information is accurate and my own contact details are current", "legal-declaration-off", null],
    ["Is the vehicle you will use for deliveries your own? Yes", null, null],
    ["Please answer in your own words", null, null],
  ];
  test.each(rows)("%s", (text, off, on) => {
    const box = { text, markedRequired: true, optional: false };
    expect(consentRefusal(box, false)).toBe(off);
    expect(consentRefusal(box, true)).toBe(on);
  });

  test("The sweep leaves it unticked with the setting on and ticks the certification beside it", async () => {
    mount(`<label><input type="checkbox" id="own" required> I confirm that everything in this application is true and my own</label>
      <label><input type="checkbox" id="t" required> I certify that the information I provided is true and complete</label>${PAD}`);
    await runFill(makeEngine(fakeHost({})), { applicant: ADA, acceptLegalDeclarations: true });
    expect(byId("own").checked).toBe(false);
    expect(byId("t").checked).toBe(true);
  });
});

describe("Automatic acknowledgement sweep preserves the option's polarity", () => {
  // Every wording on a native box; the two role shapes only change where the label is read from, so one wording each.
  const refusals = ["I don&apos;t agree", "I don’t agree", "I donʼt agree", "I don＇t agree", "I don\u200b't agree", "I don\ufeff't agree", "Never confirm", "I refuse to accept", "We don't certify", "I have never read the policy", "I will not readily and fully agree", "I dont agree", "I doesnt agree", "I wont accept", "dont agree", "I havent agreed"];
  for (const label of refusals) {
    for (const shape of label === refusals[1] ? ["native", "role", "associated role"] : ["native"]) {
      const role = shape !== "native";
      test(`${shape} refusal ${label} stays unticked under an affirmative legend`, async () => {
        const box = role ? `<button type="button" id="negative" role="checkbox" aria-checked="false" aria-required="true">${shape === "role" ? label : ""}</button>` : `<input id="negative" type="checkbox" required>`;
        const associated = shape === "role" ? "" : `<label for="negative">${label}</label>`;
        mount(`<fieldset><legend>I acknowledge the recruitment privacy notice *</legend>${associated}${box}</fieldset><label for="fn">First name</label><input id="fn">${PAD}`);
        if (role) byId("negative").addEventListener("click", () => byId("negative").setAttribute("aria-checked", "true"));
        await runFill(makeEngine(fakeHost({})), { applicant: ADA, acceptLegalDeclarations: true });
        if (role) expect(byId("negative").getAttribute("aria-checked")).toBe("false");
        else expect(byId("negative").checked).toBe(false);
      });
    }
  }

  const notice = "Please be informed that your application to this job offer will trigger some processing of your personal data by the recruiting company, the data controller. SmartRecruiters, the data processor, has no control over such personal data processing. For more information on these personal data processing, please refer to the recruiting company’s privacy notice.";
  const affirmatives = ["I agree not to use AI tools", "I certify that I will not accept outside assistance", "Yes, I have no objections to the privacy acknowledgement", "I will not readily and fully ever agree to the privacy policy", notice];
  for (const label of affirmatives) {
    for (const shape of label === affirmatives[0] ? ["native", "role", "associated role"] : ["native"]) {
      const role = shape !== "native";
      test(`${shape} affirmative scope ${label} still ticks`, async () => {
        const box = role ? `<button type="button" id="positive" role="checkbox" aria-checked="false" aria-required="true">${shape === "role" ? label : ""}</button>` : `<input id="positive" type="checkbox" required>`;
        const associated = shape === "role" ? "" : `<label for="positive">${label}</label>`;
        mount(`<fieldset><legend>I acknowledge the recruitment privacy notice *</legend>${associated}${box}</fieldset><label for="fn">First name</label><input id="fn">${PAD}`);
        if (role) byId("positive").addEventListener("click", () => byId("positive").setAttribute("aria-checked", "true"));
        await runFill(makeEngine(fakeHost({})), { applicant: ADA, acceptLegalDeclarations: true });
        if (role) expect(byId("positive").getAttribute("aria-checked")).toBe("true");
        else expect(byId("positive").checked).toBe(true);
      });
    }
  }
});
