import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { registerAdvance } from "../src/sw/advance";
import { dispatch } from "../src/sw/router";
import { installChrome } from "./chrome-mock";

const TAB = { id: "ext-id", frameId: 0, tab: { id: 31, url: "https://example.com/careers/apply" } as chrome.tabs.Tab, url: "https://example.com/careers/apply" } as chrome.runtime.MessageSender;

type Frame = { frameId: number; url: string; documentLifecycle?: string; errorOccurred?: boolean };

/** Frames by id, and what each frame answers when asked to press. */
function setup(frames: Frame[], answers: Record<number, { advanced: boolean; label: string | null }>) {
  const injected: number[] = [];
  const asked: number[] = [];
  const { chrome } = installChrome({
    webNavigation: { getAllFrames: vi.fn(async () => frames) },
    scripting: {
      executeScript: vi.fn(async (inj: { target: { frameIds: number[] }; files?: string[] }) => {
        const id = inj.target.frameIds[0]!;
        if (inj.files) {
          injected.push(id);
          return [{ result: undefined }];
        }
        asked.push(id);
        return [{ result: answers[id] ?? null }];
      }),
    },
  });
  registerAdvance();
  return { chrome, injected, asked };
}

const advance = () => dispatch({ kind: "step.advance" }, TAB) as Promise<{ ok: boolean; data: { advanced: boolean; label: string | null }; error?: unknown }>;
afterEach(() => vi.unstubAllGlobals());
beforeEach(() => vi.clearAllMocks());

describe("Next step: the worker's fan-out", () => {
  it("asks each sub-frame in turn and stops at the first that pressed, never the top frame", async () => {
    const s = setup(
      [
        { frameId: 0, url: "https://example.com/careers/apply" },
        { frameId: 4, url: "https://jobs.jobvite.com/example/apply" },
        { frameId: 5, url: "https://boards.example.org/embed/apply" },
        { frameId: 6, url: "https://other.example.net/form" },
      ],
      { 4: { advanced: false, label: null }, 5: { advanced: true, label: "Next" }, 6: { advanced: true, label: "Next" } },
    );
    const r = await advance();
    expect(r).toMatchObject({ ok: true, data: { advanced: true, label: "Next" } });
    expect(s.asked).toEqual([4, 5]);
    expect(s.injected).toEqual([4, 5]);
  });

  it("skips deny-listed, non-http, failed and prerendered frames", async () => {
    const s = setup(
      [
        { frameId: 0, url: "https://example.com/" },
        { frameId: 1, url: "https://newassets.hcaptcha.com/captcha/v1" },
        { frameId: 2, url: "chrome-extension://abc/popup.html" },
        { frameId: 3, url: "https://jobs.jobvite.com/x", errorOccurred: true },
        { frameId: 4, url: "https://jobs.jobvite.com/y", documentLifecycle: "prerender" },
      ],
      {},
    );
    expect((await advance()).data).toEqual({ advanced: false, label: null });
    expect(s.asked).toEqual([]);
  });
});
