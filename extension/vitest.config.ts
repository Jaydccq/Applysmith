import { defineConfig } from "vitest/config";

export default defineConfig({
  define: { __BUILD_STAMP__: JSON.stringify("0000000000000000") },
  test: {
    maxWorkers: 2,
    environment: "happy-dom",
    environmentOptions: { happyDOM: { settings: { navigation: { disableChildFrameNavigation: true } } } },
    include: ["test/**/*.test.ts"],
  },
});
