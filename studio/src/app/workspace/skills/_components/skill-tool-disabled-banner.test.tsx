import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  SKILL_TOOL_DISABLED_TEXT,
  SkillToolDisabledBanner,
} from "./skill-tool-disabled-banner";

/**
 * The Skills page banner keyed on the DAEMON's own `capabilities.skills`:
 * renders only for an explicit `false` (the tool is off — a managed daemon
 * spawned without --skills-dir), points a managed daemon at Settings →
 * Tools and an external one at its deployment, and renders nothing when
 * the daemon reports the tool on or reports nothing at all.
 */

const runtime = vi.hoisted(() => ({
  connected: true,
  mode: "managed" as "managed" | "external",
  serverCapabilities: {} as Record<string, unknown>,
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

beforeEach(() => {
  runtime.mode = "managed";
  runtime.serverCapabilities = { skills: false };
});

describe("SkillToolDisabledBanner", () => {
  it("names the disabled tool and links a managed daemon to Settings → Tools", () => {
    render(<SkillToolDisabledBanner />);
    const banner = screen.getByTestId("skill-tool-disabled");
    expect(banner).toHaveTextContent(SKILL_TOOL_DISABLED_TEXT);
    expect(
      screen.getByRole("link", { name: "Settings → Tools" }),
    ).toHaveAttribute("href", "/workspace/settings/tools");
  });

  it("points an external deployment at its own mecated flags instead", () => {
    runtime.mode = "external";
    render(<SkillToolDisabledBanner />);
    expect(screen.getByTestId("skill-tool-disabled")).toHaveTextContent(
      /external deployment starts mecated without a skills directory/,
    );
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("renders nothing while the tool is registered or unreported", () => {
    runtime.serverCapabilities = { skills: true };
    const { rerender } = render(<SkillToolDisabledBanner />);
    expect(screen.queryByTestId("skill-tool-disabled")).not.toBeInTheDocument();
    runtime.serverCapabilities = {};
    rerender(<SkillToolDisabledBanner />);
    expect(screen.queryByTestId("skill-tool-disabled")).not.toBeInTheDocument();
  });
});
