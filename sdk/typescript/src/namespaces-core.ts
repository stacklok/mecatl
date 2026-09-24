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
  RefreshMcpSourcesRequest,
  RefreshMcpSourcesResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import type { RawClient } from "./raw.js";
import { RPC_CATALOG } from "./rpc-catalog.js";

/** Request controls shared by all thin typed namespaces. @public */
export type RequestOptions = CallOptions;

/** MCP resource, prompt, source, and ToolHive-group inventory operations. @public */
export interface McpInventory {
  /** Lists the MCP resources exposed by configured servers. */
  listResources(
    request: ListMcpResourcesRequest,
    options?: RequestOptions,
  ): Promise<ListMcpResourcesResponse>;
  /** Reads one MCP resource by URI. */
  readResource(
    request: ReadMcpResourceRequest,
    options?: RequestOptions,
  ): Promise<ReadMcpResourceResponse>;
  /** Lists the MCP prompts exposed by configured servers. */
  listPrompts(
    request: ListMcpPromptsRequest,
    options?: RequestOptions,
  ): Promise<ListMcpPromptsResponse>;
  /** Expands one MCP prompt into its rendered messages. */
  getPrompt(request: GetMcpPromptRequest, options?: RequestOptions): Promise<GetMcpPromptResponse>;
  /** Lists configured MCP sources and their diagnostics. */
  listSources(
    request: ListMcpSourcesRequest,
    options?: RequestOptions,
  ): Promise<ListMcpSourcesResponse>;
  /** Refreshes direct MCP sources for one eligible owned session. */
  refresh(
    request: RefreshMcpSourcesRequest,
    options?: RequestOptions,
  ): Promise<RefreshMcpSourcesResponse>;
  /** Lists ToolHive groups present in the resolved MCP inventory. */
  listToolHiveGroups(
    request: ListToolHiveGroupsRequest,
    options?: RequestOptions,
  ): Promise<ListToolHiveGroupsResponse>;
}

/** Resolved agent-definition inventory operations. @public */
export interface Agents {
  /** Lists the resolved agent definitions. */
  list(request: ListAgentsRequest, options?: RequestOptions): Promise<ListAgentsResponse>;
}

/** Session-scoped slash-command inventory operations. @public */
export interface Commands {
  /** Lists slash commands available to a session. */
  list(request: ListCommandsRequest, options?: RequestOptions): Promise<ListCommandsResponse>;
}

/** Session-scoped worktree inventory operations. @public */
export interface Worktrees {
  /** Lists worktrees eligible for a session fork or clear operation. */
  list(request: ListWorktreesRequest, options?: RequestOptions): Promise<ListWorktreesResponse>;
}

/** Selectable model inventory operations. @public */
export interface Models {
  /** Lists selectable providers and models. */
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
      refresh: (request, options) =>
        operations.unary(
          RPC_CATALOG["HarnessService.RefreshMcpSources"].grpc.descriptor,
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
