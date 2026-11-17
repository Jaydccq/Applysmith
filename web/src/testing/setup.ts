import { afterEach, vi } from "vitest";
import { forgetReadsOnTheWay } from "../api/client";
import { forgetSession } from "../api/session";
import { forgetKept } from "../hooks";
import { expectAccessible } from "./axe";
import { unmount } from "./dom";

// `bun run test:a11y` runs axe over the page every test leaves behind, so
// each state a test reaches (errors, dialogs, empty lists) is audited too.
afterEach(async () => {
  if (process.env.APPLYSMITH_A11Y === "1" && typeof document !== "undefined" && document.body.children.length > 0) await expectAccessible();
  unmount();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  forgetSession();
  forgetReadsOnTheWay();
  forgetKept();
  // The browser panel's module reads the address when it loads, so a file
  // run without a DOM never loads it.
  if (typeof window !== "undefined") (await import("../pages/browser/Lanes")).resetPanel();
  if (typeof window !== "undefined") (window.localStorage.clear(), window.sessionStorage.clear());
  if (typeof window !== "undefined") window.history.replaceState(null, "", "/");
});
