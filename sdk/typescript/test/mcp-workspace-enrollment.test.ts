import { create } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { connectTransport } from "../src/client.js";
import {
  HarnessService,
  ListSessionMcpConnectorsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  McpConnectorAvailability,
  McpConnectorCatalogueState,
  McpConnectorEnrollmentState,
  ProtocolError,
  type Session,
  WorkspaceEnrollmentStatus,
} from "../src/index.js";
import { projectMcpConnectorInventory } from "../src/mcp-workspace-enrollment.js";

it("session MCP connector inventory projects every typed protocol state", async () => {
  const sessionId = "inventory-session";
  const targetCalls: string[] = [];
  const responses = [
    {
      availability: "available",
      enrollmentState: "not_required",
      connectors: [
        { catalogueState: "hidden", name: "hidden", toolCount: 0 },
        { catalogueState: "declared", name: "declared", toolCount: 2 },
        { catalogueState: "discovered", name: "discovered-empty", toolCount: 0 },
        { catalogueState: "future", name: "future", toolCount: 0 },
        { catalogueState: "", name: "empty", toolCount: 0 },
      ],
      totalConnectors: 7,
      truncated: true,
    },
    {
      availability: "unavailable",
      enrollmentState: "not_started",
      connectors: [],
      totalConnectors: 0,
      truncated: false,
    },
    { availability: "future", enrollmentState: "pending", connectors: [] },
    { availability: "", enrollmentState: "completed", connectors: [] },
    { availability: "available", enrollmentState: "future", connectors: [] },
    { availability: "available", enrollmentState: "", connectors: [] },
  ];
  let responseIndex = 0;
  const client = connect({
    transport: createRouterTransport((router) => {
      router.service(HarnessService, {
        connectWorkspaceServices: () => {
          targetCalls.push("connect");
          return {};
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        listMcpSources: () => {
          targetCalls.push("direct-mcp");
          return {};
        },
        listSessionMcpConnectors: (request) => {
          expect(request.sessionId).toBe(sessionId);
          targetCalls.push("inventory");
          const response = responses[responseIndex];
          responseIndex += 1;
          return response ?? {};
        },
      });
    }),
  });

  const session = await client.sessions.get(sessionId);
  const inventories = [];
  for (const _response of responses) inventories.push(await session.listMcpConnectors());

  expect(inventories[0]).toEqual({
    availability: McpConnectorAvailability.Available,
    connectors: [
      { catalogueState: McpConnectorCatalogueState.Hidden, name: "hidden", toolCount: 0 },
      { catalogueState: McpConnectorCatalogueState.Declared, name: "declared", toolCount: 2 },
      {
        catalogueState: McpConnectorCatalogueState.Discovered,
        name: "discovered-empty",
        toolCount: 0,
      },
      { catalogueState: McpConnectorCatalogueState.Unknown, name: "future", toolCount: 0 },
      { catalogueState: McpConnectorCatalogueState.Unknown, name: "empty", toolCount: 0 },
    ],
    enrollmentState: McpConnectorEnrollmentState.NotRequired,
    totalConnectors: 7,
    truncated: true,
  });
  expect(inventories.map(({ availability }) => availability)).toEqual([
    McpConnectorAvailability.Available,
    McpConnectorAvailability.Unavailable,
    McpConnectorAvailability.Unknown,
    McpConnectorAvailability.Unknown,
    McpConnectorAvailability.Available,
    McpConnectorAvailability.Available,
  ]);
  expect(inventories.map(({ enrollmentState }) => enrollmentState)).toEqual([
    McpConnectorEnrollmentState.NotRequired,
    McpConnectorEnrollmentState.NotStarted,
    McpConnectorEnrollmentState.Pending,
    McpConnectorEnrollmentState.Completed,
    McpConnectorEnrollmentState.Unknown,
    McpConnectorEnrollmentState.Unknown,
  ]);
  expect(targetCalls).toEqual(Array.from({ length: responses.length }, () => "inventory"));
  await client.close();
});

it("session MCP connector inventory projection is detached from its protobuf source", () => {
  const response = create(ListSessionMcpConnectorsResponseSchema, {
    availability: "available",
    connectors: [{ catalogueState: "discovered", name: "calendar", toolCount: 3 }],
    enrollmentState: "completed",
    totalConnectors: 1,
    truncated: false,
  });

  const inventory = projectMcpConnectorInventory(response);
  response.availability = "unavailable";
  response.enrollmentState = "not_started";
  response.totalConnectors = 2;
  response.truncated = true;
  const connector = response.connectors[0];
  if (connector === undefined) throw new Error("expected seeded connector");
  connector.catalogueState = "hidden";
  connector.name = "mutated";
  connector.toolCount = 0;
  response.connectors.push({
    $typeName: "mecatl.v1.McpConnectorStatus",
    catalogueState: "declared",
    name: "added",
    toolCount: 1,
  });

  expect(inventory).toEqual({
    availability: McpConnectorAvailability.Available,
    connectors: [
      { catalogueState: McpConnectorCatalogueState.Discovered, name: "calendar", toolCount: 3 },
    ],
    enrollmentState: McpConnectorEnrollmentState.Completed,
    totalConnectors: 1,
    truncated: false,
  });
});

it("workspace enrollment methods preserve correlation and state transitions", async () => {
  const sessionId = "enrollment-session";
  const connectStatuses = [
    "pending",
    "connected",
    "denied",
    "cancelled",
    "expired",
    "failed",
    "future",
    "",
  ];
  const expectedConnectStatuses = [
    WorkspaceEnrollmentStatus.Pending,
    WorkspaceEnrollmentStatus.Connected,
    WorkspaceEnrollmentStatus.Denied,
    WorkspaceEnrollmentStatus.Cancelled,
    WorkspaceEnrollmentStatus.Expired,
    WorkspaceEnrollmentStatus.Failed,
    WorkspaceEnrollmentStatus.Unknown,
    WorkspaceEnrollmentStatus.Unknown,
  ];
  const cancelStatuses = ["connected", "denied", "cancelled", "expired", "failed", "future", ""];
  const requests: Array<{ enrollmentId: string; operation: string; sessionId: string }> = [];
  let connectIndex = 0;
  let cancelIndex = 0;
  const client = connect({
    transport: createRouterTransport((router) => {
      router.service(HarnessService, {
        cancelWorkspaceEnrollment: (request) => {
          requests.push({
            enrollmentId: request.enrollmentId,
            operation: "cancel",
            sessionId: request.sessionId,
          });
          const status = cancelStatuses[cancelIndex] ?? "";
          cancelIndex += 1;
          return {
            enrollmentId: request.enrollmentId,
            requiredServices: 2,
            status,
          };
        },
        connectWorkspaceServices: (request) => {
          requests.push({ enrollmentId: "", operation: "connect", sessionId: request.sessionId });
          const status = connectStatuses[connectIndex] ?? "";
          connectIndex += 1;
          return {
            enrollmentId: `connect-${connectIndex}`,
            ...(status === "pending" ? { presentationUrl: "https://example.com/authorize" } : {}),
            requiredServices: 2,
            status,
          };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        retryWorkspaceEnrollment: (request) => {
          requests.push({
            enrollmentId: request.enrollmentId,
            operation: "retry",
            sessionId: request.sessionId,
          });
          return {
            enrollmentId: "replacement-enrollment",
            presentationUrl: "http://127.0.0.1/authorize",
            requiredServices: 2,
            status: "pending",
          };
        },
      });
    }),
  });
  const session = await client.sessions.get(sessionId);

  const connected = [];
  for (const _status of connectStatuses) connected.push(await session.connectWorkspaceServices());
  expect(connected.map(({ status }) => status)).toEqual(expectedConnectStatuses);
  expect(connected[0]).toEqual({
    enrollmentId: "connect-1",
    presentationUrl: "https://example.com/authorize",
    requiredServices: 2,
    status: WorkspaceEnrollmentStatus.Pending,
  });
  expect(connected.slice(1).every((value) => value.presentationUrl === undefined)).toBe(true);

  await expect(session.retryWorkspaceEnrollment("prior-enrollment")).resolves.toEqual({
    enrollmentId: "replacement-enrollment",
    presentationUrl: "http://127.0.0.1/authorize",
    requiredServices: 2,
    status: WorkspaceEnrollmentStatus.Pending,
  });
  const cancelled = [];
  for (const _status of cancelStatuses) {
    cancelled.push(await session.cancelWorkspaceEnrollment("replacement-enrollment"));
  }
  expect(cancelled.map(({ status }) => status)).toEqual([
    WorkspaceEnrollmentStatus.Connected,
    WorkspaceEnrollmentStatus.Denied,
    WorkspaceEnrollmentStatus.Cancelled,
    WorkspaceEnrollmentStatus.Expired,
    WorkspaceEnrollmentStatus.Failed,
    WorkspaceEnrollmentStatus.Unknown,
    WorkspaceEnrollmentStatus.Unknown,
  ]);
  expect(requests.filter(({ operation }) => operation === "retry")).toEqual([
    { enrollmentId: "prior-enrollment", operation: "retry", sessionId },
  ]);
  expect(requests.filter(({ operation }) => operation === "cancel")).toEqual(
    cancelStatuses.map(() => ({
      enrollmentId: "replacement-enrollment",
      operation: "cancel",
      sessionId,
    })),
  );
  expect(requests).toHaveLength(connectStatuses.length + cancelStatuses.length + 1);
  await client.close();

  const invalidResponses = [
    { enrollmentId: "", requiredServices: 1, status: "pending" },
    { enrollmentId: "bad-count", requiredServices: 0, status: "pending" },
    {
      enrollmentId: "terminal-url",
      presentationUrl: "https://secret.example/terminal",
      requiredServices: 1,
      status: "connected",
    },
    {
      enrollmentId: "relative-url",
      presentationUrl: "/secret/path",
      requiredServices: 1,
      status: "pending",
    },
    {
      enrollmentId: "wrong-scheme",
      presentationUrl: "file:///secret/path",
      requiredServices: 1,
      status: "pending",
    },
    {
      enrollmentId: "backslash-authority",
      presentationUrl: String.raw`https:\\evil.example/path`,
      requiredServices: 1,
      status: "pending",
    },
    {
      enrollmentId: "embedded-newline",
      presentationUrl: "https://example.com/authorize\nevil",
      requiredServices: 1,
      status: "pending",
    },
    {
      enrollmentId: "embedded-control",
      presentationUrl: "https://example.com/authorize\u0001evil",
      requiredServices: 1,
      status: "pending",
    },
    {
      enrollmentId: "embedded-delete",
      presentationUrl: "https://example.com/authorize\u007fevil",
      requiredServices: 1,
      status: "pending",
    },
  ];
  for (const response of invalidResponses) {
    const invalid = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          connectWorkspaceServices: () => response,
          getCompatibilityInfo: () => ({ apiMajor: 1 }),
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        });
      }),
    });
    const invalidSession = await invalid.sessions.get(sessionId);
    await expect(invalidSession.connectWorkspaceServices()).rejects.toBeInstanceOf(ProtocolError);
    await invalid.close();
  }

  for (const [operation, response] of [
    ["retry", { enrollmentId: "same-id", requiredServices: 1, status: "pending" }],
    ["cancel-mismatch", { enrollmentId: "other-id", requiredServices: 1, status: "cancelled" }],
    ["cancel-pending", { enrollmentId: "same-id", requiredServices: 1, status: "pending" }],
  ] as const) {
    const invalid = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          cancelWorkspaceEnrollment: () => response,
          getCompatibilityInfo: () => ({ apiMajor: 1 }),
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
          retryWorkspaceEnrollment: () => response,
        });
      }),
    });
    const invalidSession = await invalid.sessions.get(sessionId);
    const call =
      operation === "retry"
        ? invalidSession.retryWorkspaceEnrollment("same-id")
        : invalidSession.cancelWorkspaceEnrollment("same-id");
    await expect(call).rejects.toBeInstanceOf(ProtocolError);
    await invalid.close();
  }

  const rejected = {
    enrollmentId: "private-enrollment-id",
    presentationUrl: "https://secret.example/private-path",
    requiredServices: 1,
    status: "private-future-status",
  };
  const diagnostics: unknown[] = [];
  const sanitized = connectTransport({
    diagnostics: (record) => diagnostics.push(record),
    owned: false,
    transport: createRouterTransport((router) => {
      router.service(HarnessService, {
        connectWorkspaceServices: () => rejected,
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    }),
    transportKind: "grpc",
    visibility: false,
  });
  const sanitizedSession = await sanitized.sessions.get(sessionId);
  const error = await sanitizedSession.connectWorkspaceServices().catch((cause) => cause);
  expect(error).toBeInstanceOf(ProtocolError);
  expect((error as ProtocolError).cause).toBeUndefined();
  const visibleError = `${String(error)} ${JSON.stringify((error as ProtocolError).toJSON())}`;
  expect(visibleError).not.toContain(rejected.enrollmentId);
  expect(visibleError).not.toContain(rejected.status);
  expect(visibleError).not.toContain(rejected.presentationUrl);
  expect(diagnostics).toEqual([]);
  await sanitized.close();
});

it("workspace enrollment exports and documentation stay complete", async () => {
  const [root, node, deno] = await Promise.all([
    import("../src/index.js"),
    import("../src/node.js"),
    import("../src/deno.js"),
  ]);
  for (const entrypoint of [root, node, deno]) {
    expect(entrypoint.McpConnectorAvailability).toBe(McpConnectorAvailability);
    expect(entrypoint.McpConnectorCatalogueState).toBe(McpConnectorCatalogueState);
    expect(entrypoint.McpConnectorEnrollmentState).toBe(McpConnectorEnrollmentState);
    expect(entrypoint.WorkspaceEnrollmentStatus).toBe(WorkspaceEnrollmentStatus);
  }

  const publicMethods: Array<keyof Session> = [
    "listMcpConnectors",
    "connectWorkspaceServices",
    "retryWorkspaceEnrollment",
    "cancelWorkspaceEnrollment",
  ];
  expect(publicMethods).toHaveLength(4);
});
