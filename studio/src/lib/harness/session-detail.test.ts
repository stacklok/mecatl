import { SessionMode, type SessionSnapshot } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import { sessionIdentityFromSnapshot } from "./sessions";

/**
 * Pins the pure projection behind the `/session` details dialog: every row
 * the dialog renders is read off ONE GET-session snapshot (id, title and its
 * provenance, lifecycle state, kind, mode, resolved model, placement DISPLAY
 * metadata, creation time, turn/tool-call counts, limits, relationship), and
 * an older daemon that omits the newer blocks projects to nulls rather than
 * throwing or inventing values.
 */

/** Only the fields the projection reads are populated, hence the cast. */
const snapshot = (fields: Record<string, unknown>): SessionSnapshot =>
  ({
    sessionId: "s1",
    state: "idle",
    mode: SessionMode.Default,
    kind: "main",
    tokenUsage: {},
    turns: 0,
    toolCalls: 0,
    createdAtUnix: BigInt(0),
    debugMcpServers: [],
    debugMcpTools: [],
    ...fields,
  }) as unknown as SessionSnapshot;

describe("sessionIdentityFromSnapshot", () => {
  it("projects every dialog row from one snapshot", () => {
    const identity = sessionIdentityFromSnapshot(
      snapshot({
        sessionId: "session-abc",
        state: "awaiting",
        kind: "debug",
        mode: SessionMode.AcceptEdits,
        title: { value: "Fix the flaky test", provenance: "operator" },
        createdAtUnix: BigInt(1755000000),
        turns: 7,
        toolCalls: 12,
        limits: { maxTurns: 50, maxToolCalls: 200, maxConsecutiveFailures: 3 },
        resolvedModel: {
          providerId: "openrouter",
          modelId: "openai/gpt-5",
          contextWindow: BigInt(400000),
          reasoningEffort: "high",
        },
        placement: {
          kind: "worktree",
          label: "feature-x",
          branch: "feature/x",
          revision: "abc123",
        },
        relationship: {
          debugTargetSessionId: "session-target",
          parentSessionId: "",
          branchIndex: 0,
        },
      }),
    );
    expect(identity).toEqual({
      id: "session-abc",
      title: "Fix the flaky test",
      titleProvenance: "operator",
      state: "awaiting",
      kind: "debug",
      mode: "acceptEdits",
      resolvedModel: {
        providerId: "openrouter",
        modelId: "openai/gpt-5",
        contextWindow: 400000,
        reasoningEffort: "high",
      },
      placement: {
        kind: "worktree",
        label: "feature-x",
        branch: "feature/x",
        revision: "abc123",
      },
      createdAtUnix: 1755000000,
      turns: 7,
      toolCalls: 12,
      limits: { maxTurns: 50, maxToolCalls: 200, maxConsecutiveFailures: 3 },
      // Only the fields the daemon set: the empty parent id and the zero
      // branch index are omitted rather than rendered as blank rows.
      relationship: { debugTargetSessionId: "session-target" },
    });
  });

  it("keeps every relationship kind the daemon can set", () => {
    const identity = sessionIdentityFromSnapshot(
      snapshot({
        relationship: {
          parentSessionId: "parent-1",
          callId: "call-9",
          branchIndex: 2,
          scheduleName: "nightly",
          originSessionId: "origin-1",
          teamId: "team-1",
          memberName: "reviewer",
        },
      }),
    );
    expect(identity.relationship).toEqual({
      parentSessionId: "parent-1",
      callId: "call-9",
      branchIndex: 2,
      scheduleName: "nightly",
      originSessionId: "origin-1",
      teamId: "team-1",
      memberName: "reviewer",
    });
  });

  it("tolerates an older daemon that omits the newer blocks", () => {
    const identity = sessionIdentityFromSnapshot(
      snapshot({ sessionId: "", kind: undefined, state: undefined }),
      "fallback-id",
    );
    expect(identity).toEqual({
      id: "fallback-id",
      title: "",
      titleProvenance: "",
      state: "",
      kind: "",
      mode: "default",
      resolvedModel: null,
      placement: null,
      createdAtUnix: 0,
      turns: 0,
      toolCalls: 0,
      limits: null,
      relationship: null,
    });
  });

  it("drops a placement with neither label nor kind, and an empty relationship", () => {
    const identity = sessionIdentityFromSnapshot(
      snapshot({
        placement: { kind: "", label: "", branch: "main", revision: "" },
        relationship: {},
      }),
    );
    expect(identity.placement).toBeNull();
    expect(identity.relationship).toBeNull();
  });
});
