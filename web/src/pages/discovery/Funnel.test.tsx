import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../../testing/api";
import { mount, qa, text, waitFor } from "../../testing/dom";
import type { FunnelWeek, SourceFunnel } from "../../wire";
import { FunnelPanel, WeekFunnel } from "./Funnel";

const lane = (over: Partial<SourceFunnel["total"]>) => ({ lane: "x", discovered: 0, inserted: 0, evaluated: 0, failed: 0, gateRejected: 0, high: 0, applied: 0, responded: 0, interview: 0, ...over });

describe("Discovery: source funnel", () => {
  it("lists each source with its stages and a total row, with shares of the stage before", async () => {
    const api = fakeApi({
      "GET /api/discovery/funnel": () =>
        ok({
          days: 30,
          lanes: [lane({ lane: "indeed", discovered: 65, inserted: 13, evaluated: 20, failed: 1, gateRejected: 2, high: 5, applied: 4, responded: 2, interview: 1 }), lane({ lane: "manual", evaluated: 1 })],
          total: lane({ lane: "total", discovered: 65, evaluated: 21, failed: 1, gateRejected: 2, high: 5, applied: 4, responded: 2, interview: 1 }),
        } satisfies SourceFunnel),
    });
    await mount(<FunnelPanel />);
    await waitFor(() => text().includes("Source funnel"));
    expect(api.callsTo("GET /api/discovery/funnel")[0].query.get("days")).toBe("30");
    const rows = qa("tbody tr").map((r) => r.textContent ?? "");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toContain("indeed");
    expect(rows[0]).toContain("13 (20%)"); // new of fetched
    expect(rows[0]).toContain("5 (25%)"); // strong of evaluated
    expect(rows[0]).toContain("4 (20%)"); // applied of evaluated
    expect(rows[0]).toContain("2 (50%)"); // answered of applied
    expect(rows[0]).toContain("3"); // failed plus gated
    expect(rows[2]).toContain("total");
  });
});

const week = (over: Partial<FunnelWeek["current"]>) => ({ found: 0, evaluated: 0, applied: 0, responded: 0, interview: 0, offer: 0, ...over });

describe("Weekly funnel", () => {
  it("shows each stage, its change on last week, its share of the stage before and the list it opens", async () => {
    const api = fakeApi({ "GET /api/discovery/funnel/week": () => ok({ since: "2026-10-16", current: week({}), previous: week({}) } satisfies FunnelWeek) });
    await mount(<WeekFunnel />);
    await waitFor(() => text().includes("Nothing moved in these 7 days or the 7 before."));
    api.on("GET /api/discovery/funnel/week", () => ok({ since: "2026-10-16", current: week({ found: 40, evaluated: 10, applied: 4, responded: 1, interview: 2 }), previous: week({ found: 30, evaluated: 10, applied: 6 }) }));
    await mount(<WeekFunnel />);
    const tiles = await waitFor(() => (qa(".tiles a").length === 6 ? qa<HTMLAnchorElement>(".tiles a") : null));
    // More interviews than replies in one week is no conversion rate.
    expect(tiles.map((t) => t.textContent)).toEqual(["Found40+10 vs last week", "Evaluated10same as last week25% of found", "Applied4\u22122 vs last week40% of evaluated", "Responded1+1 vs last week25% of applied", "Interview2+2 vs last week", "Offer0same as last week0% of interview"]);
    expect(tiles[2].getAttribute("href")).toBe("/tracker?reached=applied&since=2026-10-16");
  });
});
