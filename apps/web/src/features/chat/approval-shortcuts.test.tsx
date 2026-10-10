// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApprovalPanel, type ApprovalRequest } from "./approval-panel";
import { describeAskArgs } from "./ask-args";
import { EscapeHintContext } from "./escape-hint-context";
import { PlanReviewCard } from "./plan-review-card";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
});

function ask(overrides: Partial<ApprovalRequest> = {}): ApprovalRequest {
  return {
    args: '{"command":"ls"}',
    askId: "ask-a",
    controlTarget: { askId: "ask-a", runId: "run-a", sessionId: "chat-a" },
    reason: "Needs a shell",
    tool: "Bash",
    ...overrides,
  };
}

function renderActive(
  approval = ask(),
  props: { disabled?: boolean; uncertain?: boolean; active?: boolean } = {},
) {
  const respond = vi.fn();
  render(
    <EscapeHintContext.Provider value={props.active === false ? undefined : approval}>
      <ApprovalPanel
        approval={approval}
        disabled={props.disabled ?? false}
        onRespond={respond}
        uncertain={props.uncertain}
      />
    </EscapeHintContext.Provider>,
  );
  return respond;
}

function press(key: string, init: KeyboardEventInit = {}, target: Element = document.body) {
  fireEvent.keyDown(target, { key, ...init });
  fireEvent.keyUp(target, { key, ...init });
}

describe("approval shortcuts", () => {
  it.each([
    ["y", "allow_once"],
    ["Y", "allow_once"],
    ["a", "allow_once"],
    ["w", "allow_always"],
    ["W", "allow_always"],
    ["n", "deny"],
    ["d", "deny"],
    ["D", "deny"],
  ] as const)("answers %j with %s", (key, verdict) => {
    const respond = renderActive();
    press(key);
    expect(respond.mock.calls).toEqual([[verdict]]);
  });

  it("ignores modified, held, composing, and unrelated keys", () => {
    const respond = renderActive();
    press("y", { ctrlKey: true });
    press("a", { metaKey: true });
    press("n", { altKey: true });
    press("y", { repeat: true });
    press("y", { isComposing: true });
    for (const key of ["x", "j", "k", "Enter", " ", "Escape"]) press(key);
    expect(respond).not.toHaveBeenCalled();
  });

  it("never fires while typing anywhere on its surface", () => {
    const approval = ask();
    const respond = vi.fn();
    render(
      <div data-chat-surface="workspace">
        <input aria-label="Field" />
        <textarea aria-label="Composer" />
        <select aria-label="Choice">
          <option>One</option>
        </select>
        <div aria-label="Editor" contentEditable role="textbox" suppressContentEditableWarning />
        <EscapeHintContext.Provider value={approval}>
          <ApprovalPanel approval={approval} disabled={false} onRespond={respond} />
        </EscapeHintContext.Provider>
      </div>,
    );
    for (const name of ["Field", "Composer", "Editor"]) {
      const target = screen.getByRole("textbox", { name });
      target.focus();
      for (const key of ["y", "a", "w", "n", "d"]) press(key, {}, target);
    }
    const select = screen.getByRole("combobox", { name: "Choice" });
    select.focus();
    for (const key of ["y", "n"]) press(key, {}, select);
    expect(respond).not.toHaveBeenCalled();
    // The same surface does answer once focus leaves the field.
    press("y", {}, screen.getByRole("button", { name: "Raw arguments" }));
    expect(respond.mock.calls).toEqual([["allow_once"]]);
  });

  it("fires only for the surface's active ask while the ledger leaves it answerable", () => {
    for (const props of [{ active: false }, { disabled: true }, { uncertain: true }]) {
      const respond = renderActive(ask(), props);
      const card = screen.getByRole("region", { name: "Permission required: Bash" });
      for (const key of ["y", "w", "n"]) {
        press(key);
        press(key, {}, card);
      }
      expect(respond).not.toHaveBeenCalled();
      expect(card.querySelector("kbd")).toBeNull();
      expect(card.querySelector("[aria-keyshortcuts]")).toBeNull();
      cleanup();
    }
  });

  it("offers only Deny when the ask has no arguments", () => {
    const respond = renderActive(ask({ args: "" }));
    const card = screen.getByRole("region", { name: "Permission required: Bash" });
    press("y");
    press("w");
    expect(respond).not.toHaveBeenCalled();
    press("n");
    expect(respond.mock.calls).toEqual([["deny"]]);
    expect([...card.querySelectorAll("kbd")].map((kbd) => kbd.textContent)).toEqual(["N"]);
    expect(document.activeElement).toBe(within(card).getByRole("button", { name: "Deny" }));
  });

  it("yields to an open dialog or menu", () => {
    const respond = renderActive();
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.dataset.state = "open";
    document.body.append(dialog);
    press("y");
    expect(respond).not.toHaveBeenCalled();
    dialog.remove();
    press("y");
    expect(respond.mock.calls).toEqual([["allow_once"]]);
  });

  it("keeps each chat surface's keys to its own ask", () => {
    const workspaceAsk = ask({ reason: "Workspace ask" });
    const threadAsk = ask({
      askId: "ask-t",
      controlTarget: { askId: "ask-t", runId: "run-t", sessionId: "thread-a" },
      reason: "Thread ask",
    });
    const workspace = vi.fn();
    const thread = vi.fn();
    render(
      <div data-chat-surface="workspace">
        <button type="button">Workspace control</button>
        <EscapeHintContext.Provider value={workspaceAsk}>
          <ApprovalPanel approval={workspaceAsk} disabled={false} onRespond={workspace} />
        </EscapeHintContext.Provider>
        <aside data-chat-surface="thread">
          <button type="button">Thread control</button>
          <EscapeHintContext.Provider value={threadAsk}>
            <ApprovalPanel approval={threadAsk} disabled={false} onRespond={thread} />
          </EscapeHintContext.Provider>
        </aside>
      </div>,
    );
    press("y");
    expect(workspace.mock.calls).toEqual([["allow_once"]]);
    expect(thread).not.toHaveBeenCalled();

    press("n", {}, screen.getByRole("button", { name: "Workspace control" }));
    expect(workspace.mock.calls).toEqual([["allow_once"], ["deny"]]);
    expect(thread).not.toHaveBeenCalled();

    press("w", {}, screen.getByRole("button", { name: "Thread control" }));
    expect(thread.mock.calls).toEqual([["allow_always"]]);

    const threadCard = screen.getByText("Thread ask").closest("section") as HTMLElement;
    press("d", {}, within(threadCard).getByRole("button", { name: "Deny" }));
    expect(thread.mock.calls).toEqual([["allow_always"], ["deny"]]);
    expect(workspace.mock.calls).toHaveLength(2);
  });

  it("steps focus between the verdict buttons with the arrow keys", () => {
    renderActive();
    const card = screen.getByRole("region", { name: "Permission required: Bash" });
    const [once, always, deny] = ["Allow once", "Always allow", "Deny"].map((name) =>
      within(card).getByRole("button", { name }),
    );
    expect(document.activeElement).toBe(once);
    const steps: Array<[string, HTMLElement | undefined]> = [
      ["ArrowRight", always],
      ["ArrowDown", deny],
      ["ArrowRight", once],
      ["ArrowLeft", deny],
      ["ArrowUp", always],
    ];
    for (const [key, expected] of steps) {
      const focused = document.activeElement as HTMLElement;
      const event = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key });
      act(() => {
        focused.dispatchEvent(event);
      });
      expect(event.defaultPrevented).toBe(true);
      expect(document.activeElement).toBe(expected);
    }
    // Arrows outside the verdict buttons are left alone.
    const raw = within(card).getByRole("button", { name: "Raw arguments" });
    raw.focus();
    const event = new KeyboardEvent("keydown", {
      bubbles: true,
      cancelable: true,
      key: "ArrowDown",
    });
    raw.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(raw);
  });

  it("focuses Allow once for a new ask only when nothing else holds focus", () => {
    const composer = document.createElement("textarea");
    document.body.append(composer);
    composer.focus();
    renderActive();
    expect(document.activeElement).toBe(composer);
    cleanup();
    composer.remove();

    renderActive();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Allow once" }));
  });

  it("shows keycaps that match the keys", () => {
    renderActive();
    const card = screen.getByRole("region", { name: "Permission required: Bash" });
    const buttons = ["Allow once", "Always allow", "Deny"].map((name) =>
      within(card).getByRole("button", { name }),
    );
    expect(buttons.map((button) => button.getAttribute("aria-keyshortcuts"))).toEqual([
      "y a",
      "w",
      "n d",
    ]);
    expect(buttons.map((button) => button.querySelector("kbd")?.textContent)).toEqual([
      "Y",
      "W",
      "N",
    ]);
    expect(within(card).getByText("Esc to Deny")).toBeTruthy();
  });

  it("gives the plan card no letter shortcuts", () => {
    const plan = ask({ args: '{"plan":"Ship"}', tool: "PresentPlan" });
    const respond = vi.fn();
    render(
      <EscapeHintContext.Provider value={plan}>
        <PlanReviewCard approval={plan} disabled={false} onRespond={respond} />
      </EscapeHintContext.Provider>,
    );
    for (const key of ["y", "a", "w", "n", "d"]) press(key);
    expect(respond).not.toHaveBeenCalled();
    expect(screen.getByText("Esc to Iterate")).toBeTruthy();
  });
});

describe("approval card", () => {
  it("names the tool, its risk, and its arguments", () => {
    renderActive(ask({ args: '{"path":"x"}', tool: "delete_file" }), { active: false });
    const card = screen.getByRole("region", { name: "Permission required: delete_file" });
    expect(within(card).getByText("This action modifies or deletes data.")).toBeTruthy();
    expect(within(card).getByText(/"path": "x"/)).toBeTruthy();
    fireEvent.click(within(card).getByRole("button", { name: "Raw arguments" }));
    expect(within(card).getByText('{"path":"x"}')).toBeTruthy();
    expect(
      within(card).getByRole("button", { name: "Raw arguments" }).getAttribute("aria-pressed"),
    ).toBe("true");
    cleanup();

    renderActive(ask(), { active: false });
    const warning = screen.getByRole("region", { name: "Permission required: Bash" });
    expect(within(warning).queryByText("This action modifies or deletes data.")).toBeNull();
    expect(within(warning).getByText("ls")).toBeTruthy();
    // No side-panel slot: no detail button.
    expect(within(warning).queryByRole("button", { name: "Open in detail panel" })).toBeNull();
  });
});

describe("describeAskArgs", () => {
  it("decodes each tool's arguments", () => {
    expect(describeAskArgs("Bash", '{"command":"ls -la","timeout_ms":5}')).toEqual({
      command: "ls -la",
      kind: "shell",
    });
    expect(describeAskArgs("shell", '{"command":"pwd"}')).toEqual({
      command: "pwd",
      kind: "shell",
    });
    expect(describeAskArgs("Edit", '{"path":"a","old_string":"x","new_string":"y"}')).toEqual({
      kind: "edit",
    });
    expect(describeAskArgs("Write", '{"path":"a","content":"x"}')).toEqual({ kind: "write" });
    expect(describeAskArgs("Edit", '{"path":"a"}')).toEqual({
      kind: "json",
      pretty: '{\n  "path": "a"\n}',
    });
    expect(describeAskArgs("Read", "[1]")).toEqual({ kind: "json", pretty: "[\n  1\n]" });
    expect(describeAskArgs("Read", "{broken")).toEqual({ kind: "raw", text: "{broken" });
    expect(describeAskArgs("Read", "")).toEqual({ kind: "raw", text: "" });
    const long = JSON.stringify({ command: "x".repeat(70_000) });
    expect(describeAskArgs("Bash", long)).toEqual({ kind: "raw", text: long });
  });
});
