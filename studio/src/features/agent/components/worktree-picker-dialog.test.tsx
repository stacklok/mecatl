import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps } from "react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { HarnessWorktree } from "@/lib/harness/worktrees";
import {
  NO_ELIGIBLE_WORKTREES,
  WORKTREE_LIST_CHANGED,
  WORKTREE_SOURCE_BUSY,
  WORKTREE_SWITCH_CONFIRM,
  WorktreePickerDialog,
} from "./worktree-picker-dialog";

/**
 * Pins the `/worktrees` picker: it lists the daemon's eligible worktrees on
 * open (label, branch, short revision, bare, the current one marked), keeps
 * Confirm disabled until a pick, mints the successor through clear (default)
 * or fork with the OPAQUE selector, hands the new id to the owner, and on a
 * stale selector relists without closing.
 */

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  switchTo: vi.fn(),
  identity: vi.fn(),
}));

vi.mock("@/lib/harness/worktrees", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/worktrees")>()),
  fetchHarnessWorktrees: mocks.list,
  switchHarnessSessionWorktree: mocks.switchTo,
}));

vi.mock("@/lib/harness/sessions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/sessions")>()),
  fetchHarnessSessionIdentity: mocks.identity,
}));

const MAIN: HarnessWorktree = {
  selector: "wt-main",
  kind: "git-worktree",
  label: "main",
  branch: "main",
  revision: "abcdef0123456789",
  bare: false,
};
const FEATURE: HarnessWorktree = {
  selector: "wt-feature",
  kind: "git-worktree",
  label: "feature-x",
  branch: "feature/x",
  revision: "0123456789abcdef",
  bare: false,
};
const BARE: HarnessWorktree = {
  selector: "wt-bare",
  kind: "git-worktree",
  label: "archive",
  branch: "",
  revision: "",
  bare: true,
};

beforeEach(() => {
  mocks.list.mockReset();
  mocks.switchTo.mockReset();
  mocks.identity.mockReset();
  mocks.list.mockResolvedValue([MAIN, FEATURE, BARE]);
  mocks.identity.mockResolvedValue({
    id: "s1",
    placement: { kind: "worktree", label: "main", branch: "main" },
  });
});

type PickerProps = ComponentProps<typeof WorktreePickerDialog>;

function renderPicker() {
  const onSwitched = vi.fn<PickerProps["onSwitched"]>();
  const onOpenChange = vi.fn<PickerProps["onOpenChange"]>();
  render(
    <WorktreePickerDialog
      sessionId="s1"
      sessionTitle="Fix the flaky test"
      open
      onOpenChange={onOpenChange}
      onSwitched={onSwitched}
    />,
  );
  return { onSwitched, onOpenChange };
}

const confirmButton = () =>
  screen.getByRole("button", { name: WORKTREE_SWITCH_CONFIRM });

describe("WorktreePickerDialog", () => {
  it("lists the worktrees with branch, short revision, bare and the current mark", async () => {
    renderPicker();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Listing worktrees…");
    const feature = await screen.findByRole("radio", { name: /feature-x/ });
    expect(mocks.list).toHaveBeenCalledWith("s1", expect.any(AbortSignal));
    expect(feature).not.toBeChecked();
    expect(screen.getByText("feature/x")).toBeInTheDocument();
    // Revisions abbreviate to seven characters, the way git shows them.
    expect(screen.getByText("abcdef0")).toBeInTheDocument();
    expect(screen.getByText("0123456")).toBeInTheDocument();
    expect(screen.getByText("bare")).toBeInTheDocument();
    // The row whose label matches the chat's placement is marked current —
    // and only that row. The mark is part of the radio's accessible name.
    const main = screen.getByRole("radio", { name: /^main main abcdef0/ });
    expect(main.closest("label")).toHaveTextContent("current");
    expect(feature.closest("label")).not.toHaveTextContent("current");
    expect(screen.getAllByText("current")).toHaveLength(1);
    // Nothing picked yet: Confirm is disabled.
    expect(confirmButton()).toBeDisabled();
    // Clear is the default mode.
    expect(
      screen.getByRole("radio", { name: "Start fresh there" }),
    ).toBeChecked();
  });

  it("clear mode (default) mints the successor with the selector and hands over the id", async () => {
    const user = userEvent.setup();
    mocks.switchTo.mockResolvedValue("s2");
    const { onSwitched, onOpenChange } = renderPicker();
    await user.click(await screen.findByRole("radio", { name: /feature-x/ }));
    expect(confirmButton()).toBeEnabled();
    await user.click(confirmButton());
    await waitFor(() => expect(onSwitched).toHaveBeenCalledTimes(1));
    expect(mocks.switchTo).toHaveBeenCalledWith(
      "s1",
      "wt-feature",
      "clear",
      "Fix the flaky test",
    );
    expect(onSwitched).toHaveBeenCalledWith("s2", FEATURE, "clear");
    expect(toast.success).toHaveBeenCalledWith(
      "Now working in feature-x (feature/x)",
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("fork mode carries the conversation", async () => {
    const user = userEvent.setup();
    mocks.switchTo.mockResolvedValue("s3");
    const { onSwitched } = renderPicker();
    await user.click(await screen.findByRole("radio", { name: /archive/ }));
    await user.click(
      screen.getByRole("radio", { name: "Bring this conversation" }),
    );
    await user.click(confirmButton());
    await waitFor(() => expect(onSwitched).toHaveBeenCalledTimes(1));
    expect(mocks.switchTo).toHaveBeenCalledWith(
      "s1",
      "wt-bare",
      "fork",
      "Fix the flaky test",
    );
    expect(onSwitched).toHaveBeenCalledWith("s3", BARE, "fork");
    // A branchless (bare) worktree's toast names only the label.
    expect(toast.success).toHaveBeenCalledWith("Now working in archive");
  });

  it("a stale selector relists, resets the pick and keeps the dialog open", async () => {
    const user = userEvent.setup();
    mocks.switchTo.mockRejectedValue(
      new HarnessApiError(400, "placement_selector_stale", "expired"),
    );
    const { onSwitched, onOpenChange } = renderPicker();
    await user.click(await screen.findByRole("radio", { name: /feature-x/ }));
    await user.click(confirmButton());
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(WORKTREE_LIST_CHANGED),
    );
    await waitFor(() => expect(mocks.list).toHaveBeenCalledTimes(2));
    expect(onSwitched).not.toHaveBeenCalled();
    expect(onOpenChange).not.toHaveBeenCalled();
    // Back to no pick: the relisted rows are unchecked and Confirm is off.
    const relisted = await screen.findByRole("radio", { name: /feature-x/ });
    expect(relisted).not.toBeChecked();
    expect(confirmButton()).toBeDisabled();
  });

  it("a busy source (412) is explained inline and the dialog stays open", async () => {
    const user = userEvent.setup();
    mocks.switchTo.mockRejectedValue(
      new HarnessApiError(412, "failed_precondition", "session is running"),
    );
    const { onOpenChange } = renderPicker();
    await user.click(await screen.findByRole("radio", { name: /feature-x/ }));
    await user.click(confirmButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(
      WORKTREE_SOURCE_BUSY,
    );
    expect(onOpenChange).not.toHaveBeenCalled();
    // The pick survives, so the user can retry once the run settles.
    expect(confirmButton()).toBeEnabled();
  });

  it("an empty list says nothing is eligible (a no-FS chat)", async () => {
    mocks.list.mockResolvedValue([]);
    renderPicker();
    expect(await screen.findByText(NO_ELIGIBLE_WORKTREES)).toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    expect(confirmButton()).toBeDisabled();
  });

  it("a failed list shows the daemon's words and Retry re-reads it", async () => {
    const user = userEvent.setup();
    mocks.list.mockRejectedValueOnce(
      new HarnessApiError(503, "draining", "restarting"),
    );
    renderPicker();
    expect(await screen.findByRole("alert")).toHaveTextContent("restarting");
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(
      await screen.findByRole("radio", { name: /feature-x/ }),
    ).toBeVisible();
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("loses only the current mark when the identity read fails", async () => {
    mocks.identity.mockRejectedValue(new Error("nope"));
    renderPicker();
    expect(
      await screen.findByRole("radio", { name: /feature-x/ }),
    ).toBeInTheDocument();
    expect(screen.queryByText("current")).not.toBeInTheDocument();
  });

  it("Cancel closes without calling the daemon", async () => {
    const user = userEvent.setup();
    const { onOpenChange } = renderPicker();
    await screen.findByRole("radio", { name: /feature-x/ });
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(mocks.switchTo).not.toHaveBeenCalled();
  });
});
