// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Checkbox groups, consent boxes and single-answer choice groups; Question pass (button choices);
// Controls the engine never writes or presses.
import { describe, expect, test } from "vitest";
import { inspectPage } from "../src/summary";
import { toggleOn } from "../src/toggles";
import { PAD as PAD1, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, write } from "./helpers";

/** Two unrelated fields, so a page of buttons and one more field still counts as an application form. */
const PAD = `${PAD1}<label for="pad2">Nickname</label><input id="pad2">`;

/** The row of a control, found by its selector, since a lone box may carry no question text. */
const rowOf = (s: { rows: { selector: string }[] }, id: string) => {
  const row = s.rows.find((r) => r.selector.includes(`"${id}"`));
  if (!row) throw new Error(`no row for #${id}`);
  return row as ReturnType<typeof rowFor>;
};

/** Two buttons holding their state in aria-pressed. */
function pressedPair(question = "Are you 18 or older?") {
  mount(`<div role="group" aria-labelledby="q">${`<span id="q">${question}</span>`}<button type="button" id="yes" aria-pressed="false">Yes</button><button type="button" id="no" aria-pressed="false">No</button></div>${PAD}`);
  for (const id of ["yes", "no"]) {
    byId(id).addEventListener("click", () => {
      for (const other of ["yes", "no"]) byId(other).setAttribute("aria-pressed", String(other === id));
    });
  }
}

describe("Button choices", () => {
  test("Yes/No buttons: a form shows Are you 18 or older with two buttons whose aria-pressed toggles; one radio-group question with Yes and No is sent and the matching button is activated", async () => {
    pressedPair();
    const host = fakeHost({ "are you 18 or older?": { outcome: "filled", value: "Yes" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    const qs = host.matchRequests.flatMap((r) => r.questions).filter((q) => q.kind === "radio-group");
    expect(qs).toHaveLength(1);
    expect(qs[0]).toMatchObject({ kind: "radio-group", options: ["Yes", "No"] });
    expect(byId("yes").getAttribute("aria-pressed")).toBe("true");
    expect(byId("no").getAttribute("aria-pressed")).toBe("false");
    expect(rowFor(s, "18 or older").outcome).toBe("filled");
  });

  // Color (Ashby): the review doubted the run's Yes, and the fix's No came back "existing-value": the run's own choice
  // read as the page's selection.
  test("A choice this run made is replaced only when the host asks to overwrite it", async () => {
    pressedPair("Do you meet this requirement?");
    const engine = makeEngine(fakeHost({ "do you meet this requirement?": { outcome: "filled", value: "Yes" } }));
    const selector = rowFor(summaryOf(await runFill(engine)), "meet this requirement").selector;
    expect(byId("yes").getAttribute("aria-pressed")).toBe("true");
    const kept = await engine.answer([{ selector, outcome: "filled", value: "No" }]);
    expect(kept.results.map((r) => [r.outcome, r.reason])).toEqual([["prefilled", "existing-value"]]);
    expect(byId("yes").getAttribute("aria-pressed")).toBe("true");
    const again = await engine.answer([{ selector, outcome: "filled", value: "No", overwrite: true }]);
    expect(again.results.map((r) => r.outcome)).toEqual(["filled"]);
    expect(byId("no").getAttribute("aria-pressed")).toBe("true");
    expect(byId("yes").getAttribute("aria-pressed")).toBe("false");
  });

  test("Yes and No drawn as the two ARIA checkboxes of one group (Rippling) are one Yes/No question under its title, and only the answer is ticked", async () => {
    const box = (v: string) =>
      `<div id="${v.toLowerCase()}" data-value="${v}" role="checkbox" tabindex="0" aria-checked="false"><div><svg></svg><input aria-labelledby="l-${v}" type="checkbox" value="${v}" name="customQuestions.q1"></div><div id="l-${v}"><p>${v}</p></div></div>`;
    mount(`<div><div><p>Have you ever been employed with Example Insurance?</p></div><div data-testid="field"><div id="field-105" role="group">${box("Yes")}${box("No")}</div></div></div>${PAD}`);
    for (const id of ["yes", "no"]) byId(id).addEventListener("click", () => byId(id).setAttribute("aria-checked", String(byId(id).getAttribute("aria-checked") !== "true")));
    const host = fakeHost({ "have you ever been employed with example insurance?": { outcome: "filled", value: "No" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    const qs = host.matchRequests.flatMap((r) => r.questions).filter((q) => /employed with/.test(q.text));
    expect(qs).toHaveLength(1);
    expect(qs[0]).toMatchObject({ kind: "radio-group", options: ["Yes", "No"] });
    expect(byId("no").getAttribute("aria-checked")).toBe("true");
    expect(byId("yes").getAttribute("aria-checked")).toBe("false");
    expect(rowFor(s, "employed with").outcome).toBe("filled");
  });

  test("Pill buttons with hidden proxy: two type=button siblings beside a hidden tabindex=-1 input make one radio-group question and the proxy is not a text question", async () => {
    mount(`<div><span id="q">Open to relocation?</span><button type="button" id="a">Open</button><button type="button" id="b">Not open</button><input id="proxy" tabindex="-1" style="opacity:0;position:absolute"></div>${PAD}`);
    const host = fakeHost({ "open to relocation?": { outcome: "filled", value: "Open" } });
    await runFill(makeEngine(host));
    const qs = host.matchRequests.flatMap((r) => r.questions);
    expect(qs.filter((q) => q.kind === "radio-group")).toHaveLength(1);
    expect(qs.find((q) => q.kind === "radio-group")?.options).toEqual(["Open", "Not open"]);
    expect(qs.some((q) => q.kind === "text" && q.selector.includes("proxy"))).toBe(false);
  });

  test("State is read from aria-pressed, a CSS-module active, selected or chosen class token, or a hidden backing checkbox that vouches for Yes only", () => {
    mount(`<button id="a" aria-pressed="true">Yes</button><button id="b" class="_active_1x2y3z">Yes</button><button id="c" class="btn _chosen_9f8e">No</button><button id="d" class="btn">No</button>
      <div><button id="e" type="button">Yes<input type="checkbox" checked hidden></button></div><div><button id="f" type="button">No<input type="checkbox" checked hidden></button></div>`);
    const on = (id: string) => toggleOn(byId(id));
    expect([on("a"), on("b"), on("c"), on("d"), on("e"), on("f")]).toEqual([true, true, true, false, true, false]);
  });

  test("A selected button is never clicked again and a different answered choice is not undone", async () => {
    pressedPair();
    byId("yes").setAttribute("aria-pressed", "true");
    let clicks = 0;
    byId("yes").addEventListener("click", () => clicks++);
    const same = await write("yes", "Yes");
    expect(same.outcome).toBe("prefilled");
    expect(clicks).toBe(0);
    const other = await write("yes", "No");
    expect(other).toMatchObject({ outcome: "prefilled", reason: "existing-value" });
    expect(byId("yes").getAttribute("aria-pressed")).toBe("true");
  });

  test("A button that never takes its state is failed, never counted as filled", async () => {
    pressedPair();
    for (const id of ["yes", "no"]) byId(id).replaceWith(byId(id).cloneNode(true));
    const r = await write("yes", "Yes");
    expect(r.outcome).toBe("failed");
  });

  test("A role=radio group is one question and the chosen radio's aria-checked is verified", async () => {
    mount(`<div role="radiogroup" aria-label="Preferred contact"><div role="radio" id="r1" aria-checked="false" tabindex="0">Email</div><div role="radio" id="r2" aria-checked="false" tabindex="0">Phone</div></div>${PAD}`);
    for (const id of ["r1", "r2"]) {
      byId(id).addEventListener("click", () => ["r1", "r2"].forEach((o) => byId(o).setAttribute("aria-checked", String(o === id))));
    }
    const r = await write("r1", "Phone");
    expect(r.outcome).toBe("filled");
    expect(byId("r2").getAttribute("aria-checked")).toBe("true");
    expect(byId("r1").getAttribute("aria-checked")).toBe("false");
  });
});

describe("Checkbox groups, consent boxes and single-answer choice groups", () => {
  const group = `<fieldset><legend>Preferred language</legend><label><input type="checkbox" id="l1" name="l" value="en"> English</label><label><input type="checkbox" id="l2" name="l" value="fr"> French</label></fieldset>`;

  test("Plural label payload: a single-answer group refuses two labels unless the question is multi-select or the host marked the binding", async () => {
    mount(`<fieldset><legend>Preferred contact</legend><label><input type="radio" id="c1" name="c" value="e"> Email</label><label><input type="radio" id="c2" name="c" value="p"> Phone</label></fieldset>${PAD}`);
    const r = await write("c1", "Email", { optionLabels: ["Email", "Phone"] });
    expect(r).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
    expect(document.querySelectorAll("input:checked")).toHaveLength(0);
  });

  test("A host-marked multi-select binding ticks each label; unknown labels are skipped and ticked boxes stay on", async () => {
    mount(`${group}${PAD}`);
    (document.querySelector("input[value=en]") as HTMLInputElement).click();
    const r = await write("l1", "French", { optionLabels: ["French", "Klingon"], multiSelectBinding: true });
    expect(r.outcome).toBe("filled");
    expect(Array.from(document.querySelectorAll<HTMLInputElement>("input:checked")).map((i) => i.value)).toEqual(["en", "fr"]);
  });

  test("Nameless inputs group by their question wrapper, and only nameless inputs are each other's group-mates", async () => {
    mount(`<div class="q"><span>Do you need sponsorship?</span><label><input type="checkbox"> Yes</label><label><input type="checkbox"> No</label></div><div class="q"><span>Another question</span><label><input type="checkbox" name="named" value="1"> Yes</label><label><input type="checkbox"> No</label></div>${PAD}`);
    const host = fakeHost({ "do you need sponsorship?": { outcome: "filled", value: "Yes" } });
    await runFill(makeEngine(host));
    const boxes = Array.from(document.querySelectorAll<HTMLInputElement>("input[type=checkbox]"));
    expect(boxes.map((b) => b.checked)).toEqual([true, false, false, false]);
  });

  test("A single-answer group the page locks by disabling every box but the ticked one (Workday's disability form) is still one question, read at its own selector", () => {
    const options = ["Yes, I have a disability", "No, I do not have a disability", "I do not want to answer"];
    mount(`<fieldset><legend>Please check one of the boxes below:<abbr>*</abbr></legend><fieldset id="grp" aria-required="true">${options
      .map((o, i) => `<div><input type="checkbox" id="d${i}" aria-required="true"><label for="d${i}">${o}</label></div>`)
      .join("")}</fieldset></fieldset>${PAD}`);
    const before = inspectPage(document).find((r) => r.label.startsWith("Please check one"));
    byId("d1").click();
    for (const id of ["d0", "d2"]) byId(id).setAttribute("disabled", "");
    const after = inspectPage(document).find((r) => r.selector === before?.selector);
    expect(after).toMatchObject({ label: before?.label, value: options[1], options: before?.options });
  });

  test("Nameless boxes that share only a section's fieldset, among its text boxes and selects (HPE), are each their own box: the required notice is ticked and the optional opt-ins are not", async () => {
    mount(`<fieldset><legend>You are applying for</legend><label for="fn">First name</label><input id="fn"><label for="src">Source</label><select id="src"><option>Job board</option></select>
      <div><label><input type="checkbox" id="ai"><span>I consent to the use of AI-enabled tools in recruitment.</span></label></div>
      <div><label><input type="checkbox" id="sms"><span>I consent to receive SMS messages about my application.</span></label></div>
      <fieldset><legend>Additional Fields</legend><label><input type="checkbox" id="notice" required><span>I agree to the processing of my personal data as described in the Recruitment Notice. Should your resume hold sensitive personal data, you consent to its storing and processing.</span></label></fieldset></fieldset>${PAD}`);
    expect(inspectPage(document).filter((r) => r.kind === "checkbox")).toHaveLength(3);
    await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true });
    expect(["ai", "sms", "notice"].map((id) => byId(id).checked)).toEqual([false, false, true]);
  });

  test("Nameless boxes in a fieldset around a whole step of text boxes and no select (Qualtrics) are each their own box: the required privacy box is ticked and the opt-ins are not", async () => {
    const optIn = (id: string, text: string) =>
      `<div class="checkbox"><label><input type="checkbox" aria-describedby="${id}" id="${id}" aria-required="false" value="false"><span>${text}</span></label></div>`;
    mount(`<fieldset aria-labelledby="t"><legend id="t">Apply</legend>
      <div><label for="first">First Name*</label><input id="first" required></div><div><label for="mail">Email*</label><input id="mail" required></div>
      ${optIn("emailAgreement", "I agree that Qualtrics may send me communications related to jobs, company updates, events, and other related news.")}
      ${optIn("smsOptIn", "Yes, I would like to receive communications from Qualtrics via SMS.")}
      <div class="checkbox"><label><input type="checkbox" aria-labelledby="privacyCheckbox-label" id="privacyCheckbox" required aria-required="true" value=""><span class="checkmark">*</span><span id="privacyCheckbox-label">By submitting my application, I agree that Qualtrics may process my data and information in accordance with the <a href="#p">Qualtrics Candidate Privacy Policy</a>.</span></label></div></fieldset>${PAD}`);
    // Live, the three were one unlabelled group the matcher abstained on, and Continue kept returning the step.
    expect(inspectPage(document).filter((r) => r.kind === "checkbox")).toHaveLength(3);
    await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true });
    expect(["emailAgreement", "smsOptIn", "privacyCheckbox"].map((id) => byId(id).checked)).toEqual([false, false, true]);
  });

  test("A question's fieldset with one Other box beside its nameless options stays one question", () => {
    mount(`<fieldset><legend>Which languages do you use? (select all that apply)</legend>${["Go", "Rust", "Other"]
      .map((o, i) => `<label><input type="checkbox" id="l${i}"> ${o}</label>`)
      .join("")}<input id="other" aria-label="Other language"></fieldset>${PAD}`);
    expect(inspectPage(document).filter((r) => r.kind === "checkbox")).toHaveLength(1);
  });
});

describe("Consent sweep", () => {
  const TERMS = `<label><input type="checkbox" id="terms" required> I agree to the terms and conditions</label>`;

  test("A required acknowledgement is ticked only with the setting on, reported as the run's own attestation with the authored sentence, and tapped", async () => {
    mount(`${PAD}${TERMS}`);
    const touches: string[] = [];
    for (const t of ["focus", "blur"]) byId("terms").addEventListener(t, () => touches.push(t));
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true }));
    expect(byId("terms").checked).toBe(true);
    const row = rowOf(s, "terms");
    expect(row).toMatchObject({ lane: "attestation", outcome: "filled" });
    expect(row.optionLabel).toContain("I agree to the terms and conditions");
    expect(touches).toEqual(["focus", "blur"]);
  });

  test("With the setting off nothing is ticked and the box is a needs-user leftover", async () => {
    mount(`${PAD}${TERMS}`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(byId("terms").checked).toBe(false);
    expect(s.rows.find((r) => r.selector.includes("terms"))).toMatchObject({ outcome: "skipped", reason: "legal-declaration-off", required: true });
  });

  test("A box found already ticked is reported as inherited, also with the setting off, not as the run's own attestation", async () => {
    mount(`${PAD}${TERMS}`);
    byId("terms").checked = true;
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    const row = rowOf(s, "terms");
    expect(row.outcome).toBe("prefilled");
    expect(row.lane).not.toBe("attestation");
    expect(row.optionLabel).toContain("I agree to the terms");
  });

  test("Marketing wording on a box the page marks required follows the setting", async () => {
    for (const accept of [false, true]) {
      mount(`${PAD}<label><input type="checkbox" id="mk" required> Keep me informed about future roles</label>`);
      await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: accept });
      expect(byId("mk").checked).toBe(accept);
    }
  });

  describe("Optional marketing opt-in worded around the document", () => {
    const SLOT = `<label for="up">Resume</label><input type="file" id="up">`;
    const box = (required: boolean) => `<label><input type="checkbox" id="cv"${required ? " required" : ""}> Send me job alerts; my resume is attached</label>`;

    test("A box the page does not mark required stays unticked with a document present and the setting on or off", async () => {
      for (const accept of [true, false]) {
        mount(`${PAD}${SLOT}${box(false)}`);
        await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: accept });
        expect(byId("cv").checked).toBe(false);
      }
    });

  });

  test("A document-conditional acknowledgement is ticked only when the run has a document", async () => {
    mount(`${PAD}<label><input type="checkbox" id="cv" required> My resume is attached and I have one</label>`);
    const withDoc = makeEngine(fakeHost());
    await runFill(withDoc, { acceptLegalDeclarations: true });
    expect(byId("cv").checked).toBe(false);
    mount(`${PAD}<label for="up">Resume</label><input type="file" id="up"><label><input type="checkbox" id="cv" required> My resume is attached and I have one</label>`);
    await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true });
    expect(byId("cv").checked).toBe(true);
  });

  test("The sweep pierces shadow roots", async () => {
    mount(`${PAD}<div id="host"></div>`);
    const root = byId("host").attachShadow({ mode: "open" });
    root.innerHTML = `<label><input type="checkbox" id="sh" required> I certify the information is true</label>`;
    await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true });
    expect((root.getElementById("sh") as HTMLInputElement).checked).toBe(true);
  });

  test("Radix boxes are driven through their role button and read from aria-checked", async () => {
    mount(`${PAD}<label for="rb">I accept the privacy policy</label><button type="button" role="checkbox" id="rb" aria-required="true" aria-checked="false"></button><input type="checkbox" aria-hidden="true" tabindex="-1" style="opacity:0">`);
    byId("rb").addEventListener("click", () => byId("rb").setAttribute("aria-checked", String(byId("rb").getAttribute("aria-checked") !== "true")));
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true }));
    expect(byId("rb").getAttribute("aria-checked")).toBe("true");
    expect(rowOf(s, "rb").lane).toBe("attestation");
  });
});

