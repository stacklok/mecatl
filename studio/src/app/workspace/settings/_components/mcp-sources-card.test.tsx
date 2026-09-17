import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { McpInventoryView } from "@/features/agent/hooks/use-mcp-inventory";
import {
  MCP_BROKER_ONLY_TEXT,
  MCP_EMPTY_TEXT,
  MCP_NO_INVENTORY_TEXT,
  MCP_SNAPSHOT_TEXT,
  MCP_UPDATED_TEXT,
  McpSourcesCard,
} from "./mcp-sources-card";

/**
 * Settings → MCP tools: the agent's tools as a plain list. Pins that (1) the
 * card is gated on `capabilities.mcp` — a broker-only agent gets the pointer
 * note and NEVER the direct source read, one with neither says so, offline
 * reads nothing; (2) each source renders under a readable name with an
 * On/Off badge and its tool names, and the technical detail (kind,
 * transport, address, groups, verbatim skip reasons) stays out; (3) skipped
 * tools surface as a plain count; (4) the status line reads the refresh
 * hint until a refresh lands, and Refresh drives `refresh()`; (5) empty and
 * error states.
 */

const { runtime, inventory } = vi.hoisted(() => ({
  runtime: {
    connected: true,
    serverCapabilities: {} as Record<string, unknown>,
  },
  inventory: {
    enabledCalls: [] as boolean[],
    view: {} as McpInventoryView,
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

function view(overrides: Partial<McpInventoryView> = {}): McpInventoryView {
  return {
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
          {
            name: "slack",
            url: "http://127.0.0.1:1/slack",
            transport: "streamable-http",
            group: "",
          },
        ],
        diagnostics: [],
      },
      {
        name: "toolhive(default)",
        kind: "toolhive",
        enabled: false,
        group: "default",
        servers: [],
        diagnostics: ["fetch: skipped, unsupported transport stdio"],
      },
    ],
    groups: ["default", "research"],
    groupsError: false,
    groupsLoaded: true,
    isLoading: false,
    refreshing: false,
    refreshed: false,
    error: null,
    refresh: vi.fn(async () => undefined),
    ...overrides,
  };
}

beforeEach(() => {
  runtime.connected = true;
  runtime.serverCapabilities = { mcp: true };
  inventory.enabledCalls = [];
  inventory.view = view();
});

describe("McpSourcesCard", () => {
  it("lists each source under a plain name with its state and tool names only", () => {
    render(<McpSourcesCard />);

    expect(
      screen.getByRole("heading", { name: "MCP tools" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("The tools the agent can use right now."),
    ).toBeInTheDocument();

    const sources = screen.getAllByTestId("mcp-source");
    expect(sources).toHaveLength(2);
    expect(sources[0]).toHaveTextContent("Connected tools");
    expect(sources[0]).toHaveTextContent("On");
    expect(sources[0]).toHaveTextContent("github");
    expect(sources[0]).toHaveTextContent("slack");
    expect(sources[1]).toHaveTextContent("ToolHive");
    expect(sources[1]).toHaveTextContent("Off");
    expect(sources[1]).toHaveTextContent("One tool couldn't be connected.");

    // The technical detail stays out of the office user's view.
    expect(screen.queryByText("static")).toBeNull();
    expect(screen.queryByText("toolhive(default)")).toBeNull();
    expect(screen.queryByText("streamable-http")).toBeNull();
    expect(screen.queryByText("http://127.0.0.1:1/gh")).toBeNull();
    expect(screen.queryByText(/group default/)).toBeNull();
    expect(screen.queryByText(/ToolHive groups/)).toBeNull();
    expect(screen.queryByText(/unsupported transport/)).toBeNull();
    expect(inventory.enabledCalls.at(-1)).toBe(true);
  });

  it("counts several skipped tools in one plain sentence", () => {
    inventory.view = view({
      sources: [
        {
          name: "toolhive(default)",
          kind: "toolhive",
          enabled: true,
          group: "default",
          servers: [],
          diagnostics: ["a: skipped", "b: skipped", "c: skipped"],
        },
      ],
    });
    render(<McpSourcesCard />);
    expect(screen.getByTestId("mcp-skipped")).toHaveTextContent(
      "3 tools couldn't be connected.",
    );
  });

  it("reads the refresh hint until a refresh lands, and Refresh drives refresh()", () => {
    const { rerender } = render(<McpSourcesCard />);
    expect(screen.getByTestId("mcp-footer")).toHaveTextContent(
      MCP_SNAPSHOT_TEXT,
    );
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(inventory.view.refresh).toHaveBeenCalledTimes(1);

    inventory.view = view({ refreshing: true });
    rerender(<McpSourcesCard />);
    expect(screen.getByTestId("mcp-footer")).toHaveTextContent("Refreshing…");
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled();

    inventory.view = view({ refreshed: true });
    rerender(<McpSourcesCard />);
    expect(screen.getByTestId("mcp-footer")).toHaveTextContent(
      MCP_UPDATED_TEXT,
    );
  });

  it("renders the loading, empty and error states", () => {
    inventory.view = view({ sources: [], isLoading: true });
    const { rerender } = render(<McpSourcesCard />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading tools…");

    inventory.view = view({ sources: [] });
    rerender(<McpSourcesCard />);
    expect(screen.getByText(MCP_EMPTY_TEXT)).toBeInTheDocument();

    inventory.view = view({ sources: [], error: "Could not read the tools." });
    rerender(<McpSourcesCard />);
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Could not read the tools.",
    );
  });

  it("never reads sources on a broker-only agent, and points at the chat panel", () => {
    runtime.serverCapabilities = { mcp_connector_status: true };
    render(<McpSourcesCard />);
    expect(screen.getByText(MCP_BROKER_ONLY_TEXT)).toBeInTheDocument();
    expect(screen.queryByTestId("mcp-source")).toBeNull();
    expect(inventory.enabledCalls.every((enabled) => enabled === false)).toBe(
      true,
    );
  });

  it("says so plainly when the agent cannot list its tools at all", () => {
    runtime.serverCapabilities = {};
    render(<McpSourcesCard />);
    expect(screen.getByText(MCP_NO_INVENTORY_TEXT)).toBeInTheDocument();
    expect(inventory.enabledCalls.every((enabled) => enabled === false)).toBe(
      true,
    );
  });

  it("reads nothing and lists nothing while the agent is offline", () => {
    runtime.connected = false;
    render(<McpSourcesCard />);
    expect(screen.getByText(/offline/i)).toBeInTheDocument();
    expect(screen.queryByTestId("mcp-source")).toBeNull();
    expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull();
    expect(inventory.enabledCalls.every((enabled) => enabled === false)).toBe(
      true,
    );
  });
});
