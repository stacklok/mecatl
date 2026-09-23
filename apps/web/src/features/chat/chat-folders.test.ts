// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import {
  assignChatFolder,
  deleteChatFolder,
  groupSessions,
  parseChatFolders,
  useChatFolders,
} from "./chat-folders";

beforeEach(() => clearUserScopedStorage());
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  clearUserScopedStorage();
});

describe("chat folders", () => {
  it("keeps folder edits available in memory when browser storage is full", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("Quota exceeded");
    });
    const { result, unmount } = renderHook(() => useChatFolders());
    act(() => result.current.create("Planning", "chat-a"));
    const folderId = result.current.state.folders[0]?.id;
    expect(folderId).toBeTruthy();
    expect(result.current.state.assignments["chat-a"]).toBe(folderId);

    act(() => result.current.rename(folderId ?? "", "Reviews"));
    act(() => result.current.move("chat-b", folderId));
    expect(result.current.state.folders[0]?.name).toBe("Reviews");
    expect(result.current.state.assignments["chat-b"]).toBe(folderId);
    expect(window.localStorage.getItem("studio.chat.folders")).toBeNull();

    unmount();
    const reopened = renderHook(() => useChatFolders());
    expect(reopened.result.current.state.folders[0]?.name).toBe("Reviews");
    expect(reopened.result.current.state.assignments["chat-b"]).toBe(folderId);
  });

  it("validates persisted folders and drops orphan assignments", () => {
    expect(
      parseChatFolders(
        JSON.stringify({
          assignments: { orphan: "missing", session: "folder-1" },
          folders: [{ id: "folder-1", name: "  Planning  " }],
        }),
      ),
    ).toEqual({
      assignments: { session: "folder-1" },
      folders: [{ id: "folder-1", name: "Planning" }],
    });
  });

  it("deduplicates folder names case-insensitively without splitting emoji", () => {
    const longName = `${"a".repeat(59)}🧰extra`;
    expect(
      parseChatFolders(
        JSON.stringify({
          assignments: {},
          folders: [
            { id: "folder-1", name: "Work" },
            { id: "folder-2", name: " work " },
            { id: "folder-3", name: longName },
          ],
        }),
      ).folders,
    ).toEqual([
      { id: "folder-1", name: "Work" },
      { id: "folder-3", name: `${"a".repeat(59)}🧰` },
    ]);
  });

  it("unfiles chats when deleting their folder", () => {
    const state = {
      assignments: { first: "folder-1", second: "folder-2" },
      folders: [
        { id: "folder-1", name: "One" },
        { id: "folder-2", name: "Two" },
      ],
    };
    expect(deleteChatFolder(state, "folder-1")).toEqual({
      assignments: { second: "folder-2" },
      folders: [{ id: "folder-2", name: "Two" }],
    });
    expect(assignChatFolder(state, "first")).toMatchObject({ assignments: { second: "folder-2" } });
    expect(assignChatFolder(state, "first", "missing")).toBe(state);
  });

  it("keeps folder order and recency-buckets unfiled chats", () => {
    const session = (id: string, updatedAt: string): SessionSummaryResponse => ({
      capabilities: { delete: true, deleteReason: "", rename: true, renameReason: "" },
      createdAt: updatedAt,
      debugTargetSessionId: "",
      id,
      modelId: "",
      state: "idle",
      title: id,
      titleProvenance: "",
      titleRevision: "0",
      turns: 0,
      updatedAt,
    });
    const groups = groupSessions(
      [
        session("filed", "2026-09-01T12:00:00Z"),
        session("today", "2026-09-17T09:00:00Z"),
        session("week", "2026-09-14T09:00:00Z"),
      ],
      {
        assignments: { filed: "folder-1" },
        folders: [{ id: "folder-1", name: "Project" }],
      },
      new Date("2026-09-17T12:00:00Z"),
    );
    expect(groups.map((group) => [group.label, group.items.map((item) => item.id)])).toEqual([
      ["Project", ["filed"]],
      ["Today", ["today"]],
      ["This week", ["week"]],
    ]);
  });
});
