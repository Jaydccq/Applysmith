// Clean-room: docs/provenance.md#CR-ENGINE
// SuccessFactors rows, ADP choice groups, Paylocity react-selects and concealed decoys.
import { describe, expect, test } from "vitest";
import type { ApplicantSnapshot } from "../src/protocol";
import { readInfo, scanControls } from "../src/scan";
import { ADA, byId, fakeHost, makeEngine, mount, runFill, setUrl, summaryOf } from "./helpers";

const ALEX: ApplicantSnapshot = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example", email: "alex@example.com", phone: "+1 555 0100", address: { line1: "1 Example Street", city: "Durham", state: "NC", postalCode: "27701", country: "United States" } },
  history: {
    employment: [
      { employer: "Example Labs", title: "Staff Engineer", location: "Durham", startDate: "2021-03-01", current: true },
      { employer: "Sample Works", title: "Engineer", location: "Raleigh", startDate: "2017-06-01", endDate: "2021-02-01" },
    ],
    education: [{ school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" }],
  },
};

/** A Shadow-DOM date picker: the host carries the label, the input inside takes the text. */
function picker(id: string, label: string, placeholder: string): HTMLElement {
  const host = document.createElement("ui5-date-picker");
  host.id = id;
  host.setAttribute("aria-label", label);
  host.attachShadow({ mode: "open" }).innerHTML = `<input placeholder="${placeholder}" aria-label="${label}">`;
  return host;
}
const shown = (id: string): string => (document.getElementById(id)!.shadowRoot!.querySelector("input") as HTMLInputElement).value;

describe("ENT-57 SuccessFactors repeating rows", () => {
  const page = () => {
    setUrl("https://career5.successfactors.com/career?company=example");
    mount(`<h3>Personal</h3><label for="fn">First name</label><input id="fn"><label for="subj">Subject</label><input id="subj">
      <h3>Previous Employment 1</h3><div id="r1"><label for="t1">Title</label><input id="t1"><label for="c1">Company name</label><input id="c1"><label for="l1">Location</label><input id="l1"></div>
      <h3>Education 1</h3><div id="r2"><label for="i1">Institute</label><input id="i1"><label for="s1">Subject</label><input id="s1"></div>`);
    document.getElementById("r1")!.append(picker("d1", "Start Date", "MM/dd/yyyy"));
    document.getElementById("r2")!.append(picker("d2", "End Date", "dd.MM.yyyy"));
  };

  test("Title, Company name and Location go to the experience entry, Institute and Subject to the school and field of study", async () => {
    page();
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(["t1", "c1", "l1", "i1", "s1"].map((id) => byId(id).value)).toEqual(["Staff Engineer", "Example Labs", "Durham", "Example University", "Computer Science"]);
  });

  test("each row's date picker inside a shadow root takes that row's date in the control's own layout, never a raw ISO", async () => {
    page();
    await runFill(makeEngine(fakeHost()), { applicant: ALEX });
    expect(shown("d1")).toBe("03/01/2021");
    expect(shown("d2")).toBe("01.05.2017");
  });

  test("a bare Subject outside an education section is never bound into a row", async () => {
    page();
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ALEX }));
    expect(byId("subj").value).toBe("");
    expect(s.rows.find((r) => r.selector.includes("subj"))).toMatchObject({ outcome: "abstained" });
  });

});

describe("ENT-99 ADP self-identification and questions", () => {
  const ADP = "https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html?cid=x";
  const form = (chosen: string, ids: string) => `<label for="fn">First name</label><input id="fn">
    <fieldset><legend>Gender *</legend><label><input type="radio" name="gender" value="f" ${chosen === "f" ? "checked" : ""}> Female</label><label><input type="radio" name="gender" value="m" ${chosen === "m" ? "checked" : ""}> Male</label></fieldset>
    <fieldset><legend>Race (select all that apply)</legend><label for="${ids}1"><input type="checkbox" id="${ids}1" name="race" value="a"> Asian</label><label for="${ids}2"><input type="checkbox" id="${ids}2" name="race" value="b"> White</label></fieldset>`;
  const bank = { gender: { outcome: "filled" as const, value: "Female" }, "race (select all that apply)": { outcome: "filled" as const, value: "Asian" } };

  test("an already-answered group is reported as prefilled and keeps the chosen option; a cleared group stays answerable", async () => {
    setUrl(ADP);
    mount(form("m", "chk_83712_"));
    const answered = summaryOf(await runFill(makeEngine(fakeHost(bank)), { applicant: ADA }));
    expect(answered.rows.find((r) => r.label === "Gender")).toMatchObject({ outcome: "prefilled", required: true });
    expect((document.querySelector('input[value="m"]') as HTMLInputElement).checked).toBe(true);
    mount(form("", "chk_83712_"));
    const cleared = summaryOf(await runFill(makeEngine(fakeHost(bank)), { applicant: ADA }));
    expect(cleared.rows.find((r) => r.label === "Gender")).toMatchObject({ outcome: "filled" });
    expect((document.querySelector('input[value="f"]') as HTMLInputElement).checked).toBe(true);
  });

  test("a checkbox's generated id is not the group's identity: the selector follows the name across a re-render", async () => {
    setUrl(ADP);
    mount(form("", "chk_83712_"));
    const s = summaryOf(await runFill(makeEngine(fakeHost(bank)), { applicant: ADA }));
    const row = s.rows.find((r) => r.label.startsWith("Race"))!;
    expect(row.selector).not.toContain("chk_83712_");
    expect(row.selector).toContain('[name="race"]');
    mount(form("", "chk_99001_"));
    expect(document.querySelectorAll(row.selector)).toHaveLength(2);
  });
});

describe("ENT-120 Paylocity capture hygiene and refill", () => {
  const PCTY = "https://recruiting.paylocity.com/recruiting/jobs/Apply/1/example";
  /** A react-select built without class names of its own: the generated ones, and no value in the search input. */
  const emotion = (id: string, label: string, value: string) => `<div><label for="${id}">${label}</label><div class="css-b62m3t-container"><div class="css-13cymwt-control"><div class="css-hlgwow">${value ? `<div class="css-1dimb5e-singleValue">${value}</div>` : '<div class="css-1jqq78o-placeholder">Select...</div>'}<div class="css-19bb58m"><input id="${id}" role="combobox" aria-autocomplete="list" aria-expanded="false" value=""></div></div></div></div></div>`;
  const page = `<header><h1>Apply to Example Corp</h1></header><div role="dialog"><h2>Upload your resume</h2><p>Drag a file here</p></div>${emotion("co", "Country", "United States")}${emotion("st", "State", "")}<label for="ad">Address</label><input id="ad">`;

  test("a committed country is read through the empty search input, and a placeholder reads as empty", () => {
    setUrl(PCTY);
    mount(page);
    const infos = scanControls(document);
    expect(infos.map((i) => [i.el.id, readInfo(i)])).toEqual([["co", "United States"], ["st", ""], ["ad", ""]]);
  });

});

describe("BRD-84 concealed decoys", () => {
  const writeNames = async (html: string, applicant = ADA) => {
    mount(html);
    await runFill(makeEngine(fakeHost()), { applicant });
  };

  test("aria-hidden hides a control only while a dialog is open: with none open it is filled, behind an open dialog it is left, and inside the dialog it is filled", async () => {
    // Jobvite marks a visible required box aria-hidden with no dialog on the page.
    const form = `<div aria-hidden="true"><label for="fn">First name</label><input id="fn"></div><label for="ln">Last name</label><input id="ln"><label for="em">Email</label><input id="em">`;
    await writeNames(form);
    expect(byId("fn").value).toBe("Ada");
    await writeNames(`${form}<dialog style="display:none">x</dialog>`);
    expect(byId("fn").value).toBe("Ada");
    await writeNames(`${form}<div role="dialog">Sign in</div>`);
    expect(byId("fn").value).toBe("");
    expect(byId("ln").value).toBe("Example");
    await writeNames(`<div role="dialog"><div aria-hidden="true"><label for="fn">First name</label><input id="fn"></div><label for="ln">Last name</label><input id="ln"></div>`);
    expect(byId("fn").value).toBe("Ada");
    await writeNames(`<div aria-hidden="true"><div role="dialog"><label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln"></div></div><div role="dialog">Sign in</div>`);
    expect(byId("fn").value).toBe("");
  });

  test("a box its own label names a honeypot is never a question (Oracle, Chase)", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln"><label for="honey-pot-1" aria-hidden="true"><span>honeypot</span></label><input id="honey-pot-1" name="honey-pot" aria-hidden="true" tabindex="-1">`);
    const host = fakeHost({});
    await runFill(makeEngine(host), { applicant: ADA });
    expect(host.matchRequests.flatMap((r) => r.questions).map((q) => q.text)).not.toContain("honeypot");
    expect(scanControls(document).map((i) => i.el.id)).not.toContain("honey-pot-1");
    expect(byId("fn").value).toBe("Ada");
  });

  test("a native radio hidden under a styled label stays fillable", async () => {
    mount(`<fieldset><legend>Are you over 18?</legend><label class="pill"><input type="radio" name="o" value="Yes" style="opacity:0;position:absolute"> Yes</label><label class="pill"><input type="radio" name="o" value="No" style="opacity:0;position:absolute"> No</label></fieldset><label for="ln">Last name</label><input id="ln">`);
    const host = fakeHost({ "are you over 18?": { outcome: "filled", value: "Yes" } });
    await runFill(makeEngine(host), { applicant: ADA });
    expect((document.querySelector('input[value="Yes"]') as HTMLInputElement).checked).toBe(true);
  });

  test("a zero-height overflow-hidden wrapper with tabindex -1 and no label conceals a text decoy; opacity 0 alone with a label does not", async () => {
    await writeNames(`<label for="fn">First name</label><input id="fn"><div style="height:0;overflow:hidden"><input id="d2" name="company_url" tabindex="-1"></div><label for="ln">Last name</label><input id="ln" style="opacity:0">`);
    expect(byId("d2").value).toBe("");
    expect(byId("ln").value).toBe("Example");
  });
});
