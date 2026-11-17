// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: LinkedIn Easy Apply scope.
import { beforeEach, describe, expect, test } from "vitest";
import { fakeHost, makeEngine, mount, runFill, setUrl, summaryOf } from "./helpers";

beforeEach(() => setUrl("https://www.linkedin.com/jobs/view/1234567890/"));

const FEED = Array.from({ length: 30 }, (_, i) => `<button type="button" role="combobox" aria-haspopup="listbox" aria-expanded="false" class="feed-filter" aria-label="Filter ${i}">Filter ${i}</button>`).join("");

const DIALOG = `<div role="dialog" class="jobs-easy-apply-modal" aria-labelledby="h"><h2 id="h">Apply to Example Corp</h2>
  <label for="yrs-numeric">Years of experience with Go</label><input id="yrs-numeric" type="number" min="0" max="40">
  <label for="mob">Mobile phone number</label><input id="mob" type="tel">
  <fieldset><legend>Are you legally authorized to work in the United States?</legend><label><input type="radio" name="wa" value="Yes"> Yes</label><label><input type="radio" name="wa" value="No"> No</label></fieldset>
  <label for="src">How did you hear about us?</label><select id="src"><option value="">Select</option><option>Job board</option><option>Referral</option></select>
  <button type="button" id="next">Next</button><button type="button" id="review">Review</button><button type="button" id="submit">Submit application</button></div>`;

describe("LinkedIn Easy Apply scope", () => {
  test("Dialog-only scan: 30 comboboxes in the feed chrome and an Easy Apply dialog with 4 questions, only the 4 questions are scanned and no feed combobox is opened", async () => {
    mount(`<main>${FEED}</main>${DIALOG}`);
    let opened = 0;
    document.querySelectorAll(".feed-filter").forEach((b) => ["mousedown", "click", "keydown"].forEach((t) => b.addEventListener(t, () => opened++)));
    const host = fakeHost({
      "years of experience with go": { outcome: "filled", value: "5" },
      "are you legally authorized to work in the united states?": { outcome: "filled", value: "Yes" },
      "how did you hear about us?": { outcome: "filled", value: "Job board" },
    });
    const s = summaryOf(await runFill(makeEngine(host), { applicant: { candidate: { phone: "+1 555 0100" } } }));
    expect(opened).toBe(0);
    expect(s.rows).toHaveLength(4);
    expect(s.rows.map((r) => r.label).sort()).toEqual(["Are you legally authorized to work in the United States?", "How did you hear about us?", "Mobile phone number", "Years of experience with Go"]);
    expect(s.site).toBe("linkedin");
  });

  test("Numeric question: the dialog asks Years of experience with Go with a number input and the bank answer 5, and 5 is written within the input's min and max", async () => {
    mount(`${DIALOG}`);
    await runFill(makeEngine(fakeHost({ "years of experience with go": { outcome: "filled", value: "5" } })));
    expect((document.getElementById("yrs-numeric") as HTMLInputElement).value).toBe("5");
    mount(`${DIALOG}`);
    await runFill(makeEngine(fakeHost({ "years of experience with go": { outcome: "filled", value: "120" } })));
    expect((document.getElementById("yrs-numeric") as HTMLInputElement).value).toBe("40");
  });

  test("A numeric answer that states more than one plain number is refused and the field stays empty", async () => {
    mount(`<div role="dialog" class="jobs-easy-apply-modal"><label for="a-numeric">Years of experience with Go</label><input id="a-numeric" type="text"><label for="b">Notes</label><input id="b"></div>`);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "years of experience with go": { outcome: "filled", value: "2-3 years" } }))));
    expect((document.getElementById("a-numeric") as HTMLInputElement).value).toBe("");
    expect(s.rows.find((r) => r.label === "Years of experience with Go")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("No advance: a filled step is still the same step and Next, Review and Submit were not activated", async () => {
    mount(`${DIALOG}`);
    const pressed: string[] = [];
    for (const id of ["next", "review", "submit"]) document.getElementById(id)!.addEventListener("click", () => pressed.push(id));
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "years of experience with go": { outcome: "filled", value: "5" } }))));
    expect(pressed).toEqual([]);
    expect(document.querySelector("[role=dialog]")?.isConnected).toBe(true);
    expect(s.counts.filled).toBeGreaterThanOrEqual(1);
  });

  test("LinkedIn with no Easy Apply dialog scans nothing, touches nothing and tells the person it is not an application", async () => {
    mount(`<label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b"><button type="button" id="more" aria-expanded="false">Add experience</button>`);
    let pressed = 0;
    document.getElementById("more")!.addEventListener("click", () => pressed++);
    const engine = makeEngine(fakeHost());
    const s = summaryOf(await runFill(engine, { applicant: { candidate: { firstName: "Alex", lastName: "Example" } } }));
    expect(s.notAnApplication).toBe(true);
    expect(s.rows).toHaveLength(0);
    expect(s.counts.filled).toBe(0);
    expect((document.getElementById("a") as HTMLInputElement).value).toBe("");
    expect(pressed).toBe(0);
    expect(engine.inspect()).toHaveLength(0);
    expect(engine.probe()).toMatchObject({ controls: 0 });
  });

  test("Indeed's hosted apply uses the same step contract: one page of the flow is filled and nothing advances", async () => {
    setUrl("https://smartapply.indeed.com/beta/indeedapply/form/contact-info");
    mount(`<label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b"><button id="continue" type="button">Continue</button>`);
    let pressed = 0;
    document.getElementById("continue")!.addEventListener("click", () => pressed++);
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: { candidate: { firstName: "Alex", lastName: "Example" } } }));
    expect(s.site).toBe("indeed-apply");
    expect(s.counts.filled).toBe(2);
    expect(pressed).toBe(0);
  });
});
