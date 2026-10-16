// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { ReasoningDisclosure } from "./reasoning-disclosure";

// Pins the settled reasoning summary's disclosure before it moves onto the
// Collapsible primitive: the toggle's name and expanded state, the region it
// controls, click and keyboard toggling, the expand-details preference, and
// no toggle while streaming. They read roles, names and ARIA, never markup.

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
});

const TEXT = "Check the config first.\nThen read the logs.";

function controlled(button: HTMLElement): HTMLElement | null {
  const id = button.getAttribute("aria-controls");
  return id ? document.getElementById(id) : null;
}

describe("reasoning disclosure", () => {
  it("starts collapsed, naming the line count, with the text out of view", () => {
    render(<ReasoningDisclosure streaming={false} text={TEXT} />);
    const toggle = screen.getByRole("button", { name: "Reasoning summary · 2 lines" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/Then read the logs/)).toBeNull();
  });

  it("expands and collapses on click, revealing the controlled text", async () => {
    const user = userEvent.setup();
    render(<ReasoningDisclosure streaming={false} text={TEXT} />);
    const toggle = screen.getByRole("button", { name: "Reasoning summary · 2 lines" });
    await user.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(controlled(toggle)?.textContent).toContain("Then read the logs.");
    await user.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/Then read the logs/)).toBeNull();
  });

  it("toggles from the keyboard with Enter and Space", async () => {
    const user = userEvent.setup();
    render(<ReasoningDisclosure streaming={false} text="One thought." />);
    await user.tab();
    const toggle = screen.getByRole("button", { name: "Reasoning summary · 1 line" });
    expect(document.activeElement).toBe(toggle);
    await user.keyboard("{Enter}");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    await user.keyboard(" ");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });

  it("starts expanded when the expand-details preference is on", () => {
    window.localStorage.setItem("studio.profile.expand-details", "expanded");
    render(<ReasoningDisclosure streaming={false} text={TEXT} />);
    const toggle = screen.getByRole("button", { name: "Reasoning summary · 2 lines" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(controlled(toggle)?.textContent).toContain("Check the config first.");
  });

  it("offers no toggle while the reasoning is still streaming", () => {
    render(<ReasoningDisclosure streaming text={TEXT} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("Reasoning…")).toBeTruthy();
  });
});
