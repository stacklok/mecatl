import { describe, expect, it } from "vitest";
import type { AgentSession } from "@/features/agent";
import { MOCK_TOUR_SESSION } from "@/features/agent/mock-tour";
import { COPY_ID_NOT_OFFERED } from "./session-copy-menu-items";
import { FORK_NOT_OFFERED } from "./session-row-action-items";
import {
  copyIdShortcutOutcome,
  FORK_NOT_HERE,
  forkShortcutOutcome,
  NO_CHAT_TO_COPY,
  NO_CHAT_TO_FORK,
} from "./use-session-row-shortcuts";

/**
 * The two per-chat keys (`chat.copyId`, `chat.fork`) resolve to the same
 * verdict the row menu shows: act on an eligible open chat, otherwise name
 * why not — no chat open, the mock tour, an AI-debug chat (fork), or the
 * daemon's own capability reason.
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
    canCopyId: true,
    canFork: true,
    ...overrides,
  };
}

describe("copyIdShortcutOutcome", () => {
  it("acts on an open chat the daemon lets copy", () => {
    expect(copyIdShortcutOutcome(session())).toEqual({ kind: "act" });
  });

  it("explains a draft, the mock tour and a denied row", () => {
    expect(copyIdShortcutOutcome(undefined)).toEqual({
      kind: "info",
      message: NO_CHAT_TO_COPY,
    });
    expect(copyIdShortcutOutcome(MOCK_TOUR_SESSION)).toEqual({
      kind: "info",
      message: NO_CHAT_TO_COPY,
    });
    expect(
      copyIdShortcutOutcome(
        session({ canCopyId: false, copyIdReason: "inspect_only_kind" }),
      ),
    ).toEqual({ kind: "info", message: "Read-only run" });
    expect(copyIdShortcutOutcome(session({ canCopyId: undefined }))).toEqual({
      kind: "info",
      message: COPY_ID_NOT_OFFERED,
    });
  });
});

describe("forkShortcutOutcome", () => {
  it("acts on an open chat the daemon would fork", () => {
    expect(forkShortcutOutcome(session())).toEqual({ kind: "act" });
  });

  it("explains a draft, the mock tour, a debug chat and a denied row", () => {
    expect(forkShortcutOutcome(undefined)).toEqual({
      kind: "info",
      message: NO_CHAT_TO_FORK,
    });
    expect(forkShortcutOutcome(MOCK_TOUR_SESSION)).toEqual({
      kind: "info",
      message: NO_CHAT_TO_FORK,
    });
    expect(
      forkShortcutOutcome(session({ debugTargetSessionId: "session-target" })),
    ).toEqual({ kind: "info", message: FORK_NOT_HERE });
    expect(
      forkShortcutOutcome(
        session({ canFork: false, forkReason: "active_elsewhere" }),
      ),
    ).toEqual({ kind: "info", message: "Running in another client" });
    expect(forkShortcutOutcome(session({ canFork: undefined }))).toEqual({
      kind: "info",
      message: FORK_NOT_OFFERED,
    });
  });
});
