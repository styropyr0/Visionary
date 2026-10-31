// Builds the ESM module, the minified <script> build and the type declarations.
import { build } from "esbuild";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";

// Replay is a split chunk in the ESM build and visionary-replay.min.js for the script tag
const common = { bundle: true, target: "es2019", legalComments: "none" };

await build({
  ...common,
  entryPoints: { index: "src/index.ts", replay: "src/replay/index.ts" },
  format: "esm",
  splitting: true,
  outdir: "dist",
  chunkNames: "chunk-[hash]",
  define: { __IIFE__: "false" },
});
await build({ ...common, entryPoints: ["src/index.ts"], format: "iife", globalName: "Visionary", minify: true, outfile: "dist/visionary.min.js", define: { __IIFE__: "true" } });
await build({ ...common, entryPoints: ["src/replay/index.ts"], format: "iife", globalName: "VisionaryReplay", minify: true, outfile: "dist/visionary-replay.min.js" });
execFileSync(process.execPath, ["node_modules/typescript/bin/tsc", "-p", "."], { stdio: "inherit" });

for (const [file, budget] of [["dist/visionary.min.js", 6], ["dist/visionary-replay.min.js", 8]]) {
  const size = gzipSync(readFileSync(file)).length;
  console.log(`${file}: ${(size / 1024).toFixed(2)} KB gzipped`);
  if (size > budget * 1024) {
    console.error(`over the ${budget} KB budget`);
    process.exit(1);
  }
}
