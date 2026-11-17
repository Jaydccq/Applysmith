import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Board } from "./api/board";
import { BoardContext, useResource } from "./hooks";
import { manualBoard } from "./testing/api";
import { byText, mount, text, waitFor, settle, unmount } from "./testing/dom";
import { get, post, readsInFlight } from "./api/client";
import { object, str } from "./api/decode";
import { act } from "react";
import { vi } from "vitest";

function Probe({ topic, counter, renders }: { topic: string; counter: { n: number }; renders?: { n: number } }) {
  if (renders) renders.n++;
  const [res] = useResource(async () => ++counter.n, [], [topic]);
  return <p>{res.status === "ready" ? `loads ${res.data}` : "loading"}</p>;
}

describe("useResource and the board", () => {
  it("refetches only the topic whose revision changed after reconnecting", async () => {
    const board = manualBoard();
    const tracker = { n: 0 };
    const runs = { n: 0 };
    await mount(
      <BoardContext.Provider value={board}>
        <Probe topic="tracker" counter={tracker} />
        <Probe topic="runs" counter={runs} />
      </BoardContext.Provider>,
    );
    await act(async () => board.set({ tracker: 38, runs: 5 }));
    await settle();
    await act(async () => board.set({ tracker: 41, runs: 5 }));
    await waitFor(() => tracker.n === 2);
    await settle();
    expect(runs.n).toBe(1);
    expect(byText("p", "loads 2")).not.toBeNull();
  });
});

describe("useResource and a restarted server", () => {
  it("refetches everything when the stream epoch changes, even if the revisions look the same", async () => {
    const board = manualBoard();
    const tracker = { n: 0 };
    const runs = { n: 0 };
    await mount(
      <BoardContext.Provider value={board}>
        <Probe topic="tracker" counter={tracker} />
        <Probe topic="runs" counter={runs} />
      </BoardContext.Provider>,
    );
    await act(async () => board.set({ tracker: 0, runs: 0 }, "aaaa"));
    await settle();
    expect([tracker.n, runs.n]).toEqual([1, 1]);
    await act(async () => board.set({ tracker: 0, runs: 0 }, "bbbb"));
    await waitFor(() => tracker.n === 2 && runs.n === 2);
  });
});

describe("useResource and a page opened again", () => {
  const value = object<{ v: string }>({ v: str });
  // One load, so every Card's load has the same code text.
  const load = () => get("/api/x", value);
  function Card({ keep = "test.card" }: { keep?: string }) {
    const [res] = useResource(load, [], ["tracker"], { keep });
    return <p>{res.status === "ready" ? res.data.v : res.status}</p>;
  }
  // The server answers each read when the test says what it holds; a write is
  // answered at once. A read left unanswered is answered after the test, so
  // the count of reads in flight starts at zero in the next.
  let reads: ((v: string) => Promise<void>)[] = [];
  beforeEach(() => {
    reads = [];
    const body = (data: unknown) => new Response(JSON.stringify({ ok: true, requestId: "r1", data }));
    vi.stubGlobal("fetch", (_: string, init: RequestInit) =>
      init.method === "GET" ? new Promise<Response>((ok) => reads.push(async (v) => act(async () => ok(body({ v }))))) : Promise.resolve(body({ v: "saved" })),
    );
  });
  afterEach(async () => {
    while (reads.length > 0) await reads.shift()!("left");
  });
  async function visit(board: Board, holds: string, then = () => {}) {
    await mount(
      <BoardContext.Provider value={board}>
        <Card />
      </BoardContext.Provider>,
    );
    await reads.shift()!(holds);
    await waitFor(() => text() === holds);
    await act(async () => then());
    unmount();
  }
  // Settle reveals a page once no read is in flight (readsInFlight), so a
  // read it counts is one the page waits for.
  async function revisit(board: Board, keep?: string) {
    await mount(
      <BoardContext.Provider value={board}>
        <Card keep={keep} />
      </BoardContext.Provider>,
    );
    return { shows: text(), waitsFor: readsInFlight() };
  }

  it("draws what it showed at once and asks again without holding the page", async () => {
    // The first snapshot arrives after the first answer, as on opening the
    // app: that answer is current as of it.
    const board = manualBoard();
    await visit(board, "first", () => board.set({ tracker: 1 }));
    expect(await revisit(board)).toEqual({ shows: "first", waitsFor: 0 });
    expect(reads.length).toBe(1);
    await reads.shift()!("second");
    await waitFor(() => text() === "second");
  });

  it("after a write asks again and waits, never drawing the old answer", async () => {
    const board = manualBoard();
    board.set({ tracker: 1 });
    await visit(board, "first");
    await post("/api/x", value, {});
    expect(await revisit(board)).toEqual({ shows: "loading", waitsFor: 1 });
    await reads.shift()!("second");
    await waitFor(() => text() === "second");
  });

  it("after the stream is back from another server run asks again and waits", async () => {
    const board = manualBoard();
    board.set({ tracker: 1 }, "aaaa");
    await visit(board, "first");
    await act(async () => {
      board.set({ tracker: 1 }, "bbbb");
      board.reconnect();
    });
    expect(await revisit(board)).toEqual({ shows: "loading", waitsFor: 1 });
    await reads.shift()!("second");
    await waitFor(() => text() === "second");
  });

  it("never draws what another name kept, even for the same code and inputs", async () => {
    const board = manualBoard();
    board.set({ tracker: 1 });
    await visit(board, "first");
    expect(await revisit(board, "test.other")).toEqual({ shows: "loading", waitsFor: 1 });
  });

  // A name stands for one call site: two sharing one would draw each other's
  // answers.
  it("gives each kept name to one call site", () => {
    const sources = import.meta.glob(["./**/*.{ts,tsx}", "!./**/*.test.{ts,tsx}"], { query: "?raw", import: "default", eager: true }) as Record<string, string>;
    const names = Object.values(sources).flatMap((src) => [...src.matchAll(/keep: "([^"]+)"/g)].map((m) => m[1]));
    expect(names.length).toBeGreaterThan(0);
    expect(names.filter((n, i) => names.indexOf(n) !== i)).toEqual([]);
  });
});
