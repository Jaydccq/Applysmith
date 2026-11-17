// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: repeated rows - Workday's work experience rows as the live page draws them.
import { expect, test } from "vitest";
import { ADA, fakeHost, makeEngine, mount, runFill, setUrl } from "./helpers";
import { mountWorkdayPrompt } from "./ats-fixtures";


const date = (row: string, which: string, label: string) => `<div data-automation-id="formField-${which}" data-fkit-id="${row}--${which}"><fieldset><legend><label id="l-${row}-${which}"><span>${label}<abbr aria-hidden="true">*</abbr></span></label></legend><div><div><div aria-hidden="true" id="helpText-${row}--${which}">current value is MM/YYYY</div>
  <div id="${row}--${which}" aria-labelledby="hiddenDateValueId-${row}--${which} ERROR" role="group" data-automation-id="dateInputWrapper"><div tabindex="-1">
  <div id="${row}--${which}-dateSectionMonth"><div aria-hidden="true" data-automation-id="dateSectionMonth-display">MM</div><input role="spinbutton" aria-label="Month" aria-valuemax="12" aria-valuemin="1" aria-valuetext="MM" aria-required="true" id="${row}--${which}-dateSectionMonth-input" data-automation-id="dateSectionMonth-input" value=""></div><div>/</div>
  <div id="${row}--${which}-dateSectionYear"><div aria-hidden="true" data-automation-id="dateSectionYear-display">YYYY</div><input role="spinbutton" aria-label="Year" aria-valuemax="9999" aria-valuemin="1" aria-valuetext="YYYY" aria-required="true" id="${row}--${which}-dateSectionYear-input" data-automation-id="dateSectionYear-input" value=""></div></div>
  <div aria-label="Calendar" data-automation-id="dateIcon" role="button" tabindex="0"></div></div></div></div></fieldset></div>`;
const text = (row: string, f: string, label: string, req = true) => `<div data-automation-id="formField-${f}"><label for="${row}--${f}">${label}${req ? '<abbr aria-hidden="true">*</abbr>' : ""}</label><div><input type="text" id="${row}--${f}" aria-required="${req}" value=""></div></div>`;
const rowHtml = (n: number, row: string) => `<div role="group" aria-labelledby="Work-Experience-${n}-panel"><h4 id="Work-Experience-${n}-panel">Work Experience ${n}</h4><button type="button" aria-label="Delete Work Experience ${n}">Delete</button>
  ${text(row, "jobTitle", "Job Title")}${text(row, "companyName", "Company")}${text(row, "location", "Location", false)}
  <div data-automation-id="formField-currentlyWorkHere"><label for="${row}--currentlyWorkHere">I currently work here</label><div><input id="${row}--currentlyWorkHere" type="checkbox" aria-checked="false" name="currentlyWorkHere" aria-required="false"></div></div>
  ${date(row, "startDate", "From")}${date(row, "endDate", "To")}</div>`;
const value = (id: string): string => (document.getElementById(id) as HTMLInputElement).value;
const ticked = (id: string): boolean => (document.getElementById(id) as HTMLInputElement).checked;

test("Workday rows numbered from its running counter take each entry's dates and current-job box", async () => {
  setUrl("https://rockwellautomation.wd1.myworkdayjobs.com/External/job/x/apply");
  mount(`<div data-automation-id="applyFlowMyExpPage"><div role="group" aria-labelledby="Work-Experience-section"><h3 id="Work-Experience-section">Work Experience</h3><div class="rows"></div><div><button type="button" data-automation-id="add-button">Add</button></div></div></div>`);
  const g = document.querySelector('[aria-labelledby="Work-Experience-section"]') as HTMLElement;
  let n = 0;
  // Workday's row numbers keep rising across adds and deletes: the rows here are 7 and 14.
  g.querySelector("button")!.addEventListener("click", () => {
    n++;
    g.querySelector(".rows")!.insertAdjacentHTML("beforeend", rowHtml(n, `workExperience-${n * 7}`));
  });
  const applicant = { ...ADA, history: { employment: [
    { employer: "Example Robotics", title: "Software Engineer", startDate: "2026-06", endDate: "present", location: "Durham, NC" },
    { employer: "Example Lab", title: "Research Intern", startDate: "2023-07", endDate: "2023-09", location: "Raleigh, NC" },
  ] } };
  await runFill(makeEngine(fakeHost()), { applicant });
  const seg = (row: number, which: string) => [value(`workExperience-${row}--${which}-dateSectionMonth-input`), value(`workExperience-${row}--${which}-dateSectionYear-input`)];
  expect(seg(7, "startDate")).toEqual(["06", "2026"]);
  expect(ticked("workExperience-7--currentlyWorkHere")).toBe(true);
  expect(seg(7, "endDate")).toEqual(["", ""]);
  expect(seg(14, "startDate")).toEqual(["07", "2023"]);
  expect(seg(14, "endDate")).toEqual(["09", "2023"]);
  expect(ticked("workExperience-14--currentlyWorkHere")).toBe(false);
});

const yearOnly = (row: string, which: string, label: string) => `<div data-automation-id="formField-${which}" data-fkit-id="${row}--${which}"><fieldset><legend><label id="l-${row}-${which}"><span>${label}<abbr aria-hidden="true">*</abbr></span></label></legend><div><div><div aria-hidden="true" id="helpText-${row}--${which}">current value is YYYY</div>
  <div id="${row}--${which}" aria-labelledby="hiddenDateValueId-${row}--${which}" role="group" data-automation-id="dateInputWrapper"><div tabindex="-1">
  <div id="${row}--${which}-dateSectionYear"><div aria-hidden="true" data-automation-id="dateSectionYear-display">YYYY</div><input role="spinbutton" aria-describedby="helpText-${row}--${which}" aria-label="Year" aria-valuemax="9999" aria-valuemin="1" aria-valuetext="YYYY" aria-required="true" id="${row}--${which}-dateSectionYear-input" data-automation-id="dateSectionYear-input" value=""></div></div></div></div></div></fieldset></div>`;

test("A field of study the matcher answers takes the faculty of the degree that holds it", async () => {
  // NXP: the question pass writes the field of study before the row plan runs.
  setUrl("https://nxp.wd3.myworkdayjobs.com/careers/job/x/apply");
  const run = async (education: Record<string, unknown>[]) => {
    mount(`<div data-automation-id="applyFlowMyExpPage" id="page">${text("education-7", "school", "School or University")}</div>`);
    const p = mountWorkdayPrompt({ id: "fieldOfStudy", label: "Field of Study", catalog: ["Arts-Mathematics", "Science-Mathematics"], answers: { "Applied Mathematics": [] }, parent: document.getElementById("page") as HTMLElement });
    await runFill(makeEngine(fakeHost({ "field of study": { outcome: "filled", value: "Applied Mathematics" } })), { applicant: { ...ADA, history: { education } } });
    return p.pills();
  };
  const bs = { school: "Example University", degree: "Bachelor of Science (B.S)", field: "Applied Mathematics", startDate: "2020-09", endDate: "2024-06" };
  expect(await run([bs])).toEqual(["Science-Mathematics"]);
  // Two degrees in the subject name no one faculty.
  expect(await run([bs, { ...bs, degree: "Master of Arts", startDate: "2024-09", endDate: "2026-05" }])).toEqual([]);
});

test("Workday's year-only From and To of an education row take the entry's start and end years", async () => {
  setUrl("https://ensemblehp.wd5.myworkdayjobs.com/EnsembleHealthPartnersCareers/job/x/apply");
  const row = "education-31";
  mount(`<div data-automation-id="applyFlowMyExpPage"><div role="group" aria-labelledby="Education-section"><h3 id="Education-section">Education</h3>
    <div role="group" aria-labelledby="Education-1-panel"><h4 id="Education-1-panel">Education 1</h4><button type="button" aria-label="Delete Education 1">Delete</button>
    ${text(row, "school", "School or University")}${yearOnly(row, "firstYearAttended", "From")}${yearOnly(row, "lastYearAttended", "To (Actual or Expected)")}</div></div></div>`);
  const applicant = { ...ADA, history: { education: [{ school: "Example University", degree: "Bachelor of Science", field: "Mathematics", startDate: "2020-09", endDate: "2024-06" }] } };
  await runFill(makeEngine(fakeHost()), { applicant });
  expect(value(`${row}--firstYearAttended-dateSectionYear-input`)).toBe("2020");
  expect(value(`${row}--lastYearAttended-dateSectionYear-input`)).toBe("2024");
});
