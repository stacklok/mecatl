import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { WorkspaceEnrollmentView } from "@/features/agent/hooks/use-workspace-enrollment";
import {
  NOT_IDLE_HINT,
  WorkspaceEnrollmentNotice,
} from "./workspace-enrollment-notice";

/**
 * The enrollment notice above the composer: one plain sentence per phase and
 * window state with the TUI's /tools-connect and /tools-cancel actions as
 * buttons, idle-only and single-flight; nothing rendered when the daemon
 * lacks the capability, the enrollment is settled connected / not required,
 * the probe has not landed, or the user dismissed the not-connected notice.
 */

function view(
  overrides: Partial<WorkspaceEnrollmentView> = {},
): WorkspaceEnrollmentView {
  return {
    supported: true,
    phase: "not_connected",
    enrollmentId: "",
    requiredServices: 0,
    outcome: "",
    error: null,
    busy: false,
    idle: true,
    dismissed: false,
    window: "elsewhere",
    connect: vi.fn(),
    retry: vi.fn(),
    cancel: vi.fn(),
    dismiss: vi.fn(),
    reopenWindow: vi.fn(),
    ...overrides,
  };
}

const notice = () => screen.queryByTestId("workspace-enrollment-notice");

describe("WorkspaceEnrollmentNotice", () => {
  it.each([
    ["unsupported", { supported: false }],
    ["probe pending", { phase: "unknown" as const }],
    ["connected", { phase: "connected" as const }],
    ["not required", { phase: "not_required" as const }],
    ["dismissed", { phase: "not_connected" as const, dismissed: true }],
  ])("renders nothing when %s", (_label, overrides) => {
    render(<WorkspaceEnrollmentNotice enrollment={view(overrides)} />);
    expect(notice()).toBeNull();
  });

  it("offers Connect and Dismiss while not connected", () => {
    const enrollment = view();
    render(<WorkspaceEnrollmentNotice enrollment={enrollment} />);
    expect(notice()).toHaveAttribute("data-phase", "not_connected");
    expect(
      screen.getByText(/Workspace services aren't connected/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(enrollment.connect).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(enrollment.dismiss).toHaveBeenCalledTimes(1);
  });

  it("disables the daemon-bound actions while the chat is busy, with the reason", () => {
    render(<WorkspaceEnrollmentNotice enrollment={view({ idle: false })} />);
    const connect = screen.getByRole("button", { name: "Connect" });
    expect(connect).toBeDisabled();
    expect(connect).toHaveAttribute("title", NOT_IDLE_HINT);
    // Dismiss is local: still available.
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeEnabled();
  });

  it("shows the in-flight label and blocks a second click while busy", () => {
    const enrollment = view({ busy: true });
    render(<WorkspaceEnrollmentNotice enrollment={enrollment} />);
    const connecting = screen.getByRole("button", { name: "Connecting…" });
    expect(connecting).toBeDisabled();
  });

  it("while pending with the window open, says where to finish and offers Cancel only", () => {
    const enrollment = view({
      phase: "pending",
      enrollmentId: "e1",
      window: "open",
    });
    render(<WorkspaceEnrollmentNotice enrollment={enrollment} />);
    expect(
      screen.getByText(/Finish connecting in the window that opened/),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("button")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(enrollment.cancel).toHaveBeenCalledTimes(1);
  });

  it("a blocked popup offers to open the connection window from a click", () => {
    const enrollment = view({
      phase: "pending",
      enrollmentId: "e1",
      window: "blocked",
    });
    render(<WorkspaceEnrollmentNotice enrollment={enrollment} />);
    expect(
      screen.getByText(/browser blocked the connection window/),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Open the connection window" }),
    );
    expect(enrollment.reopenWindow).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
  });

  it("a closed window offers to reopen it", () => {
    const enrollment = view({
      phase: "pending",
      enrollmentId: "e1",
      window: "closed",
    });
    render(<WorkspaceEnrollmentNotice enrollment={enrollment} />);
    expect(
      screen.getByText(/connection window closed before/),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Reopen the connection window" }),
    );
    expect(enrollment.reopenWindow).toHaveBeenCalledTimes(1);
  });

  it("an enrollment started elsewhere offers Check now, and Cancel only once an id is known", () => {
    const withoutId = view({ phase: "pending", window: "elsewhere" });
    const { rerender } = render(
      <WorkspaceEnrollmentNotice enrollment={withoutId} />,
    );
    expect(screen.getByText(/in progress elsewhere/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    expect(withoutId.connect).toHaveBeenCalledTimes(1);

    const withId = view({
      phase: "pending",
      window: "elsewhere",
      enrollmentId: "e9",
    });
    rerender(<WorkspaceEnrollmentNotice enrollment={withId} />);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
  });

  it("a failed enrollment names the daemon's status word and offers Retry and Cancel", () => {
    const enrollment = view({
      phase: "failed",
      enrollmentId: "e1",
      outcome: "denied",
    });
    render(<WorkspaceEnrollmentNotice enrollment={enrollment} />);
    expect(screen.getByText("Connection denied.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(enrollment.retry).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(enrollment.cancel).toHaveBeenCalledTimes(1);
  });

  it("a request failure reads 'Connection failed.' with the daemon's words underneath", () => {
    render(
      <WorkspaceEnrollmentNotice
        enrollment={view({
          phase: "failed",
          outcome: "",
          error: "workspace services are not configured",
        })}
      />,
    );
    expect(screen.getByText("Connection failed.")).toBeInTheDocument();
    expect(
      screen.getByText("workspace services are not configured"),
    ).toBeInTheDocument();
  });

  it("is a polite live region", () => {
    render(<WorkspaceEnrollmentNotice enrollment={view()} />);
    expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite");
  });
});
