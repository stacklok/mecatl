import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  CHAT_FOLDERS_KEY,
  FOLDER_NAME_MAX_CHARS,
  MAX_FOLDERS,
  parseChatFolders,
  partitionByFolder,
  useChatFolders,
} from "./chat-folders";

/**
 * Chat folders are Studio-owned, browser-local state: ONE validated, bounded
 * JSON document in localStorage, keyed by daemon session id and shared by
 * every mounted consumer. Deleting a folder unfiles its chats and never
 * touches a chat; nothing here talks to the daemon.
 */

const EMPTY = { folders: [], assignments: {} };

function stored(): unknown {
  const raw = localStorage.getItem(CHAT_FOLDERS_KEY);
  return raw === null ? null : JSON.parse(raw);
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

describe("parseChatFolders", () => {
  it("reads nothing, junk and non-objects as the empty state", () => {
    for (const raw of [null, "", "{not json", "[]", "42", '"x"', "{}"]) {
      expect(parseChatFolders(raw)).toEqual(EMPTY);
    }
  });

  it("drops malformed folders, trims and caps names, and keeps the first of a duplicate", () => {
    const state = parseChatFolders(
      JSON.stringify({
        folders: [
          { id: "a", name: "  Work  " },
          // A case-insensitive duplicate name and a duplicate id both lose
          // to the first entry.
          { id: "b", name: "work" },
          { id: "a", name: "Other" },
          { id: "", name: "No id" },
          { id: "c", name: "   " },
          { id: "d" },
          { id: 7, name: "Numeric id" },
          "junk",
          null,
          { id: "e", name: "x".repeat(FOLDER_NAME_MAX_CHARS + 10) },
        ],
      }),
    );
    expect(state.folders).toEqual([
      { id: "a", name: "Work" },
      { id: "e", name: "x".repeat(FOLDER_NAME_MAX_CHARS) },
    ]);
  });

  it("keeps at most MAX_FOLDERS folders", () => {
    const folders = Array.from({ length: MAX_FOLDERS + 5 }, (_, i) => ({
      id: `f${i}`,
      name: `Folder ${i}`,
    }));
    expect(parseChatFolders(JSON.stringify({ folders })).folders).toHaveLength(
      MAX_FOLDERS,
    );
  });

  it("keeps only assignments to a kept folder", () => {
    const state = parseChatFolders(
      JSON.stringify({
        folders: [{ id: "a", name: "Work" }],
        assignments: { s1: "a", s2: "ghost", s3: 7, "": "a" },
      }),
    );
    expect(state.assignments).toEqual({ s1: "a" });
  });
});

describe("useChatFolders", () => {
  it("creates, files, renames and persists through one document", () => {
    const { result } = renderHook(() => useChatFolders());
    expect(result.current.folders).toEqual([]);
    let id = "";
    act(() => {
      id = result.current.createFolder("  Work ") ?? "";
    });
    expect(id).not.toBe("");
    expect(result.current.folders).toEqual([{ id, name: "Work" }]);

    act(() => result.current.moveChat("s1", id));
    expect(result.current.assignments).toEqual({ s1: id });

    let renamed = false;
    act(() => {
      renamed = result.current.renameFolder(id, "Clients");
    });
    expect(renamed).toBe(true);
    expect(result.current.folders).toEqual([{ id, name: "Clients" }]);
    expect(stored()).toEqual({
      folders: [{ id, name: "Clients" }],
      assignments: { s1: id },
    });
  });

  it("reuses an existing folder for the same name in another case, and refuses a colliding rename", () => {
    const { result } = renderHook(() => useChatFolders());
    let work = "";
    let personal = "";
    act(() => {
      work = result.current.createFolder("Work") ?? "";
      personal = result.current.createFolder("Personal") ?? "";
    });
    let again: string | null = null;
    act(() => {
      again = result.current.createFolder("WORK");
    });
    expect(again).toBe(work);
    expect(result.current.folders).toHaveLength(2);

    let renamed = true;
    act(() => {
      renamed = result.current.renameFolder(personal, "work");
    });
    expect(renamed).toBe(false);
    expect(result.current.folders.map((f) => f.name)).toEqual([
      "Work",
      "Personal",
    ]);
    // A change of case on the folder's OWN name is an ordinary rename.
    act(() => {
      renamed = result.current.renameFolder(work, "WORK");
    });
    expect(renamed).toBe(true);
    expect(result.current.folders[0]?.name).toBe("WORK");
    // Unknown folder, empty name: refused, nothing written.
    act(() => {
      renamed = result.current.renameFolder("ghost", "Anything");
    });
    expect(renamed).toBe(false);
    act(() => {
      renamed = result.current.renameFolder(work, "   ");
    });
    expect(renamed).toBe(false);
  });

  it("refuses an empty name and the folder cap", () => {
    const { result } = renderHook(() => useChatFolders());
    let id: string | null = "unset";
    act(() => {
      id = result.current.createFolder("   ");
    });
    expect(id).toBeNull();
    expect(stored()).toBeNull();

    act(() => {
      for (let i = 0; i < MAX_FOLDERS; i++) {
        result.current.createFolder(`Folder ${i}`);
      }
    });
    expect(result.current.folders).toHaveLength(MAX_FOLDERS);
    act(() => {
      id = result.current.createFolder("One more");
    });
    expect(id).toBeNull();
    expect(result.current.folders).toHaveLength(MAX_FOLDERS);
  });

  it("deleting a folder unfiles its chats, and an emptied store removes the key", () => {
    const { result } = renderHook(() => useChatFolders());
    let work = "";
    let other = "";
    act(() => {
      work = result.current.createFolder("Work") ?? "";
      other = result.current.createFolder("Other") ?? "";
      result.current.moveChat("s1", work);
      result.current.moveChat("s2", work);
      result.current.moveChat("s3", other);
    });
    act(() => result.current.deleteFolder(work));
    expect(result.current.folders).toEqual([{ id: other, name: "Other" }]);
    expect(result.current.assignments).toEqual({ s3: other });

    act(() => result.current.deleteFolder(other));
    expect(result.current.folders).toEqual([]);
    expect(result.current.assignments).toEqual({});
    expect(localStorage.getItem(CHAT_FOLDERS_KEY)).toBeNull();
  });

  it("moves a chat to no folder, and ignores an unknown folder", () => {
    const { result } = renderHook(() => useChatFolders());
    let work = "";
    act(() => {
      work = result.current.createFolder("Work") ?? "";
    });
    act(() => result.current.moveChat("s1", "ghost"));
    expect(result.current.assignments).toEqual({});
    act(() => result.current.moveChat("s1", work));
    expect(result.current.assignments).toEqual({ s1: work });
    act(() => result.current.moveChat("s1", null));
    expect(result.current.assignments).toEqual({});
    expect(stored()).toEqual({
      folders: [{ id: work, name: "Work" }],
      assignments: {},
    });
  });

  it("keeps every mounted consumer in sync, and follows another tab's edit", () => {
    const a = renderHook(() => useChatFolders());
    const b = renderHook(() => useChatFolders());
    let id = "";
    act(() => {
      id = a.result.current.createFolder("Work") ?? "";
    });
    expect(b.result.current.folders).toEqual([{ id, name: "Work" }]);

    const elsewhere = {
      folders: [{ id: "t", name: "From another tab" }],
      assignments: { s9: "t" },
    };
    act(() => {
      localStorage.setItem(CHAT_FOLDERS_KEY, JSON.stringify(elsewhere));
      window.dispatchEvent(
        new StorageEvent("storage", { key: CHAT_FOLDERS_KEY }),
      );
    });
    expect(a.result.current.folders).toEqual(elsewhere.folders);
    expect(b.result.current.assignments).toEqual(elsewhere.assignments);
  });

  it("reads a junk document as empty and never trusts it", () => {
    localStorage.setItem(CHAT_FOLDERS_KEY, "{not json");
    const { result } = renderHook(() => useChatFolders());
    expect(result.current.folders).toEqual([]);
    expect(result.current.assignments).toEqual({});
  });
});

describe("partitionByFolder", () => {
  it("groups by folder in folder order, keeps an empty folder, and leaves the rest in order", () => {
    const folders = [
      { id: "a", name: "Work" },
      { id: "b", name: "Empty" },
      { id: "c", name: "Home" },
    ];
    const sessions = [{ id: "s1" }, { id: "s2" }, { id: "s3" }, { id: "s4" }];
    const { filed, unfiled } = partitionByFolder(sessions, folders, {
      s1: "c",
      s3: "a",
      // A dangling assignment files nowhere.
      s4: "ghost",
    });
    expect(filed).toEqual([
      { folder: folders[0], sessions: [{ id: "s3" }] },
      { folder: folders[1], sessions: [] },
      { folder: folders[2], sessions: [{ id: "s1" }] },
    ]);
    expect(unfiled).toEqual([{ id: "s2" }, { id: "s4" }]);
  });

  it("is the identity with no folders", () => {
    const sessions = [{ id: "s1" }, { id: "s2" }];
    expect(partitionByFolder(sessions, [], {})).toEqual({
      filed: [],
      unfiled: sessions,
    });
  });
});
