import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";
import { EMPTY_SOUL, fetchHarnessSoul, readHarnessSoul } from "./soul";

/**
 * Pins the resolved-persona read (`GET /v1/soul`) over the SDK: the route,
 * the snake_case + integer-enum wire shape mecated's stdlib JSON writes
 * (`size_bytes` as a number, `provenance` as the enum ordinal), the
 * projection to the UI vocabulary, and the older-daemon (404) → null
 * degrade against every other failure staying a typed error.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("fetchHarnessSoul", () => {
  it("GETs /v1/soul and projects a trusted project soul", async () => {
    const stub = stubHarnessFetch(() => ({
      soul: {
        content: "Be direct.",
        size_bytes: 9,
        sha256: "f".repeat(64),
        present: true,
        provenance: 2,
        trusted: true,
        drifted: true,
      },
    }));
    const soul = await fetchHarnessSoul();
    expect(stub.last()).toMatchObject({
      method: "GET",
      url: "/api/mecatl/v1/soul",
    });
    expect(soul).toEqual({
      content: "Be direct.",
      sizeBytes: 9,
      sha256: "f".repeat(64),
      present: true,
      provenance: "project",
      trusted: true,
      drifted: true,
    });
    // The int64 crosses the SDK as a bigint; the UI gets a plain number.
    expect(typeof soul?.sizeBytes).toBe("number");
  });

  it("labels every provenance ordinal, unspecified included", async () => {
    for (const [ordinal, label] of [
      [0, "none"],
      [1, "user"],
      [2, "project"],
      [3, "driver"],
    ] as const) {
      stubHarnessFetch(() => ({
        soul: { present: ordinal !== 0, provenance: ordinal },
      }));
      const soul = await fetchHarnessSoul();
      expect(soul?.provenance).toBe(label);
      await resetHarnessClient();
    }
  });

  it("reads a dropped untrusted project soul as not present", async () => {
    stubHarnessFetch(() => ({
      soul: { present: false, provenance: 2, trusted: false },
    }));
    const soul = await fetchHarnessSoul();
    expect(soul).toMatchObject({
      present: false,
      provenance: "project",
      trusted: false,
      content: "",
      sizeBytes: 0,
    });
  });

  it("falls back to the empty snapshot when the response carries no soul", async () => {
    stubHarnessFetch(() => ({}));
    await expect(fetchHarnessSoul()).resolves.toEqual(EMPTY_SOUL);
  });

  it("returns null against an older daemon without the route", async () => {
    stubHarnessFetch(() =>
      jsonResponse(404, { code: "not_found", error: "no such route" }),
    );
    await expect(fetchHarnessSoul()).resolves.toBeNull();
  });

  it("rethrows every other failure as the typed error", async () => {
    stubHarnessFetch(() =>
      jsonResponse(403, { code: "management_unauthorized", error: "nope" }),
    );
    await expect(fetchHarnessSoul()).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 403,
      code: "management_unauthorized",
    });
  });
});

describe("readHarnessSoul", () => {
  it("treats a missing message as nothing selected", () => {
    expect(readHarnessSoul(undefined)).toEqual(EMPTY_SOUL);
  });
});
