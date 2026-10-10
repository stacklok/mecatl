// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the chat list's row behaviour before its actions move into the
 * prototype's row menu: selecting a row, inline rename, delete, filing into
 * a folder, and the inspect-only group. A row's actions are reached through
 * `rowAction`, which takes whichever control the row offers (an icon button
 * or a menu item), so the outcomes stay pinned across the move.
 */

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import type { ChatFolderState } from "./chat-folders";
import { SessionSidebar } from "./session-sidebar";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function row(
  id: string,
  title: string,
  overrides: Partial<SessionSummaryResponse["capabilities"]> = {},
  extra: Partial<SessionSummaryResponse> = {},
): SessionSummaryResponse {
  return {
    capabilities: {
      copyId: true,
      copyIdReason: "",
      delete: true,
      deleteReason: "",
      fork: true,
      forkReason: "",
      inspect: true,
      inspectReason: "",
      publicChat: true,
      publicChatReason: "",
      rename: true,
      renameReason: "",
      viewTranscript: true,
      viewTranscriptReason: "",
      ...overrides,
    },
    createdAt: "2026-09-24T12:00:00.000Z",
    debugTargetSessionId: "",
    id,
    kind: "main",
    modelId: "model-a",
    state: "idle",
    title,
    titleProvenance: "",
    titleRevision: "0",
    turns: 2,
    updatedAt: "2026-09-24T12:00:00.000Z",
    ...extra,
  };
}

const inspectOnly = row(
  "worker",
  "Recorded worker",
  {
    delete: false,
    deleteReason: "inspect_only_kind",
    fork: false,
    forkReason: "inspect_only_kind",
    publicChat: false,
    publicChatReason: "inspect_only_kind",
    rename: false,
    renameReason: "inspect_only_kind",
  },
  { kind: "subagent" },
);

const folders: ChatFolderState = {
  assignments: {},
  folders: [{ id: "f1", name: "Work" }],
};

function renderSidebar(items: SessionSummaryResponse[]) {
  const props = {
    collapsed: false,
    creating: false,
    folders,
    items,
    onClose: vi.fn(),
    onCreate: vi.fn(),
    onCreateFolder: vi.fn(),
    onDelete: vi.fn(),
    onDeleteFolder: vi.fn(),
    onMoveToFolder: vi.fn(),
    onRename: vi.fn(),
    onRenameFolder: vi.fn(),
    onSelect: vi.fn(),
    open: true,
    selectedId: "a",
    side: "right" as const,
  };
  render(<SessionSidebar {...props} />);
  return props;
}

type Action = "Rename" | "Delete" | "Move to folder";

/** Opens a row's action, whether the row shows it as an icon button or inside its options menu. */
async function rowAction(user: ReturnType<typeof userEvent.setup>, title: string, action: Action) {
  const direct =
    action === "Move to folder"
      ? screen.queryByLabelText(`Move ${title} to folder`)
      : screen.queryByRole("button", { name: `${action} ${title}` });
  if (direct) {
    await user.click(direct);
    return;
  }
  await user.click(screen.getByRole("button", { name: `Options for chat: ${title}` }));
  const item = await screen.findByRole("menuitem", { name: action });
  item.focus();
  await user.keyboard(action === "Move to folder" ? "{ArrowRight}" : "{Enter}");
}

/** Picks a destination in the move-to-folder chooser, a button or a checkbox item. */
async function chooseFolder(user: ReturnType<typeof userEvent.setup>, name: string) {
  const option =
    screen.queryByRole("menuitemcheckbox", { name }) ??
    screen.queryByRole("menuitem", { name }) ??
    screen.getByRole("button", { name });
  // Keyboard selection works for a plain button and a submenu item alike.
  option.focus();
  await user.keyboard("{Enter}");
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
});

describe("chat list row pins", () => {
  it("selects a row and marks the open chat", () => {
    const props = renderSidebar([row("a", "Alpha"), row("b", "Beta")]);
    const alpha = screen.getByRole("button", { name: /^Alpha/ });
    expect(alpha.getAttribute("aria-current")).toBe("page");
    fireEvent.click(screen.getByRole("button", { name: /^Beta/ }));
    expect(props.onSelect).toHaveBeenCalledWith("b");
  });

  it("renames a row inline and submits the trimmed title", async () => {
    const user = userEvent.setup();
    const props = renderSidebar([row("a", "Alpha")]);
    await rowAction(user, "Alpha", "Rename");
    const input = await screen.findByRole("textbox", { name: "Chat title" });
    await waitFor(() => expect(document.activeElement).toBe(input));
    fireEvent.change(input, { target: { value: "  Renamed  " } });
    fireEvent.submit(input.closest("form") as HTMLFormElement);
    expect(props.onRename).toHaveBeenCalledWith(expect.objectContaining({ id: "a" }), "Renamed");
  });

  it("deletes a row through the owner's confirm", async () => {
    const user = userEvent.setup();
    const props = renderSidebar([row("a", "Alpha")]);
    await rowAction(user, "Alpha", "Delete");
    expect(props.onDelete).toHaveBeenCalledWith(expect.objectContaining({ id: "a" }));
  });

  it("files a row into a folder, out of it, and into a new folder", async () => {
    const user = userEvent.setup();
    const props = renderSidebar([row("a", "Alpha")]);
    await rowAction(user, "Alpha", "Move to folder");
    await chooseFolder(user, "Work");
    expect(props.onMoveToFolder).toHaveBeenLastCalledWith("a", "f1");
    await rowAction(user, "Alpha", "Move to folder");
    await chooseFolder(user, "No folder");
    expect(props.onMoveToFolder).toHaveBeenLastCalledWith("a", undefined);
    await rowAction(user, "Alpha", "Move to folder");
    await chooseFolder(user, "New folder…");
    expect(props.onCreateFolder).toHaveBeenCalledWith("a");
  });

  it("keeps a denied rename and delete unavailable", async () => {
    const user = userEvent.setup();
    const props = renderSidebar([
      row("a", "Alpha", {
        delete: false,
        deleteReason: "storage_unsupported",
        rename: false,
        renameReason: "rename_denied",
      }),
    ]);
    await rowAction(user, "Alpha", "Rename").catch(() => undefined);
    expect(screen.queryByRole("textbox", { name: "Chat title" })).toBeNull();
    await rowAction(user, "Alpha", "Delete").catch(() => undefined);
    expect(props.onDelete).not.toHaveBeenCalled();
  });

  it("groups inspect-only rows apart, with no chat actions", () => {
    const props = renderSidebar([row("a", "Alpha"), inspectOnly]);
    const group = screen.getByRole("heading", { name: "Inspect-only sessions" }).closest("section");
    if (!group) throw new Error("Inspect-only group missing");
    expect(within(group).getByText("Recorded worker")).toBeTruthy();
    expect(within(group).queryByText("Alpha")).toBeNull();
    for (const name of [
      "Rename Recorded worker",
      "Delete Recorded worker",
      "Options for chat: Recorded worker",
    ]) {
      expect(within(group).queryByRole("button", { name })).toBeNull();
    }
    expect(within(group).queryByLabelText("Move Recorded worker to folder")).toBeNull();
    fireEvent.click(within(group).getByRole("button", { name: /Recorded worker/ }));
    expect(props.onSelect).toHaveBeenCalledWith("worker");
  });
});
