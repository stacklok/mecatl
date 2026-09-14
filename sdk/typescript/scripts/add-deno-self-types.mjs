import { readdir, readFile, writeFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = fileURLToPath(new URL("../dist/", import.meta.url));

async function javascriptFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map((entry) => {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) return javascriptFiles(path);
      return entry.isFile() && entry.name.endsWith(".js") ? [path] : [];
    }),
  );
  return nested.flat();
}

async function prepare(path) {
  const declarationPath = path.replace(/\.js$/u, ".d.ts");
  const mapPath = `${path}.map`;
  const [source, declaration, rawMap] = await Promise.all([
    readFile(path, "utf8"),
    readFile(declarationPath, "utf8"),
    readFile(mapPath, "utf8"),
  ]);
  if (declaration.length === 0) {
    throw new Error(`empty declaration file for ${path}`);
  }

  const directive = `// @ts-self-types="./${basename(declarationPath)}"`;
  if (source.startsWith("// @ts-self-types=")) {
    throw new Error(`Deno self-types directive already exists in ${path}`);
  }

  const sourceMap = JSON.parse(rawMap);
  if (sourceMap.file !== basename(path) || typeof sourceMap.mappings !== "string") {
    throw new Error(`unexpected source map for ${path}`);
  }
  sourceMap.mappings = `;${sourceMap.mappings}`;

  return {
    mapPath,
    mapSource: JSON.stringify(sourceMap),
    path,
    source: `${directive}\n${source}`,
  };
}

const outputs = await Promise.all((await javascriptFiles(dist)).map(prepare));
await Promise.all(
  outputs.flatMap((output) => [
    writeFile(output.path, output.source),
    writeFile(output.mapPath, output.mapSource),
  ]),
);
