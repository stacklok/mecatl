import { create } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import {
  ConversationMessageSchema,
  DreamTargetCapabilitySchema,
  GetSessionTranscriptResponseSchema,
  HarnessService,
  ManualDreamCapabilitiesSchema,
  ServerCapabilitiesSchema,
  SessionCapabilitiesSchema,
  SessionSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, ProtocolError, SessionMode } from "../src/index.js";
import { projectSessionSnapshot, projectSessionTranscript } from "../src/session-projections.js";

function session() {
  return create(SessionSchema, {
    createdAtUnix: 123n,
    debugMcpServers: ["server"],
    debugMcpTools: ["server.tool"],
    kind: "future-kind",
    limits: { maxConsecutiveFailures: 3, maxToolCalls: 2, maxTurns: 1 },
    mode: SessionMode.Plan,
    placement: { branch: "topic", kind: "worktree", label: "Topic", revision: "abc" },
    relationship: {
      branchIndex: 0,
      callId: "call",
      debugTargetSessionId: "debug-target",
      memberName: "member",
      originSessionId: "origin",
      parentSessionId: "parent",
      scheduleName: "schedule",
      teamId: "team",
    },
    resolvedModel: {
      contextWindow: 128_000n,
      modelId: "model",
      providerId: "provider",
      reasoningEffort: "high",
    },
    sessionCapabilities: { audio: true, image: false },
    sessionId: "session",
    state: "future-state",
    titleMetadata: {
      generationState: "generated",
      latestAttempt: { id: "attempt", outcome: "succeeded" },
      provenance: "generated",
      revision: 4n,
      title: "Canonical title",
    },
    tokenUsage: {
      main: {
        models: {
          "provider/model": {
            cacheReadTokens: 3n,
            cacheWriteTokens: 4n,
            inputTokens: 1n,
            outputTokens: 2n,
            reasoningTokens: 1n,
          },
        },
        total: { inputTokens: 1n, outputTokens: 2n },
      },
    },
    toolCalls: 6,
    turns: 5,
  });
}

describe("session projections", () => {
  it("snapshot projects every canonical session field without aliases", () => {
    const proto = session();
    const snapshot = projectSessionSnapshot(proto, "session");
    expect(snapshot).toEqual({
      createdAtUnix: 123n,
      debugMcpServers: ["server"],
      debugMcpTools: ["server.tool"],
      kind: "future-kind",
      limits: { maxConsecutiveFailures: 3, maxToolCalls: 2, maxTurns: 1 },
      mode: SessionMode.Plan,
      placement: { branch: "topic", kind: "worktree", label: "Topic", revision: "abc" },
      relationship: {
        branchIndex: 0,
        callId: "call",
        debugTargetSessionId: "debug-target",
        memberName: "member",
        originSessionId: "origin",
        parentSessionId: "parent",
        scheduleName: "schedule",
        teamId: "team",
      },
      resolvedModel: {
        contextWindow: 128_000n,
        modelId: "model",
        providerId: "provider",
        reasoningEffort: "high",
      },
      sessionCapabilities: { audio: true, image: false, pdf: false },
      sessionId: "session",
      state: "future-state",
      title: {
        generationState: "generated",
        latestAttempt: { id: "attempt", outcome: "succeeded" },
        provenance: "generated",
        revision: 4n,
        value: "Canonical title",
      },
      tokenUsage: {
        main: {
          models: {
            "provider/model": {
              cacheReadTokens: 3n,
              cacheWriteTokens: 4n,
              inputTokens: 1n,
              outputTokens: 2n,
              reasoningTokens: 1n,
            },
          },
          total: {
            cacheReadTokens: 0n,
            cacheWriteTokens: 0n,
            inputTokens: 1n,
            outputTokens: 2n,
            reasoningTokens: 0n,
          },
        },
      },
      toolCalls: 6,
      turns: 5,
    });
    expect(snapshot).not.toHaveProperty("titleMetadata");
    expect(snapshot).not.toHaveProperty("titleProvenance");

    proto.debugMcpServers[0] = "changed";
    const modelUsage = proto.tokenUsage.main?.models["provider/model"];
    expect(modelUsage).toBeDefined();
    if (modelUsage !== undefined) modelUsage.inputTokens = 999n;
    expect(snapshot?.debugMcpServers).toEqual(["server"]);
    expect(snapshot?.tokenUsage.main?.models["provider/model"]?.inputTokens).toBe(1n);

    const untitled = create(SessionSchema, {
      mode: SessionMode.Default,
      sessionId: "untitled",
    });
    expect(projectSessionSnapshot(untitled, "untitled")?.title).toBeUndefined();
  });

  it("transcript omits provider-private replay state and detaches public data", () => {
    const proto = create(GetSessionTranscriptResponseSchema, {
      activity: { authoritative: false, available: true, complete: false },
      complete: true,
      kind: "main",
      messages: [
        {
          parts: [{ data: new Uint8Array([1, 2]), kind: 1, mimeType: "image/png" }],
          providerPhase: "private-phase",
          reasoning: "private-reasoning",
          reasoningItemId: "private-id",
          role: "assistant",
          text: "hello",
          toolCalls: [{ args: "{}", id: "call", name: "Read" }],
          toolResult: {
            blocks: [
              {
                audience: ["user"],
                data: new Uint8Array([3, 4]),
                kind: 2,
                mimeType: "image/png",
              },
            ],
            callId: "call",
            content: "result",
          },
        },
      ],
      relationship: { parentSessionId: "parent" },
      sessionId: "session",
    });
    const transcript = projectSessionTranscript(proto, "session");
    expect(transcript).toEqual({
      activity: { authoritative: false, available: true, complete: false },
      complete: true,
      kind: "main",
      messages: [
        {
          parts: [
            {
              artifactId: "",
              data: new Uint8Array([1, 2]),
              kind: 1,
              mimeType: "image/png",
              name: "",
              sha256: "",
              size: 0n,
              url: "",
            },
          ],
          role: "assistant",
          text: "hello",
          toolCalls: [{ args: "{}", id: "call", name: "Read" }],
          toolResult: {
            blocks: [expect.objectContaining({ audience: ["user"], data: new Uint8Array([3, 4]) })],
            callId: "call",
            content: "result",
            isError: false,
            structuredContent: "",
          },
        },
      ],
      relationship: { parentSessionId: "parent" },
      sessionId: "session",
    });
    expect(transcript?.messages[0]).not.toHaveProperty("reasoning");
    expect(transcript?.messages[0]).not.toHaveProperty("providerPhase");
    expect(transcript?.messages[0]).not.toHaveProperty("reasoningItemId");

    const protoMessage = proto.messages[0];
    const protoPart = protoMessage?.parts[0];
    const protoBlock = protoMessage?.toolResult?.blocks[0];
    expect(protoPart).toBeDefined();
    expect(protoBlock).toBeDefined();
    if (protoPart !== undefined) protoPart.data[0] = 9;
    if (protoBlock !== undefined) {
      protoBlock.data[0] = 9;
      protoBlock.audience[0] = "assistant";
    }
    expect(transcript?.messages[0]?.parts[0]?.data[0]).toBe(1);
    expect(transcript?.messages[0]?.toolResult?.blocks[0]?.data[0]).toBe(3);
    expect(transcript?.messages[0]?.toolResult?.blocks[0]?.audience).toEqual(["user"]);
  });

  it("snapshot and transcript reject malformed or mismatched responses", async () => {
    let getKind: "missing" | "mismatch" | "unknown-mode" = "missing";
    let transcriptId = "";
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          createSession: () => ({ sessionId: "session" }),
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: ["server_info"],
          }),
          getSession: () => {
            if (getKind === "missing") return {};
            return {
              session: {
                mode: getKind === "unknown-mode" ? (99 as SessionMode) : SessionMode.Default,
                sessionId: getKind === "mismatch" ? "other" : "session",
              },
            };
          },
          getSessionTranscript: () => ({ sessionId: transcriptId }),
          renameSession: () => ({ session: { sessionId: "other" } }),
          setMode: () => ({}),
        });
      }),
    });
    const handle = await client.sessions.create({});
    await expect(handle.snapshot()).rejects.toBeInstanceOf(ProtocolError);
    getKind = "mismatch";
    await expect(handle.snapshot()).rejects.toBeInstanceOf(ProtocolError);
    getKind = "unknown-mode";
    await expect(handle.snapshot()).rejects.toBeInstanceOf(ProtocolError);
    await expect(client.sessions.get("session")).rejects.toBeInstanceOf(ProtocolError);
    await expect(handle.rename("title")).rejects.toBeInstanceOf(ProtocolError);
    await expect(handle.setMode(SessionMode.Plan)).rejects.toBeInstanceOf(ProtocolError);
    await expect(handle.transcript()).rejects.toBeInstanceOf(ProtocolError);
    transcriptId = "other";
    await expect(handle.transcript()).rejects.toBeInstanceOf(ProtocolError);
    await client.close();
  });

  it("descriptor guards keep lifecycle projections complete", () => {
    const fields = (schema: { fields: readonly { localName: string }[] }) =>
      schema.fields.map((field) => field.localName).sort();

    expect(fields(SessionSchema)).toEqual(
      [
        "createdAtUnix",
        "debugMcpServers",
        "debugMcpTools",
        "kind",
        "limits",
        "mode",
        "placement",
        "relationship",
        "resolvedModel",
        "sessionCapabilities",
        "sessionId",
        "state",
        "titleMetadata",
        "tokenUsage",
        "toolCalls",
        "turns",
      ].sort(),
    );
    expect(fields(ServerCapabilitiesSchema)).toEqual(
      [
        "agents",
        "audio",
        "shell",
        "debugMcp",
        "image",
        "learnedSkills",
        "learningProposals",
        "manualCompaction",
        "manualDream",
        "mcp",
        "mcpConnectorStatus",
        "mcpRefresh",
        "memory",
        "modelSelection",
        "artifacts",
        "posture",
        "reflection",
        "scheduling",
        "sessionDebug",
        "skills",
        "slashCommands",
        "soul",
        "steer",
        "storageCleanup",
        "storageHealth",
        "teams",
        "userModel",
        "workspaceEnrollment",
        "worktrees",
      ].sort(),
    );
    expect(fields(SessionCapabilitiesSchema)).toEqual(["audio", "image", "pdf"]);
    expect(fields(ManualDreamCapabilitiesSchema)).toEqual(["projectMemory", "userModel"]);
    expect(fields(DreamTargetCapabilitySchema)).toEqual([
      "decide",
      "generate",
      "unavailableReason",
    ]);
    expect(fields(ConversationMessageSchema)).toEqual(
      [
        "parts",
        "providerPhase",
        "reasoning",
        "reasoningItemId",
        "role",
        "text",
        "toolCalls",
        "toolResult",
      ].sort(),
    );
  });
});
