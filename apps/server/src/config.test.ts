// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { ConfigurationError, studioConfigFromEnvironment } from "./config.js";

describe("Studio configuration", () => {
  it("binds loopback by default and every interface only inside the image or when STUDIO_HOST says so", () => {
    expect(studioConfigFromEnvironment({}).host).toBe("127.0.0.1");
    expect(
      studioConfigFromEnvironment({
        STUDIO_IMAGE: "1",
        STUDIO_PUBLIC_URL: "https://studio.example.com",
      }).host,
    ).toBe("0.0.0.0");
    expect(studioConfigFromEnvironment({ STUDIO_HOST: "0.0.0.0" }).host).toBe("0.0.0.0");
    expect(studioConfigFromEnvironment({ STUDIO_HOST: "[::1]" }).host).toBe("::1");
    expect(
      studioConfigFromEnvironment({
        STUDIO_HOST: "10.0.0.4",
        STUDIO_IMAGE: "1",
        STUDIO_PUBLIC_URL: "https://studio.example.com",
      }).host,
    ).toBe("10.0.0.4");
    expect(() => studioConfigFromEnvironment({ STUDIO_HOST: "http://x/" })).toThrow(
      ConfigurationError,
    );
  });
});
