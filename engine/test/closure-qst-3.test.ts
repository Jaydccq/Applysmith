// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Question pass (prescan budget, loop, stuck and repeat rules, budgets, apply order); Pipeline stages and their order (lane ownership, claims and releases).
import { describe, expect, test } from "vitest";
import { scanControls } from "../src/scan";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, runFill, summaryOf, tick, until } from "./helpers";
import { mountMenu } from "./widgets-fixtures";

const asked = (host: ReturnType<typeof fakeHost>) => host.matchRequests.flatMap((r) => r.questions.map((q) => q.text));

describe("Stopping between the scan and the response", () => {
  test("stop after the match response writes nothing", async () => {
    mount(`<label for="a">Favourite colour</label><input id="a">${PAD}`);
    const host = fakeHost({ "favourite colour": { outcome: "filled", value: "Green" } });
    const engine = makeEngine(host);
    const answer = host.match.bind(host);
    host.match = async (req) => {
      engine.stop(req.runId, "cancelled");
      return answer(req);
    };
    const status = await (async () => {
      engine.start({ applicant: ADA });
      await until(() => engine.status().state !== "running");
      return engine.status();
    })();
    await tick(40);
    expect(status).toMatchObject({ state: "stopped", reason: "cancelled" });
    expect(byId("a").value).toBe("");
    expect(summaryOf(status).rows.some((r) => r.outcome === "filled")).toBe(false);
    expect(host.matchRequests).toHaveLength(1);
  });
});

describe("Lane ownership", () => {
  const owners = () => Object.fromEntries(scanControls(document).map((i) => [i.label.text || i.el.id, i.owner]));

  test("whole-label \"Employer\" goes to the profile pass, \"May we contact this employer?\" goes to the matcher", async () => {
    mount(`<label for="emp">Employer</label><input id="emp"><label for="ce">May we contact this employer?</label><input id="ce">
      <label for="t">Title</label><input id="t"><label for="jt">Job title</label><input id="jt">
      <label for="e1">Email</label><input id="e1"><label for="e2">Retype your email</label><input id="e2"><label for="e3">May we contact you at your email?</label><input id="e3">
      <label for="hp">Home phone</label><input id="hp"><label for="pd">Phone Device Type</label><select id="pd"><option value="">Select</option><option>Mobile</option></select>
      <label for="up">Resume</label><input id="up" type="file">`);
    mountMenu({ id: "ctry", label: "Country", options: ["United States", "Canada"] });
    expect(owners()).toMatchObject({
      Employer: "profile",
      "May we contact this employer?": "matcher",
      Title: "matcher",
      "Job title": "profile",
      Email: "profile",
      "Retype your email": "profile",
      "May we contact you at your email?": "matcher",
      "Home phone": "profile",
      "Phone Device Type": "matcher",
      Country: "matcher",
      Resume: "profile",
    });
    const host = fakeHost();
    await runFill(makeEngine(host));
    const texts = asked(host);
    expect(texts).toEqual(expect.arrayContaining(["May we contact this employer?", "Title", "May we contact you at your email?", "Phone Device Type", "Country"]));
    for (const never of ["Employer", "Job title", "Email", "Retype your email", "Home phone", "Resume"]) expect(texts).not.toContain(never);
  });

  test("Education Address Line 1 is sent to the matcher, top-level Address Line 1 is profile-backed", async () => {
    mount(`<label for="top">Address Line 1</label><input id="top">
      <fieldset><legend>Education</legend><label for="ed">Address Line 1</label><input id="ed"><label for="edc">City</label><input id="edc"></fieldset>
      <fieldset><legend>Employment History</legend><label for="jt">Job title</label><input id="jt"><label for="wp">Phone</label><input id="wp"></fieldset>`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    // The home address reaches its own box only; the contact labels inside a history section go to the matcher, which holds the policy for them.
    expect(byId("top").value).toBe("1 Example Street");
    expect(["ed", "edc", "jt", "wp"].map((id) => byId(id).value)).toEqual(["", "", "", ""]);
    expect(asked(host)).toEqual(["Address Line 1", "City", "Job title", "Phone"]);
  });
});

describe("Claims and releases", () => {
  const EMPLOYER = `<div class="employment"><h3>Employment History</h3><input type="hidden" name="$$employerId_1_x" value="1">
    <label for="co">Company</label><input id="co" name="employerCompany_1_x">
    <label for="c1">City</label><input id="c1" name="employerCity_1_x"></div>
    <label for="home">City</label><input id="home" name="city" autocomplete="address-level2">`;
  const applicant = { ...ADA, history: { employment: [{ employer: "Example Labs", title: "Staff Engineer", city: "Raleigh" }] } };

  test("section-policy abstain on an employer City releases it to the profile pass, which writes that row's own city", async () => {
    mount(EMPLOYER);
    const host = fakeHost({ city: { outcome: "abstained", reason: "section-policy", terminal: true } });
    await runFill(makeEngine(host), { applicant });
    expect(byId("c1").value).toBe("Raleigh");
    expect(byId("home").value).toBe("Durham");
    expect(asked(host)).toEqual(["Company", "City"]);
    // Anywhere else the same abstain stays final.
    mount(`<fieldset><legend>Other</legend><label for="x">Referral city</label><input id="x"></fieldset>${PAD}`);
    const finalHost = fakeHost({ "referral city": { outcome: "abstained", reason: "section-policy", terminal: true } });
    const s = summaryOf(await runFill(makeEngine(finalHost), { applicant }));
    expect(byId("x").value).toBe("");
    expect(s.rows.find((r) => r.label === "Referral city")).toMatchObject({ outcome: "abstained", reason: "terminal-abstain" });
  });

});
