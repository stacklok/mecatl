import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import {
  create,
  type DescMessage,
  type DescMethodStreaming,
  type DescMethodUnary,
  type MessageInitShape,
} from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  type ContextValues,
  type StreamResponse,
  type Transport,
  type UnaryResponse,
} from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import {
  GetMcpPromptRequestSchema,
  ListAgentsRequestSchema,
  ListCommandsRequestSchema,
  ListMcpPromptsRequestSchema,
  ListMcpResourcesRequestSchema,
  ListMcpSourcesRequestSchema,
  ListModelsRequestSchema,
  ListToolHiveGroupsRequestSchema,
  ListWorktreesRequestSchema,
  ReadMcpResourceRequestSchema,
  RefreshMcpSourcesRequestSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { connect } from "../src/index.js";
import { RPC_CATALOG } from "../src/rpc-catalog.js";

const packageRoot = fileURLToPath(new URL("../", import.meta.url));
const apiReports = ["mecatl-sdk.api.md", "mecatl-sdk-node.api.md"] as const;
const signatureAllowlist = new Set(["Promise", "RequestOptions"]);
const batchACatalogKeys = [
  "HarnessService.ListMcpResources",
  "HarnessService.ReadMcpResource",
  "HarnessService.ListMcpPrompts",
  "HarnessService.GetMcpPrompt",
  "HarnessService.ListMcpSources",
  "HarnessService.RefreshMcpSources",
  "HarnessService.ListToolHiveGroups",
  "HarnessService.ListAgents",
  "HarnessService.ListCommands",
  "HarnessService.ListWorktrees",
  "HarnessService.ListModels",
] as const;
const generatedSignatureTypes = new Set(
  batchACatalogKeys.flatMap((key) => {
    const descriptor = RPC_CATALOG[key].grpc.descriptor;
    return [
      descriptor.input.typeName.split(".").at(-1),
      descriptor.output.typeName.split(".").at(-1),
    ];
  }),
);

const expectedSignatures = {
  Agents: [
    "list(request: ListAgentsRequest, options?: RequestOptions): Promise<ListAgentsResponse>;",
  ],
  Commands: [
    "list(request: ListCommandsRequest, options?: RequestOptions): Promise<ListCommandsResponse>;",
  ],
  McpInventory: [
    "getPrompt(request: GetMcpPromptRequest, options?: RequestOptions): Promise<GetMcpPromptResponse>;",
    "listPrompts(request: ListMcpPromptsRequest, options?: RequestOptions): Promise<ListMcpPromptsResponse>;",
    "listResources(request: ListMcpResourcesRequest, options?: RequestOptions): Promise<ListMcpResourcesResponse>;",
    "listSources(request: ListMcpSourcesRequest, options?: RequestOptions): Promise<ListMcpSourcesResponse>;",
    "refresh(request: RefreshMcpSourcesRequest, options?: RequestOptions): Promise<RefreshMcpSourcesResponse>;",
    "listToolHiveGroups(request: ListToolHiveGroupsRequest, options?: RequestOptions): Promise<ListToolHiveGroupsResponse>;",
    "readResource(request: ReadMcpResourceRequest, options?: RequestOptions): Promise<ReadMcpResourceResponse>;",
  ],
  Models: [
    "list(request: ListModelsRequest, options?: RequestOptions): Promise<ListModelsResponse>;",
  ],
  Worktrees: [
    "list(request: ListWorktreesRequest, options?: RequestOptions): Promise<ListWorktreesResponse>;",
  ],
} as const;

function interfaceMethodSignatures(report: string, name: string): string[] {
  const body = new RegExp(`export interface ${name} \\{([\\s\\S]*?)\\n\\}`, "u").exec(report)?.[1];
  if (body === undefined) throw new Error(`API report is missing ${name}`);
  return body
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => /^[a-z][A-Za-z0-9]*\(/u.test(line));
}

function codedError(code: string): ConnectError {
  const bytes = (value: string): number[] => [...new TextEncoder().encode(value)];
  const field = (number: number, value: number[]): number[] => [
    (number << 3) | 2,
    value.length,
    ...value,
  ];
  const error = new ConnectError(code, Code.PermissionDenied);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([...field(1, bytes(code)), ...field(2, bytes("mecatl.stacklok.com"))]),
  });
  return error;
}

type RecordedCall = {
  readonly header: Headers;
  readonly inputType: string;
  readonly method: string;
  readonly signal: AbortSignal | undefined;
  readonly timeoutMs: number | undefined;
};

class NamespaceTransport implements Transport {
  readonly calls: RecordedCall[] = [];
  fail = false;

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: MessageInitShape<I>,
    _contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    const inputType = (input as { $typeName?: string }).$typeName ?? "";
    this.calls.push({
      header: new Headers(header),
      inputType,
      method: method.name,
      signal,
      timeoutMs,
    });
    if (this.fail && method.name !== "GetCompatibilityInfo") {
      throw codedError("management_unauthorized");
    }
    return {
      header: new Headers(),
      message: create(
        method.output,
        (method.name === "GetCompatibilityInfo"
          ? { apiMajor: 1, capabilities: {}, features: ["server_info"] }
          : {}) as MessageInitShape<O>,
      ),
      method,
      service: method.parent,
      stream: false,
      trailer: new Headers(),
    };
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    _method: DescMethodStreaming<I, O>,
    _signal: AbortSignal | undefined,
    _timeoutMs: number | undefined,
    _header: HeadersInit | undefined,
    _input: AsyncIterable<MessageInitShape<I>>,
    _contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    throw new Error("core inventory namespaces are unary");
  }
}

describe("core typed namespaces", () => {
  it("core inventory namespaces are thin generated-type wrappers", async () => {
    for (const reportName of apiReports) {
      const report = readFileSync(`${packageRoot}/etc/${reportName}`, "utf8");

      for (const [namespace, signatures] of Object.entries(expectedSignatures)) {
        const actual = interfaceMethodSignatures(report, namespace).sort();
        expect(actual, `${reportName}:${namespace}`).toEqual([...signatures].sort());

        for (const signature of actual) {
          const typeNames = signature.match(/\b[A-Z][A-Za-z0-9]*\b/gu) ?? [];
          const disallowed = typeNames.filter(
            (name) => !generatedSignatureTypes.has(name) && !signatureAllowlist.has(name),
          );
          expect(disallowed, `${reportName}:${namespace}.${signature}`).toEqual([]);
        }
      }
    }

    const transport = new NamespaceTransport();
    const client = connect({ transport });
    const calls = [
      () => client.mcp.listResources(create(ListMcpResourcesRequestSchema, { server: "git" })),
      () =>
        client.mcp.readResource(
          create(ReadMcpResourceRequestSchema, { server: "git", uri: "file://README.md" }),
        ),
      () => client.mcp.listPrompts(create(ListMcpPromptsRequestSchema, { server: "git" })),
      () =>
        client.mcp.getPrompt(
          create(GetMcpPromptRequestSchema, {
            arguments: { depth: "short" },
            name: "review",
            server: "git",
          }),
        ),
      () => client.mcp.listSources(create(ListMcpSourcesRequestSchema)),
      () => client.mcp.refresh(create(RefreshMcpSourcesRequestSchema, { sessionId: "session-1" })),
      () => client.mcp.listToolHiveGroups(create(ListToolHiveGroupsRequestSchema)),
      () => client.agents.list(create(ListAgentsRequestSchema)),
      () => client.commands.list(create(ListCommandsRequestSchema, { sessionId: "session-1" })),
      () => client.worktrees.list(create(ListWorktreesRequestSchema, { sessionId: "session-1" })),
      () => client.models.list(create(ListModelsRequestSchema)),
    ];
    for (const call of calls) await call();

    const expected = [
      "ListMcpResources",
      "ReadMcpResource",
      "ListMcpPrompts",
      "GetMcpPrompt",
      "ListMcpSources",
      "RefreshMcpSources",
      "ListToolHiveGroups",
      "ListAgents",
      "ListCommands",
      "ListWorktrees",
      "ListModels",
    ];
    const inventoryCalls = transport.calls.filter(
      ({ method }) => method !== "GetCompatibilityInfo",
    );
    expect(inventoryCalls.map(({ method }) => method)).toEqual(expected);
    expect(inventoryCalls.map(({ inputType }) => inputType)).toEqual(
      batchACatalogKeys.map((key) => RPC_CATALOG[key].grpc.descriptor.input.typeName),
    );
    await client.close();
  });

  it("thin namespace methods preserve request options and typed errors", async () => {
    const transport = new NamespaceTransport();
    const grpc = connect({ transport });
    const abort = new AbortController();
    await grpc.agents.list(create(ListAgentsRequestSchema), {
      headers: { "x-request": "kept" },
      signal: abort.signal,
      timeoutMs: 4_321,
    });
    const grpcCall = transport.calls.find(({ method }) => method === "ListAgents");
    expect(grpcCall).toMatchObject({ method: "ListAgents", timeoutMs: 4_321 });
    expect(grpcCall?.header.get("x-request")).toBe("kept");
    abort.abort();
    expect(grpcCall?.signal?.aborted).toBe(true);

    transport.fail = true;
    await expect(grpc.models.list(create(ListModelsRequestSchema))).rejects.toMatchObject({
      code: "management_unauthorized",
      transport: "grpc",
    });
    await grpc.close();

    const requests: RequestInit[] = [];
    const httpAbort = new AbortController();
    const http = connect({
      baseUrl: "http://mecatl.test",
      credentialProvider: () => ({ authorization: "Bearer dynamic" }),
      credentials: "include",
      fetch: async (input, init = {}) => {
        const path = new URL(String(input)).pathname;
        if (path === "/v1/compatibility")
          return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
        requests.push(init);
        if (path === "/v1/models") return Response.json({ models: [], provider_status: [] });
        return Response.json(
          { code: "management_unauthorized", detail: "denied" },
          { headers: { "content-type": "application/problem+json" }, status: 403 },
        );
      },
      headers: { "x-static": "static" },
    });
    await http.models.list(create(ListModelsRequestSchema), {
      headers: { "x-request": "request" },
      signal: httpAbort.signal,
      timeoutMs: 3_210,
    });
    const httpRequest = requests[0];
    const httpHeaders = new Headers(httpRequest?.headers);
    expect(httpHeaders.get("authorization")).toBe("Bearer dynamic");
    expect(httpHeaders.get("x-request")).toBe("request");
    expect(httpHeaders.get("x-static")).toBe("static");
    expect(httpRequest?.credentials).toBe("include");
    httpAbort.abort();
    expect(httpRequest?.signal?.aborted).toBe(true);

    await expect(http.mcp.listSources(create(ListMcpSourcesRequestSchema))).rejects.toMatchObject({
      code: "management_unauthorized",
      transport: "http",
    });
    await http.close();
  });
});
