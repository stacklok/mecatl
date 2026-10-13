// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { readInstalledSdkVersion, reportedSdkVersion } from "./sdk-version.js";
import { installedSdkPackageVersion } from "./testing/sdk-package.js";

describe("installed SDK version", () => {
  it("uses the installed package metadata and omits unreadable metadata", () => {
    expect(readInstalledSdkVersion()).toBe(installedSdkPackageVersion());
    expect(readInstalledSdkVersion("file:///missing/dist/index.js")).toBeUndefined();
  });

  it("stamps the SDK version with the Studio build that compiled it", () => {
    expect(reportedSdkVersion("0.5.0", "v0.0.48-main.212.g1a2b3c4d")).toBe(
      "0.5.0+v0.0.48-main.212.g1a2b3c4d",
    );
    expect(reportedSdkVersion("0.5.0", undefined)).toBe("0.5.0");
    expect(reportedSdkVersion(undefined, "v0.0.48")).toBeUndefined();
  });
});
