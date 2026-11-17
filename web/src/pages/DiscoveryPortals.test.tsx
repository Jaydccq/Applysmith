import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, click, mount, q, qa, text, type, waitFor } from "../testing/dom";
import { sources } from "../testing/fixtures_discovery";
import Discovery from "./Discovery";

const stamp = "2026-10-12T09:00:00.000Z";
const open = (search = "") => window.history.replaceState(null, "", "/discovery?tab=sources" + search);

const values = {
  version: 1,
  filters: { title: { positive: ["engineer"], negative: ["intern"] }, locations: { include: ["Remote"] } },
  companies: [
    { name: "Example Co", ats: "greenhouse", slug: "example-co" },
    { name: "Plain Careers", careers_url: "https://careers.example.org/jobs", enabled: false },
  ],
  sources: { indeed: { enabled: true, queries: ["backend engineer", { search: "platform engineer", location: "Remote" }] } },
};
const doc = (over: Record<string, unknown> = {}) => ({ file: "portals", exists: true, template: false, content: "version: 1\n", revision: "r1", issues: [], values, ...over });
const row = (over: Record<string, unknown> = {}) => ({ name: "Example Co", enabled: true, ats: "greenhouse", slug: "example-co", careersUrl: "", board: "greenhouse:example-co", status: "active", ownTitleFilter: false, lastScannedAt: stamp, jobCount: 12, ...over });
const companies = [row(), row({ name: "Plain Careers", enabled: false, ats: "", slug: "", careersUrl: "https://careers.example.org/jobs", board: "", status: "", lastScannedAt: null, jobCount: null, ownTitleFilter: true })];
const registry = { active: 2, dead: 1, blocked: 0, total: 3, enabled: true, lastSeededAt: stamp };
const saved = () => ok({ revision: "r2", changed: true });

function server(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}): FakeApi {
  return fakeApi({
    "GET /api/scan-sources": () => ok({ sources }),
    "GET /api/config/portals": () => ok(doc()),
    "GET /api/portals/companies": () => ok({ companies }),
    "GET /api/registry": () => ok(registry),
    "GET /api/portals/target-roles": () => ok({ revision: "r1", lines: [], ops: [] }),
    ...extra,
  });
}

describe("Portals: company table", () => {
  it("turns a company on by setting that entry alone, with the revision it read", async () => {
    open();
    const api = server({ "PATCH /api/config/portals": saved });
    await mount(<Discovery />);
    await waitFor(() => q('input[aria-label="Scan Plain Careers"]'));
    await click(q('input[aria-label="Scan Plain Careers"]'));
    expect(api.callsTo("PATCH /api/config/portals")[0].body).toEqual({ expectedRevision: "r1", ops: [{ op: "set", path: "companies[1].enabled", value: "true" }] });
  });

  it("asks before removing a company and then removes that entry alone", async () => {
    open();
    const api = server({ "PATCH /api/config/portals": saved });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Remove Example Co$/));
    await click(byLabel(/^Remove Example Co$/));
    expect(api.callsTo("PATCH /api/config/portals")).toHaveLength(0);
    await click(await waitFor(() => qa("dialog button").find((b) => b.textContent === "Remove")));
    expect(api.callsTo("PATCH /api/config/portals")[0].body).toEqual({ expectedRevision: "r1", ops: [{ op: "remove", path: "companies", index: 0 }] });
  });

  // The file on disk changed between the read and the write: someone put a
  // company in front, or removed the one the user is changing.
  const changedOnDisk = (list: unknown[]) => {
    let patches = 0;
    return server({
      "GET /api/config/portals": () => ok(patches === 0 ? doc() : doc({ revision: "r2", values: { ...values, companies: list } })),
      "PATCH /api/config/portals": () => (patches++ === 0 ? fail("revision_conflict", "portals changed on disk; reload it and apply the change again") : saved()),
    });
  };

  it("works a change out again on the file that changed meanwhile and saves it only when the user keeps it", async () => {
    open();
    const api = changedOnDisk([{ name: "First Co", ats: "lever", slug: "first" }, ...values.companies]);
    await mount(<Discovery />);
    await click(await waitFor(() => q('input[aria-label="Scan Plain Careers"]')));
    await click(await waitFor(() => button("Keep my change")));
    expect(api.callsTo("PATCH /api/config/portals")[1].body).toEqual({ expectedRevision: "r2", ops: [{ op: "set", path: "companies[2].enabled", value: "true" }] });
  });

  it("does not bring back a company that was removed meanwhile, and says so", async () => {
    open();
    const api = changedOnDisk([values.companies[0]]);
    await mount(<Discovery />);
    await click(await waitFor(() => q('input[aria-label="Scan Plain Careers"]')));
    await waitFor(() => text().includes("Plain Careers is no longer in portals.yml"));
    expect(button("Keep my change")).toBeNull();
    expect(api.callsTo("PATCH /api/config/portals")).toHaveLength(1);
  });
});

describe("Portals: add a company", () => {
  const detection = (over: Record<string, unknown> = {}) => ({
    input: "https://boards.greenhouse.io/new-co", kind: "board", name: "New Co", careersUrl: "", note: "",
    candidates: [{ ats: "greenhouse", slug: "new-co", company: "New Co", jobs: 4, live: true, reason: "", inRegistry: false, inPortals: false }], ...over,
  });

  it("previews the detected board and slug before anything is written, then adds it", async () => {
    open();
    const api = server({ "POST /api/portals/detect": () => ok(detection()), "PATCH /api/config/portals": saved });
    await mount(<Discovery />);
    await waitFor(() => q('input[type="text"]'));
    await type(q<HTMLInputElement>('form[aria-label="Find a company"] input'), "https://boards.greenhouse.io/new-co");
    await click(button("Preview"));
    await waitFor(() => q('[aria-label="Detection preview"]'));
    expect(q('[aria-label="Detection preview"]')!.textContent).toContain("greenhouse");
    expect(q('[aria-label="Detection preview"]')!.textContent).toContain("new-co");
    expect(q('[aria-label="Detection preview"]')!.textContent).toContain("4 postings");
    expect(api.callsTo("PATCH /api/config/portals")).toHaveLength(0);
    expect(api.callsTo("POST /api/portals/detect")[0].body).toEqual({ input: "https://boards.greenhouse.io/new-co" });
    await click(button("Add to portals"));
    const body = api.callsTo("PATCH /api/config/portals")[0].body as { ops: { op: string; path: string; value: string }[] };
    expect(body.ops).toMatchObject([{ op: "append", path: "companies" }]);
    expect(body.ops[0].value).toContain('name: "New Co"');
    expect(body.ops[0].value).toContain('ats: "greenhouse"');
    expect(body.ops[0].value).toContain('slug: "new-co"');
  });

  it("does not add a board that is already listed or a name that is taken", async () => {
    open();
    server({ "POST /api/portals/detect": () => ok(detection({ candidates: [{ ats: "greenhouse", slug: "example-co", company: "Example Co", jobs: 4, live: true, reason: "", inRegistry: true, inPortals: true }] })) });
    await mount(<Discovery />);
    await waitFor(() => q('form[aria-label="Find a company"] input'));
    await type(q<HTMLInputElement>('form[aria-label="Find a company"] input'), "https://boards.greenhouse.io/example-co");
    await click(button("Preview"));
    await waitFor(() => text().includes("already in portals.yml"));
    expect(button("Add to portals")!.disabled).toBe(true);
    await type(q<HTMLInputElement>('input[type="text"]:not([required])'), "example co");
    expect(text()).toContain("A company called example co is already listed.");
  });
});

describe("Portals: search queries and title filters", () => {
  it("lists a source's saved searches and adds one as a string or a mapping", async () => {
    open();
    const api = server({ "PATCH /api/config/portals": saved });
    await mount(<Discovery />);
    await waitFor(() => q('select'));
    await type(q<HTMLSelectElement>("section[aria-label='Search queries'] select"), "indeed");
    await waitFor(() => q('[aria-label="Saved searches of indeed"]'));
    expect(q('[aria-label="Saved searches of indeed"]')!.textContent).toContain("platform engineer, location: Remote");
    const form = q("form[aria-label='Add a search']")!;
    await type(form.querySelector<HTMLInputElement>("input")!, "data engineer");
    await click(button("Add search"));
    expect(api.callsTo("PATCH /api/config/portals")[0].body).toEqual({ expectedRevision: "r1", ops: [{ op: "append", path: "sources.indeed.queries", value: '"data engineer"' }] });
  });

  it("removes a saved search", async () => {
    open();
    const api = server({ "PATCH /api/config/portals": saved });
    await mount(<Discovery />);
    await waitFor(() => q("section[aria-label='Search queries'] select"));
    await type(q<HTMLSelectElement>("section[aria-label='Search queries'] select"), "indeed");
    await click(await waitFor(() => byLabel(/^Remove the search backend engineer$/)));
    expect(api.callsTo("PATCH /api/config/portals")[0].body).toEqual({ expectedRevision: "r1", ops: [{ op: "remove", path: "sources.indeed.queries", index: 0 }] });
  });

  it("writes only the filter keys the user changed, one per line, and a missing map whole", async () => {
    open();
    const api = server({ "PATCH /api/config/portals": saved });
    await mount(<Discovery />);
    const field = (label: string) => qa<HTMLTextAreaElement | HTMLInputElement>("textarea, input").find((e) => e.labels?.[0]?.textContent === label)!;
    await waitFor(() => field("Title must contain one of"));
    expect(field("Title must contain one of").value).toBe("engineer");
    expect(field("Requests at once to one site").placeholder).toBe("4 (default)");
    await type(field("Title must contain one of"), "engineer\nanalyst");
    await type(field("Title must contain none of"), "intern\n\n  contract  ");
    await type(field("Companies to skip"), "Example Corp");
    await type(field("Requests at once to one site"), "3");
    await click(button("Save filters"));
    await waitFor(() => api.callsTo("PATCH /api/config/portals").length === 1);
    const body = api.callsTo("PATCH /api/config/portals")[0].body as { expectedRevision: string; ops: { path: string; value: string }[] };
    expect(body.ops.map((o) => o.path)).toEqual(["filters.title.positive", "filters.title.negative", "filters.skip_companies", "concurrency"]);
    expect(body.ops[1].value).toContain('- "contract"');
    expect(body.ops[1].value).not.toContain('""');
    expect(body.ops[3].value).toContain("3");
    expect(body.expectedRevision).toBe("r1");
  });
});

describe("Portals: use my target roles", () => {
  it("offers the server's plan while it would change the file and writes exactly that plan", async () => {
    open();
    const ops = [{ op: "set", path: "filters.title.positive", value: '["engineer","Data Engineer"]' }];
    const api = server({
      "GET /api/portals/target-roles": () => ok({ revision: "r1", lines: ["Title must contain one of: add Data Engineer"], ops }),
      "PATCH /api/config/portals": saved,
    });
    await mount(<Discovery />);
    const section = await waitFor(() => q("section[aria-label='Use my target roles']"));
    expect(section.textContent).toContain("add Data Engineer");
    await click(button("Use my target roles"));
    await waitFor(() => api.callsTo("PATCH /api/config/portals").length === 1);
    expect(api.callsTo("PATCH /api/config/portals")[0].body).toEqual({ expectedRevision: "r1", ops });
  });
});

describe("Portals: raw text and a file the forms cannot read", () => {
  it("stands the forms down and points to the raw editor when the file has problems", async () => {
    open();
    server({ "GET /api/config/portals": () => ok(doc({ issues: [{ line: 3, path: "companies[0]", code: "constraint", message: "needs one source form" }], values: undefined })) });
    await mount(<Discovery />);
    await waitFor(() => text().includes("portals.yml has problems"));
    expect(qa("a").some((a) => a.getAttribute("href") === "/discovery?tab=sources&view=raw")).toBe(true);
    expect(q("section[aria-label='Filters and speed']")).toBeNull();
  });
});
