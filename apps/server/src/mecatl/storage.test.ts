// SPDX-License-Identifier: Apache-2.0

import type { Client } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";
import { createMecatlStorageService } from "./storage";

describe("Mecatl storage health", () => {
  it("reports unsupported without calling the daemon when the capability is off", async () => {
    const getHealth = vi.fn();
    const service = createMecatlStorageService(
      { storage: { getHealth } } as unknown as Client,
      false,
    );

    await expect(service.getHealth()).resolves.toMatchObject({
      available: false,
      supported: false,
    });
    expect(getHealth).not.toHaveBeenCalled();
  });

  it("maps the SDK's bigint counts and byte availability flags", async () => {
    const getHealth = vi.fn().mockResolvedValue({
      activeJob: "job-1",
      available: true,
      childCount: 4n,
      corruptCount: 0n,
      currentBytes: 2048n,
      currentBytesAvailable: true,
      lastFailure: "",
      mainCount: 5n,
      reclaimableBytes: 0n,
      reclaimableBytesAvailable: false,
      scheduledCount: 2n,
      sessionCount: 11n,
      unknownCount: 0n,
    });
    const service = createMecatlStorageService(
      { storage: { getHealth } } as unknown as Client,
      true,
    );

    await expect(service.getHealth()).resolves.toEqual({
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
  });

  it("reads the supported flag freshly on every call from a function source", async () => {
    let supported = false;
    const getHealth = vi.fn().mockResolvedValue({
      activeJob: "",
      available: true,
      childCount: 0n,
      corruptCount: 0n,
      currentBytes: 0n,
      currentBytesAvailable: true,
      lastFailure: "",
      mainCount: 0n,
      reclaimableBytes: 0n,
      reclaimableBytesAvailable: true,
      scheduledCount: 0n,
      sessionCount: 0n,
      unknownCount: 0n,
    });
    const service = createMecatlStorageService(
      { storage: { getHealth } } as unknown as Client,
      () => supported,
    );

    expect(service.supported).toBe(false);
    supported = true;
    expect(service.supported).toBe(true);
    await service.getHealth();
    expect(getHealth).toHaveBeenCalledTimes(1);
  });
});
