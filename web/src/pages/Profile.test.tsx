import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, byText, click, mount, q, qa, settle, text, type, waitFor } from "../testing/dom";
import { config, profileValues } from "../testing/fixtures_pages";
import type { ConfigDocument } from "../wire";
import { navigate } from "../router";
import Profile from "./Profile";
import { parseSheet, sheetToMarkdown } from "./profile/CvSheet";

interface Server {
  api: FakeApi;
  docs: Record<string, ConfigDocument>;
  values: { [key: string]: unknown };
  profileRevision: () => string;
}

function visit(tab: string) {
  window.history.replaceState(null, "", tab === "cv" ? "/profile" : `/profile?tab=${tab}`);
}

const editMode = (name: string) => qa<HTMLInputElement>("input[type=radio]").find((r) => r.parentElement?.textContent === name) ?? null;
// field answers the nth control whose visible label reads text.
const field = <T extends HTMLElement = HTMLInputElement>(text: string, n = 0): T | null => {
  const label = qa<HTMLLabelElement>("label").filter((l) => l.textContent === text)[n];
  return label ? document.getElementById(label.htmlFor) as T | null : null;
};

// The server keeps the Markdown files and the parsed profile. A profile patch
// does not rewrite the values: each test reads the request and states what it
// expects, then moves the values itself when the page must show the result.
function server(over: { docs?: Record<string, ConfigDocument>; values?: { [key: string]: unknown }; profile?: Partial<ConfigDocument> } = {}): Server {
  const docs: Record<string, ConfigDocument> = {
    cv: config("cv", { content: "# Jordan Sample\n\nBuilt reef survey tools.\n" }),
    narrative: config("narrative", { content: "I build careful software.\n" }),
    voice: config("voice", { exists: false, revision: "absent", content: "" }),
    ...over.docs,
  };
  const s: Server = { api: undefined as never, docs, values: over.values ?? profileValues(), profileRevision: () => "profile-rev-1" };
  s.api = fakeApi({
    "GET /api/config/cv": () => ok(s.docs.cv),
    "GET /api/config/narrative": () => ok(s.docs.narrative),
    "GET /api/config/voice": () => ok(s.docs.voice),
    "GET /api/config/cv/draft": () => ok({ revision: "cv-rev-1", employment: [], education: [], skills: [], identity: {}, dismissed: [] }),
    "GET /api/config/profile": () => ok(config("profile", { revision: s.profileRevision(), values: s.values, ...over.profile })),
    "PUT /api/config/cv": (c) => {
      s.docs.cv = { ...s.docs.cv, content: (c.body as { content: string }).content, revision: "cv-rev-2", template: false };
      return ok({ revision: "cv-rev-2", changed: true });
    },
    "PUT /api/config/voice": (c) => {
      s.docs.voice = config("voice", { content: (c.body as { content: string }).content, revision: "voice-rev-1" });
      return ok({ revision: "voice-rev-1", changed: true });
    },
    "PATCH /api/config/profile": () => ok({ revision: "profile-rev-2", changed: true }),
  });
  return s;
}

describe("Profile: CV, narrative and voice", () => {
  it("edits the CV as Markdown with a live preview and saves it against the loaded revision", async () => {
    const s = server();
    visit("cv");
    await mount(<Profile />);
    await click(await waitFor(() => editMode("Markdown")));
    const area = await waitFor(() => q<HTMLTextAreaElement>("textarea.editor"));
    expect(area.value).toContain("Built reef survey tools.");
    expect(q("[aria-label=Preview]")?.textContent).toContain("Jordan Sample");
    // The preview scrolls on its own beside the editor, so the keyboard can reach it.
    expect(q<HTMLElement>("[aria-label=Preview]")?.tabIndex).toBe(0);
    await type(area, "# Jordan Sample\n\nBuilt reef survey tools and a CLI.\n");
    expect(q("[aria-label=Preview]")?.textContent).toContain("and a CLI");
    expect(q(".save-bar")?.textContent).toContain("Unsaved changes.");
    expect(qa("h2, label").filter((e) => e.textContent === "CV" && !e.classList.contains("sr-only"))).toHaveLength(1);
    await click(button("Save CV"));
    await waitFor(() => s.api.callsTo("PUT /api/config/cv").length === 1);
    expect(s.api.callsTo("PUT /api/config/cv")[0].body).toEqual({ content: "# Jordan Sample\n\nBuilt reef survey tools and a CLI.\n", expectedRevision: "cv-rev-1" });
    await waitFor(() => /Saved at /.test(q(".save-bar")?.textContent ?? ""));
  });

  it("edits the CV field by field and writes it back in the shape it came in", async () => {
    const s = server({
      docs: {
        cv: config("cv", {
          content: [
            "# Jordan Sample",
            "",
            "**Email:** jordan@example.com",
            "**Mobile:** +1 555 0100",
            "",
            "## Education",
            "",
            "### Example University",
            "**Master of Science in Marine Biology**",
            "Durham, USA",
            "Aug. 2024 \u2013 May 2026",
            "",
            "## Experience",
            "",
            "### Reef Analyst, Sample Labs, Austin, TX (2021-03 to present)",
            "",
            "- Built reef survey tools, used by four teams.",
            "",
          ].join("\n"),
        }),
      },
    });
    visit("cv");
    await mount(<Profile />);
    await waitFor(() => q(".cv-form"));
    // Under the panel's h2 its heading is an h3: no level is skipped (axe heading-order).
    expect(qa(".cv-form h3, .cv-form h4").map((h) => `${h.tagName} ${h.textContent}`)).toEqual(["H3 Contact details"]);
    expect(editMode("Form")?.checked).toBe(true);
    await type(field("Degree or title"), "MSc Marine Biology");
    expect(q(".cv-entry em")?.textContent).toBe("MSc Marine Biology");
    await type(field<HTMLTextAreaElement>("Bullets, one per line", 1), "Built reef survey tools, used by four teams.\nMentored two interns.\n");
    await click(byLabel(/^Move Experience up$/));
    expect(qa(".cv-sheet h4").map((h) => h.textContent)).toEqual(["Experience", "Education"]);
    await click(button("Save CV"));
    await waitFor(() => s.api.callsTo("PUT /api/config/cv").length === 1);
    expect((s.api.callsTo("PUT /api/config/cv")[0].body as { content: string }).content).toBe(
      [
        "# Jordan Sample",
        "",
        "**Email:** jordan@example.com",
        "**Mobile:** +1 555 0100",
        "",
        "## Experience",
        "",
        "### Reef Analyst, Sample Labs, Austin, TX (2021-03 to present)",
        "",
        "- Built reef survey tools, used by four teams.",
        "- Mentored two interns.",
        "",
        "## Education",
        "",
        "### Example University",
        "**MSc Marine Biology**",
        "Durham, USA",
        "Aug. 2024 \u2013 May 2026",
        "",
      ].join("\n"),
    );
  });

  it("opens a CV with comments or tables as Markdown, since the form would drop them", async () => {
    server({ docs: { cv: config("cv", { content: "# Jordan Sample\n\n<!-- private note -->\n\n## Skills\n\n- Go\n" }) } });
    visit("cv");
    await mount(<Profile />);
    await waitFor(() => q("textarea.editor"));
    expect(editMode("Markdown")?.checked).toBe(true);
    await click(editMode("Form"));
    expect(q(".editor-modes .note")?.textContent).toContain("does not keep");
  });

  it("writes an unchanged CV back byte for byte, in either cv.md shape", () => {
    const shapes = [
      "# Jordan Sample\n\n**Email:** jordan@example.com\n**GitHub:** https://github.com/jordan\n\n## Education\n\n### Example University\n**BSc Mathematics**\nHarborview, China\nSep. 2020 \u2013 Jun. 2024\n\n## Skills\n\n- **Languages:** Go, Python\n",
      "# Alex Example\n\nDurham, NC | alex@example.com\n\n## Experience\n\n### Senior Engineer, Northwind Labs, Durham, NC (2022-04 to present)\n\n- Replaced a hand-run release process.\n- Moved 120 services to Kubernetes.\n\n## Summary\n\nPlatform and backend engineer with nine years of experience building platforms.\n",
    ];
    for (const md of shapes) expect(sheetToMarkdown(parseSheet(md))).toBe(md);
  });

  it("reads the narrative as section cards, hides its comments and edits a table as a grid", async () => {
    const content = [
      "# Profile Context",
      "",
      "<!-- THIS FILE IS YOURS. -->",
      "",
      "## Your Target Roles",
      "",
      "| Archetype | What they buy |",
      "|-----------|---------------|",
      "| **Applied AI Engineer** | Real AI products |",
      "",
      "## Proof points",
      "",
      "- Shipped a survey tool.",
      "",
    ].join("\n");
    const s = server({ docs: { narrative: config("narrative", { content }) } });
    s.api.on("PUT /api/config/narrative", () => ok({ revision: "narrative-rev-2", changed: true }));
    visit("narrative");
    await mount(<Profile />);
    await waitFor(() => q(".doc-part"));
    expect(qa(".doc-part-head h3").map((h) => h.textContent)).toEqual(["Introduction", "Your Target Roles", "Proof points"]);
    expect(text()).not.toContain("THIS FILE IS YOURS");
    expect(q("[aria-label='Your Target Roles'] table")?.textContent).toContain("Real AI products");
    await click(byLabel(/^Edit Your Target Roles$/));
    await type(q<HTMLTextAreaElement>("[aria-label='Row 1, What they buy']"), "Shipped AI products");
    await click(byLabel(/^Done editing Your Target Roles$/));
    expect(q("[aria-label='Your Target Roles'] table")?.textContent).toContain("Shipped AI products");
    await click(button("Save Narrative"));
    await waitFor(() => s.api.callsTo("PUT /api/config/narrative").length === 1);
    expect((s.api.callsTo("PUT /api/config/narrative")[0].body as { content: string }).content).toBe(
      content.replace("|-----------|---------------|\n| **Applied AI Engineer** | Real AI products |", "|---|---|\n| **Applied AI Engineer** | Shipped AI products |"),
    );
  });

  it("refuses a stale save, then keeps the user's text or takes the latest, as the user picks", async () => {
    const s = server();
    s.api.on("PUT /api/config/cv", () => fail("revision_conflict", "cv.md changed on disk; reload it and apply the change again"));
    visit("cv");
    await mount(<Profile />);
    await click(await waitFor(() => editMode("Markdown")));
    const area = () => q<HTMLTextAreaElement>("textarea.editor")!;
    await type(area(), "my edit");
    await click(button("Save CV"));
    await waitFor(() => button("Keep my text"));
    s.docs.cv = config("cv", { content: "someone else's text", revision: "cv-rev-9" });
    await click(button("Keep my text"));
    await waitFor(() => s.api.callsTo("GET /api/config/cv").length === 2);
    expect(area().value).toBe("my edit");
    await click(button("Save CV"));
    await waitFor(() => s.api.callsTo("PUT /api/config/cv").length === 2);
    expect(s.api.callsTo("PUT /api/config/cv")[1].body).toEqual({ content: "my edit", expectedRevision: "cv-rev-9" });
    s.docs.cv = config("cv", { content: "someone else's text", revision: "cv-rev-10" });
    await click(await waitFor(() => button("Load the new version")));
    await waitFor(() => area().value === "someone else's text");
  });

  // GET /api/config/{file}/backups lists what earlier saves replaced; one is
  // brought back only by a save against the loaded revision (ConfigSave).
  it("brings back a text an earlier save replaced, written only when the user saves", async () => {
    const s = server();
    // Two saves in the same minute (the stamps are whole seconds, newest first).
    s.api.on("GET /api/config/narrative/backups", () => ok({ backups: [{ savedAt: "2026-10-01T09:00:40.000Z", content: "A later draft.\n" }, { savedAt: "2026-10-01T09:00:05.000Z", content: "My careful first narrative.\n" }] }));
    s.api.on("PUT /api/config/narrative", () => ok({ revision: "narrative-rev-2", changed: true }));
    visit("narrative");
    await mount(<Profile />);
    const earlier = await waitFor(() => qa<HTMLDetailsElement>("details").find((d) => d.querySelector("summary")?.textContent === "Earlier versions"));
    earlier.open = true;
    await act(async () => earlier.dispatchEvent(new Event("toggle")));
    const versions = () => qa("button").filter((b) => /^Open the version replaced/.test(b.getAttribute("aria-label") ?? ""));
    const opens = await waitFor(() => versions().length === 2 && versions());
    // Each entry is told apart from the other by its accessible name.
    expect(new Set(opens.map((b) => b.getAttribute("aria-label"))).size).toBe(2);
    await click(opens[1]);
    expect(s.api.callsTo("PUT /api/config/narrative")).toHaveLength(0);
    await click(await waitFor(() => editMode("Markdown")));
    expect(q<HTMLTextAreaElement>("textarea.editor")!.value).toBe("My careful first narrative.\n");
    await click(button("Save Narrative"));
    await waitFor(() => s.api.callsTo("PUT /api/config/narrative").length === 1);
    expect(s.api.callsTo("PUT /api/config/narrative")[0].body).toEqual({ content: "My careful first narrative.\n", expectedRevision: s.docs.narrative.revision });
  });

  it("imports a CV file into the editor without saving it", async () => {
    const s = server();
    s.api.on("POST /api/setup/cv/extract", () => ok({ markdown: "# Imported CV\n" }));
    visit("cv");
    await mount(<Profile />);
    const picker = await waitFor(() => q<HTMLInputElement>("input[type=file]"));
    const file = new File(["# Imported CV\n"], "cv.md", { type: "text/markdown" });
    Object.defineProperty(picker, "files", { value: [file], configurable: true });
    await act(async () => {
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await waitFor(() => field("Full name")?.value === "Imported CV");
    expect(q("[aria-label=Preview]")?.textContent).toContain("Imported CV");
    expect(s.api.callsTo("PUT /api/config/cv")).toHaveLength(0);
  });

  it("creates the optional voice file by saving against the absent revision", async () => {
    const s = server();
    visit("voice");
    await mount(<Profile />);
    await click(await waitFor(() => button("Edit")));
    await type(q<HTMLTextAreaElement>("textarea.doc-text"), "Plain and direct.");
    await click(button("Save Voice samples"));
    await waitFor(() => s.api.callsTo("PUT /api/config/voice").length === 1);
    expect(s.api.callsTo("PUT /api/config/voice")[0].body).toEqual({ content: "Plain and direct.", expectedRevision: "absent" });
  });
});

describe("Profile: résumés", () => {
  const roster = (...ids: string[]) =>
    profileValues({ resumes: ids.map((id, i) => ({ id, label: id.toUpperCase(), file: `resumes/${id}.pdf`, default: i === 0, tags: [] })) });
  const pdf = (name: string, size = 20, type = "application/pdf") => new File([new Uint8Array(size).fill(37)], name, { type });
  async function choose(file: File) {
    const picker = await waitFor(() => q<HTMLInputElement>("input[type=file]"));
    Object.defineProperty(picker, "files", { value: [file], configurable: true });
    await act(async () => {
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
  }
  const written = (s: Server) => (s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: { path: string; value: string }[] }).ops[0];

  it("Second default: marking another résumé as default sends a roster with exactly one default", async () => {
    const s = server({ values: roster("backend", "data") });
    visit("resumes");
    await mount(<Profile />);
    const radios = await waitFor(() => (qa<HTMLInputElement>("input[type=radio]").length === 2 ? qa<HTMLInputElement>("input[type=radio]") : null));
    expect(radios.map((r) => r.checked)).toEqual([true, false]);
    await click(radios[1]);
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    const op = written(s);
    expect(op.path).toBe("resumes");
    expect(op.value.match(/default: true/g)).toHaveLength(1);
    expect(op.value.match(/default: false/g)).toHaveLength(1);
    expect(op.value.indexOf('id: "data"')).toBeLessThan(op.value.indexOf("default: true"));
    expect((s.api.callsTo("PATCH /api/config/profile")[0].body as { expectedRevision: string }).expectedRevision).toBe("profile-rev-1");
  });

  it("uploads a PDF, then adds it to the roster as the default when it is the first", async () => {
    const s = server({ values: profileValues({ resumes: [] }) });
    s.api.on("POST /api/resumes", () => ok({ file: "resumes/backend-cv.pdf" }));
    visit("resumes");
    await mount(<Profile />);
    await choose(pdf("Backend CV.pdf"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    const up = s.api.callsTo("POST /api/resumes")[0];
    expect(up.query.get("name")).toBe("backend-cv");
    expect(up.headers.get("Content-Type")).toBe("application/pdf");
    expect(written(s).value).toContain('file: "resumes/backend-cv.pdf"');
    expect(written(s).value).toContain("default: true");
  });

  it("re-adding a removed résumé tries the next free name instead of replacing the PDF left on disk", async () => {
    const s = server({ values: roster("data") });
    const stored = new Set(["backend", "backend-2"]);
    s.api.on("POST /api/resumes", (c) => {
      const name = c.query.get("name") ?? "";
      if (stored.has(name)) return fail("conflict", `resumes/${name}.pdf already exists`, { details: { reason: "file_exists" } });
      stored.add(name);
      return ok({ file: `resumes/${name}.pdf` });
    });
    visit("resumes");
    await mount(<Profile />);
    await choose(pdf("Backend.pdf"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect(s.api.callsTo("POST /api/resumes").map((c) => c.query.get("name"))).toEqual(["backend", "backend-2", "backend-3"]);
    expect(written(s).value).toContain('id: "backend-3"');
    expect(written(s).value).toContain('file: "resumes/backend-3.pdf"');
  });

  it("tells the user when the PDF was stored but the roster was not updated", async () => {
    const s = server();
    s.api.on("POST /api/resumes", () => ok({ file: "resumes/extra.pdf" }));
    s.api.on("PATCH /api/config/profile", () => fail("validation_failed", "resumes[1].file: file is not a PDF"));
    visit("resumes");
    await mount(<Profile />);
    await choose(pdf("extra.pdf"));
    await waitFor(() => text().includes("The file was stored as resumes/extra.pdf, but the roster was not updated"));
  });

  it("confirms a deletion and hands the default to the next résumé", async () => {
    const s = server({ values: roster("backend", "data") });
    visit("resumes");
    await mount(<Profile />);
    await click(await waitFor(() => qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === "Delete BACKEND")!));
    await waitFor(() => q("dialog"));
    expect(s.api.callsTo("PATCH /api/config/profile")).toHaveLength(0);
    await click(button("Delete from roster"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect(written(s).value).not.toContain("backend");
    expect(written(s).value).toContain("default: true");
  });
});

describe("Profile: locations", () => {
  const places = () => qa("ol li span:first-child").map((s) => s.textContent);

  it("saves the home location, the work arrangement, the order of places and the refused places in one edit", async () => {
    const s = server();
    visit("locations");
    await mount(<Profile />);
    await waitFor(() => places().length === 3);
    await click(qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === "Move down Lisbon")!);
    await type(qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent === "City")!, "Porto");
    await click(qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === "Stop refusing Atlantis")!);
    await click(qa<HTMLInputElement>("input[type=checkbox]").find((i) => i.labels?.[0]?.textContent === "Hybrid")!);
    await type(qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent === "Most days a week in an office")!, "2");
    await type(document.getElementById("relocate-default") as HTMLSelectElement, "closed");
    await click(button("Save locations"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    const ops = (s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: { op: string; path: string; value?: string }[] }).ops;
    expect(ops.find((o) => o.path === "candidate.address.city")).toEqual({ op: "set", path: "candidate.address.city", value: '"Porto"' });
    expect(ops.find((o) => o.path === "preferences.relocation.places")?.value).toBe('- "Oslo"\n- "Lisbon"\n- "Berlin"');
    expect(ops.find((o) => o.path === "preferences.relocation.excluded")?.value).toBe("[]");
    expect(ops.map((o) => o.path)).not.toContain("candidate.address.country");
    // The work preferences profile.yml keeps (workspace.Preferences): a list of modes, a number, an enum.
    expect(ops.filter((o) => o.path.startsWith("preferences.") && !o.path.includes("places") && !o.path.includes("excluded"))).toEqual([
      { op: "set", path: "preferences.relocation.default", value: '"closed"' },
      { op: "set", path: "preferences.work_modes", value: '- "hybrid"' },
      { op: "set", path: "preferences.max_days_in_office", value: "2" },
    ]);
  });

  // Office days and travel are whole numbers; typed text the profile cannot
  // hold is never read as empty, which would clear the stored value.
  it("holds a save of office days that are no whole number, and saves one that is", async () => {
    const s = server();
    visit("locations");
    await mount(<Profile />);
    await type(await waitFor(() => field("Most days a week in an office")), "two");
    expect(button("Save locations")!.disabled).toBe(true);
    await type(field("Most days a week in an office"), "3");
    await click(button("Save locations"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect((s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: unknown[] }).ops).toEqual([{ op: "set", path: "preferences.max_days_in_office", value: "3" }]);
  });

});

describe("Profile: work authorization", () => {
  it("preselects no sponsorship answer and sends only what the user changed", async () => {
    const s = server();
    visit("authorization");
    await mount(<Profile />);
    await waitFor(() => qa("input[type=radio]").length === 6);
    expect(qa<HTMLInputElement>("input[type=radio]").filter((r) => r.checked).map((r) => r.parentElement?.textContent)).toEqual(["Not answered", "Not answered"]);
    expect(text()).toContain("Not answered yet. Forms that ask are left for you.");
    expect(button("Save authorization")?.disabled).toBe(true);
    await click(qa<HTMLInputElement>("input[type=radio]")[1]); // now: No
    await click(button("Save authorization"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect((s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: unknown[] }).ops).toEqual([{ op: "set", path: "work_authorization.requires_sponsorship_now", value: "false" }]);
  });

  // Sanctions questions read every citizenship, so a dual national lists both.
  it("records each country of citizenship as a two-letter code", async () => {
    const s = server();
    visit("authorization");
    await mount(<Profile />);
    await waitFor(() => qa("input[type=radio]").length === 6);
    await type(field("Countries of citizenship"), "cn, ca");
    await click(button("Save authorization"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect((s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: unknown[] }).ops).toEqual([{ op: "set", path: "work_authorization.citizenships", value: '- "CN"\n- "CA"' }]);
  });

  it("takes a sponsorship answer back to not answered, which removes it from the profile", async () => {
    const s = server({ values: profileValues({ work_authorization: { authorized_countries: ["PT"], requires_sponsorship_now: true, requires_sponsorship_future: null } }) });
    visit("authorization");
    await mount(<Profile />);
    await click(await waitFor(() => qa<HTMLInputElement>("input[type=radio]").find((r) => r.name === "Do you need sponsorship now?" && r.parentElement?.textContent === "Not answered")));
    await click(button("Save authorization"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect((s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: unknown[] }).ops).toEqual([{ op: "unset", path: "work_authorization.requires_sponsorship_now" }]);
  });
});

describe("Profile: identity and targets", () => {

  // A link or shortcut to this same page without the tab (the sidebar's
  // Profile) drops the form as surely as another page does. Leave is asked once.
  it("asks before a link or a tab switch drops unsaved edits", async () => {
    server();
    visit("identity");
    await mount(<Profile />);
    await type(await waitFor(() => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent === "Preferred name")), "Jo");
    await act(async () => navigate("/profile"));
    await click(await waitFor(() => button("Cancel")));
    expect(window.location.search).toBe("?tab=identity");
    await click(byText("button[role=tab]", "CV")!);
    await click(await waitFor(() => button("Leave")));
    await waitFor(() => q("dialog") === null);
    expect(window.location.pathname + window.location.search).toBe("/profile");
  });

  // A typed amount is never read as empty, which would clear the stored one.
  it("saves pay typed with thousands separators as a number, and holds a save of text that is none", async () => {
    const s = server();
    visit("identity");
    await mount(<Profile />);
    await type(await waitFor(() => field("Pay you are aiming for")), "120,000");
    await type(field("Lowest pay you would accept"), "about 90k");
    expect(button("Save targets")!.disabled).toBe(true);
    await type(field("Lowest pay you would accept"), "");
    await click(button("Save targets"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect((s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: unknown[] }).ops).toEqual([{ op: "set", path: "targets.compensation.target", value: "120000" }]);
  });

  it("shows no marker for the user's own values and saves only the changed fields, a street address left to the user as the ask-me marker", async () => {
    const s = server();
    visit("identity");
    await mount(<Profile />);
    await waitFor(() => qa("input").length > 5);
    expect(text()).not.toContain("Still an example");
    const labelled = (name: string) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent === name)!;
    await type(labelled("Preferred name"), "Jo");
    await click(labelled("Street address").parentElement!.querySelector<HTMLInputElement>("input[type=checkbox]")!);
    expect(labelled("Street address").disabled).toBe(true);
    await click(button("Save identity"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect((s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: unknown[] }).ops).toEqual([
      { op: "set", path: "candidate.preferred_name", value: '"Jo"' },
      { op: "set", path: "candidate.address.line1", value: '"@ask-me"' },
    ]);
  });

  it("a stale identity save reloads under the kept edits, takes what changed elsewhere and sends only the edits; history edits survive it", async () => {
    const s = server();
    s.api.on("PATCH /api/config/profile", () => fail("revision_conflict", "profile.yml changed on disk; reload it and apply the change again"));
    visit("identity");
    await mount(<Profile />);
    await click(await waitFor(() => button("Add job")));
    const jobs = () => qa("input").filter((i) => (i as HTMLInputElement).labels?.[0]?.textContent?.startsWith("Employer")).length;
    const before = jobs();
    await type(qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent === "Preferred name")!, "Jo");
    await click(button("Save identity"));
    await waitFor(() => button("Keep my text"));
    s.profileRevision = () => "profile-rev-7";
    s.values = { ...s.values, candidate: { ...(s.values.candidate as object), email: "changed@elsewhere.example" } };
    s.api.on("PATCH /api/config/profile", () => ((s.profileRevision = () => "profile-rev-8"), ok({ revision: "profile-rev-8", changed: true })));
    await click(button("Keep my text"));
    await waitFor(() => qa<HTMLInputElement>("input").some((i) => i.value === "changed@elsewhere.example"));
    await click(button("Save identity"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 2);
    expect(s.api.callsTo("PATCH /api/config/profile")[1].body).toEqual({ expectedRevision: "profile-rev-7", ops: [{ op: "set", path: "candidate.preferred_name", value: '"Jo"' }] });
    await waitFor(() => s.api.callsTo("GET /api/config/profile").length === 3);
    await settle();
    expect(jobs()).toBe(before);
  });

  it("edits employment as rows and shows the validator's message when the profile refuses it", async () => {
    const s = server();
    s.api.on("PATCH /api/config/profile", () => fail("validation_failed", "profile.yml history.employment[0].end: start (2021-05) must not be later than end (2020-01)"));
    visit("identity");
    await mount(<Profile />);
    await click(await waitFor(() => button("Add job")));
    const cell = (label: string) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;
    await type(cell("Employer"), "Example Corp");
    await type(cell("Start"), "2021-05");
    await type(cell("End"), "2020-01");
    await click(button("Save history and skills"));
    await waitFor(() => text().includes("must not be later than end"));
    const op = (s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: { path: string; value: string }[] }).ops[0];
    expect(op.path).toBe("history.employment");
    expect(op.value).toBe('- employer: "Example Corp"\n  start: "2021-05"\n  end: "2020-01"');
  });

  // Every key of a history.education item in profile.yml (workspace.Education)
  // survives an edit of another field of that item: forms read grade_average.
  it("keeps a school's grade average when another field of the school is edited", async () => {
    const s = server({ values: profileValues({ history: { education: [{ school: "Example Polytechnic", degree: "BSc", grade_average: "3.8/4.0" }] } }) });
    visit("identity");
    await mount(<Profile />);
    const degree = await waitFor(() => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent?.startsWith("Degree")));
    await type(degree, "MSc");
    await click(button("Save history and skills"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    const op = (s.api.callsTo("PATCH /api/config/profile")[0].body as { ops: { path: string; value: string }[] }).ops[0];
    expect(op).toEqual({ op: "set", path: "history.education", value: '- school: "Example Polytechnic"\n  degree: "MSc"\n  grade_average: "3.8/4.0"' });
  });

  it("does not offer to edit a profile file that has errors", async () => {
    server({ profile: { issues: [{ code: "unknown_key", message: "unknown key unknown_thing", path: "unknown_thing" }] } });
    visit("identity");
    await mount(<Profile />);
    await waitFor(() => text().includes("The profile file has errors"));
    expect(button("Save identity")).toBeNull();
  });
});

describe("Profile: company relationships", () => {
  // The matcher answers a family from its list only while
  // company_relationships.complete.<family> is a date no earlier than the
  // recorded date of every relationship of that family (answer-bank spec,
  // "Company-relationship lane"); aliases are a list and current a boolean
  // (workspace-and-config spec), and a company name may hold a comma, so
  // aliases are separated by semicolons. The press writes today's date (flow round 12, S-2).
  it("shows each list with its complete date as the matcher reads it, confirms one with today's date keeping focus on the press, and saves the lists", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2027, 2, 10, 12));
    const relationships = [
      { family: "relative", company: "Northwind Example", aliases: ["Northwind Example, Inc.", "Northwind Labs"], current: true, person_name: "Alex Example", relation: "Sibling", recorded: "2027-03-08" },
      { family: "friend", company: "Contoso Example", recorded: "2027-03-04" },
    ];
    const values = (complete: Record<string, string>) => profileValues({ company_relationships: { relationships, complete } });
    const s = server({ values: values({ employment: "2027-03-05", relative: "2027-03-01", friend: "2027-03-04" }) });
    s.api.on("PATCH /api/config/profile", () => {
      s.values = values({ employment: "2027-03-05", relative: "2027-03-10", friend: "2027-03-04" });
      s.profileRevision = () => "profile-rev-2";
      return ok({ revision: "profile-rev-2", changed: true });
    });
    visit("identity");
    await mount(<Profile />);
    const mark = (list: string) => qa<HTMLInputElement>("input[type=checkbox]").find((i) => i.labels?.[0]?.textContent?.trim() === `This list is complete: ${list}`)!;
    const state = (list: string) => document.getElementById(mark(list).getAttribute("aria-describedby")!)?.textContent ?? "";
    await waitFor(() => mark("companies where a relative works"));
    expect([field("Company of relative 1")?.value, field("Other names of relative 1")?.value, field<HTMLSelectElement>("Current of relative 1")?.value]).toEqual(["Northwind Example", "Northwind Example, Inc.; Northwind Labs", "yes"]);
    for (const [list, confirmed, dates] of [
      ["companies you worked for", true, ["2027-03-05"]],
      ["companies where a relative works", false, ["2027-03-08", "2027-03-01"]],
      ["companies where a friend works", true, ["2027-03-04"]],
      ["companies you applied to before", false, []],
    ] as const) {
      expect(mark(list).checked, list).toBe(confirmed);
      for (const d of dates) expect(state(list), list).toContain(d);
    }

    const relative = mark("companies where a relative works");
    relative.focus();
    await click(relative);
    await waitFor(() => relative.checked);
    expect(s.api.callsTo("PATCH /api/config/profile")[0].body).toEqual({ expectedRevision: "profile-rev-1", ops: [{ op: "set", path: "company_relationships.complete", value: 'employment: "2027-03-05"\nrelative: "2027-03-10"\nfriend: "2027-03-04"' }] });
    expect(document.activeElement).toBe(relative);
    // Clearing a mark takes that family's date back.
    await click(mark("companies you worked for"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 2);
    expect((s.api.callsTo("PATCH /api/config/profile")[1].body as { ops: unknown[] }).ops).toEqual([{ op: "set", path: "company_relationships.complete", value: 'relative: "2027-03-10"\nfriend: "2027-03-04"' }]);

    await type(field<HTMLSelectElement>("Current of relative 1"), "no");
    await click(button("Add friend"));
    expect(field("Recorded on of friend 2")?.value).toBe("2027-03-10");
    await type(field("Company of friend 2"), "Fabrikam Example");
    expect([relative.disabled, mark("companies where a friend works").disabled, mark("companies you worked for").disabled]).toEqual([true, true, false]);
    await click(button("Save relationships"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 3);
    expect((s.api.callsTo("PATCH /api/config/profile")[2].body as { ops: unknown[] }).ops).toEqual([
      {
        op: "set",
        path: "company_relationships.relationships",
        value: [
          '- family: "relative"\n  company: "Northwind Example"\n  aliases:\n    - "Northwind Example, Inc."\n    - "Northwind Labs"\n  current: false\n  person_name: "Alex Example"\n  relation: "Sibling"\n  recorded: "2027-03-08"',
          '- family: "friend"\n  company: "Contoso Example"\n  recorded: "2027-03-04"',
          '- family: "friend"\n  company: "Fabrikam Example"\n  recorded: "2027-03-10"',
        ].join("\n"),
      },
    ]);
  });
});

describe("Profile: draft from an imported CV", () => {
  const draft = {
    revision: "cv-rev-1",
    employment: [{ employer: "Fictional Systems Inc.", title: "Platform Engineer", start: "2020-01", end: "present" }, { employer: "Sample Software LLC" }],
    education: [{ school: "Example University", degree: "BSc Computing", end: "2014-12" }],
    skills: ["Go", "SQL"],
    identity: {},
    dismissed: [] as string[],
  };
  const named = (label: string) => qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === label)!;
  const ops = (s: Server) => (s.api.callsTo("PATCH /api/config/profile")[0].body as { expectedRevision: string; ops: { path: string; value: string }[] });

  async function open(over: Parameters<typeof server>[0] = {}, served: typeof draft = draft) {
    const s = server(over);
    s.api.on("GET /api/config/cv/draft", () => ok(served));
    visit("identity");
    await mount(<Profile />);
    await waitFor(() => text().includes("Draft from your CV"));
    return s;
  }

  it("shows the extracted entries as a draft and saves nothing", async () => {
    const s = await open();
    const t = text();
    for (const want of ["Platform Engineer, Fictional Systems Inc., 2020-01 to present", "Sample Software LLC", "BSc Computing, Example University, 2014-12", "Go", "SQL"]) expect(t).toContain(want);
    expect(t).toContain("None is saved until you confirm it");
    expect(s.api.callsTo("PATCH /api/config/profile")).toHaveLength(0);
  });

  it("confirming one entry saves only that entry, after the entries already saved", async () => {
    const s = await open({ values: profileValues({ history: { employment: [{ employer: "Northwind Example", title: "Analyst" }] } }) });
    await click(named("Confirm Platform Engineer, Fictional Systems Inc., 2020-01 to present"));
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    const body = ops(s);
    expect(body.expectedRevision).toBe("profile-rev-1");
    expect(body.ops).toHaveLength(1);
    expect(body.ops[0].path).toBe("history.employment");
    expect(body.ops[0].value).toBe('- employer: "Northwind Example"\n  title: "Analyst"\n- employer: "Fictional Systems Inc."\n  title: "Platform Engineer"\n  start: "2020-01"\n  end: "present"');
  });

  it("confirms every skill left in one change, after the skills already saved", async () => {
    const served = { ...draft, skills: ["Go", "SQL", "Rust"], dismissed: [] as string[] };
    const s = await open({ values: profileValues({ skills: [{ name: "Python" }] }) }, served);
    keeping(s, served);
    await click(named("Discard Rust"));
    await waitFor(() => named("Confirm Rust") === undefined);
    await click(byText("button", /^Confirm all 2 skills$/)!);
    await waitFor(() => s.api.callsTo("PATCH /api/config/profile").length === 1);
    expect(ops(s).ops).toEqual([{ op: "set", path: "skills", value: '- name: "Python"\n- name: "Go"\n- name: "SQL"' }]);
  });

  // The server keeps the discarded keys (POST /api/config/cv/draft/dismiss).
  const keeping = (s: Server, served: typeof draft) =>
    s.api.on("POST /api/config/cv/draft/dismiss", (c) => {
      const { keys, restore } = c.body as { keys: string[]; restore?: boolean };
      served.dismissed = restore ? served.dismissed.filter((k) => !keys.includes(k)) : [...served.dismissed, ...keys];
      return ok({ ...served });
    });

  it("a discarded entry stays gone after a reload, and the profile is not written", async () => {
    const served = { ...draft, dismissed: [] as string[] };
    const s = await open({}, served);
    keeping(s, served);
    await click(named("Discard SQL"));
    await waitFor(() => named("Confirm SQL") === undefined);
    expect(text()).toContain("Draft from your CV");
    expect(s.api.callsTo("PATCH /api/config/profile")).toHaveLength(0);
    await mount(<Profile />);
    await waitFor(() => text().includes("Draft from your CV"));
    expect(text()).not.toContain("SQL");
  });

  it("does not offer an entry the profile already holds", async () => {
    await open({ values: profileValues({ skills: [{ name: "go" }], history: { employment: [{ employer: "Sample Software LLC" }] } }) });
    expect(named("Confirm Go")).toBeUndefined();
    expect(named("Confirm Sample Software LLC")).toBeUndefined();
    expect(named("Confirm SQL")).toBeDefined();
  });

});
