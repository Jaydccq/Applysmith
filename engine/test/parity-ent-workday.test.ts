// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for enterprise application suites (Workday page behaviors).
import { describe, expect, test } from "vitest";
import { byId, fakeHost, makeEngine, mount, PAD, rowFor, runFill, setUrl, summaryOf } from "./helpers";
import { mountMenu } from "./widgets-fixtures";
import { mountWorkdayPrompt } from "./ats-fixtures";

const WORKDAY = "https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply";
const asked = (h: ReturnType<typeof fakeHost>) => h.matchRequests.flatMap((r) => r.questions.map((q) => q.text));

describe("Workday page behaviors", () => {

  test("Phone Device Type goes to the matcher while the number is profile-backed and the extension stays empty", async () => {
    setUrl(WORKDAY);
    mount(`<label for="phoneNumber--phoneNumber">Phone Number</label><input id="phoneNumber--phoneNumber"><label for="phoneNumber--extension">Phone Extension</label><input id="phoneNumber--extension">${PAD}`);
    const type = mountMenu({ id: "phoneNumber--phoneType", label: "Phone Device Type", options: ["Home", "Mobile"], openOn: "sequence" });
    const host = fakeHost({ "phone device type": { outcome: "filled", value: "Mobile" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(type.shown()).toBe("Mobile");
    expect(rowFor(s, "Phone Device Type").lane).toBe("bank");
    expect(rowFor(s, "Phone Number")).toMatchObject({ lane: "profile", outcome: "filled" });
    expect(byId("phoneNumber--extension").value).toBe("");
  });

  test("A row reports the question, not the value its control held at scan time", async () => {
    mount(`<div><label for="d">Degree</label><button id="d" type="button" aria-haspopup="listbox">Master's Degree</button></div><label for="x">Referral</label><input id="x" value="r"><label for="y">Email</label><input id="y">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ degree: { outcome: "filled", value: "Master's Degree" } }))));
    expect(rowFor(s, "Degree").label).toBe("Degree");
    expect(s.rows.some((r) => /master/i.test(r.label))).toBe(false);
  });

  test("A prompt's selected pills (role=option inside selectedItemList) are never read as another menu's options", async () => {
    setUrl(WORKDAY);
    mount(`<label for="fn">First Name</label><input id="fn">`);
    // Workday's State button names no popup; its list is drawn only when opened.
    const state = mountMenu({ id: "address--countryRegion", label: "State", options: ["Alabama", "North Carolina", "Texas"], ref: false });
    const pills = document.createElement("div");
    pills.innerHTML = `<label for="cpc">Country Phone Code</label><ul role="listbox" data-automation-id="selectedItemList" aria-label="items selected">
      <li role="presentation"><div role="option" data-automation-id="selectedItem" aria-label="United States of America (+1), press delete to clear value.">United States of America (+1)</div></li></ul>`;
    document.body.append(pills);
    const host = fakeHost({ state: { outcome: "filled", value: "North Carolina" } });
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.text === "State");
    expect(q?.options ?? []).not.toContain("United States of America (+1)");
    expect(state.shown()).toBe("North Carolina");
  });

  test("A search prompt whose input reads placeholder Search is the field its label names, not a site search box", async () => {
    setUrl(WORKDAY);
    mount(`<label for="fn">First Name</label><input id="fn">`);
    const school = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University", "Oakmont College"] });
    const source = mountWorkdayPrompt({ id: "source--source", label: "How Did You Hear About Us?", catalog: ["LinkedIn"] });
    for (const p of [school, source]) p.input.setAttribute("placeholder", "Search");
    const host = fakeHost({ "school or university": { outcome: "filled", value: "Example University" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(asked(host)).toEqual(expect.arrayContaining(["School or University", "How Did You Hear About Us?"]));
    expect(rowFor(s, "School or University")).toMatchObject({ outcome: "filled" });
    expect(school.pills()).toEqual(["Example University"]);
  });
});
