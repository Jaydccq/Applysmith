// "Next step" for an embedded application form: the progress control lives in a
// sub-frame, which only the worker can enumerate and address. It asks each
// frame to press its own control and stops at the first frame that did.
import { classifyFrame } from "../../../engine/src/frames";
import { ApiError } from "./api";
import { injectFiles, run } from "./fill";
import { on } from "./router";
import type { AdvanceResult } from "../widgets/nextstep";

const MAX_FRAMES = 30;

const advanceFn = () => (globalThis as unknown as { __applysmithAdvance?: () => unknown }).__applysmithAdvance?.() ?? null;

export function registerAdvance(): void {
  on("step.advance", async (_m, sender): Promise<AdvanceResult> => {
    const tabId = sender.tab?.id;
    if (tabId === undefined) throw new ApiError("bad_request", "That needs a page.");
    const frames = ((await chrome.webNavigation.getAllFrames({ tabId })) ?? []).filter((f) => f.frameId !== 0 && f.documentLifecycle !== "prerender" && !f.errorOccurred);
    for (const f of frames.slice(0, MAX_FRAMES)) {
      const kind = classifyFrame(f.url);
      if (kind === "denylist" || kind === "not-http") continue;
      if (!(await injectFiles(tabId, f.frameId, ["advance.js"]))) continue;
      const out = await run<AdvanceResult>(tabId, f.frameId, advanceFn);
      if (out?.advanced) return out;
    }
    return { advanced: false, label: null };
  });
}
