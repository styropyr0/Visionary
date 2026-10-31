import { defineConfig } from "tsup";

// Bundles the tests with the sources they import, for `node --test`.
export default defineConfig({
  entry: ["test/*.test.ts"],
  outDir: "test-dist",
  format: ["esm"],
  clean: true,
  target: "node18",
  platform: "node",
  define: { __REPORTER_VERSION__: JSON.stringify("0.0.0-test") },
});
