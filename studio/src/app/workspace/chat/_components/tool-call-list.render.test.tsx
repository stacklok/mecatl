import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ToolCallInfo } from "@/features/agent";
import { memoryStorage } from "@/test/memory-storage";
import { ToolCallList } from "./tool-call-list";

const mcpCall: ToolCallInfo = {
  callId: "c1",
  name: "mcp__github__list_issues",
  input: "owner: stacklok · repo: mecatl",
  rawArgs: JSON.stringify({ owner: "stacklok", repo: "mecatl" }),
  output: JSON.stringify({ items: [1, 2], total: 2 }),
  status: "completed",
  parts: [
    {
      kind: "resource_link",
      url: "https://example.com/report.html",
      name: "report.html",
      title: "Report",
    },
    { kind: "resource_link", url: "javascript:alert(1)", name: "evil" },
    { kind: "image", mimeType: "image/png", data: "iVBORw==" },
    { kind: "image", mimeType: "image/svg+xml", data: "PHN2Zz4=" },
  ],
};

const editCall: ToolCallInfo = {
  callId: "c2",
  name: "Edit",
  input: "path: src/a.ts · …",
  rawArgs: JSON.stringify({
    path: "src/a.ts",
    old_string: "one\ntwo",
    new_string: "one\n2",
  }),
  changedPath: "src/a.ts",
  output: "edited",
  status: "completed",
};

/**
 * The expanded activity rows are the TUI's compact tool cards: a friendly
 * `server · tool` head for an MCP tool (raw name on hover), the clamped arg
 * summary, the shaped result preview, the result's link/image parts
 * (untrusted — only http(s) links and raster images render as such), and an
 * Edit/Write's inline diff. The list starts collapsed unless the global
 * Expand details preference is on.
 */
describe("ToolCallList rows", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("starts collapsed, then shows friendly heads, arg and result summaries", () => {
    const onSelect = vi.fn();
    render(
      <ToolCallList toolCalls={[mcpCall, editCall]} onSelect={onSelect} />,
    );
    const activity = screen.getByRole("button", { name: /Activity: 2 tools/ });
    expect(activity).toHaveAttribute("aria-expanded", "false");
    expect(activity).toHaveTextContent("GitHub · List issues · Edit");
    expect(screen.queryByTestId("tool-args-summary")).toBeNull();

    fireEvent.click(activity);
    expect(activity).toHaveAttribute("aria-expanded", "true");
    const head = screen.getByText("GitHub · List issues");
    expect(head).toHaveAttribute("title", "mcp__github__list_issues");
    const summaries = screen.getAllByTestId("tool-args-summary");
    expect(summaries[0]).toHaveTextContent("owner: stacklok · repo: mecatl");
    expect(summaries[1]).toHaveTextContent(
      "path: src/a.ts · old_string: <2 lines> · new_string: <2 lines>",
    );
    const results = screen.getAllByTestId("tool-result-summary");
    expect(results[0]).toHaveTextContent("keys: items, total");
    expect(results[1]).toHaveTextContent("edited");

    fireEvent.click(
      screen.getByRole("button", { name: "Open GitHub · List issues details" }),
    );
    expect(onSelect).toHaveBeenCalledWith(mcpCall);
  });

  it("renders result parts with the untrusted-content allowlists", () => {
    render(<ToolCallList toolCalls={[mcpCall]} />);
    fireEvent.click(screen.getByRole("button", { name: /Activity/ }));
    const link = screen.getByRole("link", { name: /Report/ });
    expect(link).toHaveAttribute("href", "https://example.com/report.html");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    // The javascript: link degrades to a text label — never an anchor.
    expect(screen.queryByRole("link", { name: /evil/ })).toBeNull();
    expect(screen.getByText("evil")).toBeInTheDocument();
    // The PNG renders as a thumbnail; the SVG degrades to a label.
    const image = screen.getByRole("img", { name: "Returned by the tool" });
    expect(image).toHaveAttribute("src", "data:image/png;base64,iVBORw==");
    expect(screen.getByText("[image]")).toBeInTheDocument();
  });

  it("renders an Edit row's inline diff once expanded", () => {
    render(<ToolCallList toolCalls={[editCall]} onSelect={() => {}} />);
    expect(screen.queryByTestId("edit-diff")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Activity/ }));
    expect(screen.getByTestId("edit-diff")).toBeInTheDocument();
    expect(screen.getByTestId("diff-header")).toHaveTextContent(
      "src/a.ts -1 +1",
    );
  });

  it("starts expanded when the global Expand details preference is on", () => {
    window.localStorage.setItem("mecatl-studio.expand-details", "1");
    render(<ToolCallList toolCalls={[editCall]} />);
    expect(screen.getByRole("button", { name: /Activity/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(screen.getByTestId("edit-diff")).toBeInTheDocument();
  });

  it("summarises a hydrated call whose raw JSON rides `input`", () => {
    const hydrated: ToolCallInfo = {
      callId: "c3",
      name: "Shell",
      input: JSON.stringify({ command: "go test ./...", timeout_ms: 5000 }),
      output: "ok",
      status: "completed",
    };
    render(<ToolCallList toolCalls={[hydrated]} />);
    fireEvent.click(screen.getByRole("button", { name: /Activity/ }));
    expect(screen.getByTestId("tool-args-summary")).toHaveTextContent(
      "command: go test ./... · timeout_ms: 5000",
    );
  });
});
