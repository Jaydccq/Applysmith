// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Frame fan-out decisions (registry, deny-list); Page API (log, reader); Job-site and application-form
// detection (embedded reveal); Pipeline stages and their order (value reader).
import { describe, expect, test } from "vitest";
import { revealControl } from "../src/reveal";
import { readInfo, scanControls } from "../src/scan";
import { byId, fakeHost, makeEngine, mount, runFill, setUrl, summaryOf, tick } from "./helpers";

describe("Embedded reveal", () => {
  test("application tab activated, anchor never clicked", async () => {
    // Only a button opens the form; a link, a submit control and the tab already selected do not.
    mount(`<a href="#x" id="link">Apply now</a><form><button id="sub" type="submit">Apply for this Job</button></form>
      <div role="tablist"><a role="tab" aria-selected="true" id="on">Application</a><a role="tab" aria-selected="false" id="other">Application form help</a></div>`);
    expect(revealControl(document)).toBeNull();
    mount(`<div role="tablist"><button role="tab" aria-selected="true" id="o">Overview</button><button role="tab" aria-selected="false" id="a">Application</button></div><a href="#y">Apply now</a>`);
    expect(revealControl(document)?.id).toBe("a");
    mount(`<button id="b">Apply for this Job</button>`);
    expect(revealControl(document)?.id).toBe("b");

    setUrl("https://jobs.ashbyhq.com/example/abc");
    // An overview with no controls: the tab is pressed once and the form that appears is filled.
    mount(`<div role="tablist"><a role="tab" aria-selected="true">Overview</a><a role="tab" aria-selected="false" id="t2" href="#application">Application</a></div>
      <a id="anchor" href="#apply">Apply now</a><main id="body">Example Corp is hiring.</main>`);
    const pressed: string[] = [];
    document.addEventListener("click", (e) => pressed.push((e.target as HTMLElement).id), true);
    byId("t2").addEventListener("click", () => {
      setTimeout(() => {
        byId("body").innerHTML = `<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em" type="email">`;
      }, 20);
    });
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(pressed).toEqual(["t2"]);
    expect(byId("fn").value).toBe("Ada");
    expect(s.counts.filled).toBeGreaterThanOrEqual(2);

    // A frame that already has its controls is not touched, and one with nothing to press waits and then reports no form.
    mount(`<label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b"><button id="go" type="button">Apply now</button>`);
    const again: string[] = [];
    byId("go").addEventListener("click", () => again.push("go"));
    await runFill(makeEngine(fakeHost()));
    expect(again).toEqual([]);

    mount(`<main>Example Corp is hiring.</main>`);
    const none = makeEngine(fakeHost(), { revealWaitMs: 100, revealPollMs: 10 });
    const empty = summaryOf(await runFill(none));
    expect(empty.notAnApplication).toBe(true);
    expect(none.log().some((l) => l.includes("reveal") && l.includes("no-fields"))).toBe(true);
    await tick(1);
  });
});

describe("One value reader", () => {
  test("checkbox group and whitespace-value select read back", async () => {
    mount(`<label for="st">Status</label><select id="st"><option value=" "> </option><option>Active</option></select>
      <fieldset><legend>Languages (select all that apply)</legend>
        <label><input type="checkbox" name="l" value="en" checked> English</label><label><input type="checkbox" name="l" value="fr"> French</label>
        <label><input type="checkbox" name="l" value="de" checked> German</label></fieldset>
      <fieldset><legend>Preferred contact</legend>
        <label><input type="radio" name="c" value="e"> Email</label><label><input type="radio" name="c" value="p" checked> Phone</label></fieldset>
      <div role="combobox" aria-label="Team"><input id="team" value="Platform"></div>
      <label for="nick">Nickname</label><input id="nick" value="Ace">`);
    const infos = scanControls(document);
    const byLabel = (label: string) => infos.find((i) => i.label.text === label) as (typeof infos)[number];
    expect(readInfo(byLabel("Status"))).toBe("");
    expect(readInfo(byLabel("Languages (select all that apply)"))).toBe("English, German");
    expect(readInfo(byLabel("Preferred contact"))).toBe("Phone");
    expect(readInfo(byLabel("Team"))).toBe("Platform");

    // The inventory, the summary rows and an answer call read the same strings.
    const engine = makeEngine(fakeHost());
    const inventory = Object.fromEntries(engine.inspect().map((r) => [r.label, r.value]));
    expect(inventory).toMatchObject({ "Languages (select all that apply)": "English, German", "Preferred contact": "Phone", Team: "Platform", Nickname: "Ace" });
    expect(inventory.Status).toBeUndefined();
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: { candidate: {} } }));
    for (const label of ["Languages (select all that apply)", "Preferred contact", "Team", "Nickname"]) {
      expect(s.rows.find((r) => r.label === label)?.value, label).toBe(inventory[label]);
    }
    expect(s.rows.find((r) => r.label === "Status")?.value ?? "").toBe("");
    const reply = await engine.answer([{ selector: byLabel("Team").selector, outcome: "filled", value: "Platform" }]);
    expect(reply.results[0]).toMatchObject({ outcome: "prefilled", value: inventory.Team });
  });
});
