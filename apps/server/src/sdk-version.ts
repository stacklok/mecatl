// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/** Read the installed SDK's manifest, never Studio's placeholder version. */
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

/**
 * Studio compiles the SDK from its own checkout, so the SDK's package version
 * can predate the source in a build. A baked Studio build ID names that source.
 */
export function reportedSdkVersion(
  version: string | undefined,
  studioBuildId: string | undefined,
): string | undefined {
  return version === undefined || !studioBuildId ? version : `${version}+${studioBuildId}`;
}
