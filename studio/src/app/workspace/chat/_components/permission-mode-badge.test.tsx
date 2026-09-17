import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PermissionModeBadge } from "./permission-mode-badge";

/**
 * The chat header's mode badge (mecatui's `mode <x>` header segment):
 * nothing for Manual, a coloured badge for Plan / Accept edits, and the
 * TARGET with "· pending" while a mid-run switch is held. Never rendered on
 * a surface with no Mode selector.
 */
describe("PermissionModeBadge", () => {
  const badge = () => screen.queryByTestId("permission-mode-badge");

  it("renders nothing for the everyday Manual posture", () => {
    render(<PermissionModeBadge mode="default" />);
    expect(badge()).toBeNull();
  });

  it("names Plan with the info cue and Accept edits with the success cue", () => {
    const { rerender } = render(<PermissionModeBadge mode="plan" />);
    expect(badge()).toHaveTextContent("Plan");
    expect(badge()).toHaveAttribute("data-mode", "plan");
    expect(badge()).toHaveAttribute("title", "Permission mode: Plan");
    expect(badge()?.className).toContain("text-info");
    expect(badge()).not.toHaveAttribute("data-pending");

    rerender(<PermissionModeBadge mode="acceptEdits" />);
    expect(badge()).toHaveTextContent("Accept edits");
    expect(badge()?.className).toContain("text-success");
  });

  it("shows the held target as pending while the run is live", () => {
    render(<PermissionModeBadge mode="default" pendingMode="plan" />);
    expect(badge()).toHaveTextContent("Plan · pending");
    expect(badge()).toHaveAttribute("data-pending", "true");
    expect(badge()).toHaveAttribute(
      "title",
      "Permission mode: Plan — pending, applies when the run ends (currently Manual)",
    );
    expect(badge()?.className).toContain("text-info");
  });

  it("shows a pending return to Manual too — the user's pick must be visible", () => {
    render(<PermissionModeBadge mode="plan" pendingMode="default" />);
    expect(badge()).toHaveTextContent("Manual · pending");
    // No mode colour for Manual, even pending: the muted variant.
    expect(badge()?.className).not.toContain("text-info");
    expect(badge()?.className).not.toContain("text-success");
  });

  it("ignores a pending value equal to the confirmed mode", () => {
    render(<PermissionModeBadge mode="plan" pendingMode="plan" />);
    expect(badge()).toHaveTextContent("Plan");
    expect(badge()).not.toHaveAttribute("data-pending");
  });

  it("renders nothing where the mode cannot be set (mock tour, AI-debug chat)", () => {
    render(<PermissionModeBadge mode="plan" enabled={false} />);
    expect(badge()).toBeNull();
  });

  it("announces itself as the permission mode to assistive technology", () => {
    render(<PermissionModeBadge mode="acceptEdits" />);
    expect(badge()).toHaveTextContent("Permission mode: Accept edits");
  });
});
