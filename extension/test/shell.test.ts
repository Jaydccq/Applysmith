import { beforeEach, describe, expect, it, vi } from "vitest";
import { createShell, type ShellOptions } from "../src/panel/shell";
import type { UiState } from "../src/shared/messages";

function options(over: Partial<ShellOptions> & { saved?: UiState } = {}) {
  const set = vi.fn();
  const mount = vi.fn((el: HTMLElement) => void (el.textContent = "tab body"));
  const opts: ShellOptions = {
    doc: document,
    win: window,
    off: false,
    tabs: [
      { id: "analyze", label: "Analyze", mount },
      { id: "autofill", label: "Autofill", mount: vi.fn() },
    ],
    ui: { get: async () => over.saved ?? {}, set },
    ...over,
  };
  return { opts, set, mount };
}

beforeEach(() => {
  document.body.innerHTML = '<input id="page-input">';
  Object.defineProperty(window, "innerWidth", { value: 1200, configurable: true });
});

describe("panel shell", () => {
  it("lives in a closed shadow root the page cannot reach", async () => {
    const { opts } = options();
    const shell = await createShell(opts);
    const host = document.querySelector("[data-applysmith]")!;
    expect(host.shadowRoot).toBeNull();
    expect(shell.root.querySelector(".panel")).not.toBeNull();
    expect(document.querySelector(".panel")).toBeNull();
  });
});
