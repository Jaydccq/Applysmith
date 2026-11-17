// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Repeated rows are answered only by their own entry; Entity-row reconciliation safety; Drivers for application boards (Ashby).
import { describe, expect, test } from "vitest";
import type { ApplicantSnapshot } from "../src/protocol";
import { fakeHost, makeEngine, mount, runFill, setUrl, until } from "./helpers";


const PROFILE: ApplicantSnapshot = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example", email: "alex@example.com" },
  history: {
    education: [
      { school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" },
      { school: "Sample College", degree: "Master of Science", field: "Data Science", startDate: "2017-09-01", endDate: "2019-05-01" },
    ],
  },
};

interface Entry {
  school: string;
  degree: string;
  major: string;
}

interface Section {
  values: () => Entry[];
  addRow: (e: Partial<Entry>) => void;
  addClicks: () => number;
  removeClicks: () => number;
}

/** Ashby's education field: entries without numbered ids, each with its own Remove control and a shared Add control. */
function mountEducation(rows: Partial<Entry>[]): Section {
  mount(`<div data-field-path="_systemfield_education_history"><div class="entries"></div><button type="button" class="add">Add another</button></div>`);
  const holder = document.querySelector(".entries") as HTMLElement;
  let serial = 0;
  let adds = 0;
  let removes = 0;
  const addRow = (e: Partial<Entry>) => {
    const n = ++serial;
    const el = document.createElement("div");
    el.className = "entry";
    el.innerHTML = `<label for="s${n}">School</label><input id="s${n}" value="${e.school ?? ""}"><label for="d${n}">Degree</label><input id="d${n}" value="${e.degree ?? ""}">
      <label for="m${n}">Field of Study</label><input id="m${n}" value="${e.major ?? ""}"><button type="button" class="rm" aria-label="Remove education entry">Remove</button>`;
    el.querySelector(".rm")!.addEventListener("click", () => {
      removes++;
      el.remove();
    });
    holder.append(el);
  };
  for (const r of rows) addRow(r);
  document.querySelector(".add")!.addEventListener("click", () => {
    adds++;
    addRow({});
  });
  return {
    values: () => Array.from(holder.children).map((el) => ({ school: (el.querySelector('[id^="s"]') as HTMLInputElement).value, degree: (el.querySelector('[id^="d"]') as HTMLInputElement).value, major: (el.querySelector('[id^="m"]') as HTMLInputElement).value })),
    addRow,
    addClicks: () => adds,
    removeClicks: () => removes,
  };
}

const onAshby = (): void => setUrl("https://jobs.ashbyhq.com/example/00000000/application");

/** The page's résumé parse: when the file is chosen, it adds this entry a moment later. */
function parseOnUpload(s: Section, row: Partial<Entry>): void {
  const holder = document.createElement("div");
  holder.innerHTML = `<label for="cv">Resume</label><input id="cv" type="file">`;
  document.body.append(holder);
  holder.querySelector("input")!.addEventListener("change", () => setTimeout(() => s.addRow(row), 5));
}

describe("Ashby education entries", () => {
  test("Ashby education: a parser that lists the Master's entry first is bound by the school each entry shows, and an entry with no school falls back to its position", async () => {
    onAshby();
    const s = mountEducation([{ school: "Sample College", degree: "Old" }, { school: "Example University" }, {}]);
    await runFill(makeEngine(fakeHost()), { applicant: { ...PROFILE, history: { education: [...PROFILE.history!.education!, { school: "Third Institute", degree: "Diploma" }] } } });
    const v = s.values();
    expect(v[0]).toMatchObject({ school: "Sample College", degree: "Master of Science" });
    expect(v[1]).toMatchObject({ school: "Example University", degree: "Bachelor of Science" });
    expect(v[2]).toMatchObject({ school: "Third Institute", degree: "Diploma" });
  });

  test("Ashby education: surplus parser entries are deleted from the end and the entries the profile has are kept", async () => {
    onAshby();
    const s = mountEducation([{ school: "Example University" }, { school: "Sample College" }]);
    parseOnUpload(s, { school: "Project Atlas Final Report" });
    await runFill(makeEngine(fakeHost()), { applicant: PROFILE });
    expect(s.values().map((e) => e.school)).toEqual(["Example University", "Sample College"]);
    expect(s.removeClicks()).toBe(1);
  });

  test("Ashby education: with no education in the profile nothing is deleted and nothing is added", async () => {
    onAshby();
    const s = mountEducation([{ school: "Parsed College" }]);
    parseOnUpload(s, { school: "Parsed Institute" });
    await runFill(makeEngine(fakeHost()), { applicant: { candidate: PROFILE.candidate } });
    expect(s.values()).toHaveLength(2);
    expect([s.removeClicks(), s.addClicks()]).toEqual([0, 0]);
  });

  test("Ashby education: an entry that arrives after the fill is deleted and none is ever added", async () => {
    onAshby();
    const s = mountEducation([{ school: "Example University" }, { school: "Sample College" }]);
    // The parse lands again as soon as the last entry's degree is written.
    let late = false;
    const onInput = () => {
      if (!late && s.values()[1]?.degree !== "") {
        late = true;
        s.addRow({ school: "Late Parsed Academy" });
      }
    };
    document.addEventListener("input", onInput);
    // The sweep outlasts the slowest machine; the wait below is on the page, not on a clock.
    await runFill(makeEngine(fakeHost(), { sweepIdleMs: 20_000, sweepCapMs: 40_000 }), { applicant: PROFILE });
    await until(() => late && s.values().length === 2, 10_000);
    document.removeEventListener("input", onInput);
    expect(s.values().map((e) => e.school)).toEqual(["Example University", "Sample College"]);
    expect(s.addClicks()).toBe(0);
  });
});
