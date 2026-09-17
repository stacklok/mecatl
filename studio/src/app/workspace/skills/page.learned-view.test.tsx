import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import WorkspaceSkillsPage from "./page";

/**
 * The `/workspace/skills?view=learned` deep link — where the post-run
 * receipt toast's "Open Skills" action and the learning queue's "View
 * learned skill" link land: on a daemon with `learned_skills` the page opens
 * on the Learned pill and renders the Learned panel; without the capability
 * the pill does not exist and the query falls back to the All view instead
 * of stranding the page on a hidden tab.
 */

const state = vi.hoisted(() => ({
  capabilities: { learned_skills: true } as Record<string, unknown>,
  query: "view=learned",
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(state.query),
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    mode: "managed",
    serverCapabilities: state.capabilities,
  }),
}));

vi.mock("@/features/agent/hooks/use-agent-skills", () => ({
  useAgentSkills: () => ({
    skills: [
      {
        name: "pr-feedback",
        description: "Handles PR feedback",
        agentOwned: false,
        ownerAgent: "",
        activeVersion: "",
      },
    ],
    disabled: [],
    manageable: true,
    isLoading: false,
    error: null,
    actionError: null,
    create: vi.fn(() => Promise.resolve()),
    fetchBody: vi.fn(() => Promise.resolve("")),
    saveBody: vi.fn(() => Promise.resolve()),
    setEnabled: vi.fn(() => Promise.resolve()),
    remove: vi.fn(() => Promise.resolve()),
    refresh: vi.fn(() => Promise.resolve()),
  }),
}));

// The panel talks to the daemon; this test is about which view opens.
vi.mock("./_components/learned-skills-panel", () => ({
  LearnedSkillsPanel: () => <div data-testid="learned-panel">Learned</div>,
}));

beforeEach(() => {
  state.capabilities = { learned_skills: true };
  state.query = "view=learned";
});

describe("skills page ?view=learned", () => {
  it("opens the Learned view when the daemon supports learned skills", () => {
    render(<WorkspaceSkillsPage />);
    expect(screen.getByTestId("learned-panel")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Learned" })).toBeInTheDocument();
    // The ordinary skills table is not shown behind the Learned view.
    expect(screen.queryByText("pr-feedback")).not.toBeInTheDocument();
  });

  it("falls back to the All view without the capability", () => {
    state.capabilities = {};
    render(<WorkspaceSkillsPage />);
    expect(screen.queryByTestId("learned-panel")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Learned" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("pr-feedback")).toBeInTheDocument();
  });

  it("opens on All when no view is named", () => {
    state.query = "";
    render(<WorkspaceSkillsPage />);
    expect(screen.queryByTestId("learned-panel")).not.toBeInTheDocument();
    expect(screen.getByText("pr-feedback")).toBeInTheDocument();
  });
});
