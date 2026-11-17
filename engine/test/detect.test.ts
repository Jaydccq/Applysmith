// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Job-site and application-form detection (form detection part).
import { describe, expect, test } from "vitest";
import { detectApplication } from "../src/detect";
import { ADA, fakeHost, makeEngine, mount, runFill, summaryOf } from "./helpers";

describe("Application-form detection", () => {
  test("Form detection floor: a site-search box and a newsletter input are not an application form", () => {
    mount(`<header><input type="search" name="q" aria-label="Search jobs"></header>
      <footer><label for="nl">Newsletter</label><input id="nl" type="email" placeholder="you@example.com"></footer>`);
    expect(detectApplication(document)).toEqual({ application: false, reason: "no-form" });
  });

  test("Form detection floor, run level: nothing is written and the summary says so", async () => {
    mount(`<label for="nl">Newsletter email</label><input id="nl" type="email">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(s.notAnApplication).toBe(true);
    expect(s.rows).toHaveLength(0);
  });

  test("Workday's experience step: bare Add buttons under section headings and an uploaded résumé are an application form, and an employer is added", async () => {
    const group = (id: string, name: string) => `<div role="group" aria-labelledby="${id}"><h3 id="${id}">${name}</h3><div class="rows"></div><div><button type="button" data-automation-id="add-button">Add</button></div></div>`;
    mount(`<div data-automation-id="applyFlowMyExpPage">${group("Work-Experience-section", "Work Experience")}${group("Education-section", "Education")}${group("Websites-section", "Websites")}
      <div><h3>Resume/CV</h3><div data-automation-id="file-upload-item"><div data-automation-id="file-upload-item-name">jordan-avery.pdf</div><div>Successfully Uploaded!</div></div></div></div>`);
    let added = 0;
    const work = document.querySelector('[aria-labelledby="Work-Experience-section"]') as HTMLElement;
    work.querySelector("button")!.addEventListener("click", () => {
      added++;
      work.querySelector(".rows")!.insertAdjacentHTML("beforeend", `<div><h4>Work Experience ${added}</h4><label for="t${added}">Job Title</label><input id="t${added}"><label for="c${added}">Company</label><input id="c${added}"></div>`);
    });
    const applicant = { ...ADA, history: { employment: [{ employer: "Acme Robotics", title: "Engineer", startDate: "2020-01-01", endDate: "2023-01-01" }] } };
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant }));
    expect(s.notAnApplication).toBeUndefined();
    expect(added).toBe(1);
  });

  test("Workday's experience step with only a résumé upload, named from its upload area, is an application form, and the résumé is attached", async () => {
    mount(`<div data-automation-id="applyFlowMyExpPage"><div><h3>Resume/CV Only</h3><p>Please upload your RESUME ONLY. If additional attachments are required, you will be asked later in the application.</p>
      <div data-automation-id="formField-"><label id="label10"><span>Upload a file (5MB max)<abbr aria-hidden="true">*</abbr></span></label><div data-automation-id="attachments-FileUpload" aria-labelledby="label10">
        <div data-automation-id="file-upload-drop-zone"><div>Drop files here</div><div>or</div><button type="button" data-automation-id="select-files">Select files</button></div>
        <input data-automation-id="file-upload-input-ref" type="file" multiple="" style="display:none"></div></div></div>
      <div role="group" aria-labelledby="Websites-section"><h3 id="Websites-section">Websites</h3><p>Add any relevant websites.</p><button type="button" data-automation-id="add-button">Add</button></div></div>`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(s.notAnApplication).toBeUndefined();
    expect(s.rows.find((r) => r.kind === "file")?.fileName).toBe("ada-example.pdf");
  });

  test("A posting page's résumé drop for job matches, with nothing to add, is not an application form", async () => {
    mount(`<h1>Software Engineer</h1><button type="button">Apply</button><button type="button">Add to job cart</button>
      <div><h3>Upload your resume to find matching jobs</h3><input type="file" style="display:none"></div>`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(s.notAnApplication).toBe(true);
  });

  test("One required field left: a wizard's last step with one required text input is an application form", () => {
    mount(`<label for="y">Years of experience *</label><input id="y" required>`);
    expect(detectApplication(document)).toMatchObject({ application: true });
  });

  test("Workday step of required listbox questions only: the questions make it an application form", () => {
    const q = (id: string, text: string) => `<fieldset><legend><p><b>${text}<abbr title="required">*</abbr></b></p></legend>
      <button aria-haspopup="listbox" type="button" aria-label=" Select One Required" id="${id}">Select One</button><input type="text" style="display:none"></fieldset>`;
    mount(q("q1", "Are you at least 18 years of age?") + q("q2", "Are you willing to relocate?"));
    expect(detectApplication(document)).toMatchObject({ application: true });
  });

  test("NVIDIA's Workday questions carry no required marker: a listbox in Workday's own form field is a question", () => {
    const q = (id: string, text: string) => `<div data-automation-id="formField-${id}"><fieldset><legend><div data-automation-id="richText"><p><b>${text}</b></p></div></legend>
      <button aria-haspopup="listbox" type="button" aria-label=" Select One" id="primaryQuestionnaire--${id}">Select One</button><input type="text" value="" style="display:none"></fieldset></div>`;
    mount(`<button aria-haspopup="listbox" type="button" id="settingsSelectorButton">Settings</button>` +
      q("a1", "Are you legally authorized to work in the United States?") + q("a2", "Will you now or in the future require sponsorship for employment visa status (e.g. H-1B visa status)?"));
    expect(detectApplication(document)).toMatchObject({ application: true, controls: 2 });
  });

  test("Optional listbox filters on a job search page are not an application form", () => {
    mount(`<button aria-haspopup="listbox" type="button" aria-label="Location">Any location</button>
      <button aria-haspopup="listbox" type="button" aria-label="Category">Any category</button>`);
    expect(detectApplication(document)).toEqual({ application: false, reason: "no-form" });
  });

  test("Hidden and disabled controls do not count", () => {
    mount(`<input type="hidden" name="csrf" value="x"><label for="a">First name</label><input id="a" style="display:none"><label for="b">Last</label><input id="b" disabled>`);
    expect(detectApplication(document)).toMatchObject({ application: false });
  });

  test("Dialog blocks the form: an open sign-up modal over an aria-hidden application form reports blocked-by-dialog", async () => {
    mount(`<div aria-hidden="true"><label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b"></div>
      <div role="dialog"><p>Sign up to continue</p><button>Close</button></div>`);
    expect(detectApplication(document)).toEqual({ application: false, reason: "blocked-by-dialog" });
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(s.blockedByDialog).toBe(true);
  });

  test("aria-hidden is ignored inside a dialog the user can see", () => {
    mount(`<div role="dialog" aria-hidden="true"><label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b"></div>`);
    expect(detectApplication(document)).toMatchObject({ application: true });
  });
});
