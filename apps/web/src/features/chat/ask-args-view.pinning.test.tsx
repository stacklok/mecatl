// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { ApprovalDetailPanel } from "./approval-detail-panel";
import { ApprovalPanel, type ApprovalRequest } from "./approval-panel";

// Pins the approval card's argument views before #2259 changes how a long
// command and invisible characters show. An ordinary command, its raw
// arguments, a JSON view, and the detail panel keep exactly this text, these
// controls, and a compact scroll box on the card. They assert text and
// controls, never markup, so the same file passes before and after.

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

function ask(args: string, tool = "Shell"): ApprovalRequest {
  return { args, askId: "ask-a", reason: "Needs a shell", tool };
}

function renderCard(args: string, tool = "Shell"): HTMLElement {
  render(<ApprovalPanel approval={ask(args, tool)} disabled={false} onRespond={() => {}} />);
  return screen.getByRole("region", { name: `Permission required: ${tool}` });
}

function buttonNames(element: HTMLElement): string[] {
  return within(element)
    .getAllByRole("button")
    .map((button) => button.textContent ?? "");
}

function onlyPre(element: HTMLElement): HTMLPreElement {
  const pres = element.querySelectorAll("pre");
  expect(pres).toHaveLength(1);
  return pres[0] as HTMLPreElement;
}

function lines(count: number): string {
  return Array.from({ length: count }, (_, index) => `echo step ${index + 1}`).join("\n");
}

/** Nothing on the card beyond the command itself: no count, toggle, badge, or chip. */
function expectPlain(element: HTMLElement) {
  expect(within(element).queryByText(/\d+ lines?$/)).toBeNull();
  expect(within(element).queryByText(/more lines?/)).toBeNull();
  expect(within(element).queryByText(/invisible/i)).toBeNull();
  expect(within(element).queryAllByRole("img")).toEqual([]);
}

describe("approval card command view, before #2259", () => {
  it.each([
    ["one line", "make test"],
    ["three lines", "set -e\nmake build\nmake test"],
    ["twelve lines", lines(12)],
  ])("shows a %s command as exactly its text, with the same controls", (_name, command) => {
    const card = renderCard(JSON.stringify({ command }));
    const pre = onlyPre(card);
    expect(pre.textContent).toBe(command);
    // Plain text, with no element inside the command.
    expect(pre.children).toHaveLength(0);
    expectPlain(card);
    expect(buttonNames(card)).toEqual(["Raw arguments", "Allow once", "Always allow", "Deny"]);
  });

  it("keeps the command in a compact scroll box on the card", () => {
    const card = renderCard(JSON.stringify({ command: "make test" }));
    const box = onlyPre(card).closest(".max-h-56");
    expect(box).not.toBeNull();
    expect(card.contains(box)).toBe(true);
    expect(box?.getAttribute("class")?.split(" ")).toContain("overflow-y-auto");
  });

  it("shows a command's raw arguments as exactly the JSON sent", () => {
    const args = JSON.stringify({ command: "set -e\nmake test", timeout_ms: 5000 });
    const card = renderCard(args);
    const toggle = within(card).getByRole("button", { name: "Raw arguments" });
    fireEvent.click(toggle);
    const raw = onlyPre(card);
    expect(raw.textContent).toBe(args);
    expect(raw.children).toHaveLength(0);
    expect(raw.getAttribute("class")?.split(" ")).toContain("break-all");
    expect(raw.closest(".max-h-56")).not.toBeNull();
    expect(within(card).queryByRole("term")).toBeNull();
    expectPlain(card);
    fireEvent.click(toggle);
    expect(onlyPre(card).textContent).toBe("set -e\nmake test");
  });

  it("shows other arguments as exactly their formatted JSON", () => {
    const args = '{"path":"notes.md","limit":5}';
    const card = renderCard(args, "Read");
    const pretty = onlyPre(card);
    expect(pretty.textContent).toBe('{\n  "path": "notes.md",\n  "limit": 5\n}');
    expect(pretty.children).toHaveLength(0);
    expect(pretty.closest(".max-h-56")).not.toBeNull();
    expectPlain(card);
    fireEvent.click(within(card).getByRole("button", { name: "Raw arguments" }));
    expect(onlyPre(card).textContent).toBe(args);
  });

  it("keeps the Edit diff in the card's compact box", () => {
    const args = JSON.stringify({ new_string: "after", old_string: "before", path: "notes.txt" });
    const card = renderCard(args, "Edit");
    const diff = within(card).getByTestId("edit-diff");
    expect(diff.closest(".max-h-56")).not.toBeNull();
    expect(card.querySelector("pre")).toBeNull();
    expectPlain(card);
    expect(buttonNames(card)).toEqual(["Raw arguments", "Allow once", "Always allow", "Deny"]);
  });
});

describe("approval detail panel command view, before #2259", () => {
  it("shows a long command whole, with no cap and no toggle", () => {
    const command = lines(60);
    render(
      <ApprovalDetailPanel
        approval={ask(JSON.stringify({ command }))}
        disabled={false}
        onRespond={() => {}}
        scope={createRef<HTMLElement>()}
        uncertain={false}
      />,
    );
    const panel = document.body;
    const pre = onlyPre(panel);
    expect(pre.textContent).toBe(command);
    expect(pre.closest(".max-h-56")).toBeNull();
    expectPlain(panel);
    expect(buttonNames(panel)).toEqual(["Raw arguments", "Allow once", "Always allow", "Deny"]);
  });
});
