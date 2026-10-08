import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, SESSION_ID_HEADER_NAME, UnsupportedFeatureError } from "../src/index.js";

describe("session guardrail diagnostics", () => {
  it("invokes the exact guardrail RPCs with session affinity", async () => {
    const calls: Array<{
      method: string;
      sessionId: string;
      reviewId: string;
      caller: string | null;
    }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-1" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getGuardrailReviewDetail: (request, context) => {
          calls.push({
            method: "GetGuardrailReviewDetail",
            sessionId: request.sessionId,
            reviewId: request.reviewId,
            caller: context.requestHeader.get("x-caller"),
          });
          expect(context.requestHeader.get(SESSION_ID_HEADER_NAME)).toBe("session-1");
          return {
            reviewId: request.reviewId,
            concern: "untrusted instruction",
            sourceDisplay: "tool output",
            nextAction: "review",
          };
        },
        listGuardrailCoverage: (request, context) => {
          calls.push({
            method: "ListGuardrailCoverage",
            sessionId: request.sessionId,
            reviewId: "",
            caller: context.requestHeader.get("x-caller"),
          });
          expect(context.requestHeader.get(SESSION_ID_HEADER_NAME)).toBe("session-1");
          return { enabled: true, checkerProviderId: "provider", checkerModelId: "model" };
        },
      });
    });
    const client = connect({ transport });
    try {
      const session = await client.sessions.create({});
      const options = { headers: new Headers({ "x-caller": "sdk-test" }) };
      await expect(session.guardrailCoverage(options)).resolves.toMatchObject({
        enabled: true,
        checkerProviderId: "provider",
      });
      await expect(session.guardrailReviewDetail("review-1", options)).resolves.toMatchObject({
        reviewId: "review-1",
        concern: "untrusted instruction",
      });
      expect(calls).toEqual([
        {
          method: "ListGuardrailCoverage",
          sessionId: "session-1",
          reviewId: "",
          caller: "sdk-test",
        },
        {
          method: "GetGuardrailReviewDetail",
          sessionId: "session-1",
          reviewId: "review-1",
          caller: "sdk-test",
        },
      ]);
    } finally {
      await client.close();
    }
  });

  it("reports unavailable HTTP routes without sending a guardrail request", async () => {
    const paths: string[] = [];
    const client = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input) => {
        const path = new URL(String(input)).pathname;
        paths.push(path);
        if (path === "/v1/compatibility") {
          return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
        }
        if (path === "/v1/sessions") {
          return Response.json({ session_id: "session-1" }, { status: 201 });
        }
        throw new Error(`unexpected HTTP route ${path}`);
      },
    });
    try {
      const session = await client.sessions.create({});
      await expect(session.guardrailCoverage()).rejects.toBeInstanceOf(UnsupportedFeatureError);
      await expect(session.guardrailReviewDetail("review-1")).rejects.toBeInstanceOf(
        UnsupportedFeatureError,
      );
      expect(paths).toEqual(["/v1/compatibility", "/v1/sessions"]);
    } finally {
      await client.close();
    }
  });
});
