import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";
import { fetchHarnessServerInfo } from "./server-info";

/**
 * Pins the safe server-identity probe (ADR 0245) over the SDK: the route and
 * provider_id query, the About-card projection, and the older-daemon (404)
 * → null degrade.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("fetchHarnessServerInfo", () => {
  it("GETs /v1/info?provider_id=… and projects the identity", async () => {
    const stub = stubHarnessFetch(() => ({
      build_id: "abc123",
      server_implementation: "mecated",
      llm_provider_display_endpoint: "https://api.openai.com/v1",
    }));
    const info = await fetchHarnessServerInfo("openai");
    expect(stub.last()).toMatchObject({
      method: "GET",
      url: "/api/mecatl/v1/info?provider_id=openai",
    });
    expect(info).toEqual({
      buildId: "abc123",
      serverImplementation: "mecated",
      providerEndpoint: "https://api.openai.com/v1",
    });
  });

  it("sends no provider_id when none is selected", async () => {
    const stub = stubHarnessFetch(() => ({ build_id: "dev" }));
    const info = await fetchHarnessServerInfo();
    expect(stub.last().url).toBe("/api/mecatl/v1/info");
    // The SDK reads a blank composition family as "unknown" (ADR 0245).
    expect(info).toEqual({
      buildId: "dev",
      serverImplementation: "unknown",
      providerEndpoint: "",
    });
  });

  it("returns null against an older daemon without the route", async () => {
    stubHarnessFetch(() =>
      jsonResponse(404, { code: "not_found", error: "no such route" }),
    );
    await expect(fetchHarnessServerInfo("openai")).resolves.toBeNull();
  });

  it("rethrows every other failure as the typed error", async () => {
    stubHarnessFetch(() =>
      jsonResponse(403, { code: "management_unauthorized", error: "nope" }),
    );
    await expect(fetchHarnessServerInfo()).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 403,
      code: "management_unauthorized",
    });
  });
});
