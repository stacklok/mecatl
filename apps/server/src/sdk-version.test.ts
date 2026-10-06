// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { readInstalledSdkVersion } from "./sdk-version.js";
import { installedSdkPackageVersion } from "./testing/sdk-package.js";

describe("installed SDK version", () => {
  it("uses the published package metadata and omits unreadable metadata", () => {
    expect(readInstalledSdkVersion()).toBe(installedSdkPackageVersion());
    expect(readInstalledSdkVersion("file:///missing/dist/index.js")).toBeUndefined();
  });
});
