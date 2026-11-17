// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Repeated rows are answered only by their own entry; Entity-row reconciliation safety;
// Profile-field pass (repeated education rows); Drivers for HR-suite application systems (rows).
import { describe, expect, test, vi } from "vitest";
import { isUnsafeRemoval } from "../src/click-guard";
import { bindRows, collectRows, fieldOf, readRows, rowValue } from "../src/rows";
import { junkOf, removerFor } from "../src/sections";
import { readInfo, scanControls } from "../src/scan";
import type { ApplicantSnapshot } from "../src/protocol";
import { fakeHost, makeEngine, mount, runFill, setUrl, summaryOf, until } from "./helpers";
import { mountOracleAddress, mountPaylocityAddress, mountRows } from "./ats-fixtures";
import { FAST } from "./helpers";


const ALEX: ApplicantSnapshot = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example", email: "alex@example.com", phone: "+1 555 0100", address: { line1: "1 Example Street", city: "Durham", state: "NC", postalCode: "27701", country: "United States" } },
  history: {
    employment: [
      { employer: "Example Labs", title: "Staff Engineer", location: "Durham", startDate: "2021-03-01", current: true },
      { employer: "Sample Works", title: "Engineer", location: "Raleigh", startDate: "2017-06-01", endDate: "2021-02-01" },
    ],
    education: [
      { school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" },
      { school: "Sample College", degree: "Master of Science", field: "Data Science", startDate: "2017-09-01", endDate: "2019-05-01" },
    ],
  },
};

function rowsOf() {
  return collectRows(scanControls(document), document);
}

/** The page's résumé parse: when a file is chosen, it adds this row a moment later. */
function parseOnUpload(f: { addRow: (v: Record<string, string>) => void }, row: Record<string, string>): void {
  const holder = document.createElement("div");
  holder.innerHTML = `<label for="cv">Resume</label><input id="cv" type="file">`;
  document.body.append(holder);
  holder.querySelector("input")!.addEventListener("change", () => f.addRow(row));
}

describe("Repeated rows", () => {
  test("Repeated education rows: two profile entries and one form row, one row is added through the form's own add control and both are filled", async () => {
    setUrl("https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply");
    mount("");
    const f = mountRows({ entity: "education", style: "workday", rows: [{}] });
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ALEX }));
    expect(f.addClicks()).toBe(1);
    expect(f.values()).toEqual([
      { school: "Example University", degree: "Bachelor of Science", major: "Computer Science", start: "2013-09-01", end: "2017-05-01" },
      { school: "Sample College", degree: "Master of Science", major: "Data Science", start: "2017-09-01", end: "2019-05-01" },
    ]);
    expect(s.rows.filter((r) => r.lane === "profile" && r.outcome === "filled").length).toBeGreaterThanOrEqual(10);
  });

  test("Add does nothing: an Add click that mounts no row stops the add loop after that click", async () => {
    setUrl("https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply");
    mount("");
    const f = mountRows({ entity: "education", style: "workday", rows: [{}], noAdd: true });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(f.addClicks()).toBeLessThanOrEqual(1);
    expect(f.values()).toHaveLength(1);
  });

  test("Reordered parser rows: the parser lists the Master's school first and each row receives the degree of the school it shows", async () => {
    setUrl("https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply");
    mount("");
    const f = mountRows({
      entity: "education",
      style: "workday",
      rows: [
        { school: "Sample College", degree: "Wrong Degree" },
        { school: "Example University", degree: "Wrong Degree" },
      ],
    });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const v = f.values();
    expect(v[0]).toMatchObject({ school: "Sample College", degree: "Master of Science" });
    expect(v[1]).toMatchObject({ school: "Example University", degree: "Bachelor of Science" });
  });

  test("ADP employer row: a row's City, State and Country take only the row's own data", async () => {
    mount(`<div class="employment"><h3>Employment History</h3>
      <input type="hidden" name="$$employerId_1_x" value="1">
      <label for="c1">City</label><input id="c1" name="employerCity_1_x">
      <label for="s1">State</label><input id="s1" name="employerState_1_x">
      <label for="co1">Country</label><input id="co1" name="employerCountry_1_x"></div>
      <label for="home">City</label><input id="home" name="city" autocomplete="address-level2">`);
    await runFill(makeEngine(fakeHost()), { applicant: { ...ALEX, history: { employment: [{ employer: "Example Labs", title: "Staff Engineer" }] } } });
    expect((document.getElementById("home") as HTMLInputElement).value).toBe("Durham");
    for (const id of ["c1", "s1", "co1"]) expect((document.getElementById(id) as HTMLInputElement).value, id).toBe("");
  });

  test("Education N heading: a bare row label under Education 2 answers from the second entry, and a row the profile has no entry for stays blank", async () => {
    mount("");
    const f = mountRows({ entity: "education", style: "heading", rows: [{}, {}, {}] });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const v = f.values();
    expect(v[0]).toMatchObject({ school: "Example University" });
    expect(v[1]).toMatchObject({ school: "Sample College" });
    expect(v[2]).toMatchObject({ school: "" });
  });

  test("Rails-style nested names number the rows: Citadel's profile[education][new0][school] rows take one entry each", async () => {
    // Citadel's own ATS: every row repeats the same ids, the label's for names the field's name, and only the name carries the row.
    const row = (n: number) => `<div class="field-group"><div class="form-group"><label for="profile[education][new${n}][school]">College / University / School</label>
      <input required type="text" name="profile[education][new${n}][school]" id="profile_education__school"></div>
      <div class="form-group"><label for="profile[education][new${n}][to]">Graduation Date</label>
      <input required type="date" name="profile[education][new${n}][to]" id="profile_education__to"></div></div>`;
    mount(`<h2>Education History</h2><fieldset class="repeatable-inputs education-inputs">${row(0)}${row(1)}</fieldset>`);
    const rows = rowsOf();
    expect(rows.map((r) => [r.entity, r.ordinal, r.controls.map((c) => c.field)])).toEqual([
      ["education", 0, ["school", "end"]],
      ["education", 1, ["school", "end"]],
    ]);
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const value = (name: string) => (document.querySelector(`[name="${name}"]`) as HTMLInputElement).value;
    expect([value("profile[education][new0][school]"), value("profile[education][new0][to]")]).toEqual(["Example University", "2017-05-01"]);
    expect([value("profile[education][new1][school]"), value("profile[education][new1][to]")]).toEqual(["Sample College", "2019-05-01"]);
  });

  test("Phenom numbers a row in brackets after the section's name (HPE's educationData[1].degree): each row's degree and field come from the entry its school names", async () => {
    const row = (n: number, school: string) => `<div><label for="educationData[${n}].schoolName">School or University</label><input id="educationData[${n}].schoolName" value="${school}">
      <label for="educationData[${n}].degree">Degree</label><select id="educationData[${n}].degree"><option value="">Select</option><option>Bachelor of Science</option><option>Master of Science</option></select>
      <label for="educationData[${n}].fieldOfStudy">Field of study</label><select id="educationData[${n}].fieldOfStudy"><option value="">Select</option><option>Computer Science</option><option>Data Science</option></select></div>`;
    mount(`<h2>Education</h2>${row(0, "Sample College")}${row(1, "Example University")}`);
    expect(rowsOf().map((r) => [r.entity, r.ordinal, r.controls.map((c) => c.field)])).toEqual([
      ["education", 0, ["school", "degree", "major"]],
      ["education", 1, ["school", "degree", "major"]],
    ]);
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const value = (id: string) => (document.getElementById(id) as HTMLSelectElement).value;
    expect([value("educationData[0].degree"), value("educationData[0].fieldOfStudy")]).toEqual(["Master of Science", "Data Science"]);
    expect([value("educationData[1].degree"), value("educationData[1].fieldOfStudy")]).toEqual(["Bachelor of Science", "Computer Science"]);
  });

  test("A row's question names its row for the matcher, which keeps one answer for every row out of it (HPE's field of study)", async () => {
    const row = (n: number, school: string) => `<div><label for="educationData[${n}].schoolName">School or University</label><input id="educationData[${n}].schoolName" value="${school}">
      <label for="educationData[${n}].fieldOfStudy">Field of study</label><select id="educationData[${n}].fieldOfStudy"><option value="">Select</option><option>Computer Science</option><option>Data Science</option></select></div>`;
    mount(`<h2>Education :</h2>${row(0, "Sample College")}${row(1, "Example University")}`);
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ALEX });
    const asked = host.matchRequests.flatMap((r) => r.questions).filter((q) => q.text === "Field of study");
    expect(asked.map((q) => q.section)).toEqual([
      ["Education", "Education 1"],
      ["Education", "Education 2"],
    ]);
  });

  test("A row's GPA box takes its own entry's grade average and its scale box the scale written with it (a bare grade of at most 4 is on the 4.0 scale), and a box the label names as a grade measure is never the degree its name holds (Citadel's degree_measure)", async () => {
    const row = (n: number) => `<div class="field-group"><div class="form-group"><label for="profile[education][new${n}][school]">College / University / School</label>
      <input required type="text" name="profile[education][new${n}][school]" id="profile_education__school"></div>
      <div class="form-group"><label for="profile[education][new${n}][degree_measure][value]">GPA</label>
      <input required type="text" name="profile[education][new${n}][degree_measure][value]" id="profile_education__degree_measure_value" aria-label="GPA"></div>
      <div class="form-group"><label for="profile[education][new${n}][degree_measure][highest_value]">GPA Scale</label>
      <select required name="profile[education][new${n}][degree_measure][highest_value]" id="profile_education__degree_measure_highest_value">
      <option value="">-Select -</option><option value="4.0">4.0</option><option value="5.0">5.0</option><option value="100">0-100</option></select></div></div>`;
    mount(`<h2>Education History</h2><fieldset class="repeatable-inputs education-inputs">${row(0)}${row(1)}</fieldset>`);
    expect(rowsOf().map((r) => r.controls.map((c) => c.field))).toEqual([
      ["school", "gpa", "gpaScale"],
      ["school", "gpa", "gpaScale"],
    ]);
    const graded: ApplicantSnapshot = { ...ALEX, history: { ...ALEX.history, education: [{ ...ALEX.history!.education![0], gradeAverage: "3.8" }, { ...ALEX.history!.education![1], gradeAverage: "3.4/4.0" }] } };
    await runFill(makeEngine(fakeHost()), { applicant: graded });
    const value = (name: string) => (document.querySelector(`[name="${name}"]`) as HTMLInputElement).value;
    expect([value("profile[education][new0][degree_measure][value]"), value("profile[education][new1][degree_measure][value]")]).toEqual(["3.8", "3.4"]);
    expect([value("profile[education][new0][degree_measure][highest_value]"), value("profile[education][new1][degree_measure][highest_value]")]).toEqual(["4.0", "4.0"]);
    // A bare grade above 4 may be on a 4.3 or a 5.0 scale: the box is left.
    mount(`<h2>Education History</h2><fieldset class="repeatable-inputs education-inputs">${row(0)}${row(1)}</fieldset>`);
    const high: ApplicantSnapshot = { ...ALEX, history: { ...ALEX.history, education: [{ ...ALEX.history!.education![0], gradeAverage: "4.2" }, { ...ALEX.history!.education![1], gradeAverage: "88/100" }] } };
    await runFill(makeEngine(fakeHost()), { applicant: high });
    expect([value("profile[education][new0][degree_measure][highest_value]"), value("profile[education][new1][degree_measure][highest_value]")]).toEqual(["", "100"]);
  });

  test("Dayforce rows keep every field with its own entry: the row index comes from the id and the per-entity form", async () => {
    mount("");
    const f = mountRows({ entity: "employment", style: "dayforce", rows: [{}, {}] });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const v = f.values();
    expect(v[0]).toMatchObject({ company: "Example Labs", title: "Staff Engineer" });
    expect(v[1]).toMatchObject({ company: "Sample Works", title: "Engineer" });
  });
});

describe("Row model", () => {
  test("Websites rows take the profile's links by position, distinct, and a parser duplicate is repaired", async () => {
    mount(`<h3>Websites 1</h3><label for="w1">URL</label><input id="w1" value="https://github.com/alex-example">
      <h3>Websites 2</h3><label for="w2">URL</label><input id="w2" value="https://github.com/alex-example">`);
    setUrl("https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply");
    await runFill(makeEngine(fakeHost()), { applicant: { candidate: {}, links: { linkedin: "https://www.linkedin.com/in/alex-example", github: "https://github.com/alex-example" } } });
    expect((document.getElementById("w1") as HTMLInputElement).value).toBe("https://www.linkedin.com/in/alex-example");
    expect((document.getElementById("w2") as HTMLInputElement).value).toBe("https://github.com/alex-example");
  });

  test("A pair already holding both of the applicant's addresses in some order is left alone", async () => {
    mount(`<h3>Websites 1</h3><label for="w1">URL</label><input id="w1" value="https://github.com/alex-example">
      <h3>Websites 2</h3><label for="w2">URL</label><input id="w2" value="https://www.linkedin.com/in/alex-example">`);
    setUrl("https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply");
    await runFill(makeEngine(fakeHost()), { applicant: { candidate: {}, links: { linkedin: "https://www.linkedin.com/in/alex-example", github: "https://github.com/alex-example" } } });
    expect((document.getElementById("w1") as HTMLInputElement).value).toBe("https://github.com/alex-example");
    expect((document.getElementById("w2") as HTMLInputElement).value).toBe("https://www.linkedin.com/in/alex-example");
  });

  test("Binding: the school a row shows outranks its position, a second row for a claimed entry is a duplicate, and a row past the last entry has none", () => {
    mount("");
    mountRows({ entity: "education", style: "dayforce", rows: [{ school: "Sample College" }, { school: "Sample College" }, { school: "" }, { school: "" }] });
    // (the fixture numbers its rows one-based; a row past the last entry has none)
    const rows = rowsOf();
    readRows(rows, readInfo);
    const b = bindRows(rows, ALEX.history!.education!, "education");
    expect(b.map((x) => (x.entry ? (x.entry.school as string) : x.why))).toEqual(["Sample College", "duplicate", "Example University", "beyond"]);
  });

  test("Two roles at one employer are two entries: each row takes the entry whose title and start year it shows, and only a row past both is a duplicate", () => {
    const TWO = [
      { employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true },
      { employer: "Example Labs", title: "Engineer", startDate: "2017-06-01", endDate: "2021-02-01" },
    ];
    mount("");
    mountRows({ entity: "employment", style: "workday", rows: [{ company: "Example Labs", title: "Engineer", start: "2017-06-01" }, { company: "Example Labs", title: "Staff Engineer", start: "2021-03-01" }, { company: "Example Labs", title: "Engineer", start: "2017-06-01" }] });
    const rows = rowsOf();
    readRows(rows, readInfo);
    expect(bindRows(rows, TWO, "employment").map((x) => (x.entry ? (x.entry.title as string) : x.why))).toEqual(["Engineer", "Staff Engineer", "duplicate"]);
    // Rows that show the employer alone take its entries in order.
    mount("");
    mountRows({ entity: "employment", style: "workday", rows: [{ company: "Example Labs" }, { company: "Example Labs" }] });
    const bare = rowsOf();
    readRows(bare, readInfo);
    expect(bindRows(bare, TWO, "employment").map((x) => (x.entry ? (x.entry.title as string) : x.why))).toEqual(["Staff Engineer", "Engineer"]);
  });

  test("A parser host keeps both rows of two roles at one employer: neither is removed and each holds its own entry", async () => {
    setUrl("https://career5.successfactors.com/career?company=example");
    mount("");
    const f = mountRows({ entity: "employment", style: "workday", rows: [] });
    parseOnUpload(f, { company: "Example Labs", title: "Staff Engineer", start: "2021-03-01" });
    document.getElementById("cv")?.addEventListener("change", () => f.addRow({ company: "Example Labs", title: "Engineer", start: "2017-06-01" }));
    const applicant: ApplicantSnapshot = { ...ALEX, history: { employment: [ALEX.history!.employment![0]!, { employer: "Example Labs", title: "Engineer", startDate: "2017-06-01", endDate: "2021-02-01" }] } };
    await runFill(makeEngine(fakeHost()), { applicant });
    expect(f.removed()).toEqual([]);
    expect(f.values().map((v) => [v.company, v.title, v.end])).toEqual([["Example Labs", "Staff Engineer", ""], ["Example Labs", "Engineer", "2021-02-01"]]);
  });

  test("An employer row's phone, supervisor and email boxes are not the employer's name (Paylocity wrote the company name into Company Phone Number)", () => {
    mount(`<div><label for="c1">Company Name</label><input id="c1" name="workHistory.companyName.0">
      <label for="c2">Company Phone Number</label><input id="c2" name="workHistory.companyPhone.0">
      <label for="c3">Supervisor Name</label><input id="c3" name="workHistory.supervisor.0">
      <label for="c4">Position</label><input id="c4" name="workHistory.position.0"></div>`);
    const by = (id: string) => fieldOf("employment", scanControls(document).find((i) => i.el.id === id)!);
    expect([by("c1"), by("c2"), by("c3"), by("c4")]).toEqual(["company", "other", "other", "title"]);
  });

  test("A combined date needs a stated day: a month and year give the whole-date box nothing, while split month and year boxes still resolve", () => {
    mount(`<label for="d">End date</label><input id="d"><label for="m">End Month</label><input id="m"><label for="y">End Year</label><input id="y">`);
    const [d, m, y] = scanControls(document);
    const entry = { school: "Example University", startDate: "2013-09", endDate: "2017-05" };
    expect(rowValue("education", "end", entry, d!)).toBeNull();
    expect(rowValue("education", "start", entry, d!)).toBeNull();
    expect(rowValue("education", "endMonth", entry, m!)).toBe("05");
    expect(rowValue("education", "endYear", entry, y!)).toBe("2017");
    expect(rowValue("education", "graduated", entry, d!)).toBe("Yes");
    // A day the entry states is used as it is.
    expect(rowValue("education", "end", { ...entry, endDate: "2017-05-14" }, d!)).toBe("2017-05-14");
  });

  test("An end month with no day is over once the next month has begun, not on the month's last day", () => {
    mount(`<label for="g">Did you graduate?</label><input id="g">`);
    const [g] = scanControls(document);
    const entry = { school: "Example University", endDate: "2030-05" };
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(new Date(2030, 4, 31, 12));
      expect(rowValue("education", "graduated", entry, g!)).toBe("No");
      vi.setSystemTime(new Date(2030, 5, 1, 0, 0, 1));
      expect(rowValue("education", "graduated", entry, g!)).toBe("Yes");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("Row removal", () => {
  const setup = (rows: Record<string, string>[], extra = {}) => {
    setUrl("https://career5.successfactors.com/career?company=example");
    mount("");
    return mountRows({ entity: "employment", style: "workday", rows, ...extra });
  };

  test("SuccessFactors parser junk: the parse adds a work row whose employer matches no profile entry; it is deleted and the profile entries are present once each", async () => {
    const f = setup([{ company: "Example Labs" }]);
    parseOnUpload(f, { company: "UNSPECIFIED" });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const v = f.values();
    expect(v.map((x) => x.company).sort()).toEqual(["Example Labs", "Sample Works"]);
    expect(f.removed()).toContain("UNSPECIFIED");
  });

  test("User-typed row kept: only the junk row the parse made is removed", () => {
    mount("");
    mountRows({ entity: "employment", style: "workday", rows: [{ company: "My Own Employer" }, { company: "Junk Project" }] });
    const rows = collectRows(scanControls(document), document);
    readRows(rows, readInfo);
    const start = new Map(rows.map((r) => [r.root, `${r.identity}|${r.detail}`]));
    // The parse changes the second row after the run began; the first was typed by the person.
    const second = rows[1]!;
    start.set(second.root, "was something else|");
    const bindings = bindRows(rows, ALEX.history!.employment!, "employment");
    const typed = new Set([rows[0]!.controls[0]!.info.selector]);
    const edits = { edited: (i: { selector: string }) => typed.has(i.selector) };
    expect(junkOf(edits as never, bindings, start).map((r) => r.identity)).toEqual(["Junk Project"]);
  });

  test("A row the run itself wrote is not the parse's: the page files the school the engine typed as its catalog's Other row, and the sweep leaves the row", async () => {
    setUrl("https://career5.successfactors.com/career?company=example");
    mount("");
    const f = mountRows({ entity: "education", style: "workday", rows: [{}] });
    const school = f.root.querySelector("input") as HTMLInputElement;
    // The catalog does not hold the school: once the fill has left the box, the page shows its own row for it.
    school.addEventListener("blur", () => school.value && (school.value = "Other"));
    const engine = makeEngine(fakeHost(), { ...FAST, sweepPollMs: 10, sweepIdleMs: 400, sweepCapMs: 800, restoreIdleMs: 800, restoreCapMs: 900 });
    await runFill(engine, { applicant: { ...ALEX, history: { education: [ALEX.history!.education![0]!] } } });
    await until(() => engine.log().some((l) => l.includes("restore end reason")), 4000);
    expect(school.value).toBe("Other");
    expect(f.removed()).toEqual([]);
    expect(f.values()).toHaveLength(1);
  });

  test("A row that was on the page before the run is not the parse's: it stays; a section with no profile entries is never touched; nothing is removed on a host without a parser", async () => {
    setup([{ company: "UNSPECIFIED" }, {}]);
    const noHistory = await runFill(makeEngine(fakeHost()), { applicant: { candidate: {}, history: { employment: [], education: [] } } });
    expect(noHistory.state).toBe("done");
    expect(document.querySelectorAll(".row").length).toBe(2);
    setUrl("about:blank");
    mount("");
    const f = mountRows({ entity: "employment", style: "workday", rows: [{ company: "Example Labs" }] });
    parseOnUpload(f, { company: "UNSPECIFIED" });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(f.removed()).toEqual([]);
  });

  test("A confirmation is answered only when its title names deleting a history, record or item", async () => {
    const f = setup([{ company: "Example Labs" }], { confirm: "Delete work history?" });
    parseOnUpload(f, { company: "UNSPECIFIED" });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(f.pressed).toContain("confirm");
    setUrl("about:blank");
    mount("");
    const g = mountRows({ entity: "employment", style: "workday", rows: [{ company: "Example Labs" }], confirm: "Submit your application?" });
    parseOnUpload(g, { company: "UNSPECIFIED" });
    setUrl("https://career5.successfactors.com/career?company=example");
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(g.pressed).not.toContain("confirm");
  });

  test("The remover guard: a delete control that names the application, or sits outside the row, is refused", () => {
    mount("");
    const f = mountRows({ entity: "employment", style: "workday", rows: [{ company: "A One" }, { company: "B Two" }] });
    const rows = collectRows(scanControls(document), document);
    const row = rows[0]!;
    const del = row.root.querySelector("button.del") as HTMLElement;
    expect(isUnsafeRemoval("Delete Work Experience 1", row.root, del)).toBe(false);
    expect(isUnsafeRemoval("Delete application", row.root, del)).toBe(true);
    expect(isUnsafeRemoval("Delete Work Experience 1", row.root, document.body)).toBe(true);
    expect(isUnsafeRemoval("Withdraw", row.root, del)).toBe(true);
    expect(isUnsafeRemoval("Delete", null, del)).toBe(true);
    void f;
  });

  test("A page-level Cancel and Next are never pressed, and a container that holds another row's controls offers no remover", async () => {
    // A junk row the parse adds is deleted by its own Delete while the page's Cancel and Next sit beside the rows.
    const f = setup([{ company: "Example Labs" }], { cancelButton: true });
    parseOnUpload(f, { company: "UNSPECIFIED" });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(f.removed()).toContain("UNSPECIFIED");
    expect(f.pressed.filter((p) => p === "cancel" || p === "next")).toEqual([]);
    // The row's wrapper also holds a control of another question and one Delete: pressing it could take that control too, so the row is offered none.
    mount(`<div id="all"><label for="a1">Company</label><input id="a1" name="employer_1_x"><label for="o">Other company you applied to</label><input id="o"><button class="del">Delete</button></div>`);
    const shared = collectRows(scanControls(document), document);
    expect(shared).toHaveLength(1);
    expect(shared[0]!.root.contains(document.getElementById("o"))).toBe(true);
    expect(removerFor(shared[0]!, shared, document)).toBeNull();
  });

  test("A numbered heading names a job row by whole words: an address, volunteer or coursework block is no employment row", () => {
    const under = (heading: string) => {
      mount(`<div><h4>${heading}</h4><label for="x1">Name</label><input id="x1"><label for="x2">City</label><input id="x2"></div>`);
      return rowsOf().map((r) => r.entity);
    };
    for (const h of ["Address History 1", "Volunteer Experience 2", "Military Service History 1", "Network 1", "Coursework 1"]) expect(under(h), h).toEqual([]);
    for (const h of ["Work Experience 1", "Employment History 2", "Previous Employers 1"]) expect(under(h), h).toEqual(["employment"]);
  });

  test("A committed card's Delete Record inside a lone row's widened root is not the row's remover", () => {
    const page = (editor: string) => {
      mount(`<section><h3>Work Experience</h3><ul><li><span>Example Labs</span><button type="button" class="card">Delete Record</button></li></ul>
        <div class="editor"><label for="jobPostingApplication_workHistory_0_company">Company</label><input id="jobPostingApplication_workHistory_0_company"><label for="jobPostingApplication_workHistory_0_title">Title</label><input id="jobPostingApplication_workHistory_0_title">${editor}</div></section>`);
      const rows = rowsOf();
      expect(rows).toHaveLength(1);
      expect(rows[0]!.root.querySelector(".card")).not.toBeNull();
      return removerFor(rows[0]!, rows, document);
    };
    expect(page("")).toBeNull();
    expect(page(`<button type="button" class="del">Delete Record</button>`)?.button.className).toBe("del");
  });

  test("Re-created row: the page makes a removed junk row again 20 ms later; it is removed again, and after three removals of the same row identity the engine stops", async () => {
    setUrl("https://career5.successfactors.com/career?company=example");
    mount("");
    const f = mountRows({ entity: "employment", style: "workday", rows: [{ company: "Example Labs" }, { company: "Sample Works" }], recreate: { row: { company: "UNSPECIFIED" }, times: 6, afterMs: 20 } });
    parseOnUpload(f, { company: "UNSPECIFIED" });
    const engine = makeEngine(fakeHost(), { ...FAST, sweepPollMs: 10, sweepIdleMs: 1500, sweepCapMs: 4000, restoreIdleMs: 4000, restoreCapMs: 5000 });
    engine.start({ applicant: ALEX });
    await until(() => engine.status().state === "done");
    await until(() => engine.log().some((l) => l.includes("abandoned")), 4000);
    // The initial removal plus the sweep's: the junk identity is removed three times in all by the sweep and then surrendered.
    expect(f.removed().filter((c) => c === "UNSPECIFIED").length).toBeLessThanOrEqual(4);
    expect(f.removed().filter((c) => c === "UNSPECIFIED").length).toBeGreaterThanOrEqual(3);
    expect(engine.log().some((l) => l.includes("abandoned"))).toBe(true);
  });

  test("Paylocity rows: two duplicate work rows with swapped dates leave one row whose dates equal the profile's, removed from the end by the section's single delete control", async () => {
    setUrl("https://recruiting.paylocity.com/recruiting/jobs/Apply/1/example");
    mount("");
    const f = mountRows({
      entity: "employment",
      style: "paylocity",
      rows: [{ company: "Example Labs", title: "Staff Engineer", start: "2021-02-01", end: "2017-06-01" }],
    });
    parseOnUpload(f, { company: "Example Labs", title: "Staff Engineer", start: "2017-06-01", end: "2021-02-01" });
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: { ...ALEX, history: { employment: [ALEX.history!.employment![0]!] } } }));
    expect(f.pressed).toContain("section-delete");
    expect(f.values()).toHaveLength(1);
    expect(f.values()[0]).toMatchObject({ company: "Example Labs", start: "2021-03-01" });
    expect(s.rows.some((r) => r.reason === "user-edited")).toBe(false);
  });
});

describe("Paylocity late parse", () => {
  test("Paylocity second pass: a row the parse appends after the reconcile is deleted from the end, and the profile row keeps the applicant's dates", async () => {
    setUrl("https://recruiting.paylocity.com/recruiting/jobs/Apply/1/example");
    mount("");
    const f = mountRows({ entity: "employment", style: "paylocity", rows: [{ company: "Example Labs", title: "Staff Engineer", start: "2021-02-01", end: "2017-06-01" }] });
    // The parse lands twice: one row at upload, another after the first reconcile has finished.
    parseOnUpload(f, { company: "Example Labs", title: "Staff Engineer", start: "2017-06-01", end: "2021-02-01" });
    // The second parse lands when the first reconcile has written the row's start date. The sweep runs inside the restore watch, so both outlast the slowest machine.
    let late = false;
    const onInput = () => {
      if (!late && f.values()[0]?.start === "2021-03-01") {
        late = true;
        f.addRow({ company: "Project Atlas", title: "Side Project", start: "2019-01-01", end: "2019-06-01" });
      }
    };
    document.addEventListener("input", onInput);
    const applicant: ApplicantSnapshot = { ...ALEX, history: { employment: [ALEX.history!.employment![0]!] } };
    await runFill(makeEngine(fakeHost(), { sweepIdleMs: 20_000, sweepCapMs: 40_000, restoreIdleMs: 20_000, restoreCapMs: 40_000 }), { applicant });
    await until(() => late && f.values().length === 1, 10_000);
    document.removeEventListener("input", onInput);
    expect(f.values()[0]).toMatchObject({ company: "Example Labs", start: "2021-03-01" });
    expect(f.pressed.filter((p) => p === "section-delete").length).toBeGreaterThanOrEqual(2);
  });
});

describe("Row address block", () => {
  test("Paylocity country morph: the country changes from the United States to China and the locality, administrative-area and postal-code fields are written and the zip field is not", async () => {
    setUrl("https://recruiting.paylocity.com/recruiting/jobs/Apply/1/example");
    mount("");
    const a = mountPaylocityAddress();
    const applicant: ApplicantSnapshot = { candidate: {}, history: { employment: [{ employer: "Example Labs", country: "China", city: "Shanghai", region: "Shanghai", postalCode: "200000" }] } };
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant }));
    expect(a.fields()).toEqual(["workHistory.0.locality", "workHistory.0.adminArea", "workHistory.0.postalCode"]);
    expect(a.value("workHistory.0.locality")).toBe("Shanghai");
    expect(a.value("workHistory.0.adminArea")).toBe("Shanghai");
    expect(a.value("workHistory.0.postalCode")).toBe("200000");
    expect(a.writes).not.toContain("workHistory.0.zip");
    expect(s.rows.filter((r) => r.reason === "control-detached")).toEqual([]);
  });
});

describe("Row address lines", () => {

  test("The row writes the split into its two address boxes", async () => {
    mount(`<h3>Work Experience 1</h3><label for="a1">Address line 1</label><input id="a1" maxlength="40"><label for="a2">Address line 2</label><input id="a2">`);
    const line1 = "1234 Example Boulevard Northwest Suite Number Four Hundred";
    await runFill(makeEngine(fakeHost()), { applicant: { candidate: {}, history: { employment: [{ employer: "Example Labs", address1: line1 }] } } });
    const a1 = (document.getElementById("a1") as HTMLInputElement).value;
    const a2 = (document.getElementById("a2") as HTMLInputElement).value;
    expect(a1.length).toBeLessThanOrEqual(40);
    expect(`${a1} ${a2}`.trim()).toBe(line1);
  });
});

describe("Row structure", () => {

  test("Numbered company boxes each open a row of a flat block (GoodLeap on Lever: Company 1, Title, Company 2, Title): each bare Title takes its own row's entry, and a question in the block is no row field", async () => {
    mount(`<div class="card"><h4>Employment</h4>
      <div><label for="c1">Company 1</label><input id="c1" name="cards[x][field0]"></div>
      <div><label for="t1">Title</label><input id="t1" name="cards[x][field1]"></div>
      <div><label for="y1">Years Worked</label><input id="y1" name="cards[x][field2]"></div>
      <div><label for="c2">Company 2</label><input id="c2" name="cards[x][field3]"></div>
      <div><label for="t2">Title</label><input id="t2" name="cards[x][field4]"></div>
      <div><label for="y2">Years Worked</label><input id="y2" name="cards[x][field5]"></div>
      <fieldset><legend>Are you legally eligible to work in the U.S.?</legend>
        <label><input type="radio" name="cards[x][field6]" value="Yes">Yes</label><label><input type="radio" name="cards[x][field6]" value="No">No</label></fieldset>
    </div>`);
    const rows = rowsOf();
    expect(rows.map((r) => [r.entity, r.ordinal, r.controls.map((c) => c.field)])).toEqual([
      ["employment", 0, ["company", "title"]],
      ["employment", 1, ["company", "title"]],
    ]);
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const v = (id: string) => (document.getElementById(id) as HTMLInputElement).value;
    expect([v("c1"), v("t1"), v("c2"), v("t2")]).toEqual(["Example Labs", "Staff Engineer", "Sample Works", "Engineer"]);
  });

  test("A work-experience row's location is the employer's city, never the home city", async () => {
    mount(`<h3>Work Experience 1</h3><label for="c">Company</label><input id="c"><label for="l">Location</label><input id="l"><label for="home">City</label><input id="home" autocomplete="address-level2">`);
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect((document.getElementById("l") as HTMLInputElement).value).toBe("Durham");
    expect((document.getElementById("home") as HTMLInputElement).value).toBe("Durham");
    mount(`<h3>Work Experience 2</h3><label for="c">Company</label><input id="c"><label for="l">Location</label><input id="l">`);
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect((document.getElementById("l") as HTMLInputElement).value).toBe("Raleigh");
  });

  test("A record that pairs one entry's degree with another's school is junk on a host that deletes such records, and its remover is used", async () => {
    setUrl("https://jobs.dayforcehcm.com/en-US/example/CANDIDATEPORTAL/jobs/1");
    mount("");
    const f = mountRows({ entity: "education", style: "dayforce", rows: [{ school: "Example University", degree: "Bachelor of Science", major: "Computer Science" }] });
    parseOnUpload(f, { school: "Example University", degree: "Wrong Degree", major: "Wrong Major" });
    await runFill(makeEngine(fakeHost()), { applicant: { ...ALEX, history: { education: [ALEX.history!.education![0]!] } } });
    expect(f.removed()).toContain("Example University");
    expect(f.values()).toHaveLength(1);
    expect(f.values()[0]).toMatchObject({ degree: "Bachelor of Science" });
  });

  test("Oracle: the address master is waited for after the country commits, and the cascade pass writes it", async () => {
    mount(`<label for="fn">First Name</label><input id="fn"><label for="ln">Last Name</label><input id="ln"><label for="co">Country</label><div class="cx-select-container"><input class="cx-select-input" id="co" name="country" role="combobox" aria-haspopup="grid" aria-controls="co-grid"><div id="co-grid" role="grid" class="cx-select-dropdown"></div></div><div id="block"></div>`);
    const input = document.getElementById("co") as HTMLInputElement;
    const grid = document.getElementById("co-grid") as HTMLElement;
    const all = ["United States Minor Outlying Islands", "United States"];
    const show = (rows: string[]) => {
      grid.classList.add("cx-select-dropdown--open");
      grid.innerHTML = rows.map((t) => `<div role="row"><div role="gridcell"><span class="cx-select__list-item--content">${t}</span></div></div>`).join("");
      grid.querySelectorAll("[role=gridcell]").forEach((c) => {
        let hovered = false;
        c.addEventListener("mouseover", () => (hovered = true));
        c.addEventListener("click", () => {
          if (!hovered) return;
          input.value = c.textContent ?? "";
          grid.classList.remove("cx-select-dropdown--open");
          // The block that depends on the country appears a moment after it commits.
          setTimeout(() => mountOracleAddress({ waves: [[5, ["1 Example Street, Durham, NC 27701"]]] }), 40);
        });
      });
    };
    input.addEventListener("mousedown", () => show([]));
    input.addEventListener("input", () => show(all.filter((x) => x.toLowerCase().includes(input.value.toLowerCase()))));
    const engine = makeEngine(fakeHost(), { ...FAST, oracleGateWaitMs: 600 });
    engine.start({ applicant: ALEX });
    await until(() => engine.status().state === "done");
    const rows = (engine.status() as { summary: { rows: { label: string; outcome: string; reason?: string }[] } }).summary.rows;
    expect(rows.find((r) => r.label === "Country")?.outcome).toBe("filled");
    // The master appeared only after the country committed, and the cascade pass picked its suggestion.
    expect(rows.find((r) => r.label === "Address Line 1")?.outcome).toBe("filled");
    expect((document.getElementById("addressLine1") as HTMLInputElement).value).toBe("1 Example Street, Durham, NC 27701");
  });
});

describe("Dayforce parse", () => {
  const ALEX2 = { ...ALEX, history: { employment: ALEX.history!.employment } };

  test("The records the parse makes long after its own settle signal are waited for: no Add is pressed against zero rows", async () => {
    setUrl("https://jobs.dayforcehcm.com/en-US/example/CANDIDATEPORTAL/jobs/1");
    mount("");
    const f = mountRows({ entity: "employment", style: "dayforce", rows: [] });
    f.root.insertAdjacentHTML("beforeend", '<label for="cv">Resume</label><input id="cv" type="file"><label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln">');
    document.getElementById("cv")!.addEventListener("change", () => {
      setTimeout(() => f.addRow({ company: "Example Labs", title: "Staff Engineer" }), 90);
      setTimeout(() => f.addRow({ company: "Sample Works", title: "Engineer" }), 140);
    });
    await runFill(makeEngine(fakeHost(), { ...FAST, dayforceQuietMs: 120, dayforceParseCapMs: 1500 }), { applicant: ALEX2 });
    expect(f.addClicks()).toBe(0);
    expect(f.values()).toHaveLength(2);
  });

});

describe("Paylocity Work History rows", () => {
  /** The field's name first and the zero-based row number last, as the page numbers them; `named: false` gives ids that say no row. */
  const pctyRow = (n: number, named: boolean) => {
    const id = (f: string) => (named ? `workHistory.${f}.${n}` : `blk${n}-${f}`);
    return `<div><label for="${id("companyName")}">Company Name</label><input id="${id("companyName")}">
      <label for="${id("position")}">Position</label><input id="${id("position")}">
      <label for="${id("responsibilities")}">Responsibilities</label><textarea id="${id("responsibilities")}"></textarea></div>`;
  };
  async function fillWithAdd(named: boolean): Promise<{ adds: number; values: string[] }> {
    setUrl("https://recruiting.paylocity.com/recruiting/jobs/Apply/1/example");
    mount(`<h3>Work History</h3><div id="rows">${pctyRow(0, named)}</div><button type="button" id="add">Add Work History</button><label for="em">Email</label><input id="em"><label for="pad">Referral code</label><input id="pad" value="R">`);
    let adds = 0;
    (document.getElementById("add") as HTMLElement).addEventListener("click", () => {
      adds++;
      (document.getElementById("rows") as HTMLElement).insertAdjacentHTML("beforeend", pctyRow(adds, named));
    });
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    const values = Array.from(document.querySelectorAll<HTMLInputElement>("#rows input")).map((i) => i.value);
    setUrl("about:blank");
    return { adds, values };
  }

  test("Rows named workHistory.field.N are rows of the section: one Add is pressed for two entries and both rows are filled", async () => {
    const r = await fillWithAdd(true);
    expect(r.adds).toBe(1);
    expect(r.values).toEqual(["Example Labs", "Staff Engineer", "Sample Works", "Engineer"]);
  });

  test("An Add that mounts controls the engine cannot place in a row is the last one pressed, however many controls it brings", async () => {
    const r = await fillWithAdd(false);
    expect(r.adds).toBe(1);
  });
});

describe("Workable history editors", () => {
  const editor = (id: string, label: string, required = false) =>
    `<div><label><span><span>${required ? "<strong>*</strong>" : ""}</span><span><span id="${id}_label"><strong>${label}</strong></span></span></span><div><div><input id="${id}" name="${id}" aria-labelledby="${id}_label" type="text" value=""${required ? ' required aria-required="true"' : ""}></div></div></label></div>`;
  const page = (adds: { count: number }) => {
    mount(`<form><div data-ui="education"><div><p id="education_label">Education&nbsp;<small>(Optional)</small></p><button type="button" data-ui="add-section" aria-label="Add Education">+ Add</button></div>
        <ul><li><div data-ui="editor">${editor("school", "School", true)}${editor("field_of_study", "Field of study")}${editor("degree", "Degree")}</div></li></ul></div>
      <div data-ui="experience"><div><p id="experience_label">Experience&nbsp;<small>(Optional)</small></p><button type="button" data-ui="add-section" aria-label="Add Experience">+ Add</button></div>
        <ul><li><div data-ui="editor">${editor("title", "Title", true)}${editor("company", "Company")}${editor("industry", "Industry")}
          <div><label><input id="current" type="checkbox"><span>I currently work here</span></label></div></div></li></ul></div></form>`);
    document.querySelectorAll('[data-ui="add-section"]').forEach((b) => b.addEventListener("click", () => adds.count++));
  };

  test("the editors the page opened are filled from the profile's history (the latest school, the first job), and no more editors are opened", async () => {
    setUrl("https://apply.workable.com/example/j/ABC123/apply/");
    const adds = { count: 0 };
    page(adds);
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ALEX }));
    const val = (id: string) => (document.getElementById(id) as HTMLInputElement).value;
    expect({ title: val("title"), company: val("company"), school: val("school"), major: val("field_of_study"), degree: val("degree") }).toEqual({
      title: "Staff Engineer",
      company: "Example Labs",
      school: "Sample College",
      major: "Data Science",
      degree: "Master of Science",
    });
    expect(adds.count).toBe(0);
    expect(s.rows.find((r) => r.label === "Title")).toMatchObject({ outcome: "filled" });
    // The caption a plain-text heading carries is the row's section, so no later lane treats the box as a question.
    expect(scanControls(document).find((i) => i.el.id === "title")?.section).toEqual(["Experience"]);
  });
});
