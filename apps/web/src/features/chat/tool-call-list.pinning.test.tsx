// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the turn's tool-call disclosure before it moves from a hand-rolled
 * `aria-expanded` button to the Collapsible primitive: the toggle is one
 * button named by its summary, it opens and closes from the pointer and the
 * keyboard, the closed list keeps its rows in the document (hidden), so the
 * calls stay findable by name, and the disclosure adds no heading or region.
 * Find-in-page itself is pinned in a real browser by
 * `e2e/tool-call-list.spec.ts`.
 */

import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import type { ToolActivity } from "./tool-activity";
import { ToolCallList } from "./tool-call-list";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
});

const read: ToolActivity = {
  args: JSON.stringify({ path: "src/notes.md" }),
  id: "call-read",
  name: "Read",
  output: JSON.stringify({ content: "hello" }),
  runId: "run-a",
};
const shell: ToolActivity = {
  args: JSON.stringify({ command: "false" }),
  id: "call-shell",
  isError: true,
  name: "Shell",
  output: "exit 1",
  runId: "run-a",
};

const toggleName = "Activity: 2 tools · 1 failed Read · Shell";

describe("tool call disclosure pins", () => {
  it("is one button named by its summary, closed under the default preference", () => {
    render(<ToolCallList tools={[read, shell]} />);
    const buttons = screen.getAllByRole("button");
    expect(buttons).toHaveLength(1);
    expect(screen.getByRole("button", { name: toggleName }).getAttribute("aria-expanded")).toBe(
      "false",
    );
  });

  it("keeps the closed list's rows in the document, hidden, with the names in the toggle", () => {
    render(<ToolCallList tools={[read, shell]} />);
    expect(screen.queryByRole("list")).toBeNull();
    const list = screen.getByRole("list", { hidden: true });
    expect(list.hidden).toBe(true);
    expect(list.closest("[hidden]")).toBe(list);
    expect(within(list).getAllByRole("listitem", { hidden: true })).toHaveLength(2);
    expect(within(list).getByText("path: src/notes.md").isConnected).toBe(true);
    expect(within(list).getByText("exit 1").isConnected).toBe(true);
    expect(screen.getByRole("button", { name: toggleName }).textContent).toContain("Read · Shell");
  });

  it("opens and closes from the pointer and the keyboard", async () => {
    const user = userEvent.setup();
    render(<ToolCallList tools={[read, shell]} />);
    const toggle = screen.getByRole("button", { name: toggleName });
    await user.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(screen.getByRole("list")).getAllByRole("listitem")).toHaveLength(2);
    await user.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("list")).toBeNull();

    toggle.focus();
    await user.keyboard("{Enter}");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("list")).toBeTruthy();
    await user.keyboard(" ");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("list")).toBeNull();
  });

  it("adds no heading or region, closed or open", async () => {
    const user = userEvent.setup();
    render(<ToolCallList tools={[read, shell]} />);
    const landmarks = () => [
      ...screen.queryAllByRole("heading", { hidden: true }),
      ...screen.queryAllByRole("region", { hidden: true }),
    ];
    expect(landmarks()).toEqual([]);
    await user.click(screen.getByRole("button", { name: toggleName }));
    expect(landmarks()).toEqual([]);
  });
});
