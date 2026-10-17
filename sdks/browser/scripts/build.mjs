// Builds the ESM module, the minified <script> build and the type declarations.
import { build } from "esbuild";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";

const common = { entryPoints: ["src/index.ts"], bundle: true, target: "es2019", legalComments: "none" };

await build({ ...common, format: "esm", outfile: "dist/index.js" });
await build({ ...common, format: "iife", globalName: "Visionary", minify: true, outfile: "dist/visionary.min.js" });
execFileSync(process.execPath, ["node_modules/typescript/bin/tsc", "-p", "."], { stdio: "inherit" });

const size = gzipSync(readFileSync("dist/visionary.min.js")).length;
console.log(`dist/visionary.min.js: ${(size / 1024).toFixed(2)} KB gzipped`);
if (size > 6 * 1024) {
  console.error("over the 6 KB budget");
  process.exit(1);
}
