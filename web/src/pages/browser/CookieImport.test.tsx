import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../../testing/api";
import { button, byLabel, click, mount, q, text, waitFor } from "../../testing/dom";
import Browser from "../Browser";

const sources = { supported: true, sources: [{ id: "chrome/Default", browserId: "chrome", browserLabel: "Google Chrome", profile: "Default" }] };
const sites = { sites: [{ host: "linkedin.com", cookies: 3 }, { host: "greenhouse.io", cookies: 1 }] };
const result = { imported: 3, failed: 0, replaced: 0, notInSource: 0, truncated: false };

describe("Import from your browser", () => {
  it("lists a browser's sites and imports the chosen ones", async () => {
    const api = fakeApi({
      "GET /api/browser/import/sources": () => ok(sources),
      "GET /api/browser/import/sources/chrome%2FDefault/sites": () => ok(sites),
      "GET /api/browser/imported": () => ok({ sites: [] }),
      "POST /api/browser/import": () => ok(result),
    });
    await mount(<Browser />);
    await waitFor(() => text().includes("linkedin.com"));
    expect(text()).toContain("3 cookies");

    await click(q('input[type="checkbox"]')); // linkedin.com, the first site
    await click(button(/^Import 1 site$/));
    await waitFor(() => text().includes("Brought in sign-ins for 3 cookies"));

    const calls = api.callsTo("POST /api/browser/import");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ sourceId: "chrome/Default", hosts: ["linkedin.com"] });
  });

  it("clears one brought-over site", async () => {
    const api = fakeApi({
      "GET /api/browser/import/sources": () => ok({ supported: true, sources: [] }),
      "GET /api/browser/imported": () => ok({ sites: [{ host: "linkedin.com", cookies: 2, at: "2027-03-10T09:00:00Z" }] }),
      "POST /api/browser/imported/forget": () => ok({ cleared: 2 }),
    });
    await mount(<Browser />);
    await waitFor(() => text().includes("brought over"));
    await click(byLabel(/Remove linkedin\.com/));
    await waitFor(() => api.callsTo("POST /api/browser/imported/forget").length === 1);
    expect(api.callsTo("POST /api/browser/imported/forget")[0].body).toEqual({ host: "linkedin.com" });
  });

  it("explains when importing is not supported", async () => {
    fakeApi({
      "GET /api/browser/import/sources": () => ok({ supported: false, sources: [], reason: "supported on macOS only" }),
      "GET /api/browser/imported": () => ok({ sites: [] }),
    });
    await mount(<Browser />);
    await waitFor(() => text().includes("supported on macOS only"));
  });
});
