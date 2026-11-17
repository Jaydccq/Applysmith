import { describe, expect, it, vi } from "vitest";
import { sseResponse } from "../testing/api";
import { openRunStream, type RunStreamEvent } from "./runstream";

const progress = (id: string, line: string) => `id: ${id}\nevent: progress\ndata: ${JSON.stringify({ runId: "r1", line })}\n\n`;
const state = `event: state\ndata: ${JSON.stringify({ runId: "r1", kind: "scan", status: "running" })}\n\n`;
const final = (id: string) => `id: ${id}\nevent: final\ndata: ${JSON.stringify({ runId: "r1", status: "succeeded", scanId: "scan-1" })}\n\n`;

describe("openRunStream", () => {
  it("delivers progress lines in order and ends once on the final event", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => sseResponse([state, progress("e-1", "one"), progress("e-2", "two"), final("e-3")])));
    const seen: RunStreamEvent[] = [];
    const end = vi.fn();
    openRunStream("r1", (e) => seen.push(e), end);
    await vi.waitFor(() => expect(end).toHaveBeenCalledTimes(1));
    expect(seen.map((e) => (e.type === "progress" ? e.line : e.final.status))).toEqual(["one", "two", "succeeded"]);
    const last = seen.at(-1);
    expect(last?.type === "final" && last.final.scanId).toBe("scan-1");
  });

  it("attaches again with the id of the last event after the connection drops", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    const headers: Array<Record<string, string>> = [];
    const answers = [sseResponse([progress("e-1", "A"), progress("e-2", "B")], true), sseResponse([progress("e-3", "C"), final("e-4")])];
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
      headers.push(init.headers as Record<string, string>);
      return answers.shift()!;
    }));
    const lines: string[] = [];
    const end = vi.fn();
    openRunStream("r1", (e) => e.type === "progress" && lines.push(e.line), end);
    await vi.advanceTimersByTimeAsync(50);
    expect(headers).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(headers[1]["Last-Event-ID"]).toBe("e-2");
    expect(lines).toEqual(["A", "B", "C"]);
    expect(end).toHaveBeenCalledTimes(1);
  });

  it("ends without events when the run is unknown", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 404 })));
    const onEvent = vi.fn();
    const end = vi.fn();
    openRunStream("r1", onEvent, end);
    await vi.waitFor(() => expect(end).toHaveBeenCalledTimes(1));
    expect(onEvent).not.toHaveBeenCalled();
  });

  it("gives up after the first attempt and five retries fail, and ends once", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    const fetchMock = vi.fn(async () => {
      throw new Error("offline");
    });
    vi.stubGlobal("fetch", fetchMock);
    const end = vi.fn();
    openRunStream("r1", () => {}, end);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(end).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(6);
  });

  it("attaches no more once it is closed, even when the connection then drops", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    let drop = () => {};
    const fetchMock = vi.fn(async () => new Response(new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(new TextEncoder().encode(progress("e-1", "one")));
        drop = () => c.close();
      },
    })));
    vi.stubGlobal("fetch", fetchMock);
    const lines: string[] = [];
    const end = vi.fn();
    const stream = openRunStream("r1", (e) => e.type === "progress" && lines.push(e.line), end);
    await vi.advanceTimersByTimeAsync(50);
    expect(lines).toEqual(["one"]);
    stream.close();
    drop();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(end).not.toHaveBeenCalled();
  });
});
