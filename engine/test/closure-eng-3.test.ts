// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Pipeline stages and their order (apply loop, summary, stale prose, foreign surfaces); Question pass (claim keys).
import { afterEach, describe, expect, test, vi } from "vitest";
import { scanControls } from "../src/scan";
import { unclaimedRows } from "../src/summary";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, newRun } from "./helpers";

afterEach(() => {
  vi.useRealTimers();
});

describe("Controls no lane claimed", () => {
  test("unclaimed required control row and event", async () => {
    mount(`<label for="fn">First name</label><input id="fn">
      <label for="pc">Postal code</label><input id="pc" required>
      <label for="kept">Reference code</label><input id="kept" value="AB-1">
      <fieldset><legend>Preferred contact</legend>
        <label><input type="radio" name="c" value="e" checked> Email</label><label><input type="radio" name="c" value="p"> Phone</label></fieldset>
      <fieldset><legend>Tools you use (select all that apply)</legend>
        <label><input type="checkbox" name="t" value="a" checked> Alpha</label><label><input type="checkbox" name="t" value="b"> Beta</label></fieldset>
      <div role="combobox" aria-label="Team"><input id="team" value="Platform"><button type="button" aria-label=""></button><button type="button"></button></div>
      <div id="applysmith-panel"><label for="ours">Extension box</label><input id="ours"></div>
      <div class="simplify-jobs-root"><label for="theirs">Report</label><input id="theirs"></div>`);
    const applicant = { candidate: { fullName: "Ada Example", firstName: "Ada", lastName: "Example", email: "ada@example.com" } };
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant }));
    const row = rowFor(s, "postal code");
    expect(row).toMatchObject({ outcome: "skipped", reason: "no-lane-claimed", required: true });
    expect(s.counts.requiredUnfilled).toBe(1);
    expect(s.events?.find((e) => e.selector === row.selector)).toMatchObject({ kind: "uncertainty", reason: "unrecognised", required: true });
    // A control the page already filled is listed prefilled, with no event.
    expect(rowFor(s, "reference code")).toMatchObject({ outcome: "prefilled", reason: "no-lane-claimed", value: "AB-1" });
    expect(s.events?.some((e) => e.question === "Reference code")).toBe(false);
    // Groups are listed once, not once per input; a combobox is one row, and the extension's surfaces are not listed.
    expect(s.rows.filter((r) => r.label === "Preferred contact")).toHaveLength(1);
    expect(s.rows.filter((r) => r.label === "Tools you use (select all that apply)")).toHaveLength(1);
    expect(s.rows.filter((r) => r.label === "Team")).toHaveLength(1);
    expect(s.rows.some((r) => ["Extension box", "Report"].includes(r.label))).toBe(false);
  });

  test("foreign extension panel controls are ignored", async () => {
    mount(`<label for="fn">First name</label><input id="fn">${PAD}
      <div id="auto-job-widget"><label for="mine">Note to self</label><input id="mine"></div>
      <div id="simplify-jobs-root" class="simplify-jobs-panel"><label for="r1">Report</label><input id="r1"><label for="r2">Resume Score</label><input id="r2"></div>
      <div id="shadowhost"></div>`);
    const root = (byId("shadowhost") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<div id="applysmith-shadow"><label for="sh">Inside own shadow</label><input id="sh"></div>`;
    document.body.insertAdjacentHTML("beforeend", `<div class="panel-simplify-jobs-x"><div id="inner"></div></div>`);
    const nested = (document.getElementById("inner") as HTMLElement).attachShadow({ mode: "open" });
    nested.innerHTML = `<label for="deep">Deep foreign box</label><input id="deep">`;
    const labels = scanControls(document).map((i) => i.label.text);
    expect(labels).toEqual(["First name", "Referral code"]);

    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions).map((q) => q.text)).not.toContain("Report");
    expect(s.rows.map((r) => r.label)).toEqual(["First name", "Referral code"]);
    expect(s.events ?? []).toEqual([]);
    expect(byId("r1").value).toBe("");
  });

  test("unclaimed choice rows carry their options, a lone checkbox none", async () => {
    mount(`<fieldset><legend>Open-source track record?</legend><label><input type="radio" name="r" required> Yes</label><label><input type="radio" name="r"> No</label></fieldset>
      <label for="t">Team</label><select id="t"><option value="">Select</option><option>Core</option><option>Infra</option></select>
      <fieldset><legend>Tools</legend><label><input type="checkbox" name="k"> Go</label><label><input type="checkbox" name="k"> SQL</label></fieldset>
      <label><input type="checkbox" id="lone"> Send me news</label>`);
    const run = newRun();
    await unclaimedRows(run);
    const rows = Object.fromEntries(Array.from(run.rows.values()).map((r) => [r.label, r]));
    expect(rows["Open-source track record?"]).toMatchObject({ outcome: "skipped", reason: "no-lane-claimed", kind: "radio-group", options: ["Yes", "No"] });
    expect(rows["Team"]?.options).toEqual(["Core", "Infra"]);
    expect(rows["Tools"]?.options).toEqual(["Go", "SQL"]);
    expect(rows["Send me news"]?.options).toBeUndefined();
  });
});

describe("Stale prose", () => {
  test("stale profile value in a prose box is cleared", async () => {
    mount(`<label for="sum">Summary</label><textarea id="sum">Bachelor of Science</textarea>
      <label for="deg">Highest degree earned</label><textarea id="deg">Bachelor of Science</textarea>
      <label for="own">About you</label><textarea id="own">I enjoy writing small reliable tools.</textarea>
      <label for="yn">Do you agree</label><textarea id="yn">Yes</textarea>${PAD}`);
    const applicant = { ...ADA, history: { education: [{ school: "Example University", degree: "Bachelor of Science", field: "Computer Science" }] } };
    const engine = makeEngine(fakeHost());
    const s = summaryOf(await runFill(engine, { applicant }));
    expect(byId("sum").value).toBe("");
    expect(engine.log().some((l) => l.includes("stale cleared"))).toBe(true);
    expect(byId("deg").value).toBe("Bachelor of Science");
    expect(byId("own").value).toBe("I enjoy writing small reliable tools.");
    // The cleared box is reported as one nothing answered, not as a value the run wrote.
    expect(rowFor(s, "summary").outcome).not.toBe("filled");
    expect(rowFor(s, "summary").value ?? "").toBe("");
    // A second run finds nothing left to clear.
    const again = makeEngine(fakeHost());
    await runFill(again, { applicant });
    expect(again.log().some((l) => l.includes("stale cleared"))).toBe(false);
  });
});
