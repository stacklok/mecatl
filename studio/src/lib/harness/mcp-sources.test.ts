import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchHarnessMcpServerNames } from "./mcp-sources";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";

/**
 * Pins the MCP-server enumeration the "Debug with AI" dialog offers: the
 * configured names off GET /v1/mcp/sources, enabled sources only, deduped
 * and sorted; an empty inventory is an empty list (not an error), and a
 * daemon that cannot answer throws the typed error so the dialog can fall
 * back to typed names.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("fetchHarnessMcpServerNames", () => {
  it("collects the configured server names of enabled sources, deduped and sorted", async () => {
    const { requests } = stubHarnessFetch((request) =>
      request.path === "/v1/mcp/sources"
        ? jsonResponse(200, {
            sources: [
              {
                name: "static",
                kind: "static",
                enabled: true,
                servers: [
                  { name: "github", url: "http://x/gh" },
                  { name: "slack", url: "http://x/slack" },
                ],
              },
              {
                name: "toolhive(default)",
                kind: "toolhive",
                enabled: true,
                group: "default",
                servers: [{ name: "github", url: "http://y/gh" }],
              },
              {
                name: "toolhive(off)",
                kind: "toolhive",
                enabled: false,
                servers: [{ name: "hidden", url: "http://z" }],
              },
            ],
          })
        : undefined,
    );
    await expect(fetchHarnessMcpServerNames()).resolves.toEqual([
      "github",
      "slack",
    ]);
    expect(requests[0].method).toBe("GET");
    expect(requests[0].url).toBe("/api/mecatl/v1/mcp/sources");
  });

  it("returns an empty list when no server is configured", async () => {
    stubHarnessFetch((request) =>
      request.path === "/v1/mcp/sources"
        ? jsonResponse(200, { sources: [] })
        : undefined,
    );
    await expect(fetchHarnessMcpServerNames()).resolves.toEqual([]);
  });

  it("throws the typed error when the daemon cannot answer", async () => {
    stubHarnessFetch(() => problemResponse(404, "not_found", "no such route"));
    await expect(fetchHarnessMcpServerNames()).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 404,
    });
  });
});
