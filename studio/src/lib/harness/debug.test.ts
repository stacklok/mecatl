import { afterEach, describe, expect, it, vi } from "vitest";
import { createHarnessDebugSession } from "./debug";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";

/**
 * Pins the ADR-0254 debug-create contract as the SDK spells it: the daemon
 * strictly decodes the body and REQUIRES profile "no-fs" alongside the target
 * binding, so the exact field set is load-bearing — a drift here is a runtime
 * 400, not a cosmetic change. Placement is server-owned (ADR 0291), so no
 * workspace key rides the body.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("createHarnessDebugSession", () => {
  it("sends exactly the ADR-0254 create body: no-fs profile and the target", async () => {
    const { requests } = stubHarnessFetch((request) =>
      request.path === "/v1/sessions"
        ? jsonResponse(201, { session_id: "dbg-1" })
        : undefined,
    );
    await expect(createHarnessDebugSession("target-1")).resolves.toBe("dbg-1");
    expect(requests[0].url).toBe("/api/mecatl/v1/sessions");
    expect(requests[0].method).toBe("POST");
    expect(requests[0].body).toEqual({
      mode: "default",
      profile: "no-fs",
      debug_target_session_id: "target-1",
    });
  });

  it("carries debug_mcp_servers only when servers are requested", async () => {
    const { requests } = stubHarnessFetch(() =>
      jsonResponse(201, { session_id: "dbg-2" }),
    );
    await createHarnessDebugSession("target-1", { mcpServers: ["fetch"] });
    expect(requests[0].body).toMatchObject({
      debug_target_session_id: "target-1",
      debug_mcp_servers: ["fetch"],
    });
  });

  it("throws the typed error on a refusal (e.g. the 404 concealing ownership)", async () => {
    stubHarnessFetch(() =>
      problemResponse(404, "not_found", "debug target is unavailable"),
    );
    await expect(createHarnessDebugSession("target-x")).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 404,
      code: "not_found",
    });
  });

  it("fails loudly when the daemon returns no session id", async () => {
    stubHarnessFetch(() => jsonResponse(201, {}));
    await expect(createHarnessDebugSession("target-1")).rejects.toThrow(
      /session id/,
    );
  });
});
