// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Job-site and application-form detection; Frame fan-out decisions; Widget drivers for custom controls;
// Profile-field pass; Question pass; Resilience to re-renders and user edits.
import { describe, expect, test } from "vitest";
import { detectApplication } from "../src/detect";
import { byId, fakeHost, makeEngine, mount, runFill, summaryOf, write } from "./helpers";
import { mountRows } from "./ats-fixtures";
import { mountReactSelect } from "./library-fixtures";

const ALEX = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example", email: "alex@example.com", phone: "+1 555 0100", address: { line1: "1 Example Street", city: "Durham", state: "NC", postalCode: "27701", country: "United States" } },
  history: {
    education: [
      { school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" },
      { school: "Sample College", degree: "Master of Science", field: "Data Science", startDate: "2017-09-01", endDate: "2019-05-01" },
    ],
  },
};

describe("Detection and summaries", () => {
  test("A form whose controls live only in open shadow roots is an application form", () => {
    mount(`<div id="host"></div>`);
    const root = byId<HTMLDivElement>("host").attachShadow({ mode: "open" });
    root.innerHTML = `<label for="a">Email</label><input id="a"><label for="b">Full name</label><input id="b"><label for="c">Phone</label><input id="c">`;
    expect(detectApplication(document)).toEqual({ application: true, controls: 3 });
  });

  test("A second pass over a complete form counts every control as prefilled and none as filled", async () => {
    mount(`<label for="fn">First name</label><input id="fn" value="Alex"><label for="ln">Last name</label><input id="ln" value="Example">
      <label for="em">Email</label><input id="em" value="alex@example.com">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ALEX }));
    expect(s.counts).toMatchObject({ filled: 0, prefilled: 3, failed: 0, requiredUnfilled: 0 });
    expect(s.rows.every((r) => r.outcome === "prefilled")).toBe(true);
  });

});

describe("Selectors and widgets", () => {

  test("A react-select pick whose option click never mounts the single value is failed and nothing is typed into the input", async () => {
    mount("");
    const rs = mountReactSelect({ inputId: "react-select-9-input", label: "Degree", options: ["Bachelor of Science"], clickable: false });
    const r = await write("react-select-9-input", "Bachelor of Science");
    expect(r.outcome).toBe("failed");
    expect(rs.input.value).toBe("");
    expect(rs.value()).toEqual([]);
  });

});

describe("Rows", () => {
  test("One profile entry and one form row: no Add control is pressed", async () => {
    mount("");
    const f = mountRows({ entity: "education", style: "workday", rows: [{}] });
    await runFill(makeEngine(fakeHost()), { applicant: { ...ALEX, history: { education: [ALEX.history.education[0]!] } } });
    expect(f.addClicks()).toBe(0);
    expect(f.values()).toHaveLength(1);
  });
});
