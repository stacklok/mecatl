import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ApprovalRequest } from "@/features/agent";
import { ApprovalPanel } from "./approval-panel";

const mainAsk: ApprovalRequest = {
  approvalId: "s1:1:c1:r1",
  sessionId: "s1",
  toolName: "Bash",
  description: "Bash needs your approval.",
  details: "ls -la",
};

const childAsk: ApprovalRequest = {
  ...mainAsk,
  approvalId: "subagent-x:1:c1:r1",
  child: true,
};

describe("ApprovalPanel keyboard verdicts", () => {
  // The TUI's approval modal keys, on the inline card: focus lands on Allow
  // once when the ask arrives, ←/→ cycle the buttons, y/w/n answer, and the
  // buttons show their keys.
  const keyDown = (key: string) =>
    fireEvent.keyDown(document.activeElement ?? document.body, { key });

  it("focuses Allow once when a new ask lands", () => {
    render(<ApprovalPanel approval={mainAsk} onRespond={() => {}} />);
    expect(screen.getByRole("button", { name: "Allow once" })).toHaveFocus();
  });

  it("cycles → over Allow once, Always allow, Deny and wraps", () => {
    render(<ApprovalPanel approval={mainAsk} onRespond={() => {}} />);
    keyDown("ArrowRight");
    expect(screen.getByRole("button", { name: "Always allow" })).toHaveFocus();
    keyDown("ArrowRight");
    expect(screen.getByRole("button", { name: "Deny" })).toHaveFocus();
    keyDown("ArrowRight");
    expect(screen.getByRole("button", { name: "Allow once" })).toHaveFocus();
    keyDown("ArrowLeft");
    expect(screen.getByRole("button", { name: "Deny" })).toHaveFocus();
  });

  it("answers y → once, w → always, n → deny from the focused card", () => {
    const onRespond = vi.fn();
    render(<ApprovalPanel approval={mainAsk} onRespond={onRespond} />);
    keyDown("y");
    keyDown("w");
    keyDown("n");
    expect(onRespond.mock.calls.map(([choice]) => choice)).toEqual([
      "once",
      "always",
      "deny",
    ]);
  });

  it("ignores w on a child ask and renders no W hint", () => {
    const onRespond = vi.fn();
    render(<ApprovalPanel approval={childAsk} onRespond={onRespond} />);
    keyDown("w");
    expect(onRespond).not.toHaveBeenCalled();
    expect(
      screen.getAllByTestId("verdict-key").map((k) => k.textContent),
    ).toEqual(["Y", "N"]);
    keyDown("ArrowRight");
    expect(screen.getByRole("button", { name: "Deny" })).toHaveFocus();
  });

  it("shows the Y / W / N keycaps without changing the buttons' names", () => {
    render(<ApprovalPanel approval={mainAsk} onRespond={() => {}} />);
    expect(
      screen.getAllByTestId("verdict-key").map((k) => k.textContent),
    ).toEqual(["Y", "W", "N"]);
    expect(screen.getByRole("button", { name: "Allow once" })).toHaveAttribute(
      "aria-keyshortcuts",
      "y a",
    );
    expect(screen.getByRole("button", { name: "Deny" })).toHaveAttribute(
      "aria-keyshortcuts",
      "n d",
    );
  });
});

describe("ApprovalPanel", () => {
  it("shows the head's place in the queue when more than one ask is waiting", () => {
    render(
      <ApprovalPanel
        approval={mainAsk}
        onRespond={() => {}}
        queuePosition={{ index: 1, total: 2 }}
      />,
    );
    expect(screen.getByText("1 of 2")).toBeInTheDocument();
    expect(screen.getByLabelText("Request 1 of 2")).toBeInTheDocument();
  });

  it("shows no queue badge for a lone ask", () => {
    render(
      <ApprovalPanel
        approval={mainAsk}
        onRespond={() => {}}
        queuePosition={{ index: 1, total: 1 }}
      />,
    );
    expect(screen.queryByText(/\bof 1\b/)).toBeNull();
    expect(screen.queryByText("Subagent")).toBeNull();
  });

  it("offers all three verdicts for a main-agent ask and routes each to its choice", () => {
    const onRespond = vi.fn();
    render(<ApprovalPanel approval={mainAsk} onRespond={onRespond} />);
    expect(screen.getByText("Bash needs your approval.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    fireEvent.click(screen.getByRole("button", { name: "Always allow" }));
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    expect(onRespond.mock.calls.map(([choice]) => choice)).toEqual([
      "once",
      "always",
      "deny",
    ]);
  });

  it("withholds Always allow for a child ask and names the subagent", () => {
    const onRespond = vi.fn();
    render(<ApprovalPanel approval={childAsk} onRespond={onRespond} />);
    expect(
      screen.getByText("A subagent's Bash needs your approval."),
    ).toBeInTheDocument();
    expect(screen.getByText("Subagent")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Always allow" })).toBeNull();
    expect(screen.getByRole("button", { name: "Allow once" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Deny" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    expect(onRespond).toHaveBeenCalledWith("deny");
  });

  it("renders a legacy ask (no raw tier) as its joined details, verbatim", () => {
    render(<ApprovalPanel approval={mainAsk} onRespond={() => {}} />);
    expect(screen.getByText("ls -la")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Raw" })).toBeNull();
  });

  it("shows a Shell ask as the bare command with its timeout, not the flattened line", () => {
    const shellAsk: ApprovalRequest = {
      ...mainAsk,
      toolName: "Shell",
      description: "Shell needs your approval.",
      reason: "runs a command",
      args: JSON.stringify({ command: "go test ./...", timeout_ms: 120000 }),
      details: "runs a command\n\ncommand: go test ./... · timeout_ms: 120000",
    };
    render(<ApprovalPanel approval={shellAsk} onRespond={() => {}} />);
    expect(screen.getByText("runs a command")).toBeInTheDocument();
    expect(screen.getByText("go test ./...")).toBeInTheDocument();
    expect(screen.getByText("timeout 120s")).toBeInTheDocument();
    expect(screen.queryByText(/command: go test/)).toBeNull();
  });

  it("renders an Edit ask as a diff with + and - rows", () => {
    const editAsk: ApprovalRequest = {
      ...mainAsk,
      toolName: "Edit",
      description: "Edit needs your approval.",
      reason: "",
      args: JSON.stringify({
        path: "main.go",
        old_string: "a\nold line\nc",
        new_string: "a\nnew line\nc",
      }),
      details: "",
    };
    const { container } = render(
      <ApprovalPanel approval={editAsk} onRespond={() => {}} />,
    );
    expect(screen.getByText("main.go")).toBeInTheDocument();
    const dels = container.querySelectorAll('[data-diff="del"]');
    const adds = container.querySelectorAll('[data-diff="add"]');
    expect(dels).toHaveLength(1);
    expect(adds).toHaveLength(1);
    expect(dels[0]).toHaveTextContent("old line");
    expect(adds[0]).toHaveTextContent("new line");
  });

  it("toggles to the verbatim args string with Raw and back with Pretty", () => {
    const args = JSON.stringify({ command: "ls -la" });
    const shellAsk: ApprovalRequest = {
      ...mainAsk,
      toolName: "Shell",
      reason: "runs a command",
      args,
      details: "runs a command\n\ncommand: ls -la",
    };
    render(<ApprovalPanel approval={shellAsk} onRespond={() => {}} />);
    const toggle = screen.getByRole("button", { name: "Raw" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(toggle);
    expect(screen.getByText(args)).toBeInTheDocument();
    expect(screen.getByText("Raw arguments")).toBeInTheDocument();
    const back = screen.getByRole("button", { name: "Pretty" });
    expect(back).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(back);
    expect(screen.queryByText(args)).toBeNull();
    expect(screen.getByText("ls -la")).toBeInTheDocument();
  });

  it("offers Expand only when a handler is wired, and passes the ask", () => {
    const { rerender } = render(
      <ApprovalPanel approval={mainAsk} onRespond={() => {}} />,
    );
    expect(
      screen.queryByRole("button", { name: "Expand permission details" }),
    ).toBeNull();
    const onExpand = vi.fn();
    rerender(
      <ApprovalPanel
        approval={mainAsk}
        onRespond={() => {}}
        onExpand={onExpand}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Expand permission details" }),
    );
    expect(onExpand).toHaveBeenCalledWith(mainAsk);
  });

  // A debugger MCP call in an AI-debug session (ADR 0254): the daemon forces
  // every such call through a fresh ask and never learns Always allow, so
  // the card withholds the button and says why.
  const debugMcpAsk: ApprovalRequest = {
    ...mainAsk,
    approvalId: "dbg-1:1:c1:r1",
    sessionId: "dbg-1",
    toolName: "mcp__github__create_issue",
    description: "mcp__github__create_issue needs your approval.",
    reason: "debug MCP call requires fresh current operator approval",
    args: JSON.stringify({ title: "Flaky scheduler test" }),
    details: "",
  };

  it("withholds Always allow for a debugger MCP ask in a debug session and explains the one-call rule", () => {
    const onRespond = vi.fn();
    render(
      <ApprovalPanel
        approval={debugMcpAsk}
        onRespond={onRespond}
        debugSession
      />,
    );
    expect(screen.getByText("Debugger MCP")).toBeInTheDocument();
    expect(screen.getByTestId("debug-mcp-ask-note")).toHaveTextContent(
      "Always allow is not learned",
    );
    expect(screen.queryByRole("button", { name: "Always allow" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    expect(onRespond.mock.calls.map(([choice]) => choice)).toEqual([
      "once",
      "deny",
    ]);
  });

  it("keeps the generic card for the same ask outside a debug session, and for a non-MCP ask inside one", () => {
    const { unmount } = render(
      <ApprovalPanel approval={debugMcpAsk} onRespond={() => {}} />,
    );
    expect(screen.getByRole("button", { name: "Always allow" })).toBeEnabled();
    expect(screen.queryByTestId("debug-mcp-ask-note")).toBeNull();
    unmount();
    render(
      <ApprovalPanel approval={mainAsk} onRespond={() => {}} debugSession />,
    );
    expect(screen.getByRole("button", { name: "Always allow" })).toBeEnabled();
    expect(screen.queryByText("Debugger MCP")).toBeNull();
  });
});

/**
 * An MCP ask's badge reads the TUI's humanized `Server · Tool` while the
 * exact daemon id stays on hover; destructiveness is judged on both forms,
 * so an MCP delete tool still paints the card red.
 */
describe("ApprovalPanel MCP tool titles", () => {
  const mcpAsk = (toolName: string): ApprovalRequest => ({
    ...mainAsk,
    toolName,
    description: `${toolName} needs your approval.`,
    details: "",
  });

  it("names an MCP tool Server · Tool on the badge and keeps the exact id on hover", () => {
    render(
      <ApprovalPanel
        approval={mcpAsk("mcp__github__issue_write")}
        onRespond={() => {}}
      />,
    );
    const badge = screen.getByText("GitHub · Issue write");
    expect(badge).toHaveAttribute("title", "mcp__github__issue_write");
    expect(
      screen.queryByText("This action modifies or deletes data."),
    ).toBeNull();
  });

  it("still classifies an MCP delete tool as destructive", () => {
    render(
      <ApprovalPanel
        approval={mcpAsk("mcp__github__delete_branch")}
        onRespond={() => {}}
      />,
    );
    expect(screen.getByText("GitHub · Delete branch")).toHaveAttribute(
      "title",
      "mcp__github__delete_branch",
    );
    expect(
      screen.getByText("This action modifies or deletes data."),
    ).toBeInTheDocument();
  });

  it("leaves a core tool's badge unchanged", () => {
    render(<ApprovalPanel approval={mainAsk} onRespond={() => {}} />);
    expect(screen.getByText("Bash")).toHaveAttribute("title", "Bash");
  });
});
