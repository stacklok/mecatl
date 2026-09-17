/**
 * The daemon's resolved MCP source inventory (GET /v1/mcp/sources): the
 * server-global MCP servers every source contributed, by configured name.
 *
 * Read by the "Debug with AI" dialog (ADR 0254) to offer the servers a debug
 * session may borrow (`debug_mcp_servers` names exactly these configured
 * names). Studio never guesses the list from its own controller state — in
 * managed mode the controller's gateway is one such server, but an operator
 * can configure others in the daemon's own settings, and in external mode
 * Studio knows none — so the daemon's inventory is the single source.
 */

import { getHarnessClient, harness } from "./sdk";

/**
 * The configured names of the MCP servers the daemon resolved from its
 * ENABLED sources, deduplicated and sorted. Empty when no server is
 * configured. Throws the typed harness error when the daemon cannot answer
 * (an older daemon without the route, a refused read) — callers fall back
 * to letting the operator type names.
 */
export async function fetchHarnessMcpServerNames(
  signal?: AbortSignal,
): Promise<string[]> {
  const response = await harness(() =>
    getHarnessClient().mcp.listSources(
      { $typeName: "mecatl.v1.ListMcpSourcesRequest" },
      { signal },
    ),
  );
  const names = new Set<string>();
  for (const source of response.sources ?? []) {
    if (source.enabled === false) continue;
    for (const server of source.servers ?? []) {
      const name = (server.name ?? "").trim();
      if (name) names.add(name);
    }
  }
  return [...names].sort((a, b) => a.localeCompare(b));
}
