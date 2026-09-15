import type {
  SessionTranscript as SdkSessionTranscript,
  SessionMode,
} from "@stacklok-oss/mecatl-sdk";
import type { ListSessionsResponse } from "@stacklok-oss/mecatl-sdk/gen";
import { describe, expect, it } from "vitest";
import {
  decodeSessionPermissionMode,
  encodeSessionPermissionMode,
  sessionInventoryFromResponse,
  sessionPermissionModeFromSdk,
  sessionPermissionModeToSdk,
  sessionTranscriptFromSdk,
} from "./sessions";

/**
 * Inventory rows arrive as the SDK's generated `ListSessionsResponse`; these
 * fixtures are the decoded (camelCase, bigint) shape. Only the fields the
 * mapper reads are populated — the proto `$typeName` brand is a compile-time
 * detail, hence the cast.
 */
const inventory = (
  sessions: Record<string, unknown>[],
  nextCursor = "",
): ListSessionsResponse =>
  ({ sessions, nextCursor, totalCount: 0 }) as unknown as ListSessionsResponse;

describe("sessionInventoryFromResponse", () => {
  it("takes action eligibility from the row's capabilities, never re-deriving it", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        {
          sessionId: "s1",
          title: "Fix the flaky test",
          state: "idle",
          capabilities: {
            rename: true,
            delete: true,
            viewTranscript: true,
            reasons: {},
          },
        },
      ]),
    );
    expect(page.sessions[0]).toMatchObject({
      sessionId: "s1",
      canRename: true,
      canDelete: true,
      canViewTranscript: true,
      isChat: true,
    });
  });

  it("treats an omitted capability as a denial", () => {
    const page = sessionInventoryFromResponse(
      inventory([{ sessionId: "s1", capabilities: {} }]),
    );
    expect(page.sessions[0]).toMatchObject({
      canRename: false,
      canDelete: false,
      canViewTranscript: false,
    });
  });

  it("filters chats on inspect_only_kind and ONLY that reason", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        {
          sessionId: "subagent-1",
          capabilities: { reasons: { publicChat: "inspect_only_kind" } },
        },
        {
          sessionId: "s2",
          capabilities: { reasons: { publicChat: "busy_running" } },
        },
      ]),
    );
    expect(page.sessions[0].isChat).toBe(false);
    expect(page.sessions[1].isChat).toBe(true);
  });

  it("drops a row with no session id instead of rendering an inert chat", () => {
    const page = sessionInventoryFromResponse(
      inventory([{ sessionId: "", title: "corrupt" }, { sessionId: "s1" }]),
    );
    expect(page.sessions).toHaveLength(1);
    expect(page.sessions[0].sessionId).toBe("s1");
  });

  it("carries the cursor and converts bigint unix seconds to millis", () => {
    const page = sessionInventoryFromResponse(
      inventory(
        [
          {
            sessionId: "s1",
            modifiedAtUnix: BigInt(1700000000),
            createdAtUnix: BigInt(1699999999),
          },
          { sessionId: "s2", modifiedAtUnix: BigInt(0) },
        ],
        "abc",
      ),
    );
    expect(page.nextCursor).toBe("abc");
    expect(page.sessions[0].modifiedAt).toBe(1700000000000);
    expect(page.sessions[0].createdAt).toBe(1699999999000);
    expect(page.sessions[1].modifiedAt).toBe(0);
  });

  it("carries per-action denial reasons for the UI to explain with", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        {
          sessionId: "s1",
          capabilities: {
            reasons: { rename: "busy_running", delete: "no_pruning" },
          },
        },
      ]),
    );
    expect(page.sessions[0].renameReason).toBe("busy_running");
    expect(page.sessions[0].deleteReason).toBe("no_pruning");
  });

  it("carries titleProvenance verbatim, defaulting an absent field to unknown (F4)", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        { sessionId: "s1", title: "Hand-set", titleProvenance: "operator" },
        { sessionId: "s2", title: "Seeded", titleProvenance: "first-prompt" },
        { sessionId: "s3", title: "Legacy row", titleProvenance: "" },
      ]),
    );
    expect(page.sessions.map((s) => s.titleProvenance)).toEqual([
      "operator",
      "first-prompt",
      "",
    ]);
  });

  it("decodes the debug relationship and treats a debug row as a chat despite inspect_only_kind (ADR 0254)", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        {
          // The live daemon stamps debug rows inspect_only_kind (the KIND is
          // not main) yet drives them as ordinary chats; the relationship is
          // the honest chat signal. Rename/delete stay denied per the row.
          sessionId: "dbg-1",
          kind: "debug",
          relationship: { debugTargetSessionId: "target-9" },
          capabilities: {
            viewTranscript: true,
            reasons: {
              publicChat: "inspect_only_kind",
              rename: "inspect_only_kind",
              delete: "inspect_only_kind",
            },
          },
        },
        {
          // A relationship WITHOUT the debug binding (a scheduled fire) stays
          // inspect-only — the exception is the debug field, not any
          // relationship.
          sessionId: "sched-1",
          kind: "scheduled",
          relationship: { scheduleName: "nightly", debugTargetSessionId: "" },
          capabilities: { reasons: { publicChat: "inspect_only_kind" } },
        },
      ]),
    );
    const debug = page.sessions[0];
    expect(debug.debugTargetSessionId).toBe("target-9");
    expect(debug.isChat).toBe(true);
    expect(debug.canRename).toBe(false);
    expect(debug.canDelete).toBe(false);
    const scheduled = page.sessions[1];
    expect(scheduled.debugTargetSessionId).toBe("");
    expect(scheduled.isChat).toBe(false);
  });
});

describe("sessionTranscriptFromSdk", () => {
  const transcript = (
    partial: Partial<SdkSessionTranscript>,
  ): SdkSessionTranscript =>
    ({
      sessionId: "s1",
      kind: "main",
      complete: false,
      messages: [],
      ...partial,
    }) as SdkSessionTranscript;

  it("maps messages with tool calls and results, and the completeness attestation", () => {
    const mapped = sessionTranscriptFromSdk(
      transcript({
        complete: true,
        messages: [
          { role: "user", text: "hello", toolCalls: [], parts: [] },
          {
            role: "assistant",
            text: "",
            toolCalls: [{ id: "c1", name: "bash", args: "{}" }],
            parts: [],
          },
          {
            role: "tool",
            text: "",
            toolCalls: [],
            parts: [],
            toolResult: {
              blocks: [],
              callId: "c1",
              content: "ok",
              isError: false,
              structuredContent: "",
            },
          },
          { role: "assistant", text: "done", toolCalls: [], parts: [] },
        ],
      }),
    );
    expect(mapped.sessionId).toBe("s1");
    expect(mapped.complete).toBe(true);
    expect(mapped.messages).toHaveLength(4);
    expect(mapped.messages[1].toolCalls).toEqual([
      { id: "c1", name: "bash", args: "{}" },
    ]);
    expect(mapped.messages[2].toolResult).toEqual({
      callId: "c1",
      content: "ok",
      isError: false,
    });
    expect(mapped.messages[0].toolResult).toBeUndefined();
  });

  it("reports an unproven transcript as incomplete rather than whole", () => {
    expect(sessionTranscriptFromSdk(transcript({})).complete).toBe(false);
  });
});

describe("session permission mode mapping", () => {
  it("decodes the daemon's echo spellings, mirroring the Go modeFromString", () => {
    expect(decodeSessionPermissionMode("acceptEdits")).toBe("acceptEdits");
    expect(decodeSessionPermissionMode("plan")).toBe("plan");
    expect(decodeSessionPermissionMode("default")).toBe("default");
    expect(decodeSessionPermissionMode("accept_edits")).toBe("acceptEdits");
    expect(decodeSessionPermissionMode("accept-edits")).toBe("acceptEdits");
    expect(decodeSessionPermissionMode("accept")).toBe("acceptEdits");
    expect(decodeSessionPermissionMode("PLAN")).toBe("plan");
  });

  it("falls through to default for unknown, empty, or non-string values", () => {
    expect(decodeSessionPermissionMode("")).toBe("default");
    expect(decodeSessionPermissionMode("yolo")).toBe("default");
    expect(decodeSessionPermissionMode(undefined)).toBe("default");
    expect(decodeSessionPermissionMode(3)).toBe("default");
  });

  it("encodes requests as protojson snake_case, never the camelCase echo", () => {
    expect(encodeSessionPermissionMode("default")).toBe("default");
    expect(encodeSessionPermissionMode("plan")).toBe("plan");
    expect(encodeSessionPermissionMode("acceptEdits")).toBe("accept_edits");
  });

  it("maps both ways onto the SDK's numeric SessionMode", () => {
    expect(sessionPermissionModeToSdk("default")).toBe(1);
    expect(sessionPermissionModeToSdk("plan")).toBe(2);
    expect(sessionPermissionModeToSdk("acceptEdits")).toBe(3);
    expect(sessionPermissionModeToSdk("accept_edits")).toBe(3);
    expect(sessionPermissionModeFromSdk(1)).toBe("default");
    expect(sessionPermissionModeFromSdk(2)).toBe("plan");
    expect(sessionPermissionModeFromSdk(3)).toBe("acceptEdits");
    // Unspecified reads as the daemon's default mode.
    expect(sessionPermissionModeFromSdk(0 as SessionMode)).toBe("default");
    for (const mode of ["default", "plan", "acceptEdits"] as const) {
      expect(
        sessionPermissionModeFromSdk(sessionPermissionModeToSdk(mode)),
      ).toBe(mode);
    }
  });
});
