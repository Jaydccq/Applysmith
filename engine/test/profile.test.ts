// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Profile-field pass.
import { describe, expect, test, vi } from "vitest";
import { ADA, byId, fakeHost, makeEngine, mount, PAD, rowFor, runFill, setUrl, summaryOf } from "./helpers";

const fill = async (applicant = ADA) => summaryOf(await runFill(makeEngine(fakeHost()), { applicant }));

describe("Profile-field pass", () => {
  test("Honeypot ignored: an off-screen text input labelled 'Leave this field empty' stays empty", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><div style="position:absolute;left:-9999px"><label for="h">Leave this field empty</label><input id="h" name="website"></div><label for="ln">Last name</label><input id="ln">`);
    await fill();
    expect(byId("h").value).toBe("");
    expect(byId("fn").value).toBe("Ada");
  });

  test("Off-screen and aria-hidden controls with no label are skipped", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><input id="x" name="email" aria-hidden="true" tabindex="-1" style="opacity:0"><input id="y" name="phone" style="position:absolute;left:-5000px"><label for="em">Email</label><input id="em">`);
    await fill();
    expect([byId("x").value, byId("y").value]).toEqual(["", ""]);
    expect(byId("em").value).toBe("ada@example.com");
  });

  test("Full name versus parts: the parts are filled from the name parts and no control receives the full name", async () => {
    mount(`<label for="f">Legal first name</label><input id="f"><label for="l">Legal last name</label><input id="l"><label for="n">Full name</label><input id="n">`);
    await fill();
    expect([byId("f").value, byId("l").value, byId("n").value]).toEqual(["Ada", "Example", ""]);
  });

  // Anduril (Greenhouse, live): the name, then the legal name in questions of their own.
  test("A legal first and last name asked after the name take the applicant's name again; another person's first name stays empty", async () => {
    mount(`<label for="f">First Name</label><input id="f"><label for="l">Last Name</label><input id="l"><label for="lf">Legal First Name</label><input id="lf"><label for="ll">Legal Last Name</label><input id="ll"><label for="e">Emergency contact first name</label><input id="e">`);
    await fill();
    expect([byId("f").value, byId("l").value, byId("lf").value, byId("ll").value, byId("e").value]).toEqual(["Ada", "Example", "Ada", "Example", ""]);
  });

  // DRW (Greenhouse, live): the legal name asked in questions of their own drawn as multi-line boxes.
  test("A legal first and last name in multi-line boxes take the applicant's name; another person's name in one stays empty", async () => {
    mount(`<label for="f">First Name</label><input id="f"><label for="lf">Legal First Name</label><textarea id="lf"></textarea><label for="ll">Legal Last Name</label><textarea id="ll"></textarea><label for="e">Emergency contact first name</label><textarea id="e"></textarea>`);
    await fill();
    expect([byId("lf").value, byId("ll").value, byId("e").value]).toEqual(["Ada", "Example", ""]);
  });

  // Mattermost (Greenhouse, live): the whole legal name asked after the parts; a bare "Full name" beside them still takes nothing.
  test("The legal name asked as a question after the name parts takes the full name again", async () => {
    mount(`<label for="f">First Name</label><input id="f"><label for="l">Last Name</label><input id="l"><label for="ln">What is your legal name?</label><input id="ln">`);
    await fill();
    expect([byId("f").value, byId("l").value, byId("ln").value]).toEqual(["Ada", "Example", "Ada Example"]);
  });

  // Khan Academy (Greenhouse, live): "Legal Name (First and Last)" after the name parts is the whole legal name again.
  test("'Legal Name (First and Last)' after the name parts takes the full name", async () => {
    mount(`<label for="f">First Name</label><input id="f"><label for="l">Last Name</label><input id="l"><label for="ln">Legal Name (First and Last)</label><input id="ln">`);
    await fill();
    expect([byId("f").value, byId("l").value, byId("ln").value]).toEqual(["Ada", "Example", "Ada Example"]);
  });

  // Dropbox (Greenhouse, live): the legal name asked as a question of its own.
  test("A short question that only asks for a name part ('What is your legal first name?') takes it; a longer question is no field", async () => {
    mount(`<label for="f">First Name</label><input id="f"><label for="l">Last Name</label><input id="l"><label for="lf">What is your legal first name?</label><input id="lf">
      <label for="ll">What is your Legal Last Name?</label><input id="ll"><label for="q">What is your first name's origin and meaning?</label><input id="q">`);
    await fill();
    expect([byId("f").value, byId("l").value, byId("lf").value, byId("ll").value, byId("q").value]).toEqual(["Ada", "Example", "Ada", "Example", ""]);
  });

  test("A form with only a full-name control gets the full name; 'First and Last Name' is the full name", async () => {
    mount(`<label for="n">First and Last Name</label><input id="n"><label for="em">Email</label><input id="em">`);
    await fill();
    expect(byId("n").value).toBe("Ada Example");
  });

  test("Existing value kept: the email input already contains a value the user typed, the engine leaves it and reports the row prefilled", async () => {
    mount(`<label for="em">Email</label><input id="em" value="typed@example.org"><label for="fn">First name</label><input id="fn">`);
    const s = await fill();
    expect(byId("em").value).toBe("typed@example.org");
    expect(rowFor(s, "email").outcome).toBe("prefilled");
  });

  test("A control that was empty at scan and holds a value the page authored is prefilled with the review flag", async () => {
    mount(`<label for="cv">Resume</label><input id="cv" type="file"><label for="em">Email</label><input id="em"><label for="fn">First name</label><input id="fn">`);
    byId("cv").addEventListener("change", () => {
      byId("em").value = "parsed@example.org";
    });
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    const row = rowFor(s, "email");
    expect(byId("em").value).toBe("parsed@example.org");
    expect(row).toMatchObject({ outcome: "prefilled", reason: "page-authored-value", review: true });
  });

  test("An identity field binds only to a control whose direct label contains its canonical word", async () => {
    mount(`<label for="m">Middle name</label><input id="m" autocomplete="additional-name"><label for="p">Preferred name</label><input id="p"><label for="fn">First name</label><input id="fn">`);
    await fill();
    expect([byId("m").value, byId("p").value, byId("fn").value]).toEqual(["", "", "Ada"]);
  });

  test("The autocomplete token binds when there is no direct label", async () => {
    mount(`<input id="g" autocomplete="given-name"><input id="f" autocomplete="family-name"><input id="e" type="email" autocomplete="email">`);
    await fill();
    expect([byId("g").value, byId("f").value, byId("e").value]).toEqual(["Ada", "Example", "ada@example.com"]);
  });

  test("Contact, address, link and confirm-email families", async () => {
    mount(`<label for="e">Email</label><input id="e"><label for="e2">Confirm your email</label><input id="e2">
      <label for="p">Phone</label><input id="p" type="tel"><label for="a">Street address</label><input id="a">
      <label for="c">City</label><input id="c"><label for="z">ZIP code</label><input id="z"><label for="li">LinkedIn profile</label><input id="li">
      <label for="gh">GitHub</label><input id="gh"><label for="st">State</label><select id="st"><option value="">Select</option><option>North Carolina</option><option>Ohio</option></select>`);
    await fill();
    expect(["e", "e2", "p", "a", "c", "z", "li", "gh", "st"].map((id) => byId(id).value)).toEqual([
      "ada@example.com",
      "ada@example.com",
      "+1 555 0100",
      "1 Example Street",
      "Durham",
      "27701",
      "https://www.linkedin.com/in/ada-example",
      "https://github.com/ada-example",
      "North Carolina",
    ]);
  });

  test("Personal address and phone never bind a Work History or Education control; references receive no profile value", async () => {
    mount(`<label for="fn">First name</label><input id="fn">
      <fieldset><legend>Work History</legend><label for="c1">City</label><input id="c1"><label for="p1">Phone</label><input id="p1"></fieldset>
      <fieldset><legend>References</legend><label for="r1">Email</label><input id="r1"></fieldset>`);
    await fill();
    expect([byId("c1").value, byId("p1").value, byId("r1").value]).toEqual(["", "", ""]);
  });

  test("Employer, reference and emergency-contact phone boxes stay empty; a narrative label takes no structured value", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="a">Employer phone</label><input id="a"><label for="b">Emergency contact phone</label><input id="b">
      <label for="c">Please state your email preferences</label><input id="c"><label for="d">Message to the hiring team</label><input id="d">`);
    await fill();
    expect([byId("a").value, byId("b").value, byId("c").value, byId("d").value]).toEqual(["", "", "", ""]);
  });

  test("Another person's name and email boxes stay empty: a supervisor, a manager, a reference, an emergency contact, a referrer", async () => {
    const labels = ["Supervisor email", "Emergency contact first name", "Emergency contact last name", "Reference email address", "Hiring manager first name", "Referrer email"];
    mount(labels.map((l, i) => `<label for="o${i}">${l}</label><input id="o${i}">`).join("") + `<label for="ac">Manager's email</label><input id="ac" autocomplete="email">`);
    await fill();
    expect([...labels.map((_, i) => byId(`o${i}`).value), byId("ac").value]).toEqual([...labels.map(() => ""), ""]);
  });

  test("ADP's phone label with a bracketed format hint binds the phone; the company's phone box beside it stays empty", async () => {
    const box = (label: string, name: string) =>
      `<div class="element"><label>${label}</label><div class="dijit dijitReset dijitInline dijitLeft dijitTextBox dijitValidationTextBox" widgetid="w_${name}"><div class="dijitReset dijitInputField dijitInputContainer"><input class="dijitReset dijitInputInner" type="tel" name="${name}" id="${name}" autocomplete="off"></div></div></div>`;
    mount(`<form>${box("Legal First Name", "firstName").replace('type="tel"', 'type="text"')}${box("Phone (Enter with dashes. Ex: 555-555-0100)", "phone")}${box("Company Phone Number (Enter with dashes. Ex: 555-555-0100)", "employerSupvPhone_1")}</form>`);
    const s = await fill({ ...ADA, candidate: { ...ADA.candidate, phone: "+1 919 555 0100" } });
    expect(byId("phone").value).toBe("919-555-0100");
    expect(rowFor(s, "Phone (Enter with dashes. Ex: 555-555-0100)")?.outcome).toBe("filled");
    expect(byId("employerSupvPhone_1").value).toBe("");
    // Alone on its step, the company's phone box is still not the applicant's.
    mount(`<form>${box("Legal First Name", "firstName").replace('type="tel"', 'type="text"')}${box("Company Phone Number (Enter with dashes. Ex: 555-555-0100)", "employerSupvPhone_1")}</form>`);
    await fill({ ...ADA, candidate: { ...ADA.candidate, phone: "+1 919 555 0100" } });
    expect(byId("employerSupvPhone_1").value).toBe("");
  });

  test("A bare Date in a self-identification block under its heading is the signature date; under any other heading it stays empty", async () => {
    // Jobvite's OFCCP block, captured live: plain divs under a heading, no fieldset or group.
    const block = (heading: string) => `<form><div class="jv-ofccp-section"><h4>${heading}</h4><p>Individuals seeking employment are considered without regard to race, color, religion.</p>
      <div class="jv-form-field"><div class="jv-form-field-input"><label for="n">Name*</label><div class="jv-form-field-control"><input id="n" type="text"></div></div></div>
      <div class="jv-form-field"><div class="jv-form-field-input"><label for="d">Date*</label><div class="jv-form-field-control"><input id="d" type="date"></div></div></div></div></form>`;
    mount(block("Equal Opportunity"));
    await fill();
    expect(byId("d").value).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    mount(block("Personal Information"));
    await fill();
    expect(byId("d").value).toBe("");
  });

  test("Every signature-date control receives today's date, a bare Date label does not", async () => {
    mount(`<label for="s">Signature date</label><input id="s"><label for="d">Date</label><input id="d"><label for="fn">First name</label><input id="fn">`);
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 9, 11, 12));
    try {
      await fill();
    } finally {
      vi.useRealTimers();
    }
    expect(byId("s").value).toBe("10/11/2026");
    expect(byId("d").value).toBe("");
  });

  test("A field holding the ask-me marker is a needs-user leftover and never reaches the page", async () => {
    mount(`<label for="a">Street address</label><input id="a"><label for="c">City</label><input id="c">`);
    const s = await fill({ candidate: { ...ADA.candidate, address: { city: "Durham" } }, askMe: ["address.line1"] });
    expect(byId("a").value).toBe("");
    expect(rowFor(s, "street").reason).toBe("ask-me");
    expect(byId("c").value).toBe("Durham");
  });

  test("The profile pass leaves self-identification, work-authorization and compensation controls to the matcher", async () => {
    mount(`<label for="g">Gender</label><input id="g"><label for="s">Will you require sponsorship?</label><input id="s"><label for="c">Desired salary</label><input id="c">${PAD}`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    expect([byId("g").value, byId("s").value, byId("c").value]).toEqual(["", "", ""]);
    expect(host.matchRequests[0]?.questions.map((q) => q.text)).toEqual(["Gender", "Will you require sponsorship?", "Desired salary"]);
  });
});

describe("Profile-field pass: boxes that stay empty", () => {
  test("A Zip+4 add-on box stays empty while the ZIP code box is filled", async () => {
    mount(`<label for="z4">Zip+4</label><input id="z4" name="longPostalCode" autocomplete="postal-code"><label for="pc">Zip Code</label><input id="pc" autocomplete="postal-code">${PAD}`);
    await fill();
    expect([byId("pc").value, byId("z4").value]).toEqual(["27701", ""]);
  });

  test("A job-alert sign-up form beside the application takes nothing; the application form takes the name (Procore)", async () => {
    const box = (id: string, label: string) => `<label for="${id}">${label}</label><input id="${id}" required>`;
    mount(
      `<h2>Sign Up For Job Alerts</h2><form>${box("a1", "First Name")}${box("a2", "Last Name")}${box("a3", "Email")}<button name="next_step" type="submit">Sign Up for Job Alerts</button></form>` +
        `<h2>Apply Now</h2><form>${box("b1", "First Name")}${box("b2", "Last Name")}${box("b3", "Email")}<button name="next_step" type="submit">Apply</button></form>`,
    );
    await fill();
    expect(["a1", "a2", "a3"].map((id) => byId(id).value)).toEqual(["", "", ""]);
    expect(["b1", "b2", "b3"].map((id) => byId(id).value)).toEqual(["Ada", "Example", "ada@example.com"]);
    // A sign-up box inside the application is a box, not the form's submit: the form is still the application.
    mount(`<form>${box("c1", "First Name")}<label><input type="checkbox" id="c2"> Subscribe to job alerts</label><button type="submit">Submit application</button></form>${PAD}`);
    await fill();
    expect(byId("c1").value).toBe("Ada");
    // A page wrapped in one form (ASP.NET) whose footer subscribes to a newsletter is still the application.
    mount(`<form>${box("d1", "First Name")}<button type="submit">Apply</button><footer-like><button type="submit">Subscribe</button></footer-like></form>${PAD}`);
    await fill();
    expect(byId("d1").value).toBe("Ada");
  });

  test("A job-alert box under an invitation outside its form takes nothing (HelloFresh, Phenom)", async () => {
    mount(
      `<section><h3>Get notified for similar jobs</h3><p>Sign up to receive job alerts</p><form><label for="n">Enter Email address (Required)</label><input id="n" type="text"><button type="button">Activate</button></form></section>` +
        `<form><label for="f">First Name</label><input id="f"><label for="e">Email</label><input id="e"><button type="submit">Submit</button></form>${PAD}`,
    );
    await fill();
    expect([byId("n").value, byId("f").value, byId("e").value]).toEqual(["", "Ada", "ada@example.com"]);
    // The same short form whose own submit applies is the application.
    mount(`<section><p>Get notified for similar jobs</p><form><label for="m">Email</label><input id="m"><button type="submit">Apply</button></form></section>${PAD}`);
    await fill();
    expect(byId("m").value).toBe("ada@example.com");
  });

  test("A field's label whose title is a span inside it names the box though its for points nowhere (Bending Spoons)", async () => {
    const field = (title: string, ph: string) =>
      `<div class="relative"><div class="flex items-end"><label class="label "><span class="label--title">${title}</span></label><label class="label label--requirements"></label></div><input inputmode="text" placeholder="${ph}" autocomplete="off"></div>`;
    mount(`<fieldset><legend><p>Step 1 of 3</p><p>Personal details</p></legend>${field("First name", "Olivia")}${field("Last name", "Smith")}${field("Email address", "olivia.smith@example.com")}</fieldset>`);
    await fill();
    const boxes = Array.from(document.querySelectorAll("input")).map((i) => i.value);
    expect(boxes).toEqual(["Ada", "Example", "ada@example.com"]);
  });

  test("A box labelled only Postal (JazzHR) is the ZIP code", async () => {
    mount(`<label for="p">Postal</label><input id="p">${PAD}`);
    await fill();
    expect(byId("p").value).toBe("27701");
  });

  test("The measurement twin of an auto-height text area is never filled", async () => {
    mount(`<label for="real">Cover note</label><textarea id="real"></textarea><label for="twin">Cover note</label><textarea id="twin" class="text-area-autoheight-mirror"></textarea><label for="em">Email</label><input id="em">`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(byId<HTMLTextAreaElement>("twin").value).toBe("");
    expect(host.matchRequests.flatMap((r) => r.questions).filter((q) => q.selector.includes("twin"))).toEqual([]);
    expect(s.rows.some((r) => r.selector.includes("twin"))).toBe(false);
  });

  test("Oracle's --helper twin, out of the accessibility tree and the tab order, is never asked, filled or listed", async () => {
    // Tallgrass: each long-text field is two text areas in one container; the helper only measures the height.
    mount(`<div class="input-row"><span class="input-row__label">Are you willing to relocate?</span>
      <ul class="cx-select-pills-container"><li><button class="cx-select-pill-section" aria-pressed="false">Yes</button></li><li><button class="cx-select-pill-section" aria-pressed="false">No</button></li></ul></div>
      <div class="input-row input-row--textarea"><form-element-label><label class="input-row__label input-row__label--required" for="300000063131107-21" aria-labelledby="labelText-300000063131107-21" aria-hidden="false">
        <span class="input-row__linebreak" id="labelText-300000063131107-21">Please provide three professional references. (Provide name, phone, and or email contact)</span></label></form-element-label>
        <div class="input-row__control-container"><textarea class="input-row__control input-row__control--autoheight" rows="1" id="300000063131107-21" name="300000063131107" required="true"></textarea><textarea class="input-row__control input-row__control--helper" rows="1" aria-hidden="true" tabindex="-1" style="height: 70px;"></textarea></div></div>${PAD}`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host)));
    const twin = document.querySelector(".input-row__control--helper") as HTMLTextAreaElement;
    expect(twin.value).toBe("");
    const asked = host.matchRequests.flatMap((r) => r.questions).filter((q) => q.text.startsWith("Please provide three"));
    expect(asked).toHaveLength(1);
    expect(document.querySelector(asked[0]!.selector)?.id).toBe("300000063131107-21");
    expect(s.rows.filter((r) => r.kind === "textarea")).toHaveLength(1);
  });

  test("A Middle box whose autocomplete token only ends in additional-name stays empty, and the first name is still filled", async () => {
    mount(`<label for="f">First Name</label><input id="f"><label for="mn">Middle</label><input id="mn" autocomplete="3f2c9a40-ukg-no-autocomplete-additional-name"><label for="l">Last Name</label><input id="l">`);
    await fill();
    expect([byId("f").value, byId("mn").value, byId("l").value]).toEqual(["Ada", "", "Example"]);
  });
});

describe("Profile-field pass: the disability form's signature block", () => {

  test("Workday's form heads its fields with an h3 before them rather than around them (ERCOT): the bare Name under it is the full name", async () => {
    mount(`${PAD}<div><h3>Voluntary Self-Identification of Disability</h3><div><label for="cc">Form</label><span id="cc">CC-305</span></div>
      <div data-automation-id="formField-name"><label for="sd-name"><span>Name</span></label><div><input id="sd-name" type="text" aria-required="true"></div></div></div>
      <div><h3>References</h3><div><label for="ref">Name</label><div><input id="ref" type="text"></div></div></div>`);
    await fill();
    expect(byId("sd-name").value).toBe("Ada Example");
    expect(byId("ref").value).toBe("");
  });

  test("Lever asks its sub-questions in h5s under the form's h4: the bare Name of the signature block is the full name, and not under a later section's sub-heading", async () => {
    const lever = `<h4>Self-identification of veteran status</h4><p>x</p><h4>Voluntary self-identification of disability</h4>
      <h5>Why are you being asked to complete this form?</h5><p>x</p><h5>How do you know if you have a disability?</h5><p>x</p>`;
    mount(`${PAD}<div>${lever}<div id="disabilitySignatureSection"><label><div>Name</div><div><input id="lv-name" type="text" name="eeo[disabilitySignature]" placeholder="Enter your full name"></div></label></div></div>`);
    await fill();
    expect(byId("lv-name").value).toBe("Ada Example");
    mount(`${PAD}<div>${lever}<h4>Additional information</h4><h5>Who referred you?</h5><label><div>Name</div><div><input id="lv-ref" type="text"></div></label></div>`);
    await fill();
    expect(byId("lv-ref").value).toBe("");
  });

  test("The disability form is signed in full below the applicant's own Full name, and on a form that asks the name in parts (Shield AI)", async () => {
    const lever = `<h4>Voluntary self-identification of disability</h4><h5>How do you know if you have a disability?</h5><p>x</p>`;
    const sign = `<div id="disabilitySignatureSection"><label><div>Name</div><div><input id="lv-sign" type="text" name="eeo[disabilitySignature]"></div></label></div>`;
    mount(`<label for="full">Full name</label><input id="full" name="name"><label for="em">Email</label><input id="em"><div>${lever}${sign}</div>`);
    await fill();
    expect([byId("full").value, byId("lv-sign").value]).toEqual(["Ada Example", "Ada Example"]);
    mount(`<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln"><div>${lever}${sign}</div>`);
    await fill();
    expect([byId("fn").value, byId("ln").value, byId("lv-sign").value]).toEqual(["Ada", "Example", "Ada Example"]);
  });
});

describe("Profile-field pass: a misspelt LinkedIn label", () => {
  test("'Linkedln', the capital I typed as a lower-case l (Firecrawl), is the LinkedIn box", async () => {
    mount(`<label for="em">Email</label><input id="em"><label for="li">Linkedln</label><input id="li">`);
    await fill();
    expect(byId("li").value).toBe("https://www.linkedin.com/in/ada-example");
  });
});

describe("Profile-field pass: a preferred last name", () => {
  test("A Preferred Last Name box below the legal Last Name takes the last name too (Harvey); a preferred first name is not the profile's", async () => {
    mount(`<label for="fn">First Name</label><input id="fn"><label for="ln">Last Name</label><input id="ln"><label for="pf">Preferred First Name</label><input id="pf"><label for="pl">Preferred Last Name</label><input id="pl">`);
    await fill();
    expect([byId("fn").value, byId("ln").value, byId("pf").value, byId("pl").value]).toEqual(["Ada", "Example", "", "Example"]);
  });
  test("A request for the preferred surname with its own note takes the last name too (Abnormal Security)", async () => {
    mount(`<label for="ln">Last Name</label><input id="ln"><label for="pl">Please enter your preferred Last Name/Surname (only enter your preferred Last Name/Surname)</label><input id="pl">`);
    await fill();
    expect([byId("ln").value, byId("pl").value]).toEqual(["Example", "Example"]);
  });
});

describe("Profile-field pass: a declaration signed in a bare Name box", () => {
  test("SuccessFactors' applicantConsentName box takes the full name (Huntington Ingalls); a bare Name box with no such name stays unbound", async () => {
    mount(`<p>I hereby certify that all information I provided is true and complete.</p><label for="481:_txtFld">Name</label><input id="481:_txtFld" name="applicantConsentName" type="text"><label for="q1">Name</label><input id="q1" name="q1" type="text">`);
    await fill();
    expect([byId("481:_txtFld").value, byId("q1").value]).toEqual(["Ada Example", ""]);
  });
});

describe("Page values on profile-owned fields after a résumé parse", () => {
  const PAYLOCITY = "https://recruiting.paylocity.com/recruiting/jobs/Apply/1/example";
  /** Paylocity draws a label as a sibling of its box in the field's own div, with no for. */
  const field = (id: string, label: string, value = "") => `<div class="form-group"><label>${label}<span><em> (required)</em></span></label><input id="${id}" data-for="${label}" type="text" value="${value}"></div>`;
  const parsed = () => `<label for="cv">Resume</label><input id="cv" type="file">${field("info.firstName", "First Name")}${field("info.email", "Email Address")}`;
  const parseOnUpload = () =>
    byId("cv").addEventListener("change", () => {
      byId("info.firstName").value = "Jordan";
      byId("info.email").value = "resume.address@example.org";
    });

  test("A label element with no for, alone in its field, names the box: the profile owns it and the parser's different guess is replaced", async () => {
    setUrl(PAYLOCITY);
    mount(parsed());
    parseOnUpload();
    const s = await fill();
    expect([byId("info.firstName").value, byId("info.email").value]).toEqual(["Ada", "ada@example.com"]);
    expect(rowFor(s, "Email Address")).toMatchObject({ lane: "profile", outcome: "filled" });
  });

  test("On a host whose parser is not known to guess, the page's value stays and is flagged for review", async () => {
    mount(parsed());
    parseOnUpload();
    const s = await fill();
    expect(byId("info.email").value).toBe("resume.address@example.org");
    expect(rowFor(s, "Email Address")).toMatchObject({ outcome: "prefilled", reason: "page-authored-value", review: true });
  });

  test("A box the page fills only once the run is under way is corrected too, where the parser guesses", async () => {
    setUrl(PAYLOCITY);
    mount(`${field("info.street", "Street address")}${field("info.city", "City")}`);
    // Picking the street makes the page fill the city after the profile pass planned it as empty.
    byId("info.street").addEventListener("input", () => {
      byId("info.city").value = "Pagetown";
    });
    const s = await fill();
    expect(byId("info.city").value).toBe("Durham");
    expect(rowFor(s, "City")).toMatchObject({ outcome: "filled", review: false });
  });

  test("Ashby's own applicant-name field is the full name, so its parser's guess without the space is replaced; another bare Name stays unbound", async () => {
    setUrl("https://jobs.ashbyhq.com/example/00000000/application");
    mount(`<label for="cv">Resume</label><input id="cv" type="file"><div><label for="_systemfield_name">Name</label><input id="_systemfield_name" name="_systemfield_name" type="text"></div><div><label for="q1">Name</label><input id="q1" type="text"></div>`);
    byId("cv").addEventListener("change", () => {
      byId("_systemfield_name").value = "AdaExample";
    });
    const s = await fill();
    expect([byId("_systemfield_name").value, byId("q1").value]).toEqual(["Ada Example", ""]);
    expect(rowFor(s, "Name")).toMatchObject({ lane: "profile", outcome: "filled" });
  });

  test("Ashby's applicant-name field relabelled First Name beside the tenant's own Last Name takes the first name, and the Last Name box the last", async () => {
    setUrl("https://jobs.ashbyhq.com/example/00000000/application");
    mount(`<div><label for="_systemfield_name">First Name</label><input id="_systemfield_name" name="_systemfield_name" type="text" required></div><div><label for="q1">Last Name</label><input id="q1" type="text" required></div>`);
    const s = await fill();
    expect([byId("_systemfield_name").value, byId("q1").value]).toEqual(["Ada", "Example"]);
    expect(rowFor(s, "First Name")).toMatchObject({ lane: "profile", outcome: "filled" });
  });

  test("Ashby's applicant-name field labelled First and last name keeps the full name (n8n)", async () => {
    setUrl("https://jobs.ashbyhq.com/example/00000000/application");
    mount(`<div><label for="_systemfield_name">First and last name</label><input id="_systemfield_name" name="_systemfield_name" type="text" required></div>`);
    await fill();
    expect(byId("_systemfield_name").value).toBe("Ada Example");
  });

  test("A value the person typed is never replaced, parser host or not", async () => {
    setUrl(PAYLOCITY);
    mount(parsed());
    const email = byId("info.email");
    // The person's own typing, as the browser marks it, replaces the parser's guess before the engine reaches the box.
    byId("cv").addEventListener("change", () => {
      email.value = "mine@example.org";
      const ev = new Event("input", { bubbles: true, composed: true });
      Object.defineProperty(ev, "isTrusted", { value: true });
      email.dispatchEvent(ev);
    });
    const s = await fill();
    expect(email.value).toBe("mine@example.org");
    expect(rowFor(s, "Email Address").outcome).not.toBe("filled");
  });
});
