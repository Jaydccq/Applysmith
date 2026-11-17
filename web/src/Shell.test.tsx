import { describe, expect, it, vi } from "vitest";
import { PageBoundary, Shell } from "./Shell";
import { fakeApi, ok } from "./testing/api";
import { button, byLabel, byText, click, mount, press, q, qa, type, waitFor } from "./testing/dom";
import { applyList, cost, page, reportPage, routing, summary } from "./testing/fixtures";
import { sources } from "./testing/fixtures_discovery";
import { BoardContext } from "./hooks";
import type { Board, BoardState } from "./api/board";

function server() {
  return fakeApi({
    "GET /api/apply/list": () => ok(applyList),
    "GET /api/tracker/summary": () => ok(summary),
    "GET /api/candidacies": () => ok(page([])),
    "GET /api/reports": () => ok(reportPage),
    "GET /api/llm/routing": () => ok(routing),
    "GET /api/llm/cost": () => ok(cost),
    "GET /api/health": () => ok({ status: "ok", apiVersion: 1, version: "dev", buildId: "b", workspaceId: "w", startedAt: "2026-10-01T09:00:00.000Z" }),
  });
}

describe("Shell", () => {
  it("navigates by clicking a link without a page load and moves focus to the content, named for the page a screen reader then says", async () => {
    server();
    await mount(<Shell />);
    await click(byText("nav a", "Reports"));
    await waitFor(() => q("h1")?.textContent === "Reports");
    expect(window.location.pathname).toBe("/reports");
    expect(document.activeElement).toBe(q("main"));
    expect(q("main")?.getAttribute("aria-label")).toBe("Reports");
  });
});

describe("Shell: command registry", () => {
  const where = () => window.location.pathname + window.location.search;

  it("starts a scan and shows the notices from the palette: the launcher's first source that can start takes the keyboard, the press stays the user's", async () => {
    window.history.replaceState(null, "", "/");
    const api = server();
    api.on("GET /api/scan-sources", () => ok({ sources: [sources[1], sources[0]] }));
    api.on("GET /api/scans", () => ok({ scans: [], page: 1, limit: 20, total: 0 }));
    api.on("GET /api/notices", () => ok({ notices: [{ id: 2, title: "Goal check-in", body: "Plan the week", link: "/ask#goals", createdAt: "2026-10-20T06:30:00.000Z", read: false }], unread: 1 }));
    await mount(<Shell />);
    const choose = async (what: string) => {
      await press("k", { metaKey: true });
      await type(await waitFor(() => q<HTMLInputElement>("dialog input[role=combobox]")), what);
      await click(await waitFor(() => byText("[role=option]", new RegExp(`^${what}`))));
    };
    await choose("Start a scan");
    await waitFor(() => document.activeElement?.getAttribute("aria-label") === "Start registry");
    expect(where()).toBe("/discovery");
    expect(api.callsTo("POST /api/scans")).toHaveLength(0);
    await choose("Show notices");
    const drawer = await waitFor(() => q("dialog.drawer"));
    expect(drawer.querySelector("h2")?.textContent).toBe("Notices");
    expect(drawer.textContent).toContain("Goal check-in");
  });
});

describe("Shell: notices", () => {
  const held = { id: 2, title: "Goal check-in", body: "Plan the week", link: "/ask#goals", createdAt: "2026-10-20T06:30:00.000Z", read: false };
  const paired = { id: 1, title: "Telegram paired", body: "This chat can now talk to the app.", link: "/settings", createdAt: "2026-10-19T20:00:00.000Z", read: false };

  it("opens a notice's page, marking it read, and marks all read at once", async () => {
    window.history.replaceState(null, "", "/");
    const api = server();
    api.on("GET /api/notices", () => ok({ notices: [held, paired], unread: 2 }));
    api.on("POST /api/notices/read", () => ok({ notices: [], unread: 0 }));
    await mount(<Shell />);
    await click(await waitFor(() => byLabel(/^Notices: 2 unread notices$/)));
    await click(await waitFor(() => button("Mark all as read")));
    expect(api.callsTo("POST /api/notices/read")[0].body).toEqual({});
    await click(byText<HTMLAnchorElement>("dialog a", "Open"));
    expect(api.callsTo("POST /api/notices/read")[1].body).toEqual({ ids: [2] });
    expect(window.location.pathname).toBe("/ask");
    await waitFor(() => !q("dialog.drawer"));
  });
});

describe("Shell: a page that fails", () => {
  it("keeps the fault to the page, says so in words and draws it again on Try again", async () => {
    let broken = true;
    const Flaky = () => {
      if (broken) throw new Error("the list could not be drawn");
      return <h1>Drawn</h1>;
    };
    const quiet = vi.spyOn(console, "error").mockImplementation(() => {});
    await mount(
      <PageBoundary>
        <Flaky />
      </PageBoundary>,
    );
    expect(q('[role="alert"]')?.textContent).toBe("This page could not be shown.the list could not be drawnTry again Reload the app");
    broken = false;
    await click(button("Try again"));
    expect(q("h1")?.textContent).toBe("Drawn");
    quiet.mockRestore();
  });
});

describe("Shell: a server that is not answering", () => {
  it("says so once, with what to do, and reconnects at once on Try now", async () => {
    server();
    const state: BoardState = { revisions: null, epoch: null, reconnecting: true, stale: true, links: 0 };
    const retryNow = vi.fn();
    const board: Board = { getState: () => state, subscribe: () => () => {}, start() {}, stop() {}, retryNow };
    await mount(
      <BoardContext.Provider value={board}>
        <Shell />
      </BoardContext.Provider>,
    );
    expect(qa(".banner[role=status]")).toHaveLength(1);
    expect(q(".banner")?.textContent).toContain("What you see may be out of date. If it does not come back, start the Applysmith app or run applysmith serve.");
    await click(button("Try now"));
    expect(retryNow).toHaveBeenCalledTimes(1);
  });
});
