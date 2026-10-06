// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { createApp } from "../app";
import type { StorageService } from "../mecatl/storage";

describe("storage routes", () => {
  it("serves storage health and answers 503 without a runtime", async () => {
    const storage: StorageService = {
      supported: true,
      async getHealth() {
        return {
          activeJob: true,
          available: true,
          childCount: "4",
          corruptCount: "0",
          currentBytes: "2048",
          lastFailure: false,
          mainCount: "5",
          reclaimableBytes: null,
          scheduledCount: "2",
          sessionCount: "11",
          supported: true,
          unknownCount: "0",
        };
      },
    };
    const response = await createApp({ storage }).request("/api/v1/storage/health");
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({
      activeJob: true,
      available: true,
      childCount: "4",
      corruptCount: "0",
      currentBytes: "2048",
      lastFailure: false,
      mainCount: "5",
      reclaimableBytes: null,
      scheduledCount: "2",
      sessionCount: "11",
      supported: true,
      unknownCount: "0",
    });

    const detached = await createApp().request("/api/v1/storage/health");
    expect(detached.status).toBe(503);
    await expect(detached.json()).resolves.toMatchObject({ code: "runtime_unavailable" });
  });
});
