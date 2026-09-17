import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ApprovalRequest } from "@/features/agent";
import { ApprovalPanel } from "./approval-panel";
import { WRITE_OVERWRITE_NOTE } from "./tool-diff";

/**
 * The approval gate carries the TUI's size signal and risk caveat: an Edit
 * ask's path line says `-N +N` (and `replace all`), a Write ask's says
 * `N lines · overwrites if it exists` — so the reader knows the blast
 * radius before scrolling the diff or the file body.
 */
describe("ApprovalPanel diff headers", () => {
  it("shows -N +N and replace all on an Edit ask", () => {
    const ask: ApprovalRequest = {
      approvalId: "s1:1:c1:r1",
      sessionId: "s1",
      toolName: "Edit",
      description: "Edit needs your approval.",
      details: "",
      reason: "edits a file",
      args: JSON.stringify({
        path: "src/a.ts",
        old_string: "one\ntwo",
        new_string: "one\n2\nthree",
        replace_all: true,
      }),
    };
    render(<ApprovalPanel approval={ask} onRespond={() => {}} />);
    expect(screen.getByText("src/a.ts")).toBeInTheDocument();
    expect(screen.getByText(/-1 \+2 · replace all/)).toBeInTheDocument();
  });

  it("shows the line count and the overwrite caveat on a Write ask", () => {
    const ask: ApprovalRequest = {
      approvalId: "s1:1:c2:r1",
      sessionId: "s1",
      toolName: "Write",
      description: "Write needs your approval.",
      details: "",
      reason: "writes a file",
      args: JSON.stringify({ path: "notes.md", content: "a\nb\nc" }),
    };
    render(<ApprovalPanel approval={ask} onRespond={() => {}} />);
    expect(screen.getByText("notes.md")).toBeInTheDocument();
    expect(
      screen.getByText(new RegExp(`3 lines · ${WRITE_OVERWRITE_NOTE}`)),
    ).toBeInTheDocument();
  });
});
