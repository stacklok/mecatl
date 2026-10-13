// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Studio, the SDK, and the daemon ship from one commit only while the BFF builds
// the SDK from this checkout. An npm version here would let a Studio image pair
// main's daemon contract with an older released SDK.
describe("SDK source", () => {
  it("builds the SDK from the checkout, never from a registry release", () => {
    const manifest = JSON.parse(
      readFileSync(new URL("../package.json", import.meta.url), "utf8"),
    ) as { dependencies: Record<string, string> };
    expect(manifest.dependencies["@stacklok-oss/mecatl-sdk"]).toBe("file:../../sdk/typescript");
  });
});
