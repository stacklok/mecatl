// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";

/** Test fixture: the published package installed by this workspace's lockfile. */
export function installedSdkPackageVersion(): string {
  const manifest = JSON.parse(
    readFileSync(
      new URL("../../node_modules/@stacklok-oss/mecatl-sdk/package.json", import.meta.url),
      "utf8",
    ),
  ) as { version: string };
  return manifest.version;
}
