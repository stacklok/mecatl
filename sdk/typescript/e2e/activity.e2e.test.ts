import { describe, expect, it } from "vitest";

import type { Session, SessionActivity, WatchEnvelope } from "../src/index.js";
import { connect } from "../src/node.js";
import { fixture, type ReadyDocument, withDaemon } from "./harness.js";

function endpoint(ready: ReadyDocument): string {
  return `http://${ready.grpc_address}`;
}

async function collectThroughBoundary(
  activity: SessionActivity,
): Promise<{ envelopes: WatchEnvelope[]; iterator: AsyncIterator<WatchEnvelope> }> {
  const envelopes: WatchEnvelope[] = [];
  const iterator = activity[Symbol.asyncIterator]();
  for (;;) {
    const next = await iterator.next();
    if (next.done) throw new Error("activity ended before the live boundary");
    envelopes.push(next.value);
    if (next.value.kind === "boundary") return { envelopes, iterator };
  }
}

async function collectUntilRunResult(
  iterator: AsyncIterator<WatchEnvelope>,
  envelopes: WatchEnvelope[],
  runId: string,
  first: Promise<IteratorResult<WatchEnvelope>> | undefined = undefined,
): Promise<void> {
  let next = first === undefined ? await iterator.next() : await first;
  for (;;) {
    if (next.done) throw new Error(`activity ended before run ${runId}`);
    envelopes.push(next.value);
    if (
      next.value.kind === "event" &&
      next.value.event.runId === runId &&
      next.value.event.kind === "result"
    ) {
      return;
    }
    next = await iterator.next();
  }
}

async function replayActivity(session: Session): Promise<WatchEnvelope[]> {
  const activity = await session.activity({ includeLogOnly: true });
  const { envelopes } = await collectThroughBoundary(activity);
  await activity.close();
  return envelopes;
}

function eventKeys(envelopes: WatchEnvelope[]): string[] {
  return envelopes.flatMap((envelope) =>
    envelope.kind === "event"
      ? [`${envelope.event.runId}:${envelope.event.seq}:${envelope.event.kind}`]
      : [],
  );
}

describe("offline durable activity wire", () => {
  it("a daemon restart mid-watch resumes from the cursor across a new run", async () => {
    await withDaemon(
      { durable: true, script: fixture("activity-before-restart.json") },
      async (daemon) => {
        const watchingClient = connect({ baseUrl: endpoint(daemon.ready) });
        let actorClient: ReturnType<typeof connect> | undefined;
        try {
          const session = await watchingClient.sessions.create({});
          const firstRun = await session.run("record activity before restart");
          await firstRun.result();

          const activity = await session.activity({ includeLogOnly: true });
          const { envelopes, iterator } = await collectThroughBoundary(activity);
          const pending = iterator.next();

          await daemon.restart({ script: fixture("activity-after-restart.json") });
          actorClient = connect({ baseUrl: endpoint(daemon.ready) });
          const adopted = await actorClient.sessions.get(session.id);
          const secondRun = await adopted.run("record activity after restart");
          const secondResult = secondRun.result();
          await collectUntilRunResult(iterator, envelopes, secondRun.id, pending);
          await expect(secondResult).resolves.toMatchObject({
            runId: secondRun.id,
            text: "activity after daemon restart",
          });
          await activity.close();

          const replay = await replayActivity(adopted);
          const resumedKeys = eventKeys(envelopes);
          const replayKeys = eventKeys(replay);
          expect(resumedKeys).toEqual(replayKeys);
          expect(new Set(resumedKeys).size).toBe(resumedKeys.length);
          expect(resumedKeys.some((key) => key.startsWith(`${firstRun.id}:`))).toBe(true);
          expect(resumedKeys.some((key) => key.startsWith(`${secondRun.id}:`))).toBe(true);
          expect(secondRun.id).not.toBe(firstRun.id);
          await adopted.delete();
        } finally {
          await actorClient?.close();
          await watchingClient.close();
        }
      },
    );
  });

  it("activity spans two runs on the real wire", async () => {
    await withDaemon({ script: fixture("activity-before-restart.json") }, async ({ ready }) => {
      const client = connect({ baseUrl: endpoint(ready) });
      try {
        const session = await client.sessions.create({});
        const firstRun = await session.run("first activity run");
        await firstRun.result();

        const activity = await session.activity();
        const { envelopes, iterator } = await collectThroughBoundary(activity);
        const secondRun = await session.run("second activity run");
        const secondResult = secondRun.result();
        await collectUntilRunResult(iterator, envelopes, secondRun.id);
        await secondResult;
        await activity.close();

        const results = envelopes.flatMap((envelope) =>
          envelope.kind === "event" && envelope.event.kind === "result"
            ? [envelope.event.runId]
            : [],
        );
        expect(results).toEqual([firstRun.id, secondRun.id]);
        expect(secondRun.id).not.toBe(firstRun.id);
        await session.delete();
      } finally {
        await client.close();
      }
    });
  });
});
