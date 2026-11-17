// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for application boards (Lever, Rippling, Workable, Breezy, JazzHR, BambooHR, Jobvite,
// SmartRecruiters, Ashby details).
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { singleFlight } from "../src/flight";
import { installHelper } from "../src/mainworld/helper";
import { MainWorld } from "../src/mainworld-client";
import type { Win } from "../src/dom";
import { readInfo, scanControls } from "../src/scan";
import { ADA, PAD, fakeHost, infoOf, makeEngine, mount, runFill, summaryOf, write, uninstall } from "./helpers";
import { defineSplInput, mountLeverLocation } from "./ats-fixtures";

const win = window as unknown as Win;
const NONCE = "board-nonce";
beforeEach(uninstall);
afterEach(uninstall);

describe("Lever", () => {
  test("Lever hidden location: a visible Durham with an empty hidden input is not filled; after a suggestion is picked the hidden input is populated and the row is filled", async () => {
    mount(PAD);
    const l = mountLeverLocation({ places: ["Durham, NC, USA", "Durham, England, UK", "Raleigh, NC, USA"] });
    l.input.value = "Durham";
    expect(readInfo(infoOf("location-input"))).toBe("");
    l.input.value = "";
    const r = await write("location-input", "Durham, NC, USA");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Durham, NC, USA" });
    expect(l.hidden.value).not.toBe("");
    expect(readInfo(infoOf("location-input"))).toBe("Durham, NC, USA");
  });

  test("Suggestion choice never takes the first row blindly: no suggestion holding the city leaves the box unfilled", async () => {
    mount(PAD);
    const l = mountLeverLocation({ places: ["Durham, England, UK", "Durham, NC, USA"] });
    const r = await write("location-input", "Durham, NC, USA");
    expect(l.clicks).toEqual(["Durham, NC, USA"]);
    expect(r.outcome).toBe("filled");
    mount(PAD);
    const none = mountLeverLocation({ places: ["Raleigh, NC, USA"] });
    const miss = await write("location-input", "Durham, NC, USA");
    expect(miss.outcome).not.toBe("filled");
    expect(none.hidden.value).toBe("");
  });

  test("A search that outlasts the wait is waited for while its spinner shows; with no spinner the wait ends as before", async () => {
    mount(PAD);
    // Aledade (live): Lever's place search answered about 3 s after the last key, past the wait.
    const slow = mountLeverLocation({ places: ["Charleston, SC, USA", "Charleston, WV, USA"], lagMs: 400, spinner: true });
    const r = await write("location-input", "Charleston, South Carolina, United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Charleston, SC, USA" });
    expect(slow.hidden.value).not.toBe("");
    mount(PAD);
    const quiet = mountLeverLocation({ places: ["Charleston, SC, USA"], lagMs: 400 });
    const miss = await write("location-input", "Charleston, South Carolina, United States");
    expect(miss.outcome).not.toBe("filled");
    expect(quiet.hidden.value).toBe("");
  });

});

describe("Ashby details", () => {
  test("A react-datepicker receives one parsable date, the end or the start of a range by its label, or is refused", async () => {
    mount(`<div class="react-datepicker__input-container"><label for="grad">Graduation date</label><input id="grad" type="text"></div>
      <div class="react-datepicker__input-container"><label for="start">Start date</label><input id="start" type="text" placeholder="DD/MM/YYYY"></div>`);
    expect((await write("grad", "Aug 2024 - May 2026")).value).toBe("05/01/2026");
    expect((await write("start", "Aug 2024 - May 2026")).value).toBe("01/08/2024");
    mount(`<div class="react-datepicker__input-container"><label for="grad">Graduation date</label><input id="grad" type="text"></div>`);
    expect(await write("grad", "2026")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
    expect((document.getElementById("grad") as HTMLInputElement).value).toBe("");
  });

  // OpenAI (Ashby, live): the "..." of "Pick date..." is no date separator.
  test("A react-datepicker whose placeholder trails off in dots receives a slash date", async () => {
    mount(`<div class="react-datepicker__input-container"><label for="start">When can you start a new role?</label><input id="start" type="text" placeholder="Pick date..."></div>
      <div class="react-datepicker__input-container"><label for="eu">Start date</label><input id="eu" type="text" placeholder="DD.MM.YYYY"></div>`);
    expect((await write("start", "2026-11-03")).value).toBe("11/03/2026");
    expect((await write("eu", "2026-11-03")).value).toBe("03.11.2026");
  });

  test("A react-datepicker box is asked as a date (Harvey's Graduation Date)", () => {
    mount(`<div class="react-datepicker-wrapper"><label for="grad">Graduation Date</label><div class="react-datepicker__input-container"><input id="grad" type="text" placeholder="Pick date..."></div></div>${PAD}`);
    expect(scanControls(document).find((i) => i.el.id === "grad")).toMatchObject({ kind: "text", inputType: "date" });
  });
});

describe("JazzHR and Workable", () => {
  test("JazzHR checkbox pair: two checkboxes named q1-1 and q1-2 in one group, answer No, check only the No box and read the group as No", async () => {
    mount(`<div class="form-group"><label class="question">Are you over 18?</label>
      <label><input type="checkbox" name="q1-1" class="resumator-questionnaire-checkbox" id="y"> Yes</label>
      <label><input type="checkbox" name="q1-2" class="resumator-questionnaire-checkbox" id="n"> No</label></div>${PAD}`);
    const infos = scanControls(document).filter((i) => i.members.length > 0);
    const pair = infos.find((i) => i.members.some((m) => m.id === "y"))!;
    expect(pair.members).toHaveLength(2);
    expect(pair.label.text).toBe("Are you over 18?");
    const host = fakeHost({ "are you over 18?": { outcome: "filled", value: "No" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect((document.getElementById("y") as HTMLInputElement).checked).toBe(false);
    expect((document.getElementById("n") as HTMLInputElement).checked).toBe(true);
    expect(s.rows.find((r) => r.label === "Are you over 18?")).toMatchObject({ outcome: "filled", value: "No" });
  });

  test("Workable: a Choose file label with a button role is upload state, not a question", () => {
    mount(`<label for="cv" role="button">Choose file</label><input id="cv" type="file"><label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b">`);
    const questions = scanControls(document).filter((i) => i.kind !== "file").map((i) => i.label.text);
    expect(questions).toEqual(["First name", "Last name"]);
    const file = scanControls(document).find((i) => i.kind === "file")!;
    expect(file.label.text).not.toMatch(/choose file/i);
  });
});

describe("Rippling, BambooHR, Jobvite, Breezy", () => {
  test("Rippling: the applicant's value wins over the parser's guess for the current company", async () => {
    mount(`<label for="co">Current company</label><input id="co" value="Wrong Parse Inc">${PAD}`);
    const host = fakeHost({ "current company": { outcome: "filled", value: "Example Corp" } });
    await runFill(makeEngine(host));
    expect((document.getElementById("co") as HTMLInputElement).value).toBe("Example Corp");
  });

  test("Rippling: a field with no label takes its question text from the nearest container", () => {
    mount(`<div class="field"><div class="title">What city are you in?</div><input id="x"></div>${PAD}`);
    expect(scanControls(document).find((i) => i.el.id === "x")?.label.text).toBe("What city are you in?");
  });

  test("Breezy: a hidden application-submitted panel is not taken for the page's heading", async () => {
    mount(`<h1 style="display:none">Application Submitted</h1><h1>Staff Engineer</h1><label for="a">First name</label><input id="a"><label for="y">Years of experience with Go</label><input id="y">`);
    const host = fakeHost();
    await runFill(makeEngine(host), { posting: {} });
    expect(host.matchRequests[0]?.posting.title).toBe("Staff Engineer");
  });
});

describe("SmartRecruiters", () => {
  test("SmartRecruiters component: a shadow-DOM field that ignores synthetic input events accepts its own change method, and the value is read back from the component", async () => {
    defineSplInput();
    mount(`<spl-input id="host" label="First name"></spl-input><spl-input id="h2" label="Last name"></spl-input>`);
    installHelper(win, NONCE, "h1");
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    const s = summaryOf(await runFill(engine, { applicant: ADA }));
    const host = document.getElementById("host") as unknown as { calls: unknown[]; shadowRoot: ShadowRoot };
    expect(host.calls).toEqual(["Ada"]);
    expect((host.shadowRoot.querySelector("input") as HTMLInputElement).value).toBe("Ada");
    expect(s.rows.find((r) => r.label === "First name")).toMatchObject({ outcome: "filled", value: "Ada" });
  });

  test("Without the helper a component is reported unavailable, not filled by events it ignores", async () => {
    defineSplInput();
    mount(`<spl-input id="host" label="First name"></spl-input><spl-input id="h2" label="Last name"></spl-input>`);
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));
    expect(s.rows.find((r) => r.label === "First name")).toMatchObject({ outcome: "failed", reason: "binding-unavailable" });
  });

  test("A date reaches a SmartRecruiters datepicker as a real Date in local time", async () => {
    defineSplInput("spl-datepicker");
    mount(`<spl-datepicker id="d" label="Date available"></spl-datepicker>${PAD}`);
    installHelper(win, NONCE, "h1");
    const engine = makeEngine(fakeHost({ "date available": { outcome: "filled", value: "2026-09-14" } }));
    engine.useHelper(NONCE);
    await runFill(engine);
    const host = document.getElementById("d") as unknown as { calls: unknown[] };
    expect(host.calls[0]).toBeInstanceOf(Date);
    expect((host.calls[0] as Date).getDate()).toBe(14);
  });

  test("The Office location field is never touched", async () => {
    defineSplInput();
    mount(`<spl-input id="o" label="Office location"></spl-input><spl-input id="n" label="First name"></spl-input><spl-input id="m" label="Last name"></spl-input>`);
    installHelper(win, NONCE, "h1");
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    await runFill(engine, { applicant: ADA });
    expect((document.getElementById("o") as unknown as { calls: unknown[] }).calls).toEqual([]);
  });

  test("Concurrent SmartRecruiters reconcile: two requests together run one pass and both receive its result", async () => {
    installHelper(win, NONCE, "h1");
    const a = new MainWorld(win, NONCE);
    const b = new MainWorld(win, NONCE);
    let runs = 0;
    const work = async () => {
      runs++;
      await new Promise((r) => setTimeout(r, 30));
      return "reconciled";
    };
    const [x, y] = await Promise.all([singleFlight(a, "sr-reconcile", work), singleFlight(b, "sr-reconcile", work)]);
    expect(runs).toBe(1);
    expect([x, y]).toEqual(["reconciled", "reconciled"]);
    // A later request runs again.
    expect(await singleFlight(a, "sr-reconcile", work)).toBe("reconciled");
    expect(runs).toBe(2);
    a.dispose();
    b.dispose();
  });
});
