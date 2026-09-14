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
  "deno-local.ts",
  "deno-remote.ts",
  "local-spawn.ts",
  "multimodal.ts",
  "one-shot-query.ts",
  "permissions.ts",
  "plan-resolution.ts",
  "quickstart.ts",
  "remote-connect.ts",
  "run-events.ts",
  "schedules.ts",
  "teams.ts",
] as const;
const publicImports = new Set([
  "@stacklok-oss/mecatl-sdk",
  "@stacklok-oss/mecatl-sdk/gen",
  "@stacklok-oss/mecatl-sdk/deno",
  "@stacklok-oss/mecatl-sdk/node",
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
      if (specifier.startsWith("@stacklok-oss/mecatl-sdk")) {
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

test("the Deno examples cover remote connect and Deno.Command-backed local spawn", () => {
  const remote = readFileSync(join(examplesRoot, "deno-remote.ts"), "utf8");
  expect(remote).toContain('from "@stacklok-oss/mecatl-sdk/deno"');
  expect(remote).not.toContain("@stacklok-oss/mecatl-sdk/node");
  expect(remote).toContain("connect(");

  const local = readFileSync(join(examplesRoot, "deno-local.ts"), "utf8");
  expect(local).toContain('from "@stacklok-oss/mecatl-sdk/deno"');
  expect(local).not.toContain("@stacklok-oss/mecatl-sdk/node");
  expect(local).toContain("spawn(");
});

test("the Deno gate runs the local Deno.Command lifecycle", () => {
  const taskfile = readFileSync(join(packageRoot, "Taskfile.yml"), "utf8");
  expect(taskfile).toContain("node scripts/run-deno-integration.mjs");

  const integration = readFileSync(join(packageRoot, "e2e", "deno.e2e.ts"), "utf8");
  expect(integration).toContain('from "@stacklok-oss/mecatl-sdk/deno"');
  expect(integration).toContain("await spawn(");
  expect(integration).toContain("await query(");
  expect(integration).toContain("await client.close()");
  expect(integration).toContain("runtimeDirectories()");
  expect(integration).toContain("HarnessService.method.converse");

  const launcher = readFileSync(join(sourceRoot, "deno-spawn.ts"), "utf8");
  expect(launcher).toContain("new runtime.Command(");
  expect(launcher).toContain('"--lifetime-stdin"');
  expect(launcher).toContain("process.closeLifetime()");
  expect(launcher).toContain("createNodeTransport(");
  expect(integration).toContain('client.daemon.transport === "grpc"');
  expect(integration).toContain("client.daemon.grpcAddress");
  expect(launcher).not.toMatch(/from\s+["']node:/u);
  const grpc = readFileSync(join(packageRoot, "e2e", "deno-grpc.e2e.ts"), "utf8");
  expect(grpc).toContain('event.kind === "turn.start"');
  expect(grpc).toContain("await run.cancel()");
  expect(grpc).toContain('event.payload.stop === "cancelled"');
  expect(grpc).toContain('"stream/watch abort"');
  expect(grpc).toContain("Promise.allSettled([drain(iterator), drain(watch)])");
  expect(grpc).toContain('session.once("close", resolve)');
  expect(grpc).toContain('"native HTTP/2 session close"');
  expect(grpc).toContain("nodeOptions: { ca }");
  expect(grpc).toContain('mode === "uds"');
  expect(grpc).toContain("? { socketPath }");
  const runner = readFileSync(join(packageRoot, "scripts", "run-deno-integration.mjs"), "utf8");
  expect(runner).toContain('["tcp", "tls", "uds"]');
  expect(runner).toMatch(/--allow-net=127\.0\.0\.1,unix:\$\{socketPath\}/u);
  expect(runner).toContain('runArguments(mode === "uds" ? join(directory, "g.sock") : undefined)');
  expect(runner).toContain("delay_ms: 30_000");
  expect(runner).toContain("timeout: 45_000");
});
