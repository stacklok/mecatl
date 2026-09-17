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
      canCopyId: false,
      copyIdReason: "",
    });
  });

  it("takes the copy-id offer and its denial reason from the row's capabilities", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        { sessionId: "s1", capabilities: { copyId: true, reasons: {} } },
        {
          sessionId: "s2",
          capabilities: {
            copyId: false,
            reasons: { copyId: "inspect_only_kind" },
          },
        },
      ]),
    );
    expect(page.sessions[0]).toMatchObject({
      canCopyId: true,
      copyIdReason: "",
    });
    expect(page.sessions[1]).toMatchObject({
      canCopyId: false,
      copyIdReason: "inspect_only_kind",
    });
  });

  it("takes the successor (fork/clear) offer and its denial reason from the row's capabilities", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        { sessionId: "s1", capabilities: { fork: true, reasons: {} } },
        {
          sessionId: "s2",
          capabilities: {
            fork: false,
            reasons: { fork: "active_elsewhere" },
          },
        },
        // Omitted reads as denied, like every other capability.
        { sessionId: "s3", capabilities: {} },
      ]),
    );
    expect(page.sessions[0]).toMatchObject({ canFork: true, forkReason: "" });
    expect(page.sessions[1]).toMatchObject({
      canFork: false,
      forkReason: "active_elsewhere",
    });
    expect(page.sessions[2]).toMatchObject({ canFork: false, forkReason: "" });
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

  it("decodes kind, activity state, relationship, placement, owner and the inspect/reason fields for the inventory tabs", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        {
          sessionId: "subagent-1",
          kind: "subagent",
          activityState: "active",
          relationship: {
            parentSessionId: "main-1",
            callId: "call-7",
            debugTargetSessionId: "",
          },
          placement: {
            kind: "git-worktree",
            label: "studio",
            branch: "feat/tabs",
            revision: "abc123",
          },
          owner: { issuer: "idp", subject: "u1", grantType: "user", name: "J" },
          capabilities: {
            inspect: true,
            viewTranscript: false,
            reasons: {
              publicChat: "inspect_only_kind",
              viewTranscript: "transcript_unavailable",
            },
          },
        },
        {
          sessionId: "branch-1",
          kind: "parallel_branch",
          relationship: { parentSessionId: "main-1", branchIndex: 2 },
          capabilities: { reasons: { publicChat: "inspect_only_kind" } },
        },
        {
          sessionId: "member-1",
          kind: "team_member",
          relationship: { teamId: "t1", memberName: "reviewer" },
          capabilities: { reasons: { publicChat: "inspect_only_kind" } },
        },
      ]),
    );
    expect(page.sessions[0]).toMatchObject({
      kind: "subagent",
      activityState: "active",
      isChat: false,
      canInspect: true,
      canViewTranscript: false,
      publicChatReason: "inspect_only_kind",
      viewTranscriptReason: "transcript_unavailable",
      ownerName: "J",
      placement: {
        kind: "git-worktree",
        label: "studio",
        branch: "feat/tabs",
        revision: "abc123",
      },
      relationship: {
        parentSessionId: "main-1",
        callId: "call-7",
        branchIndex: null,
        scheduleName: "",
        originSessionId: "",
        teamId: "",
        memberName: "",
      },
    });
    // The branch index is optional on the wire: present → number, absent → null.
    expect(page.sessions[1].relationship.branchIndex).toBe(2);
    expect(page.sessions[2].relationship).toMatchObject({
      teamId: "t1",
      memberName: "reviewer",
      branchIndex: null,
    });
    // A row with no placement / owner / relationship reads as empty, never
    // as a fabricated value.
    const bare = sessionInventoryFromResponse(inventory([{ sessionId: "s" }]))
      .sessions[0];
    expect(bare).toMatchObject({
      kind: "",
      activityState: "",
      placement: null,
      ownerName: "",
      canInspect: false,
      publicChatReason: "",
      viewTranscriptReason: "",
      titleRevision: null,
    });
    expect(bare.relationship.parentSessionId).toBe("");
  });

  it("prefers the canonical title_metadata over the deprecated title fields and carries the revision", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        {
          sessionId: "s1",
          title: "stale copy",
          titleProvenance: "first-prompt",
          titleMetadata: {
            title: "Canonical title",
            provenance: "operator",
            revision: BigInt(4),
          },
        },
        // An older daemon fills only the deprecated fields.
        { sessionId: "s2", title: "Legacy title", titleProvenance: "operator" },
        // Metadata present but empty falls back to the deprecated copy.
        {
          sessionId: "s3",
          title: "Fallback",
          titleMetadata: { title: "", provenance: "", revision: BigInt(0) },
        },
      ]),
    );
    expect(page.sessions[0]).toMatchObject({
      title: "Canonical title",
      titleProvenance: "operator",
      titleRevision: 4,
    });
    expect(page.sessions[1]).toMatchObject({
      title: "Legacy title",
      titleProvenance: "operator",
      titleRevision: null,
    });
    expect(page.sessions[2]).toMatchObject({
      title: "Fallback",
      titleRevision: 0,
    });
  });

  it("keeps the activity state verbatim so the Drafts tab can key on it", () => {
    const page = sessionInventoryFromResponse(
      inventory([
        { sessionId: "d1", kind: "main", activityState: "draft" },
        { sessionId: "a1", kind: "main", activityState: "active" },
      ]),
    );
    expect(page.sessions.map((s) => s.activityState)).toEqual([
      "draft",
      "active",
    ]);
    expect(page.sessions.every((s) => s.isChat)).toBe(true);
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

  it("decodes a tool result's image and link blocks into parts, the same way the live stream does", () => {
    const mapped = sessionTranscriptFromSdk(
      transcript({
        complete: true,
        messages: [
          {
            role: "tool",
            text: "",
            toolCalls: [],
            parts: [],
            toolResult: {
              callId: "c1",
              content: "ok",
              isError: false,
              structuredContent: "",
              blocks: [
                { kind: 1, text: "ok" },
                {
                  kind: 2,
                  mimeType: "image/png",
                  data: new Uint8Array([137, 80, 78, 71]),
                },
                {
                  kind: 4,
                  url: "https://example.com/r",
                  name: "r",
                  title: "",
                },
              ],
            },
          } as unknown as SdkSessionTranscript["messages"][number],
        ],
      }),
    );
    expect(mapped.messages[0].toolResult).toEqual({
      callId: "c1",
      content: "ok",
      isError: false,
      parts: [
        { kind: "image", mimeType: "image/png", data: "iVBORw==" },
        { kind: "resource_link", url: "https://example.com/r", name: "r" },
      ],
    });
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
