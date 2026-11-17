import { act } from "react";
import { describe, expect, it } from "vitest";
import { fail, ok } from "../../testing/api";
import { button, click, mount, q, qa, text, type, waitFor } from "../../testing/dom";
import { setupApi, setupState } from "../../testing/fixtures_setup";
import type { IntakeItem, IntakePreview, IntakeSource } from "../../wire";
import Setup from "../Setup";

const src = (label: string, locator = 'line 11, under "Education"', origin = "direct"): IntakeSource => ({ label, hash: label.padEnd(64, "0"), locator, origin });

const item = (over: Partial<IntakeItem> & Pick<IntakeItem, "id" | "type" | "kind" | "summary">): IntakeItem => ({
  entry: {}, sources: [src("cv.md")], fields: [], needsMonth: [], ...over,
});

const conflict = item({
  id: "c1", type: "conflict", kind: "education", summary: "BSc Computer Science, Northern College (2011-09 to 2015-06)",
  entry: { school: "Northern College", degree: "BSc Computer Science", start: "2011-09", end: "2015-06" },
  fields: [{ field: "end", profile: "2015-06", options: [{ value: "2015-06", sources: [src("cv.md")] }, { value: "2015-05", sources: [src("registrar.md")] }] }],
});

const preview = (items: IntakeItem[], over: Partial<IntakePreview> = {}): IntakePreview => ({
  revision: "rev-1",
  documents: [{ name: "cv.md", label: "cv.md", kind: "cv", hash: "a".repeat(64), signal: false }],
  items,
  notes: ["nothing is stored until you accept items"],
  ...over,
});

const files = [new File(["# Alex Example\n"], "cv.md"), new File(["## Education\n"], "registrar.md")];

async function choose(list: File[] = files) {
  await click(await waitFor(() => button("Import several documents")));
  const picker = await waitFor(() => q<HTMLInputElement>("input[aria-label='Career documents']"));
  Object.defineProperty(picker, "files", { value: list, configurable: true });
  await act(async () => {
    picker.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await waitFor(() => qa("[data-doc]").length === list.length);
}

async function host(pv: IntakePreview, accept: Parameters<typeof setupApi>[1] = {}) {
  const api = setupApi(setupState({ cv: { status: "complete" } }), {
    "POST /api/setup/intake/preview": () => ok(pv),
    "POST /api/setup/intake/accept": () => ok({ wrote: true, revision: "rev-2", applied: [], notes: [] }),
    ...accept,
  });
  window.history.replaceState(null, "", "/setup?step=cv");
  await mount(<Setup />);
  await choose();
  await click(button("Preview changes"));
  await waitFor(() => q("[aria-label='Change preview']"));
  return api;
}

const row = (id: string) => q(`[data-item="${id}"]`)!;

describe("Setup: multi-document evidence import", () => {
  it("conflicting graduation dates show both sources and apply only the side the person picks", async () => {
    const api = await host(preview([conflict]));
    expect(api.callsTo("POST /api/setup/intake/preview")[0].body).toEqual({
      documents: [{ name: "cv.md", kind: "cv", text: "# Alex Example\n" }, { name: "registrar.md", kind: "cv", text: "## Education\n" }],
    });
    const c = row("c1").textContent ?? "";
    expect(c).toContain("Keep what your profile says: 2015-06");
    expect(c).toContain("2015-06, from cv.md");
    expect(c).toContain("2015-05, from registrar.md");
    // Nothing is picked on arrival, so nothing can be applied.
    expect(button(/^Apply/)!.disabled).toBe(true);
    await click(row("c1").querySelector("input[type=checkbox]"));
    expect(button(/^Apply/)!.disabled).toBe(true);
    expect(text()).toContain("Pick a side");
    const radios = qa<HTMLInputElement>("input[type=radio]");
    expect(radios).toHaveLength(3);
    expect(radios.some((r) => r.checked)).toBe(false);
    await click(radios[2]);
    await click(button("Apply 1 selected item"));
    await waitFor(() => api.callsTo("POST /api/setup/intake/accept").length === 1);
    expect(api.callsTo("POST /api/setup/intake/accept")[0].body).toMatchObject({
      revision: "rev-1",
      selections: [{ id: "c1", choices: { end: "2015-05" } }],
    });
    await waitFor(() => /^.*Saved \d+ items? to your profile\./.test(text()));
  });

  it("a framework a course mentions is a suggestion, never a fact, until the person confirms it", async () => {
    const sig = item({
      id: "s1", type: "signal", kind: "skill", summary: "Django", entry: { name: "Django" }, sources: [src("outline.txt", "line 3", "inferred")],
      question: "A course outline mentions Django. Do you have working experience with it?",
    });
    const add = item({ id: "a1", type: "addition", kind: "skill", summary: "Python", entry: { name: "Python" } });
    const api = await host(preview([add, sig]));
    expect(q("#intake-additions")!.parentElement!.textContent).not.toContain("Django");
    const s = q("#intake-signals")!.parentElement!;
    expect(s.textContent).toContain("Suggestions, not facts");
    expect(s.textContent).toContain("A course outline mentions Django");
    expect(s.textContent).toContain("outline.txt, line 3 (inferred)");
    expect(qa<HTMLInputElement>("input[type=checkbox]").some((c) => c.checked)).toBe(false);
    // Ticking the addition alone applies the addition, not the suggestion.
    await click(row("a1").querySelector("input"));
    await click(button("Apply 1 selected item"));
    await waitFor(() => api.callsTo("POST /api/setup/intake/accept").length === 1);
    expect(api.callsTo("POST /api/setup/intake/accept")[0].body).toMatchObject({ selections: [{ id: "a1" }] });
    expect((api.callsTo("POST /api/setup/intake/accept")[0].body as { selections: unknown[] }).selections).toHaveLength(1);
  });

  it("a confirmed suggestion is sent with the confirmation and nothing else", async () => {
    const sig = item({ id: "s1", type: "signal", kind: "skill", summary: "Django", entry: { name: "Django" }, question: "Do you have it?" });
    const api = await host(preview([sig]));
    await click(row("s1").querySelector("input"));
    await click(button("Apply 1 selected item"));
    await waitFor(() => api.callsTo("POST /api/setup/intake/accept").length === 1);
    expect((api.callsTo("POST /api/setup/intake/accept")[0].body as { selections: unknown[] }).selections).toEqual([{ id: "s1", confirm: true }]);
  });

  it("a date given only as a year needs its month before the item can be applied", async () => {
    const job = item({
      id: "j1", type: "addition", kind: "employment", summary: "Developer, Harbour Software (2015 to 2017)",
      entry: { employer: "Harbour Software", title: "Developer", start: "2015", end: "2017" }, needsMonth: ["start", "end"],
    });
    const api = await host(preview([job]));
    await click(row("j1").querySelector("input"));
    expect(button(/^Apply/)!.disabled).toBe(true);
    const [start, end] = qa<HTMLInputElement>("input[type=text]").filter((i) => row("j1").contains(i));
    await type(start, "2016-01");
    await type(end, "2017-12");
    expect(button(/^Apply/)!.disabled).toBe(true);
    await type(start, "2015-02");
    expect(button(/^Apply/)!.disabled).toBe(false);
    await click(button("Apply 1 selected item"));
    await waitFor(() => api.callsTo("POST /api/setup/intake/accept").length === 1);
    expect((api.callsTo("POST /api/setup/intake/accept")[0].body as { selections: unknown[] }).selections).toEqual([{ id: "j1", months: { start: "2015-02", end: "2017-12" } }]);
  });

  it("a profile that changed after the preview is refused, and the person is told to preview again", async () => {
    const a = item({ id: "a1", type: "addition", kind: "skill", summary: "Python", entry: { name: "Python" } });
    const api = await host(preview([a]), { "POST /api/setup/intake/accept": () => fail("revision_conflict", "profile.yml changed since the preview was built; preview the documents again") });
    await click(row("a1").querySelector("input"));
    await click(button("Apply 1 selected item"));
    await waitFor(() => /preview the documents again/.test(text()));
    expect(text()).toContain("Preview the documents again to review the change against your current profile.");
    await click(button("Preview changes"));
    await waitFor(() => api.callsTo("POST /api/setup/intake/preview").length === 2);
  });
});
