"use client";

import { Loader2, Plug, RotateCw } from "lucide-react";
import Link from "next/link";
import { useEffect } from "react";
import { McpSourcesList } from "@/components/mcp/mcp-sources-list";
import { Button } from "@/components/ui/button";
import { useMcpInventory } from "@/features/agent/hooks/use-mcp-inventory";
import { useSessionMcpConnectors } from "@/features/agent/hooks/use-session-mcp-connectors";
import type { WorkspaceEnrollmentView } from "@/features/agent/hooks/use-workspace-enrollment";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import type { SessionConnectors } from "@/lib/harness/enrollment";
import {
  availabilityLabel,
  catalogueLabel,
  connectorToolCount,
  enrollmentLabel,
} from "@/lib/harness/mcp";
import { SidePanel } from "./side-panel";

/**
 * The chat's MCP tools panel — mecatui's `/mcp` (ctrl+o) panel as a right-hand
 * side panel. Two sections, each behind its own capability bit, exactly as
 * the TUI separates them:
 *
 * - `mcp_connector_status`: this chat's BROKER connector inventory — the
 *   enrollment state in the TUI's words, each connector's catalogue state
 *   and tool count, the truncation notice, and (behind
 *   `workspace_enrollment`) the whole-bundle "Connect tools" / "Cancel
 *   setup" actions, driven by the chat's existing `useWorkspaceEnrollment`
 *   (which owns the consent window and the 3 s observe poll). Catalogue
 *   status is never presented as a live connection check.
 * - `mcp`: the daemon's resolved SOURCES and ToolHive groups with a manual
 *   refresh (`McpSourcesList`), plus a link to the Settings page.
 *
 * A daemon granting only `mcp_connector_status` never receives a source,
 * resource or prompt RPC from this panel (the TUI invariant).
 */

export const MCP_PANEL_TITLE = "MCP tools";
export const MCP_PANEL_NO_INVENTORY_TEXT =
  "No tools are connected to this agent.";
export const MCP_PANEL_NO_SESSION_TEXT =
  "Start a chat to see the agent's connected tools.";
export const MCP_CATALOGUE_CAPTION =
  "Catalogue status · not a live connection check";
export const MCP_CONNECTORS_TRUNCATED_TEXT = "Connector list truncated.";

/** True when the daemon serves anything this panel can show. */
export function mcpPanelAvailable(
  serverCapabilities: Record<string, unknown>,
): boolean {
  return (
    serverCapabilities.mcp === true ||
    serverCapabilities.mcp_connector_status === true
  );
}

// ── Open requests from outside the chat view ────────────────────────────────

/**
 * The DOM event a keyboard shortcut or built-in dispatches to open this panel
 * (the TUI's ctrl+o / `/mcp`); `ChatView` listens through
 * `useOpenMcpPanelRequests`. A window event keeps the shortcut registry
 * decoupled from the chat view's panel state.
 */
export const OPEN_MCP_PANEL_EVENT = "mecatl-studio:open-mcp-panel";

export function requestOpenMcpPanel(): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent(OPEN_MCP_PANEL_EVENT));
}

/** Runs `handler` on every open request while mounted. */
export function useOpenMcpPanelRequests(handler: () => void): void {
  useEffect(() => {
    const listener = () => handler();
    window.addEventListener(OPEN_MCP_PANEL_EVENT, listener);
    return () => window.removeEventListener(OPEN_MCP_PANEL_EVENT, listener);
  }, [handler]);
}

// ── Broker section ──────────────────────────────────────────────────────────

/**
 * The enrollment line's label: the inventory's own word, overridden by what
 * this tab knows first-hand from its own connect/cancel (the TUI's
 * `renderBrokerMCPPanel` override order).
 */
export function brokerEnrollmentText(
  inventory: SessionConnectors,
  enrollment: WorkspaceEnrollmentView | null,
): string {
  if (enrollment?.phase === "pending") return "Setup in progress";
  if (enrollment?.phase === "connected") return "Catalogue ready";
  return enrollmentLabel(inventory.enrollmentState);
}

function BrokerSection({
  sessionId,
  enrollment,
  enrollmentActions,
}: {
  sessionId: string | null;
  enrollment: WorkspaceEnrollmentView | null;
  /** `serverCapabilities.workspace_enrollment`: offers connect/cancel. */
  enrollmentActions: boolean;
}) {
  const connectors = useSessionMcpConnectors(sessionId, {
    enabled: sessionId !== null,
    revision: enrollment?.phase,
  });

  let body: React.ReactNode;
  if (sessionId === null) {
    body = (
      <p className="text-sm text-muted-foreground">
        {MCP_PANEL_NO_SESSION_TEXT}
      </p>
    );
  } else if (connectors.isLoading) {
    body = (
      <p className="text-sm text-muted-foreground" role="status">
        Loading connectors…
      </p>
    );
  } else if (connectors.error) {
    body = (
      <p className="text-sm text-destructive break-words" role="alert">
        {connectors.error}
      </p>
    );
  } else if (connectors.inventory === null) {
    body = <p className="text-sm text-muted-foreground">Status unavailable</p>;
  } else if (connectors.inventory.availability !== "available") {
    body = (
      <p className="text-sm text-muted-foreground">
        {availabilityLabel(connectors.inventory.availability)}
      </p>
    );
  } else {
    const inventory = connectors.inventory;
    body = (
      <>
        <p className="text-sm" data-testid="mcp-enrollment">
          <span className="text-muted-foreground">Enrollment: </span>
          {brokerEnrollmentText(inventory, enrollment)}
        </p>
        {inventory.connectors.length > 0 && (
          <ul className="divide-y divide-border/60">
            {inventory.connectors.map((connector) => (
              <li
                key={connector.name}
                className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 py-2 text-xs first:pt-0 last:pb-0"
                data-testid="mcp-connector"
              >
                <span className="font-mono text-sm text-foreground">
                  {connector.name}
                </span>
                <span className="text-muted-foreground">
                  {catalogueLabel(connector.catalogueState)}
                </span>
                <span className="ml-auto tabular-nums text-muted-foreground">
                  {connectorToolCount(connector)} tools
                </span>
              </li>
            ))}
          </ul>
        )}
        {inventory.truncated && (
          <p className="text-xs text-muted-foreground">
            {MCP_CONNECTORS_TRUNCATED_TEXT}
          </p>
        )}
      </>
    );
  }

  // The TUI's `canConnect`: an eligible idle chat with no setup of its own
  // in flight may connect (or re-observe) regardless of the catalogue state;
  // cancel needs the pending enrollment this tab knows the id of.
  const eligible = Boolean(
    enrollmentActions && enrollment?.supported && sessionId,
  );
  const blocked = !enrollment || enrollment.busy || !enrollment.idle;
  const canConnect = eligible && !blocked && enrollment?.phase !== "pending";
  const canCancel =
    eligible &&
    !blocked &&
    enrollment?.phase === "pending" &&
    enrollment.enrollmentId !== "";
  const hint =
    enrollment && !enrollment.idle
      ? "Wait for the current response to finish."
      : undefined;

  return (
    <section
      className="flex flex-col gap-3"
      aria-labelledby="mcp-panel-connectors"
    >
      <div className="flex items-center justify-between gap-2">
        <h4
          id="mcp-panel-connectors"
          className="text-xs font-semibold tracking-wide text-muted-foreground uppercase"
        >
          Connectors
        </h4>
        {sessionId !== null && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 shrink-0 gap-1 px-2 text-muted-foreground"
            disabled={connectors.refreshing || connectors.isLoading}
            onClick={() => void connectors.refresh()}
            aria-label="Refresh connectors"
          >
            {connectors.refreshing ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <RotateCw className="size-3.5" aria-hidden="true" />
            )}
            <span className="text-xs">Refresh</span>
          </Button>
        )}
      </div>
      {body}
      <p className="text-xs text-muted-foreground">{MCP_CATALOGUE_CAPTION}</p>
      {eligible && (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-7"
            disabled={!canConnect}
            title={hint}
            onClick={enrollment?.connect}
          >
            {enrollment?.busy && enrollment.phase !== "pending"
              ? "Connecting…"
              : "Connect tools"}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-7"
            disabled={!canCancel}
            title={hint}
            onClick={enrollment?.cancel}
          >
            {enrollment?.busy && enrollment.phase === "pending"
              ? "Cancelling…"
              : "Cancel setup"}
          </Button>
        </div>
      )}
      {enrollment?.error && (
        <p className="text-xs text-destructive break-words">
          {enrollment.error}
        </p>
      )}
    </section>
  );
}

// ── Panel ───────────────────────────────────────────────────────────────────

export function McpPanel({
  sessionId,
  enrollment,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  /** The chat's daemon session id; null on the mock tour (no daemon chat). */
  sessionId: string | null;
  /** The chat's enrollment controller; null on the mock tour. */
  enrollment: WorkspaceEnrollmentView | null;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  const { connected, serverCapabilities } = useRuntimeStatus();
  const mcp = connected && serverCapabilities.mcp === true;
  const broker = connected && serverCapabilities.mcp_connector_status === true;
  const enrollmentActions = serverCapabilities.workspace_enrollment === true;
  const inventory = useMcpInventory({ enabled: mcp });

  return (
    <SidePanel
      icon={Plug}
      title={MCP_PANEL_TITLE}
      closeLabel="Close MCP tools"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      windowControls={windowControls}
    >
      <div
        className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-4 py-3"
        data-testid="mcp-panel"
      >
        {!connected ? (
          <p className="text-sm text-muted-foreground">
            The daemon is offline — its MCP inventory cannot be read right now.
          </p>
        ) : !mcp && !broker ? (
          <p className="text-sm text-muted-foreground">
            {MCP_PANEL_NO_INVENTORY_TEXT}
          </p>
        ) : null}
        {broker && (
          <BrokerSection
            sessionId={sessionId}
            enrollment={enrollment}
            enrollmentActions={enrollmentActions}
          />
        )}
        {mcp && (
          <section
            className="flex flex-col gap-3"
            aria-labelledby="mcp-panel-sources"
          >
            <h4
              id="mcp-panel-sources"
              className="text-xs font-semibold tracking-wide text-muted-foreground uppercase"
            >
              Sources
            </h4>
            <McpSourcesList view={inventory} />
            <Link
              href="/workspace/settings/gateway"
              className="text-xs text-brand underline-offset-4 hover:underline"
            >
              Configure in Settings → MCP tools
            </Link>
          </section>
        )}
      </div>
    </SidePanel>
  );
}
