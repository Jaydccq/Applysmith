import { describe, expect, it } from "vitest";
import { act } from "react";
import { fail, ok } from "../../testing/api";
import { button, byText, click, mount, q, qa, settle, submit, text, type, waitFor } from "../../testing/dom";
import { applied, setupApi, setupState, setupStep } from "../../testing/fixtures_setup";
import type { SetupDraft } from "../../wire";
import Setup from "../Setup";

const draft: SetupDraft = {
  employment: [
    { employer: "Northwind Example", title: "Staff Engineer", start: "2019-03", end: "present" },
    { employer: "Contoso Example", title: "Engineer", start: "2015", end: "2019" },
  ],
  education: [{ school: "Example University", degree: "BSc Computing", end: "2014" }],
  skills: ["Go", "SQL"],
};

async function review(d: SetupDraft = draft) {
  const api = setupApi(setupState({ cv: { status: "partial" } }), { "POST /api/setup/steps/cv/apply": () => ok(applied(setupStep("cv"), { draft: d })) });
  window.history.replaceState(null, "", "/setup?step=cv");
  await mount(<Setup />);
  await type(await waitFor(() => q<HTMLTextAreaElement>("textarea")), "# Riley Sample\n\nStaff Engineer at Northwind Example since 2019.");
  await submit(q("form"));
  await waitFor(() => q("#cv-draft"));
  return api;
}

const entry = (key: string) => q(`[data-entry="${key}"]`)!;
const keep = (key: string) => entry(key).querySelector<HTMLInputElement>("input[type=checkbox]")!;

describe("Setup: CV draft review", () => {
  it("sends only the entries that were kept, with the months that were typed, and records the removed ones as discarded", async () => {
    const api = await review();
    api.on("POST /api/config/cv/draft/dismiss", () => ok({ revision: "r", employment: [], education: [], skills: [], identity: {}, dismissed: [] }));
    await click(keep("job:1"));
    await click(keep("skill:1"));
    await click(keep("study:0"));
    expect(button(/^Confirm/)!.disabled).toBe(false);
    await click(button("Confirm 2 entries"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/cv/apply").length === 2);
    expect(api.callsTo("POST /api/setup/steps/cv/apply")[1].body).toEqual({
      input: { employment: [{ employer: "Northwind Example", title: "Staff Engineer", start: "2019-03", end: "present" }], skills: [{ name: "Go" }] },
    });
    // The keys Profile's draft from your CV leaves out (profile/CvDraft.tsx).
    await waitFor(() => api.callsTo("POST /api/config/cv/draft/dismiss").length === 1);
    expect(api.callsTo("POST /api/config/cv/draft/dismiss")[0].body).toEqual({ keys: ["job|Contoso Example|Engineer|2015", "study|Example University|BSc Computing|2014", "skill|sql"] });
  });

  it("sends a mis-read entry as corrected, and shows a refusal beside the entry it names", async () => {
    const api = await review({ employment: [{ employer: "Staff Engineer", title: "Northwind Example", start: "2019-03", end: "2018-01" }], education: [], skills: [] });
    const field = (label: string) => q<HTMLInputElement>(`#${CSS.escape(qa("[data-entry] label").find((l) => l.textContent?.startsWith(label))!.getAttribute("for")!)}`)!;
    await type(field("Employer"), "Northwind Example");
    await type(field("Job title"), "Staff Engineer");
    api.on("POST /api/setup/steps/cv/apply", () => fail("validation_failed", "invalid", { details: { fields: { "history.employment[0].end": "start (2019-03) must not be later than end (2018-01)" } } }));
    await click(button("Confirm 1 entry"));
    await waitFor(() => entry("job:0").textContent?.includes("End: start (2019-03) must not be later than end (2018-01)"));
    expect(api.callsTo("POST /api/setup/steps/cv/apply")[1].body).toEqual({ input: { employment: [{ employer: "Northwind Example", title: "Staff Engineer", start: "2019-03", end: "2018-01" }] } });
  });

  it("asks before another step drops a review in progress", async () => {
    await review();
    await click(keep("skill:1"));
    await click(qa("nav[aria-label='Other steps'] a").pop()!);
    await waitFor(() => text().includes("Leave without saving?"));
    await click(button("Cancel"));
    expect(keep("skill:1").checked).toBe(false);
  });

  it("records that the draft was reviewed when nothing is kept", async () => {
    const api = await review();
    for (const k of ["job:0", "job:1", "study:0", "skill:0", "skill:1"]) await click(keep(k));
    await click(byText("button", "Confirm that nothing is kept"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/cv/apply").length === 2);
    expect(api.callsTo("POST /api/setup/steps/cv/apply")[1].body).toEqual({ input: { review_complete: true } });
  });
});

it("previews an uploaded DOCX in setup before saving the confirmed text", async () => {
 const api=setupApi(setupState({cv:{status:"partial"}}), {"POST /api/setup/cv/extract":()=>ok({markdown:"# Riley Sample\nPlatform Engineer"}),"POST /api/setup/steps/cv/apply":()=>ok(applied(setupStep("cv")))});
 window.history.replaceState(null,"","/setup?step=cv");
 await mount(<Setup />);
 const picker=await waitFor(()=>q<HTMLInputElement>('input[aria-label="CV file"]'));
 Object.defineProperty(picker,"files",{value:[new File(["synthetic"],"cv.docx")],configurable:true});
 await act(async()=>{picker.dispatchEvent(new Event("change",{bubbles:true}));});
 await waitFor(()=>q<HTMLTextAreaElement>("textarea")?.value==="# Riley Sample\nPlatform Engineer");
 expect(api.callsTo("POST /api/setup/steps/cv/apply")).toHaveLength(0);
 expect(api.callsTo("POST /api/setup/cv/extract")[0].query.get("name")).toBe("cv.docx");
 await click(button("Save CV"));
 await waitFor(()=>api.callsTo("POST /api/setup/steps/cv/apply").length===1);
 expect(api.callsTo("POST /api/setup/steps/cv/apply")[0].body).toEqual({input:{markdown:"# Riley Sample\nPlatform Engineer"}});
});

it("keeps the edited CV when extraction fails and sends no save", async () => {
 const api=setupApi(setupState({cv:{status:"partial"}}), {"POST /api/setup/cv/extract":()=>fail("validation_failed","The PDF has no selectable text; use OCR first.")});
 window.history.replaceState(null,"","/setup?step=cv");await mount(<Setup />);
 await type(await waitFor(()=>q<HTMLTextAreaElement>("textarea")),"my reviewed draft");
 await click(button("Import from file"));
 await click(await waitFor(()=>button("Cancel")));
 const picker=q<HTMLInputElement>('input[aria-label="CV file"]')!;
 Object.defineProperty(picker,"files",{value:[new File(["synthetic"],"scan.pdf")],configurable:true});
 await act(async()=>{picker.dispatchEvent(new Event("change",{bubbles:true}));});
 await waitFor(()=>text().includes("OCR first"));
 expect(q<HTMLTextAreaElement>("textarea")?.value).toBe("my reviewed draft");
 expect(api.callsTo("POST /api/setup/steps/cv/apply")).toHaveLength(0);
});


it("holds saving during extraction and importing during save", async () => {
  let finishExtract!: () => void;
  let finishSave!: () => void;
  const extraction = new Promise<void>((resolve) => { finishExtract = resolve; });
  const saving = new Promise<void>((resolve) => { finishSave = resolve; });
  const api = setupApi(setupState({ cv: { status: "partial" } }), {
    "POST /api/setup/cv/extract": async () => { await extraction; return ok({ markdown: "review this imported draft" }); },
    "POST /api/setup/steps/cv/apply": async () => { await saving; return ok(applied(setupStep("cv"))); },
  });
  window.history.replaceState(null, "", "/setup?step=cv");
  await mount(<Setup />);
  await type(await waitFor(() => q<HTMLTextAreaElement>("textarea")), "previous edited draft");
  const picker = q<HTMLInputElement>('input[aria-label="CV file"]')!;
  Object.defineProperty(picker, "files", { value: [new File(["owned"], "cv.docx")], configurable: true });
  await act(async () => { picker.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(button("Save CV")!.disabled).toBe(true);
  await submit(q("form"));
  expect(api.callsTo("POST /api/setup/steps/cv/apply")).toHaveLength(0);
  await act(async () => { finishExtract(); });
  await waitFor(() => !button("Save CV")!.disabled);
  await click(button("Save CV"));
  expect(button("Import from file")!.disabled).toBe(true);
  expect(q<HTMLTextAreaElement>("textarea")!.disabled).toBe(true);
  await act(async () => { finishSave(); });
  await waitFor(() => !button("Import from file")!.disabled);
  expect(api.callsTo("POST /api/setup/steps/cv/apply")[0].body).toEqual({ input: { markdown: "review this imported draft" } });
});

it("quotes the revision the CV was loaded at, and on a newer file keeps or loads it as chosen", async () => {
  const doc = (revision: string, content: string) => ok({ file: "cv.md", exists: true, template: false, content, revision, issues: [] });
  let onDisk = doc("r1", "# Loaded here");
  let refuse = true;
  const api = setupApi(setupState({ cv: { status: "partial" } }), {
    "GET /api/config/cv": () => onDisk,
    "POST /api/setup/steps/cv/apply": () => (refuse ? fail("revision_conflict", "cv.md changed after this text was loaded; nothing was saved") : ok(applied(setupStep("cv")))),
  });
  window.history.replaceState(null, "", "/setup?step=cv");
  await mount(<Setup />);
  await type(await waitFor(() => q<HTMLTextAreaElement>("textarea")), "# Typed here");
  onDisk = doc("r2", "# Saved in another window");
  await click(button("Save CV"));
  await waitFor(() => button("Keep my text"));
  expect(api.callsTo("POST /api/setup/steps/cv/apply")[0].body).toEqual({ input: { revision: "r1", markdown: "# Typed here" } });
  expect(q<HTMLTextAreaElement>("textarea")!.value).toBe("# Typed here");
  await click(button("Keep my text"));
  refuse = false;
  await waitFor(() => !button("Keep my text"));
  await click(button("Save CV"));
  await waitFor(() => api.callsTo("POST /api/setup/steps/cv/apply").length === 2);
  expect(api.callsTo("POST /api/setup/steps/cv/apply")[1].body).toEqual({ input: { revision: "r2", markdown: "# Typed here" } });

  refuse = true;
  onDisk = doc("r3", "# Saved again elsewhere");
  await type(q<HTMLTextAreaElement>("textarea"), "# Typed again");
  await click(button("Save CV"));
  await click(await waitFor(() => button("Load the new version")));
  await waitFor(() => q<HTMLTextAreaElement>("textarea")?.value === "# Saved again elsewhere");
});

it("takes a CV saved elsewhere when the window comes back and nothing is typed here, and never replaces typed text", async () => {
  const doc = (revision: string, content: string) => ok({ file: "cv.md", exists: true, template: false, content, revision, issues: [] });
  let onDisk = doc("r1", "# Loaded here");
  const api = setupApi(setupState({ cv: { status: "partial" } }), { "GET /api/config/cv": () => onDisk });
  window.history.replaceState(null, "", "/setup?step=cv");
  await mount(<Setup />);
  await waitFor(() => q<HTMLTextAreaElement>("textarea")?.value === "# Loaded here");
  onDisk = doc("r2", "# Saved in another window");
  await act(async () => void window.dispatchEvent(new Event("focus")));
  await waitFor(() => q<HTMLTextAreaElement>("textarea")?.value === "# Saved in another window");
  await type(q<HTMLTextAreaElement>("textarea"), "# Typed here");
  onDisk = doc("r3", "# Saved again elsewhere");
  await act(async () => void window.dispatchEvent(new Event("focus")));
  await waitFor(() => api.callsTo("GET /api/config/cv").length >= 3);
  await settle();
  expect(q<HTMLTextAreaElement>("textarea")!.value).toBe("# Typed here");
});
