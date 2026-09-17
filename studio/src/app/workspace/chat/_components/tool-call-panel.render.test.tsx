import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ToolCallInfo } from "@/features/agent";
import { memoryStorage } from "@/test/memory-storage";
import { ToolCallPanel } from "./tool-call-panel";

const shared = {
  onClose: () => {},
  maximized: false,
  onToggleMaximize: () => {},
};

const editCall: ToolCallInfo = {
  callId: "c1",
  name: "Edit",
  input: "path: src/a.ts · …",
  rawArgs: JSON.stringify({
    path: "src/a.ts",
    old_string: "one\ntwo",
    new_string: "one\n2",
    replace_all: false,
  }),
  output: "edited",
  status: "completed",
};

const mcpCall: ToolCallInfo = {
  callId: "c2",
  name: "mcp__github__list_issues",
  input: "owner: stacklok",
  rawArgs: JSON.stringify({ owner: "stacklok" }),
  output: JSON.stringify({ items: [] }),
  status: "completed",
  parts: [
    { kind: "resource_link", url: "https://example.com/r", name: "r.html" },
  ],
};

// The panel frame persists its width in localStorage; stub a real Storage.
beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

/**
 * The drill-down tier: an Edit/Write shows its diff with a Raw toggle back
 * to the exact args JSON, an MCP tool is titled `server · tool` with the
 * raw name beneath, and the result's parts render under the output.
 */
describe("ToolCallPanel", () => {
  it("shows an Edit call as a diff, with Raw swapping in the pretty JSON", () => {
    render(<ToolCallPanel call={editCall} {...shared} />);
    expect(screen.getByTestId("edit-diff")).toBeInTheDocument();
    expect(screen.getByTestId("diff-header")).toHaveTextContent(
      "src/a.ts -1 +1",
    );
    const raw = screen.getByRole("button", { name: "Raw" });
    expect(raw).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(raw);
    expect(screen.queryByTestId("edit-diff")).toBeNull();
    expect(screen.getByText(/"old_string": "one\\ntwo"/)).toBeInTheDocument();
    const back = screen.getByRole("button", { name: "Diff" });
    expect(back).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(back);
    expect(screen.getByTestId("edit-diff")).toBeInTheDocument();
  });

  it("offers no Raw toggle for a call that is not an Edit/Write", () => {
    render(<ToolCallPanel call={mcpCall} {...shared} />);
    expect(screen.queryByRole("button", { name: "Raw" })).toBeNull();
    expect(screen.getByText(/"owner": "stacklok"/)).toBeInTheDocument();
  });

  it("titles an MCP tool by Server · Tool and shows the exact name and result parts", () => {
    render(<ToolCallPanel call={mcpCall} {...shared} />);
    expect(screen.getByText("GitHub · List issues")).toBeInTheDocument();
    expect(screen.getByText("· mcp__github__list_issues")).toBeInTheDocument();
    expect(screen.getByText("Result parts")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /r\.html/ })).toHaveAttribute(
      "href",
      "https://example.com/r",
    );
  });
});
