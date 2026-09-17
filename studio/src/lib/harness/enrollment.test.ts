import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cancelWorkspaceEnrollment,
  connectWorkspaceServices,
  fetchSessionConnectors,
  isStaleEnrollment,
  retryWorkspaceEnrollment,
} from "./enrollment";
import { HarnessApiError } from "./errors";
import { resetHarnessClient } from "./sdk";
import {
  problemResponse,
  sessionSnapshot,
  stubHarnessFetch,
} from "./sdk-test-stub";

/**
 * Pins the workspace-enrollment daemon calls as the SDK spells them: the
 * three controls are bodyless POSTs on the session's `workspace-enrollment`
 * routes (the daemon 400s any body byte), the projection crosses field for
 * field, and the connector inventory is a GET whose ownership/verification
 * refusals fold to `null` rather than an error.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const WIRE_PENDING = {
  enrollment_id: "e1",
  status: "pending",
  required_services: 2,
  presentation_url: "https://broker.test/consent?e=1",
};

describe("connectWorkspaceServices", () => {
  it("posts no body to the session's connect route and maps the projection", async () => {
    const stub = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return sessionSnapshot("s1");
      if (request.path === "/v1/sessions/s1/workspace-enrollment/connect") {
        return WIRE_PENDING;
      }
      return undefined;
    });

    const result = await connectWorkspaceServices("s1");

    const call = stub.last();
    expect(call.method).toBe("POST");
    expect(call.path).toBe("/v1/sessions/s1/workspace-enrollment/connect");
    expect(call.body).toBeUndefined();
    expect(result).toEqual({
      enrollmentId: "e1",
      status: "pending",
      requiredServices: 2,
      presentationUrl: "https://broker.test/consent?e=1",
    });
  });

  it("surfaces a daemon refusal as a typed HarnessApiError", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return sessionSnapshot("s1");
      return problemResponse(
        412,
        "failed_precondition",
        "workspace services are not configured",
      );
    });

    const failure = await connectWorkspaceServices("s1").catch((e) => e);
    expect(failure).toBeInstanceOf(HarnessApiError);
    expect(failure.status).toBe(412);
    expect(failure.code).toBe("failed_precondition");
    expect(isStaleEnrollment(failure)).toBe(true);
  });
});

describe("retryWorkspaceEnrollment / cancelWorkspaceEnrollment", () => {
  it("address the exact enrollment id, bodyless", async () => {
    const stub = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return sessionSnapshot("s1");
      if (request.path.endsWith("/workspace-enrollment/e1/retry")) {
        return { ...WIRE_PENDING, enrollment_id: "e2" };
      }
      if (request.path.endsWith("/workspace-enrollment/e2/cancel")) {
        return {
          enrollment_id: "e2",
          status: "cancelled",
          required_services: 2,
          presentation_url: "",
        };
      }
      return undefined;
    });

    const retried = await retryWorkspaceEnrollment("s1", "e1");
    expect(stub.last().method).toBe("POST");
    expect(stub.last().path).toBe(
      "/v1/sessions/s1/workspace-enrollment/e1/retry",
    );
    expect(stub.last().body).toBeUndefined();
    expect(retried.enrollmentId).toBe("e2");

    const cancelled = await cancelWorkspaceEnrollment("s1", "e2");
    expect(stub.last().path).toBe(
      "/v1/sessions/s1/workspace-enrollment/e2/cancel",
    );
    expect(stub.last().body).toBeUndefined();
    expect(cancelled.status).toBe("cancelled");
    expect(cancelled.presentationUrl).toBe("");
  });
});

describe("fetchSessionConnectors", () => {
  it("GETs the session's connector inventory and maps it", async () => {
    const stub = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return sessionSnapshot("s1");
      if (request.path === "/v1/sessions/s1/mcp/connectors") {
        return {
          availability: "available",
          enrollment_state: "completed",
          connectors: [
            { name: "github", catalogue_state: "ready", tool_count: 12 },
          ],
          total_connectors: 1,
          truncated: false,
        };
      }
      return undefined;
    });

    const result = await fetchSessionConnectors("s1");

    expect(stub.last().method).toBe("GET");
    expect(stub.last().path).toBe("/v1/sessions/s1/mcp/connectors");
    expect(result).toEqual({
      availability: "available",
      enrollmentState: "completed",
      connectors: [{ name: "github", toolCount: 12, catalogueState: "ready" }],
      totalConnectors: 1,
      truncated: false,
    });
  });

  it.each([
    [412, "failed_precondition"],
    [401, "unauthenticated"],
    [403, "permission_denied"],
    [404, "not_found"],
  ])("folds a %i %s refusal to null (inspection unavailable)", async (status, code) => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return sessionSnapshot("s1");
      return problemResponse(status, code, "connector inspection unavailable");
    });

    await expect(fetchSessionConnectors("s1")).resolves.toBeNull();
  });

  it("rethrows any other failure", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return sessionSnapshot("s1");
      return problemResponse(500, "internal", "boom");
    });

    const failure = await fetchSessionConnectors("s1").catch((e) => e);
    expect(failure).toBeInstanceOf(HarnessApiError);
    expect(failure.status).toBe(500);
  });
});

describe("isStaleEnrollment", () => {
  it("is true only for the daemon's failed_precondition refusal", () => {
    expect(isStaleEnrollment(new HarnessApiError(412, "", "stale"))).toBe(true);
    expect(
      isStaleEnrollment(new HarnessApiError(0, "failed_precondition", "x")),
    ).toBe(true);
    expect(isStaleEnrollment(new HarnessApiError(404, "not_found", "x"))).toBe(
      false,
    );
    expect(isStaleEnrollment(new Error("offline"))).toBe(false);
  });
});
