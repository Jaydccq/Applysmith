// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Repeated rows are answered only by their own entry; Drivers for HR-suite application systems.
import { describe, expect, test } from "vitest";
import type { ApplicantSnapshot } from "../src/protocol";
import { mountRows } from "./ats-fixtures";
import { byId, fakeHost, makeEngine, mount, PAD, runFill, setUrl, summaryOf } from "./helpers";

const WORKDAY = "https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply";
const DAYFORCE = "https://jobs.dayforcehcm.com/en-US/example/CANDIDATEPORTAL/jobs/1";

const ALEX: ApplicantSnapshot = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example", email: "alex@example.com", phone: "+1 555 0100", address: { line1: "1 Example Street", city: "Durham", state: "NC", postalCode: "27701", country: "United States" } },
  history: {
    employment: [
      { employer: "Example Labs", title: "Staff Engineer", location: "Durham", startDate: "2021-03-01", current: true },
      { employer: "Sample Works", title: "Engineer", location: "Raleigh", startDate: "2017-06-01", endDate: "2021-02-01" },
    ],
  },
};

describe("Rows after the fill", () => {

  test("A work row that repeats an earlier row when the fill is done is deleted before the summary, and its controls are not on it", async () => {
    // Workday Verizon: two rows with the same title, company and dates for a profile holding one experience.
    setUrl(WORKDAY);
    mount("");
    const same = { company: "Example Labs", title: "Staff Engineer", start: "2021-03-01" };
    const f = mountRows({ entity: "employment", style: "workday", rows: [same, same] });
    const one: ApplicantSnapshot = { ...ALEX, history: { employment: [ALEX.history!.employment![0]!] } };
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: one }));
    expect(f.values().map((v) => v.company)).toEqual(["Example Labs"]);
    expect(s.rows.filter((r) => r.selector.includes("workExperience-2--"))).toEqual([]);
  });

  test("Committed Dayforce cards count toward the rows: two cards for two entries press no Add", async () => {
    setUrl(DAYFORCE);
    mount(`<h3>Employment History</h3><button type="button" id="add">Add Employment History</button>
      <div class="card"><span>Example Labs</span><button type="button">Delete Record</button></div>
      <div class="card"><span>Sample Works</span><button type="button">Delete Record</button></div>
      <button type="button" id="cancel">Cancel</button><button type="button" id="next">Next</button>${PAD}<label for="em">Email</label><input id="em">`);
    const pressed: string[] = [];
    for (const id of ["add", "cancel", "next"]) byId(id).addEventListener("click", () => pressed.push(id));
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(pressed).toEqual([]);
  });

  test("Cards count only for their own section: two employment cards do not stand in for two education entries, whose Add is pressed", async () => {
    setUrl(DAYFORCE);
    mount(`<h3>Employment History</h3><button type="button" id="add">Add Employment History</button>
      <div class="card"><span>Example Labs</span><button type="button">Delete Record</button></div>
      <div class="card"><span>Sample Works</span><button type="button">Delete Record</button></div>
      <h3>Education History</h3><button type="button" id="addEdu">Add Education History</button>${PAD}<label for="em">Email</label><input id="em">`);
    const pressed: string[] = [];
    for (const id of ["add", "addEdu"]) byId(id).addEventListener("click", () => pressed.push(id));
    const education = [{ school: "Example University", degree: "Bachelor of Science" }, { school: "Sample College", degree: "Master of Science" }];
    await runFill(makeEngine(fakeHost()), { applicant: { ...ALEX, history: { ...ALEX.history, education } } });
    expect(pressed).toEqual(["addEdu"]);
  });

  test("An Add that mounted no row the engine reads is not pressed again by the next run on the same page", async () => {
    setUrl(DAYFORCE);
    mount(`<h3>Work Experience</h3><div id="blanks"></div><button type="button" id="add">Add Work Experience</button>${PAD}<label for="em">Email</label><input id="em">`);
    byId("add").addEventListener("click", () => byId("blanks").append(document.createElement("hr")));
    const engine = makeEngine(fakeHost());
    await runFill(engine, { applicant: ALEX });
    await runFill(engine, { applicant: ALEX });
    expect(byId("blanks").children).toHaveLength(1);
  });

  test("In a kept Dayforce row the profile's end date replaces the parser's and the bank's availability never answers the row's start date", async () => {
    setUrl(DAYFORCE);
    mount("");
    const f = mountRows({ entity: "employment", style: "dayforce", rows: [{ company: "Example Labs", title: "Staff Engineer", start: "2021-03-01" }, { company: "Sample Works", title: "Engineer", start: "2017-06-01", end: "2021-02-28" }] });
    const host = fakeHost({ "start date": { outcome: "filled", value: "2031-03-04" }, from: { outcome: "filled", value: "2031-03-04" } });
    await runFill(makeEngine(host), { applicant: ALEX });
    const [first, second] = f.values();
    expect(second).toMatchObject({ company: "Sample Works", end: "2021-02-01", start: "2017-06-01" });
    expect(first?.start).toBe("2021-03-01");
  });
});

describe("Identity binding", () => {
  test("A City box never takes the name from a look-alike autocomplete token, and a postal box never takes the city", async () => {
    mount(`<label for="c">City</label><input id="c" autocomplete="xfamily-name"><label for="p">Postal Code</label><input id="p" autocomplete="address-level2">${PAD}`);
    await runFill(makeEngine(fakeHost()));
    expect(byId("c").value).toBe("Durham");
    expect(byId("p").value).not.toBe("Durham");
  });
});
