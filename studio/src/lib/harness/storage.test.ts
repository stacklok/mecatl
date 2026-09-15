import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";
import {
  decodeStorageHealth,
  fetchStorageHealth,
  isStorageDegraded,
} from "./storage";

/**
 * Pins the storage-health contract (ADR 0226) over the SDK: the route, the
 * banner subset projected off the daemon's stdlib-JSON body, and the
 * degraded classification.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const healthy = {
  available: true,
  unavailableReason: "",
  sessionCount: BigInt(4),
  corruptCount: BigInt(0),
  lastFailure: "",
  activeJob: "",
};

describe("decodeStorageHealth", () => {
  it("projects the banner subset, converting int64 counts to numbers", () => {
    expect(
      decodeStorageHealth({
        ...healthy,
        sessionCount: BigInt(12),
        corruptCount: BigInt(2),
        lastFailure: "sweep: disk full",
      }),
    ).toEqual({
      available: true,
      unavailableReason: "",
      sessionCount: 12,
      corruptCount: 2,
      v1Count: 0,
      lastFailure: "sweep: disk full",
      activeJob: "",
    });
  });
});

describe("isStorageDegraded", () => {
  const health = decodeStorageHealth(healthy);
  it("healthy store → not degraded", () => {
    expect(isStorageDegraded(health)).toBe(false);
  });
  it("unavailable, corrupt families, or a recorded failure → degraded", () => {
    expect(isStorageDegraded({ ...health, available: false })).toBe(true);
    expect(isStorageDegraded({ ...health, corruptCount: 1 })).toBe(true);
    expect(isStorageDegraded({ ...health, lastFailure: "boom" })).toBe(true);
  });
  it("plain unmigrated v1 sessions still list, so they are not degraded", () => {
    expect(isStorageDegraded({ ...health, v1Count: 9 })).toBe(false);
  });
});

describe("fetchStorageHealth", () => {
  it("GETs /v1/storage/health and decodes the daemon's snake_case answer", async () => {
    const stub = stubHarnessFetch(() => ({
      available: true,
      session_count: 2,
      corrupt_count: 1,
      last_failure: "",
      active_job: "migrate",
    }));
    const health = await fetchStorageHealth();
    expect(stub.last()).toMatchObject({
      method: "GET",
      url: "/api/mecatl/v1/storage/health",
    });
    expect(health).toMatchObject({
      available: true,
      sessionCount: 2,
      corruptCount: 1,
      activeJob: "migrate",
    });
    expect(isStorageDegraded(health)).toBe(true);
  });

  it("throws the typed error when the daemon refuses (management auth)", async () => {
    stubHarnessFetch(() =>
      jsonResponse(403, {
        code: "management_unauthorized",
        error: "management authorization required",
      }),
    );
    await expect(fetchStorageHealth()).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "management_unauthorized",
      status: 403,
    });
  });
});
