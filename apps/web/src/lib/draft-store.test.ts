// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { beforeEach, describe, expect, it } from "vitest";
import { clearUserScopedStorage, reconcileAccount } from "./account-storage";
import { clearDraft, draftStorageKey, readDraft, writeDraft } from "./draft-store";

beforeEach(() => {
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

describe("draftStorageKey", () => {
  it("keeps the key under the account-scoped prefix, one per chat", () => {
    expect(draftStorageKey("abc")).toBe("studio.chat.draft.abc");
    expect(draftStorageKey("new")).toBe("studio.chat.draft.new");
    expect(draftStorageKey("session/a")).toBe("studio.chat.draft.session%2Fa");
  });
});

describe("readDraft, writeDraft, and clearDraft", () => {
  it("round-trips a draft through session storage only", () => {
    writeDraft("chat-a", "hello");
    expect(readDraft("chat-a")).toBe("hello");
    expect(window.sessionStorage.getItem("studio.chat.draft.chat-a")).toBe("hello");
    expect(window.localStorage.getItem("studio.chat.draft.chat-a")).toBeNull();
  });

  it("reads an empty draft for a chat with nothing stored", () => {
    expect(readDraft("missing")).toBe("");
  });

  it("removes the entry instead of storing a blank draft", () => {
    writeDraft("chat-a", "hello");
    writeDraft("chat-a", "   ");
    expect(readDraft("chat-a")).toBe("");
    expect(window.sessionStorage.getItem("studio.chat.draft.chat-a")).toBeNull();
  });

  it("clears a stored draft", () => {
    writeDraft("chat-a", "hello");
    clearDraft("chat-a");
    expect(readDraft("chat-a")).toBe("");
  });

  it("keeps drafts for different chats apart", () => {
    writeDraft("chat-a", "one");
    writeDraft("new", "two");
    expect(readDraft("chat-a")).toBe("one");
    expect(readDraft("new")).toBe("two");
  });

  it("never hands one account's draft to another account", () => {
    reconcileAccount("alice");
    writeDraft("chat-a", "Alice's unsent secret");
    writeDraft("new", "Alice's new chat");
    expect(readDraft("chat-a")).toBe("Alice's unsent secret");

    expect(reconcileAccount("bob")).toBe(true);
    expect(readDraft("chat-a")).toBe("");
    expect(readDraft("new")).toBe("");
    expect(window.sessionStorage.getItem("studio.chat.draft.chat-a")).toBeNull();
    expect(window.sessionStorage.getItem("studio.chat.draft.new")).toBeNull();

    writeDraft("chat-a", "Bob's draft");
    expect(readDraft("chat-a")).toBe("Bob's draft");
  });

  it("drops every draft on sign-out", () => {
    reconcileAccount("alice");
    writeDraft("chat-a", "unsent");
    clearUserScopedStorage();
    expect(readDraft("chat-a")).toBe("");
    expect(window.sessionStorage.getItem("studio.chat.draft.chat-a")).toBeNull();
  });
});
