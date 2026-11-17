// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for HR-suite application systems (row cleanup); Entity-row reconciliation safety.
import { describe, expect, test } from "vitest";
import type { ApplicantSnapshot } from "../src/protocol";
import { fakeHost, makeEngine, mount, runFill, setUrl } from "./helpers";
import { mountRows } from "./ats-fixtures";


const APPLICANT: ApplicantSnapshot = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example" },
  history: {
    employment: [
      { employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true },
      { employer: "Sample Works", title: "Engineer", startDate: "2017-06-01", endDate: "2021-02-01" },
    ],
  },
};

/** The page's parse: once a file is chosen it adds these rows. */
function parseOnUpload(f: { addRow: (v: Record<string, string>) => void }, rows: Record<string, string>[]): void {
  const holder = document.createElement("div");
  holder.innerHTML = `<label for="cv">Resume</label><input id="cv" type="file">`;
  document.body.append(holder);
  holder.querySelector("input")!.addEventListener("change", () => rows.forEach((r) => f.addRow(r)));
}

describe("UltiPro work experience", () => {
  test("A parsed row whose organization names no employer, or repeats one already claimed, is removed through its own Delete button with no confirmation; a blank template stays", async () => {
    setUrl("https://recruiting.ultipro.com/EXA1000/JobBoard/abc/OpportunityApply");
    mount("");
    const f = mountRows({ entity: "employment", style: "workday", rows: [{ company: "Example Labs" }] });
    parseOnUpload(f, [{ company: "Personal Project Alpha" }, {}, { company: "Example Labs Holdings" }]);
    await runFill(makeEngine(fakeHost()), { applicant: APPLICANT });
    expect(f.removed().sort()).toEqual(["Example Labs Holdings", "Personal Project Alpha"]);
    expect(f.pressed.filter((p) => p === "confirm")).toEqual([]);
    expect(f.values()).toHaveLength(2);
    expect(f.values()[0]).toMatchObject({ company: "Example Labs", title: "Staff Engineer" });
  });

  test("Without employers in the profile no row is removed", async () => {
    setUrl("https://recruiting.ultipro.com/EXA1000/JobBoard/abc/OpportunityApply");
    mount("");
    const f = mountRows({ entity: "employment", style: "workday", rows: [{ company: "Example Labs" }] });
    parseOnUpload(f, [{ company: "Personal Project Alpha" }]);
    await runFill(makeEngine(fakeHost()), { applicant: { candidate: APPLICANT.candidate, history: { employment: [] } } });
    expect(f.removed()).toEqual([]);
    expect(f.values()).toHaveLength(2);
  });
});
