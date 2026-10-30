import { create, fromBinary, fromJson, toBinary, toJson } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import {
  ConversationMessageSchema,
  EventSchema,
  GetLearningProposalRequestSchema,
  GetLearningProposalResponseSchema,
  GetSessionTranscriptResponseSchema,
  HarnessService,
  LearningEvidenceRefSchema,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, type Event, InvalidStateError } from "../src/index.js";
import { sseResponse } from "./scripted-state.js";

const sessionId = "identity-session";
const runId = "identity-run";
// The server accepted two turns reusing one provider key; only engine IDs cross the wire.
const callIds = ["opaque-engine-a", "opaque-engine-b"] as const;
const messages = [
  ...callIds.flatMap((id) => [
    { role: "assistant", toolCalls: [{ args: "{}", id, name: "Read" }] },
    { role: "tool", toolResult: { callId: id, content: id } },
  ]),
  { providerPhase: "commentary", role: "assistant", text: "Readable output" },
];
const transcript = create(GetSessionTranscriptResponseSchema, { messages, sessionId });
const proposal = create(GetLearningProposalResponseSchema, {
  proposal: {
    evidence: [
      {
        attachmentUnavailable: true,
        available: true,
        digest: "verified-current-digest",
        locator: "conversation",
        ordinal: 4,
        preview: "Readable citation",
        protocol: "reflection-evidence/v2",
        sessionId,
      },
      { available: true, protocol: "reflection-evidence/v2", toolCallId: callIds[1] },
      { attachmentUnavailable: true, available: false, protocol: "reflection-evidence/v2" },
    ],
    id: "proposal",
  },
});
const events = [
  ...callIds.flatMap((id) => [
    { toolCall: { args: "{}", id, name: "Read" }, type: "tool.call" },
    { toolResult: { callId: id, content: id }, type: "tool.result" },
  ]),
  { compactionArchive: { replaced: messages }, type: "compaction.archive" },
  { text: "Readable reasoning summary", type: "reasoning.delta" },
  { result: { stop: "end_turn", text: "done" }, type: "result" },
].map((event, index) =>
  create(WatchSessionEventsResponseSchema, {
    cursor: `durable-${index + 1}`,
    event: { ...event, runId, seq: BigInt(index + 1) },
    phase: "replay",
  }),
);

function harness(kind: "grpc" | "http") {
  const cursors: string[] = [];
  let watches = 0;
  const frames = (cursor: string) => {
    cursors.push(cursor);
    watches += 1;
    const start = cursor === "" ? 0 : events.findIndex((event) => event.cursor === cursor) + 1;
    // A reconnect resumes after the last acknowledged durable position, not a call ID.
    const batch = events.slice(start, watches === 1 ? 2 : undefined);
    return batch.length === 0
      ? [create(WatchSessionEventsResponseSchema, { cursor, phase: "live" })]
      : batch;
  };
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => ({
        apiMajor: 2,
        capabilities: {},
        features: ["watch_session_events", "skill_files"],
      }),
      getSession: () => ({ session: { sessionId } }),
      getSessionTranscript: () =>
        fromBinary(
          GetSessionTranscriptResponseSchema,
          toBinary(GetSessionTranscriptResponseSchema, transcript),
        ),
      getLearningProposal: () =>
        fromBinary(
          GetLearningProposalResponseSchema,
          toBinary(GetLearningProposalResponseSchema, proposal),
        ),
      watchSessionEvents: async function* (request) {
        const batch = frames(request.cursor);
        for (const frame of batch) {
          yield fromBinary(
            WatchSessionEventsResponseSchema,
            toBinary(WatchSessionEventsResponseSchema, frame),
          );
        }
        if (watches === 1) throw new ConnectError("reconnect", Code.Unavailable);
      },
    });
  });
  const fetch: typeof globalThis.fetch = async (input) => {
    const url = new URL(String(input));
    if (url.pathname === "/v1/compatibility") {
      return Response.json({
        api_major: 2,
        capabilities: {},
        features: ["watch_session_events", "skill_files"],
      });
    }
    if (url.pathname === `/v1/sessions/${sessionId}`)
      return Response.json({ session_id: sessionId });
    if (url.pathname === `/v1/sessions/${sessionId}/transcript`) {
      return Response.json(toJson(GetSessionTranscriptResponseSchema, transcript));
    }
    if (url.pathname === "/v1/learning/proposals/proposal") {
      return Response.json(toJson(GetLearningProposalResponseSchema, proposal));
    }
    if (url.pathname === `/v1/sessions/${sessionId}/watch`) {
      // EOF is retryable for an attached run without a terminal result.
      return sseResponse(
        frames(url.searchParams.get("cursor") ?? "").map((frame) =>
          toJson(WatchSessionEventsResponseSchema, frame),
        ),
      );
    }
    throw new Error(`Unexpected fixture route: ${url.pathname}`);
  };
  return {
    client:
      kind === "grpc" ? connect({ transport }) : connect({ baseUrl: "http://mecatl.test", fetch }),
    cursors,
  };
}

describe("major-2 canonical identity projections", () => {
  it.each(["grpc", "http"] as const)(
    "preserves distinct calls, history, evidence and reconnect cursors over %s",
    async (kind) => {
      const { client, cursors } = harness(kind);
      try {
        const session = await client.sessions.get(sessionId);
        const view = await session.transcript();
        expect(
          view.messages
            .filter((message) => message.toolCalls.length > 0)
            .map((message) => message.toolCalls[0]?.id),
        ).toEqual(callIds);
        expect(
          view.messages
            .filter((message) => message.toolResult !== undefined)
            .map((message) => message.toolResult?.callId),
        ).toEqual(callIds);
        const standalone = view.messages[4];
        expect(standalone?.text).toBe("Readable output");
        expect(standalone?.providerPhase).toBe("commentary");
        expect(standalone?.toolCalls).toEqual([]);
        expect(standalone?.toolResult).toBeUndefined();

        const evidence = (
          await client.learningProposals.get(
            create(GetLearningProposalRequestSchema, { id: "proposal" }),
          )
        ).proposal?.evidence;
        expect(evidence?.[0]).toMatchObject({
          attachmentUnavailable: true,
          available: true,
          protocol: "reflection-evidence/v2",
          toolCallId: "",
        });
        expect(evidence?.[1]).toMatchObject({
          attachmentUnavailable: false,
          available: true,
          protocol: "reflection-evidence/v2",
          toolCallId: callIds[1],
        });
        expect(evidence?.[2]).toMatchObject({
          attachmentUnavailable: true,
          available: false,
          protocol: "reflection-evidence/v2",
          toolCallId: "",
        });

        const attached = await session.attach(runId, { includeLogOnly: true });
        const delivered: Event[] = [];
        for await (const envelope of attached) {
          if (envelope.kind === "event") delivered.push(envelope.event);
        }
        expect(cursors).toEqual(["", "durable-2"]);
        expect(delivered.map((event) => event.seq)).toEqual(
          events.map((frame) => frame.event?.seq),
        );
        expect(
          delivered.filter((event) => event.kind === "tool.call").map((event) => event.payload.id),
        ).toEqual(callIds);
        expect(
          delivered
            .filter((event) => event.kind === "tool.result")
            .map((event) => event.payload.callId),
        ).toEqual(callIds);
        const archive = delivered[4];
        if (archive?.kind !== "compaction.archive") throw new Error("Expected archive");
        expect(archive.payload.replaced[4]?.text).toBe("Readable output");
        expect(archive.payload.replaced[4]?.providerPhase).toBe("commentary");
        for (const message of archive.payload.replaced) {
          expect(message).not.toHaveProperty("reasoning");
          expect(message).not.toHaveProperty("reasoningItemId");
          expect(message).not.toHaveProperty("replay");
        }
        expect(delivered[5]).toMatchObject({
          kind: "reasoning.delta",
          text: "Readable reasoning summary",
        });
        // Reopening from the saved cursor is idempotent and does not reset the log.
        const reopened = await session.attach(runId, { from: attached.cursor });
        const iterator = reopened[Symbol.asyncIterator]();
        expect((await iterator.next()).value).toMatchObject({ kind: "boundary" });
        expect(reopened.cursor).toBe(attached.cursor);
        await reopened.close();
        expect(cursors.at(-1)).toBe("durable-7");
      } finally {
        await client.close();
      }
    },
  );

  it("never turns canonical call IDs into ask authority", async () => {
    const responded: string[] = [];
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          createSession: () => ({ sessionId }),
          getCompatibilityInfo: () => ({
            apiMajor: 2,
            capabilities: {},
            features: ["server_info"],
          }),
          converse: async function* () {
            yield {
              event: {
                ask: { askId: "retained-ask", callId: callIds[0], tool: "Read" },
                runId,
                type: "permission.ask",
              },
            };
            yield { event: { result: { stop: "end_turn" }, runId, type: "result" } };
          },
        });
      }),
    });
    try {
      const session = await client.sessions.create({});
      const run = await session.run("inspect", {
        onPermissionAsk: (ask) => {
          responded.push(ask.askId);
          return undefined;
        },
      });
      const iterator = run[Symbol.asyncIterator]();
      expect((await iterator.next()).value?.kind).toBe("permission.ask");
      expect(responded).toEqual(["retained-ask"]);
      await expect(run.resolveAsk(callIds[0], "allow_once")).rejects.toBeInstanceOf(
        InvalidStateError,
      );
      expect((await iterator.next()).value?.kind).toBe("result");
      await expect(run.resolveAsk("retained-ask", "allow_once")).rejects.toBeInstanceOf(
        InvalidStateError,
      );
      await iterator.next();
    } finally {
      await client.close();
    }
  });

  it("uses the approved tags and withdraws private archive fields", () => {
    const tag = (
      schema: { fields: readonly { localName: string; number: number }[] },
      name: string,
    ) => schema.fields.find((field) => field.localName === name)?.number;
    expect(tag(EventSchema, "historical")).toBeUndefined();
    expect(tag(ConversationMessageSchema, "historical")).toBeUndefined();
    expect(tag(LearningEvidenceRefSchema, "protocol")).toBe(10);
    expect(tag(LearningEvidenceRefSchema, "attachmentUnavailable")).toBe(11);
    expect(tag(ConversationMessageSchema, "reasoning")).toBeUndefined();
    expect(tag(ConversationMessageSchema, "reasoningItemId")).toBeUndefined();
    const archived = fromJson(
      ConversationMessageSchema,
      {
        reasoning: "withdrawn-private-replay",
        reasoning_item_id: "provider-item",
      },
      { ignoreUnknownFields: true },
    );
    expect(archived).not.toHaveProperty("reasoning");
    expect(archived).not.toHaveProperty("reasoningItemId");
  });
});
