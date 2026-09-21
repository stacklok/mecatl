// SPDX-License-Identifier: Apache-2.0

/**
 * Bundles the BFF into one ESM file. Real dependencies (everything declared in
 * package.json) stay external and are resolved from the pruned node_modules the
 * image ships; workspace packages (`@mecatl-studio/*`) are INLINED, because
 * their package exports point at TypeScript sources that Node will not
 * type-strip from inside node_modules.
 */
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const packageJsonUrl = new URL("../package.json", import.meta.url);
const manifest = JSON.parse(await readFile(packageJsonUrl, "utf8")) as {
  dependencies?: Record<string, string>;
};
const external = Object.keys(manifest.dependencies ?? {}).filter(
  (name) => !name.startsWith("@mecatl-studio/"),
);

await build({
  bundle: true,
  entryPoints: [fileURLToPath(new URL("../src/index.ts", import.meta.url))],
  external,
  format: "esm",
  outfile: fileURLToPath(new URL("../dist/index.js", import.meta.url)),
  platform: "node",
  sourcemap: true,
  target: "node24",
});
console.log(`bundled server; external: ${external.join(", ")}`);
