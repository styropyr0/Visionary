import { defineConfig } from "tsup";

// Bundles the tests (test/*.test.ts) with the sources they import, the same
// way as the package, for `node --test`.
export default defineConfig({
  entry: ["test/*.test.ts"],
  outDir: "test-dist",
  format: ["esm"],
  clean: true,
  target: "node18",
  platform: "node",
  loader: { ".sql": "text" },
  define: { __SDK_VERSION__: JSON.stringify("0.0.0-test") },
});
