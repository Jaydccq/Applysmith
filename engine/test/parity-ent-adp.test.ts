// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for HR-suite application systems (ADP WorkforceNow: Dojo text boxes, street lines, consent boxes).
import { describe, expect, test } from "vitest";
import { byId, fakeHost, makeEngine, mount, PAD, runFill, setUrl, summaryOf, write } from "./helpers";

const ADP = "https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html";

describe("ADP Dojo text boxes", () => {
  test("A dijit text box and its inner input are one control, and the whole-date box takes the combined date, never a month name", async () => {
    setUrl(ADP);
    mount(`<div class="element"><label for="dt">Start Date</label><div class="dijit dijitTextBox" id="w_dt" widgetid="w_dt"><div class="dijitPlaceHolder dijitInputField">mm/dd/yyyy</div><input id="dt" class="dijitInputInner" type="text"></div></div>
      <div class="element"><label for="nm">First Name</label><div class="dijit dijitTextBox" widgetid="w_nm"><input id="nm" class="dijitInputInner" type="text"></div></div>${PAD}`);
    const host = fakeHost({ "start date": { outcome: "filled", value: "2021-03-01" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(s.rows.filter((r) => r.label === "Start Date")).toHaveLength(1);
    expect(s.rows.filter((r) => r.label === "First Name")).toHaveLength(1);
    expect(byId("dt").value).toBe("2021-03-01");
    expect(byId("nm").value).toBe("Ada");
  });

  test("Street line labels take the street, even on a box named location", async () => {
    setUrl(ADP);
    mount(`<label for="a">Address Line 1</label><input id="a" name="location">${PAD}`);
    await runFill(makeEngine(fakeHost()));
    expect(byId("a").value).toBe("1 Example Street");
  });

  test("An optional text-message consent box whose question text is polluted with page chrome stays unticked", async () => {
    setUrl(ADP);
    mount(`<div><p>Home Page Careers Search Jobs id_x9f3a</p><label><input type="checkbox" id="sms"> I agree to receive text messages (optional)</label></div>${PAD}<label for="e">Email</label><input id="e">`);
    const host = fakeHost({ "i agree to receive text messages (optional)": { outcome: "filled", value: "Yes" } });
    await runFill(makeEngine(host), { acceptLegalDeclarations: true });
    expect(byId("sms").checked).toBe(false);
  });
});

describe("ADP Section 503 radio group", () => {
  // Live: right after the page cleared the answer, the group read "" (property and attribute) while the old option still read aria-checked=true.
  const group = (value: string) =>
    mount(`<sdf-radio-group label="Section 503 Disability Status" value="${value}"><div role="radiogroup">
      <div id="disabilityStatusIdYes" role="radio" value="Y" aria-checked="false">Yes</div>
      <div id="disabilityStatusIdNo" role="radio" value="N" aria-checked="true">No</div>
      <div id="disabilityStatusIdDecline" role="radio" value="D" aria-checked="false">I do not want to answer</div></div></sdf-radio-group>`);

  test("A cleared group whose old option is the answer is pressed again and takes it", async () => {
    group("");
    const host = document.querySelector("sdf-radio-group") as HTMLElement;
    for (const r of Array.from(host.querySelectorAll("[role=radio]"))) r.addEventListener("click", () => host.setAttribute("value", r.getAttribute("value") ?? ""));
    expect(await write("disabilityStatusIdYes", "No")).toMatchObject({ outcome: "filled" });
    expect(host.getAttribute("value")).toBe("N");
  });

  test("A cleared group that takes no press stays unanswered: its old option's stale state is no proof of the answer", async () => {
    group("");
    const host = document.querySelector("sdf-radio-group") as HTMLElement;
    expect(await write("disabilityStatusIdYes", "No")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(host.getAttribute("value")).toBe("");
  });
});
