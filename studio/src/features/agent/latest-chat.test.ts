import { afterEach, describe, expect, it, vi } from "vitest";
import { MOCK_TOUR_SESSION_ID } from "@/features/agent/mock-tour";
import type { AgentSession } from "@/features/agent/types";
import { memoryStorage } from "@/test/memory-storage";
import {
  consumeExplicitDraft,
  EXPLICIT_DRAFT_KEY,
  EXPLICIT_DRAFT_TTL_MS,
  isLatestChatEligible,
  markExplicitDraft,
  pickLatestEligibleChat,
} from "./latest-chat";

function row(id: string, over: Partial<AgentSession> = {}): AgentSession {
  return {
    id,
    title: `Chat ${id}`,
    projectId: null,
    model: "",
    createdAt: 0,
    updatedAt: 0,
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
    state: "idle",
    ...over,
  };
}

const none: ReadonlySet<string> = new Set();

/**
 * The web analogue of `--resume-latest`'s pick: the newest owned main chat
 * that is not busy. Each exclusion below is one the TUI applies (or one
 * Studio's own local content demands), and an empty inventory is "start
 * fresh", never an error.
 */
describe("pickLatestEligibleChat", () => {
  it("returns the newest row by updatedAt regardless of input order", () => {
    const picked = pickLatestEligibleChat(
      [
        row("old", { updatedAt: 100 }),
        row("newest", { updatedAt: 300 }),
        row("mid", { updatedAt: 200 }),
      ],
      none,
    );
    expect(picked?.id).toBe("newest");
  });

  it("skips running and awaiting chats (the TUI's active/awaiting exclusion)", () => {
    const picked = pickLatestEligibleChat(
      [
        row("quiet", { updatedAt: 100 }),
        row("busy", { updatedAt: 300, state: "running" }),
        row("parked", { updatedAt: 200, state: "awaiting" }),
      ],
      none,
    );
    expect(picked?.id).toBe("quiet");
  });

  it("skips thread-backing sessions named in the exclude set", () => {
    const picked = pickLatestEligibleChat(
      [row("chat", { updatedAt: 100 }), row("thread", { updatedAt: 500 })],
      new Set(["thread"]),
    );
    expect(picked?.id).toBe("chat");
  });

  it("skips AI-debug sessions (main-kind chats only)", () => {
    const picked = pickLatestEligibleChat(
      [
        row("chat", { updatedAt: 100 }),
        row("debug", { updatedAt: 500, debugTargetSessionId: "chat" }),
      ],
      none,
    );
    expect(picked?.id).toBe("chat");
  });

  it("skips the Labs mock tour and inspect-only rows", () => {
    const picked = pickLatestEligibleChat(
      [
        row("chat", { updatedAt: 100 }),
        row(MOCK_TOUR_SESSION_ID, { updatedAt: 900 }),
        row("subagent", { updatedAt: 800, isChat: false }),
      ],
      none,
    );
    expect(picked?.id).toBe("chat");
  });

  it("returns null when nothing is eligible (start fresh)", () => {
    expect(pickLatestEligibleChat([], none)).toBeNull();
    expect(
      pickLatestEligibleChat([row("busy", { state: "running" })], none),
    ).toBeNull();
  });

  it("keeps completed, failed and cancelled chats eligible (they reopen on send)", () => {
    for (const state of ["completed", "failed", "cancelled", undefined]) {
      expect(isLatestChatEligible(row("x", { state }), none)).toBe(true);
    }
  });
});

/**
 * The explicit-draft mark: written by "New chat", read once by the next
 * mount (whatever the preference), honoured only while fresh. A stale mark
 * from a "New chat" that never remounted must not eat a later landing.
 */
describe("explicit draft mark", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("round-trips a fresh mark exactly once", () => {
    vi.stubGlobal("sessionStorage", memoryStorage());
    markExplicitDraft(1_000);
    expect(window.sessionStorage.getItem(EXPLICIT_DRAFT_KEY)).toBe("1000");
    expect(consumeExplicitDraft(1_500)).toBe(true);
    // Consumed: the key is gone and a second read finds nothing.
    expect(window.sessionStorage.getItem(EXPLICIT_DRAFT_KEY)).toBeNull();
    expect(consumeExplicitDraft(1_600)).toBe(false);
  });

  it("discards a stale mark past the TTL and clears it", () => {
    vi.stubGlobal("sessionStorage", memoryStorage());
    markExplicitDraft(1_000);
    expect(consumeExplicitDraft(1_000 + EXPLICIT_DRAFT_TTL_MS)).toBe(false);
    expect(window.sessionStorage.getItem(EXPLICIT_DRAFT_KEY)).toBeNull();
  });

  it("treats a malformed mark as no request", () => {
    const storage = memoryStorage();
    vi.stubGlobal("sessionStorage", storage);
    storage.setItem(EXPLICIT_DRAFT_KEY, "not-a-time");
    expect(consumeExplicitDraft(5_000)).toBe(false);
    expect(storage.getItem(EXPLICIT_DRAFT_KEY)).toBeNull();
  });

  it("is fail-safe when storage throws", () => {
    vi.stubGlobal("sessionStorage", {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
      removeItem: () => {
        throw new Error("blocked");
      },
    });
    expect(() => markExplicitDraft()).not.toThrow();
    expect(consumeExplicitDraft()).toBe(false);
  });
});
