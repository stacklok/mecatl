import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import { connectTransport } from "../src/client.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  DEFAULT_TOOL_SERVER_NAME,
  type NodeClient,
  ToolRegistry,
  type ToolSchema,
  withToolRegistration,
} from "../src/tool.js";

const objectSchema = {
  additionalProperties: false,
  properties: {
    query: { type: "string" },
  },
  required: ["query"],
  type: "object",
} as const satisfies ToolSchema;

interface HarnessOptions {
  serverName?: string;
  sources?: Array<{ servers: Array<{ name: string }> }>;
}

function toolHarness(options: HarnessOptions = {}) {
  const created: Array<{ mcpServers?: unknown[] }> = [];
  const binding = {
    abort: vi.fn(),
    mcpServer: () => ({
      headers: { Authorization: "Bearer test-capability" },
      type: "http",
      url: "http://127.0.0.1:43123/mcp",
    }),
    start: vi.fn(),
    stop: vi.fn(async () => undefined),
  };
  const registry = new ToolRegistry(options.serverName ?? DEFAULT_TOOL_SERVER_NAME, binding);
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      createSession: (request) => {
        created.push(request);
        return { sessionId: `session-${created.length}` };
      },
      getCompatibilityInfo: () => ({ apiMajor: 1, features: ["mcp_servers_on_create"] }),
      listMcpSources: () => ({ sources: options.sources ?? [] }),
    });
  });
  const client = withToolRegistration(
    connectTransport({
      internal: { toolHost: registry },
      owned: false,
      transport,
      transportKind: "grpc",
      visibility: false,
    }),
    registry,
  );
  return { binding, client, created, registry, transport };
}

async function close(client: NodeClient): Promise<void> {
  await client.close();
}

describe("callback tool registration", () => {
  it("the tool set travels as a single loopback http server spec", async () => {
    const harness = toolHarness();
    harness.client.tool("lookup", objectSchema, () => "found");
    harness.client.tool("summarise", { type: "object" }, () => "summary");

    await harness.client.sessions.create({});

    expect(harness.created).toHaveLength(1);
    expect(harness.created[0]?.mcpServers).toHaveLength(1);
    expect(harness.created[0]?.mcpServers?.[0]).toMatchObject({
      headers: { Authorization: "Bearer test-capability" },
      name: "sdk",
      type: "http",
      url: "http://127.0.0.1:43123/mcp",
    });
    expect(harness.binding.start).toHaveBeenCalledOnce();
    await close(harness.client);
  });

  it("the default server name yields mcp__sdk__ prefixed tools", async () => {
    const harness = toolHarness();

    const definition = harness.client.tool("lookup", objectSchema, () => "found");

    expect(definition.modelName).toBe("mcp__sdk__lookup");
    await close(harness.client);
  });

  it("an invalid server name is refused locally against the harness grammar", () => {
    const invalidNames = ["", "a".repeat(65), "forged__server", "not valid"];
    for (const serverName of invalidNames) {
      expect(() => toolHarness({ serverName }), serverName).toThrowError(
        expect.objectContaining({ code: "tool_registration", reason: "invalid_server_name" }),
      );
    }

    const configured = toolHarness({ serverName: "app.tools-v1" });
    expect(configured.client.tool("lookup", objectSchema, () => "found").modelName).toBe(
      "mcp__app.tools-v1__lookup",
    );
    void configured.client.close();
  });

  it("a duplicate tool name fails registration", async () => {
    const harness = toolHarness();
    harness.client.tool("lookup", objectSchema, () => "first");

    expect(() => harness.client.tool("lookup", objectSchema, () => "second")).toThrowError(
      expect.objectContaining({
        code: "tool_registration",
        reason: "duplicate_name",
      }),
    );
    await close(harness.client);
  });

  it("a tool name that would forge a namespace is refused", async () => {
    const harness = toolHarness();

    for (const name of ["", "github__create_issue"]) {
      expect(() => harness.client.tool(name, objectSchema, () => "nope"), name).toThrowError(
        expect.objectContaining({
          code: "tool_registration",
          reason: "invalid_tool_name",
        }),
      );
    }
    await close(harness.client);
  });

  it("a server-global name collision is refused by the pre-flight", async () => {
    const harness = toolHarness({
      sources: [{ servers: [{ name: "other" }, { name: "sdk" }] }],
    });
    harness.client.tool("lookup", objectSchema, () => "found");

    await expect(harness.client.sessions.create({})).rejects.toMatchObject({
      code: "tool_registration",
    });
    expect(harness.created).toHaveLength(0);
    await close(harness.client);
  });

  it("an invalid schema fails at registration time", async () => {
    const harness = toolHarness();
    const invalidSchema = { required: "query", type: "object" } as unknown as ToolSchema;

    expect(() => harness.client.tool("lookup", invalidSchema, () => "found")).toThrowError(
      expect.objectContaining({
        code: "tool_registration",
        reason: "invalid_schema",
      }),
    );
    await close(harness.client);
  });

  it("arguments are validated before the handler is invoked", async () => {
    const harness = toolHarness();
    const handler = vi.fn((arguments_: Readonly<Record<string, unknown>>) => arguments_.query);
    harness.client.tool(
      "lookup",
      {
        ...objectSchema,
        properties: { query: { default: "injected", type: "string" } },
      },
      handler,
    );

    const invalid = await harness.registry.invoke("lookup", {});
    expect(invalid).toMatchObject({
      content: [{ text: expect.stringContaining("do not match its schema"), type: "text" }],
      isError: true,
    });
    expect(handler).not.toHaveBeenCalled();

    await expect(harness.registry.invoke("lookup", { query: "needle" })).resolves.toBe("needle");
    expect(handler).toHaveBeenCalledOnce();
    expect(handler.mock.calls[0]?.[0]).toEqual({ query: "needle" });
    await close(harness.client);
  });

  it("a plain JSON Schema object is sufficient to register a tool", async () => {
    const harness = toolHarness();

    expect(() => harness.client.tool("lookup", objectSchema, () => "found")).not.toThrow();
    expect(harness.registry.advertisedTools()[0]?.inputSchema).toEqual(objectSchema);
    await close(harness.client);
  });

  it("tools default to mutating and read-only is explicit", async () => {
    const harness = toolHarness();
    harness.client.tool("mutate", { type: "object" }, () => "changed");
    harness.client.tool("inspect", { type: "object" }, () => "read", { readOnly: true });

    expect(harness.registry.advertisedTools()).toMatchObject([
      { annotations: { readOnlyHint: false }, name: "mutate" },
      { annotations: { readOnlyHint: true }, name: "inspect" },
    ]);
    await close(harness.client);
  });

  it("the tool set is immutable once a session is created", async () => {
    const harness = toolHarness();
    harness.client.tool("lookup", objectSchema, () => "found");
    await harness.client.sessions.create({});

    expect(() => harness.client.tool("late", { type: "object" }, () => "late")).toThrowError(
      expect.objectContaining({ code: "invalid_state" }),
    );
    expect(harness.registry.advertisedTools().map((tool) => tool.name)).toEqual(["lookup"]);
    await close(harness.client);
  });

  it("argument payloads cannot pollute prototypes or inherit into the handler", async () => {
    const harness = toolHarness();
    let received: Readonly<Record<string, unknown>> | undefined;
    harness.client.tool("safe", { additionalProperties: true, type: "object" }, (arguments_) => {
      received = arguments_;
      return "safe";
    });
    const payload = JSON.parse(
      '{"safe":1,"__proto__":{"polluted":true},"constructor":{"polluted":true},"prototype":{"polluted":true}}',
    ) as Record<string, unknown>;

    await expect(harness.registry.invoke("safe", payload)).resolves.toBe("safe");

    expect(Object.getPrototypeOf(received)).toBeNull();
    expect(Object.prototype).not.toHaveProperty("polluted");
    expect(received).toHaveProperty("__proto__");
    expect(received).not.toHaveProperty("polluted");
    await close(harness.client);
  });

  it("readOnly is documented as an unverified caller assertion", async () => {
    const source = await readFile(
      fileURLToPath(new URL("../src/tool.ts", import.meta.url)),
      "utf8",
    );

    expect(source).toContain("Unverified caller assertion");
    expect(source).toContain("concurrent read batch");
    expect(source).toContain("plan mode's fixed");
  });
});
