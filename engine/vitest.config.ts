// Clean-room: docs/provenance.md#CR-ENGINE
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    maxWorkers: 2,
    environment: "happy-dom",
    include: ["test/**/*.test.ts"],
    // A cap only: a test that waits on a condition has to outlast a loaded machine.
    testTimeout: 30_000,
  },
});
