import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { McpInventoryView } from "@/features/agent/hooks/use-mcp-inventory";
import type { SessionMcpConnectorsView } from "@/features/agent/hooks/use-session-mcp-connectors";
import type { WorkspaceEnrollmentView } from "@/features/agent/hooks/use-workspace-enrollment";
import type { SessionConnectors } from "@/lib/harness/enrollment";
import { memoryStorage } from "@/test/memory-storage";
import {
  brokerEnrollmentText,
  MCP_CATALOGUE_CAPTION,
  MCP_CONNECTORS_TRUNCATED_TEXT,
  MCP_PANEL_NO_INVENTORY_TEXT,
  MCP_PANEL_NO_SESSION_TEXT,
  McpPanel,
  mcpPanelAvailable,
  OPEN_MCP_PANEL_EVENT,
  requestOpenMcpPanel,
  useOpenMcpPanelRequests,
} from "./mcp-panel";

/**
 * The chat's MCP tools panel (mecatui's `/mcp`). Pins that (1) the broker
 * section renders the enrollment state and each connector's catalogue label
 * and tool count in the TUI's words, plus the truncation notice and the
 * "not a live connection check" caption; (2) the whole-bundle Connect /
 * Cancel actions are gated on `workspace_enrollment` and on the chat's own
 * enrollment controller (idle, not busy, pending-only cancel); (3) the
 * capability gates: broker-only never reads sources, `mcp` renders the
 * sources section with its Settings link, neither says so, and the mock
 * tour (no daemon id) gets the "start a chat" line; (4) the availability
 * and unavailable-inspection wordings; (5) the open-request window event.
 */

const { runtime, inventory, connectors } = vi.hoisted(() => ({
  runtime: {
    connected: true,
    serverCapabilities: {} as Record<string, unknown>,
  },
  inventory: {
    enabledCalls: [] as boolean[],
    view: {} as McpInventoryView,
  },
  connectors: {
    calls: [] as Array<{ sessionId: string | null; enabled: boolean }>,
    view: {} as SessionMcpConnectorsView,
  },
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

vi.mock("@/features/agent/hooks/use-mcp-inventory", () => ({
  useMcpInventory: (options?: { enabled?: boolean }) => {
    inventory.enabledCalls.push(options?.enabled ?? true);
    return inventory.view;
  },
}));

vi.mock("@/features/agent/hooks/use-session-mcp-connectors", () => ({
  useSessionMcpConnectors: (
    sessionId: string | null,
    options?: { enabled?: boolean },
  ) => {
    connectors.calls.push({ sessionId, enabled: options?.enabled ?? true });
    return connectors.view;
  },
}));

const sourcesView: McpInventoryView = {
  sources: [
    {
      name: "static",
      kind: "static",
      enabled: true,
      group: "",
      servers: [
        {
          name: "github",
          url: "http://127.0.0.1:1/gh",
          transport: "streamable-http",
          group: "",
        },
      ],
      diagnostics: [],
    },
  ],
  groups: ["default"],
  groupsError: false,
  groupsLoaded: true,
  isLoading: false,
  refreshing: false,
  refreshed: false,
  error: null,
  refresh: vi.fn(async () => undefined),
};

function connectorInventory(
  overrides: Partial<SessionConnectors> = {},
): SessionConnectors {
  return {
    availability: "available",
    enrollmentState: "not_started",
    connectors: [
      { name: "github", toolCount: 12, catalogueState: "discovered" },
      { name: "jira", toolCount: 0, catalogueState: "hidden" },
    ],
    totalConnectors: 2,
    truncated: false,
    ...overrides,
  };
}

function connectorsView(
  overrides: Partial<SessionMcpConnectorsView> = {},
): SessionMcpConnectorsView {
  return {
    inventory: connectorInventory(),
    isLoading: false,
    refreshing: false,
    error: null,
    refresh: vi.fn(async () => undefined),
    ...overrides,
  };
}

function enrollmentView(
  overrides: Partial<WorkspaceEnrollmentView> = {},
): WorkspaceEnrollmentView {
  return {
    supported: true,
    phase: "not_connected",
    enrollmentId: "",
    requiredServices: 0,
    outcome: "",
    error: null,
    busy: false,
    idle: true,
    dismissed: false,
    window: "elsewhere",
    connect: vi.fn(),
    retry: vi.fn(),
    cancel: vi.fn(),
    dismiss: vi.fn(),
    reopenWindow: vi.fn(),
    ...overrides,
  };
}

const shared = {
  onClose: () => {},
  maximized: false,
  onToggleMaximize: () => {},
};

beforeEach(() => {
  // The panel frame (SidePanel) persists its width in localStorage.
  vi.stubGlobal("localStorage", memoryStorage());
  runtime.connected = true;
  runtime.serverCapabilities = {
    mcp: true,
    mcp_connector_status: true,
    workspace_enrollment: true,
  };
  inventory.enabledCalls = [];
  inventory.view = sourcesView;
  connectors.calls = [];
  connectors.view = connectorsView();
});

describe("McpPanel", () => {
  it("renders the broker connectors in the TUI's words with the caption", () => {
    render(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );

    expect(screen.getByTestId("mcp-enrollment")).toHaveTextContent(
      "Enrollment: No active setup",
    );
    const rows = screen.getAllByTestId("mcp-connector");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("github");
    expect(rows[0]).toHaveTextContent("Tools discovered");
    expect(rows[0]).toHaveTextContent("12 tools");
    expect(rows[1]).toHaveTextContent("jira");
    expect(rows[1]).toHaveTextContent("Awaiting discovery");
    expect(rows[1]).toHaveTextContent("— tools");
    expect(screen.getByText(MCP_CATALOGUE_CAPTION)).toBeInTheDocument();
    expect(screen.queryByText(MCP_CONNECTORS_TRUNCATED_TEXT)).toBeNull();
    expect(connectors.calls.at(-1)).toEqual({ sessionId: "s1", enabled: true });
    // The sources section and its Settings link share the panel.
    expect(screen.getByTestId("mcp-source")).toHaveTextContent("github");
    expect(
      screen.getByRole("link", { name: "Configure in Settings → MCP tools" }),
    ).toHaveAttribute("href", "/workspace/settings/gateway");
  });

  it("notes a truncated connector list", () => {
    connectors.view = connectorsView({
      inventory: connectorInventory({ truncated: true }),
    });
    render(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    expect(screen.getByText(MCP_CONNECTORS_TRUNCATED_TEXT)).toBeInTheDocument();
  });

  it("overrides the enrollment line with what this tab knows first-hand", () => {
    const inv = connectorInventory({ enrollmentState: "not_started" });
    expect(
      brokerEnrollmentText(inv, enrollmentView({ phase: "pending" })),
    ).toBe("Setup in progress");
    expect(
      brokerEnrollmentText(inv, enrollmentView({ phase: "connected" })),
    ).toBe("Catalogue ready");
    expect(brokerEnrollmentText(inv, null)).toBe("No active setup");
    expect(
      brokerEnrollmentText(
        connectorInventory({ enrollmentState: "not_required" }),
        enrollmentView({ phase: "not_required" }),
      ),
    ).toBe("No setup required");
  });

  it("offers Connect tools on an idle chat and Cancel setup only while pending", () => {
    const enrollment = enrollmentView();
    const { rerender } = render(
      <McpPanel sessionId="s1" enrollment={enrollment} {...shared} />,
    );
    const connect = screen.getByRole("button", { name: "Connect tools" });
    const cancel = screen.getByRole("button", { name: "Cancel setup" });
    expect(connect).toBeEnabled();
    expect(cancel).toBeDisabled();
    fireEvent.click(connect);
    expect(enrollment.connect).toHaveBeenCalledTimes(1);

    const pending = enrollmentView({ phase: "pending", enrollmentId: "e1" });
    rerender(<McpPanel sessionId="s1" enrollment={pending} {...shared} />);
    expect(
      screen.getByRole("button", { name: "Connect tools" }),
    ).toBeDisabled();
    const cancelPending = screen.getByRole("button", { name: "Cancel setup" });
    expect(cancelPending).toBeEnabled();
    fireEvent.click(cancelPending);
    expect(pending.cancel).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("mcp-enrollment")).toHaveTextContent(
      "Enrollment: Setup in progress",
    );
  });

  it("disables both actions while a run is in flight or a control is busy", () => {
    const running = enrollmentView({ idle: false });
    const { rerender } = render(
      <McpPanel sessionId="s1" enrollment={running} {...shared} />,
    );
    const connect = screen.getByRole("button", { name: "Connect tools" });
    expect(connect).toBeDisabled();
    expect(connect).toHaveAttribute(
      "title",
      "Wait for the current response to finish.",
    );

    rerender(
      <McpPanel
        sessionId="s1"
        enrollment={enrollmentView({ busy: true })}
        {...shared}
      />,
    );
    expect(screen.getByRole("button", { name: "Connecting…" })).toBeDisabled();
  });

  it("hides the setup actions without workspace_enrollment or an unsupported enrollment", () => {
    runtime.serverCapabilities = { mcp: true, mcp_connector_status: true };
    const { rerender } = render(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    expect(screen.queryByRole("button", { name: "Connect tools" })).toBeNull();

    runtime.serverCapabilities = {
      mcp: true,
      mcp_connector_status: true,
      workspace_enrollment: true,
    };
    rerender(
      <McpPanel
        sessionId="s1"
        enrollment={enrollmentView({ supported: false })}
        {...shared}
      />,
    );
    expect(screen.queryByRole("button", { name: "Connect tools" })).toBeNull();
    // The connectors themselves still render: inspection is its own bit.
    expect(screen.getAllByTestId("mcp-connector")).toHaveLength(2);
  });

  it("words an unavailable broker, an unknown availability and refused inspection", () => {
    connectors.view = connectorsView({
      inventory: connectorInventory({ availability: "unavailable" }),
    });
    const { rerender } = render(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    expect(screen.getByText("Broker state unavailable")).toBeInTheDocument();
    expect(screen.queryByTestId("mcp-connector")).toBeNull();

    connectors.view = connectorsView({
      inventory: connectorInventory({ availability: "odd" }),
    });
    rerender(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    expect(screen.getByText("Status unavailable")).toBeInTheDocument();

    connectors.view = connectorsView({ inventory: null });
    rerender(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    expect(screen.getByText("Status unavailable")).toBeInTheDocument();
    // The caption holds regardless — status is never a connection claim.
    expect(screen.getByText(MCP_CATALOGUE_CAPTION)).toBeInTheDocument();
  });

  it("refreshes the connectors on demand", () => {
    render(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Refresh connectors" }));
    expect(connectors.view.refresh).toHaveBeenCalledTimes(1);
  });

  it("never reads sources on a broker-only daemon", () => {
    runtime.serverCapabilities = {
      mcp_connector_status: true,
      workspace_enrollment: true,
    };
    render(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    expect(screen.getAllByTestId("mcp-connector")).toHaveLength(2);
    expect(screen.queryByTestId("mcp-source")).toBeNull();
    expect(inventory.enabledCalls.every((enabled) => enabled === false)).toBe(
      true,
    );
  });

  it("renders only the sources on a direct-MCP daemon, and says so with neither bit", () => {
    runtime.serverCapabilities = { mcp: true };
    const { rerender } = render(
      <McpPanel sessionId="s1" enrollment={null} {...shared} />,
    );
    expect(screen.getByTestId("mcp-source")).toBeInTheDocument();
    expect(screen.queryByTestId("mcp-enrollment")).toBeNull();
    expect(connectors.calls.every((call) => call.enabled === false)).toBe(true);

    runtime.serverCapabilities = {};
    rerender(<McpPanel sessionId="s1" enrollment={null} {...shared} />);
    expect(screen.getByText(MCP_PANEL_NO_INVENTORY_TEXT)).toBeInTheDocument();
    expect(screen.queryByTestId("mcp-source")).toBeNull();
  });

  it("tells a chat without a daemon session to start one, and never reads connectors for it", () => {
    render(<McpPanel sessionId={null} enrollment={null} {...shared} />);
    expect(screen.getByText(MCP_PANEL_NO_SESSION_TEXT)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Connect tools" })).toBeNull();
    expect(connectors.calls.every((call) => call.enabled === false)).toBe(true);
  });

  it("renders the offline note while the daemon is unreachable", () => {
    runtime.connected = false;
    render(
      <McpPanel sessionId="s1" enrollment={enrollmentView()} {...shared} />,
    );
    expect(
      screen.getByText("The daemon is offline", { exact: false }),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("mcp-enrollment")).toBeNull();
  });
});

describe("mcpPanelAvailable", () => {
  it("is true for either capability bit and false for neither", () => {
    expect(mcpPanelAvailable({ mcp: true })).toBe(true);
    expect(mcpPanelAvailable({ mcp_connector_status: true })).toBe(true);
    expect(mcpPanelAvailable({ mcp: false, posture: "auto" })).toBe(false);
    expect(mcpPanelAvailable({})).toBe(false);
  });
});

describe("open requests", () => {
  it("delivers requestOpenMcpPanel() to a mounted listener and stops on unmount", () => {
    const handler = vi.fn();
    function Listener() {
      useOpenMcpPanelRequests(handler);
      return null;
    }
    const { unmount } = render(<Listener />);
    requestOpenMcpPanel();
    expect(handler).toHaveBeenCalledTimes(1);
    unmount();
    window.dispatchEvent(new CustomEvent(OPEN_MCP_PANEL_EVENT));
    expect(handler).toHaveBeenCalledTimes(1);
  });
});
