// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { FailedTurnCard } from "./failed-turn-card";

// Pins the failed-turn card's "Show details" disclosure before it moves onto
// the Collapsible primitive: the toggle's name and expanded state, the region
// it controls, click and keyboard toggling, and the expand-details
// preference. They read roles, names and ARIA, never markup.

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
});

const RAW = '{"error":{"type":"overloaded_error","message":"Overloaded"}}';

function controlled(button: HTMLElement): HTMLElement | null {
  const id = button.getAttribute("aria-controls");
  return id ? document.getElementById(id) : null;
}

describe("failed turn card details", () => {
  it("starts collapsed with the raw payload out of view", () => {
    render(<FailedTurnCard detail={RAW} permanent={false} summary="The provider is overloaded." />);
    const toggle = screen.getByRole("button", { name: "Show details" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(RAW)).toBeNull();
    expect(screen.queryByText("Raw payload")).toBeNull();
  });

  it("expands and collapses on click, renaming the toggle and revealing the controlled payload", async () => {
    const user = userEvent.setup();
    render(<FailedTurnCard detail={RAW} permanent={false} summary="The provider is overloaded." />);
    await user.click(screen.getByRole("button", { name: "Show details" }));
    const toggle = screen.getByRole("button", { name: "Hide details" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const region = controlled(toggle);
    expect(region?.textContent).toContain("Raw payload");
    expect(region?.textContent).toContain(RAW);
    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Show details" }).getAttribute("aria-expanded")).toBe(
      "false",
    );
    expect(screen.queryByText(RAW)).toBeNull();
  });

  it("toggles from the keyboard with Enter and Space", async () => {
    const user = userEvent.setup();
    render(<FailedTurnCard detail={RAW} permanent summary="Invalid request." />);
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Show details" }));
    await user.keyboard("{Enter}");
    expect(screen.getByRole("button", { name: "Hide details" })).toBe(document.activeElement);
    expect(screen.getByText(RAW)).toBeTruthy();
    await user.keyboard(" ");
    expect(screen.getByRole("button", { name: "Show details" })).toBe(document.activeElement);
    expect(screen.queryByText(RAW)).toBeNull();
  });

  it("starts expanded when the expand-details preference is on", () => {
    window.localStorage.setItem("studio.profile.expand-details", "expanded");
    render(<FailedTurnCard detail={RAW} permanent={false} summary="The provider is overloaded." />);
    const toggle = screen.getByRole("button", { name: "Hide details" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(controlled(toggle)?.textContent).toContain(RAW);
  });

  it("offers no toggle when the payload adds nothing to the summary", () => {
    render(<FailedTurnCard detail="Same text" permanent={false} summary="Same text" />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});
