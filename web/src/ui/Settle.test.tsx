import { act, useEffect, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { get } from "../api/client";
import { fakeApi, ok } from "../testing/api";
import { q } from "../testing/dom";
import { QUIET_MS, SETTLE_MAX_MS, Settle } from "./Settle";

const settled = () => q(".settle")?.getAttribute("data-settled");

// The timers are faked, and the shared mount helper waits on one, so the page
// is rendered here.
let shown: { root: Root; host: HTMLElement } | null = null;
async function mount(ui: ReactNode) {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  shown = { root, host };
  await act(async () => root.render(ui));
}

afterEach(() => {
  vi.useRealTimers();
  if (shown) {
    act(() => shown!.root.unmount());
    shown.host.remove();
    shown = null;
  }
});

const anything = (v: unknown) => v;

function Reader({ path }: { path: string }) {
  useEffect(() => void get(path, anything).catch(() => {}), [path]);
  return <p>page</p>;
}

describe("Settle", () => {
  it("keeps a page hidden, busy, until the reads it started have arrived and nothing follows", async () => {
    const api = fakeApi();
    let answer!: () => void;
    api.on("GET /api/a", () => new Promise((r) => (answer = () => r(ok({})))));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await mount(
      <Settle>
        <Reader path="/api/a" />
      </Settle>,
    );
    expect(settled()).toBe("false");
    expect(q(".settle")?.getAttribute("aria-busy")).toBe("true");
    await act(async () => void vi.advanceTimersByTime(SETTLE_MAX_MS - 100));
    expect(settled()).toBe("false");
    await act(async () => answer());
    // The answer starts the quiet period; the page opens when it has passed.
    expect(settled()).toBe("false");
    await act(async () => void vi.advanceTimersByTime(QUIET_MS + 1));
    expect(settled()).toBe("true");
    expect(q(".settle")?.getAttribute("aria-busy")).toBe("false");
  });

  it("opens at the cap when a read does not come back, so a slow server shows its skeletons", async () => {
    const api = fakeApi();
    let answer!: () => void;
    api.on("GET /api/slow", () => new Promise((r) => (answer = () => r(ok({})))));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await mount(
      <Settle>
        <Reader path="/api/slow" />
      </Settle>,
    );
    await act(async () => void vi.advanceTimersByTime(SETTLE_MAX_MS + 1));
    expect(settled()).toBe("true");
    // The count is shared by the whole page, so a read left open would hold the next test.
    await act(async () => answer());
  });
});
