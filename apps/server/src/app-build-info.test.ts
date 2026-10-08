// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { createApp } from "./app.js";
import { fakeRuntime } from "./testing/fakes.js";
import { installedSdkPackageVersion } from "./testing/sdk-package.js";

describe("authenticated About runtime facts", () => {
  it("reports baked Studio, installed SDK, and daemon identities separately", async () => {
    const runtime = fakeRuntime();
    const response = await createApp({ runtime, studioBuildId: "v1.2.3" }).request(
      "/api/v1/runtime",
    );
    expect(response.status).toBe(200);
    const body = (await response.json()) as Record<string, unknown>;
    expect(body.studioBuildId).toBe("v1.2.3");
    expect(body.sdkVersion).toBe(installedSdkPackageVersion());
    expect(body).not.toHaveProperty("buildId");
  });
});
