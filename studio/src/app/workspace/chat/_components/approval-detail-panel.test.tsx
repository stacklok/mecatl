import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ApprovalRequest } from "@/features/agent";
import { memoryStorage } from "@/test/memory-storage";
import { ApprovalDetailPanel } from "./approval-detail-panel";

const shellAsk: ApprovalRequest = {
  approvalId: "s1:1:c1:r1",
  sessionId: "s1",
  toolName: "Shell",
  description: "Shell needs your approval.",
  reason: "runs a command",
  args: JSON.stringify({ command: "go test ./...", timeout_ms: 5000 }),
  details: "runs a command\n\ncommand: go test ./... · timeout_ms: 5000",
};

function renderPanel(
  approval: ApprovalRequest = shellAsk,
  handlers: Partial<{
    onRespond: (choice: string) => void;
    onClose: () => void;
  }> = {},
) {
  const onRespond = vi.fn(handlers.onRespond);
  const onClose = vi.fn(handlers.onClose);
  render(
    <ApprovalDetailPanel
      approval={approval}
      onRespond={onRespond}
      onClose={onClose}
      maximized={false}
      onToggleMaximize={() => {}}
    />,
  );
  return { onRespond, onClose };
}

// The panel frame (SidePanel) persists its width in localStorage; this vitest
// environment's storage shim is method-less, so a real in-memory Storage is
// stubbed per test (the global afterEach unstubs it).
beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

describe("ApprovalDetailPanel", () => {
  it("titles the panel by tool, shows the reason and the decoded command with a raw toggle", () => {
    renderPanel();
    expect(screen.getByText("Shell — permission ask")).toBeInTheDocument();
    expect(screen.getByText("Shell needs your approval.")).toBeInTheDocument();
    expect(screen.getByText("runs a command")).toBeInTheDocument();
    expect(screen.getByText("go test ./...")).toBeInTheDocument();
    expect(screen.getByText("timeout 5s")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Raw" }));
    expect(screen.getByText(shellAsk.args as string)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Close permission details" }),
    ).toBeInTheDocument();
  });

  it("answers the ask from the pinned bar and closes the panel", () => {
    const { onRespond, onClose } = renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Always allow" }));
    expect(onRespond).toHaveBeenCalledWith("always");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("withholds Always allow for a child ask and names the subagent", () => {
    const { onRespond } = renderPanel({
      ...shellAsk,
      approvalId: "subagent-x:1:c1:r1",
      child: true,
    });
    expect(
      screen.getByText("A subagent's Shell needs your approval."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Always allow" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    expect(onRespond).toHaveBeenCalledWith("deny");
  });

  it("closes without a verdict from the header close control", () => {
    const { onRespond, onClose } = renderPanel();
    fireEvent.click(
      screen.getByRole("button", { name: "Close permission details" }),
    );
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onRespond).not.toHaveBeenCalled();
  });

  it("withholds Always allow for a debugger MCP ask in a debug session and shows the one-call note", () => {
    const debugMcpAsk: ApprovalRequest = {
      ...shellAsk,
      approvalId: "dbg-1:1:c1:r1",
      sessionId: "dbg-1",
      toolName: "mcp__github__create_issue",
      description: "mcp__github__create_issue needs your approval.",
      reason: "debug MCP call requires fresh current operator approval",
      args: JSON.stringify({ title: "Flaky scheduler test" }),
      details: "",
    };
    const onRespond = vi.fn();
    const onClose = vi.fn();
    render(
      <ApprovalDetailPanel
        approval={debugMcpAsk}
        onRespond={onRespond}
        onClose={onClose}
        maximized={false}
        onToggleMaximize={() => {}}
        debugSession
      />,
    );
    expect(screen.getByTestId("debug-mcp-ask-note")).toHaveTextContent(
      "one call at a time",
    );
    expect(screen.queryByRole("button", { name: "Always allow" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    expect(onRespond).toHaveBeenCalledWith("once");
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

/**
 * An MCP ask is titled with the TUI's humanized `Server · Tool`; the
 * destructive tint is judged on both the raw id and the title, so an MCP
 * delete tool still paints Allow once red (the inline card does the same).
 */
describe("ApprovalDetailPanel MCP tool titles", () => {
  const mcpAsk = (toolName: string): ApprovalRequest => ({
    ...shellAsk,
    toolName,
    description: `${toolName} needs your approval.`,
    reason: "",
    args: JSON.stringify({ owner: "stacklok" }),
    details: "",
  });

  it("titles an MCP delete ask Server · Tool and keeps the destructive tint", () => {
    renderPanel(mcpAsk("mcp__github__delete_branch"));
    expect(
      screen.getByText("GitHub · Delete branch — permission ask"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow once" })).toHaveClass(
      "bg-destructive",
    );
  });

  it("keeps a non-destructive MCP ask amber", () => {
    renderPanel(mcpAsk("mcp__github__issue_write"));
    expect(
      screen.getByText("GitHub · Issue write — permission ask"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow once" })).toHaveClass(
      "bg-warning",
    );
  });
});
