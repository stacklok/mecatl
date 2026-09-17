import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  useWorktreeSwitch,
  type WorktreeSwitchDeps,
} from "./use-worktree-switch";

/**
 * Pins the workspace's worktree-switch gate: the header-menu opener exists
 * only when the daemon advertises `worktrees` AND the row's `fork` verdict is
 * on AND there is a daemon session; opening it renders the picker for that
 * session, and the picker's success hands the new id to the owner.
 */

vi.mock("@/features/agent/components/worktree-picker-dialog", () => ({
  WorktreePickerDialog: ({
    sessionId,
    sessionTitle,
    open,
    onOpenChange,
    onSwitched,
  }: {
    sessionId: string | null;
    sessionTitle: string;
    open: boolean;
    onOpenChange: (open: boolean) => void;
    onSwitched: (id: string) => void | Promise<void>;
  }) =>
    open ? (
      <div role="dialog">
        picker for {sessionId} ({sessionTitle})
        <button type="button" onClick={() => void onSwitched("s-new")}>
          pretend switch
        </button>
        <button type="button" onClick={() => onOpenChange(false)}>
          pretend close
        </button>
      </div>
    ) : null,
}));

function Harness(props: WorktreeSwitchDeps) {
  const { openWorktreePicker, worktreePickerDialog } = useWorktreeSwitch(props);
  return (
    <>
      {openWorktreePicker ? (
        <button type="button" onClick={openWorktreePicker}>
          Switch worktree…
        </button>
      ) : (
        <span>not offered</span>
      )}
      {worktreePickerDialog}
    </>
  );
}

const ROW = { id: "s1", title: "Fix the flaky test", canFork: true };

describe("useWorktreeSwitch", () => {
  it("offers the opener only with the capability, a session and the fork verdict", () => {
    const onSwitched = vi.fn();
    const { rerender } = render(
      <Harness session={ROW} supported onSwitched={onSwitched} />,
    );
    expect(
      screen.getByRole("button", { name: "Switch worktree…" }),
    ).toBeInTheDocument();

    rerender(
      <Harness session={ROW} supported={false} onSwitched={onSwitched} />,
    );
    expect(screen.getByText("not offered")).toBeInTheDocument();

    rerender(<Harness session={null} supported onSwitched={onSwitched} />);
    expect(screen.getByText("not offered")).toBeInTheDocument();

    rerender(
      <Harness
        session={{ ...ROW, canFork: false }}
        supported
        onSwitched={onSwitched}
      />,
    );
    expect(screen.getByText("not offered")).toBeInTheDocument();

    // An omitted verdict is a denial (never re-derived client-side).
    rerender(
      <Harness
        session={{ id: "s1", title: "x" }}
        supported
        onSwitched={onSwitched}
      />,
    );
    expect(screen.getByText("not offered")).toBeInTheDocument();
  });

  it("opens the picker for the selected chat and relays the new id", async () => {
    const user = userEvent.setup();
    const onSwitched = vi.fn();
    render(<Harness session={ROW} supported onSwitched={onSwitched} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Switch worktree…" }));
    expect(screen.getByRole("dialog")).toHaveTextContent(
      "picker for s1 (Fix the flaky test)",
    );
    await user.click(screen.getByRole("button", { name: "pretend switch" }));
    expect(onSwitched).toHaveBeenCalledWith("s-new");
    await user.click(screen.getByRole("button", { name: "pretend close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("closes the picker when the row stops being eligible", async () => {
    const user = userEvent.setup();
    const onSwitched = vi.fn();
    const { rerender } = render(
      <Harness session={ROW} supported onSwitched={onSwitched} />,
    );
    await user.click(screen.getByRole("button", { name: "Switch worktree…" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    rerender(<Harness session={null} supported onSwitched={onSwitched} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
