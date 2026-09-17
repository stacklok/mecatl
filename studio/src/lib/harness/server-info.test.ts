import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";
import {
  fetchHarnessServerInfo,
  probeHarnessServerInfo,
  serverInfoLookup,
} from "./server-info";

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

/**
 * The non-throwing probe names HOW the lookup went in the TUI's closed
 * vocabulary (cmd/mecatui/client/serverinfo.go `SafeInfoFailure`): ok /
 * not-supported (the route is missing) / unreachable (nothing answered, or
 * the proxy's 503 for a dead daemon) / invalid-response (anything else).
 * The About card stays rendered on every branch and the `/diagnostics`
 * report prints the class, never the message.
 */
describe("probeHarnessServerInfo", () => {
  it("reports ok with the identity", async () => {
    stubHarnessFetch(() => ({
      build_id: "abc123",
      server_implementation: "mecated",
      llm_provider_display_endpoint: "https://api.openai.com/v1",
    }));
    await expect(probeHarnessServerInfo("openai")).resolves.toEqual({
      info: {
        buildId: "abc123",
        serverImplementation: "mecated",
        providerEndpoint: "https://api.openai.com/v1",
      },
      lookup: "ok",
    });
  });

  it("reports not-supported when the route is missing (404)", async () => {
    stubHarnessFetch(() =>
      jsonResponse(404, { code: "not_found", error: "no such route" }),
    );
    await expect(probeHarnessServerInfo()).resolves.toEqual({
      info: null,
      lookup: "not-supported",
    });
  });

  it("reports not-supported when the daemon's feature list omits server_info", async () => {
    stubHarnessFetch(() => ({ build_id: "x" }), {
      features: ["http_steer"],
    });
    await expect(probeHarnessServerInfo()).resolves.toEqual({
      info: null,
      lookup: "not-supported",
    });
  });

  it("reports unreachable for the proxy's 503 and for a transport failure", async () => {
    stubHarnessFetch(() =>
      jsonResponse(503, {
        code: "upstream_unavailable",
        error: "Mecatl is unavailable",
      }),
    );
    await expect(probeHarnessServerInfo()).resolves.toEqual({
      info: null,
      lookup: "unreachable",
    });
    await resetHarnessClient();
    stubHarnessFetch(() => {
      throw new TypeError("Failed to fetch");
    });
    await expect(probeHarnessServerInfo()).resolves.toEqual({
      info: null,
      lookup: "unreachable",
    });
  });

  it("reports invalid-response for a refusal", async () => {
    stubHarnessFetch(() =>
      jsonResponse(403, { code: "management_unauthorized", error: "nope" }),
    );
    await expect(probeHarnessServerInfo()).resolves.toEqual({
      info: null,
      lookup: "invalid-response",
    });
  });
});

describe("serverInfoLookup", () => {
  it("classifies by status and code, never by message", () => {
    expect(serverInfoLookup(new HarnessApiError(404, "", "x"))).toBe(
      "not-supported",
    );
    expect(
      serverInfoLookup(new HarnessApiError(0, "unsupported_feature", "x")),
    ).toBe("not-supported");
    expect(serverInfoLookup(new HarnessApiError(0, "transport", "x"))).toBe(
      "unreachable",
    );
    expect(serverInfoLookup(new HarnessApiError(502, "", "x"))).toBe(
      "unreachable",
    );
    expect(serverInfoLookup(new HarnessApiError(0, "protocol", "x"))).toBe(
      "invalid-response",
    );
    expect(serverInfoLookup(new HarnessApiError(500, "", "unreachable"))).toBe(
      "invalid-response",
    );
    expect(serverInfoLookup(new Error("unreachable"))).toBe("invalid-response");
  });
});
