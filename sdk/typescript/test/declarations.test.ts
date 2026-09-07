import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, rmSync, symlinkSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, test } from "vitest";

const packageRoot = fileURLToPath(new URL("../", import.meta.url));
const consumerConfig = join(packageRoot, "declaration-consumer", "tsconfig.json");
const consumerInstall = join(
  packageRoot,
  "declaration-consumer",
  "node_modules",
  "@stacklok",
  "mecatl-sdk",
);

function compileDeclarations(compilerPackage: "typescript" | "typescript-5.7"): void {
  const result = spawnSync(
    process.execPath,
    [join(packageRoot, "node_modules", compilerPackage, "bin", "tsc"), "-p", consumerConfig],
    {
      cwd: packageRoot,
      encoding: "utf8",
      env: { ...process.env, NO_COLOR: "1" },
    },
  );
  if (result.status !== 0) {
    throw new Error(
      [`TypeScript compiler exited with status ${result.status}`, result.stdout, result.stderr]
        .filter(Boolean)
        .join("\n"),
    );
  }
}

beforeAll(() => {
  execFileSync("pnpm", ["run", "build"], {
    cwd: packageRoot,
    env: { ...process.env, NO_COLOR: "1" },
    stdio: "pipe",
  });
  rmSync(join(packageRoot, "declaration-consumer", "node_modules"), {
    force: true,
    recursive: true,
  });
  mkdirSync(dirname(consumerInstall), { recursive: true });
  symlinkSync(packageRoot, consumerInstall, "junction");
}, 60_000);

afterAll(() => {
  rmSync(join(packageRoot, "declaration-consumer", "node_modules"), {
    force: true,
    recursive: true,
  });
});

test("public declarations compile with TypeScript 5.7", () => {
  compileDeclarations("typescript-5.7");
});

test("public declarations compile with TypeScript 6", () => {
  compileDeclarations("typescript");
});
