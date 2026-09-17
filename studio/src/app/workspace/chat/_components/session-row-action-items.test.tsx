import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { AgentSession } from "@/features/agent";
import { MOCK_TOUR_SESSION } from "@/features/agent/mock-tour";
import {
  FORK_CHAT_LABEL,
  FORK_NOT_OFFERED,
  ForkChatMenuItem,
  ForkChatSheetItem,
  forkGate,
  forkOffered,
  TRANSCRIPT_NOT_OFFERED,
  VIEW_TRANSCRIPT_LABEL,
  ViewTranscriptMenuItem,
  ViewTranscriptSheetItem,
  viewTranscriptGate,
} from "./session-row-action-items";

/**
 * Pins the two per-row actions the TUI's /sessions overlay has (`v` view
 * transcript, `f` fork): each is gated on the daemon row's capability with
 * the daemon's reason in plain words under a disabled item, Fork is never
 * offered on an AI-debug row, and neither appears on the mock tour row.
 */

function session(overrides: Partial<AgentSession> = {}): AgentSession {
  return {
    id: "session-abc",
    title: "Fix the flaky test",
    projectId: null,
    model: "m",
    createdAt: 1,
    updatedAt: 2,
    pinned: false,
    archived: false,
    messageCount: 0,
    isStreaming: false,
    inputTokens: 0,
    outputTokens: 0,
    unread: false,
    estimatedCost: null,
    contextLength: null,
    lastPromptTokens: null,
    thresholdTokens: null,
    canViewTranscript: true,
    canFork: true,
    ...overrides,
  };
}

function inMenu(children: React.ReactNode) {
  return render(
    <DropdownMenu open>
      <DropdownMenuTrigger>menu</DropdownMenuTrigger>
      <DropdownMenuContent>{children}</DropdownMenuContent>
    </DropdownMenu>,
  );
}

describe("gates", () => {
  it("read the row's capabilities and translate the closed reason codes", () => {
    expect(viewTranscriptGate(session())).toEqual({
      disabled: false,
      reason: "",
    });
    expect(
      viewTranscriptGate(
        session({
          canViewTranscript: false,
          viewTranscriptReason: "transcript_unavailable",
        }),
      ),
    ).toEqual({ disabled: true, reason: "Transcript unavailable" });
    // An omitted capability is a denial, with a plain floor for no reason.
    expect(
      viewTranscriptGate(session({ canViewTranscript: undefined })),
    ).toEqual({ disabled: true, reason: TRANSCRIPT_NOT_OFFERED });
    expect(forkGate(session())).toEqual({ disabled: false, reason: "" });
    expect(
      forkGate(session({ canFork: false, forkReason: "active_elsewhere" })),
    ).toEqual({ disabled: true, reason: "Running in another client" });
    expect(forkGate(session({ canFork: undefined }))).toEqual({
      disabled: true,
      reason: FORK_NOT_OFFERED,
    });
  });

  it("withholds Fork from an AI-debug row and the mock tour", () => {
    expect(forkOffered(session())).toBe(true);
    expect(
      forkOffered(session({ debugTargetSessionId: "session-target" })),
    ).toBe(false);
    expect(forkOffered(MOCK_TOUR_SESSION)).toBe(false);
  });
});

describe("ViewTranscriptMenuItem", () => {
  it("asks the owner to open the read-only transcript", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    inMenu(<ViewTranscriptMenuItem session={session()} onSelect={onSelect} />);
    await user.click(
      screen.getByRole("menuitem", { name: VIEW_TRANSCRIPT_LABEL }),
    );
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("is disabled with the daemon's reason when the row withholds it", () => {
    inMenu(
      <ViewTranscriptMenuItem
        session={session({
          canViewTranscript: false,
          viewTranscriptReason: "transcript_unavailable",
        })}
        onSelect={() => {}}
      />,
    );
    const item = screen.getByRole("menuitem", { name: /View transcript/ });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Transcript unavailable")).toBeInTheDocument();
  });

  it("renders nothing for the mock tour row", () => {
    inMenu(
      <ViewTranscriptMenuItem
        session={MOCK_TOUR_SESSION}
        onSelect={() => {}}
      />,
    );
    expect(screen.queryByRole("menuitem")).toBeNull();
  });
});

describe("ForkChatMenuItem", () => {
  it("asks the owner to fork", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    inMenu(<ForkChatMenuItem session={session()} onSelect={onSelect} />);
    await user.click(screen.getByRole("menuitem", { name: FORK_CHAT_LABEL }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("is disabled with the daemon's reason when fork is denied", () => {
    inMenu(
      <ForkChatMenuItem
        session={session({ canFork: false, forkReason: "awaiting_approval" })}
        onSelect={() => {}}
      />,
    );
    const item = screen.getByRole("menuitem", { name: /Fork chat/ });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Waiting for approval")).toBeInTheDocument();
  });

  it("names the missing capability plainly when the daemon gave no reason", () => {
    inMenu(
      <ForkChatMenuItem
        session={session({ canFork: false })}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText(FORK_NOT_OFFERED)).toBeInTheDocument();
  });

  it("renders nothing on an AI-debug row or the mock tour row", () => {
    const { unmount } = inMenu(
      <ForkChatMenuItem
        session={session({ debugTargetSessionId: "session-target" })}
        onSelect={() => {}}
      />,
    );
    expect(screen.queryByRole("menuitem")).toBeNull();
    unmount();
    inMenu(
      <ForkChatMenuItem session={MOCK_TOUR_SESSION} onSelect={() => {}} />,
    );
    expect(screen.queryByRole("menuitem")).toBeNull();
  });
});

describe("sheet items", () => {
  it("act and then close the sheet", async () => {
    const user = userEvent.setup();
    const onView = vi.fn();
    const onFork = vi.fn();
    const onDone = vi.fn();
    render(
      <>
        <ViewTranscriptSheetItem
          session={session()}
          onSelect={onView}
          onDone={onDone}
        />
        <ForkChatSheetItem
          session={session()}
          onSelect={onFork}
          onDone={onDone}
        />
      </>,
    );
    await user.click(
      screen.getByRole("button", { name: VIEW_TRANSCRIPT_LABEL }),
    );
    await user.click(screen.getByRole("button", { name: FORK_CHAT_LABEL }));
    expect(onView).toHaveBeenCalledTimes(1);
    expect(onFork).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledTimes(2);
  });

  it("are disabled buttons carrying the reason when the row denies them", () => {
    render(
      <>
        <ViewTranscriptSheetItem
          session={session({
            canViewTranscript: false,
            viewTranscriptReason: "inspect_only_kind",
          })}
          onSelect={() => {}}
        />
        <ForkChatSheetItem
          session={session({ canFork: false, forkReason: "active_elsewhere" })}
          onSelect={() => {}}
        />
      </>,
    );
    const view = screen.getByRole("button", { name: /View transcript/ });
    expect(view).toBeDisabled();
    expect(view).toHaveTextContent("Read-only run");
    const fork = screen.getByRole("button", { name: /Fork chat/ });
    expect(fork).toBeDisabled();
    expect(fork).toHaveTextContent("Running in another client");
  });
});
