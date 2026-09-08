import { readFile } from "node:fs/promises";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type AttachedRun,
  type Client,
  CursorMalformedError,
  connect as connectHttp,
  createHttpTransport,
  createRawClient,
  ServerError,
  type WatchEnvelope,
} from "../src/index.js";
import { connect as connectGrpc } from "../src/node.js";
import { fixture, type ReadyDocument, withDaemon } from "./harness.js";

function grpcClient(ready: ReadyDocument): Client {
  return ready.transport === "unix"
    ? connectGrpc({ socketPath: ready.socket_path as string })
    : connectGrpc({ baseUrl: `http://${ready.grpc_address}` });
}

async function collectAttachment(attached: AttachedRun): Promise<WatchEnvelope[]> {
  const envelopes: WatchEnvelope[] = [];
  for await (const envelope of attached) envelopes.push(envelope);
  return envelopes;
}

async function drainIterator(
  iterator: AsyncIterator<WatchEnvelope>,
  envelopes: WatchEnvelope[],
): Promise<void> {
  for (;;) {
    const next = await iterator.next();
    if (next.done) return;
    envelopes.push(next.value);
  }
}

async function collectThroughBoundary(
  attached: AttachedRun,
): Promise<{ envelopes: WatchEnvelope[]; iterator: AsyncIterator<WatchEnvelope> }> {
  const iterator = attached[Symbol.asyncIterator]();
  const envelopes: WatchEnvelope[] = [];
  for (;;) {
    const next = await iterator.next();
    if (next.done) throw new Error("attachment ended before the live boundary");
    envelopes.push(next.value);
    if (next.value.kind === "boundary") return { envelopes, iterator };
  }
}

function assertReplayFollow(envelopes: WatchEnvelope[], runId: string): void {
  const boundary = envelopes.findIndex((envelope) => envelope.kind === "boundary");
  const replay = envelopes.findIndex(
    (envelope) => envelope.kind === "event" && envelope.phase === "replay",
  );
  const live = envelopes.findIndex(
    (envelope) => envelope.kind === "event" && envelope.phase === "live",
  );
  expect(replay).toBeGreaterThanOrEqual(0);
  expect(boundary).toBeGreaterThan(replay);
  expect(live).toBeGreaterThan(boundary);
  expect(
    envelopes.some(
      (envelope) =>
        envelope.kind === "event" &&
        envelope.event.runId === runId &&
        envelope.event.kind === "result",
    ),
  ).toBe(true);
}

async function exerciseAttach(client: Client): Promise<void> {
  const session = await client.sessions.create({});
  const run = await session.run("prove replay then live follow");
  const ownedResult = run.result();
  const attached = await session.attach(run.id);
  const envelopes = await collectAttachment(attached);

  assertReplayFollow(envelopes, run.id);
  await expect(ownedResult).resolves.toMatchObject({
    runId: run.id,
    stopReason: "end_turn",
    text: "attachment completed on the real wire",
  });
  expect(attached.live).toBe(false);
  await session.delete();
}

async function* one<T>(value: T): AsyncGenerator<T> {
  yield value;
}

async function drain<T>(values: AsyncIterable<T>): Promise<void> {
  for await (const _value of values) {
    // The terminal SSE error is raised by the iterator rather than returned.
  }
}

describe("offline durable attachment wire", () => {
  it("attach replays and follows a live run over gRPC", async () => {
    await withDaemon({ script: fixture("attach.json") }, async ({ ready }) => {
      const client = grpcClient(ready);
      try {
        await exerciseAttach(client);
      } finally {
        await client.close();
      }
    });
  });

  it("attach reaches the daemon over a unix domain socket", async () => {
    await withDaemon({ script: fixture("attach.json"), uds: true }, async ({ ready }) => {
      expect(ready.socket_path).toBeTypeOf("string");
      const client = grpcClient(ready);
      try {
        await exerciseAttach(client);
      } finally {
        await client.close();
      }
    });
  });

  it("attach replays and follows a live run over HTTP/SSE", async () => {
    await withDaemon({ http: true, script: fixture("attach.json") }, async ({ ready }) => {
      if (ready.http_address === undefined) throw new Error("mecated omitted HTTP readiness");
      const client = connectHttp({ baseUrl: `http://${ready.http_address}` });
      try {
        await exerciseAttach(client);
      } finally {
        await client.close();
      }
    });
  });

  it("an awaiting approval resolved outside the SDK survives a restart on the same run id", async () => {
    await withDaemon(
      { durable: true, http: true, script: fixture("awaiting.json") },
      async (daemon) => {
        const client = grpcClient(daemon.ready);
        try {
          const session = await client.sessions.create({});
          const run = await session.run("park on the scripted write approval");
          const ownedEnd = run.result().catch((error: unknown) => error);
          const attached = await session.attach(run.id);
          const iterator = attached[Symbol.asyncIterator]();
          const envelopes: WatchEnvelope[] = [];
          let askId = "";
          while (askId === "") {
            const next = await iterator.next();
            if (next.done) throw new Error("attachment ended before the permission ask");
            envelopes.push(next.value);
            if (next.value.kind === "event" && next.value.event.kind === "permission.ask") {
              askId = next.value.event.payload.askId;
            }
          }

          await daemon.restart({ script: fixture("awaiting-resume.json") });
          if (daemon.ready.http_address === undefined) {
            throw new Error("restarted mecated omitted HTTP readiness");
          }
          const response = await fetch(
            `http://${daemon.ready.http_address}/v1/sessions/${encodeURIComponent(session.id)}/approve`,
            {
              body: JSON.stringify({
                ask_id: askId,
                expected_run_id: run.id,
                verdict: "allow_once",
              }),
              headers: { "content-type": "application/json" },
              method: "POST",
              signal: AbortSignal.timeout(10_000),
            },
          );
          expect(response.status).toBe(200);
          expect(response.headers.get("content-type")).toContain("text/event-stream");
          await response.arrayBuffer();
          await drainIterator(iterator, envelopes);

          const terminal = envelopes.find(
            (envelope) => envelope.kind === "event" && envelope.event.kind === "result",
          );
          expect(terminal).toMatchObject({
            event: {
              payload: { stop: "end_turn", text: "approval resumed the persisted run" },
              runId: run.id,
            },
            kind: "event",
          });
          expect(
            envelopes.some(
              (envelope) =>
                envelope.kind === "event" &&
                envelope.event.runId === run.id &&
                envelope.event.kind === "tool.result",
            ),
          ).toBe(true);
          await expect(
            readFile(join(daemon.workspace, "restart-approved.txt"), "utf8"),
          ).resolves.toBe("approved after restart\n");
          expect(attached.live).toBe(false);
          await ownedEnd;
          await session.delete();
        } finally {
          await client.close();
        }
      },
    );
  });

  it("an attached cancel stops a real run and a stale one fails typed", async () => {
    await withDaemon({ http: true, script: fixture("attach-cancel.json") }, async ({ ready }) => {
      if (ready.http_address === undefined) throw new Error("mecated omitted HTTP readiness");
      const client = connectHttp({ baseUrl: `http://${ready.http_address}` });
      try {
        const session = await client.sessions.create({});
        const run = await session.run("cancel this attached run");
        const ownedResult = run.result();
        const attached = await session.attach(run.id);
        const { envelopes, iterator } = await collectThroughBoundary(attached);
        const attachedEnd = drainIterator(iterator, envelopes);
        await attached.cancel();
        await attachedEnd;

        await expect(ownedResult).resolves.toMatchObject({ stopReason: "cancelled" });
        expect(envelopes.at(-1)).toMatchObject({
          event: { payload: { stop: "cancelled" }, runId: run.id },
          kind: "event",
        });

        const newer = await session.run("the stale cancel must not touch this run");
        const newerResult = newer.result();
        const stale = await attached.cancel().catch((error: unknown) => error);
        expect(stale).toBeInstanceOf(ServerError);
        expect(stale).toMatchObject({ code: "stale_run_control", transport: "http" });
        await expect(newerResult).resolves.toMatchObject({
          stopReason: "end_turn",
          text: "the newer run survived the stale cancel",
        });
        await session.delete();
      } finally {
        await client.close();
      }
    });
  });

  it("a malformed cursor mid-watch arrives as a terminal SSE error frame", async () => {
    await withDaemon({ http: true }, async ({ ready }) => {
      if (ready.http_address === undefined) throw new Error("mecated omitted HTTP readiness");
      const baseUrl = `http://${ready.http_address}`;
      const client = connectHttp({ baseUrl });
      try {
        const session = await client.sessions.create({});
        const raw = createRawClient({ transport: createHttpTransport({ baseUrl }) });
        const failure = await drain(
          raw.stream(
            HarnessService.method.watchSessionEvents,
            one({ cursor: "%", runId: "", sessionId: session.id }),
          ),
        ).catch((error: unknown) => error);

        expect(failure).toBeInstanceOf(CursorMalformedError);
        expect(failure).toMatchObject({
          code: "cursor_malformed",
          status: 200,
          transport: "http",
        });
        await session.delete();
      } finally {
        await client.close();
      }
    });
  });

  it("a log-only record is filtered by default and restored by opt-in", async () => {
    await withDaemon({ script: fixture("log-only.json") }, async ({ ready }) => {
      const client = grpcClient(ready);
      try {
        const session = await client.sessions.create({});
        const run = await session.run("this prompt is a durable log-only record");
        await run.result();

        const defaultView = await session.attach(run.id);
        const defaultEnvelopes = await collectAttachment(defaultView);
        expect(
          defaultEnvelopes.some(
            (envelope) => envelope.kind === "event" && envelope.event.kind === "user_prompt",
          ),
        ).toBe(false);

        const fullView = await session.attach(run.id, { includeLogOnly: true });
        const fullEnvelopes = await collectAttachment(fullView);
        expect(
          fullEnvelopes.some(
            (envelope) => envelope.kind === "event" && envelope.event.kind === "user_prompt",
          ),
        ).toBe(true);

        const resumed = await session.attach(run.id, { from: defaultView.cursor });
        const resumedIterator = resumed[Symbol.asyncIterator]();
        const first = await resumedIterator.next();
        expect(first).toMatchObject({ done: false, value: { kind: "boundary", phase: "live" } });
        await resumedIterator.return?.();
        await session.delete();
      } finally {
        await client.close();
      }
    });
  });
});
