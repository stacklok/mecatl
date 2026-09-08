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
  engines: { node: string };
  exports: Record<"." | "./gen" | "./node", { import: string; types: string }>;
  homepage: string;
  license: string;
  name: string;
  packageManager: string;
  publishConfig: { registry: string };
  repository: { directory: string; type: string; url: string };
  type: string;
  version: string;
};

const packageRoot = fileURLToPath(new URL("../", import.meta.url));
const packageJson = JSON.parse(
  readFileSync(join(packageRoot, "package.json"), "utf8"),
) as PackageJson;

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

function browserEntrypointGraph(): ReadonlySet<string> {
  const pending = ["package/dist/index.js"];
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
  const installedRoot = join(consumerRoot, "node_modules", "@stacklok", "mecatl-sdk");
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

test("exports map exposes exactly ., ./node, ./gen", () => {
  expect(Object.keys(packageJson.exports)).toEqual([".", "./node", "./gen"]);
  expect(packageJson.type).toBe("module");

  for (const target of Object.values(packageJson.exports)) {
    expect(Object.keys(target).sort()).toEqual(["import", "types"]);
    expect(target.import).toMatch(/^\.\/dist\/.*\.js$/);
    expect(target.types).toMatch(/^\.\/dist\/.*\.d\.ts$/);
    expect(packedFiles.has(`package/${target.import.slice(2)}`)).toBe(true);
    expect(packedFiles.has(`package/${target.types.slice(2)}`)).toBe(true);
  }

  for (const subpath of ["", "/node", "/gen"]) {
    execFileSync(
      process.execPath,
      ["--input-type=module", "--eval", `await import("@stacklok/mecatl-sdk${subpath}");`],
      { cwd: consumerRoot, stdio: "pipe" },
    );
  }

  const requireResult = spawnSync(
    process.execPath,
    ["--input-type=commonjs", "--eval", 'require("@stacklok/mecatl-sdk")'],
    { cwd: consumerRoot, encoding: "utf8" },
  );
  expect(requireResult.status).not.toBe(0);
  expect(requireResult.stderr).toMatch(/ERR_PACKAGE_PATH_NOT_EXPORTED|ERR_REQUIRE_ESM/);

  const privateImport = spawnSync(
    process.execPath,
    ["--input-type=module", "--eval", 'await import("@stacklok/mecatl-sdk/private");'],
    { cwd: consumerRoot, encoding: "utf8" },
  );
  expect(privateImport.status).not.toBe(0);
  expect(privateImport.stderr).toContain("ERR_PACKAGE_PATH_NOT_EXPORTED");
});

test("packed tarball carries dist and license only", () => {
  expect(packageJson.engines).toEqual({ node: ">=22" });
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
  expect(packedPackageJson.name).toBe("@stacklok/mecatl-sdk");
  expect(packedPackageJson.version).toBe("0.0.1");
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
    registry: "https://npm.pkg.github.com",
  });
  expect(packedPackageJson.engines).toEqual({ node: ">=22" });
  expect(packedPackageJson.dependencies).toEqual({
    "@bufbuild/protobuf": "2.14.0",
    "@connectrpc/connect": "2.1.2",
    "@connectrpc/connect-node": "2.1.2",
    ajv: "8.20.0",
  });
  expect(packedPackageJson.dependencyLicenses).toEqual({ ajv: "MIT" });
  expect(packedFiles.get("package/LICENSE")?.toString("utf8")).toContain(
    "Apache License\n                           Version 2.0",
  );
});

test("the namespace batches are exported from both supported entrypoints", () => {
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
} from "@stacklok/mecatl-sdk";
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
} from "@stacklok/mecatl-sdk/node";
import {
  FireNowRequestSchema,
  ListAgentsRequestSchema,
  ListModelsRequestSchema,
  PlanSessionMigrationRequestSchema,
  type FireNowResponse,
  type ListAgentsResponse,
  type ListModelsResponse,
  type SessionMigrationPlan,
} from "@stacklok/mecatl-sdk/gen";

declare const browser: Client;
declare const node: NodeClient;
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

  const graph = browserEntrypointGraph();
  expect(graph).toContain("package/dist/namespaces-core.js");
  expect(graph).toContain("package/dist/namespaces-ops.js");
  const builtins = new Set(builtinModules.map((name) => name.replace(/^node:/u, "")));
  const builtinImports = [...graph].flatMap((path) => {
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
