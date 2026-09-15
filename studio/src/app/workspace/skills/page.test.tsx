import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import WorkspaceSkillsPage from "./page";

// Rows navigate on click via useRouter; jsdom has no app-router context.
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

/**
 * Pins the skills-list kebab: Disable/Enable/Delete confirm first (a write
 * that restarts the daemon must warn) and then call the right hook action
 * with the row's name; external mode renders the items disabled instead of
 * manufacturing controller 409s.
 */

const state = vi.hoisted(() => ({
  manageable: true,
  setEnabled: vi.fn(() => Promise.resolve()),
  remove: vi.fn(() => Promise.resolve()),
  saveBody: vi.fn(() => Promise.resolve()),
  fetchBody: vi.fn(() => Promise.resolve("---\nname: x\n---\nbody")),
}));

// The page reads capabilities for the Learned tab gate; the kebab tests run
// against a daemon without learned_skills, so the tab stays hidden here.
vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    mode: "managed",
    serverCapabilities: {},
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
    disabled: [{ name: "parked-skill", description: "Sits in .disabled" }],
    manageable: state.manageable,
    isLoading: false,
    error: null,
    actionError: null,
    create: vi.fn(() => Promise.resolve()),
    fetchBody: state.fetchBody,
    saveBody: state.saveBody,
    setEnabled: state.setEnabled,
    remove: state.remove,
    refresh: vi.fn(() => Promise.resolve()),
  }),
}));

beforeEach(() => {
  state.manageable = true;
  state.setEnabled.mockClear();
  state.remove.mockClear();
});

describe("skills list management kebab", () => {
  it("disables an active skill through the confirm dialog", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSkillsPage />);

    await user.click(
      screen.getByRole("button", { name: "Actions for pr-feedback" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "Disable" }));

    // The confirm names the restart consequence before anything moves.
    expect(await screen.findByText(/daemon restarts/)).toBeTruthy();
    expect(state.setEnabled).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Disable" }));
    expect(state.setEnabled).toHaveBeenCalledWith("pr-feedback", false);
  });

  it("offers Enable on a disabled row and calls setEnabled(name, true)", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSkillsPage />);

    await user.click(
      screen.getByRole("button", { name: "Actions for parked-skill" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "Enable" }));
    await user.click(await screen.findByRole("button", { name: "Enable" }));

    expect(state.setEnabled).toHaveBeenCalledWith("parked-skill", true);
    expect(state.remove).not.toHaveBeenCalled();
  });

  it("deletes only after the destructive confirm", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSkillsPage />);

    await user.click(
      screen.getByRole("button", { name: "Actions for pr-feedback" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    expect(state.remove).not.toHaveBeenCalled();

    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(state.remove).toHaveBeenCalledWith("pr-feedback");
  });

  it("renders the actions disabled in external mode", async () => {
    state.manageable = false;
    const user = userEvent.setup();
    render(<WorkspaceSkillsPage />);

    await user.click(
      screen.getByRole("button", { name: "Actions for pr-feedback" }),
    );
    for (const label of ["Edit", "Disable", "Delete"]) {
      const item = await screen.findByRole("menuitem", { name: label });
      expect(item.getAttribute("data-disabled")).not.toBeNull();
      expect(item.getAttribute("title")).toMatch(/external mecated/);
    }
  });
});
