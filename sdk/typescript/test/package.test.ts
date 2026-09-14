import { execFileSync, spawnSync } from "node:child_process";
import {
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { builtinModules } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, normalize, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gunzipSync } from "node:zlib";
import { afterAll, beforeAll, expect, test } from "vitest";

type PackageJson = {
  bugs: { url: string };
  dependencies: Record<string, string>;
  dependencyLicenses: Record<string, string>;
  engines: { deno: string; node: string };
  exports: Record<"." | "./deno" | "./gen" | "./node", { import: string; types: string }>;
  homepage: string;
  license: string;
  name: string;
  packageManager: string;
  publishConfig: { access?: string; registry: string };
  repository: { directory: string; type: string; url: string };
  type: string;
  version: string;
};

const packageRoot = fileURLToPath(new URL("../", import.meta.url));
const packageJson = JSON.parse(
  readFileSync(join(packageRoot, "package.json"), "utf8"),
) as PackageJson;
const packageVersion = readFileSync(join(packageRoot, "VERSION"), "utf8").trim();

let fixtureRoot: string;
let packedFiles: Map<string, Buffer>;
let consumerRoot: string;

function readTarString(block: Buffer, offset: number, length: number): string {
  const field = block.subarray(offset, offset + length);
  const end = field.indexOf(0);
  return field.subarray(0, end === -1 ? field.length : end).toString("utf8");
}

function readPackedFiles(archivePath: string): Map<string, Buffer> {
  const archive = gunzipSync(readFileSync(archivePath));
  const files = new Map<string, Buffer>();

  for (let offset = 0; offset + 512 <= archive.length; ) {
    const header = archive.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) {
      break;
    }

    const name = readTarString(header, 0, 100);
    const prefix = readTarString(header, 345, 155);
    const path = prefix === "" ? name : `${prefix}/${name}`;
    const sizeText = readTarString(header, 124, 12).trim();
    const size = sizeText === "" ? 0 : Number.parseInt(sizeText, 8);
    const type = String.fromCharCode(header[156] ?? 0);
    offset += 512;

    if (type === "0" || type === "\0") {
      files.set(path, Buffer.from(archive.subarray(offset, offset + size)));
    }
    offset += Math.ceil(size / 512) * 512;
  }

  return files;
}

function entrypointGraph(entrypoint: string): ReadonlySet<string> {
  const pending = [entrypoint];
  const visited = new Set<string>();
  while (pending.length > 0) {
    const current = pending.pop();
    if (current === undefined || visited.has(current)) continue;
    visited.add(current);
    const source = packedFiles.get(current)?.toString("utf8");
    expect(source, `packed browser module ${current}`).toBeDefined();
    for (const match of source?.matchAll(/\b(?:from|import)\s*(?:\(\s*)?["']([^"']+)["']/gu) ??
      []) {
      const specifier = match[1];
      if (specifier?.startsWith(".") !== true) continue;
      pending.push(normalize(join(dirname(current), specifier)).replaceAll("\\", "/"));
    }
  }
  return visited;
}

beforeAll(() => {
  fixtureRoot = mkdtempSync(join(tmpdir(), "mecatl-sdk-package-"));
  const suppliedArchive = process.env.MECATL_SDK_PACKED_TARBALL;
  let archivePath: string;
  if (suppliedArchive === undefined) {
    execFileSync("pnpm", ["pack", "--pack-destination", fixtureRoot], {
      cwd: packageRoot,
      env: { ...process.env, NO_COLOR: "1" },
      stdio: "pipe",
    });

    const [archive, ...extraArchives] = readdirSync(fixtureRoot).filter((name) =>
      name.endsWith(".tgz"),
    );
    expect(archive).toBeDefined();
    expect(extraArchives).toHaveLength(0);
    if (archive === undefined) {
      throw new Error("pnpm pack did not create an archive");
    }
    archivePath = join(fixtureRoot, archive);
  } else {
    archivePath = resolve(packageRoot, suppliedArchive);
  }
  packedFiles = readPackedFiles(archivePath);

  consumerRoot = join(fixtureRoot, "consumer");
  const installedRoot = join(consumerRoot, "node_modules", "@stacklok-oss", "mecatl-sdk");
  for (const [path, content] of packedFiles) {
    if (!path.startsWith("package/")) {
      continue;
    }
    const destination = join(installedRoot, path.slice("package/".length));
    mkdirSync(dirname(destination), { recursive: true });
    writeFileSync(destination, content);
  }
  for (const dependency of Object.keys(packageJson.dependencies)) {
    const dependencyPath = dependency.split("/");
    const destination = join(consumerRoot, "node_modules", ...dependencyPath);
    mkdirSync(dirname(destination), { recursive: true });
    symlinkSync(join(packageRoot, "node_modules", ...dependencyPath), destination, "junction");
  }
}, 60_000);

afterAll(() => {
  rmSync(fixtureRoot, { force: true, recursive: true });
});

test("exports map exposes exactly ., ./node, ./deno, ./gen", () => {
  expect(Object.keys(packageJson.exports)).toEqual([".", "./node", "./deno", "./gen"]);
  expect(packageJson.type).toBe("module");

  for (const target of Object.values(packageJson.exports)) {
    expect(Object.keys(target).sort()).toEqual(["import", "types"]);
    expect(target.import).toMatch(/^\.\/dist\/.*\.js$/);
    expect(target.types).toMatch(/^\.\/dist\/.*\.d\.ts$/);
    expect(packedFiles.has(`package/${target.import.slice(2)}`)).toBe(true);
    expect(packedFiles.has(`package/${target.types.slice(2)}`)).toBe(true);
  }

  for (const subpath of ["", "/node", "/deno", "/gen"]) {
    execFileSync(
      process.execPath,
      ["--input-type=module", "--eval", `await import("@stacklok-oss/mecatl-sdk${subpath}");`],
      { cwd: consumerRoot, stdio: "pipe" },
    );
  }

  const requireResult = spawnSync(
    process.execPath,
    ["--input-type=commonjs", "--eval", 'require("@stacklok-oss/mecatl-sdk")'],
    { cwd: consumerRoot, encoding: "utf8" },
  );
  expect(requireResult.status).not.toBe(0);
  expect(requireResult.stderr).toMatch(/ERR_PACKAGE_PATH_NOT_EXPORTED|ERR_REQUIRE_ESM/);

  const privateImport = spawnSync(
    process.execPath,
    ["--input-type=module", "--eval", 'await import("@stacklok-oss/mecatl-sdk/private");'],
    { cwd: consumerRoot, encoding: "utf8" },
  );
  expect(privateImport.status).not.toBe(0);
  expect(privateImport.stderr).toContain("ERR_PACKAGE_PATH_NOT_EXPORTED");
});

test("packed tarball carries dist and license only", () => {
  expect(packageJson.engines).toEqual({ deno: ">=2.9.3 <3", node: ">=22" });
  expect(packageJson.packageManager).toBe("pnpm@11.25.0");

  const expectedFiles = [
    "package/LICENSE",
    "package/README.md",
    "package/dist/client.d.ts",
    "package/dist/client.d.ts.map",
    "package/dist/client.js",
    "package/dist/client.js.map",
    "package/dist/credentials.d.ts",
    "package/dist/credentials.d.ts.map",
    "package/dist/credentials.js",
    "package/dist/credentials.js.map",
    "package/dist/deno-query.d.ts",
    "package/dist/deno-query.d.ts.map",
    "package/dist/deno-query.js",
    "package/dist/deno-query.js.map",
    "package/dist/deno-spawn.d.ts",
    "package/dist/deno-spawn.d.ts.map",
    "package/dist/deno-spawn.js",
    "package/dist/deno-spawn.js.map",
    "package/dist/deno.d.ts",
    "package/dist/deno.d.ts.map",
    "package/dist/deno.js",
    "package/dist/deno.js.map",
    "package/dist/errors.d.ts",
    "package/dist/errors.d.ts.map",
    "package/dist/errors.js",
    "package/dist/errors.js.map",
    "package/dist/events.d.ts",
    "package/dist/events.d.ts.map",
    "package/dist/events.js",
    "package/dist/events.js.map",
    "package/dist/gen/buf/validate/validate_pb.d.ts",
    "package/dist/gen/buf/validate/validate_pb.d.ts.map",
    "package/dist/gen/buf/validate/validate_pb.js",
    "package/dist/gen/buf/validate/validate_pb.js.map",
    "package/dist/gen/index.d.ts",
    "package/dist/gen/index.d.ts.map",
    "package/dist/gen/index.js",
    "package/dist/gen/index.js.map",
    "package/dist/gen/mecatl/v1/harness_pb.d.ts",
    "package/dist/gen/mecatl/v1/harness_pb.d.ts.map",
    "package/dist/gen/mecatl/v1/harness_pb.js",
    "package/dist/gen/mecatl/v1/harness_pb.js.map",
    "package/dist/gen/mecatl/v1/local_session_context_pb.d.ts",
    "package/dist/gen/mecatl/v1/local_session_context_pb.d.ts.map",
    "package/dist/gen/mecatl/v1/local_session_context_pb.js",
    "package/dist/gen/mecatl/v1/local_session_context_pb.js.map",
    "package/dist/gen/mecatl/v1/schedule_pb.d.ts",
    "package/dist/gen/mecatl/v1/schedule_pb.d.ts.map",
    "package/dist/gen/mecatl/v1/schedule_pb.js",
    "package/dist/gen/mecatl/v1/schedule_pb.js.map",
    "package/dist/grpc-client.d.ts",
    "package/dist/grpc-client.d.ts.map",
    "package/dist/grpc-client.js",
    "package/dist/grpc-client.js.map",
    "package/dist/http.d.ts",
    "package/dist/http.d.ts.map",
    "package/dist/http.js",
    "package/dist/http.js.map",
    "package/dist/index.d.ts",
    "package/dist/index.d.ts.map",
    "package/dist/index.js",
    "package/dist/index.js.map",
    "package/dist/media.d.ts",
    "package/dist/media.d.ts.map",
    "package/dist/media.js",
    "package/dist/media.js.map",
    "package/dist/namespaces-core.d.ts",
    "package/dist/namespaces-core.d.ts.map",
    "package/dist/namespaces-core.js",
    "package/dist/namespaces-core.js.map",
    "package/dist/namespaces-ops.d.ts",
    "package/dist/namespaces-ops.d.ts.map",
    "package/dist/namespaces-ops.js",
    "package/dist/namespaces-ops.js.map",
    "package/dist/node-client.d.ts",
    "package/dist/node-client.d.ts.map",
    "package/dist/node-client.js",
    "package/dist/node-client.js.map",
    "package/dist/node-media.d.ts",
    "package/dist/node-media.d.ts.map",
    "package/dist/node-media.js",
    "package/dist/node-media.js.map",
    "package/dist/node-query.d.ts",
    "package/dist/node-query.d.ts.map",
    "package/dist/node-query.js",
    "package/dist/node-query.js.map",
    "package/dist/node-transport.d.ts",
    "package/dist/node-transport.d.ts.map",
    "package/dist/node-transport.js",
    "package/dist/node-transport.js.map",
    "package/dist/node.d.ts",
    "package/dist/node.d.ts.map",
    "package/dist/node.js",
    "package/dist/node.js.map",
    "package/dist/plan.d.ts",
    "package/dist/plan.d.ts.map",
    "package/dist/plan.js",
    "package/dist/plan.js.map",
    "package/dist/query.d.ts",
    "package/dist/query.d.ts.map",
    "package/dist/query.js",
    "package/dist/query.js.map",
    "package/dist/raw.d.ts",
    "package/dist/raw.d.ts.map",
    "package/dist/raw.js",
    "package/dist/raw.js.map",
    "package/dist/rpc-catalog.d.ts",
    "package/dist/rpc-catalog.d.ts.map",
    "package/dist/rpc-catalog.js",
    "package/dist/rpc-catalog.js.map",
    "package/dist/run.d.ts",
    "package/dist/run.d.ts.map",
    "package/dist/run.js",
    "package/dist/run.js.map",
    "package/dist/spawn.d.ts",
    "package/dist/spawn.d.ts.map",
    "package/dist/spawn.js",
    "package/dist/spawn.js.map",
    "package/dist/team.d.ts",
    "package/dist/team.d.ts.map",
    "package/dist/team.js",
    "package/dist/team.js.map",
    "package/dist/tool-host.d.ts",
    "package/dist/tool-host.d.ts.map",
    "package/dist/tool-host.js",
    "package/dist/tool-host.js.map",
    "package/dist/tool.d.ts",
    "package/dist/tool.d.ts.map",
    "package/dist/tool.js",
    "package/dist/tool.js.map",
    "package/dist/watch.d.ts",
    "package/dist/watch.d.ts.map",
    "package/dist/watch.js",
    "package/dist/watch.js.map",
    "package/package.json",
  ];
  expect([...packedFiles.keys()].sort()).toEqual(expectedFiles);

  const packedPackageJson = JSON.parse(
    packedFiles.get("package/package.json")?.toString("utf8") ?? "{}",
  ) as PackageJson;
  expect(packedPackageJson.name).toBe("@stacklok-oss/mecatl-sdk");
  expect(packageJson.version).toBe(packageVersion);
  expect(packedPackageJson.version).toBe(packageVersion);
  expect(packedPackageJson.license).toBe("Apache-2.0");
  expect(packedPackageJson.repository).toEqual({
    directory: "sdk/typescript",
    type: "git",
    url: "https://github.com/stacklok/mecatl.git",
  });
  expect(packedPackageJson.homepage).toBe(
    "https://github.com/stacklok/mecatl/tree/main/sdk/typescript#readme",
  );
  expect(packedPackageJson.bugs).toEqual({
    url: "https://github.com/stacklok/mecatl/issues",
  });
  expect(packedPackageJson.publishConfig).toEqual({
    access: "public",
    registry: "https://registry.npmjs.org",
  });
  expect(packedPackageJson.engines).toEqual({ deno: ">=2.9.3 <3", node: ">=22" });
  expect(packedPackageJson.dependencies).toEqual(packageJson.dependencies);
  expect(packedPackageJson.dependencyLicenses).toEqual({ ajv: "MIT" });
  expect(packedFiles.get("package/LICENSE")?.toString("utf8")).toContain(
    "Apache License\n                           Version 2.0",
  );
});

test("every JavaScript module declares its Deno type slot without shifting mappings", () => {
  const modules = [...packedFiles.keys()].filter((path) => path.endsWith(".js"));
  expect(modules.length).toBeGreaterThan(0);

  for (const path of modules) {
    const declaration = `./${path.slice(path.lastIndexOf("/") + 1, -3)}.d.ts`;
    const source = packedFiles.get(path)?.toString("utf8") ?? "";
    expect(source.startsWith(`// @ts-self-types=${JSON.stringify(declaration)}\n`), path).toBe(
      true,
    );

    const sourceMap = JSON.parse(packedFiles.get(`${path}.map`)?.toString("utf8") ?? "{}") as {
      mappings?: unknown;
    };
    expect(sourceMap.mappings, `${path}.map mappings`).toEqual(expect.any(String));
    expect(String(sourceMap.mappings).startsWith(";"), `${path}.map first line`).toBe(true);
  }
});

test("the namespace batches are exported from every runtime entrypoint", () => {
  const consumer = join(consumerRoot, "core-namespaces.mts");
  writeFileSync(
    consumer,
    `
import { create } from "@bufbuild/protobuf";
import type {
  Agents,
  Client,
  Commands,
  DreamPlans,
  LearnedSkills,
  LearningAttempts,
  LearningProposals,
  McpInventory,
  Models,
  Reflection,
  RequestOptions,
  Schedules,
  Skills,
  Soul,
  Storage,
  UserModel,
  Worktrees,
} from "@stacklok-oss/mecatl-sdk";
import type {
  Agents as NodeAgents,
  Commands as NodeCommands,
  DreamPlans as NodeDreamPlans,
  LearnedSkills as NodeLearnedSkills,
  LearningAttempts as NodeLearningAttempts,
  LearningProposals as NodeLearningProposals,
  McpInventory as NodeMcpInventory,
  Models as NodeModels,
  NodeClient,
  Reflection as NodeReflection,
  RequestOptions as NodeRequestOptions,
  Schedules as NodeSchedules,
  Skills as NodeSkills,
  Soul as NodeSoul,
  Storage as NodeStorage,
  UserModel as NodeUserModel,
  Worktrees as NodeWorktrees,
} from "@stacklok-oss/mecatl-sdk/node";
import type {
  Agents as DenoAgents,
  Client as DenoClient,
  Commands as DenoCommands,
  McpInventory as DenoMcpInventory,
  Models as DenoModels,
  SpawnedClient as DenoSpawnedClient,
  Worktrees as DenoWorktrees,
} from "@stacklok-oss/mecatl-sdk/deno";
import {
  FireNowRequestSchema,
  ListAgentsRequestSchema,
  ListModelsRequestSchema,
  PlanSessionMigrationRequestSchema,
  type FireNowResponse,
  type ListAgentsResponse,
  type ListModelsResponse,
  type SessionMigrationPlan,
} from "@stacklok-oss/mecatl-sdk/gen";

declare const browser: Client;
declare const node: NodeClient;
declare const deno: DenoSpawnedClient;
const browserNamespaces: readonly [McpInventory, Agents, Commands, Worktrees, Models] = [
  browser.mcp,
  browser.agents,
  browser.commands,
  browser.worktrees,
  browser.models,
];
const nodeNamespaces: readonly [
  NodeMcpInventory,
  NodeAgents,
  NodeCommands,
  NodeWorktrees,
  NodeModels,
] = [node.mcp, node.agents, node.commands, node.worktrees, node.models];
const denoNamespaces: readonly [
  DenoMcpInventory,
  DenoAgents,
  DenoCommands,
  DenoWorktrees,
  DenoModels,
] = [deno.mcp, deno.agents, deno.commands, deno.worktrees, deno.models];
const denoClient: DenoClient = deno;
const browserOperational: readonly [
  Skills,
  LearnedSkills,
  LearningAttempts,
  LearningProposals,
  Reflection,
  Soul,
  UserModel,
  DreamPlans,
  Schedules,
  Storage,
] = [
  browser.skills,
  browser.learnedSkills,
  browser.learningAttempts,
  browser.learningProposals,
  browser.reflection,
  browser.soul,
  browser.userModel,
  browser.dreamPlans,
  browser.schedules,
  browser.storage,
];
const nodeOperational: readonly [
  NodeSkills,
  NodeLearnedSkills,
  NodeLearningAttempts,
  NodeLearningProposals,
  NodeReflection,
  NodeSoul,
  NodeUserModel,
  NodeDreamPlans,
  NodeSchedules,
  NodeStorage,
] = [
  node.skills,
  node.learnedSkills,
  node.learningAttempts,
  node.learningProposals,
  node.reflection,
  node.soul,
  node.userModel,
  node.dreamPlans,
  node.schedules,
  node.storage,
];
const browserOptions: RequestOptions = { timeoutMs: 100 };
const nodeOptions: NodeRequestOptions = browserOptions;
const browserResponse: Promise<ListAgentsResponse> = browser.agents.list(
  create(ListAgentsRequestSchema),
  browserOptions,
);
const nodeResponse: Promise<ListModelsResponse> = node.models.list(
  create(ListModelsRequestSchema),
  nodeOptions,
);
const fireResponse: Promise<FireNowResponse> = browser.schedules.fireNow(
  create(FireNowRequestSchema, { name: "nightly" }),
  browserOptions,
);
const migrationResponse: Promise<SessionMigrationPlan> = node.storage.planMigration(
  create(PlanSessionMigrationRequestSchema),
  nodeOptions,
);
void [
  browserNamespaces,
  nodeNamespaces,
  denoNamespaces,
  denoClient,
  browserOperational,
  nodeOperational,
  browserResponse,
  nodeResponse,
  fireResponse,
  migrationResponse,
];
`,
  );
  const typecheck = spawnSync(
    process.execPath,
    [
      join(packageRoot, "node_modules", "typescript", "bin", "tsc"),
      "--noEmit",
      "--strict",
      "--target",
      "ES2022",
      "--lib",
      "ESNext,DOM,DOM.Iterable",
      "--module",
      "NodeNext",
      "--moduleResolution",
      "NodeNext",
      "--types",
      "node",
      "--typeRoots",
      join(packageRoot, "node_modules", "@types"),
      consumer,
    ],
    { cwd: consumerRoot, encoding: "utf8" },
  );
  expect(typecheck.stderr).toBe("");
  expect(typecheck.stdout).toBe("");
  expect(typecheck.status).toBe(0);

  const rootGraph = entrypointGraph("package/dist/index.js");
  expect(rootGraph).toContain("package/dist/namespaces-core.js");
  expect(rootGraph).toContain("package/dist/namespaces-ops.js");
  const denoGraph = entrypointGraph("package/dist/deno.js");
  expect(denoGraph).toContain("package/dist/deno-spawn.js");
  expect(denoGraph).not.toContain("package/dist/spawn.js");
  expect(denoGraph).toContain("package/dist/node-transport.js");
  expect(denoGraph).toContain("package/dist/grpc-client.js");
  expect(denoGraph).not.toContain("package/dist/node-client.js");
  expect(denoGraph).not.toContain("package/dist/node-media.js");
  expect(denoGraph).not.toContain("package/dist/tool.js");
  expect(denoGraph).not.toContain("package/dist/tool-host.js");
  const builtins = new Set(builtinModules.map((name) => name.replace(/^node:/u, "")));
  const builtinImports = [...rootGraph].flatMap((path) => {
    const source = packedFiles.get(path)?.toString("utf8") ?? "";
    return [...source.matchAll(/\b(?:from|import)\s*(?:\(\s*)?["']([^"']+)["']/gu)]
      .map((match) => match[1] ?? "")
      .filter(
        (specifier) =>
          specifier.startsWith("node:") || builtins.has(specifier.replace(/^node:/u, "")),
      );
  });
  expect(builtinImports).toEqual([]);
});
