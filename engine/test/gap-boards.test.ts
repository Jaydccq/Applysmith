// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Profile-field pass; Question pass; Control writers and commit verification; Drivers for application boards.
import { describe, expect, test } from "vitest";
import { scanControls } from "../src/scan";
import { ADA, byId, fakeHost, makeEngine, mount, PAD, rowFor, runFill, setUrl, summaryOf } from "./helpers";


const WITH_EDUCATION = {
  ...ADA,
  history: {
    education: [
      { school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" },
      { school: "Sample College", degree: "Master of Science", field: "Data Science", startDate: "2017-09-01", endDate: "2019-05-01" },
    ],
  },
};

describe("Lever parser overwrite", () => {
  test("Lever parser overwrite: a parser-mangled phone and a truncated LinkedIn address are replaced by the profile's, and a box that holds the profile value is left alone", async () => {
    setUrl("https://jobs.lever.co/example/00000000-0000-0000-0000-000000000000/apply");
    mount(`<label for="ph">Phone</label><input id="ph" type="tel" value="55501">
      <label for="li">LinkedIn URL</label><input id="li" value="linkedin.com/in/ad">
      <label for="em">Email</label><input id="em" value="ada@example.com">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));
    expect(byId("ph").value).toBe("+1 555 0100");
    expect(byId("li").value).toBe("https://www.linkedin.com/in/ada-example");
    expect(byId("em").value).toBe("ada@example.com");
    expect(s.rows.find((r) => r.label.toLowerCase().includes("email"))?.outcome).toBe("prefilled");
  });
});

describe("Greenhouse numbered education", () => {
  test("Greenhouse education: the controls of row N take entry N for school, degree and discipline, with no cross-wiring between rows", async () => {
    setUrl("https://job-boards.greenhouse.io/example/jobs/1234567");
    mount(`<h3>Education</h3>
      <label for="school--0">School</label><input id="school--0"><label for="degree--0">Degree</label><input id="degree--0"><label for="discipline--0">Discipline</label><input id="discipline--0">
      <label for="school--1">School</label><input id="school--1"><label for="degree--1">Degree</label><input id="degree--1"><label for="discipline--1">Discipline</label><input id="discipline--1">`);
    await runFill(makeEngine(fakeHost()), { applicant: WITH_EDUCATION });
    expect(["school--0", "degree--0", "discipline--0"].map((id) => byId(id).value)).toEqual(["Example University", "Bachelor of Science", "Computer Science"]);
    expect(["school--1", "degree--1", "discipline--1"].map((id) => byId(id).value)).toEqual(["Sample College", "Master of Science", "Data Science"]);
  });
});

describe("Greenhouse numbered employment", () => {
  test("Greenhouse employment: the boxes of row N (company-name-N, one dash) are asked for their own row, never take row 0's answer, and a current job ticks its Current role box", async () => {
    setUrl("https://job-boards.greenhouse.io/example/jobs/1234567");
    const row = (n: number) => `<div class="employment--form"><p>Employment</p>
      <label for="company-name-${n}">Company name*</label><input id="company-name-${n}" aria-required="true">
      <label for="title-${n}">Title*</label><input id="title-${n}" aria-required="true">
      <label for="start-date-year-${n}">Start date year*</label><input id="start-date-year-${n}" aria-required="true">
      <label for="end-date-year-${n}">End date year*</label><input id="end-date-year-${n}" aria-required="true">
      <input type="checkbox" id="current-role-${n}_1" name="current-role-${n}" value="1"><label for="current-role-${n}_1">Current role</label></div>`;
    mount(`${row(0)}${row(1)}`);
    const said = (value: string) => ({ outcome: "filled" as const, value });
    const host = fakeHost({
      '[id="company-name-0"]': said("Example Labs"),
      '[id="title-0"]': said("Staff Engineer"),
      '[id="start-date-year-0"]': said("2021"),
      '[id="end-date-year-0"]': { outcome: "abstained" as const, reason: "section-policy", terminal: true },
      '[id="company-name-1"]': said("Sample Corp"),
      '[id="title-1"]': said("Intern"),
      '[id="start-date-year-1"]': said("2019"),
      '[id="end-date-year-1"]': said("2019"),
    });
    const applicant = {
      ...ADA,
      history: {
        employment: [
          { employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true },
          { employer: "Sample Corp", title: "Intern", startDate: "2019-06-01", endDate: "2019-08-01" },
        ],
      },
    };
    await runFill(makeEngine(host), { applicant });
    expect(["company-name-0", "title-0", "start-date-year-0", "end-date-year-0"].map((id) => byId(id).value)).toEqual(["Example Labs", "Staff Engineer", "2021", ""]);
    expect(["company-name-1", "title-1", "start-date-year-1", "end-date-year-1"].map((id) => byId(id).value)).toEqual(["Sample Corp", "Intern", "2019", "2019"]);
    expect([byId("current-role-0_1").checked, byId("current-role-1_1").checked]).toEqual([true, false]);
  });
});

describe("Greenhouse bare Add another buttons", () => {
  test("Greenhouse: each block's bare Add another grows its own block, so one job per entry and the education Add never adds an employment row", async () => {
    setUrl("https://job-boards.greenhouse.io/example/jobs/1234567");
    const job = (n: number) => `<div class="employment-form"><label for="company-name-${n}">Company name*</label><input id="company-name-${n}">
      <label for="title-${n}">Title*</label><input id="title-${n}"></div>`;
    const school = (n: number) => `<div class="education--form"><label for="school--${n}">School</label><input id="school--${n}">
      <label for="degree--${n}">Degree</label><input id="degree--${n}"></div>`;
    mount(`<form><div class="employment--container">${job(0)}<button type="button" class="add-another-button">Add another</button></div>
      <div class="education--container">${school(0)}<button type="button" class="add-another-button">Add another</button></div></form>`);
    const [addJob, addSchool] = Array.from(document.querySelectorAll<HTMLButtonElement>(".add-another-button"));
    addJob!.addEventListener("click", () => addJob!.insertAdjacentHTML("beforebegin", job(document.querySelectorAll(".employment-form").length)));
    addSchool!.addEventListener("click", () => addSchool!.insertAdjacentHTML("beforebegin", school(document.querySelectorAll(".education--form").length)));
    const applicant = {
      ...WITH_EDUCATION,
      history: {
        ...WITH_EDUCATION.history,
        employment: [
          { employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true },
          { employer: "Sample Corp", title: "Intern", startDate: "2019-06-01", endDate: "2019-08-01" },
        ],
      },
    };
    await runFill(makeEngine(fakeHost()), { applicant });
    expect([document.querySelectorAll(".employment-form").length, document.querySelectorAll(".education--form").length]).toEqual([2, 2]);
  });
});

describe("Phone country trap", () => {
  test("A bare Country control inside a phone fieldset picks a dial code and never takes the address country", async () => {
    mount(`<fieldset><legend>Phone</legend><label for="pc">Country</label><select id="pc"><option value="">Select</option><option>United States (+1)</option><option>Canada (+1)</option><option>United Kingdom (+44)</option></select>
      <label for="pn">Phone number</label><input id="pn" type="tel"></fieldset>
      <label for="ad">Country</label><select id="ad"><option value="">Select</option><option>United States</option><option>Canada</option></select>`);
    await runFill(makeEngine(fakeHost()), { applicant: ADA });
    expect(byId<HTMLSelectElement>("ad").value).toBe("United States");
    expect(byId<HTMLSelectElement>("pc").value).not.toBe("United States");
  });

  test("A country list made of dial codes is not the address country even without a phone legend", async () => {
    mount(`<label for="pc">Country</label><select id="pc"><option value="">Select</option><option>United States (+1)</option><option>Canada (+1)</option><option>United Kingdom (+44)</option><option>France (+33)</option></select>`);
    const infos = scanControls(document);
    await runFill(makeEngine(fakeHost()), { applicant: ADA });
    expect(infos).toHaveLength(1);
    expect(byId<HTMLSelectElement>("pc").value).toBe("");
  });
});

describe("Native radio commit", () => {
  test("Ashby native radio default: a radio the page already checked still gets input and change dispatched", async () => {
    setUrl("https://jobs.ashbyhq.com/example/00000000/application");
    mount(`<fieldset><legend>Are you over 18?</legend><label><input type="radio" name="a" value="Yes" checked> Yes</label><label><input type="radio" name="a" value="No"> No</label></fieldset>${PAD}`);
    const seen: string[] = [];
    document.querySelector<HTMLInputElement>('input[value="Yes"]')!.addEventListener("change", () => seen.push("change"));
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "are you over 18?": { outcome: "filled", value: "Yes" } }))));
    expect(seen.length).toBeGreaterThan(0);
    expect(s.rows.find((r) => r.label.includes("over 18"))?.outcome).toBe("prefilled");
  });

  test("A click that the page undoes on a later tick is a verify mismatch, never filled", async () => {
    mount(`<fieldset><legend>Are you over 18?</legend><label><input type="radio" name="a" value="Yes"> Yes</label><label><input type="radio" name="a" value="No"> No</label></fieldset>${PAD}`);
    const yes = document.querySelector<HTMLInputElement>('input[value="Yes"]')!;
    // The undo comes on the next microtask, after the click handler and before anything awaits the write.
    yes.addEventListener("click", () => queueMicrotask(() => (yes.checked = false)));
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "are you over 18?": { outcome: "filled", value: "Yes" } }))));
    expect(s.rows.find((r) => r.label.includes("over 18"))).toMatchObject({ outcome: "failed", reason: "verify-mismatch" });
  });
});

describe("Ashby question text", () => {
  test("A Yes/No question under a legend carries the field description as well as the title", async () => {
    mount(`<div data-field-path="question-1"><fieldset class="_fieldEntry_x1"><legend>Will you now or in the future require sponsorship?</legend><div class="_description_x1">This includes work visas.</div>
      <label><input type="radio" name="q1" value="Yes"> Yes</label><label><input type="radio" name="q1" value="No"> No</label></fieldset></div>${PAD}`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests[0]?.questions[0];
    expect(q?.text).toBe("Will you now or in the future require sponsorship? This includes work visas.");
  });
});

describe("Workable ARIA radios", () => {
  test("A role=radio wrapper that owns no handler is pressed through the inert input inside it and verified by aria-checked", async () => {
    setUrl("https://apply.workable.com/example/j/ABC123/apply/");
    mount(`${PAD}<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em">
      <div role="radiogroup" aria-label="Are you authorized to work in the US?">
        <div role="radio" id="r-yes" aria-checked="false"><input type="radio" aria-hidden="true" tabindex="-1" name="auth" value="Yes"> Yes</div>
        <div role="radio" id="r-no" aria-checked="false"><input type="radio" aria-hidden="true" tabindex="-1" name="auth" value="No"> No</div></div>`);
    for (const id of ["r-yes", "r-no"]) {
      const wrap = document.getElementById(id)!;
      // The page listens on the nested input only; its state shows on the wrapper.
      wrap.querySelector("input")!.addEventListener("click", () => {
        document.querySelectorAll("[role=radio]").forEach((r) => r.setAttribute("aria-checked", String(r === wrap)));
      });
    }
    const host = fakeHost({ "are you authorized to work in the us?": { outcome: "filled", value: "Yes" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(document.getElementById("r-yes")!.getAttribute("aria-checked")).toBe("true");
    expect(s.rows.find((r) => r.label.includes("authorized"))?.outcome).toBe("filled");
  });

  test("A radio named by the question's label and then its own is offered and picked by its own words", async () => {
    setUrl("https://apply.workable.com/example/j/ABC123/apply/");
    const opt = (id: string, w: string) =>
      `<div role="radio" id="w-${id}" aria-checked="false" aria-labelledby="ql rl-${id}"><label><input type="radio" aria-hidden="true" tabindex="-1" name="CA_1" value="${id}"><span id="rl-${id}">${w}</span></label></div>`;
    mount(`${PAD}<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em">
      <span id="ql"><strong>Are you eligible to work in the country you are applying?</strong></span>
      <fieldset role="radiogroup" aria-labelledby="ql">${opt("y", "YES")}${opt("n", "NO")}</fieldset>`);
    for (const id of ["w-y", "w-n"]) {
      const wrap = document.getElementById(id)!;
      wrap.querySelector("input")!.addEventListener("click", () => document.querySelectorAll("[role=radio]").forEach((r) => r.setAttribute("aria-checked", String(r === wrap))));
    }
    const host = fakeHost({ "are you eligible to work in the country you are applying?": { outcome: "filled", value: "No" } });
    await runFill(makeEngine(host));
    expect(host.matchRequests[0]?.questions.find((x) => x.text.includes("eligible"))?.options).toEqual(["YES", "NO"]);
    expect(document.getElementById("w-n")!.getAttribute("aria-checked")).toBe("true");
  });
});

describe("Workable read-only dropdown", () => {
  const OPTIONS = ["Available immediately", "1 week", "2 weeks", "3 weeks", "1 month", "2 months", "3 months or more"];

  /**
   * As recorded on a live Workable form: a press on the read-only box inserts a dialog beside it that holds a
   * search box (it takes focus and the keys) and the list; Escape on the box does nothing, a second press closes
   * the list, and an option is chosen by its own click, which also sets the hidden field.
   */
  function workableDropdown(): { input: HTMLInputElement; hidden: HTMLInputElement } {
    mount(`${PAD}<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em">
      <div><span><span><span id="CA_1_label"><strong>Notice period / availability</strong></span><span> (Optional)</span></span></span>
      <div data-ui="CA_1" data-open="false" data-input-type="select"><label><div data-role="illustrated-input"><div>
      <input id="input_CA_1_input" role="combobox" aria-haspopup="listbox" aria-expanded="false" aria-owns="input_CA_1_listbox" aria-controls="input_CA_1_listbox"
        aria-autocomplete="list" autocomplete="off" aria-labelledby="CA_1_label" placeholder="Select an option\u2026" type="text" value="" readonly="true"></div></div></label>
      <input name="CA_1" tabindex="-1" aria-hidden="true" value=""></div></div>`);
    const field = document.querySelector<HTMLElement>('[data-ui="CA_1"]')!;
    const input = byId("input_CA_1_input");
    const hidden = document.querySelector<HTMLInputElement>('input[name="CA_1"]')!;
    const close = () => {
      field.querySelector("dialog")?.remove();
      input.setAttribute("aria-expanded", "false");
      field.dataset.open = "false";
    };
    input.addEventListener("click", () => {
      if (field.querySelector("dialog")) return close();
      const dialog = document.createElement("dialog");
      dialog.setAttribute("open", "");
      dialog.setAttribute("role", "presentation");
      dialog.innerHTML = `<div data-role="search"><input type="search" placeholder="Search"></div><ul role="listbox" tabindex="-1" id="input_CA_1_listbox">${OPTIONS.map(
        (o, i) => `<li value="${101 + i}" id="opt-${101 + i}" role="option" aria-selected="false"><div role="presentation"><span data-role="option-label"> ${o}</span></div></li>`,
      ).join("")}</ul>`;
      dialog.addEventListener("keydown", (e) => {
        if (e.key === "Escape") close();
      });
      for (const li of Array.from(dialog.querySelectorAll("li"))) {
        li.addEventListener("click", () => {
          input.value = (li.textContent ?? "").trim();
          hidden.value = li.getAttribute("value") ?? "";
          close();
        });
      }
      field.append(dialog);
      input.setAttribute("aria-expanded", "true");
      field.dataset.open = "true";
      dialog.querySelector<HTMLInputElement>("input")!.focus();
    });
    return { input, hidden };
  }

  test("A read-only combobox that names its list is asked with the list's options, and the bank's choice is picked from the list and read back", async () => {
    setUrl("https://apply.workable.com/example/j/ABC123/apply/");
    const { input, hidden } = workableDropdown();
    const host = fakeHost({ "notice period / availability": { outcome: "filled", value: "2 weeks" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.text.startsWith("Notice period"));
    expect(q).toMatchObject({ kind: "custom-listbox", options: OPTIONS });
    // Picked from the list, as a person does, so the page's own field holds the choice as well.
    expect([input.value, hidden.value]).toEqual(["2 weeks", "103"]);
    expect(rowFor(s, "Notice period")).toMatchObject({ outcome: "filled", value: "2 weeks" });
  });

  test.each([
    ["a combobox that names no list", `role="combobox" aria-haspopup="listbox"`],
    ["a combobox whose popup is a dialog", `role="combobox" aria-haspopup="dialog" aria-controls="cal"`],
    ["a text box that controls a popup", `aria-controls="cal"`],
  ])("A read-only box that is %s is still skipped", (_, attrs) => {
    mount(`<span id="l">Start date</span><input id="ro" ${attrs} aria-labelledby="l" readonly>${PAD}`);
    expect(scanControls(document).map((i) => i.el.id)).not.toContain("ro");
  });
});

describe("LinkedIn location typeahead", () => {
  test("A typeahead input with aria-autocomplete list and no combobox role is an async search: the city is typed and the matching suggestion is clicked", async () => {
    setUrl("https://www.linkedin.com/jobs/view/1234567890/");
    mount(`<div role="dialog" class="jobs-easy-apply-modal"><h2>Apply to Example Corp</h2><label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em">
      <label for="loc">Location (city)</label><input id="loc" data-testid="typeahead-input" aria-autocomplete="list" aria-controls="loc-list" autocomplete="off"><div id="loc-list"></div></div>`);
    const input = byId("loc");
    const list = document.getElementById("loc-list")!;
    input.addEventListener("input", () => {
      const q = input.value.toLowerCase();
      setTimeout(() => {
        list.innerHTML = "";
        if (!q) return;
        for (const place of ["Durham, North Carolina, United States", "Durham, England, United Kingdom"].filter((p) => p.toLowerCase().includes(q))) {
          const row = document.createElement("div");
          row.setAttribute("role", "option");
          row.textContent = place;
          row.addEventListener("click", () => {
            input.value = place;
            list.innerHTML = "";
          });
          list.append(row);
        }
      }, 3);
    });
    const host = fakeHost({ "location (city)": { outcome: "filled", value: "Durham, North Carolina, United States" } });
    await runFill(makeEngine(host));
    expect(input.value).toBe("Durham, North Carolina, United States");
  });
});
