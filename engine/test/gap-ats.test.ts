// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: File upload and résumé-first ordering; Drivers for enterprise application suites.
import { describe, expect, test } from "vitest";
import { ADA, byId, fakeHost, makeEngine, mount, PAD, rowFor, runFill, setUrl, summaryOf } from "./helpers";

describe("Empty résumé slot", () => {
  test("An empty résumé slot is listed as a required row when the run has a résumé to attach, and the row is the one the page asks for", async () => {
    mount(`<section><span id="up-title">Resume/CV</span><div><input id="up" type="file" aria-labelledby="up-title"></div></section>${PAD}`);
    Object.defineProperty(byId("up"), "files", { configurable: true, get: () => ({ length: 0 }), set: () => {} });
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(rowFor(s, "Resume")).toMatchObject({ kind: "file", outcome: "skipped", required: true });
    expect(s.counts.requiredUnfilled).toBe(1);
  });

  test("Without a résumé to attach the slot is not made a required row", async () => {
    mount(`<section><span id="up-title">Resume/CV</span><div><input id="up" type="file" aria-labelledby="up-title"></div></section>${PAD}`);
    const host = fakeHost();
    host.resume = async () => null;
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(s.rows.find((r) => r.kind === "file")?.required ?? false).toBe(false);
    expect(s.counts.requiredUnfilled).toBe(0);
  });
});

describe("Workday question text", () => {

  test("A legend that hydrates late is read after the wait, not the page chrome around it", async () => {
    setUrl("https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply");
    mount(`<p>Back to Job Posting. Indicates a required field</p>${PAD}
      <div data-automation-id="formField-q1"><fieldset><legend></legend><label><input type="radio" name="q1" value="Yes"> Yes</label><label><input type="radio" name="q1" value="No"> No</label></fieldset></div>`);
    setTimeout(() => ((document.querySelector("legend") as HTMLElement).textContent = "Are you legally authorized to work here?"), 8);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const texts = host.matchRequests.flatMap((r) => r.questions.map((q) => q.text));
    expect(texts).toContain("Are you legally authorized to work here?");
  });

  test("A 3000-character legend yields its leading question, and its required marker is kept", async () => {
    const detail = "Additional guidance about this question follows here. ".repeat(60);
    mount(`${PAD}<fieldset><legend>Have you ever been employed by Example Corp? ${detail} *</legend><label><input type="radio" name="q2" value="Yes"> Yes</label><label><input type="radio" name="q2" value="No"> No</label></fieldset>`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host)));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => (x.options?.length ?? 0) > 0);
    expect(q?.text).toBe("Have you ever been employed by Example Corp?");
    expect(q?.required).toBe(true);
    expect(s.rows.some((r) => r.label === "Have you ever been employed by Example Corp?")).toBe(true);
  });

  test("Control furniture such as Select One Required is never a question", async () => {
    mount(`${PAD}<button id="b" type="button" aria-haspopup="listbox" aria-label="Select One Required">Select One Required</button>`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    expect(host.matchRequests.flatMap((r) => r.questions).some((q) => /select one/i.test(q.text))).toBe(false);
  });
});

describe("ADP county after state", () => {
  const ADP = "https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html";

  /** Two dijit selects: the county list is long until a state is chosen, and short after it. */
  function mountPair(): { hidden: (name: string) => string } {
    mount(`<div><label id="co-lbl">County</label><table id="co" role="listbox" widgetid="dijit_form_Select_1" aria-haspopup="true" aria-labelledby="co-lbl" class="dijit dijitSelect"><tbody><tr><td class="dijitButtonText">Please Specify</td></tr></tbody></table><input type="hidden" name="county" value=" "></div>
      <div><label id="st-lbl">State</label><table id="st" role="listbox" widgetid="dijit_form_Select_2" aria-haspopup="true" aria-labelledby="st-lbl" class="dijit dijitSelect"><tbody><tr><td class="dijitButtonText">Please Specify</td></tr></tbody></table><input type="hidden" name="state" value=" "></div>${PAD}<label for="fn2">First name</label><input id="fn2"><label for="em2">Email</label><input id="em2">`);
    let stateChosen = false;
    const wire = (id: string, widget: string, options: () => string[], onPick: (l: string) => void, name: string) => {
      const trigger = byId(id);
      const hidden = document.querySelector(`input[name=${name}]`) as HTMLInputElement;
      trigger.addEventListener("click", () => {
        document.getElementById(`${widget}_dropdown`)?.remove();
        const pop = document.createElement("div");
        pop.id = `${widget}_dropdown`;
        pop.innerHTML = options().map((l) => `<div class="dijitMenuItem" role="option">${l}</div>`).join("");
        document.body.appendChild(pop);
        Array.from(pop.children).forEach((row) =>
          row.addEventListener("click", () => {
            const label = row.textContent ?? "";
            hidden.value = label;
            (trigger.querySelector(".dijitButtonText") as HTMLElement).textContent = label;
            onPick(label);
            pop.remove();
          }),
        );
      });
    };
    wire("co", "dijit_form_Select_1", () => (stateChosen ? ["Durham County", "Wake County"] : ["Aaa County", "Bbb County"]), () => {}, "county");
    wire("st", "dijit_form_Select_2", () => ["North Carolina", "Ohio"], () => (stateChosen = true), "state");
    return { hidden: (n) => (document.querySelector(`input[name=${n}]`) as HTMLInputElement).value };
  }

  test("County options are read after the state commits: the state is written first and the county list is the one for that state", async () => {
    setUrl(ADP);
    const p = mountPair();
    const host = fakeHost({ county: { outcome: "filled", value: "Durham County" }, state: { outcome: "filled", value: "North Carolina" } });
    await runFill(makeEngine(host), { applicant: ADA });
    expect(p.hidden("state")).toBe("North Carolina");
    expect(p.hidden("county")).toBe("Durham County");
  });
});

describe("Workday question groups and stale follow-ups", () => {
  test("A fieldset-declared checkbox group of unnamed boxes is one question under its legend, and a box Workday disabled is not an option", async () => {
    mount(`${PAD}<label for="fn3">First name</label><input id="fn3"><label for="em3">Email</label><input id="em3"><div data-automation-id="formField-q"><fieldset data-automation-id="q-CheckboxGroup"><legend>Which of these apply to you? (select all that apply)</legend>
      <div><input type="checkbox" id="c1" required><label for="c1">Veteran</label></div><div><input type="checkbox" id="c2"><label for="c2">Reservist</label></div><div><input type="checkbox" id="c3" disabled><label for="c3">Retired</label></div></fieldset></div>`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const qs = host.matchRequests.flatMap((r) => r.questions).filter((q) => /apply to you/i.test(q.text));
    expect(qs).toHaveLength(1);
    expect(qs[0]?.options).toEqual(["Veteran", "Reservist"]);
  });

  test("A stale N/A that a draft left in a visa follow-up text area is replaced once, and a narrative text area is not", async () => {
    mount(`${PAD}<label for="visa">If you need a visa, which type?</label><textarea id="visa">N/A</textarea>
      <label for="story">Tell us about yourself</label><textarea id="story">My own draft</textarea>`);
    const host = fakeHost({ "if you need a visa, which type?": { outcome: "filled", value: "No visa needed" }, "tell us about yourself": { outcome: "filled", value: "Replaced" } });
    await runFill(makeEngine(host));
    expect(byId<HTMLTextAreaElement>("visa").value).toBe("No visa needed");
    expect(byId<HTMLTextAreaElement>("story").value).toBe("My own draft");
  });
});

describe("ADP Dojo field labels", () => {
  test("The help line inside a Dojo field's label is not part of the question", async () => {
    mount(`${PAD}<label for="fn4">First name</label><input id="fn4"><label for="em4">Email</label><input id="em4">
      <div class="element"><label id="gl" for="gender">Gender <span class="indentedButtonSubText">Select the best answer</span></label><input id="gender" class="dijitInputInner" type="text" role="combobox" aria-autocomplete="list"></div>`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => /gender/i.test(x.text));
    expect(q?.text).toBe("Gender");
  });
});

describe("Dayforce committed cards", () => {

  /** An education section whose parse, on upload, commits these cards; each Delete Record asks for a confirmation. */
  const parsedCards = (texts: string[]): string[] => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em"><label for="cv">Resume</label><input id="cv" type="file" accept=".pdf">
      <h3>Education</h3><ul id="cards"></ul>`);
    const presses: string[] = [];
    const card = (text: string) => {
      const li = document.createElement("li");
      li.innerHTML = `<span>${text}</span><button type="button">Delete Record</button>`;
      li.querySelector("button")!.addEventListener("click", () => {
        presses.push(`delete:${text}`);
        const dlg = document.createElement("div");
        dlg.setAttribute("role", "dialog");
        dlg.innerHTML = `<h2>Delete this education record?</h2><button type="button" class="ok">OK</button><button type="button">Cancel</button>`;
        dlg.querySelector(".ok")!.addEventListener("click", () => {
          presses.push("confirm");
          dlg.remove();
          li.remove();
        });
        document.body.append(dlg);
      });
      byId("cards").append(li);
    };
    byId("cv").addEventListener("change", () => setTimeout(() => texts.forEach(card), 3));
    return presses;
  };
  const cardsLeft = () => Array.from(document.querySelectorAll("#cards li span")).map((s) => s.textContent);

  test("A committed education card the parse made that names no profile entry is deleted through Delete Record and its confirmation; the matching card stays", async () => {
    setUrl("https://jobs.dayforcehcm.com/en-US/example/CANDIDATEPORTAL/jobs/1");
    const presses = parsedCards(["Fake Institute of Nothing", "Example University Bachelor of Science"]);
    await runFill(makeEngine(fakeHost()), { applicant: { ...ADA, history: { education: [{ school: "Example University", degree: "Bachelor of Science", field: "Computer Science" }] } } });
    expect(cardsLeft()).toEqual(["Example University Bachelor of Science"]);
    expect(presses).toEqual(["delete:Fake Institute of Nothing", "confirm"]);
  });

  test("A card names an entry by the entry's whole name: an entry with no school names no card, and MIT is not inside Summit", async () => {
    setUrl("https://jobs.dayforcehcm.com/en-US/example/CANDIDATEPORTAL/jobs/1");
    parsedCards(["Fake Institute of Nothing", "Summit Academy", "MIT Master of Science"]);
    await runFill(makeEngine(fakeHost()), { applicant: { ...ADA, history: { education: [{ school: "", degree: "Bachelor of Science" }, { school: "MIT", degree: "Master of Science" }] } } });
    expect(cardsLeft()).toEqual(["MIT Master of Science"]);
  });

  test("A card that stood on the page before the run is left alone even when it matches no entry", async () => {
    setUrl("https://jobs.dayforcehcm.com/en-US/example/CANDIDATEPORTAL/jobs/1");
    mount(`<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em">
      <h3>Education</h3><ul id="cards"><li><span>Old School I Typed</span><button type="button">Delete Record</button></li></ul>`);
    let pressed = 0;
    document.querySelector("#cards button")!.addEventListener("click", () => pressed++);
    await runFill(makeEngine(fakeHost()), { applicant: { ...ADA, history: { education: [{ school: "Example University", degree: "Bachelor of Science" }] } } });
    expect(pressed).toBe(0);
  });
});
