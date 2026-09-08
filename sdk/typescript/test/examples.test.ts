import { spawnSync } from "node:child_process";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

import { expect, test } from "vitest";

const packageRoot = fileURLToPath(new URL("../", import.meta.url));
const repositoryRoot = fileURLToPath(new URL("../../../", import.meta.url));
const examplesRoot = join(packageRoot, "examples");
const sourceRoot = join(packageRoot, "src");
const conciseExamples = [
  "browser-bff.ts",
  "callback-tool.ts",
  "durable-attachment.ts",
  "local-spawn.ts",
  "one-shot-query.ts",
  "permissions.ts",
  "plan-resolution.ts",
  "remote-connect.ts",
  "schedules.ts",
  "teams.ts",
] as const;
const publicImports = new Set([
  "@stacklok/mecatl-sdk",
  "@stacklok/mecatl-sdk/gen",
  "@stacklok/mecatl-sdk/node",
]);

function compileExamples(): void {
  const build = spawnSync("pnpm", ["run", "build"], {
    cwd: packageRoot,
    encoding: "utf8",
    env: { ...process.env, NO_COLOR: "1" },
  });
  if (build.status !== 0) {
    throw new Error([build.stdout, build.stderr].filter(Boolean).join("\n"));
  }

  const compile = spawnSync(
    process.execPath,
    [join(packageRoot, "node_modules", "typescript", "bin", "tsc"), "-p", "examples/tsconfig.json"],
    {
      cwd: packageRoot,
      encoding: "utf8",
      env: { ...process.env, NO_COLOR: "1" },
    },
  );
  if (compile.status !== 0) {
    throw new Error([compile.stdout, compile.stderr].filter(Boolean).join("\n"));
  }
}

function typescriptFiles(root: string): string[] {
  return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const path = join(root, entry.name);
    if (entry.isDirectory()) {
      return entry.name === "node_modules" ? [] : typescriptFiles(path);
    }
    return entry.name.endsWith(".ts") ? [path] : [];
  });
}

function importSpecifiers(source: string): string[] {
  return [...source.matchAll(/\b(?:from\s+|import\s*(?:\(\s*)?)["']([^"']+)["']/gu)].flatMap(
    (match) => (match[1] === undefined ? [] : [match[1]]),
  );
}

test("every published example typechecks against package exports", () => {
  expect(
    readdirSync(examplesRoot)
      .filter((name) => name.endsWith(".ts"))
      .sort(),
  ).toEqual([...conciseExamples].sort());

  compileExamples();

  for (const file of typescriptFiles(examplesRoot)) {
    const source = readFileSync(file, "utf8");
    for (const specifier of importSpecifiers(source)) {
      if (specifier.startsWith(".")) {
        const target = resolve(dirname(file), specifier);
        expect(
          target === sourceRoot || target.startsWith(`${sourceRoot}${sep}`),
          `${file} uses the SDK source tree as a backdoor`,
        ).toBe(false);
      }
      if (specifier.startsWith("@stacklok/mecatl-sdk")) {
        expect(publicImports, `${file} imports a private package path`).toContain(specifier);
      }
    }
  }

  const config = readFileSync(join(examplesRoot, "tsconfig.json"), "utf8");
  expect(config).not.toMatch(/"(?:baseUrl|paths|rootDirs)"/u);

  // slack-bot is a separate pnpm project: the SDK-local compiler cannot cover it.
  const workflow = readFileSync(join(repositoryRoot, ".github", "workflows", "ci.yml"), "utf8");
  expect(workflow).toContain("slack-bot-example:");
  expect(workflow).toContain("run: task slack-bot:typecheck");
});

test("the Node and Bun examples cover connect spawn query and callback tools", () => {
  const sources = new Map(
    conciseExamples.map((name) => [name, readFileSync(join(examplesRoot, name), "utf8")]),
  );
  expect(sources.get("remote-connect.ts")).toContain("connect(");
  expect(sources.get("local-spawn.ts")).toContain("spawn(");
  expect(sources.get("one-shot-query.ts")).toContain("query(");
  expect(sources.get("callback-tool.ts")).toContain("client.tool(");

  const inventory = readFileSync(join(examplesRoot, "README.md"), "utf8");
  expect(inventory).toContain("Node.js 22+ and Bun");
  expect(inventory).toContain("guidance, not shipped BFF server code");
});
