import { create, type JsonValue } from "@bufbuild/protobuf";
import { WireType } from "@bufbuild/protobuf/wire";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import type {
  Event,
  ParallelEventPayload,
  RoutingDecisionEventPayload,
  SessionTitleEventPayload,
  SubagentEventPayload,
  TeamEventPayload,
  ToolCallEventPayload,
} from "../src/events.js";
import { EventSchema, HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { connect } from "../src/index.js";
import { sseResponse } from "./scripted-state.js";

function terminal(runId: string) {
  return {
    event: {
      result: { stop: "end_turn", text: "done" },
      runId,
      type: "result",
    },
  };
}

describe("event unions", () => {
  it("known kinds narrow by literal kind", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-known" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          yield {
            event: {
              runId: "run-known",
              toolCall: { id: "call-1", name: "Read" },
              type: "tool.call",
            },
          };
          yield {
            event: {
              runId: "run-known",
              team: { innerKind: "message.delta", member: "scout", text: "found it" },
              type: "team.member",
            },
          };
          yield {
            event: {
              runId: "run-known",
              title: {
                generationState: "generated",
                latestAttempt: { id: "attempt-1", outcome: "succeeded" },
                provenance: "generated",
                title: "Inspect event kinds",
              },
              type: "session.title",
            },
          };
          yield terminal("run-known");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const events: Event[] = [];
    for await (const event of await session.run("inspect")) events.push(event);

    const tool = events[0];
    if (tool?.kind !== "tool.call") throw new Error("expected tool.call");
    expectTypeOf(tool.payload).toEqualTypeOf<ToolCallEventPayload>();
    expect(tool.payload.name).toBe("Read");

    const team = events[1];
    if (team?.kind !== "team.member") throw new Error("expected team.member");
    expectTypeOf(team.payload).toEqualTypeOf<TeamEventPayload>();
    expect(team.payload.member).toBe("scout");

    const title = events[2];
    if (title?.kind !== "session.title") throw new Error("expected session.title");
    expectTypeOf(title.payload).toEqualTypeOf<SessionTitleEventPayload>();
    expect(title.payload).toMatchObject({
      generationState: "generated",
      latestAttempt: { id: "attempt-1", outcome: "succeeded" },
      provenance: "generated",
      title: "Inspect event kinds",
    });

    const result = events[3];
    if (result?.kind !== "result") throw new Error("expected result");
    expect(result.payload.stop).toBe("end_turn");
    await client.close();
  });

  it("preserves routing decision presence over gRPC and HTTP event decoding", async () => {
    const decision = {
      backend: "jev",
      breakerOpen: false,
      candidateCategory: "deep",
      candidateModel: "capable",
      classifierModel: "jev-1.13.0",
      confidence: 0,
      consecutiveMisses: 1,
      minimumConfidence: 0,
      missLimit: 3,
      outcome: "fallback",
    } satisfies RoutingDecisionEventPayload;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "routing-grpc" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          yield {
            event: { runId: "r", subagent: { routingDecision: decision }, type: "subagent.start" },
          };
          yield {
            event: {
              runId: "r",
              team: {
                roster: [
                  { lead: true, name: "lead", routingDecision: decision },
                  { name: "unscored", routingDecision: { backend: "jev", outcome: "skipped" } },
                  { name: "historic" },
                ],
              },
              type: "team.start",
            },
          };
          yield terminal("r");
        },
      });
    });
    const grpc = connect({ transport });
    const grpcSession = await grpc.sessions.create({});
    const grpcEvents: Event[] = [];
    for await (const event of await grpcSession.run("route")) grpcEvents.push(event);
    const grpcStart = grpcEvents[0];
    if (grpcStart?.kind !== "subagent.start") throw new Error("expected subagent.start");
    expectTypeOf(grpcStart.payload).toEqualTypeOf<SubagentEventPayload>();
    expect(grpcStart.payload.routingDecision).toMatchObject(decision);
    const grpcTeamStart = grpcEvents[1];
    if (grpcTeamStart?.kind !== "team.start") throw new Error("expected team.start");
    expectTypeOf(grpcTeamStart.payload).toEqualTypeOf<TeamEventPayload>();
    expect(grpcTeamStart.payload.roster[0]?.routingDecision).toMatchObject(decision);
    expect(grpcTeamStart.payload.roster[0]?.routingDecision?.confidence).toBe(0);
    expect(grpcTeamStart.payload.roster[0]?.routingDecision?.minimumConfidence).toBe(0);
    expect(grpcTeamStart.payload.roster[0]).not.toHaveProperty("memberSessionId");
    expect(grpcTeamStart.payload.roster[1]?.routingDecision).toMatchObject({
      backend: "jev",
      outcome: "skipped",
    });
    expect(grpcTeamStart.payload.roster[1]?.routingDecision?.confidence).toBeUndefined();
    expect(grpcTeamStart.payload.roster[1]?.routingDecision?.minimumConfidence).toBeUndefined();
    expect(grpcTeamStart.payload.roster[2]?.routingDecision).toBeUndefined();
    await grpc.close();

    const httpFetch: typeof globalThis.fetch = async (input, init) => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/compatibility")
        return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
      if (path === "/v1/sessions" && init?.method === "POST")
        return Response.json({ session_id: "routing-http" }, { status: 201 });
      if (path.endsWith("/prompt"))
        return sseResponse([
          {
            parallel: {
              kind: "branch_start",
              routing_decision: {
                backend: "jev",
                breaker_open: false,
                candidate_category: "deep",
                candidate_model: "capable",
                classifier_model: "jev-1.13.0",
                confidence: 0,
                consecutive_misses: 1,
                minimum_confidence: 0,
                miss_limit: 3,
                outcome: "fallback",
              },
            },
            run_id: "r",
            type: "parallel.branch",
          },
          {
            team: {
              roster: [
                {
                  lead: true,
                  name: "lead",
                  routing_decision: {
                    backend: "jev",
                    breaker_open: false,
                    candidate_category: "deep",
                    candidate_model: "capable",
                    classifier_model: "jev-1.13.0",
                    confidence: 0,
                    consecutive_misses: 1,
                    minimum_confidence: 0,
                    miss_limit: 3,
                    outcome: "fallback",
                  },
                },
                {
                  name: "unscored",
                  routing_decision: { backend: "jev", outcome: "skipped" },
                },
                { name: "historic" },
              ],
            },
            run_id: "r",
            type: "team.start",
          },
          { result: { stop: "end_turn", text: "done" }, run_id: "r", type: "result" },
        ]);
      return Response.json({}, { status: 404 });
    };
    const http = connect({ baseUrl: "http://mecatl.test", fetch: httpFetch });
    const httpSession = await http.sessions.create({});
    const httpEvents: Event[] = [];
    for await (const event of await httpSession.run("route")) httpEvents.push(event);
    const httpStart = httpEvents[0];
    if (httpStart?.kind !== "parallel.branch") throw new Error("expected parallel.branch");
    expectTypeOf(httpStart.payload).toEqualTypeOf<ParallelEventPayload>();
    expect(httpStart.payload.routingDecision).toMatchObject(decision);
    const httpTeamStart = httpEvents[1];
    if (httpTeamStart?.kind !== "team.start") throw new Error("expected team.start");
    expectTypeOf(httpTeamStart.payload).toEqualTypeOf<TeamEventPayload>();
    expect(httpTeamStart.payload.roster[0]?.routingDecision).toMatchObject(decision);
    expect(httpTeamStart.payload.roster[0]?.routingDecision?.confidence).toBe(0);
    expect(httpTeamStart.payload.roster[0]?.routingDecision?.minimumConfidence).toBe(0);
    expect(httpTeamStart.payload.roster[0]).not.toHaveProperty("memberSessionId");
    expect(httpTeamStart.payload.roster[1]?.routingDecision).toMatchObject({
      backend: "jev",
      outcome: "skipped",
    });
    expect(httpTeamStart.payload.roster[1]?.routingDecision?.confidence).toBeUndefined();
    expect(httpTeamStart.payload.roster[1]?.routingDecision?.minimumConfidence).toBeUndefined();
    expect(httpTeamStart.payload.roster[2]?.routingDecision).toBeUndefined();
    await http.close();
  });

  it("unknown kinds preserve transport-native raw data", async () => {
    const unknownBytes = Uint8Array.of(0xa2, 0x06, 0x03, 0x01, 0x02, 0x03);
    const futureEvent = create(EventSchema, {
      runId: "run-grpc",
      seq: 1n,
      text: "future grpc event",
      type: "future.grpc",
    });
    futureEvent.$unknown = [
      {
        data: Uint8Array.of(0x03, 0x01, 0x02, 0x03),
        no: 100,
        wireType: WireType.LengthDelimited,
      },
    ];
    const grpcTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-grpc" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          yield { event: futureEvent };
          yield {
            event: { runId: "run-grpc", text: "still running", type: "message.delta" },
          };
          yield terminal("run-grpc");
        },
      });
    });
    // createRouterTransport passes message initializers in memory and therefore
    // does not exercise protobuf decoding. Decorate its injected stream at that
    // boundary with the unknown fields a real protobuf transport would retain.
    const routerStream = grpcTransport.stream.bind(grpcTransport);
    grpcTransport.stream = async (method, signal, timeoutMs, header, input, contextValues) => {
      const response = await routerStream(method, signal, timeoutMs, header, input, contextValues);
      return {
        ...response,
        message: (async function* () {
          for await (const message of response.message) {
            const event = (message as unknown as { event?: typeof futureEvent }).event;
            if (event?.type === "future.grpc") event.$unknown = futureEvent.$unknown;
            yield message;
          }
        })(),
      };
    };
    const grpcClient = connect({ transport: grpcTransport });
    const grpcSession = await grpcClient.sessions.create({});
    const grpcEvents: Event[] = [];
    for await (const event of await grpcSession.run("grpc")) grpcEvents.push(event);
    const grpcUnknown = grpcEvents[0];
    if (grpcUnknown?.kind !== "unknown" || grpcUnknown.transport !== "grpc") {
      throw new Error("expected an unknown gRPC event");
    }
    expectTypeOf(grpcUnknown.rawData).toEqualTypeOf<Uint8Array>();
    expect(grpcUnknown).toMatchObject({
      runId: "run-grpc",
      text: "future grpc event",
      wireKind: "future.grpc",
    });
    expect(grpcUnknown.rawData).toEqual(unknownBytes);
    expect(grpcEvents.map((event) => event.kind)).toEqual(["unknown", "message.delta", "result"]);
    await grpcClient.close();

    const rawHttpEvent = {
      future_payload: { answer: 42 },
      run_id: "run-http",
      seq: "1",
      text: "future http event",
      type: "future.http",
    };
    const httpFetch: typeof globalThis.fetch = async (input, init) => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/compatibility")
        return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
      if (path === "/v1/sessions" && init?.method === "POST") {
        return Response.json({ session_id: "session-http" }, { status: 201 });
      }
      if (path === "/v1/sessions/session-http/prompt") {
        return sseResponse([
          rawHttpEvent,
          { run_id: "run-http", text: "still running", type: "message.delta" },
          {
            result: { stop: "end_turn", text: "done" },
            run_id: "run-http",
            type: "result",
          },
        ]);
      }
      return Response.json({}, { status: 404 });
    };
    const httpClient = connect({ baseUrl: "http://mecatl.test", fetch: httpFetch });
    const httpSession = await httpClient.sessions.create({});
    const httpEvents: Event[] = [];
    for await (const event of await httpSession.run("http")) httpEvents.push(event);
    const httpUnknown = httpEvents[0];
    if (httpUnknown?.kind !== "unknown" || httpUnknown.transport !== "http") {
      throw new Error("expected an unknown HTTP event");
    }
    expectTypeOf(httpUnknown.rawData).toEqualTypeOf<JsonValue>();
    expect(httpUnknown.rawData).toEqual(rawHttpEvent);
    expect(httpEvents.map((event) => event.kind)).toEqual(["unknown", "message.delta", "result"]);
    await httpClient.close();
  });
});
