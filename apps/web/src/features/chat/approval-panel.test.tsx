// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ApprovalPanel, type ApprovalRequest } from "./approval-panel";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

function ask(tool: string): ApprovalRequest {
  return { args: '{"path":"report.txt"}', askId: "ask-a", reason: "Needs access", tool };
}

/** The card's tone, read from the classes that carry it on the card, icon, and badge. */
function tone(tool: string): "destructive" | "warning" {
  render(<ApprovalPanel approval={ask(tool)} disabled={false} onRespond={() => {}} />);
  const card = screen.getByRole("region", { name: `Permission required: ${tool}` });
  const icon = card.querySelector("svg");
  const badge = card.querySelector('[data-slot="badge"]');
  const classes = (element: Element | null) => element?.getAttribute("class")?.split(" ") ?? [];
  const destructive = [
    classes(card).includes("border-destructive/40") && classes(card).includes("bg-destructive/5"),
    classes(icon).includes("text-destructive"),
    classes(badge).includes("bg-destructive/15") && classes(badge).includes("text-destructive"),
  ];
  const warning = [
    classes(card).includes("border-warning/40") && classes(card).includes("bg-warning/5"),
    classes(icon).includes("text-warning"),
    classes(badge).includes("bg-warning/15") && classes(badge).includes("text-warning"),
  ];
  expect(badge?.textContent).toBe(tool);
  cleanup();
  if (destructive.every(Boolean) && !warning.some(Boolean)) return "destructive";
  if (warning.every(Boolean) && !destructive.some(Boolean)) return "warning";
  throw new Error(`mixed tone for ${tool}: ${JSON.stringify({ destructive, warning })}`);
}

describe("ApprovalPanel tone", () => {
  // Pinned before the token classifier landed: these names already classify
  // correctly, and must keep exactly this tone.
  it.each([
    "delete",
    "Delete",
    "DELETE",
    "remove",
    "drop",
    "revoke",
    "destroy",
    "purge",
    "rm",
    "RM",
    "Remove file",
    "git rm",
    "rm -rf",
    "delete-file",
    "fs.delete",
    "mcp/delete",
    "server:drop",
    "remove branch",
  ])("keeps %j destructive", (tool) => {
    expect(tone(tool)).toBe("destructive");
  });

  it.each([
    "Bash",
    "Edit",
    "Read",
    "Write",
    "Glob",
    "Grep",
    "WebFetch",
    "PresentPlan",
    "confirm",
    "format",
    "term",
    "dropdown",
    "deleted_items_report",
    "rmdir",
    "undelete",
    "removal",
  ])("keeps %j warning", (tool) => {
    expect(tone(tool)).toBe("warning");
  });

  // #2224: these read as warnings under the word-boundary regex.
  it.each([
    "DeleteFile",
    "delete_file",
    "RemoveBranch",
    "removeBranch",
    "drop_table",
    "revokeToken",
    "github__delete_branch",
  ])("gives %j the destructive tone", (tool) => {
    expect(tone(tool)).toBe("destructive");
  });
});
