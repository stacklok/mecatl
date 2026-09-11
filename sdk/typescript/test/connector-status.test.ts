import { createRouterTransport } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  createHttpTransport,
  createRawClient,
  SESSION_ID_HEADER_NAME,
  withSessionAffinity,
} from "../src/index.js";

it("broker connector inspection has gRPC and HTTP parity without direct MCP calls", async () => {
  const sessionId = "owned-session";
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: { mcpConnectorStatus: true } }),
      listSessionMcpConnectors: (request, context) => {
        expect(request.sessionId).toBe(sessionId);
        expect(context.requestHeader.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
        return {
          availability: "available",
          enrollmentState: "pending",
          connectors: [{ name: "calendar", catalogueState: "hidden" }],
          totalConnectors: 1,
        };
      },
    });
  });
  const grpc = createRawClient({ transport });
  const paths: string[] = [];
  const http = createRawClient({
    transport: createHttpTransport({
      baseUrl: "https://mecatl.test",
      fetch: async (input, init) => {
        const path = new URL(String(input)).pathname;
        paths.push(path);
        if (path === "/v1/compatibility")
          return Response.json({ api_major: 1, capabilities: { mcp_connector_status: true } });
        expect(path).toBe(`/v1/sessions/${sessionId}/mcp/connectors`);
        expect(new Headers(init?.headers).get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
        return Response.json({
          availability: "available",
          enrollment_state: "pending",
          connectors: [{ name: "calendar", catalogue_state: "hidden" }],
          total_connectors: 1,
        });
      },
    }),
  });
  const method = HarnessService.method.listSessionMcpConnectors;
  const options = withSessionAffinity(sessionId);
  const first = await grpc.unary(method, { sessionId }, options);
  expect(await http.unary(method, { sessionId }, options)).toEqual(first);
  expect(first.connectors[0]?.toolCount).toBe(0);
  expect(paths).toContain(`/v1/sessions/${sessionId}/mcp/connectors`);
});
