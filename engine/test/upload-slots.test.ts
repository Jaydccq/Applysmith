// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: File upload and résumé-first ordering (Workday slots); Profile-field pass (split phone, third-party boxes).
import { describe, expect, test } from "vitest";
import { isSfUploader } from "../src/successfactors";
import { ADA, fakeHost, makeEngine, mount, PAD, runFill, summaryOf } from "./helpers";

describe("Workday upload slots", () => {
  test("The upload slot is claimed even when its label does not say résumé: the section heading is the marker", async () => {
    mount(`<section><h3>Resume/CV</h3><div><label for="up">Upload a file (5MB max)</label><input id="up" type="file"></div></section>${PAD}`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA }));
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["resume"]);
    expect(s.rows.find((r) => r.kind === "file")).toMatchObject({ outcome: "filled", fileName: "ada-example.pdf" });
  });

  test("A heading that names only a cover letter is a cover-letter slot and receives no résumé", async () => {
    mount(`<section><h3>Cover Letter</h3><div><label for="up">Upload a file (5MB max)</label><input id="up" type="file"></div></section>${PAD}`);
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ADA });
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["cover-letter"]);
  });

  test("After accepting a file the input reads empty: the rendered attachment list is the evidence, and a second attach is not made", async () => {
    mount(`<section><h3>Resume/CV</h3><div data-automation-id="resumeFileUpload"><input id="up" type="file"><div data-automation-id="file-upload-item">ada-example.pdf</div></div></section>${PAD}`);
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ADA });
    expect(host.resumeRequests).toEqual([]);
  });
});

describe("Lever upload cards", () => {
  const card = (question: string) =>
    `<div class="section application-form"><h4 data-qa="card-name">Attachments</h4><ul><li class="application-question custom-question"><div><div class="application-label full-width file-upload"><div class="text">${question}<span class="required">✱</span></div></div><div class="application-field full-width required-field"><a href="#" class="upload-file-overlay"><svg></svg><span class="filename" data-qa="filename"></span><span class="default-label">Upload file</span><input type="file" class="application-file-input" name="cards[c][field0]" required="required"></a></div></div></li></ul></div>`;

  // The run writes the letter only for a slot whose label names it (Go's coverSlot reads the engine's label).
  test("A card's file input is named by the card's question, not the button face drawn around it (Lean In's cover letter)", async () => {
    mount(`${card("Please attach your cover letter outlining your interest in the position and relevant experience.")}${PAD}`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA }));
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["cover-letter"]);
    expect(s.rows.find((r) => r.kind === "file")?.label).toBe("Please attach your cover letter outlining your interest in the position and relevant experience.");
  });

});

describe("Phone presentation beside a country code", () => {
  const applicant = { ...ADA, candidate: { ...ADA.candidate, phone: "+1 919 555 0100" } };

  test("Split phone: an intl-tel-input showing +1 takes the national number, and any other dial code keeps the full number so the widget re-picks the country", async () => {
    mount(`<div class="iti iti--separate-dial-code"><div class="iti__selected-flag"><div class="iti__selected-dial-code">+1</div></div><label for="ph">Phone</label><input id="ph" type="tel"></div>${PAD}`);
    await runFill(makeEngine(fakeHost()), { applicant });
    expect((document.getElementById("ph") as HTMLInputElement).value).toBe("9195550100");
    mount(`<div class="iti iti--separate-dial-code"><div class="iti__selected-flag"><div class="iti__selected-dial-code">+44</div></div><label for="ph">Phone</label><input id="ph" type="tel"></div>${PAD}`);
    await runFill(makeEngine(fakeHost()), { applicant });
    expect((document.getElementById("ph") as HTMLInputElement).value).toBe("+1 919 555 0100");
  });

  test("A third party's phone box stays empty even when the matcher offers the applicant's own number", async () => {
    mount(`<label for="e">Employer phone</label><input id="e" type="tel"><label for="r">Reference email</label><input id="r">${PAD}`);
    const host = fakeHost({ "employer phone": { outcome: "filled", value: "+1 919 555 0100" }, "reference email": { outcome: "filled", value: "ada@example.com" } });
    const s = summaryOf(await runFill(makeEngine(host), { applicant }));
    expect((document.getElementById("e") as HTMLInputElement).value).toBe("");
    expect((document.getElementById("r") as HTMLInputElement).value).toBe("");
    expect(s.rows.find((r) => r.label === "Employer phone")).toMatchObject({ outcome: "abstained", reason: "section-policy" });
  });

  test("intl-tel-input with the international placeholder takes the +1 number: its own dropdown search and an address search beside it are no country picker (Teamtailor)", async () => {
    const phoneBox = `<label for="candidate_phone">Phone<sup aria-hidden="true">*</sup><span class="sr-only">Required</span></label>
      <div class="iti iti--allow-dropdown iti--show-flags iti--inline-dropdown"><div class="iti__country-container">
        <button type="button" class="iti__selected-country" aria-expanded="false" aria-label="Change country, selected United States (+1)" aria-haspopup="dialog" aria-controls="iti-0__dropdown-content"><div class="iti__selected-country-primary"><div class="iti__flag iti__us"></div></div></button>
        <div id="iti-0__dropdown-content" class="iti__dropdown-content iti__hide" role="dialog"><div class="iti__search-input-wrapper"><input id="iti-0__search-input" type="search" class="iti__search-input" placeholder="Search" role="combobox" aria-expanded="true" aria-label="Search" aria-controls="iti-0__country-listbox" aria-autocomplete="list"></div>
          <ul class="iti__country-list" id="iti-0__country-listbox" role="listbox"><li class="iti__country" role="option" data-dial-code="1" data-country-code="us"><span class="iti__country-name">United States</span><span class="iti__dial-code" dir="ltr">+1</span></li></ul></div></div>
        <input autocomplete="tel" aria-label="Phone number with country code" class="iti__tel-input" type="tel" name="candidate[phone]" id="candidate_phone" placeholder="+1 201-555-0123"></div>`;
    const address = `<label for="candidate_location">Address</label><input id="candidate_location" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="loc-list" aria-haspopup="listbox"><div role="listbox" id="loc-list" style="display:none"></div>`;
    for (const [name, html] of [["dropdown search only", phoneBox], ["with an address search", `${phoneBox}${address}`]] as const) {
      mount(`${html}${PAD}`);
      const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant }));
      expect((document.getElementById("candidate_phone") as HTMLInputElement).value, name).toBe("+19195550100");
      expect(s.rows.find((r) => r.label.startsWith("Phone")), name).toMatchObject({ outcome: "filled", required: true });
    }
  });
});

describe("Parse waits that depend on the system", () => {
  test("iCIMS keeps the uploaded file in its own state: a saved-name field that is not empty means the upload took, and the file is not attached again", async () => {
    mount(`<label for="cv">Resume</label><input id="cv" type="file"><input type="hidden" id="x_Resume_FileName" value="ada-example.pdf">${PAD}`);
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ADA });
    expect(host.resumeRequests).toEqual([]);
  });

  test("Hirebridge after its parse postback: the file input is empty, the page shows the uploaded name, and the file is not attached again", async () => {
    // Live (recruit.hirebridge.com QuickApply.aspx): the postback leaves the file input empty and shows the name beside a hidden resumeNameSave.
    const page = (saved: string) => `<label for="uploadedresume">Resume</label><input name="ctl00$pageContent$ResumeUpload1$uploadedresume" type="file" id="uploadedresume" accept="application/pdf">
      <input name="ctl00$pageContent$ResumeUpload1$resumeNameSave" type="hidden" id="resumeNameSave" value="${saved}"><div id="resumeName" class="resume-filename">${saved}</div>${PAD}`;
    mount(page("Applicant_resume.pdf"));
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ADA });
    expect(host.resumeRequests).toEqual([]);
  });

});

describe("Résumé slots that read as already uploaded or stay closed", () => {
  const attached = async () => {
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA }));
    return { host, row: s.rows.find((r) => r.kind === "file") };
  };

  test("A list of accepted formats beside the slot is an instruction, not an upload (JazzHR's Attach resume)", async () => {
    mount(`<div class="form-group"><label id="l" for="resumator-resume-value">Resume<i class="asterisk">*</i></label>
      <div id="resumator-resume-upload-wrapper"><div class="resumator-resume-text">Attach resume as .pdf, .doc, .docx, .odt, .txt, or .rtf (limit 5MB) or <a href="#">Paste resume</a></div>
      <div class="resumator-input"><input type="hidden" name="MAX_FILE_SIZE" value="10585760"><input type="file" id="resumator-resume-value" name="resumator-resume-value"></div></div></div>${PAD}`);
    const { host, row } = await attached();
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["resume"]);
    expect(row).toMatchObject({ label: "Resume", outcome: "filled", fileName: "ada-example.pdf" });
  });

  test("A file name beside the slot still says the upload took", async () => {
    mount(`<div><label for="cv">Resume</label><div>Attach resume as .pdf, .docx</div><div>ada-example.pdf</div><input type="file" id="cv"></div>${PAD}`);
    const { host } = await attached();
    expect(host.resumeRequests).toEqual([]);
  });

  test("A label drawn around the upload button, named by a screen-reader count and the field title, is the résumé slot (Rippling)", async () => {
    mount(`<div data-testid="field"><span id="field-8-label">Résumé</span><span>*</span>
      <div data-testid="screen-reader-only" id="file-input-10">Total 0 file selected</div>
      <label data-testid="resume" aria-labelledby="file-input-10 field-8-label"><input title="" accept=".doc,.docx,.pdf" type="File" id="field-8"><button type="button"><span>Drop or select (.doc / .docx / .pdf)</span></button></label></div>${PAD}`);
    const { host, row } = await attached();
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["resume"]);
    expect(row).toMatchObject({ label: "Résumé", outcome: "filled" });
    expect((document.getElementById("field-8") as HTMLInputElement).files?.[0]?.name).toBe("ada-example.pdf");
  });

  const jobvite = (button: string, extra = "") => `<h3 id="jv-resume-header">Add Resume*</h3>${button}${extra}
    <div id="attachmentDropdown" role="dialog" aria-hidden="true" aria-label="Attachment Options" style="display:none"><div style="display:none">
      <label for="file-input-0"><span role="button">File</span></label><input id="file-input-0" type="file" style="position:absolute;width:1px;height:1px;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);border:0"></div>
      <span role="button">Type or Paste Resume</span></div><label for="fn">First Name</label><input id="fn"><label for="em">Email Address</label><input id="em" type="email">${PAD}`;
  const select = `<button type="button" aria-haspopup="true" aria-labelledby="jv-resume-header" aria-expanded="false">Select</button>`;

  test("A closed attachment menu that a résumé menu button opens takes the file directly (Jobvite's Add Resume)", async () => {
    mount(jobvite(select));
    const { host, row } = await attached();
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["resume"]);
    expect(row).toMatchObject({ outcome: "filled", fileName: "ada-example.pdf" });
    expect((document.getElementById("file-input-0") as HTMLInputElement).files?.[0]?.name).toBe("ada-example.pdf");
  });

  test("A closed upload with no résumé menu button, or beside a résumé slot the page shows, is left alone", async () => {
    mount(jobvite(`<button type="button" aria-haspopup="true">Select</button>`));
    expect((await attached()).host.resumeRequests).toEqual([]);
    mount(jobvite(select, `<label for="cv">Resume</label><input type="file" id="cv">`));
    await attached();
    expect((document.getElementById("file-input-0") as HTMLInputElement).files).toHaveLength(0);
    expect((document.getElementById("cv") as HTMLInputElement).files).toHaveLength(1);
  });

  test("Breezy keeps the uploaded file in its own state: the file name beside the hidden input answers the required slot", async () => {
    mount(`<form name="form"><div class="section-header"><input id="resume_required" type="hidden" value="required"><div class="file-input-container type">
      <a href="" class="bzyLinkColor"><span class="fa fa-paperclip"></span><span> </span><span data-private="true">alex-example-platform.pdf</span></a>
      <input id="main-attachment" name="cResume" style="display:none;" type="file" required accept=".pdf, .doc, .docx, .txt, .rtd, .pages">
      <div class="error-container"><span class="error">A resume is required</span></div></div></div>
      <h3><span>Full Name</span><span class="required" title="Required">*</span></h3><input required placeholder="Full Name" type="text" name="cName"></form>`);
    const { host, row } = await attached();
    expect(host.resumeRequests).toEqual([]);
    expect(row).toMatchObject({ outcome: "prefilled", required: true });
  });

  test("JazzHR's application form is not SuccessFactors' uploader, and its closed Attach resume slot is opened and takes the file", async () => {
    mount(`<form id="form_submit_new_resume" method="POST" enctype="multipart/form-data"><label for="fn">First Name</label><input id="fn" name="resumator-firstname-value">
      <div id="resumator-resume" class="form-group"><label id="resumator-resume-label" for="resumator-resume-value" class="control-label">Resume<i class="asterisk">*</i></label>
        <div id="resumator-resume-options" class="resumator-resume-text"><a id="resumator-choose-upload" class="resumator-resume-action" href="#">Attach resume</a> or <a id="resumator-choose-paste" class="resumator-resume-action" href="#">Paste resume</a></div>
        <div id="resumator-resume-upload-wrapper" style="display:none"><div class="resumator-resume-text">Attach resume as .pdf, .doc, .docx, .odt, .txt, or .rtf (limit 5MB) or <a id="resumator-resume-switch-paste" href="#">Paste resume</a></div>
          <div class="resumator-input" id="resumator-resume-field"><input type="hidden" name="MAX_FILE_SIZE" value="10585760"><input type="file" id="resumator-resume-value" name="resumator-resume-value"></div></div>
        <div id="resumator-resume-paste-wrapper" class="resumator-resume-text" style="display:none"><textarea name="resumator-resumetext-value" id="resumator-resumetext-value" rows="5"></textarea></div></div></form>${PAD}`);
    const input = document.getElementById("resumator-resume-value") as HTMLInputElement;
    expect(isSfUploader(input)).toBe(false);
    const opened: string[] = [];
    document.getElementById("resumator-choose-upload")?.addEventListener("click", (e) => {
      e.preventDefault();
      opened.push("upload");
      (document.getElementById("resumator-resume-upload-wrapper") as HTMLElement).style.display = "block";
    });
    const { host, row } = await attached();
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["resume"]);
    expect(opened).toEqual(["upload"]);
    expect(row).toMatchObject({ label: "Resume", outcome: "filled", required: true, fileName: "ada-example.pdf" });
    expect(input.files?.[0]?.name).toBe("ada-example.pdf");
  });

  test("A step whose only field is the résumé upload behind the page's own Upload Resume button is an application step (evlo)", async () => {
    mount(`<form action="#"><section aria-labelledby="h"><h2 id="h">Upload Resume</h2><div aria-label="Resume and cover letter"><div><input accept=".pdf" style="display:none" type="file">
      <div><p>Drag and drop your resume here or</p><button type="button">Upload Resume</button><p>Supported formats: PDF (Max 5MB)</p></div></div></div>
      <div><button type="button" disabled>Next</button></div></section></form>`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA }));
    expect(s.notAnApplication).toBeUndefined();
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["resume"]);
    expect(s.rows.find((r) => r.kind === "file")).toMatchObject({ outcome: "filled", fileName: "ada-example.pdf" });
  });

  test("A page with a résumé upload but no Upload Resume button of its own is still no application", async () => {
    mount(`<section><h2>Senior Engineer</h2><p>Drag and drop your resume here</p><input accept=".pdf" style="display:none" type="file"><a href="/apply">Apply</a></section>`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA }));
    expect(s.notAnApplication).toBe(true);
    expect(host.resumeRequests).toEqual([]);
  });

});
