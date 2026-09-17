import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { AgentSession } from "@/features/agent";
import { MOCK_TOUR_SESSION } from "@/features/agent/mock-tour";
import {
  COPY_DEBUG_TARGET_LABEL,
  COPY_ID_NOT_OFFERED,
  COPY_SESSION_ID_LABEL,
  CopyDebugTargetMenuItem,
  CopyDebugTargetSheetItem,
  CopySessionIdMenuItem,
  CopySessionIdSheetItem,
} from "./session-copy-menu-items";

/**
 * Pins the sidebar row's copy items: "Copy session ID" writes the exact id
 * and is gated on the row's `copy_id` capability (disabled with the daemon's
 * reason, or a plain fallback when it gave none); "Copy debug target ID"
 * appears only on an AI-debug row and copies the diagnosed session's id;
 * neither appears on the mock tour row.
 */

const clipboard = vi.hoisted(() => ({ copy: vi.fn() }));
vi.mock("@/lib/clipboard", () => ({ copyToClipboard: clipboard.copy }));

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
    canCopyId: true,
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

beforeEach(() => {
  clipboard.copy.mockReset();
  clipboard.copy.mockResolvedValue(true);
});

describe("CopySessionIdMenuItem", () => {
  it("copies the exact session id", async () => {
    const user = userEvent.setup();
    inMenu(<CopySessionIdMenuItem session={session()} />);
    await user.click(
      screen.getByRole("menuitem", { name: COPY_SESSION_ID_LABEL }),
    );
    expect(clipboard.copy).toHaveBeenCalledWith("session-abc", "Session ID");
  });

  it("is disabled with the daemon's reason when copy_id is denied", () => {
    inMenu(
      <CopySessionIdMenuItem
        session={session({ canCopyId: false, copyIdReason: "inspect_only" })}
      />,
    );
    const item = screen.getByRole("menuitem", { name: /Copy session ID/ });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("inspect_only")).toBeInTheDocument();
  });

  it("names the missing capability plainly when the daemon gave no reason", () => {
    inMenu(<CopySessionIdMenuItem session={session({ canCopyId: false })} />);
    expect(screen.getByText(COPY_ID_NOT_OFFERED)).toBeInTheDocument();
  });

  it("renders nothing for the mock tour row", () => {
    inMenu(<CopySessionIdMenuItem session={MOCK_TOUR_SESSION} />);
    expect(screen.queryByRole("menuitem")).toBeNull();
  });
});

describe("CopyDebugTargetMenuItem", () => {
  it("copies the debug target id on an AI-debug row only", async () => {
    const user = userEvent.setup();
    const { unmount } = inMenu(
      <CopyDebugTargetMenuItem
        session={session({ debugTargetSessionId: "session-target" })}
      />,
    );
    await user.click(
      screen.getByRole("menuitem", { name: COPY_DEBUG_TARGET_LABEL }),
    );
    expect(clipboard.copy).toHaveBeenCalledWith(
      "session-target",
      "Debug target ID",
    );
    unmount();
    inMenu(<CopyDebugTargetMenuItem session={session()} />);
    expect(screen.queryByRole("menuitem")).toBeNull();
  });
});

describe("sheet items", () => {
  it("copy and then close the sheet", async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    render(
      <>
        <CopySessionIdSheetItem session={session()} onDone={onDone} />
        <CopyDebugTargetSheetItem
          session={session({ debugTargetSessionId: "session-target" })}
          onDone={onDone}
        />
      </>,
    );
    await user.click(
      screen.getByRole("button", { name: COPY_SESSION_ID_LABEL }),
    );
    await user.click(
      screen.getByRole("button", { name: COPY_DEBUG_TARGET_LABEL }),
    );
    expect(clipboard.copy).toHaveBeenNthCalledWith(
      1,
      "session-abc",
      "Session ID",
    );
    expect(clipboard.copy).toHaveBeenNthCalledWith(
      2,
      "session-target",
      "Debug target ID",
    );
    expect(onDone).toHaveBeenCalledTimes(2);
  });

  it("disables the id copy when the row denies it", () => {
    render(
      <CopySessionIdSheetItem
        session={session({ canCopyId: false, copyIdReason: "inspect_only" })}
      />,
    );
    expect(
      screen.getByRole("button", { name: /Copy session ID/ }),
    ).toBeDisabled();
  });
});
