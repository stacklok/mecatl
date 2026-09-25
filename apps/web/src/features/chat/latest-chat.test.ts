// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { describe, expect, it } from "vitest";
import { formatRelativeTime, pickLatestEligibleChat } from "./latest-chat";

function row(id: string, over: Partial<SessionSummaryResponse> = {}): SessionSummaryResponse {
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
    },
    createdAt: "2026-01-01T00:00:00.000Z",
    debugTargetSessionId: "",
    id,
    kind: "main",
    modelId: "test-model",
    state: "completed",
    title: `Chat ${id}`,
    titleProvenance: "",
    titleRevision: "0",
    turns: 1,
    updatedAt: "2026-01-01T00:00:00.000Z",
    ...over,
  };
}

const none: ReadonlySet<string> = new Set();

describe("pickLatestEligibleChat", () => {
  it("returns the newest row by updatedAt regardless of input order", () => {
    const picked = pickLatestEligibleChat(
      [
        row("old", { updatedAt: "2026-01-01T00:00:00.000Z" }),
        row("newest", { updatedAt: "2026-01-03T00:00:00.000Z" }),
        row("mid", { updatedAt: "2026-01-02T00:00:00.000Z" }),
      ],
      none,
    );
    expect(picked?.id).toBe("newest");
  });

  it("skips running and awaiting chats (a chat busy elsewhere is not the default landing)", () => {
    const picked = pickLatestEligibleChat(
      [
        row("quiet", { updatedAt: "2026-01-01T00:00:00.000Z" }),
        row("busy", { state: "running", updatedAt: "2026-01-03T00:00:00.000Z" }),
        row("parked", { state: "awaiting", updatedAt: "2026-01-02T00:00:00.000Z" }),
      ],
      none,
    );
    expect(picked?.id).toBe("quiet");
  });

  it("skips thread-backing sessions named in the exclude set", () => {
    const picked = pickLatestEligibleChat(
      [
        row("chat", { updatedAt: "2026-01-01T00:00:00.000Z" }),
        row("thread", { updatedAt: "2026-01-05T00:00:00.000Z" }),
      ],
      new Set(["thread"]),
    );
    expect(picked?.id).toBe("chat");
  });

  it("does not offer inspect-only sessions as chats to continue", () => {
    const picked = pickLatestEligibleChat(
      [
        row("chat", { updatedAt: "2026-01-01T00:00:00.000Z" }),
        row("inspect", {
          capabilities: {
            ...row("inspect").capabilities,
            publicChat: false,
            publicChatReason: "inspect_only_kind",
          },
          kind: "child",
          updatedAt: "2026-01-05T00:00:00.000Z",
        }),
      ],
      none,
    );
    expect(picked?.id).toBe("chat");
  });

  it("returns undefined when nothing is eligible (start fresh)", () => {
    expect(pickLatestEligibleChat([], none)).toBeUndefined();
    expect(pickLatestEligibleChat([row("busy", { state: "running" })], none)).toBeUndefined();
  });

  it("keeps completed, failed and cancelled chats eligible (they reopen on send)", () => {
    for (const state of ["completed", "failed", "cancelled", "idle"]) {
      expect(pickLatestEligibleChat([row("x", { state })], none)?.id).toBe("x");
    }
  });
});

describe("formatRelativeTime", () => {
  it("formats minutes, hours, and days", () => {
    const now = Date.now();
    expect(formatRelativeTime(new Date(now - 30_000).toISOString())).toBe("<1m");
    expect(formatRelativeTime(new Date(now - 5 * 60_000).toISOString())).toBe("5m");
    expect(formatRelativeTime(new Date(now - 3 * 3_600_000).toISOString())).toBe("3h");
    expect(formatRelativeTime(new Date(now - 2 * 86_400_000).toISOString())).toBe("2d");
  });

  it("returns an empty string for an unparseable timestamp", () => {
    expect(formatRelativeTime("not-a-date")).toBe("");
  });
});
