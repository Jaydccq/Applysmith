// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for enterprise application suites; Drivers for HR-suite application systems; Drivers for
// widget libraries; Profile-field pass; Repeated rows are answered only by their own entry; Writes by label;
// Question text, required flag and selector resolution.
import { describe, expect, test } from "vitest";
import { collapse, stripSchoolingGloss } from "../src/dom";
import { committedDisplay } from "../src/committed";
import { detectApplication } from "../src/detect";
import { cleanOptionLabel } from "../src/popup";
import { resolveSelector } from "../src/selector";
import { readInfo, scanControls } from "../src/scan";
import { scoreOption } from "../src/writers";
import type { ApplicantSnapshot } from "../src/protocol";
import { ADA, byId, fakeHost, infoOf, makeEngine, mount, runFill, setUrl, summaryOf, write } from "./helpers";

describe("Controls that ask nothing", () => {
  test("A text-less button inside a combobox is no control, so a required mark on it leaves no required leftover", async () => {
    mount(`<label for="fn">First name *</label><input id="fn" required>
      <div id="w" role="combobox" aria-label="Team *" aria-required="true" aria-haspopup="listbox"><input id="team" value="Platform" aria-required="true">
        <button id="x" type="button" aria-haspopup="listbox" aria-required="true"></button><span id="chev" role="button" aria-haspopup="listbox"></span></div>`);
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));
    expect(s.rows.filter((r) => r.label === "")).toEqual([]);
    expect(s.counts.requiredUnfilled).toBe(0);
    expect(s.rows.filter((r) => r.label === "Team")).toHaveLength(1);
    // A button that names itself is still a trigger.
    mount(`<div role="combobox" aria-label="Team"><button id="own" type="button" aria-haspopup="listbox">Pick a team</button></div>`);
    expect(scanControls(document).map((i) => i.el.id)).toContain("own");
  });

  test("A dialog that an ancestor hides is not open, so an aria-hidden form behind it is not blocked by it", () => {
    const form = `<div aria-hidden="true"><label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b"></div>`;
    mount(`${form}<div role="dialog"><p>Sign up to continue</p></div>`);
    expect(detectApplication(document)).toEqual({ application: false, reason: "blocked-by-dialog" });
    // With no dialog open, aria-hidden is the site's noise: the form is the application.
    mount(`${form}<div style="display:none"><div role="dialog"><p>Sign up to continue</p></div></div>`);
    expect(detectApplication(document)).toEqual({ application: true, controls: 2 });
    mount(`${form}<dialog><p>Sign up to continue</p></dialog>`);
    expect(detectApplication(document)).toEqual({ application: true, controls: 2 });
    mount(`${form}<dialog open><p>Sign up to continue</p></dialog>`);
    expect(detectApplication(document)).toEqual({ application: false, reason: "blocked-by-dialog" });
  });
});

describe("Option labels", () => {
  test("A school list's leading article is no difference: Greenhouse's strict react-select takes \"The University of Westbridge\" (10a Labs)", () => {
    expect(scoreOption("The University of Westbridge", "University of Westbridge")).toBe(3);
    expect(scoreOption("Ohio State University", "The Ohio State University")).toBe(3);
    expect(scoreOption("University of Westbridge Harbor Campus", "University of Westbridge")).toBe(2);
    expect(scoreOption("Theological Seminary", "Seminary")).toBe(2);
  });

  test("A degree option's schooling-length gloss is dropped from its label and from the comparison; an experience bucket keeps its parenthesis", async () => {
    expect(cleanOptionLabel("Bachelor's Degree (4 years)")).toBe("Bachelor's Degree");
    expect(cleanOptionLabel("Associate Degree (Two-Year), selected")).toBe("Associate Degree");
    expect(cleanOptionLabel("Master's Degree (1-2 years full time)")).toBe("Master's Degree");
    expect(stripSchoolingGloss("Intermediate (3-5 years)")).toBe("Intermediate (3-5 years)");
    expect(stripSchoolingGloss("(4 years)")).toBe("(4 years)");
    expect(scoreOption("Bachelor's Degree (4 years)", "Bachelor's Degree")).toBe(3);
    expect(scoreOption("Bachelor's Degree (4 years)", "Bachelor's Degree (4 yrs)")).toBe(3);
    expect(scoreOption("Master's Degree (2 years)", "Bachelor's Degree")).toBe(0);
    mount(`<label for="d">Highest degree</label><select id="d"><option value="">Select</option><option value="b">Bachelor's Degree (4 years)</option><option value="m">Master's Degree (2 years)</option></select>`);
    expect(await write("d", "Master's Degree")).toMatchObject({ outcome: "filled", optionLabel: "Master's Degree (2 years)" });
    expect(byId<HTMLSelectElement>("d").value).toBe("m");
  });

  test("An abbreviated month and its full name are the same option", async () => {
    const MON = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    const FULL = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
    const page = (names: string[]) =>
      `<label for="m">Start month</label><select id="m"><option value="">Select</option>${names.map((n, i) => `<option value="${i + 1}">${n}</option>`).join("")}</select>`;
    for (const [names, answer, want] of [
      [MON, "September", "Sep"],
      [FULL, "Sep", "September"],
      [FULL, "Sept", "September"],
      [MON, "sep", "Sep"],
    ] as const) {
      mount(page([...names]));
      expect(await write("m", answer)).toMatchObject({ outcome: "filled", optionLabel: want });
    }
    // Three letters of another word are not a month, and a different month is never taken.
    expect(scoreOption("Marketing", "Mar")).toBe(0);
    expect(scoreOption("Mar", "May")).toBe(0);
    expect(scoreOption("June", "Jul")).toBe(0);
  });

  test("A bare Y and N radio pair takes Yes and No", async () => {
    const pair = `<fieldset><legend>Are you authorized to work here?</legend><label><input type="radio" name="q" id="y" value="1"> Y</label><label><input type="radio" name="q" id="n" value="0"> N</label></fieldset>`;
    mount(pair);
    expect(await write("y", "Yes")).toMatchObject({ outcome: "filled" });
    expect([byId("y").checked, byId("n").checked]).toEqual([true, false]);
    mount(pair);
    expect(await write("y", "No")).toMatchObject({ outcome: "filled" });
    expect([byId("y").checked, byId("n").checked]).toEqual([false, true]);
    expect(scoreOption("Y", "Yes")).toBe(2);
    expect(scoreOption("Y", "No")).toBe(0);
    expect(scoreOption("N", "no")).toBe(2);
  });
});

describe("Selector identity", () => {
  test("An id that two controls carry is no anchor: each copy gets its own selector that resolves to itself", () => {
    mount(`<div id="stale" style="display:none"><label for="dup">Favourite thing</label><input id="dup"></div>
      <div id="shown"><label for="dup">Favourite thing</label><input id="dup"></div><label for="solo">Solo</label><input id="solo">`);
    const [first, second] = Array.from(document.querySelectorAll<HTMLInputElement>("input[id=dup]"));
    const dups = [first, second].map((el) => {
      const sel = scanControls(document, { inventory: true }).find((i) => i.el === el)?.selector ?? "";
      return sel;
    });
    expect(dups[1]).not.toBe("");
    expect(dups[1]).not.toBe('[id="dup"]');
    expect(resolveSelector(document, dups[1] as string)).toBe(second);
    expect(scanControls(document).find((i) => i.el.id === "solo")?.selector).toBe('[id="solo"]');
  });
});

describe("Select2 chips", () => {
  const multi = (chips: string[]) => `<label for="skills">Skills</label>
    <select id="skills" multiple class="select2-hidden-accessible" data-select2-id="skills">${chips.map((c) => `<option selected>${c}</option>`).join("")}<option>Zig</option></select>
    <span class="select2 select2-container"><span class="selection"><span class="select2-selection select2-selection--multiple" role="combobox" aria-expanded="false">
      <ul class="select2-selection__rendered">${chips.map((c) => `<li class="select2-selection__choice" title="${c}"><span class="select2-selection__choice__remove" role="presentation">×</span>${c}</li>`).join("")}<li class="select2-search select2-search--inline"><input class="select2-search__field" type="search" role="searchbox" autocomplete="off"></li></ul>
    </span></span></span>`;

  test("A multi-select Select2 is one control whose value is one entry per chip, without the remove mark, and its inline search box asks nothing", () => {
    mount(multi(["Go", "Rust"]));
    const infos = scanControls(document);
    expect(infos).toHaveLength(1);
    const info = infos[0]!;
    expect(info.driver).toBe("select2");
    expect(info.label.text).toBe("Skills");
    expect(committedDisplay(document.querySelector(".select2-selection") as Element)).toBe("Go, Rust");
    expect(readInfo(info)).toBe("Go, Rust");
    expect(info.answered).toBe(true);
    mount(multi([]));
    expect(scanControls(document).map((i) => [i.label.text, readInfo(i)])).toEqual([["Skills", ""]]);
  });

  test("A Select2 single select takes the question its card states, its drawn box no second field (Veeva's Lever university picker)", () => {
    mount(`<ul><li class="application-question custom-question"><div><div class="application-label full-width university"><div class="text">Please select your college or University:<span class="required">✱</span></div></div>
      <div class="application-field full-width required-field"><div class="application-university"><select id="uni" class="select2-hidden-accessible" tabindex="-1" aria-hidden="true" data-placeholder="If you are unable to find your college or university, please select &quot;Other&quot;"><option value=""></option><option>Example University</option><option>Other</option></select><span class="select2 select2-container select2-container--default" dir="ltr"><span class="selection"><span class="select2-selection select2-selection--single" role="combobox" aria-haspopup="true" aria-expanded="false" tabindex="0" aria-labelledby="select2-uni-container"><span class="select2-selection__rendered" id="select2-uni-container"><span class="select2-selection__placeholder">If you are unable to find your college or university, please select "Other"</span></span></span></span><span class="dropdown-wrapper" aria-hidden="true"></span></span></div></div></div></li></ul>`);
    const infos = scanControls(document);
    expect(infos.map((i) => [i.driver, i.label.text, i.required])).toEqual([["select2", "Please select your college or University:", true]]);
  });
});

describe("Question text", () => {
  test("A leading ** required marker is not part of the question and marks the control required", () => {
    mount(`<label for="a">** Will you now or in the future require sponsorship?</label><input id="a"><label for="b">Notes</label><input id="b">`);
    const info = infoOf("a");
    expect(info.label.text).toBe("Will you now or in the future require sponsorship?");
    expect(info.required).toBe(true);
    expect(collapse(info.label.text)).not.toContain("*");
  });
});

describe("Existing values the component holds", () => {
  function sdfHost(display: string, value?: string): HTMLElement & { value?: string } {
    mount(`<label for="sdf">Pronouns *</label><sdf-select-simple id="sdf"></sdf-select-simple><label for="n">Notes</label><input id="n">`);
    const host = byId("sdf") as unknown as HTMLElement & { value?: string };
    host.attachShadow({ mode: "open" }).innerHTML = `<button class="trigger-button" type="button">${display}</button><div class="pane"></div>`;
    if (value !== undefined) host.value = value;
    return host;
  }

  test("An sdf-select-simple is one control, listed with its shown option and its required flag", () => {
    sdfHost("He/Him/His");
    const infos = scanControls(document);
    expect(infos.map((i) => i.el.id)).toEqual(["sdf", "n"]);
    const host = infos[0]!;
    expect(host).toMatchObject({ driver: "sdf", kind: "custom-listbox", required: true, answered: true });
    expect(readInfo(host)).toBe("He/Him/His");
    sdfHost("Choose an option");
    const open = infoOf("sdf");
    expect(open).toMatchObject({ required: true, answered: false });
    expect(readInfo(open)).toBe("");
  });

  test("When the component's display shows nothing, the value it holds itself is read", () => {
    sdfHost("Choose an option", "He/Him/His");
    expect(readInfo(infoOf("sdf"))).toBe("He/Him/His");
    expect(infoOf("sdf").answered).toBe(true);
    sdfHost("", "She/Her/Hers");
    expect(readInfo(infoOf("sdf"))).toBe("She/Her/Hers");
    sdfHost("He/Him/His", "They/Them");
    expect(readInfo(infoOf("sdf"))).toBe("He/Him/His");
  });
});

describe("A component select names the stage it failed at", () => {
  function pane(labels: string[], onPick: (host: HTMLElement & { value?: string }, label: string, trigger: HTMLElement) => void): HTMLElement & { value?: string } {
    mount(`<label for="sdf">Pronouns</label><sdf-select-simple id="sdf"></sdf-select-simple>`);
    const host = byId("sdf") as unknown as HTMLElement & { value?: string };
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<button class="trigger-button" type="button">Choose an option</button><div class="pane"></div>`;
    const trigger = root.querySelector(".trigger-button") as HTMLElement;
    trigger.addEventListener("click", () => {
      if (!labels.length) return;
      const box = root.querySelector(".pane") as HTMLElement;
      box.innerHTML = labels.map((l) => `<sdf-select-item role="option">${l}</sdf-select-item>`).join("");
      box.querySelectorAll("sdf-select-item").forEach((item) => item.addEventListener("click", () => onPick(host, item.textContent ?? "", trigger)));
    });
    return host;
  }

  test("A pane that never opens is listbox-not-opened, no matching option is option-not-found, and a pick the display shows differently is verify-mismatch", async () => {
    pane([], () => {});
    expect(await write("sdf", "He/Him")).toMatchObject({ outcome: "failed", reason: "listbox-not-opened" });
    pane(["She/Her/Hers", "They/Them/Theirs"], () => {});
    expect(await write("sdf", "He/Him")).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    pane(["She/Her/Hers", "He/Him/His"], (_h, _l, trigger) => {
      trigger.textContent = "They/Them/Theirs";
    });
    expect(await write("sdf", "He/Him")).toMatchObject({ outcome: "failed", reason: "verify-mismatch" });
    pane(["She/Her/Hers", "He/Him/His"], () => {});
    expect(await write("sdf", "He/Him")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
  });
});

describe("Repeated rows", () => {
  const history: ApplicantSnapshot["history"] = {
    employment: [
      { employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true },
      { employer: "Sample Works", title: "Engineer", startDate: "2017-06-01", endDate: "2021-02-01" },
    ],
    education: [
      { school: "Sample College", degree: "Master of Science", field: "Data Science", startDate: "2017-09-01", endDate: "2019-05-01" },
      { school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" },
    ],
  };
  const applicant: ApplicantSnapshot = { ...ADA, history };
  
  test("Avature's numbered rows: a bare Current? and In Progress are the row's own flags, read from the choice group's name", async () => {
    // Live (koch.avature.net): a row's controls are numbered group-field-row, but each radio's id carries its option suffix (929-3-0_37).
    setUrl("https://koch.avature.net/careers/ApplicationMethods");
    const radios = (id: string, q: string) => `<div class="datasetfieldSpec RadioButtonListFormField isRequired"><fieldset role="presentation">
      <legend id="${id}-label">${q}<span aria-hidden="true"> *</span></legend>
      <div id="${id}" role="radiogroup" aria-labelledby="${id}-label" aria-required="true"><input type="hidden" name="${id}" value="">
        <div><input type="radio" name="${id}" value="37" id="${id}_37" data-option-name="Yes"><label for="${id}_37">Yes</label></div>
        <div><input type="radio" name="${id}" value="38" id="${id}_38" data-option-name="No"><label for="${id}_38">No</label></div></div></fieldset></div>`;
    mount(`<h3>Work Experience</h3><div id="multipleDatasetEntry_929_0"><label for="929-2-0">Position Title *</label><input id="929-2-0" name="929-2-0" type="text">
      <label for="929-1-0">Company *</label><input id="929-1-0" name="929-1-0" type="text">${radios("929-3-0", "Current?")}</div>
      <h3>Education</h3><div id="multipleDatasetEntry_931_0"><label for="931-1-0">School *</label><input id="931-1-0" name="931-1-0" type="text">${radios("931-6-0", "In Progress")}</div>`);
    const host = fakeHost({ "current?": { outcome: "abstained", reason: "no-answer" }, "in progress": { outcome: "abstained", reason: "no-answer" } });
    await runFill(makeEngine(host), { applicant });
    expect([byId("929-3-0_37").checked, byId("929-3-0_38").checked]).toEqual([true, false]);
    expect([byId("931-6-0_37").checked, byId("931-6-0_38").checked]).toEqual([false, true]);
  });
});
