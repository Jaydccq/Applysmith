import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok, type FakeApi } from "../testing/api";
import { button, byLabel, click, mount, q, qa, text, type, waitFor } from "../testing/dom";
import { scan, sources } from "../testing/fixtures_discovery";
import Discovery from "./Discovery";

const open = (search = "") => window.history.replaceState(null, "", "/discovery" + search);

function server(extra: Record<string, Parameters<FakeApi["on"]>[1]> = {}): FakeApi {
  return fakeApi({ "GET /api/scan-sources": () => ok({ sources }), ...extra });
}

describe("Discovery: History", () => {
  const many = Array.from({ length: 20 }, (_, i) => scan({ id: `scan-${i}`, source: i % 2 ? "github" : "registry" }));
  // Five rows whose age could not be read end in no other count.
  many[0] = scan({ id: "scan-0", counts: { ...scan().counts, discovered: 45, undated: 5 } });

  it("keeps source, status and date filters in the address and sends them to the server", async () => {
    open("?tab=history");
    const api = server({ "GET /api/scans": () => ok({ scans: many, page: 1, limit: 20, total: 20 }) });
    await mount(<Discovery />);
    await waitFor(() => qa("tbody tr").length === 20);
    const [sourceSelect, statusSelect] = qa<HTMLSelectElement>("form[aria-label='Scan history filters'] select");
    await type(sourceSelect, "github");
    await type(statusSelect, "blocked");
    await type(q<HTMLInputElement>('input[type="date"]'), "2026-10-01");
    expect(window.location.search).toBe("?tab=history&source=github&status=blocked&from=2026-10-01");
    await waitFor(() => api.callsTo("GET /api/scans").some((c) => c.query.get("source") === "github" && c.query.get("status") === "blocked" && c.query.get("from") === "2026-10-01"));
  });

  it("shows an error with retry", async () => {
    open("?tab=history");
    server({ "GET /api/scans": () => fail("internal", "boom") });
    await mount(<Discovery />);
    await waitFor(() => q('[role="alert"]'));
    expect(button("Try again")).not.toBeNull();
  });
});

describe("Discovery: Blocked companies", () => {
  const list = (companies: unknown[]) => ({ companies });
  const entry = { id: 1, name: "Staffing Example Inc", normalized: "staffing example", reason: "staffing firm", addedBy: "dashboard", addedAt: "2026-10-01T09:00:00.000Z" };

  it("blocks a company with a reason and shows it", async () => {
    open("?tab=blocked");
    let companies: unknown[] = [];
    const api = server({
      "GET /api/blocked-companies": () => ok(list(companies)),
      "POST /api/blocked-companies": (c) => {
        companies = [{ ...entry, name: (c.body as { name: string }).name }];
        return ok(entry);
      },
    });
    await mount(<Discovery />);
    await waitFor(() => text().includes("No company is blocked."));
    expect(button("Block company")!.disabled).toBe(true);
    await type(q<HTMLInputElement>('form[aria-label="Block a company"] input[type="text"]'), "Example Corp");
    await type(qa<HTMLInputElement>('form[aria-label="Block a company"] input[type="text"]')[1], "too many ghost jobs");
    await click(button("Block company"));
    expect(api.callsTo("POST /api/blocked-companies")[0].body).toEqual({ name: "Example Corp", reason: "too many ghost jobs" });
    await waitFor(() => text().includes("Example Corp"));
    // The button was disabled while the request ran; a keyboard user lands back in the first field.
    expect(document.activeElement).toBe(q('form[aria-label="Block a company"] input[type="text"]'));
  });

  it("asks before unblocking and removes the company by name", async () => {
    open("?tab=blocked");
    let companies = [entry];
    const api = server({
      "GET /api/blocked-companies": () => ok(list(companies)),
      "DELETE /api/blocked-companies": () => {
        companies = [];
        return ok(list([]));
      },
    });
    await mount(<Discovery />);
    await waitFor(() => byLabel(/^Unblock Staffing Example Inc$/));
    await click(byLabel(/^Unblock Staffing Example Inc$/));
    expect(api.callsTo("DELETE /api/blocked-companies")).toHaveLength(0);
    await click(qa("dialog button").find((b) => b.textContent === "Unblock")!);
    expect(api.callsTo("DELETE /api/blocked-companies")[0].query.get("name")).toBe("Staffing Example Inc");
    await waitFor(() => text().includes("No company is blocked."));
  });

  it("shows the server's refusal", async () => {
    open("?tab=blocked");
    server({ "GET /api/blocked-companies": () => ok(list([])), "POST /api/blocked-companies": () => fail("validation_failed", "\"!!\" names no employer", { details: { field: "name" } }) });
    await mount(<Discovery />);
    await waitFor(() => text().includes("No company is blocked."));
    await type(q<HTMLInputElement>('form[aria-label="Block a company"] input[type="text"]'), "!!");
    await click(button("Block company"));
    expect(q('[role="alert"]')?.textContent).toContain("names no employer");
  });
});

describe("Discovery: Sources and portals", () => {
  const doc = (over: Record<string, unknown> = {}) => ({ file: "portals", exists: true, template: false, content: "version: 1\ncompanies: []\n", revision: "r1", issues: [], ...over });

  it("saves the edit with the revision it loaded", async () => {
    open("?tab=sources&view=raw");
    const api = server({ "GET /api/config/portals": () => ok(doc()), "PUT /api/config/portals": () => ok({ revision: "r2", changed: true }) });
    await mount(<Discovery />);
    await waitFor(() => q("textarea"));
    expect(button("Save portals.yml")!.disabled).toBe(true);
    await type(q<HTMLTextAreaElement>("textarea"), "version: 1\nfilters:\n  skip_companies: [Example Corp]\n");
    await click(button("Save portals.yml"));
    expect(api.callsTo("PUT /api/config/portals")[0].body).toEqual({ content: "version: 1\nfilters:\n  skip_companies: [Example Corp]\n", expectedRevision: "r1" });
  });

  it("blocks an invalid file and names the line and key the server found", async () => {
    open("?tab=sources&view=raw");
    server({ "GET /api/config/portals": () => ok(doc()), "PUT /api/config/portals": () => fail("validation_failed", "portals.yml:2: unknown key \"unknown_key\"") });
    await mount(<Discovery />);
    await waitFor(() => q("textarea"));
    await type(q<HTMLTextAreaElement>("textarea"), "version: 1\nunknown_key: true\n");
    await click(button("Save portals.yml"));
    expect(q('[role="alert"]')?.textContent).toContain("portals.yml:2: unknown key \"unknown_key\"");
  });

  // A save quotes the revision it replaces (ConfigSave); a conflict never drops the typed text.
  it("keeps the typed text when the file changed underneath, and saves it over the new version only when asked", async () => {
    open("?tab=sources&view=raw");
    let loads = 0;
    const api = server({
      "GET /api/config/portals": () => ok(doc(++loads > 1 ? { content: "version: 1\ncompanies: [x]\n", revision: "r9" } : {})),
      "PUT /api/config/portals": () => fail("revision_conflict", "portals changed on disk; reload it and apply the change again"),
    });
    await mount(<Discovery />);
    await waitFor(() => q("textarea"));
    await type(q<HTMLTextAreaElement>("textarea"), "version: 1\n");
    await click(button("Save portals.yml"));
    await click(await waitFor(() => button("Keep my text")));
    await waitFor(() => !button("Keep my text"));
    expect(q<HTMLTextAreaElement>("textarea")!.value).toBe("version: 1\n");
    await click(button("Save portals.yml"));
    expect(api.callsTo("PUT /api/config/portals")[1].body).toEqual({ content: "version: 1\n", expectedRevision: "r9" });
  });
});
