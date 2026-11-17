// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for enterprise application suites (SuccessFactors, Taleo);
// Drivers for HR-suite application systems (iCIMS).
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installHelper } from "../src/mainworld/helper";
import { pickIcimsRow } from "../src/icims";
import type { Win } from "../src/dom";
import { readInfo, scanControls } from "../src/scan";
import { MainWorld } from "../src/mainworld-client";
import { attachSfResume } from "../src/successfactors";
import { ADA, ctx, fakeHost, infoOf, makeEngine, mount, PAD, setUrl, summaryOf, write, uninstall, onFakeClock, runFillFaked } from "./helpers";
import { mountIcimsDropdown, mountSuccessFactors } from "./ats-fixtures";

const win = window as unknown as Win;
const NONCE = "sf-nonce";

// Every wait, the engine's and the fixtures', runs on one fake clock: a loaded machine cannot reorder them.
beforeEach(() => {
  uninstall();
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  uninstall();
});

describe("iCIMS dropdowns", () => {
  test("iCIMS placeholder select: choosing No updates the custom control's display and the hidden value the form submits", async () => {
    mount(PAD);
    const d = mountIcimsDropdown({ id: "auth", label: "Are you authorized to work? *", rows: [{ text: "Yes" }, { text: "No" }] });
    expect(d.select.options).toHaveLength(1);
    const info = scanControls(document).find((i) => i.el.id === "auth")!;
    expect(info).toMatchObject({ kind: "custom-listbox", driver: "icims", options: [{ label: "Yes" }, { label: "No" }], needsOptions: false });
    const r = await onFakeClock(write("auth", "No"));
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "No" });
    expect(d.display()).toBe("No");
    expect(d.select.value).toBe("No");
    expect(readInfo(infoOf("auth"))).toBe("No");
  });

  test("iCIMS native flag: a select stamped icimsdropdown-enabled=0 is written natively and the widget driver is not used", async () => {
    mount(PAD);
    const d = mountIcimsDropdown({ id: "pick", label: "Choice", rows: [{ text: "Yes" }], enabled: "0" });
    d.select.innerHTML = `<option value="">Make a Selection</option><option value="y">Yes</option><option value="n">No</option>`;
    const info = scanControls(document).find((i) => i.el.id === "pick")!;
    expect(info.driver).toBeNull();
    expect(info.kind).toBe("select");
    expect(await onFakeClock(write("pick", "Yes"))).toMatchObject({ outcome: "filled" });
    expect(d.select.value).toBe("y");
    expect(d.opened()).toBe(0);
  });

  test("Preloaded rows are read without opening the dropdown, and a held genuine selection is kept", async () => {
    mount(PAD);
    const d = mountIcimsDropdown({ id: "auth", label: "Willing to work weekends?", rows: [{ text: "Yes" }, { text: "No" }] });
    const host = fakeHost({ "willing to work weekends?": { outcome: "filled", value: "Yes" } });
    await runFillFaked(makeEngine(host));
    expect(d.opened()).toBe(1);
    const q = host.matchRequests[0]?.questions.find((x) => x.text === "Willing to work weekends?");
    expect(q?.options).toEqual(["Yes", "No"]);
    // Already chosen: a second run does not reopen it.
    await runFillFaked(makeEngine(fakeHost({ "willing to work weekends?": { outcome: "filled", value: "No" } })));
    expect(d.display()).toBe("Yes");
    expect(d.opened()).toBe(1);
  });

  test("Option match tiers: label, parenthetical-stripped label, unique boundary, unique containment, then the option's own value id", () => {
    const el = (t: string, value = "") => ({ el: document.createElement("li"), label: t, value });
    const rows = [el("North Carolina (NC)", "nc"), el("South Carolina (SC)", "sc"), el("Latino"), el("No"), el("Not Applicable")];
    expect(pickIcimsRow(rows, "North Carolina")?.label).toBe("North Carolina (NC)");
    expect(pickIcimsRow(rows, "No")?.label).toBe("No");
    expect(pickIcimsRow(rows.slice(2, 3), "No")).toBeNull();
    expect(pickIcimsRow(rows, "Carolina")).toBeNull();
    expect(pickIcimsRow([el("United States", "1001"), el("Canada", "1002")], "1002")?.label).toBe("Canada");
  });

  test("An AJAX list is waited for and a search list is sent with no options and typed with the bare label plus a keyup", async () => {
    mount(PAD);
    const a = mountIcimsDropdown({ id: "ajax", label: "Country", rows: [{ text: "United States", value: "us" }, { text: "Canada", value: "ca" }], ajaxMs: 30 });
    expect(await onFakeClock(write("ajax", "Canada"))).toMatchObject({ outcome: "filled" });
    expect(a.select.value).toBe("ca");
    mount(PAD);
    const s = mountIcimsDropdown({ id: "st", label: "State", rows: [{ text: "North Carolina" }, { text: "Ohio" }], search: true });
    const info = scanControls(document).find((i) => i.el.id === "st")!;
    expect(info.options).toEqual([]);
    expect(await onFakeClock(write("st", "Ohio"))).toMatchObject({ outcome: "filled" });
    expect(s.display()).toBe("Ohio");
    // The search matches the literal text, so an answer's trailing parenthetical is not typed.
    mount(PAD);
    const t = mountIcimsDropdown({ id: "st", label: "State", rows: [{ text: "North Carolina" }, { text: "Ohio" }], search: true });
    expect(await onFakeClock(write("st", "North Carolina (NC)"))).toMatchObject({ outcome: "filled", optionLabel: "North Carolina" });
    expect(t.display()).toBe("North Carolina");
  });

  test("The top frame does not scan the host page's job-search widgets when the content frame owns the form", () => {
    mount(`<iframe id="icims_content_iframe"></iframe><div id="advanced-search-box"><label for="kw">Keywords</label><input id="kw"></div><label for="x">Other</label><input id="x">`);
    const ids = scanControls(document).map((i) => i.el.id);
    expect(ids).toEqual(["x"]);
  });

  test("The upload later checkbox is an answer, not an agreement", async () => {
    mount(`<fieldset><legend>Resume</legend><label><input type="checkbox" id="later"> I will upload my resume later</label></fieldset>${PAD}`);
    const r = await onFakeClock(write("later", "Yes"));
    expect(r.outcome).toBe("filled");
    expect((document.getElementById("later") as HTMLInputElement).checked).toBe(true);
  });
});

describe("SuccessFactors", () => {
  test("Collapsed panels are opened once before scanning, never the collapse anchor, and the form is found", async () => {
    mount("");
    const sf = mountSuccessFactors();
    const s = summaryOf(await runFillFaked(makeEngine(fakeHost()), { applicant: ADA }));
    expect(sf.expandClicks()).toBe(1);
    expect((document.getElementById("fn") as HTMLInputElement).value).toBe("Ada");
    expect(s.notAnApplication).toBeUndefined();
  });

  test("The upload is attached through the main-world helper because the page's own uploader overwrites assignments", async () => {
    mount("");
    const sf = mountSuccessFactors();
    installHelper(win, NONCE, "h1");
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    const s = summaryOf(await runFillFaked(engine, { applicant: ADA }));
    expect(sf.uploads).toEqual(["ada-example.pdf"]);
    expect(s.rows.find((r) => r.kind === "file")).toMatchObject({ outcome: "filled", fileName: "ada-example.pdf" });
  });

  test("A résumé that did not land is not left staged: a later click on an uploader input gets no file", async () => {
    // The trigger opens nothing and the slot is no uploader of the system's, so the helper refuses it.
    mount(`<button type="button">Upload a resume</button><input type="file" id="slot"><input type="file" id="cover" name="fileData2">`);
    installHelper(win, NONCE, "h1");
    const main = new MainWorld(win, NONCE);
    const out = await onFakeClock(attachSfResume({ ...ctx(), main }, document.getElementById("slot") as HTMLInputElement, { fileName: "cv.pdf", mediaType: "application/pdf", contentBase64: btoa("%PDF-1.4 example") }));
    expect(out).toBe("not-landed");
    await vi.advanceTimersByTimeAsync(5);
    (document.getElementById("cover") as HTMLInputElement).click();
    expect((document.getElementById("cover") as HTMLInputElement).files?.length ?? 0).toBe(0);
    main.dispose();
  });

  test("An upload that did not land is tried again by the next run: the slot is not marked as having received a file", async () => {
    mount("");
    const sf = mountSuccessFactors();
    const engine = makeEngine(fakeHost());
    const first = summaryOf(await runFillFaked(engine, { applicant: ADA }));
    expect(first.rows.find((r) => r.kind === "file")).toMatchObject({ outcome: "skipped", reason: "binding-unavailable" });
    // The helper is there for the second run.
    installHelper(win, NONCE, "h1");
    engine.useHelper(NONCE);
    const second = summaryOf(await runFillFaked(engine, { applicant: ADA }));
    expect(sf.uploads).toEqual(["ada-example.pdf"]);
    expect(second.rows.find((r) => r.kind === "file")).toMatchObject({ outcome: "filled", fileName: "ada-example.pdf" });
  });

  test("A trash icon means a file is attached and nothing is replaced", async () => {
    mount("");
    const sf = mountSuccessFactors({ attached: true });
    installHelper(win, NONCE, "h1");
    const engine = makeEngine(fakeHost());
    engine.useHelper(NONCE);
    await runFillFaked(engine, { applicant: ADA });
    expect(sf.uploads).toEqual([]);
  });
});

describe("Taleo", () => {
  test("Taleo generic coverage: a widget no generic driver understands is listed skipped with a reason and the rest of the page is filled", async () => {
    setUrl("https://example.taleo.net/careersection/ex/apply.ftl");
    mount(`<label for="fn">First name</label><input id="fn" name="fn"><label for="em">Email</label><input id="em" type="email">
      <div id="rating" role="slider" tabindex="0" aria-label="Rate your skill" aria-valuenow="0"></div>`);
    const s = summaryOf(await runFillFaked(makeEngine(fakeHost()), { applicant: ADA }));
    expect(s.site).toBe("taleo");
    expect((document.getElementById("fn") as HTMLInputElement).value).toBe("Ada");
    const row = s.rows.find((r) => r.label === "Rate your skill");
    expect(row).toMatchObject({ outcome: "skipped", reason: "driver-unavailable" });
  });

});
