// SPDX-License-Identifier: Apache-2.0

import { existsSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

function readManifest(url: URL): { dependencies: Record<string, string> } {
  return JSON.parse(readFileSync(url, "utf8")) as { dependencies: Record<string, string> };
}

/** The version Node resolves for `name` from the installed SDK, ignoring package exports. */
function installedVersion(fromSdk: NodeJS.Require, name: string): string | undefined {
  for (const directory of fromSdk.resolve.paths(name) ?? []) {
    const manifest = join(directory, name, "package.json");
    if (existsSync(manifest)) {
      return (JSON.parse(readFileSync(manifest, "utf8")) as { version: string }).version;
    }
  }
  return undefined;
}

// Studio, the SDK, and the daemon contract are built from one commit only while
// the BFF builds the SDK from this checkout. An npm version here would let a
// Studio image pair main's daemon contract with an older released SDK.
describe("SDK source", () => {
  it("builds the SDK from the checkout, never from a registry release", () => {
    const manifest = readManifest(new URL("../package.json", import.meta.url));
    expect(manifest.dependencies["@stacklok-oss/mecatl-sdk"]).toBe("file:../../sdk/typescript");
  });

  // A frozen install does not compare a file: dependency's manifest with its
  // lockfile snapshot, so the SDK's pins can move without apps/pnpm-lock.yaml.
  it("installs the SDK's dependencies at the versions the checkout pins", () => {
    const pinned = readManifest(new URL("../../../sdk/typescript/package.json", import.meta.url));
    const fromSdk = createRequire(import.meta.resolve("@stacklok-oss/mecatl-sdk"));
    const installed = Object.fromEntries(
      Object.keys(pinned.dependencies).map((name) => [name, installedVersion(fromSdk, name)]),
    );
    expect(
      installed,
      "refresh with `pnpm --filter server update @stacklok-oss/mecatl-sdk --lockfile-only` in apps/",
    ).toEqual(pinned.dependencies);
  });
});
