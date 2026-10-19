// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the tool panel's Raw/Diff switch before it becomes the shadcn Toggle:
 * an Edit or Write opens on its diff with a "Raw" button that is not
 * pressed; pressing it, by pointer or with Enter or Space, shows the
 * formatted arguments, marks it pressed, and names it "Diff"; pressing it
 * again goes back. A call with no diff has no switch. Roles, names, and
 * `aria-pressed` only, so these hold on both implementations.
 */

import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ContentPreviewPanel } from "./content-preview-panel";
import type { ToolActivity } from "./tool-activity";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

const edit: ToolActivity = {
  args: JSON.stringify({ new_string: "b", old_string: "a", path: "src/edited.ts" }),
  id: "call-edit",
  name: "Edit",
  output: "ok",
  runId: "run-a",
};

function renderTool(tool: ToolActivity) {
  render(
    <ContentPreviewPanel
      canvas=""
      onCanvasChange={vi.fn()}
      onClose={vi.fn()}
      preview={{ kind: "tool", tool }}
    />,
  );
  return {
    panel: screen.getByRole("complementary", { name: `${tool.name} result` }),
    user: userEvent.setup(),
  };
}

/** The input as the diff, with the switch offering the raw arguments. */
function expectDiff(panel: HTMLElement, toggle: HTMLElement) {
  expect(within(panel).getByRole("button", { name: "Raw" })).toBe(toggle);
  expect(toggle.getAttribute("aria-pressed")).toBe("false");
  expect(within(panel).getByRole("heading", { name: "Input — diff" })).toBeTruthy();
  expect(within(panel).getByTestId("edit-diff")).toBeTruthy();
  expect(panel.textContent).not.toContain('"old_string": "a"');
}

/** The input as formatted arguments, with the switch offering the diff back. */
function expectRaw(panel: HTMLElement, toggle: HTMLElement) {
  expect(within(panel).getByRole("button", { name: "Diff" })).toBe(toggle);
  expect(toggle.getAttribute("aria-pressed")).toBe("true");
  expect(within(panel).getByRole("heading", { name: "Input" })).toBeTruthy();
  expect(within(panel).queryByTestId("edit-diff")).toBeNull();
  expect(panel.textContent).toContain('"old_string": "a"');
}

describe("tool panel raw switch pins", () => {
  it("switches an edit between its diff and its raw arguments on click", async () => {
    const { panel, user } = renderTool(edit);
    const toggle = within(panel).getByRole("button", { name: "Raw" });
    expectDiff(panel, toggle);

    await user.click(toggle);
    expectRaw(panel, toggle);

    await user.click(toggle);
    expectDiff(panel, toggle);
  });

  it("switches with Enter and Space from the keyboard", async () => {
    const { panel, user } = renderTool(edit);
    const toggle = within(panel).getByRole("button", { name: "Raw" });
    act(() => toggle.focus());

    await user.keyboard("{Enter}");
    expectRaw(panel, toggle);
    expect(document.activeElement).toBe(toggle);

    await user.keyboard(" ");
    expectDiff(panel, toggle);
    expect(document.activeElement).toBe(toggle);
  });

  it("keeps the output in view whichever input is shown", async () => {
    const { panel, user } = renderTool(edit);
    expect(within(panel).getByRole("heading", { name: "Output" })).toBeTruthy();
    await user.click(within(panel).getByRole("button", { name: "Raw" }));
    expect(within(panel).getByRole("heading", { name: "Output" })).toBeTruthy();
    expect(panel.textContent).toContain("ok");
  });

  it("offers no switch for a call without a diff", () => {
    const { panel } = renderTool({
      args: JSON.stringify({ path: "src/notes.md" }),
      id: "call-read",
      name: "Read",
      output: "hello",
      runId: "run-a",
    });
    expect(within(panel).queryByRole("button", { name: "Raw" })).toBeNull();
    expect(within(panel).queryByRole("button", { name: "Diff" })).toBeNull();
    expect(within(panel).getByRole("heading", { name: "Input" })).toBeTruthy();
  });
});
