// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for application boards (SmartRecruiters reconcile and late-parse sweep); Drivers for HR-suite application systems (SmartRecruiters).
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import type { Win } from "../src/dom";
import { reqEvent, resEvent } from "../src/mainworld/channel";
import { installHelper } from "../src/mainworld/helper";
import type { ApplicantSnapshot } from "../src/protocol";
import { defineSplInput } from "./ats-fixtures";
import type { Timing } from "../src/env";
import { MainWorld } from "../src/mainworld-client";
import { writeSrComponent } from "../src/sr";
import { ctx, fakeHost, FAST, makeEngine, mount, rowFor, runFill, setUrl, summaryOf, until, uninstall } from "./helpers";

const win = window as unknown as Win;
const NONCE = "sr-nonce";

beforeEach(() => {
  defineSplInput("spl-input");
  defineSplInput("spl-datepicker");
  setUrl("https://jobs.smartrecruiters.com/Example/743999-engineer");
  uninstall();
});
afterEach(() => {
  uninstall();
});

const PROFILE: ApplicantSnapshot = {
  candidate: { fullName: "Alex Example", firstName: "Alex", lastName: "Example", email: "alex@example.com" },
  history: {
    employment: [
      { employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03-01", current: true },
      { employer: "Sample Works", title: "Engineer", startDate: "2017-06-01", endDate: "2021-02-01" },
    ],
    education: [{ school: "Example University", degree: "Bachelor of Science", field: "Computer Science", startDate: "2013-09-01", endDate: "2017-05-01" }],
  },
};

interface Page {
  cards: (section: "exp" | "edu") => string[];
  addCard: (section: "exp" | "edu", text: string) => void;
  /** The values each saved editor held, by component label. */
  saved: Record<string, unknown>[];
  presses: string[];
}

const val = (c: Element): string => (c.shadowRoot?.querySelector("input") as HTMLInputElement | null)?.value ?? "";

/** Two history sections of cards with their own Add, Delete (behind a confirmation), Save and Cancel controls. */
function mountSr(initial: { exp?: string[]; edu?: string[] }, extra = ""): Page {
  mount(`<form><label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em" type="email"><label for="cv">Resume</label><input id="cv" type="file" accept=".pdf">
    <section id="exp"><h3>Experience</h3><ul class="cards"></ul><div class="slot"></div><button type="button" class="add">Add experience</button></section>
    <section id="edu"><h3>Education</h3><ul class="cards"></ul><div class="slot"></div><button type="button" class="add">Add education</button></section>${extra}</form>`);
  const saved: Record<string, unknown>[] = [];
  const presses: string[] = [];
  const root = (s: string) => document.getElementById(s) as HTMLElement;
  const addCard = (section: "exp" | "edu", text: string) => {
    const li = document.createElement("li");
    li.innerHTML = `<span>${text}</span><button type="button" aria-label="Delete ${section === "exp" ? "experience" : "education"}">Delete</button>`;
    li.querySelector("button")!.addEventListener("click", () => {
      presses.push(`delete:${text}`);
      const dlg = document.createElement("div");
      dlg.setAttribute("role", "dialog");
      dlg.setAttribute("aria-label", "Delete experience?");
      dlg.innerHTML = `<h2>Delete experience?</h2><button type="button" class="yes">Delete</button><button type="button" class="no">Cancel</button>`;
      dlg.querySelector(".yes")!.addEventListener("click", () => {
        dlg.remove();
        li.remove();
      });
      document.body.append(dlg);
    });
    root(section).querySelector(".cards")!.append(li);
  };
  for (const section of ["exp", "edu"] as const) {
    root(section).querySelector(".add")!.addEventListener("click", () => {
      presses.push(`add:${section}`);
      const slot = root(section).querySelector(".slot") as HTMLElement;
      if (slot.children.length) return;
      const labels = section === "exp" ? ["Title", "Company"] : ["Institution", "Major", "Degree"];
      const dates = section === "exp" ? ["Start date", "End date"] : ["Start date", "End date"];
      slot.innerHTML = `<div class="editor">${labels.map((l) => `<spl-input label="${l}"></spl-input>`).join("")}${dates.map((l) => `<spl-datepicker label="${l}"></spl-datepicker>`).join("")}
        <button type="button" class="save">Save</button><button type="button" class="cancel">Cancel</button></div>`;
      slot.querySelector(".save")!.addEventListener("click", () => {
        presses.push(`save:${section}`);
        const comps = Array.from(slot.querySelectorAll("spl-input, spl-datepicker"));
        saved.push(Object.fromEntries(comps.map((c) => [c.getAttribute("label"), (c as unknown as { calls: unknown[] }).calls[0] ?? ""])));
        const name = section === "exp" ? val(comps[1]!) : val(comps[0]!);
        slot.innerHTML = "";
        addCard(section, name || "Untitled");
      });
      slot.querySelector(".cancel")!.addEventListener("click", () => {
        presses.push(`cancel:${section}`);
        slot.innerHTML = "";
      });
    });
  }
  for (const t of initial.exp ?? []) addCard("exp", t);
  for (const t of initial.edu ?? []) addCard("edu", t);
  return { cards: (s) => Array.from(root(s).querySelectorAll(".cards li span")).map((e) => e.textContent ?? ""), addCard, saved, presses };
}

function parseOnUpload(page: Page, rows: [("exp" | "edu"), string][]): void {
  document.getElementById("cv")!.addEventListener("change", () => setTimeout(() => rows.forEach(([s, t]) => page.addCard(s, t)), 5));
}

function engineWithHelper(host = fakeHost(), timing?: Partial<Timing>) {
  installHelper(win, NONCE, "h1");
  const engine = makeEngine(host, { ...FAST, ...timing });
  engine.useHelper(NONCE);
  return engine;
}

describe("SmartRecruiters history cards", () => {
  test("SmartRecruiters reconcile: the cards the parse made are replaced by one card per profile entry, written through the components", async () => {
    const page = mountSr({});
    parseOnUpload(page, [["exp", "Project Atlas"], ["exp", "Side Project"], ["exp", "Internship Notes"], ["edu", "Parsed College"]]);
    await runFill(engineWithHelper(), { applicant: PROFILE });
    expect(page.cards("exp")).toEqual(["Example Labs", "Sample Works"]);
    expect(page.cards("edu")).toEqual(["Example University"]);
    expect(page.saved[0]).toMatchObject({ Title: "Staff Engineer", Company: "Example Labs" });
    expect(page.presses.filter((p) => p.startsWith("delete:")).length).toBe(4);
  });

  test("SmartRecruiters reconcile leaves a section with no profile rows untouched", async () => {
    const page = mountSr({});
    parseOnUpload(page, [["edu", "Parsed College"], ["edu", "Parsed Institute"]]);
    await runFill(engineWithHelper(), { applicant: { candidate: PROFILE.candidate, history: { employment: PROFILE.history!.employment } } });
    expect(page.cards("edu")).toEqual(["Parsed College", "Parsed Institute"]);
    expect(page.presses.some((p) => p.endsWith(":edu"))).toBe(false);
  });

  test("A card that stood on the page before the run stays, and its entry is not added a second time", async () => {
    const page = mountSr({ exp: ["Example Labs - typed by hand"] });
    parseOnUpload(page, [["exp", "Parsed Role"]]);
    await runFill(engineWithHelper(), { applicant: PROFILE });
    expect(page.cards("exp")).toEqual(["Example Labs - typed by hand", "Sample Works"]);
    expect(page.presses.filter((p) => p === "add:exp")).toHaveLength(1);
  });

  test("Two roles at one employer are two cards: the second entry is added though a card already names the employer", async () => {
    const page = mountSr({});
    const employment = [PROFILE.history!.employment![0]!, { employer: "Example Labs", title: "Engineer", startDate: "2017-06-01", endDate: "2021-02-01" }];
    await runFill(engineWithHelper(), { applicant: { ...PROFILE, history: { employment } } });
    expect(page.cards("exp")).toEqual(["Example Labs", "Example Labs"]);
    expect(page.saved.map((s) => s.Title)).toEqual(["Staff Engineer", "Engineer"]);
  });

  test("A helper that does not take the single-flight request leaves a degraded fill, not a failed one", async () => {
    const page = mountSr({});
    // The page's helper is gone (a reinstall under another nonce): its requests are refused.
    const refuse = (ev: Event) => document.dispatchEvent(new CustomEvent(resEvent(NONCE), { detail: JSON.stringify({ id: JSON.parse(String((ev as CustomEvent).detail)).id, ok: false, error: "binding-unavailable: gone" }) }));
    document.addEventListener(reqEvent(NONCE), refuse);
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    const status = await runFill(engine, { applicant: { candidate: PROFILE.candidate, history: { employment: [] } } });
    document.removeEventListener(reqEvent(NONCE), refuse);
    expect(status.state).toBe("done");
    expect(page.presses).toEqual([]);
  });

  test("A blank editor the parse left open is closed with its Cancel control", async () => {
    const page = mountSr({});
    document.getElementById("cv")!.addEventListener("change", () => setTimeout(() => (document.querySelector("#exp .add") as HTMLElement).click(), 5));
    await runFill(engineWithHelper(), { applicant: PROFILE });
    expect(page.presses).toContain("cancel:exp");
    expect(page.cards("exp")).toEqual(["Example Labs", "Sample Works"]);
  });

  test("SmartRecruiters late-parse sweep: a card the parse adds after the first pass is deleted", async () => {
    const page = mountSr({});
    parseOnUpload(page, [["exp", "Parsed Role"]]);
    // The parse lands again the moment the second profile card is saved.
    let late = false;
    new MutationObserver((_, obs) => {
      if (!late && page.cards("exp").includes("Sample Works")) {
        late = true;
        obs.disconnect();
        page.addCard("exp", "Late Parsed Role");
      }
    }).observe(document.querySelector("#exp .cards")!, { childList: true });
    await runFill(engineWithHelper(), { applicant: PROFILE });
    await until(() => late && !page.cards("exp").includes("Late Parsed Role"), 10_000);
    expect(page.cards("exp")).toEqual(["Example Labs", "Sample Works"]);
  });

  test("The late-parse sweep is skipped when no parse ran", async () => {
    const page = mountSr({}, "");
    document.getElementById("cv")!.remove();
    await runFill(engineWithHelper(), { applicant: PROFILE });
    page.addCard("exp", "Added Later By The Person");
    await new Promise((r) => setTimeout(r, 120));
    expect(page.cards("exp")).toContain("Added Later By The Person");
  });

});

type Opt = { id: string; label: string; value: unknown };
const opt = (label: string, value: unknown = label): Opt => ({ id: label, label, value });
/** What the live search lists, by the box's data-test. */
const SEARCH: Record<string, Opt[]> = {
  "location-autocomplete": [opt("Springfield, IL, US", "IL"), opt("Springfield, MO, US", "MO"), opt("")],
  "institution-autocomplete": [opt("Example University", { name: "Example University" }), opt("Example University School of Law")],
  pwc: [opt("YES, I have been employed by PwC"), opt("No, I have not been employed by PwC")],
  gender: [opt("Female"), opt("Male"), opt("I decline to self-identify")],
};

/** The live spl-autocomplete: the value is its own; the spl-input deep inside shows only the query. */
function defineSplAutocomplete(): void {
  defineSplInput("spl-input");
  if (customElements.get("spl-autocomplete")) return;
  customElements.define(
    "spl-autocomplete",
    class extends HTMLElement {
      calls: unknown[] = [];
      value: unknown = null;
      isLoading = false;
      customValueOptionId = "#custom";
      _optionsToRender: Opt[] = [];
      dropdown = { hide: () => undefined };
      get allowCustomValues() {
        return this.hasAttribute("allowcustomvalues");
      }
      get defaultOptions() {
        return SEARCH[this.getAttribute("data-test") ?? ""] ?? [];
      }
      connectedCallback() {
        this.shadowRoot ?? (this.attachShadow({ mode: "open" }).innerHTML = `<div><spl-dropdown><div><spl-input label="${this.getAttribute("label") ?? ""}"></spl-input></div></spl-dropdown></div>`);
      }
      handleChange = (v: unknown) => this.calls.push((this.value = v));
      setQuery = (q: string, _?: unknown) => (this.shadowRoot!.querySelector("spl-input") as unknown as { handleChange(v: string): Promise<void> }).handleChange(q);
      async searchByQuery(q: string) {
        await this.setQuery(q);
        const found = (SEARCH[this.getAttribute("data-test") ?? ""] ?? []).filter((o) => o.label.startsWith(q.split(",")[0] as string));
        this._optionsToRender = [...(this.allowCustomValues ? [{ id: this.customValueOptionId, label: q, value: q }] : []), ...found];
      }
      handleOptionSelect(e: { detail: { value: string } }) {
        const o = this._optionsToRender.find((x) => x.id === e.detail.value)!;
        return this.setQuery(o.label, this.handleChange(o.value));
      }
    },
  );
}

function defineSplButton(): void {
  if (customElements.get("spl-button")) return;
  customElements.define(
    "spl-button",
    class extends HTMLElement {
      connectedCallback() {
        if (!this.shadowRoot) this.attachShadow({ mode: "open" }).innerHTML = "<button type=button><slot></slot></button>";
      }
    },
  );
}

/** The live box takes its text through a slot; the engine read it as a checkbox named "I currently work here". */
function defineSplCheckbox(): void {
  if (customElements.get("spl-checkbox")) return;
  customElements.define(
    "spl-checkbox",
    class extends HTMLElement {
      connectedCallback() {
        if (!this.shadowRoot) this.attachShadow({ mode: "open" }).innerHTML = '<label><input type="checkbox"><slot name="label-content"></slot></label>';
      }
    },
  );
}

/** SmartRecruiters' live markup (2026 oneclick-ui): Lit spl-* hosts under Angular oc-* wrappers. */
function mountSrLive(opts: { dropzone?: "upload" | "parse" | "both"; cardsOnly?: boolean; parsed?: string; onAdd?: (card: (text: string) => void) => void } = {}): { cards: () => string[]; editors: () => number; presses: string[]; saved: Record<string, unknown>[] } {
  defineSplAutocomplete();
  for (const t of ["spl-date-field", "spl-textarea"]) defineSplInput(t);
  defineSplCheckbox();
  // The live dropzone empties its input once it read the file and lists the name beside it.
  if (!customElements.get("spl-dropzone")) {
    customElements.define(
      "spl-dropzone",
      class extends HTMLElement {
        connectedCallback() {
          const root = this.shadowRoot ?? this.attachShadow({ mode: "open" });
          const id = this.closest("oc-apply-with-resume") ? "fp" : "f";
          root.innerHTML = `<div><label for="${id}">Resume</label><input type="file" id="${id}"></div><ul></ul>`;
          const input = root.querySelector("input")!;
          input.addEventListener("change", () => {
            // The parse dropzone above the form starts the parse and lists nothing.
            if (!this.closest("oc-apply-with-resume")) root.querySelector("ul")!.innerHTML = `<li>${input.files?.[0]?.name}</li>`;
            input.value = "";
            this.dispatchEvent(new Event("picked"));
          });
        }
      },
    );
  }
  defineSplButton();
  const zones = { upload: ["oc-resume-upload"], parse: ["oc-apply-with-resume"], both: ["oc-apply-with-resume", "oc-resume-upload"] }[opts.dropzone ?? "upload"];
  mount(`<form><label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em" type="email">${opts.dropzone ? zones.map((z) => `<${z}><spl-dropzone></spl-dropzone></${z}>`).join("") : '<label for="cv">Resume</label><input id="cv" type="file" accept=".pdf">'}
    <oc-experience><div data-test="experience"><div><spl-typography-title data-test="section-title">Experience</spl-typography-title></div><div class="entries"></div>
    <oc-button data-test="add-experience"><spl-button aria-label="Add experience entry"><span>Add</span></spl-button></oc-button></div></oc-experience></form>`);
  const presses: string[] = [];
  const saved: Record<string, unknown>[] = [];
  const list = document.querySelector(".entries") as HTMLElement;
  const on = (el: Element | null, f: () => void) => el!.addEventListener("click", f);
  const card = (text: string) => {
    const e = document.createElement("oc-experience-entry");
    e.innerHTML = `<div data-test="experience-entry"><span class="text">${text}</span><spl-button data-test="experience-entry-delete" tooltip="Delete experience" splarialabel="Delete experience ${text}"></spl-button></div>`;
    on(e.querySelector("spl-button"), () => {
      presses.push(`delete:${text}`);
      const d = document.createElement("spl-dialog");
      d.setAttribute("open", "");
      d.innerHTML = `<p>Delete experience?</p><spl-button type="primary"><span>Delete</span></spl-button><spl-button type="secondary"><span>Cancel</span></spl-button>`;
      on(d.querySelector("spl-button[type=primary]"), () => (d.remove(), e.remove()));
      document.body.append(d);
    });
    list.append(e);
  };
  const editor = (company = "") => {
    const e = document.createElement("oc-experience-entry");
    e.innerHTML = `<div data-test="experience-entry"><oc-experience-edit-form><spl-autocomplete allowcustomvalues label="Title"></spl-autocomplete><spl-autocomplete allowcustomvalues label="Company"></spl-autocomplete><spl-autocomplete label="Office location"></spl-autocomplete>
      <spl-date-field type="month-year" label="From"></spl-date-field><spl-date-field type="month-year" label="To"></spl-date-field>
      <oc-checkbox data-test="experience-current"><spl-checkbox><div slot="label-content"><span data-test="checkbox-label">I currently work here</span></div></spl-checkbox></oc-checkbox>
      <spl-button aria-label="Save experience entry"><span>Save</span></spl-button><spl-button aria-label="Cancel adding experience entry"><span>Cancel</span></spl-button></oc-experience-edit-form></div>`;
    list.append(e);
    const comps = Array.from(e.querySelectorAll("spl-autocomplete, spl-date-field")) as unknown as { calls: unknown[]; handleChange(v: unknown): void; getAttribute(n: string): string }[];
    const current = () => (e.querySelector("spl-checkbox")!.shadowRoot!.querySelector("input") as HTMLInputElement).checked;
    if (company) comps[1]!.handleChange(company);
    on(e.querySelector("[aria-label^=Cancel]"), () => (presses.push("cancel"), e.remove()));
    on(e.querySelector("[aria-label^=Save]"), () => {
      // From is required, and To too unless "I currently work here" is ticked.
      if (!comps[3]!.calls.length || (!comps[4]!.calls.length && !current())) return void presses.push("save-refused");
      presses.push("save");
      saved.push({ ...Object.fromEntries(comps.map((c) => [c.getAttribute("label"), c.calls[c.calls.length - 1] ?? ""])), "I currently work here": current() });
      e.remove();
      card(String(comps[1]!.calls[comps[1]!.calls.length - 1] ?? "Untitled"));
    });
  };
  on(document.querySelector("[aria-label='Add experience entry']"), () => (presses.push("add"), opts.onAdd?.(card), editor()));
  const parse = () =>
    setTimeout(() => {
      card(opts.parsed ?? "Staff Engineer Example Labs");
      if (opts.cardsOnly) return card("Engineer Sample Works");
      editor("Autonomous Research Platform");
      editor("Tabletop Trainer");
    }, 5);
  if (opts.dropzone) document.querySelector("spl-dropzone")!.addEventListener("picked", parse);
  else document.getElementById("cv")!.addEventListener("change", parse);
  return { cards: () => Array.from(list.querySelectorAll(".text")).map((t) => t.textContent ?? ""), editors: () => list.querySelectorAll("oc-experience-edit-form").length, presses, saved };
}

describe("SmartRecruiters live card markup", () => {
  // The page requires To unless "I currently work here" is ticked, so the current job's card needs the box.
  test.each([
    { missing: "an ended job", parsed: "Staff Engineer Example Labs", card: "Sample Works", want: { Title: "Engineer", Company: "Sample Works", "I currently work here": false } },
    { missing: "the current job", parsed: "Engineer Sample Works", card: "Example Labs", want: { Title: "Staff Engineer", Company: "Example Labs", "I currently work here": true } },
  ])("The parse's open editors are cancelled, its card naming a profile entry stays, the missing entry is added: $missing", async ({ parsed, card, want }) => {
    const page = mountSrLive({ parsed });
    await runFill(engineWithHelper(), { applicant: PROFILE });
    expect(page.editors()).toBe(0);
    expect(page.cards()).toEqual([parsed, card]);
    expect(page.presses.filter((p) => p === "cancel")).toHaveLength(2);
    expect(page.presses.some((p) => p.startsWith("delete"))).toBe(false);
    expect(page.saved[0]).toMatchObject({ ...want, "Office location": "" });
    expect(page.saved[0]!["From"]).toBeInstanceOf(Date);
  });

  test("Month-only profile dates reach the month fields as the first of the month, so Save takes the entry", async () => {
    const page = mountSrLive();
    const employment = [
      { employer: "Example Labs", title: "Staff Engineer", startDate: "2021-03", current: true },
      { employer: "Sample Works", title: "Engineer", startDate: "2017-06", endDate: "2021-02" },
    ];
    await runFill(engineWithHelper(), { applicant: { ...PROFILE, history: { employment } } });
    expect(page.presses).not.toContain("save-refused");
    expect(page.editors()).toBe(0);
    const { From: from, To: to } = page.saved[0] as { From: Date; To: Date };
    expect([from.getFullYear(), from.getMonth(), from.getDate(), to.getFullYear(), to.getMonth()]).toEqual([2017, 5, 1, 2021, 1]);
  });

  test("Parse cards landing mid-reconcile are never added twice", async () => {
    let landed = false;
    const page = mountSrLive({ onAdd: (card) => landed || ((landed = true), card("Analyst Third Co")) });
    const employment = [...(PROFILE.history?.employment ?? []), { employer: "Third Co", title: "Analyst", startDate: "2015-01-01", endDate: "2017-01-01" }];
    await runFill(engineWithHelper(), { applicant: { ...PROFILE, history: { employment } } });
    expect(page.cards().filter((c) => c.includes("Third Co"))).toHaveLength(1);
    expect(page.presses.filter((p) => p === "add")).toHaveLength(1);
  });

  // The parse dropzone lists no name; after its parse settled, the upload dropzone's file is not waited on.
  test.each(["upload", "parse", "both"] as const)("A %s dropzone that empties its input has taken the upload: no native upload parses it again", async (dropzone) => {
    const page = mountSrLive({ dropzone });
    const engine = engineWithHelper(fakeHost(), { parseNoChangeMs: 5000 });
    const status = await runFill(engine, { applicant: PROFILE });
    expect(summaryOf(status).rows.filter((r) => r.kind === "file").map((r) => r.outcome)).toEqual(dropzone === "both" ? ["filled", "filled"] : ["filled"]);
    expect(engine.log().some((l) => l.includes("how=no-parse"))).toBe(false);
    expect(page.editors()).toBe(0);
    expect(page.cards()).toEqual(["Staff Engineer Example Labs", "Sample Works"]);
    expect(page.presses.filter((p) => p === "add")).toHaveLength(1);
  });
});

type Comp = Element & { value?: unknown; calls: unknown[] };

/** The live City of residence (a place search) and Education, whose Save needs an Institution value. */
function mountSrEducation(extra = "") {
  defineSplAutocomplete();
  defineSplInput("spl-date-field");
  defineSplButton();
  const ac = (label: string, test: string, custom = "") => `<spl-autocomplete data-test="${test}-autocomplete" ${custom} label="${label}"></spl-autocomplete>`;
  mount(`<form><label for="fn">First name</label><input id="fn"><oc-personal-information>${ac("City*", "location")}</oc-personal-information><spl-autocomplete aria-label="Select Gender"></spl-autocomplete>
    <oc-education><spl-typography-title>Education</spl-typography-title><div class="entries"></div><spl-button aria-label="Add education entry">Add</spl-button></oc-education></form>`);
  const presses: string[] = [];
  const saved: Record<string, unknown>[] = [];
  const list = document.querySelector(".entries") as HTMLElement;
  const on = (el: Element | null, f: () => void) => el!.addEventListener("click", f);
  on(document.querySelector("[aria-label^=Add]"), () => {
    presses.push("add");
    const e = document.createElement("oc-education-entry");
    e.innerHTML = `<oc-education-edit-form>${ac("Institution*", "institution", "allowcustomvalues")}<spl-input label="Major"></spl-input>${ac("School location", "location")}${extra && `<spl-input label="${extra}*"></spl-input>`}
      <spl-date-field type="month-year" label="From"></spl-date-field><spl-button aria-label="Save education entry">Save</spl-button><spl-button aria-label="Cancel adding education entry">Cancel</spl-button></oc-education-edit-form>`;
    list.append(e);
    const comps = Array.from(e.querySelectorAll("spl-autocomplete, spl-input")) as Comp[];
    on(e.querySelector("[aria-label^=Cancel]"), () => (presses.push("cancel"), e.remove()));
    on(e.querySelector("[aria-label^=Save]"), () => {
      if (!comps[0]!.value || (extra && !comps[3]!.calls.length)) return void presses.push("save-refused");
      presses.push("save");
      saved.push(Object.fromEntries(comps.map((c) => [c.getAttribute("label"), c.localName === "spl-input" ? c.calls[0] : c.value])));
      e.innerHTML = `<span>Example University</span><spl-button tooltip="Delete education"></spl-button>`;
    });
  });
  return { city: () => (document.querySelector("spl-autocomplete") as Comp).value, editors: () => list.querySelectorAll("oc-education-edit-form").length, presses, saved };
}

const RESIDENT: ApplicantSnapshot = {
  candidate: { ...PROFILE.candidate, address: { city: "Springfield", state: "MO", country: "United States" } },
  history: { education: PROFILE.history!.education },
};

describe("SmartRecruiters search boxes", () => {
  test("City is the place the profile's state names, Institution its exact option, School location untouched; Save takes the entry; a host aria-label names its box", async () => {
    const page = mountSrEducation();
    const status = await runFill(engineWithHelper(), { applicant: RESIDENT });
    expect(page.city()).toBe("MO");
    // A box named only by its host's aria-label is read by that name.
    expect(rowFor(summaryOf(status), "gender").label).toBe("Select Gender");
    expect(page.presses).toEqual(["add", "save"]);
    expect(page.saved[0]).toEqual({ "Institution*": { name: "Example University" }, Major: "Computer Science", "School location": null });
  });

  test("A bare No picks the one option that opens with it", async () => {
    defineSplAutocomplete();
    mount(`<form><label for="fn">First name</label><input id="fn"><spl-autocomplete data-test="pwc" aria-label="Employed by PwC?"></spl-autocomplete></form>`);
    await runFill(engineWithHelper(fakeHost({ "employed by pwc?": { outcome: "filled", value: "No" } })), { applicant: RESIDENT });
    expect((document.querySelector("spl-autocomplete") as unknown as { value: unknown }).value).toBe("No, I have not been employed by PwC");
  });

  test.each([
    // The live screening select and the self-identification select ("Select Gender"), each an spl-autocomplete under its question field.
    { field: "sr-question-field-select", box: "pwc", name: "Select Are you currently employed or have you ever been employed by PwC?", pick: "No, I have not been employed by PwC" },
    { field: "sr-question-field-eeo", box: "gender", name: "Select Gender", pick: "I decline to self-identify" },
  ])("A select under $field is asked with the choices its component lists and commits the one chosen", async ({ field, box, name, pick }) => {
    defineSplAutocomplete();
    mount(`<form><label for="fn">First name</label><input id="fn"><${field}><spl-autocomplete data-test="${box}" aria-label="${name}"></spl-autocomplete></${field}></form>`);
    const host = fakeHost({ [name.toLowerCase()]: { outcome: "filled", value: pick, optionLabel: pick } });
    await runFill(engineWithHelper(host), { applicant: RESIDENT });
    expect(host.matchRequests.flatMap((r) => r.questions).find((x) => x.text === name)).toMatchObject({ kind: "custom-listbox", options: SEARCH[box]!.map((o) => o.label) });
    expect((document.querySelector("spl-autocomplete") as unknown as { value: unknown }).value).toBe(pick);
  });

  test("An editor the page will not save is cancelled, and its empty required field is the user's", async () => {
    const page = mountSrEducation("Graduation status");
    const status = await runFill(engineWithHelper(), { applicant: RESIDENT });
    expect(page.presses).toEqual(["add", "save-refused", "cancel"]);
    expect(page.editors()).toBe(0);
    expect(rowFor(summaryOf(status), "Graduation status")).toMatchObject({ outcome: "failed", reason: "no-answer", required: true });
  });
});

describe("SmartRecruiters component writes over a value", () => {
  const inner = (host: Element) => host.shadowRoot!.querySelector("input") as HTMLInputElement;
  const helper = () => {
    installHelper(win, NONCE, "h1");
    return { ...ctx(), main: new MainWorld(win, NONCE) };
  };

  test("A text component that ignores the call keeps its old text, and that text is not the write", async () => {
    mount(`<form><spl-input label="LinkedIn"></spl-input></form>`);
    const host = document.querySelector("spl-input") as HTMLElement & { handleChange(v: unknown): Promise<void> };
    inner(host).value = "a parser guess";
    host.handleChange = () => Promise.resolve();
    const c = helper();
    expect(await writeSrComponent(inner(host), "https://example.org/in/alex", c)).toMatchObject({ ok: false, reason: "write-not-committed" });
    c.main.dispose();
  });

  test("A search box that ignores the selection keeps the value it held, and that value is not the write", async () => {
    defineSplAutocomplete();
    mount(`<form><spl-autocomplete data-test="institution-autocomplete" allowcustomvalues label="Institution"></spl-autocomplete></form>`);
    const box = document.querySelector("spl-autocomplete") as HTMLElement & { value: unknown; handleOptionSelect(e: unknown): unknown };
    box.value = { name: "A Parser Guess" };
    box.handleOptionSelect = () => undefined;
    const c = helper();
    expect(await writeSrComponent(inner(box.shadowRoot!.querySelector("spl-input")!), "Example University", c)).toMatchObject({ ok: false, reason: "write-not-committed" });
    // A box that takes the selection over the value it held is a write.
    delete (box as { handleOptionSelect?: unknown }).handleOptionSelect;
    expect(await writeSrComponent(inner(box.shadowRoot!.querySelector("spl-input")!), "Example University", c)).toMatchObject({ ok: true, optionLabel: "Example University" });
    c.main.dispose();
  });
});

describe("SmartRecruiters field validity", () => {
  test("A value the component marks invalid is cleared and left to the user with the site's message", async () => {
    mount(`<form><label for="fn">First name</label><input id="fn"><spl-input label="LinkedIn" data-refuse="example"></spl-input><spl-input label="GitHub" data-refuse="nothing"></spl-input></form>`);
    const links = { linkedin: "https://linkedin.example.org/in/alex", github: "https://github.com/alex" };
    const s = summaryOf(await runFill(engineWithHelper(), { applicant: { ...PROFILE, links } }));
    const [li, gh] = Array.from(document.querySelectorAll("spl-input")).map(val);
    expect(rowFor(s, "LinkedIn")).toMatchObject({ outcome: "failed", reason: "format-not-accepted", detail: "Please enter a valid LinkedIn URL." });
    expect([li, gh]).toEqual(["", links.github]);
    expect(rowFor(s, "GitHub").outcome).toBe("filled");
  });
});

describe("SmartRecruiters upload slots", () => {
  const DROPZONE = '<div><p>Autocomplete your application</p><input id="parse" type="file" accept=".pdf"></div>';
  const ATTACH = '<div><label for="cv2">Resume</label><input id="cv2" type="file" accept=".pdf"></div>';

  test("The parse-only dropzone is skipped whenever a real attachment slot exists", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em" type="email">${DROPZONE}${ATTACH}`);
    const host = fakeHost();
    await runFill(engineWithHelper(host), { applicant: PROFILE });
    expect((document.getElementById("parse") as HTMLInputElement).files?.length ?? 0).toBe(0);
    expect((document.getElementById("cv2") as HTMLInputElement).files?.length ?? 0).toBe(1);
    expect(host.resumeRequests).toHaveLength(1);
  });

  test("The parse-only dropzone is used when it is the only slot", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em" type="email">${DROPZONE}`);
    await runFill(engineWithHelper(), { applicant: PROFILE });
    expect((document.getElementById("parse") as HTMLInputElement).files?.length ?? 0).toBe(1);
  });
});
