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
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { gunzipSync } from "node:zlib";
import { afterAll, beforeAll, expect, test } from "vitest";

type PackageJson = {
  dependencies: Record<string, string>;
  exports: Record<"." | "./gen" | "./node", { import: string; types: string }>;
  license: string;
  name: string;
  type: string;
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

beforeAll(() => {
  fixtureRoot = mkdtempSync(join(tmpdir(), "mecatl-sdk-package-"));
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
  packedFiles = readPackedFiles(join(fixtureRoot, archive));

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
    "package/dist/node-client.d.ts",
    "package/dist/node-client.d.ts.map",
    "package/dist/node-client.js",
    "package/dist/node-client.js.map",
    "package/dist/node-transport.d.ts",
    "package/dist/node-transport.d.ts.map",
    "package/dist/node-transport.js",
    "package/dist/node-transport.js.map",
    "package/dist/node.d.ts",
    "package/dist/node.d.ts.map",
    "package/dist/node.js",
    "package/dist/node.js.map",
    "package/dist/raw.d.ts",
    "package/dist/raw.d.ts.map",
    "package/dist/raw.js",
    "package/dist/raw.js.map",
    "package/dist/run.d.ts",
    "package/dist/run.d.ts.map",
    "package/dist/run.js",
    "package/dist/run.js.map",
    "package/package.json",
  ];
  expect([...packedFiles.keys()].sort()).toEqual(expectedFiles);

  const packedPackageJson = JSON.parse(
    packedFiles.get("package/package.json")?.toString("utf8") ?? "{}",
  ) as PackageJson;
  expect(packedPackageJson.name).toBe("@stacklok/mecatl-sdk");
  expect(packedPackageJson.license).toBe("Apache-2.0");
  expect(packedPackageJson.dependencies).toEqual({
    "@bufbuild/protobuf": "2.14.0",
    "@connectrpc/connect": "2.1.2",
    "@connectrpc/connect-node": "2.1.2",
  });
  expect(packedFiles.get("package/LICENSE")?.toString("utf8")).toContain(
    "Apache License\n                           Version 2.0",
  );
});
