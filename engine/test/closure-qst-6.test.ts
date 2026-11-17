// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Profile-field pass (name parts, phones, narrative labels, signature dates, rows, standalone education, label disambiguation).
import { describe, expect, test } from "vitest";
import type { ApplicantSnapshot } from "../src/protocol";
import { ADA, byId, fakeHost, makeEngine, mount, PAD, rowFor, runFill, setUrl, summaryOf } from "./helpers";
import { mountRows } from "./ats-fixtures";

const fill = async (applicant: ApplicantSnapshot = ADA, host = fakeHost()) => {
  const engine = makeEngine(host);
  const s = summaryOf(await runFill(engine, { applicant }));
  // One run per page: the earlier run's repair passes must not keep working on the next page.
  engine.dispose();
  return s;
};

const HISTORY: ApplicantSnapshot = {
  ...ADA,
  history: {
    employment: [
      { employer: "Example Labs", title: "Staff Engineer", city: "Raleigh", startDate: "2021-03-01", current: true },
      { employer: "Sample Works", title: "Engineer", city: "Cary", startDate: "2017-06-01", endDate: "2021-02-01" },
    ],
    education: [
      { school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" },
      { school: "Sample College", degree: "Master of Science", field: "Data Science", startDate: "2017-09-01", endDate: "2019-05-01" },
    ],
  },
};

describe("Name parts", () => {
  test("composite First sub-label binds the first name, full-name control binds the full name", async () => {
    mount(`<fieldset><legend>Your name</legend><label for="f">First</label><input id="f"><label for="l">Last</label><input id="l"></fieldset>`);
    await fill();
    expect([byId("f").value, byId("l").value]).toEqual(["Ada", "Example"]);
    mount(`<label for="n">Full name</label><input id="n"><fieldset><legend>Name</legend><label for="f">First</label><input id="f"><label for="m">Middle</label><input id="m"><label for="l">Last</label><input id="l"></fieldset>`);
    await fill();
    // With separate parts on the page, no control takes the whole name; the middle box is left to the person.
    expect([byId("n").value, byId("f").value, byId("m").value, byId("l").value]).toEqual(["", "Ada", "", "Example"]);
    mount(`<label for="n">Full name</label><input id="n">${PAD}`);
    await fill();
    expect(byId("n").value).toBe("Ada Example");
    // A bare First outside a name group names nothing: it is not bound, and the matcher is asked as for any other question.
    mount(`<label for="x">First</label><input id="x"><label for="y">Last</label><input id="y">`);
    const host = fakeHost();
    await fill(ADA, host);
    expect([byId("x").value, byId("y").value]).toEqual(["", ""]);
    expect(host.matchRequests.flatMap((r) => r.questions.map((q) => q.text))).toEqual(expect.arrayContaining(["First", "Last"]));
    // Oracle's legal-name labels bind the parts; the middle name is not a legal name.
    mount(`<label for="lf">Legal First Name</label><input id="lf"><label for="ll">Legal Last Name</label><input id="ll"><label for="lm">Legal Middle Name</label><input id="lm">`);
    await fill();
    expect([byId("lf").value, byId("ll").value, byId("lm").value]).toEqual(["Ada", "Example", ""]);
  });
});

describe("Phones", () => {
  test("employer phone box stays empty, parser E.164 value in a split box is rewritten", async () => {
    setUrl("https://jobs.lever.co/example/00000000-0000-0000-0000-000000000000/apply");
    const split = (value: string) =>
      `<div class="iti iti--separate-dial-code"><div class="iti__selected-flag"><div class="iti__selected-dial-code">+1</div></div><label for="ph">Phone</label><input id="ph" type="tel" value="${value}"></div>
       <label for="ep">Employer phone</label><input id="ep" type="tel"><label for="rp">Reference phone</label><input id="rp" type="tel">`;
    const applicant = { ...ADA, candidate: { ...ADA.candidate, phone: "+1 919 555 0100" } };
    // The page's parser put the whole E.164 number into the national box: the national form replaces it.
    mount(split("+19195550100"));
    await fill(applicant);
    expect(byId("ph").value).toBe("9195550100");
    expect([byId("ep").value, byId("rp").value]).toEqual(["", ""]);
    // A box that already holds the right national number is left alone.
    mount(split("9195550100"));
    let writes = 0;
    byId("ph").addEventListener("input", () => writes++);
    const s = await fill(applicant);
    expect(byId("ph").value).toBe("9195550100");
    expect(writes).toBe(0);
    expect(rowFor(s, "Phone").outcome).toBe("prefilled");
  });
});

describe("Boxes that take no profile value", () => {
  test("an employer-name box takes the applicant's own employer, a supervisor's box does not", async () => {
    // Oracle's Experience editor asks "Employer Name" outside any numbered row.
    mount(`<label for="en">Employer Name</label><input id="en"><label for="pe">Previous Employer</label><input id="pe">
      <label for="sn">Supervisor name</label><input id="sn">
      <label for="cq">What is your current or most recent employer?</label><input id="cq">
      <label for="rq">What is your reference's employer?</label><input id="rq">
      <label for="wq">Who is your current or previous employer?</label><input id="wq">
      <label for="nq">What is the name of your current or most recent employer?</label><input id="nq">
      <label for="wr">Who is your reference's employer?</label><input id="wr">`);
    const host = fakeHost({
      "employer name": { outcome: "filled", value: "Example Labs" },
      "previous employer": { outcome: "filled", value: "Example Labs" },
      "supervisor name": { outcome: "filled", value: "Example Labs" },
      "what is your current or most recent employer?": { outcome: "filled", value: "Example Labs" },
      "what is your reference's employer?": { outcome: "filled", value: "Example Labs" },
      "who is your current or previous employer?": { outcome: "filled", value: "Example Labs" },
      "what is the name of your current or most recent employer?": { outcome: "filled", value: "Example Labs" },
      "who is your reference's employer?": { outcome: "filled", value: "Example Labs" },
    });
    await fill(HISTORY, host);
    expect([byId("en").value, byId("pe").value, byId("sn").value]).toEqual(["Example Labs", "Example Labs", ""]);
    // Empower asks it as a question; a question about someone else's employer still gets nothing of the applicant's.
    expect([byId("cq").value, byId("rq").value]).toEqual(["Example Labs", ""]);
    // Stripe asks who, Squarespace asks for the name: both are the applicant's own employer.
    expect([byId("wq").value, byId("nq").value, byId("wr").value]).toEqual(["Example Labs", "Example Labs", ""]);
  });
});

describe("Label disambiguation", () => {
  test("Email address label never receives the street address", async () => {
    mount(`<label for="em">Email address</label><input id="em" name="address">
      <label for="a1">Address Line 1</label><input id="a1" name="email">
      <label for="a2">Address 2</label><input id="a2"><label for="z4">Zip+4</label><input id="z4"><label for="zip">Zip code</label><input id="zip">
      <h3>Work Experience 1</h3><label for="co">Company</label><input id="co"><label for="loc">Location</label><input id="loc"><label for="sk">Skills</label><input id="sk">`);
    const applicant = { ...HISTORY, candidate: { ...HISTORY.candidate, address: { ...HISTORY.candidate.address, line2: "Suite 4" } } };
    await fill(applicant);
    expect(byId("em").value).toBe("ada@example.com");
    expect(byId("a1").value).toBe("1 Example Street");
    expect(byId("a2").value).toBe("Suite 4");
    expect([byId("z4").value, byId("zip").value]).toEqual(["", "27701"]);
    // An employer row's Location is that employer's city, never the home city, and a Skills box is not a profile field.
    expect(byId("loc").value).toBe("Raleigh");
    expect(byId("sk").value).toBe("");
  });
});

describe("Education and experience rows", () => {
  test("typed school on ADP is kept, empty row fills", async () => {
    setUrl("https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html");
    mount("");
    const f = mountRows({ entity: "education", style: "adp", rows: [{ school: "Typed College", degree: "Typed Degree" }, {}] });
    await fill(HISTORY);
    expect(f.values()[0]).toMatchObject({ school: "Typed College", degree: "Typed Degree" });
    expect(f.values()[1]).toMatchObject({ school: "Sample College" });
    // The page is not recognised as Workday by the words of its labels.
    mount("");
    const g = mountRows({ entity: "education", style: "workday", rows: [{ school: "Typed College" }, {}] });
    setUrl("https://careers.example.org/apply");
    await fill(HISTORY);
    expect(g.values()[0]?.school).toBe("Typed College");
  });

  test("two employer rows with no reliable binding are both left blank", async () => {
    mount(`<h3>Experience</h3><label for="a">Job title</label><input id="a"><label for="b">Company</label><input id="b">
      <h3>Experience</h3><label for="c">Job title</label><input id="c"><label for="d">Company</label><input id="d">`);
    const s = await fill(HISTORY);
    // Neither entry is smeared over rows the page gives no evidence for.
    expect(["a", "b", "c", "d"].map((id) => byId(id).value)).toEqual(["", "", "", ""]);
    expect(s.rows.filter((r) => r.outcome === "filled")).toEqual([]);
  });

  test("a standalone education box the matcher misses is filled from the education entry", async () => {
    // Oracle's education editor: every box of the section is the matcher's, and its Major is a bare text box.
    mount(`<fieldset><legend>Education</legend><label for="s">School</label><input id="s"><label for="m">Major</label><input id="m"></fieldset>`);
    const host = fakeHost({ major: { outcome: "abstained", reason: "input-mismatch" } });
    await fill(HISTORY, host);
    expect(byId("m").value).toBe("Data Science");
  });

  test("standalone education block takes the latest entry by end date", async () => {
    mount(`<label for="s">School</label><input id="s"><label for="dg">Degree</label><input id="dg"><label for="m">Field of study</label><input id="m"><label for="e">Graduation date</label><input id="e" type="date">`);
    // The list order is not the order of the dates.
    const applicant = { ...HISTORY, history: { education: [...HISTORY.history!.education!].reverse() } };
    await fill(applicant);
    expect(["s", "dg", "m", "e"].map((id) => byId(id).value)).toEqual(["Sample College", "Master of Science", "Data Science", "2019-05-01"]);
    // A school the page already shows keeps its own entry; a school that is on no entry is left alone with its neighbours.
    mount(`<label for="s">School</label><input id="s" value="Example University"><label for="dg">Degree</label><input id="dg"><label for="m">Field of study</label><input id="m">`);
    await fill(applicant);
    expect(["s", "dg", "m"].map((id) => byId(id).value)).toEqual(["Example University", "Bachelor of Science", "Computer Science"]);
    mount(`<label for="s">School</label><input id="s" value="Typed College"><label for="dg">Degree</label><input id="dg">`);
    await fill(applicant);
    expect(["s", "dg"].map((id) => byId(id).value)).toEqual(["Typed College", ""]);
  });
});
