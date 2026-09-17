/**
 * The daemon's MCP inventory over the SDK's `client.mcp` namespace
 * (`McpInventory`) — the data behind mecatui's `/mcp` panel
 * (cmd/mecatui/ui/mcp.go): the resolved SOURCES (static endpoints and
 * ToolHive discovery, each with the servers it contributed and the per-server
 * skip reasons) and the distinct ToolHive GROUPS.
 *
 * Two capability bits gate two different inventories, and the TUI invariant
 * holds here too: `capabilities.mcp` admits these direct source/group reads;
 * `capabilities.mcp_connector_status` alone admits ONLY the per-session
 * broker connector inventory (`./enrollment` `fetchSessionConnectors`) and
 * must never trigger a source, resource or prompt RPC.
 *
 * The daemon's source inventory is a SNAPSHOT resolved at startup: a server
 * started later appears only after a manual refresh re-reads it.
 */

import type { McpConnectorStatus } from "@stacklok-oss/mecatl-sdk";
import { HarnessApiError } from "./errors";
import { getHarnessClient, harness } from "./sdk";

/** One resolved MCP server a source contributed (proto `McpServerInfo`). */
interface McpServerView {
  name: string;
  url: string;
  /** The wire transport, e.g. "streamable-http". */
  transport: string;
  /** The ToolHive group ("" for the static source). */
  group: string;
}

/** One resolved MCP source (proto `McpSource`). */
export interface McpSourceView {
  /** The source identity, e.g. "static" or "toolhive(default)". */
  name: string;
  /** The coarse kind: "static" | "toolhive" (kept open for newer daemons). */
  kind: string;
  /** Whether the source was active in the resolution. */
  enabled: boolean;
  /** The ToolHive group ("" for the static source). */
  group: string;
  servers: McpServerView[];
  /** This source's per-server skip reasons, verbatim from the daemon. */
  diagnostics: string[];
}

/**
 * Reads the resolved source inventory (`GET /v1/mcp/sources`). Every field
 * is normalised to a present value so the UI never branches on `undefined`.
 */
export async function listHarnessMcpSources(
  signal?: AbortSignal,
): Promise<McpSourceView[]> {
  const response = await harness(() =>
    getHarnessClient().mcp.listSources(
      { $typeName: "mecatl.v1.ListMcpSourcesRequest" },
      { signal },
    ),
  );
  return (response.sources ?? []).map((source) => ({
    name: source.name ?? "",
    kind: source.kind ?? "",
    enabled: source.enabled === true,
    group: source.group ?? "",
    servers: (source.servers ?? []).map((server) => ({
      name: server.name ?? "",
      url: server.url ?? "",
      transport: server.transport ?? "",
      group: server.group ?? "",
    })),
    diagnostics: (source.diagnostics ?? []).filter(
      (line) => typeof line === "string" && line.trim() !== "",
    ),
  }));
}

/**
 * Reads the distinct ToolHive groups (`GET /v1/mcp/toolhive/groups`),
 * blank entries dropped. Best-effort decoration next to the sources: a
 * caller renders "groups unavailable" on failure rather than hiding sources.
 */
export async function listHarnessToolHiveGroups(
  signal?: AbortSignal,
): Promise<string[]> {
  const response = await harness(() =>
    getHarnessClient().mcp.listToolHiveGroups(
      { $typeName: "mecatl.v1.ListToolHiveGroupsRequest" },
      { signal },
    ),
  );
  return (response.groups ?? []).filter(
    (group) => typeof group === "string" && group.trim() !== "",
  );
}

/**
 * True for the daemon's "no MCP provider is wired" refusal
 * (`no_mcp_provider`, 412): the deployment resolved no MCP integration at
 * all, so there is nothing to list — a plain notice, not an error.
 */
export function isNoMcpProvider(error: unknown): boolean {
  return error instanceof HarnessApiError && error.code === "no_mcp_provider";
}

/** The user-facing sentence for `isNoMcpProvider`. */
export const NO_MCP_PROVIDER_TEXT = "MCP tools aren't set up for this agent.";

// ── Broker connector labels (verbatim from cmd/mecatui/ui/mcp.go) ──────────

/**
 * The connector inventory's enrollment state as the TUI words it
 * (`brokerEnrollmentLabel`). An unknown word reads "Status unavailable" —
 * never a guess about connectivity.
 */
export function enrollmentLabel(state: string): string {
  switch (state) {
    case "not_required":
      return "No setup required";
    case "not_started":
      return "No active setup";
    case "pending":
      return "Setup in progress";
    case "completed":
      return "Catalogue ready";
    default:
      return "Status unavailable";
  }
}

/**
 * One connector's catalogue state as the TUI words it
 * (`brokerCatalogueLabel`). Catalogue status is what the broker has seen of
 * the connector's tool list; it is never a live connection check.
 */
export function catalogueLabel(state: string): string {
  switch (state) {
    case "hidden":
      return "Awaiting discovery";
    case "declared":
      return "Tools declared";
    case "discovered":
      return "Tools discovered";
    default:
      return "Status unavailable";
  }
}

/**
 * The tool-count cell for one connector: the number only once the
 * catalogue is declared or discovered, else an em dash (the TUI's rule —
 * a hidden catalogue's zero is not a fact about the connector).
 */
export function connectorToolCount(
  connector: Pick<McpConnectorStatus, "catalogueState" | "toolCount">,
): string {
  return connector.catalogueState === "declared" ||
    connector.catalogueState === "discovered"
    ? String(connector.toolCount)
    : "—";
}

/**
 * The availability line for a connector inventory whose `availability` is
 * not "available": the broker itself reported it unavailable, or the daemon
 * could not say. Both are wordings, not machine tokens.
 */
export function availabilityLabel(availability: string): string {
  return availability === "unavailable"
    ? "Broker state unavailable"
    : "Status unavailable";
}

// ── Prompts and resources (the composer's pickers) ─────────────────────────
//
// mecatui's f8 prompts picker and ctrl+r resources picker
// (cmd/mecatui/ui/mcp.go mcpPrompts/mcpPromptArgs/mcpResources/
// mcpResourcePrev) read these four RPCs; the rendered text is REVIEWED in the
// composer and sent by the user, never on the picker's behalf. Every read is
// admitted by `capabilities.mcp` only — the caller gates.

/** One argument an MCP prompt takes (proto `McpPromptArgument`). */
interface McpPromptArgumentView {
  name: string;
  title: string;
  description: string;
  required: boolean;
}

/** One MCP prompt a connected server exposes (proto `McpPrompt`). */
export interface McpPromptView {
  server: string;
  name: string;
  title: string;
  description: string;
  arguments: McpPromptArgumentView[];
}

/** One rendered, role-tagged prompt message (proto `McpPromptMessage`). */
export interface McpPromptMessageView {
  role: string;
  text: string;
}

/** A prompt expanded with its arguments (proto `GetMcpPromptResponse`). */
export interface McpRenderedPrompt {
  description: string;
  messages: McpPromptMessageView[];
}

/** One MCP resource a connected server exposes (proto `McpResource`). */
export interface McpResourceView {
  server: string;
  uri: string;
  name: string;
  title: string;
  description: string;
  mimeType: string;
  /** Bytes as the server reports them; 0 when unknown. */
  size: number;
  readOnly: boolean;
}

/** A binary chunk of a read resource — reported, never inserted. */
interface McpResourceBinaryChunk {
  mimeType: string;
  bytes: number;
}

/** A read resource, flattened for preview and insertion. */
export interface McpResourceContent {
  /** Every textual chunk's text, joined with "\n" (the TUI's `joinContents`). */
  text: string;
  binaryChunks: McpResourceBinaryChunk[];
}

/**
 * Lists the MCP prompts the connected servers expose (`GET /v1/mcp/prompts`),
 * scoped to one `server` when given. Every field is normalised to a present
 * value so the picker never branches on `undefined`.
 */
export async function listHarnessMcpPrompts(
  server = "",
  signal?: AbortSignal,
): Promise<McpPromptView[]> {
  const response = await harness(() =>
    getHarnessClient().mcp.listPrompts(
      { $typeName: "mecatl.v1.ListMcpPromptsRequest", server },
      { signal },
    ),
  );
  return (response.prompts ?? []).map((prompt) => ({
    server: prompt.server ?? "",
    name: prompt.name ?? "",
    title: prompt.title ?? "",
    description: prompt.description ?? "",
    arguments: (prompt.arguments ?? []).map((argument) => ({
      name: argument.name ?? "",
      title: argument.title ?? "",
      description: argument.description ?? "",
      required: argument.required === true,
    })),
  }));
}

/**
 * Expands one prompt with its arguments (`POST /v1/mcp/prompts/get`) into
 * the rendered messages the composer inserts.
 */
export async function getHarnessMcpPrompt(
  server: string,
  name: string,
  args: Record<string, string>,
  signal?: AbortSignal,
): Promise<McpRenderedPrompt> {
  const response = await harness(() =>
    getHarnessClient().mcp.getPrompt(
      {
        $typeName: "mecatl.v1.GetMcpPromptRequest",
        server,
        name,
        arguments: args,
      },
      { signal },
    ),
  );
  return {
    description: response.description ?? "",
    messages: (response.messages ?? []).map((message) => ({
      role: message.role ?? "",
      text: message.text ?? "",
    })),
  };
}

/**
 * Lists the MCP resources the connected servers expose
 * (`GET /v1/mcp/resources`), scoped to one `server` when given.
 */
export async function listHarnessMcpResources(
  server = "",
  signal?: AbortSignal,
): Promise<McpResourceView[]> {
  const response = await harness(() =>
    getHarnessClient().mcp.listResources(
      { $typeName: "mecatl.v1.ListMcpResourcesRequest", server },
      { signal },
    ),
  );
  return (response.resources ?? []).map((resource) => ({
    server: resource.server ?? "",
    uri: resource.uri ?? "",
    name: resource.name ?? "",
    title: resource.title ?? "",
    description: resource.description ?? "",
    mimeType: resource.mimeType ?? "",
    size: Number(resource.size ?? 0),
    readOnly: resource.readOnly === true,
  }));
}

/**
 * Reads one resource (`GET /v1/mcp/resources/read`). Textual chunks join
 * into `text`; a `blob` chunk is reported by type and size and never enters
 * the composer (the TUI summarises binaries the same way).
 */
export async function readHarnessMcpResource(
  server: string,
  uri: string,
  signal?: AbortSignal,
): Promise<McpResourceContent> {
  const response = await harness(() =>
    getHarnessClient().mcp.readResource(
      { $typeName: "mecatl.v1.ReadMcpResourceRequest", server, uri },
      { signal },
    ),
  );
  const texts: string[] = [];
  const binaryChunks: McpResourceBinaryChunk[] = [];
  for (const chunk of response.contents ?? []) {
    const blobBytes = chunk.blob?.byteLength ?? 0;
    if (blobBytes > 0) {
      binaryChunks.push({ mimeType: chunk.mimeType ?? "", bytes: blobBytes });
    } else {
      texts.push(chunk.text ?? "");
    }
  }
  return { text: texts.join("\n"), binaryChunks };
}

/**
 * The rendered prompt as the TUI drops it into the input
 * (`joinPromptMessages`): each message prefixed with its role when it has
 * one, messages separated by a blank line.
 */
export function renderPromptForComposer(
  messages: readonly McpPromptMessageView[],
): string {
  return messages
    .map((message) =>
      message.role ? `${message.role}: ${message.text}` : message.text,
    )
    .join("\n\n");
}

/** The most text one insertion may add to the composer (64 KiB of UTF-16). */
export const COMPOSER_INSERT_LIMIT = 64 * 1024;

/**
 * Clamps text bound for the composer to `COMPOSER_INSERT_LIMIT` characters,
 * ending a cut with a note that says how much was dropped — a resource or
 * rendered prompt is never inserted silently truncated.
 */
export function clampComposerInsert(
  text: string,
  limit: number = COMPOSER_INSERT_LIMIT,
): string {
  if (text.length <= limit) return text;
  const dropped = text.length - limit;
  return `${text.slice(0, limit)}\n[truncated: ${dropped.toLocaleString("en-US")} more characters not inserted]`;
}
