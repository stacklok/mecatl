import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import type {
  CancelTeammateResponse,
  CleanupTeamResponse,
  ListTeamResponse,
  SpawnTeammateResponse,
  TeamMember,
  TeamOutcome,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  InvalidStateError,
  ProtocolError,
  ServerError,
  type TeamEvent,
  type TeamRunEvent,
} from "../src/index.js";

function terminalOutcome(overrides: Partial<TeamOutcome> = {}) {
  return {
    budgetExhausted: false,
    findings: [],
    quiescent: true,
    rounds: 1,
    stop: "end_turn",
    dispositions: [],
    ...overrides,
  };
}

async function collect(run: AsyncIterable<TeamRunEvent>): Promise<TeamRunEvent[]> {
  const events: TeamRunEvent[] = [];
  for await (const event of run) events.push(event);
  return events;
}

describe("ergonomic teams", () => {
  it("team creation returns an ergonomic Team", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createTeam: (request) => ({
          members: [
            {
              agentType: request.members[0]?.agentType ?? "",
              name: request.members[0]?.name ?? "",
              sessionId: "team-team-7-lead",
              state: "spawning",
            },
          ],
          teamId: "team-7",
        }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
      });
    });
    const client = connect({ transport });

    const team = await client.teams.create({
      goal: "ship scenario five",
      members: [{ agentType: "reviewer", lead: true, name: "lead" }],
      sessionId: "session-1",
    });

    expect(team.id).toBe("team-7");
    expect(team.initialMembers).toMatchObject([
      { agentType: "reviewer", name: "lead", sessionId: "team-team-7-lead" },
    ]);
    expectTypeOf(team.initialMembers).toEqualTypeOf<readonly TeamMember[]>();
    expect(team).toMatchObject({
      cancel: expect.any(Function),
      cleanup: expect.any(Function),
      list: expect.any(Function),
      message: expect.any(Function),
      run: expect.any(Function),
      spawn: expect.any(Function),
    });
    await client.close();
  });

  it("team members spawn message and cancel through the team handle", async () => {
    const requests: Record<string, unknown>[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        cancelTeammate: (request) => {
          requests.push({ ...request });
          if (request.member === "missing") {
            throw new ConnectError("member not found", Code.NotFound);
          }
          return {};
        },
        createTeam: () => ({ teamId: "team-8" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        sendTeammateMessage: (request) => {
          requests.push({ ...request });
          return {};
        },
        spawnTeammate: (request) => {
          requests.push({ ...request });
          return {
            member: {
              agentType: request.agentType,
              name: request.name,
              sessionId: "team-team-8-worker",
              state: "spawning",
            },
          };
        },
      });
    });
    const client = connect({ transport });
    const team = await client.teams.create({ sessionId: "session-2" });

    const spawned = await team.spawn({
      agentType: "implementer",
      initialPrompt: "start here",
      lead: false,
      mutating: true,
      name: "worker",
    });
    const messaged = await team.message({ body: "status?", from: "operator", to: "worker" });
    const cancelled = await team.cancel("worker");

    expectTypeOf(spawned).toEqualTypeOf<SpawnTeammateResponse>();
    expectTypeOf(messaged).toEqualTypeOf<
      import("../src/gen/mecatl/v1/harness_pb.js").SendTeammateMessageResponse
    >();
    expectTypeOf(cancelled).toEqualTypeOf<CancelTeammateResponse>();
    expect(spawned.member).toMatchObject({
      agentType: "implementer",
      name: "worker",
      sessionId: "team-team-8-worker",
    });
    expect(requests).toMatchObject([
      {
        agentType: "implementer",
        initialPrompt: "start here",
        lead: false,
        mutating: true,
        name: "worker",
        teamId: "team-8",
      },
      { body: "status?", from: "operator", teamId: "team-8", to: "worker" },
      { member: "worker", teamId: "team-8" },
    ]);
    const failure = await team.cancel("missing").catch((error: unknown) => error);
    expect(failure).toMatchObject({
      code: "unknown",
      status: Code.NotFound,
      transport: "grpc",
    });
    expect(failure).toBeInstanceOf(ServerError);
    await client.close();
  });

  it("team run streams events and requires one terminal outcome", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createTeam: (request) => ({ teamId: request.name }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        runTeam: async function* (request) {
          yield {
            event: {
              runId: "member-run-1",
              team: {
                innerKind: "turn.start",
                member: "worker",
                memberSessionId: "team-valid-worker",
                teamId: request.teamId,
              },
              type: "team.member",
            },
            member: "worker",
          };
          if (request.teamId !== "missing") yield { outcome: terminalOutcome() };
          if (request.teamId === "duplicate") yield { outcome: terminalOutcome({ rounds: 2 }) };
        },
      });
    });
    const client = connect({ transport });

    const valid = await client.teams.create({ name: "valid", sessionId: "session-3" });
    const streamed = await collect(valid.run());
    expect(streamed).toHaveLength(2);
    expect(streamed[0]).toMatchObject({
      kind: "team.member",
      member: "worker",
      payload: { memberSessionId: "team-valid-worker" },
    });
    if (streamed[0]?.kind === "team.member") {
      const existingTeamEvent: TeamEvent = streamed[0];
      expect(existingTeamEvent.kind).toBe("team.member");
    }
    expect(streamed[1]).toMatchObject({ kind: "outcome", outcome: { rounds: 1 } });
    await expect(valid.run().result()).resolves.toMatchObject({ stop: "end_turn" });

    const iterated = valid.run();
    await collect(iterated);
    await expect(iterated.result()).rejects.toBeInstanceOf(InvalidStateError);

    const missing = await client.teams.create({ name: "missing", sessionId: "session-3" });
    await expect(missing.run().result()).rejects.toThrow(
      "The RunTeam stream ended without a terminal outcome",
    );
    await expect(missing.run().result()).rejects.toBeInstanceOf(ProtocolError);

    const duplicate = await client.teams.create({ name: "duplicate", sessionId: "session-3" });
    await expect(duplicate.run().result()).rejects.toThrow(
      "The RunTeam stream returned more than one terminal outcome",
    );
    await client.close();
  });

  it("max team tokens can only tighten the daemon budget", async () => {
    const received: number[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createTeam: (request) => {
          received.push(request.maxTeamTokens);
          return { teamId: `team-${received.length}` };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
      });
    });
    const client = connect({ transport });

    await client.teams.create({ maxTeamTokens: 137, sessionId: "session-4" });
    await client.teams.create({ sessionId: "session-4" });

    // The first value crosses the SDK verbatim. The protobuf default for the omitted
    // second value is zero; the SDK neither invents nor clamps a daemon cap.
    expect(received).toEqual([137, 0]);
    await client.close();
  });

  it("team member session ids remain child ids that attach refuses", async () => {
    const childId = "team-team-10-worker";
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createTeam: () => ({ teamId: "team-10" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        runTeam: async function* () {
          yield {
            event: {
              runId: "member-run-10",
              team: {
                innerKind: "turn.end",
                member: "worker",
                memberSessionId: childId,
                teamId: "team-10",
              },
              type: "team.member",
            },
            member: "worker",
          };
          yield { outcome: terminalOutcome() };
        },
      });
    });
    const client = connect({ transport });
    const team = await client.teams.create({ sessionId: "session-5" });
    const frames = await collect(team.run());

    // This unit proof covers byte-preservation only. The e2e suite exercises the
    // real daemon's child-ID refusal instead of teaching a fake transport the answer.
    expect(frames[0]).toMatchObject({
      kind: "team.member",
      payload: { memberSessionId: childId },
    });
    await client.close();
  });

  it("team list and cleanup preserve typed server responses", async () => {
    let cleanups = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        cleanupTeam: () => {
          cleanups += 1;
          return {};
        },
        createTeam: () => ({ teamId: "team-11" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        listTeam: () => ({
          members: [
            {
              agentType: "reviewer",
              name: "lead",
              sessionId: "team-team-11-lead",
              state: "idle",
            },
          ],
          quiescent: true,
          tasks: [
            {
              assignee: "lead",
              deps: ["task-0"],
              description: "publish result",
              id: "task-1",
              state: "completed",
            },
          ],
        }),
        runTeam: async function* () {
          yield { outcome: terminalOutcome() };
        },
      });
    });
    const client = connect({ transport });
    const team = await client.teams.create({ sessionId: "session-6" });

    await team.run().result();
    expect(cleanups).toBe(0);
    const listed = await team.list();
    const cleaned = await team.cleanup();

    expectTypeOf(listed).toEqualTypeOf<ListTeamResponse>();
    expectTypeOf(cleaned).toEqualTypeOf<CleanupTeamResponse>();
    expect(listed).toMatchObject({
      members: [{ sessionId: "team-team-11-lead" }],
      quiescent: true,
      tasks: [{ deps: ["task-0"], id: "task-1", state: "completed" }],
    });
    expect(cleanups).toBe(1);
    await client.close();
  });
});
