import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSession } from "@/features/agent";
import { MOCK_TOUR_SESSION } from "@/features/agent/mock-tour";
import { COPY_SESSION_ID_LABEL } from "./session-copy-menu-items";
import {
  FORK_CHAT_LABEL,
  VIEW_TRANSCRIPT_LABEL,
} from "./session-row-action-items";
import {
  type ChatFolderActions,
  DELETE_FOLDER_LABEL,
  FolderGroupMenu,
  MOVE_TO_FOLDER_LABEL,
  NEW_FOLDER_LABEL,
  NO_FOLDER_LABEL,
  RENAME_FOLDER_LABEL,
  type SessionActions,
  SessionList,
  SidebarGroup,
} from "./session-sidebar";

/**
 * The sidebar row's context menu (the TUI's per-row `y` / `v` / `f` / `r` /
 * `d`): opening a row's "…" menu lists Copy session ID, View transcript, Fork
 * chat, Rename and Delete; each is gated on the daemon row's capabilities
 * with the daemon's reason inline; the row actions receive the row's id.
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
    canRename: true,
    canDelete: true,
    canCopyId: true,
    canViewTranscript: true,
    canFork: true,
    ...overrides,
  };
}

function actions(overrides: Partial<SessionActions> = {}): SessionActions {
  return {
    onRename: vi.fn(),
    onDelete: vi.fn(),
    onViewTranscript: vi.fn(),
    onFork: vi.fn(),
    ...overrides,
  };
}

async function openRowMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    screen.getByRole("button", {
      name: "Options for chat: Fix the flaky test",
    }),
  );
  return screen.getByRole("menu");
}

beforeEach(() => {
  clipboard.copy.mockReset();
  clipboard.copy.mockResolvedValue(true);
});

describe("SessionList row menu", () => {
  it("lists copy ID, view transcript, fork, rename and delete for an eligible row", async () => {
    const user = userEvent.setup();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions()}
      />,
    );
    const menu = await openRowMenu(user);
    const labels = within(menu)
      .getAllByRole("menuitem")
      .map((item) => item.textContent);
    expect(labels).toEqual([
      COPY_SESSION_ID_LABEL,
      VIEW_TRANSCRIPT_LABEL,
      FORK_CHAT_LABEL,
      "Rename",
      "Delete",
    ]);
    for (const item of within(menu).getAllByRole("menuitem")) {
      expect(item).not.toHaveAttribute("aria-disabled", "true");
    }
  });

  it("copies the row's exact id from the menu", async () => {
    const user = userEvent.setup();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions()}
      />,
    );
    const menu = await openRowMenu(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: COPY_SESSION_ID_LABEL }),
    );
    expect(clipboard.copy).toHaveBeenCalledWith("session-abc", "Session ID");
  });

  it("hands the row's id to View transcript and Fork without selecting the row", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const rowActions = actions();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={onSelect}
        actions={rowActions}
      />,
    );
    let menu = await openRowMenu(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: VIEW_TRANSCRIPT_LABEL }),
    );
    expect(rowActions.onViewTranscript).toHaveBeenCalledWith("session-abc");
    menu = await openRowMenu(user);
    await user.click(
      within(menu).getByRole("menuitem", { name: FORK_CHAT_LABEL }),
    );
    expect(rowActions.onFork).toHaveBeenCalledWith("session-abc");
    // Neither action opened the chat: the row stays a read-only target.
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("disables Fork and View transcript with the daemon's reasons when the row denies them", async () => {
    const user = userEvent.setup();
    render(
      <SessionList
        sessions={[
          session({
            canFork: false,
            forkReason: "active_elsewhere",
            canViewTranscript: false,
            viewTranscriptReason: "transcript_unavailable",
          }),
        ]}
        selectedId=""
        onSelect={() => {}}
        actions={actions()}
      />,
    );
    const menu = await openRowMenu(user);
    const fork = within(menu).getByRole("menuitem", { name: /Fork chat/ });
    expect(fork).toHaveAttribute("aria-disabled", "true");
    expect(fork).toHaveTextContent("Running in another client");
    const view = within(menu).getByRole("menuitem", {
      name: /View transcript/,
    });
    expect(view).toHaveAttribute("aria-disabled", "true");
    expect(view).toHaveTextContent("Transcript unavailable");
  });

  it("omits View transcript and Fork when the workspace offers no handler", async () => {
    const user = userEvent.setup();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions({ onViewTranscript: undefined, onFork: undefined })}
      />,
    );
    const menu = await openRowMenu(user);
    expect(
      within(menu).queryByRole("menuitem", { name: /View transcript/ }),
    ).toBeNull();
    expect(
      within(menu).queryByRole("menuitem", { name: /Fork chat/ }),
    ).toBeNull();
    expect(
      within(menu).getByRole("menuitem", { name: "Rename" }),
    ).toBeVisible();
  });

  it("never offers Fork on an AI-debug row", async () => {
    const user = userEvent.setup();
    render(
      <SessionList
        sessions={[session({ debugTargetSessionId: "session-target" })]}
        selectedId=""
        onSelect={() => {}}
        actions={actions()}
      />,
    );
    const menu = await openRowMenu(user);
    expect(
      within(menu).queryByRole("menuitem", { name: /Fork chat/ }),
    ).toBeNull();
    // Its transcript is still viewable, and the target id copyable.
    expect(
      within(menu).getByRole("menuitem", { name: VIEW_TRANSCRIPT_LABEL }),
    ).toBeVisible();
    expect(
      within(menu).getByRole("menuitem", { name: "Copy debug target ID" }),
    ).toBeVisible();
  });
});

/**
 * Chat folders (Studio-owned, browser-local): the row menu gains a "Move to
 * folder" submenu — every folder with the chat's current one checked, "No
 * folder", then "New folder…" — and a folder's group header carries a "…"
 * menu with Rename folder / Delete folder. Both hand back ids only; the
 * store and the dialogs live in the workspace.
 */

function folderActions(
  overrides: Partial<ChatFolderActions> = {},
): ChatFolderActions {
  return {
    folders: [
      { id: "f-work", name: "Work" },
      { id: "f-home", name: "Home" },
    ],
    assignments: { "session-abc": "f-home" },
    onMove: vi.fn(),
    onMoveToNew: vi.fn(),
    onRename: vi.fn(),
    onDelete: vi.fn(),
    ...overrides,
  };
}

async function openMoveToFolder(user: ReturnType<typeof userEvent.setup>) {
  const menu = await openRowMenu(user);
  await user.click(
    within(menu).getByRole("menuitem", { name: MOVE_TO_FOLDER_LABEL }),
  );
  return screen.findByRole("menu", { name: MOVE_TO_FOLDER_LABEL });
}

describe("SessionList Move to folder", () => {
  it("lists the folders with the chat's current one checked, then No folder and New folder…", async () => {
    const user = userEvent.setup();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions({ folders: folderActions() })}
      />,
    );
    const sub = await openMoveToFolder(user);
    const choices = within(sub).getAllByRole("menuitemcheckbox");
    expect(
      choices.map((c) => [c.textContent, c.getAttribute("aria-checked")]),
    ).toEqual([
      ["Work", "false"],
      ["Home", "true"],
      [NO_FOLDER_LABEL, "false"],
    ]);
    expect(
      within(sub).getByRole("menuitem", { name: NEW_FOLDER_LABEL }),
    ).toBeVisible();
  });

  it("checks No folder for an unfiled chat", async () => {
    const user = userEvent.setup();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions({ folders: folderActions({ assignments: {} }) })}
      />,
    );
    const sub = await openMoveToFolder(user);
    expect(
      within(sub).getByRole("menuitemcheckbox", { name: NO_FOLDER_LABEL }),
    ).toHaveAttribute("aria-checked", "true");
    expect(
      within(sub).getByRole("menuitemcheckbox", { name: "Home" }),
    ).toHaveAttribute("aria-checked", "false");
  });

  it("moves the chat to the picked folder, to no folder, or to a new one", async () => {
    const user = userEvent.setup();
    const folders = folderActions();
    const onSelect = vi.fn();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={onSelect}
        actions={actions({ folders })}
      />,
    );
    // A plain click, not a pointer move: Radix keeps a submenu open across
    // the move from its trigger only inside a layout-derived "grace area",
    // and jsdom has no layout, so userEvent's pointer path would close it.
    let sub = await openMoveToFolder(user);
    fireEvent.click(
      within(sub).getByRole("menuitemcheckbox", { name: "Work" }),
    );
    expect(folders.onMove).toHaveBeenCalledWith("session-abc", "f-work");

    sub = await openMoveToFolder(user);
    fireEvent.click(
      within(sub).getByRole("menuitemcheckbox", { name: NO_FOLDER_LABEL }),
    );
    expect(folders.onMove).toHaveBeenCalledWith("session-abc", null);

    sub = await openMoveToFolder(user);
    fireEvent.click(
      within(sub).getByRole("menuitem", { name: NEW_FOLDER_LABEL }),
    );
    expect(folders.onMoveToNew).toHaveBeenCalledWith("session-abc");
    // Filing a chat never opens it.
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("offers no folder submenu when the workspace passes none, nor on the mock tour row", async () => {
    const user = userEvent.setup();
    const { unmount } = render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions()}
      />,
    );
    let menu = await openRowMenu(user);
    expect(
      within(menu).queryByRole("menuitem", { name: MOVE_TO_FOLDER_LABEL }),
    ).toBeNull();
    await user.keyboard("{Escape}");
    unmount();

    render(
      <SessionList
        sessions={[session({ id: MOCK_TOUR_SESSION.id })]}
        selectedId=""
        onSelect={() => {}}
        actions={actions({ folders: folderActions() })}
      />,
    );
    menu = await openRowMenu(user);
    expect(
      within(menu).queryByRole("menuitem", { name: MOVE_TO_FOLDER_LABEL }),
    ).toBeNull();
    expect(
      within(menu).getByRole("menuitem", { name: "Rename" }),
    ).toBeVisible();
  });
});

describe("FolderGroupMenu", () => {
  it("renames or deletes the folder by id from the group header", async () => {
    const user = userEvent.setup();
    const folders = folderActions();
    render(
      <SidebarGroup
        label="Work"
        menu={
          <FolderGroupMenu folderId="f-work" name="Work" actions={folders} />
        }
      >
        <div />
      </SidebarGroup>,
    );
    expect(screen.getByText("Work")).toBeVisible();
    const trigger = () =>
      screen.getByRole("button", { name: "Options for folder: Work" });

    await user.click(trigger());
    const labels = screen
      .getAllByRole("menuitem")
      .map((item) => item.textContent);
    expect(labels).toEqual([RENAME_FOLDER_LABEL, DELETE_FOLDER_LABEL]);
    await user.click(
      screen.getByRole("menuitem", { name: RENAME_FOLDER_LABEL }),
    );
    expect(folders.onRename).toHaveBeenCalledWith("f-work");

    await user.click(trigger());
    await user.click(
      screen.getByRole("menuitem", { name: DELETE_FOLDER_LABEL }),
    );
    expect(folders.onDelete).toHaveBeenCalledWith("f-work");
  });

  it("renders a plain heading when a group has no menu", () => {
    render(
      <SidebarGroup label="Today">
        <div />
      </SidebarGroup>,
    );
    expect(screen.getByText("Today")).toBeVisible();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

/**
 * The touch long-press bottom sheet (phones have no hover "…"): a hold on
 * the row opens it, and "Move to folder" swaps it to a one-tap picker —
 * the folders with the current one marked, "No folder", "New folder…" —
 * so a chat can be filed without a pointer. Hidden without folder actions
 * and on the mock tour row, exactly like the menu.
 */

/** A ~450 ms touch hold on the row (mouse pointers never open the sheet). */
async function openSheet() {
  const rowButton = screen.getByRole("button", {
    name: "Open chat: Fix the flaky test",
  });
  fireEvent.pointerDown(rowButton, {
    pointerType: "touch",
    clientX: 10,
    clientY: 10,
  });
  const sheet = await screen.findByRole("dialog");
  fireEvent.pointerUp(rowButton, { pointerType: "touch" });
  return sheet;
}

describe("SessionActionsSheet Move to folder", () => {
  it("opens a picker from the sheet with the chat's current folder marked", async () => {
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions({ folders: folderActions() })}
      />,
    );
    const sheet = await openSheet();
    expect(within(sheet).getByText("Fix the flaky test")).toBeVisible();
    fireEvent.click(
      within(sheet).getByRole("button", { name: MOVE_TO_FOLDER_LABEL }),
    );
    expect(
      screen.getByRole("dialog", { name: MOVE_TO_FOLDER_LABEL }),
    ).toBeVisible();
    const marked = (name: string) =>
      within(sheet).getByRole("button", { name }).getAttribute("aria-current");
    expect(marked("Work")).toBeNull();
    expect(marked("Home")).toBe("true");
    expect(marked(NO_FOLDER_LABEL)).toBeNull();
    expect(
      within(sheet).getByRole("button", { name: NEW_FOLDER_LABEL }),
    ).toBeVisible();
    // Back returns to the chat's actions without filing anything.
    fireEvent.click(
      within(sheet).getByRole("button", { name: "Back to chat options" }),
    );
    expect(
      within(sheet).getByRole("button", { name: MOVE_TO_FOLDER_LABEL }),
    ).toBeVisible();
    expect(within(sheet).queryByRole("button", { name: "Home" })).toBeNull();
  });

  it("files the chat with one tap, closes the sheet, and never opens the chat", async () => {
    const folders = folderActions();
    const onSelect = vi.fn();
    render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={onSelect}
        actions={actions({ folders })}
      />,
    );
    const toPicker = async () => {
      const sheet = await openSheet();
      fireEvent.click(
        within(sheet).getByRole("button", { name: MOVE_TO_FOLDER_LABEL }),
      );
      return sheet;
    };

    let sheet = await toPicker();
    fireEvent.click(within(sheet).getByRole("button", { name: "Work" }));
    expect(folders.onMove).toHaveBeenCalledWith("session-abc", "f-work");
    expect(screen.queryByRole("dialog")).toBeNull();

    sheet = await toPicker();
    fireEvent.click(
      within(sheet).getByRole("button", { name: NO_FOLDER_LABEL }),
    );
    expect(folders.onMove).toHaveBeenCalledWith("session-abc", null);

    sheet = await toPicker();
    fireEvent.click(
      within(sheet).getByRole("button", { name: NEW_FOLDER_LABEL }),
    );
    expect(folders.onMoveToNew).toHaveBeenCalledWith("session-abc");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("offers no Move to folder without folder actions, nor on the mock tour row", async () => {
    const { unmount } = render(
      <SessionList
        sessions={[session()]}
        selectedId=""
        onSelect={() => {}}
        actions={actions()}
      />,
    );
    let sheet = await openSheet();
    expect(
      within(sheet).queryByRole("button", { name: MOVE_TO_FOLDER_LABEL }),
    ).toBeNull();
    expect(within(sheet).getByRole("button", { name: "Rename" })).toBeVisible();
    unmount();

    render(
      <SessionList
        sessions={[session({ id: MOCK_TOUR_SESSION.id })]}
        selectedId=""
        onSelect={() => {}}
        actions={actions({ folders: folderActions() })}
      />,
    );
    sheet = await openSheet();
    expect(
      within(sheet).queryByRole("button", { name: MOVE_TO_FOLDER_LABEL }),
    ).toBeNull();
  });
});
