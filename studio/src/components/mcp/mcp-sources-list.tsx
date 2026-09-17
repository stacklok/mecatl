"use client";

import { Loader2, RotateCw, TriangleAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { McpInventoryView } from "@/features/agent/hooks/use-mcp-inventory";
import type { McpSourceView } from "@/lib/harness/mcp";
import { cn } from "@/lib/utils";

/**
 * The daemon's resolved MCP source inventory as a list — mecatui's `/mcp`
 * panel body (`renderMCPPanel`): each source with its kind, enabled state
 * and ToolHive group, the servers it contributed (name · transport · URL),
 * its per-server skip reasons, the best-effort ToolHive groups line, and
 * the footer that reads the startup-snapshot caveat until a manual refresh
 * lands ("updated"). Shared by Settings → MCP tools and the chat's MCP
 * tools panel so both surfaces say the same thing.
 */

const MCP_SOURCES_EMPTY_TEXT = "No MCP sources are configured.";
const MCP_SNAPSHOT_FOOTER =
  "Snapshot from daemon startup — servers started later won't appear until you refresh.";
const MCP_UPDATED_FOOTER = "Updated — live MCP source status.";
const MCP_REFRESHING_FOOTER = "Refreshing…";

/** The footer sentence for the current refresh state (the TUI's `mcpPanelFooter`). */
function sourcesFooterText(
  view: Pick<McpInventoryView, "refreshing" | "refreshed">,
): string {
  if (view.refreshing) return MCP_REFRESHING_FOOTER;
  if (view.refreshed) return MCP_UPDATED_FOOTER;
  return MCP_SNAPSHOT_FOOTER;
}

/** The ToolHive groups line (the TUI's `renderGroupsLine`), "" until loaded. */
function groupsLineText(
  view: Pick<McpInventoryView, "groups" | "groupsError" | "groupsLoaded">,
): string {
  if (!view.groupsLoaded) return "";
  if (view.groupsError) return "ToolHive groups: unavailable";
  if (view.groups.length === 0) return "ToolHive groups: none";
  return `ToolHive groups: ${view.groups.join(", ")}`;
}

function SourceRow({ source }: { source: McpSourceView }) {
  return (
    <li className="py-3 first:pt-0 last:pb-0" data-testid="mcp-source">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium break-all">{source.name}</span>
        {source.kind && <Badge variant="outline">{source.kind}</Badge>}
        <Badge variant={source.enabled ? "success" : "muted"}>
          {source.enabled ? "enabled" : "disabled"}
        </Badge>
        {source.group && <Badge variant="info">group {source.group}</Badge>}
      </div>
      {source.servers.length > 0 && (
        <ul className="mt-2 flex flex-col gap-1.5">
          {source.servers.map((server) => (
            <li
              key={`${server.name}:${server.url}`}
              className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs"
              data-testid="mcp-server"
            >
              <span className="font-mono text-foreground">{server.name}</span>
              {server.transport && (
                <span className="text-muted-foreground">
                  {server.transport}
                </span>
              )}
              {server.url && (
                <code className="min-w-0 break-all font-mono text-muted-foreground">
                  {server.url}
                </code>
              )}
              {server.group && !source.group && (
                <span className="text-muted-foreground">
                  group {server.group}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      {source.diagnostics.length > 0 && (
        <ul className="mt-2 flex flex-col gap-1">
          {source.diagnostics.map((line) => (
            <li
              key={line}
              className="flex items-start gap-1.5 text-xs text-amber-800 dark:text-amber-400 break-words"
              data-testid="mcp-diagnostic"
            >
              <TriangleAlert
                className="mt-0.5 size-3 shrink-0"
                aria-hidden="true"
              />
              <span>{line}</span>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

export function McpSourcesList({
  view,
  className,
}: {
  view: McpInventoryView;
  className?: string;
}) {
  const groupsLine = groupsLineText(view);
  return (
    <div className={cn("flex flex-col gap-3", className)}>
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground" data-testid="mcp-footer">
          {sourcesFooterText(view)}
        </p>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7 shrink-0 gap-1 px-2 text-muted-foreground"
          disabled={view.refreshing}
          onClick={() => void view.refresh()}
          aria-label="Refresh MCP sources"
        >
          {view.refreshing ? (
            <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <RotateCw className="size-3.5" aria-hidden="true" />
          )}
          <span className="text-xs">Refresh</span>
        </Button>
      </div>
      {view.isLoading ? (
        <p className="text-sm text-muted-foreground" role="status">
          Loading MCP sources…
        </p>
      ) : view.error ? (
        <p className="text-sm text-destructive break-words" role="alert">
          {view.error}
        </p>
      ) : view.sources.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {MCP_SOURCES_EMPTY_TEXT}
        </p>
      ) : (
        <ul className="divide-y divide-border/60">
          {view.sources.map((source) => (
            <SourceRow key={source.name} source={source} />
          ))}
        </ul>
      )}
      {groupsLine && (
        <p className="text-xs text-muted-foreground" data-testid="mcp-groups">
          {groupsLine}
        </p>
      )}
    </div>
  );
}
