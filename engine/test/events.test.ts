// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Uncertainty events and the user's own edits; Sensitive controls and redacted fragments.
import { afterEach, describe, expect, test, vi } from "vitest";
import { makeRow } from "../src/answers";
import { unreachableFrameEvents } from "../src/frames";
import { uncertaintyEvents } from "../src/learning";
import type { LearningEvent, Resolution, Row } from "../src/protocol";
import { scanControls, type ControlInfo } from "../src/scan";
import { unclaimedRows } from "../src/summary";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, tick, trusted, newRun, runFillFaked } from "./helpers";
import { mountReactSelect } from "./library-fixtures";

/** A person types into a control: trusted key and input events, then the value. */
function type(el: HTMLInputElement, text: string): void {
  el.focus();
  trusted(new Event("keydown", { bubbles: true })) && el.dispatchEvent(trusted(new Event("keydown", { bubbles: true })));
  el.value = text;
  el.dispatchEvent(trusted(new Event("input", { bubbles: true })));
}

function leave(el: HTMLElement): void {
  el.dispatchEvent(trusted(new Event("focusout", { bubbles: true })));
}

afterEach(() => {
  vi.useRealTimers();
});

async function filled(html: string, table = {}, applicant = ADA) {
  mount(html);
  const engine = makeEngine(fakeHost(table));
  // A test on the fake clock fills on it too, so the watch's timers are the fake ones.
  const s = summaryOf(vi.isFakeTimers() ? await runFillFaked(engine, { applicant }) : await runFill(engine, { applicant }));
  return { engine, s };
}

const SCHOOL = `<label for="school">School</label><input id="school">${PAD}`;

describe("Uncertainty events", () => {
  test("One event for every control the fill could not answer: abstained, with its reason, wording, options and required flag, and no value", async () => {
    // The server refuses a whole event that holds more than 25 options or an option over 200 characters.
    const long = `Option 0, ${"a long description of the source ".repeat(8).trim()}`;
    const opts = Array.from({ length: 40 }, (_, i) => `<option>${i ? `Option ${i}` : long}</option>`).join("");
    const { s } = await filled(`<label for="src">How did you hear about us? *</label><select id="src" required><option value="">Select</option>${opts}</select>${PAD}`, {
      "how did you hear about us?": { outcome: "abstained", reason: "ask-me", terminal: true },
    });
    const ev = s.events?.find((e) => e.question === "How did you hear about us?") as LearningEvent;
    expect(ev).toMatchObject({ kind: "uncertainty", reason: "ask-me", required: true, controlKind: "select", labelSource: "label" });
    expect(ev.options).toHaveLength(25);
    expect(ev.options?.[0]).toBe(Array.from(long).slice(0, 200).join(""));
    expect(ev.eventId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(ev.after).toBeUndefined();
    expect(ev.before).toBeUndefined();
  });

  // The server learns a question with its section: a "Title" of an employment entry the row lane did not take is not a lone "Title".
  test("An event names the section captions around its control, outermost first and masked like the question; a lone control names none", async () => {
    const { s } = await filled(
      `<section aria-labelledby="we"><h2 id="we">Work Experience</h2><div class="entry"><h4>Entry #2</h4><label for="t">Title</label><input id="t" required></div></section>
       <label for="lone">Title</label><input id="lone" required>
       <fieldset><legend>References: write to hr@example.com</legend><label for="r">Reference name</label><input id="r" required></fieldset>${PAD}`,
      { title: { outcome: "abstained", reason: "no-entry" }, "reference name": { outcome: "abstained", reason: "no-entry" } },
    );
    const ofId = (id: string) => s.events?.find((e) => document.querySelector(e.selector ?? "") === byId(id));
    expect(ofId("t")).toMatchObject({ question: "Title", section: ["Work Experience", "Entry #2"] });
    expect(ofId("lone")?.question).toBe("Title");
    expect(ofId("lone")?.section).toBeUndefined();
    expect(ofId("r")?.section).toEqual(["References: write to [email]"]);
  });

  // Greenhouse's job board asks in react-select menus, closed again once read or answered: a fresh scan sees no options.
  const SPONSOR = "Will you now or in the future require visa sponsorship?";
  test.each<{ result: Partial<Resolution>; reason: string }>([
    { result: { outcome: "abstained", reason: "ambiguous" }, reason: "ambiguous" },
    { result: { outcome: "filled-review", value: "No" }, reason: "review-flagged" },
  ])("A closed listbox asked with its options names them in its $reason event", async ({ result, reason }) => {
    mount(PAD);
    mountReactSelect({ inputId: "question_7", label: SPONSOR, options: ["Yes", "No"] });
    const host = fakeHost({ [SPONSOR.toLowerCase()]: result });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions).find((q) => q.text === SPONSOR)?.options).toEqual(["Yes", "No"]);
    expect(s.events?.find((e) => e.question === SPONSOR)).toMatchObject({ reason, controlKind: "custom-listbox", options: ["Yes", "No"] });
  });

  test("A required listbox no lane claimed names the options the closing check read in its event", async () => {
    mount(PAD);
    const rs = mountReactSelect({ inputId: "question_9", label: "How did you first hear about this opportunity?", options: ["Job board", "Referral", "Other"] });
    rs.input.setAttribute("aria-required", "true");
    const run = newRun();
    await unclaimedRows(run);
    const ev = uncertaintyEvents(run).find((e) => e.question === "How did you first hear about this opportunity?");
    expect(ev).toMatchObject({ reason: "unrecognised", controlKind: "custom-listbox", options: ["Job board", "Referral", "Other"] });
    run.dispose();
  });

  test("Failed, review-flagged and unrecognised controls each raise their own reason; a failed write carries a redacted block", async () => {
    const { s } = await filled(
      `<div><label for="em">Years of experience with Go</label><input id="em" type="number"><div>Reach us at hr@example.com or +1 555 0100</div></div>
       <label for="why">Rate your Go</label><input id="why">
       <input id="blind" placeholder="">${PAD}`,
      {
        "years of experience with go": { outcome: "filled", value: "plenty" },
        "rate your go": { outcome: "filled-review", value: "Expert" },
      },
    );
    const by = (r: string) => s.events?.find((e) => e.reason === r);
    expect(by("failed")).toMatchObject({ kind: "uncertainty", question: "Years of experience with Go" });
    expect(by("review-flagged")).toMatchObject({ question: "Rate your Go" });
    expect(by("unrecognised")).toBeDefined();
    expect(by("failed")?.fragment).toBeDefined();
    expect(by("failed")?.fragment).not.toContain("hr@example.com");
    expect(by("failed")?.fragment).not.toContain("555 0100");
    expect(by("failed")?.fragment?.length).toBeLessThanOrEqual(4000);
  });

  test("Guard bail: a writer that declines because the control already holds the answer leaves a filled or prefilled row and no failed event", async () => {
    const { s } = await filled(`<label for="y">Years of experience with Go</label><input id="y" value="6">${PAD}`, { "years of experience with go": { outcome: "filled", value: "6" } });
    expect(s.rows.find((r) => r.label === "Years of experience with Go")?.outcome).toBe("prefilled");
    expect((s.events ?? []).filter((e) => e.reason === "failed")).toEqual([]);
  });

  test("No event for an abstained identity control, a control that now holds a value, or an unclaimed control the page already answered", async () => {
    const { s } = await filled(`<label for="em">Email</label><input id="em" type="email"><label for="k">Referral code</label><input id="k" value="REF-1"><input id="pre" aria-label="" value="x">`, {
      email: { outcome: "abstained", reason: "no-entry" },
    });
    expect(s.events ?? []).toEqual([]);
  });
});

describe("Event control kinds", () => {
  test("a textarea left unanswered is reported as a text control, the kind the server accepts", async () => {
    const { s } = await filled(`<label for="t">Why do you want to work here? *</label><textarea id="t" required></textarea>${PAD}`, {});
    const ev = (s.events ?? []).find((e) => e.question.startsWith("Why do you want"));
    expect(ev).toMatchObject({ kind: "uncertainty", controlKind: "text" });
  });
});

describe("Repeated rows raise no abstain event", () => {
  test("A blank work-history row control the matcher left unanswered raises no uncertainty, while an ordinary control still does", async () => {
    const { s } = await filled(
      `<label for="workHistory.companyName.0">Company Name</label><input id="workHistory.companyName.0">
       <label for="workHistory.positionTitle.0">Position</label><input id="workHistory.positionTitle.0">
       <label for="workHistory.reasonForLeaving.0">Reason for Leaving</label><input id="workHistory.reasonForLeaving.0">
       <label for="k">Referral code</label><input id="k">${PAD}`,
      {
        "company name": { outcome: "abstained", reason: "no-entry" },
        position: { outcome: "abstained", reason: "no-entry" },
        "reason for leaving": { outcome: "abstained", reason: "no-entry" },
        "referral code": { outcome: "abstained", reason: "no-entry" },
      },
    );
    expect((s.events ?? []).map((e) => e.question)).toEqual(["Referral code"]);
  });
});

describe("Already-satisfied events", () => {
  test("only required already-satisfied rows with a live control emit an event", async () => {
    const { s } = await filled(
      `<label for="y">Years of experience with Go *</label><input id="y" required value="6">
       <label for="o">Preferred team</label><input id="o" value="Platform">
       <label for="c">City of residence *</label><input id="c" required>${PAD}`,
      { "city of residence": { outcome: "filled", value: "Lyon" } },
    );
    expect(rowFor(s, "Years of experience").outcome).toBe("prefilled");
    expect(rowFor(s, "Preferred team").outcome).toBe("prefilled");
    expect(rowFor(s, "City of residence").outcome).toBe("filled");
    const satisfied = (s.events ?? []).filter((e) => e.reason === "already-satisfied");
    // The optional row and the row the run wrote emit nothing.
    expect(satisfied.map((e) => e.question)).toEqual(["Years of experience with Go"]);
    const ev = satisfied[0] as LearningEvent;
    expect(ev).toMatchObject({ kind: "uncertainty", fillOutcome: "prefilled", required: true, controlKind: "text", labelSource: "label" });
    expect(ev.selector).toBe(rowFor(s, "Years of experience").selector);
    expect(ev.fragment).toContain("Years of experience with Go");
    expect(ev.fragment).not.toContain("6");
    expect(ev.current).toBeUndefined();
    expect(ev.after).toBeUndefined();

    // A row whose control is gone, a row in another status and a row the run wrote emit nothing either.
    mount(`<label for="a">Notice period *</label><input id="a" required value="2 weeks">
      <label for="b">Salary expectation *</label><input id="b" required value="Open">
      <label for="d">Start date *</label><input id="d" required>
      <label for="e">Visa status *</label><input id="e" required value="Citizen">`);
    const run = newRun();
    const infos = scanControls(document);
    const row = (id: string, outcome: Row["outcome"]) => run.setRow(makeRow(infos.find((i) => i.el.id === id) as ControlInfo, "profile", outcome, {}));
    row("a", "prefilled");
    row("b", "prefilled");
    row("d", "skipped");
    row("e", "prefilled");
    byId("a").remove();
    const events = uncertaintyEvents(run, new Map([[infos.find((i) => i.el.id === "e")?.selector ?? "", "Citizen"]]));
    expect(events.filter((e) => e.reason === "already-satisfied").map((e) => e.question)).toEqual(["Salary expectation"]);
    run.dispose();
  });
});

describe("The user's own edits", () => {
  test("User fixes an abstained field: typing Example University into a school field the engine abstained on and leaving it captures one manual fill, the person's own answer, with no before value and the after value read back", async () => {
    const { engine } = await filled(SCHOOL, { school: { outcome: "abstained", reason: "no-entry" } });
    const school = document.getElementById("school") as HTMLInputElement;
    type(school, "Example University");
    leave(school);
    const events = engine.drainEvents();
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ kind: "manual-fill", after: "Example University", trustedInput: true, question: "School" });
    // The upload route takes an origin only on a correction and refuses a manual fill that carries one.
    expect(events[0]).not.toHaveProperty("origin");
    expect(events[0]?.before).toBeUndefined();
    expect(events[0]?.priorValue).toBeUndefined();
    expect(engine.drainEvents()).toEqual([]);
  });

  test("Typed field paused: West, a pause longer than any timer, Westbridge and leaving the field give one capture of Westbridge", async () => {
    const { engine } = await filled(SCHOOL, { school: { outcome: "abstained", reason: "no-entry" } });
    const school = document.getElementById("school") as HTMLInputElement;
    type(school, "West");
    await tick(150);
    expect(engine.drainEvents()).toEqual([]);
    type(school, "Westbridge");
    leave(school);
    const events = engine.drainEvents();
    expect(events.map((e) => e.after)).toEqual(["Westbridge"]);
  });

  test("Synthetic writes are not corrections: the engine's own writes and a page re-render raise no capture", async () => {
    const { engine } = await filled(`<label for="fn">First name</label><input id="fn">${PAD}`);
    const fn = document.getElementById("fn") as HTMLInputElement;
    fn.value = "";
    fn.dispatchEvent(new Event("input", { bubbles: true }));
    fn.dispatchEvent(new Event("focusout", { bubbles: true }));
    fn.value = "Ada";
    fn.dispatchEvent(new Event("change", { bubbles: true }));
    await tick(80);
    expect(engine.drainEvents()).toEqual([]);
  });

  test("A select or a box that ticks is captured a moment after it changed, with the engine's value as before", async () => {
    const { engine } = await filled(`<label for="src">How did you hear about us?</label><select id="src"><option value="">Select</option><option>Job board</option><option>Referral</option></select>${PAD}`, {
      "how did you hear about us?": { outcome: "filled", value: "Job board" },
    });
    const sel = document.getElementById("src") as HTMLSelectElement;
    sel.value = "Referral";
    sel.dispatchEvent(trusted(new Event("change", { bubbles: true })));
    expect(engine.drainEvents()).toEqual([]);
    await tick(90);
    const events = engine.drainEvents();
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ kind: "manual-fill", before: "Job board", priorValue: "Job board", after: "Referral" });
  });

  test("A box a script ticks raises no capture though Chrome marks its change trusted; one a person clicks does (Phenom)", async () => {
    const { engine } = await filled(`<label for="t">Willing to travel?</label><input type="checkbox" id="t">${PAD}`, {});
    const box = document.getElementById("t") as HTMLInputElement;
    box.checked = true;
    box.dispatchEvent(trusted(new Event("change", { bubbles: true })));
    await tick(90);
    expect(engine.drainEvents()).toEqual([]);
    box.checked = false;
    box.dispatchEvent(trusted(new Event("click", { bubbles: true })));
    box.dispatchEvent(trusted(new Event("change", { bubbles: true })));
    await tick(90);
    expect(engine.drainEvents()).toMatchObject([{ kind: "manual-fill" }]);
  });

  // The host's answer call writes after the fill, as the apply chain's page check does: what it wrote is the before of a later
  // edit, as the fill's own write is, and a call that wrote nothing leaves the fill's own.
  test.each([
    { name: "a write after the fill abstained", fill: { outcome: "abstained", reason: "no-entry" }, late: { outcome: "filled", value: "Job board" }, wrote: "filled" },
    { name: "a call that wrote nothing after the fill wrote", fill: { outcome: "filled", value: "Job board" }, late: { outcome: "abstained", reason: "no-entry" }, wrote: "abstained" },
  ] as const)("The before of an edit is the value the run left in the control: $name", async ({ fill, late, wrote }) => {
    const { engine, s } = await filled(`<label for="src">How did you hear about us?</label><select id="src"><option value="">Select</option><option>Job board</option><option>Referral</option></select>${PAD}`, {
      "how did you hear about us?": fill,
    });
    const reply = await engine.answer([{ selector: rowFor(s, "How did you hear").selector, ...late }]);
    expect(reply.results.map((r) => r.outcome)).toEqual([wrote]);
    const sel = document.getElementById("src") as HTMLSelectElement;
    sel.value = "Referral";
    sel.dispatchEvent(trusted(new Event("change", { bubbles: true })));
    await tick(90);
    expect(engine.drainEvents()).toMatchObject([{ kind: "manual-fill", before: "Job board", priorValue: "Job board", after: "Referral" }]);
  });

  test("Programmatic listbox change: the page changes a listbox's value with no trusted input just before, and nothing is recorded; with a trusted pointer just before, it is", async () => {
    mount(`<label for="u">University</label><button id="u" type="button" role="combobox" aria-haspopup="listbox">Select one</button>${PAD}`);
    const engine = makeEngine(fakeHost({}));
    await runFill(engine, { applicant: ADA });
    const btn = document.getElementById("u") as HTMLElement;
    btn.textContent = "Example University";
    await tick(120);
    expect(engine.drainEvents()).toEqual([]);
    document.dispatchEvent(trusted(new Event("pointerdown", { bubbles: true })));
    btn.textContent = "Sample College";
    await tick(60);
    expect(engine.drainEvents().map((e) => e.after)).toEqual(["Sample College"]);
  });

  test("Mid-typing superseded: a combobox read as uni and then committed as Example University leaves one capture holding Example University", async () => {
    // On the fake clock the two reads fall inside the trusted-input window however loaded the machine is.
    vi.useFakeTimers();
    mount(`<label for="u">University</label><button id="u" type="button" role="combobox" aria-haspopup="listbox">Select one</button>${PAD}`);
    const engine = makeEngine(fakeHost({}));
    await runFillFaked(engine, { applicant: ADA });
    const btn = document.getElementById("u") as HTMLElement;
    document.dispatchEvent(trusted(new Event("keydown", { bubbles: true })));
    btn.textContent = "uni";
    await vi.advanceTimersByTimeAsync(40);
    btn.textContent = "Example University";
    await vi.advanceTimersByTimeAsync(40);
    const events = engine.drainEvents();
    expect(events.map((e) => e.after)).toEqual(["Example University"]);
  });

  test("A box inside an open shadow root is captured as the person's edit, though the document hears the event at the host", async () => {
    mount(`<div id="host"></div>${PAD}`);
    const root = (document.getElementById("host") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<label for="school">School</label><input id="school">`;
    const engine = makeEngine(fakeHost({ school: { outcome: "abstained", reason: "no-entry" } }));
    await runFill(engine, { applicant: ADA });
    const school = root.getElementById("school") as HTMLInputElement;
    // A browser retargets an event that leaves a shadow tree to the host; only its composed path still names the box.
    const composed = (name: string) => Object.defineProperty(trusted(new Event(name, { bubbles: true, composed: true })), "target", { value: root.host });
    school.dispatchEvent(composed("keydown"));
    school.value = "Example University";
    school.dispatchEvent(composed("input"));
    school.dispatchEvent(composed("focusout"));
    expect(engine.drainEvents()).toMatchObject([{ kind: "manual-fill", after: "Example University", question: "School" }]);
  });

  test("A later value the capture filter drops takes the earlier capture of that control with it", async () => {
    const { engine } = await filled(SCHOOL, { school: { outcome: "abstained", reason: "no-entry" } });
    const school = document.getElementById("school") as HTMLInputElement;
    type(school, "Example University");
    leave(school);
    // The box holds its own label now: that is no answer, and "Example University" is not what it holds.
    type(school, "School");
    leave(school);
    expect(engine.drainEvents()).toEqual([]);
  });

  test("A rich-text edit carries its text as the answer and names no chosen option", async () => {
    const { engine } = await filled(`<label id="wl">Why this role?</label><div id="why" contenteditable="true" role="textbox" aria-labelledby="wl"></div>${PAD}`, {
      "why this role?": { outcome: "abstained", reason: "no-entry" },
    });
    const why = byId("why");
    why.textContent = "I like building compilers.";
    why.dispatchEvent(trusted(new Event("input", { bubbles: true })));
    leave(why);
    const [ev] = engine.drainEvents();
    expect(ev).toMatchObject({ kind: "manual-fill", after: "I like building compilers.", controlKind: "text" });
    expect(ev?.optionLabel).toBeUndefined();
  });

  test("Sensitive edit: editing a date of birth field and a gender select records both as edited with no value", async () => {
    vi.useFakeTimers();
    const { engine } = await filled(`<label for="dob">Date of birth</label><input id="dob"><label for="g">Gender</label><select id="g"><option value="">Select</option><option>Female</option><option>Male</option></select>${PAD}`);
    const dob = document.getElementById("dob") as HTMLInputElement;
    type(dob, "1990-04-02");
    leave(dob);
    const g = document.getElementById("g") as HTMLSelectElement;
    g.value = "Female";
    g.dispatchEvent(trusted(new Event("change", { bubbles: true })));
    await vi.advanceTimersByTimeAsync(90);
    const events = engine.drainEvents();
    expect(events).toHaveLength(2);
    for (const e of events) {
      expect(e.edited).toBe(true);
      expect(e.after).toBeUndefined();
      expect(e.fragment).toBeUndefined();
      expect(e.options).toBeUndefined();
    }
    expect(JSON.stringify(events)).not.toContain("1990");
    expect(JSON.stringify(events)).not.toContain("Female");
  });

  test("The applicant's own name, email and phone are edited and not recorded", async () => {
    const { engine } = await filled(`<label for="em">Email</label><input id="em" type="email"><label for="fn">First name</label><input id="fn">`);
    const em = document.getElementById("em") as HTMLInputElement;
    type(em, "someone@example.org");
    leave(em);
    const [ev] = engine.drainEvents();
    expect(ev).toMatchObject({ edited: true });
    expect(JSON.stringify(ev)).not.toContain("someone@example.org");
  });

  test("Submit never captured as a correction: a click on the page's Submit leaves only a scrubbed unsafe-control note with no value and no label", async () => {
    const { engine } = await filled(`<label for="fn">First name</label><input id="fn"><button id="go" type="button">Submit application to Example Corp</button>${PAD}`);
    document.getElementById("go")!.dispatchEvent(trusted(new MouseEvent("click", { bubbles: true })));
    const events = engine.drainEvents();
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ kind: "unsafe-control", question: "submit", controlKind: "text" });
    expect(events[0]?.after).toBeUndefined();
    expect(JSON.stringify(events[0])).not.toContain("Example Corp");
  });

  test("Password, one-time code, card and file controls produce only scrubbed notes", async () => {
    const { engine } = await filled(`<label for="fn">First name</label><input id="fn"><input id="pw" type="password" aria-label="Password"><input id="cc" autocomplete="cc-number" aria-label="Card"><input id="up" type="file">${PAD}`);
    document.getElementById("pw")!.dispatchEvent(trusted(new Event("input", { bubbles: true })));
    document.getElementById("cc")!.dispatchEvent(trusted(new Event("input", { bubbles: true })));
    document.getElementById("up")!.dispatchEvent(trusted(new Event("change", { bubbles: true })));
    const events = engine.drainEvents();
    expect(events.map((e) => e.question).sort()).toEqual(["file-upload", "mfa", "payment"]);
    expect(events.every((e) => e.kind === "unsafe-control" && e.after === undefined)).toBe(true);
  });

  test("A password or a card typed key by key leaves one note of its kind, not one per keystroke", async () => {
    const { engine } = await filled(`<label for="fn">First name</label><input id="fn"><input id="pw" type="password" aria-label="Password"><input id="cc" autocomplete="cc-number" aria-label="Card">${PAD}`);
    for (let i = 0; i < 8; i++) document.getElementById("pw")!.dispatchEvent(trusted(new Event("input", { bubbles: true })));
    for (let i = 0; i < 16; i++) document.getElementById("cc")!.dispatchEvent(trusted(new Event("input", { bubbles: true })));
    expect(engine.drainEvents().map((e) => e.question).sort()).toEqual(["mfa", "payment"]);
    // The notes were handed over; more typing in the same watch adds none.
    document.getElementById("pw")!.dispatchEvent(trusted(new Event("input", { bubbles: true })));
    expect(engine.drainEvents()).toEqual([]);
  });

  test("A new run removes the listeners of the earlier one and keeps what was captured; the page hiding ends the watch", async () => {
    const { engine } = await filled(SCHOOL, { school: { outcome: "abstained", reason: "no-entry" } });
    const school = document.getElementById("school") as HTMLInputElement;
    type(school, "Example University");
    leave(school);
    await runFill(engine, { applicant: ADA });
    const first = engine.drainEvents();
    expect(first.filter((e) => e.kind === "manual-fill")).toHaveLength(1);
    type(school, "Another");
    window.dispatchEvent(new Event("pagehide"));
    leave(school);
    const after = engine.drainEvents();
    expect(after.map((e) => e.after)).toEqual(["Another"]);
    type(school, "Third");
    leave(school);
    expect(engine.drainEvents()).toEqual([]);
  });

  // The apply chain's computer-use rung (owner decision F-9) types and clicks with trusted input while it holds the
  // engine's input: what it does is the run's own write, never a manual fill.
  const pointer = () => document.dispatchEvent(trusted(new Event("pointerdown", { bubbles: true })));
  const pick = () => (byId("u").textContent = "Sample College");
  const LISTBOX = `<label for="u">University</label><button id="u" type="button" role="combobox" aria-haspopup="listbox">Select one</button>${PAD}`;
  test.each([
    { name: "a box it types into and leaves", html: SCHOOL, during: () => (type(byId("school") as HTMLInputElement, "Example University"), leave(byId("school"))) },
    {
      name: "a select it changes",
      html: `<label for="src">How did you hear about us?</label><select id="src"><option value="">Select</option><option>Job board</option><option>Referral</option></select>${PAD}`,
      during: () => ((byId("src") as HTMLSelectElement).value = "Referral", byId("src").dispatchEvent(trusted(new Event("change", { bubbles: true })))),
    },
    { name: "a listbox it picks from just after a trusted pointer", html: LISTBOX, before: pointer, during: pick },
    { name: "a listbox the page redraws just after the hold", html: LISTBOX, before: pointer, holdMs: 0, after: pick },
  ])("The host's own input under a hold is no person's edit: $name", async ({ html, before, during, after, holdMs = 150 }) => {
    mount(html);
    const engine = makeEngine(fakeHost({}));
    await runFill(engine, { applicant: ADA });
    before?.();
    expect(engine.holdInput()).toBe(true);
    during?.();
    await tick(holdMs);
    engine.releaseInput([]);
    after?.();
    await tick(150);
    expect(engine.drainEvents()).toEqual([]);
  });

  test("The engine's own listbox write in an answer call is no person's pick, though a trusted key reached the page just before", async () => {
    mount(`<label for="u">University</label><button id="u" type="button" role="combobox" aria-haspopup="listbox">Select one</button>${PAD}`);
    const btn = document.getElementById("u") as HTMLElement;
    // The widget takes a pick a while after it is asked, as a page that saves first does: the watch looks in between.
    btn.addEventListener("click", () => {
      const list = document.createElement("ul");
      list.setAttribute("role", "listbox");
      list.innerHTML = `<li role="option">Example University</li>`;
      list.firstElementChild?.addEventListener("click", () => {
        btn.textContent = "Example University";
        list.remove();
      });
      btn.after(list);
    });
    const engine = makeEngine(fakeHost({}), { listboxPollMs: 1, trustedWindowMs: 60_000 });
    const s = summaryOf(await runFill(engine, { applicant: ADA }));
    document.dispatchEvent(trusted(new Event("keydown", { bubbles: true })));
    const reply = await engine.answer([{ selector: rowFor(s, "University").selector, outcome: "filled", value: "Example University" }]);
    expect(reply.results.map((r) => r.outcome)).toEqual(["filled"]);
    await tick(30);
    expect(engine.drainEvents().filter((e) => e.kind === "manual-fill")).toEqual([]);
  });

  test("The host's typing under a hold leaves each box the run's: a later answer writes it, a submission the typing provokes is held, and the person's next edit replaces the value the hold was released with", async () => {
    const { engine, s } = await filled(`<form id="f">${SCHOOL}</form>`, { school: { outcome: "abstained", reason: "no-entry" } });
    const school = byId("school") as HTMLInputElement;
    const submit = new Event("submit", { bubbles: true, cancelable: true });
    expect(engine.holdInput()).toBe(true);
    type(school, "Example University");
    leave(school);
    type(byId("pad") as HTMLInputElement, "REF-2");
    byId("f").dispatchEvent(submit);
    engine.releaseInput([{ selector: rowFor(s, "School").selector, outcome: "filled", value: "Example University" }]);
    expect(submit.defaultPrevented).toBe(true);
    const reply = await engine.answer([{ selector: rowFor(s, "Referral").selector, outcome: "filled", value: "REF-3" }]);
    expect(reply.results.map((r) => r.outcome)).toEqual(["filled"]);
    type(school, "Sample College");
    leave(school);
    expect(engine.drainEvents()).toMatchObject([{ kind: "manual-fill", before: "Example University", after: "Sample College" }]);
  });

  // The engine's own edit through the browser (a box that kept no value a script set, a rich editor) is the run's write.
  // Chrome's execCommand insertText edits the focused control as typing does: a trusted input, within maxlength, and a
  // trusted change owed to the box until it is left, which the handoff recorder would take for the person's.
  test.each([
    { name: "a box that keeps only a real edit", label: "Office code", control: `<input id="c" maxlength="5">` },
    { name: "a rich editor", label: "Extra notes", control: `<div id="c" contenteditable="true"></div>` },
  ])("The engine's own edit through the browser is no person's: $name", async ({ label, control }) => {
    mount(`<label for="c">${label}</label>${control}${PAD}`);
    const engine = makeEngine(fakeHost({}));
    const s = summaryOf(await runFill(engine, { applicant: ADA }));
    const c = byId("c") as HTMLInputElement;
    c.addEventListener("input", (e) => !e.isTrusted && c.localName === "input" && (c.value = ""));
    const owed = new Set<EventTarget>();
    let changes = 0;
    const exec = (cmd: string, _ui?: boolean, value = "") => {
      const el = document.activeElement as HTMLInputElement | null;
      if (cmd !== "insertText" || el !== c) return false;
      if (el.isContentEditable) el.textContent = value;
      else (el.value = value.slice(0, el.maxLength), owed.add(el));
      el.dispatchEvent(trusted(new InputEvent("input", { bubbles: true, inputType: "insertText", data: value })));
      return true;
    };
    const blurred = (e: Event) => owed.delete(e.target as Element) && (e.target as Element).dispatchEvent(trusted(new Event("change", { bubbles: true })));
    const changed = (e: Event) => e.isTrusted && changes++;
    Object.defineProperty(document, "execCommand", { value: exec, configurable: true });
    document.addEventListener("blur", blurred, true);
    document.addEventListener("change", changed, true);
    try {
      const selector = rowFor(s, label).selector;
      await engine.answer([{ selector, outcome: "filled", value: "NYC-12345" }]);
      const owedNow = changes;
      leave(c);
      c.blur();
      expect(changes).toBe(owedNow);
      expect(engine.drainEvents()).toEqual([]);
      if (c.isContentEditable) c.textContent = "";
      else c.value = "";
      expect((await engine.answer([{ selector, outcome: "filled", value: "SF-01" }])).results.map((r) => r.outcome)).toEqual(["filled"]);
    } finally {
      delete (document as { execCommand?: unknown }).execCommand;
      document.removeEventListener("blur", blurred, true);
      document.removeEventListener("change", changed, true);
    }
  });
});

describe("A box this run wrote and the page changed since", () => {
  // Oracle's résumé import lands after the fill and puts "JordanAvery" into the First Name the run wrote: the check's
  // second write was refused as the run's own existing value.
  test("is written again only when the host asks for it", async () => {
    mount(`<label for="fn">First Name</label><input id="fn">${PAD}`);
    const engine = makeEngine(fakeHost({}));
    const s = summaryOf(await runFill(engine, { applicant: ADA }));
    const fn = byId("fn") as HTMLInputElement;
    expect(fn.value).toBe("Ada");
    const selector = rowFor(s, "First Name").selector;
    fn.value = "AdaExample";
    const kept = await engine.answer([{ selector, outcome: "filled", value: "Ada" }]);
    expect(kept.results.map((r) => [r.outcome, r.reason])).toEqual([["prefilled", "existing-value"]]);
    expect(fn.value).toBe("AdaExample");
    const again = await engine.answer([{ selector, outcome: "filled", value: "Ada", overwrite: true }]);
    expect(again.results.map((r) => r.outcome)).toEqual(["filled"]);
    expect(fn.value).toBe("Ada");
  });
});

describe("The engine's edit through the browser lands only in its own box", () => {
  // Oracle's Last Name keeps only a real edit, and its validation sends the focus back to the first invalid field, so an
  // execCommand edit made after focusing Last Name went into First Name: "JordanAvery".
  test("A box whose focus the page moves on is never edited through another box", async () => {
    mount(`<label for="fn">First Name</label><input id="fn"><label for="ln">Last Name</label><input id="ln">${PAD}`);
    const fn = byId("fn") as HTMLInputElement;
    const ln = byId("ln") as HTMLInputElement;
    ln.addEventListener("input", (e) => !e.isTrusted && (ln.value = ""));
    ln.addEventListener("focus", () => fn.focus());
    // Chrome's insertText edits the focused box at its caret, here the end of the box the focus went to.
    const exec = (cmd: string, _ui?: boolean, value = "") => {
      const el = document.activeElement;
      if (cmd !== "insertText" || !(el instanceof HTMLInputElement)) return false;
      el.value += value;
      el.dispatchEvent(trusted(new InputEvent("input", { bubbles: true, inputType: "insertText", data: value })));
      return true;
    };
    Object.defineProperty(document, "execCommand", { value: exec, configurable: true });
    try {
      const s = summaryOf(await runFill(makeEngine(fakeHost({})), { applicant: ADA }));
      expect(fn.value).toBe("Ada");
      expect(rowFor(s, "Last Name").outcome).not.toBe("filled");
    } finally {
      delete (document as { execCommand?: unknown }).execCommand;
    }
  });
});

describe("Frames the fill could not enter", () => {
  test("failed frame yields an iframe event", async () => {
    const { engine } = await filled(`<label for="a">Favourite colour</label><input id="a">${PAD}`);
    const events = engine.frameReport([
      { frameId: "1", url: "https://forms.example.com/apply/123", outcome: "failed-inject", reason: "the browser refused the injection" },
      { frameId: "2", url: "https://ads.example.net/pixel", outcome: "skipped-denylist" },
      { frameId: "3", url: "https://boards.example.org/embed/job", outcome: "admitted-registry" },
      { frameId: "4", url: "about:blank", outcome: "failed-no-listener", reason: "the engine did not answer" },
    ]);
    // Only a frame that FAILED is an event; one skipped on purpose or entered is not.
    expect(events).toHaveLength(2);
    const [inject, blank] = events as [LearningEvent, LearningEvent];
    expect(inject).toMatchObject({
      kind: "frame-unreachable",
      controlKind: "iframe",
      reason: "failed-inject",
      question: "Application form frame could not be reached: https://forms.example.com/apply/123",
    });
    expect(inject.eventId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(inject.url).toBe(`${location.origin}${location.pathname}`);
    // Nothing of a value, an option or the failure text leaves with it.
    for (const k of ["options", "current", "before", "after", "fragment", "selector"] as const) expect(inject[k]).toBeUndefined();
    expect(JSON.stringify(inject)).not.toContain("refused");
    expect(blank.question).toBe("Application form frame could not be reached: about:blank");
  });

  test("A frame URL's query never reaches the event", () => {
    const evs = unreachableFrameEvents(
      [{ frameId: "1", url: "https://forms.example.com/t/abc?email=a%40example.com#x", outcome: "failed-probe" }],
      { host: "jobs.example.com", url: "https://jobs.example.com/apply?token=secret" },
      () => "00000000-0000-4000-8000-000000000000",
      1,
    );
    expect(evs[0]).toMatchObject({ host: "jobs.example.com", url: "https://jobs.example.com/apply", question: "Application form frame could not be reached: https://forms.example.com/t/abc" });
    expect(JSON.stringify(evs)).not.toMatch(/secret|example\.com#|%40/);
  });
});
