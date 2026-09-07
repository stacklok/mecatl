import type { CallOptions } from "@connectrpc/connect";

import type {
  GetMcpPromptRequest,
  GetMcpPromptResponse,
  ListAgentsRequest,
  ListAgentsResponse,
  ListCommandsRequest,
  ListCommandsResponse,
  ListMcpPromptsRequest,
  ListMcpPromptsResponse,
  ListMcpResourcesRequest,
  ListMcpResourcesResponse,
  ListMcpSourcesRequest,
  ListMcpSourcesResponse,
  ListModelsRequest,
  ListModelsResponse,
  ListToolHiveGroupsRequest,
  ListToolHiveGroupsResponse,
  ListWorktreesRequest,
  ListWorktreesResponse,
  ReadMcpResourceRequest,
  ReadMcpResourceResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import type { RawClient } from "./raw.js";
import { RPC_CATALOG } from "./rpc-catalog.js";

/** Request controls shared by all thin typed namespaces. @public */
export type RequestOptions = CallOptions;

/** MCP resource, prompt, source, and ToolHive-group inventory operations. @public */
export interface McpInventory {
  listResources(
    request: ListMcpResourcesRequest,
    options?: RequestOptions,
  ): Promise<ListMcpResourcesResponse>;
  readResource(
    request: ReadMcpResourceRequest,
    options?: RequestOptions,
  ): Promise<ReadMcpResourceResponse>;
  listPrompts(
    request: ListMcpPromptsRequest,
    options?: RequestOptions,
  ): Promise<ListMcpPromptsResponse>;
  getPrompt(request: GetMcpPromptRequest, options?: RequestOptions): Promise<GetMcpPromptResponse>;
  listSources(
    request: ListMcpSourcesRequest,
    options?: RequestOptions,
  ): Promise<ListMcpSourcesResponse>;
  listToolHiveGroups(
    request: ListToolHiveGroupsRequest,
    options?: RequestOptions,
  ): Promise<ListToolHiveGroupsResponse>;
}

/** Resolved agent-definition inventory operations. @public */
export interface Agents {
  list(request: ListAgentsRequest, options?: RequestOptions): Promise<ListAgentsResponse>;
}

/** Session-scoped slash-command inventory operations. @public */
export interface Commands {
  list(request: ListCommandsRequest, options?: RequestOptions): Promise<ListCommandsResponse>;
}

/** Session-scoped worktree inventory operations. @public */
export interface Worktrees {
  list(request: ListWorktreesRequest, options?: RequestOptions): Promise<ListWorktreesResponse>;
}

/** Selectable model inventory operations. @public */
export interface Models {
  list(request: ListModelsRequest, options?: RequestOptions): Promise<ListModelsResponse>;
}

export interface CoreNamespaces {
  readonly agents: Agents;
  readonly commands: Commands;
  readonly mcp: McpInventory;
  readonly models: Models;
  readonly worktrees: Worktrees;
}

/** Internal construction seam that keeps every namespace on the client's raw operation path. */
export function createCoreNamespaces(operations: Pick<RawClient, "unary">): CoreNamespaces {
  return {
    agents: {
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListAgents"].grpc.descriptor,
          request,
          options,
        ),
    },
    commands: {
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListCommands"].grpc.descriptor,
          request,
          options,
        ),
    },
    mcp: {
      getPrompt: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.GetMcpPrompt"].grpc.descriptor,
          request,
          options,
        ),
      listPrompts: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListMcpPrompts"].grpc.descriptor,
          request,
          options,
        ),
      listResources: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListMcpResources"].grpc.descriptor,
          request,
          options,
        ),
      listSources: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListMcpSources"].grpc.descriptor,
          request,
          options,
        ),
      listToolHiveGroups: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListToolHiveGroups"].grpc.descriptor,
          request,
          options,
        ),
      readResource: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ReadMcpResource"].grpc.descriptor,
          request,
          options,
        ),
    },
    models: {
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListModels"].grpc.descriptor,
          request,
          options,
        ),
    },
    worktrees: {
      list: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.ListWorktrees"].grpc.descriptor,
          request,
          options,
        ),
    },
  };
}
