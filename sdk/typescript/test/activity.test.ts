import { create } from "@bufbuild/protobuf";
import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import {
  EventSchema,
  HarnessService,
  type WatchSessionEventsResponse,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  ActivityGapError,
  connect,
  MECATL_ATTACH_FILTERED_KINDS,
  NoRunsError,
  type SessionActivity,
  type WatchEnvelope,
} from "../src/index.js";

const sessionId = "session-activity";
const watchFeature = "watch_session_events";

function message(
  cursor: string,
  runId: string,
  text: string,
  phase: "live" | "replay",
): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, {
    cursor,
    event: { runId, text, type: "message.delta" },
    phase,
  });
}

function result(
  cursor: string,
  runId: string,
  phase: "live" | "replay",
): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, {
    cursor,
    event: { result: { stop: "end_turn", text: "done" }, runId, type: "result" },
    phase,
  });
}

function boundary(cursor: string): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, { cursor, phase: "live" });
}

function filtered(
  cursor: string,
  runId: string,
  kind: (typeof MECATL_ATTACH_FILTERED_KINDS)[number],
): WatchSessionEventsResponse {
  const event = (() => {
    switch (kind) {
      case "approval":
        return create(EventSchema, {
          approval: {
            askId: "ask-1",
            callId: "call-1",
            tool: "Bash",
            verdict: "allow_once",
          },
          runId,
          type: kind,
        });
      case "compaction.archive":
        return create(EventSchema, {
          compactionArchive: { replaced: [] },
          runId,
          type: kind,
        });
      case "user_prompt":
        return create(EventSchema, {
          runId,
          type: kind,
          userPrompt: { parts: [], text: "hidden prompt" },
        });
      case "network.attempt":
      case "request.manifest":
        return create(EventSchema, { runId, type: kind });
    }
  })();
  return create(WatchSessionEventsResponseSchema, {
    cursor,
    event,
    phase: "replay",
  });
}

function schedule(
  cursor: string,
  kind: "schedule.fired" | "schedule.skipped",
  phase: "live" | "replay",
): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, {
    cursor,
    event: {
      runId: "",
      schedule: {
        fireId: "sched--fire-1",
        kind: kind.slice("schedule.".length),
        scheduleName: "nightly",
        sessionId,
      },
      type: kind,
    },
    phase,
  });
}

function activityTransport(
  watchSessionEvents: (request: {
    cursor: string;
    runId: string;
    sessionId: string;
  }) => AsyncIterable<WatchSessionEventsResponse>,
): Transport {
  return createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
      getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      watchSessionEvents,
    });
  });
}

async function take(activity: SessionActivity, count: number): Promise<WatchEnvelope[]> {
  const envelopes: WatchEnvelope[] = [];
  for await (const envelope of activity) {
    envelopes.push(envelope);
    if (envelopes.length === count) break;
  }
  return envelopes;
}

function visibleThroughDefaultFilter(envelope: WatchEnvelope): boolean {
  return !(
    envelope.kind === "event" &&
    envelope.event.kind !== "unknown" &&
    (MECATL_ATTACH_FILTERED_KINDS as readonly string[]).includes(envelope.event.kind)
  );
}

describe("session activity", () => {
  it("activity spans every run in the session", async () => {
    const requests: Array<{ cursor: string; runId: string }> = [];
    const transport = activityTransport(async function* (request) {
      requests.push({ cursor: request.cursor, runId: request.runId });
      yield message("cursor-1", "run-1", "one", "replay");
      yield result("cursor-2", "run-1", "replay");
      yield message("cursor-3", "run-2", "two", "replay");
      yield result("cursor-4", "run-2", "replay");
      yield boundary("cursor-4");
      yield message("cursor-5", "run-3", "following", "live");
    });
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);

    const envelopes = await take(await session.activity(), 6);

    expect(requests).toEqual([{ cursor: "", runId: "" }]);
    expect(
      envelopes.flatMap((envelope) =>
        envelope.kind === "event"
          ? [`${envelope.phase}:${envelope.event.runId}:${envelope.event.kind}`]
          : [],
      ),
    ).toEqual([
      "replay:run-1:message.delta",
      "replay:run-1:result",
      "replay:run-2:message.delta",
      "replay:run-2:result",
      "live:run-3:message.delta",
    ]);
    await client.close();
  });

  it("filtered kinds are omitted by default and opt-in restores them", async () => {
    const transport = activityTransport(async function* () {
      yield message("cursor-1", "run-1", "before", "replay");
      for (const [index, kind] of MECATL_ATTACH_FILTERED_KINDS.entries()) {
        yield filtered(`cursor-${index + 2}`, "run-1", kind);
      }
      yield boundary("cursor-6");
      yield message("cursor-7", "run-2", "after", "live");
    });
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);

    const defaultView = await take(await session.activity(), 3);
    const fullView = await take(await session.activity({ includeLogOnly: true }), 8);

    expect(fullView.filter(visibleThroughDefaultFilter)).toEqual(defaultView);
    expect(
      fullView.flatMap((envelope) =>
        envelope.kind === "event" && !visibleThroughDefaultFilter(envelope)
          ? [envelope.event.kind]
          : [],
      ),
    ).toEqual(MECATL_ATTACH_FILTERED_KINDS);
    await client.close();
  });

  it("gap frames survive filtering", async () => {
    const transport = activityTransport(async function* () {
      yield filtered("cursor-1", "", "user_prompt");
      yield boundary("cursor-1");
      yield create(WatchSessionEventsResponseSchema, { cursor: "cursor-gap", phase: "gap" });
    });
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);

    const activity = await session.activity();
    const iterator = activity[Symbol.asyncIterator]();
    const boundaryEnvelope = await iterator.next();
    const gapEnvelope = await iterator.next();

    expect(boundaryEnvelope.value?.kind).toBe("boundary");
    expect(gapEnvelope.value).toEqual({ kind: "gap", phase: "gap" });
    await expect(iterator.next()).rejects.toBeInstanceOf(ActivityGapError);
    await client.close();
  });

  it("activity follows a run-less session where attach refuses", async () => {
    let liveRunlessEvents = 0;
    const filters: string[] = [];
    const transport = activityTransport(async function* (request) {
      filters.push(request.runId);
      yield schedule("cursor-1", "schedule.fired", "replay");
      yield boundary("cursor-1");
      liveRunlessEvents += 1;
      yield schedule("cursor-2", "schedule.skipped", "live");
    });
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);

    await expect(session.attach()).rejects.toBeInstanceOf(NoRunsError);
    expect(liveRunlessEvents).toBe(0);

    const envelopes = await take(await session.activity(), 3);
    const runless = envelopes.filter((envelope) => envelope.kind === "event");

    expect(filters).toEqual(["", ""]);
    expect(liveRunlessEvents).toBe(1);
    expect(runless.map((envelope) => envelope.event.runId)).toEqual(["", ""]);
    expect(runless.map((envelope) => envelope.event.kind)).toEqual([
      "schedule.fired",
      "schedule.skipped",
    ]);
    await client.close();
  });
});
