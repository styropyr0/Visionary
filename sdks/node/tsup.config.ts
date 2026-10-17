import { readFileSync } from "node:fs";
import { defineConfig } from "tsup";

const pkg = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")) as { version: string };

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  sourcemap: true,
  clean: true,
  target: "node18",
  platform: "node",
  // import.meta.url in the CommonJS build, used to find optional drivers.
  shims: true,
  // The schema files are embedded as text; sdks/node/schema stays a
  // byte-identical copy of sdks/go/schema.
  loader: { ".sql": "text" },
  define: { __SDK_VERSION__: JSON.stringify(pkg.version) },
});
