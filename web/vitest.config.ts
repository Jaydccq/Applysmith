import { searchForWorkspaceRoot } from "vite";
import { defineConfig } from "vitest/config";

export default defineConfig({
  // The guides are read from docs, outside this folder.
  server: { fs: { allow: [searchForWorkspaceRoot(process.cwd()), "../docs"] } },
  test: {
    maxWorkers: 2,
    environment: "happy-dom",
    environmentOptions: { happyDOM: { url: "http://127.0.0.1:47319/" } },
    include: ["src/**/*.test.{ts,tsx}"],
    // A failing check fails fast through waitFor's own 3 s deadline; this cap
    // only bounds a whole test, and a long one (the palette walk with an axe
    // pass) outran 5 s on a loaded machine. A timed-out test keeps running in
    // the background and broke every later test in its file.
    testTimeout: 20_000,
    setupFiles: ["src/testing/setup.ts"],
    // Timestamps are shown on the viewer's clock; a fixed zone other than UTC
    // keeps the expectations the same on every machine and proves the shift.
    env: { TZ: "America/Los_Angeles" },
  },
});
