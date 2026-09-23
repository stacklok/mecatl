// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/** Read the installed published SDK's manifest, never Studio's placeholder version. */
export function readInstalledSdkVersion(sdkEntryUrl?: string): string | undefined {
  try {
    const entry = fileURLToPath(sdkEntryUrl ?? import.meta.resolve("@stacklok-oss/mecatl-sdk"));
    const manifest = JSON.parse(readFileSync(resolve(dirname(entry), "../package.json"), "utf8"));
    return manifest.name === "@stacklok-oss/mecatl-sdk" &&
      typeof manifest.version === "string" &&
      manifest.version.length > 0
      ? manifest.version
      : undefined;
  } catch {
    return undefined;
  }
}
