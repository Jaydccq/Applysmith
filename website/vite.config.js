import { defineConfig } from "vite";

// The brand artwork is shared with the dashboard, one folder up.
export default defineConfig({
  server: { fs: { allow: [".."] } },
  build: { target: "es2022" },
});
