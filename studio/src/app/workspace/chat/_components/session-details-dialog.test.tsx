import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  COPY_ID_DENIED_FALLBACK,
  lifecycleLabel,
  SessionDetailsDialog,
} from "./session-details-dialog";

/**
 * Pins the `/session` dialog: it reads the snapshot's identity on open and
 * renders id, title, state, mode, model, placement label + branch and the
 * creation time; the Copy button writes the exact id to the clipboard and
 * says so; a daemon refusal renders as an alert, never as blank rows.
 */

const identity = vi.hoisted(() => ({
  fetch: vi.fn(),
}));

vi.mock("@/lib/harness/sessions", () => ({
  fetchHarnessSessionIdentity: identity.fetch,
}));

const FULL = {
  id: "session-abc-123",
  title: "Fix the flaky test",
  titleProvenance: "first-prompt",
  state: "idle",
  kind: "main",
  mode: "acceptEdits" as const,
  resolvedModel: {
    providerId: "openrouter",
    modelId: "openai/gpt-5",
    contextWindow: 400000,
    reasoningEffort: "high",
  },
  placement: {
    kind: "worktree",
    label: "feature-x",
    branch: "feature/x",
    revision: "abc123def",
  },
  createdAtUnix: 1755000000,
  turns: 7,
  toolCalls: 12,
  limits: { maxTurns: 50, maxToolCalls: 0, maxConsecutiveFailures: 3 },
  relationship: null,
};

beforeEach(() => {
  identity.fetch.mockReset();
  identity.fetch.mockResolvedValue(FULL);
});

describe("SessionDetailsDialog", () => {
  it("renders the identity rows read from the snapshot", async () => {
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
      />,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(await screen.findByText("Fix the flaky test")).toBeInTheDocument();
    expect(identity.fetch).toHaveBeenCalledWith(
      "session-abc-123",
      expect.any(AbortSignal),
    );
    expect(screen.getByText("session-abc-123")).toBeInTheDocument();
    // The lifecycle state as a label (the first place Studio names every
    // state as text), the kind, and the title's provenance chip.
    expect(screen.getByText("Idle")).toBeInTheDocument();
    expect(screen.getByText("Main chat")).toBeInTheDocument();
    expect(screen.getByText("From first prompt")).toBeInTheDocument();
    expect(screen.getByText("Accept edits")).toBeInTheDocument();
    expect(screen.getByText("openrouter")).toBeInTheDocument();
    expect(screen.getByText("openai/gpt-5")).toBeInTheDocument();
    expect(screen.getByText("high")).toBeInTheDocument();
    expect(screen.getByText("400,000 tokens")).toBeInTheDocument();
    expect(screen.getByText("feature-x (worktree)")).toBeInTheDocument();
    expect(screen.getByText("feature/x")).toBeInTheDocument();
    expect(screen.getByText("abc123def")).toBeInTheDocument();
    expect(screen.getByText("Created")).toBeInTheDocument();
    expect(screen.getByText("Turns").nextSibling).toHaveTextContent("7");
    expect(screen.getByText("Tool calls").nextSibling).toHaveTextContent("12");
    expect(
      screen.getByText("50 turns · none tool calls · 3 consecutive failures"),
    ).toBeInTheDocument();
    // No relationship set: none of those rows render.
    expect(screen.queryByText("Debug target")).toBeNull();
    expect(screen.queryByText("Parent session")).toBeNull();
  });

  it("names every lifecycle state the daemon persists", () => {
    expect(lifecycleLabel("idle")).toBe("Idle");
    expect(lifecycleLabel("running")).toBe("Running");
    expect(lifecycleLabel("awaiting")).toBe("Awaiting approval");
    expect(lifecycleLabel("completed")).toBe("Completed");
    expect(lifecycleLabel("failed")).toBe("Failed");
    expect(lifecycleLabel("cancelled")).toBe("Cancelled");
    // An unknown spelling is shown verbatim rather than hidden; an absent
    // state says so.
    expect(lifecycleLabel("paused")).toBe("paused");
    expect(lifecycleLabel("")).toBe("unavailable");
  });

  it("renders the relationship rows and copies the debug target id (ADR 0254)", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    identity.fetch.mockResolvedValue({
      ...FULL,
      kind: "debug",
      relationship: {
        debugTargetSessionId: "session-target-9",
        parentSessionId: "session-parent-1",
        scheduleName: "nightly",
        teamId: "team-7",
        memberName: "reviewer",
        branchIndex: 2,
        callId: "call-3",
        originSessionId: "session-origin-4",
      },
    });
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
      />,
    );
    await screen.findByText("Fix the flaky test");
    expect(screen.getByText("Debug session")).toBeInTheDocument();
    expect(screen.getByText("session-target-9")).toBeInTheDocument();
    expect(screen.getByText("session-parent-1")).toBeInTheDocument();
    expect(screen.getByText("nightly")).toBeInTheDocument();
    expect(screen.getByText("team-7")).toBeInTheDocument();
    expect(screen.getByText("reviewer")).toBeInTheDocument();
    expect(screen.getByText("call-3")).toBeInTheDocument();
    expect(screen.getByText("session-origin-4")).toBeInTheDocument();
    expect(screen.getByText("Branch index").nextSibling).toHaveTextContent("2");
    await user.click(
      screen.getByRole("button", { name: "Copy Debug target ID" }),
    );
    expect(writeText).toHaveBeenCalledWith("session-target-9");
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Debug target ID copied"),
    );
  });

  it("shows the id but disables Copy with the reason when the daemon denies copy_id", async () => {
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
        extras={{ canCopyId: false, copyIdReason: "inspect_only_kind" }}
      />,
    );
    await screen.findByText("Fix the flaky test");
    expect(screen.getByText("session-abc-123")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Copy session ID" }),
    ).toBeDisabled();
    expect(
      screen.getByText("Copying is unavailable: inspect_only_kind"),
    ).toBeInTheDocument();
  });

  it("falls back to a plain sentence when copy_id is denied without a reason", async () => {
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
        extras={{ canCopyId: false }}
      />,
    );
    await screen.findByText("Fix the flaky test");
    expect(
      screen.getByText(`Copying is unavailable: ${COPY_ID_DENIED_FALLBACK}`),
    ).toBeInTheDocument();
  });

  it("shows the inventory's last-write time when the workspace passes it", async () => {
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
        extras={{ updatedAt: Date.now() - 5 * 60_000 }}
      />,
    );
    await screen.findByText("Fix the flaky test");
    expect(screen.getByText("Last updated").nextSibling).toHaveTextContent(
      /\(5m ago\)$/,
    );
  });

  it("reads 'No filesystem' for a no-fs placement", async () => {
    identity.fetch.mockResolvedValue({
      ...FULL,
      placement: { kind: "no-fs", label: "", branch: "", revision: "" },
    });
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
      />,
    );
    await screen.findByText("Fix the flaky test");
    expect(screen.getByText("No filesystem")).toBeInTheDocument();
    expect(screen.queryByText("Branch")).toBeNull();
  });

  it("offers Retry after a failed read and re-fetches on click", async () => {
    const user = userEvent.setup();
    identity.fetch.mockRejectedValueOnce(new Error("daemon unreachable"));
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
      />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "daemon unreachable",
    );
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Fix the flaky test")).toBeInTheDocument();
    expect(identity.fetch).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("copies the exact session id and confirms it", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
      />,
    );
    await screen.findByText("Fix the flaky test");
    await user.click(screen.getByRole("button", { name: "Copy session ID" }));
    expect(writeText).toHaveBeenCalledWith("session-abc-123");
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Session ID copied"),
    );
    expect(screen.getByText("Copied")).toBeInTheDocument();
  });

  it("omits placement rows when the daemon reports none", async () => {
    identity.fetch.mockResolvedValue({
      ...FULL,
      placement: null,
      resolvedModel: null,
    });
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
      />,
    );
    await screen.findByText("Fix the flaky test");
    expect(screen.queryByText("Placement")).toBeNull();
    expect(screen.queryByText("Branch")).toBeNull();
    expect(screen.getAllByText("unavailable").length).toBeGreaterThan(0);
  });

  it("shows the daemon's refusal as an alert", async () => {
    identity.fetch.mockRejectedValue(new Error("session not found"));
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open
        onOpenChange={() => {}}
      />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not load the session: session not found",
    );
  });

  it("fetches nothing while closed", () => {
    render(
      <SessionDetailsDialog
        sessionId="session-abc-123"
        open={false}
        onOpenChange={() => {}}
      />,
    );
    expect(identity.fetch).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
