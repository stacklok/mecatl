import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  sessionSnapshot,
  stubHarnessFetch,
} from "./sdk-test-stub";
import { compactHarnessSession, fetchAllSessions } from "./sessions";

/**
 * Pins the ADR-0248 error contract as it crosses the SDK: a daemon refusal
 * is typed on the stable machine `code` (problem-details body) with the
 * server's own words as the message; named codes get plainer framing; a
 * non-JSON error body degrades to the SDK's status line rather than a crash.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("HarnessApiError via the SDK", () => {
  it("carries the stable code and the server's message", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return jsonResponse(200, sessionSnapshot("s1"));
      return jsonResponse(409, {
        type: "urn:mecatl:error:stale_run_control",
        code: "stale_run_control",
        error: "the named run already ended",
        status: 409,
      });
    });
    await expect(compactHarnessSession("s1")).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 409,
      code: "stale_run_control",
      message: "the named run already ended",
    });
  });

  it("frames the named codes in plain language", async () => {
    stubHarnessFetch(() =>
      jsonResponse(503, { code: "draining", error: "server draining" }),
    );
    const error = await fetchAllSessions().catch((caught) => caught);
    expect(error).toBeInstanceOf(HarnessApiError);
    expect((error as HarnessApiError).code).toBe("draining");
    expect((error as HarnessApiError).message).toMatch(/restarting/);
  });

  it("degrades to a status-only error against a non-JSON error body", async () => {
    stubHarnessFetch(
      () => new Response("nope", { status: 500, statusText: "Internal Error" }),
    );
    const error = await fetchAllSessions().catch((caught) => caught);
    expect(error).toBeInstanceOf(HarnessApiError);
    expect((error as HarnessApiError).status).toBe(500);
    expect((error as HarnessApiError).code).toBe("");
    expect((error as HarnessApiError).message).toMatch(/500/);
  });

  it("surfaces an incompatible daemon (api_major ≠ 1) as a typed error, not a hang", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, { api_major: 2, features: [] }),
    );
    const error = await fetchAllSessions().catch((caught) => caught);
    expect(error).toBeInstanceOf(HarnessApiError);
    expect((error as HarnessApiError).code).toBe("incompatible_server");
  });
});
