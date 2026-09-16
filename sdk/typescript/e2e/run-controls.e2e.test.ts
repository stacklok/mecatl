import { readFile, stat } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

import { describe, expect, it } from "vitest";

import {
  audioPart,
  type Client,
  connect as connectHttp,
  type Event,
  imagePart,
  MecatlError,
  ServerError,
  type Session,
} from "../src/index.js";
import { connect as connectGrpc } from "../src/node.js";
import {
  type Daemon,
  type DaemonOptions,
  fixture,
  type ReadyDocument,
  withDaemon,
} from "./harness.js";

interface WireCase {
  readonly name: "grpc tcp" | "grpc uds" | "http";
  readonly daemon: DaemonOptions;
  connect(ready: ReadyDocument): Client;
}

const wireCases: readonly WireCase[] = [
  {
    connect: (ready) => connectGrpc({ baseUrl: `http://${ready.grpc_address}` }),
    daemon: { trustProject: true },
    name: "grpc tcp",
  },
  {
    connect: (ready) => {
      if (ready.socket_path === undefined) throw new Error("mecated omitted its UDS path");
      return connectGrpc({ socketPath: ready.socket_path });
    },
    daemon: { trustProject: true, uds: true },
    name: "grpc uds",
  },
  {
    connect: (ready) => {
      if (ready.http_address === undefined) throw new Error("mecated omitted its HTTP address");
      return connectHttp({ baseUrl: `http://${ready.http_address}` });
    },
    daemon: { http: true, trustProject: true },
    name: "http",
  },
];

async function nextAsk(run: AsyncIterable<Event>): Promise<{
  readonly askId: string;
  readonly events: Event[];
  readonly iterator: AsyncIterator<Event>;
  readonly tool: string;
}> {
  const iterator = run[Symbol.asyncIterator]();
  const events: Event[] = [];
  for (;;) {
    const next = await iterator.next();
    if (next.done) {
      throw new Error(
        `run ended before its permission ask; events: ${events.map((event) => event.kind).join(", ")}`,
      );
    }
    events.push(next.value);
    if (next.value.kind === "permission.ask") {
      return {
        askId: next.value.payload.askId,
        events,
        iterator,
        tool: next.value.payload.tool,
      };
    }
  }
}

async function finishRun(iterator: AsyncIterator<Event>, events: Event[]): Promise<Event> {
  for (;;) {
    const next = await iterator.next();
    if (next.done) break;
    events.push(next.value);
  }
  let terminal: Event | undefined;
  for (let index = events.length - 1; index >= 0; index -= 1) {
    if (events[index]?.kind === "result") {
      terminal = events[index];
      break;
    }
  }
  if (terminal === undefined) throw new Error("run ended without a result event");
  return terminal;
}

async function expectServerCode(
  promise: Promise<unknown>,
  code: string,
  transport: "grpc" | "http",
): Promise<ServerError> {
  const failure = await promise.then(
    () => undefined,
    (error: unknown) => error,
  );
  expect(failure).toBeInstanceOf(ServerError);
  expect(failure).toMatchObject({ code, transport });
  return failure as ServerError;
}

async function waitForCompleted(session: Session): Promise<void> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    if ((await session.snapshot()).state === "completed") return;
    await delay(20);
  }
  throw new Error(`session ${session.id} did not complete after acknowledgement`);
}

async function exerciseLiveControls(testCase: WireCase, daemon: Daemon): Promise<void> {
  if (testCase.name === "grpc tcp") {
    expect((await stat(daemon.userModelDirectory)).isDirectory()).toBe(true);
    await expect(readFile(daemon.ambientUserModelPath, "utf8")).resolves.toBe(
      "ambient user-model poison\n",
    );
  }
  if (testCase.name === "grpc uds") {
    const socketPath = daemon.ready.socket_path ?? "";
    expect(basename(dirname(socketPath))).toBe(".scratch");
    expect(basename(socketPath)).toMatch(/^[A-Za-z0-9]{6}\.sock$/u);
    expect(Buffer.byteLength(socketPath)).toBeLessThan(104);
  }
  const client = testCase.connect(daemon.ready);
  const transport = testCase.name === "http" ? "http" : "grpc";
  try {
    const rootSession = await client.sessions.create({});
    const rootRun = await rootSession.run("ask before writing");
    const rootAsk = await nextAsk(rootRun);
    expect(rootAsk.tool).toBe("Write");
    const rootControls = rootSession.controls(rootRun.id);
    await rootControls.resolveAsk(rootAsk.askId, "allow_once");
    await expectServerCode(
      rootControls.resolveAsk(rootAsk.askId, "allow_once"),
      "ask_not_pending",
      transport,
    );
    expect(await finishRun(rootAsk.iterator, rootAsk.events)).toMatchObject({
      kind: "result",
      payload: { stop: "end_turn", text: "root approval completed" },
      runId: rootRun.id,
    });
    await rootSession.delete();

    const childSession = await client.sessions.create({});
    const childRun = await childSession.run("delegate the scripted shell check");
    const childAsk = await nextAsk(childRun);
    expect(childAsk.tool).toBe("Shell");
    await childSession.controls(childRun.id).resolveAsk(childAsk.askId, "allow_once");
    expect(await finishRun(childAsk.iterator, childAsk.events)).toMatchObject({
      kind: "result",
      payload: { stop: "end_turn", text: "parent observed the child result" },
      runId: childRun.id,
    });
    await childSession.delete();

    const cancelledSession = await client.sessions.create({});
    const cancelledRun = await cancelledSession.run("hold for cancellation");
    const cancelledControls = cancelledSession.controls(cancelledRun.id);
    await expect(
      cancelledControls.cancelSteer(undefined, { timeoutMs: 10_000 }),
    ).resolves.toMatchObject({ outcome: "none_pending", runId: cancelledRun.id });
    await cancelledControls.cancel();
    const cancelledEvents: Event[] = [];
    for await (const event of cancelledRun) cancelledEvents.push(event);
    expect(cancelledEvents.at(-1)).toMatchObject({
      kind: "result",
      payload: { stop: "cancelled" },
      runId: cancelledRun.id,
    });
    for (const operation of [
      () => cancelledControls.resolveAsk("cancelled-ask", "deny"),
      () => cancelledControls.cancel(),
      () => cancelledControls.steer("late"),
      () => cancelledControls.cancelSteer(),
    ]) {
      await expectServerCode(operation(), "stale_run_control", transport);
    }
    await cancelledSession.delete();

    const steerSession = await client.sessions.create({});
    const steerRun = await steerSession.run("hold for steering");
    const steerControls = steerSession.controls(steerRun.id);
    await expect(
      steerControls.steer("inspect the media", { messageId: "text-1" }),
    ).resolves.toEqual({
      messageId: "text-1",
      outcome: "accepted",
      runId: steerRun.id,
    });
    await expect(
      steerControls.steer(
        [
          imagePart({ bytes: new Uint8Array([1, 2, 3]), mimeType: "image/png" }),
          audioPart({ mimeType: "audio/wav", url: "https://example.test/control.wav" }),
        ],
        { messageId: "media-2" },
      ),
    ).resolves.toEqual({ messageId: "media-2", outcome: "appended", runId: steerRun.id });
    await expect(steerControls.cancelSteer({ messageId: "media-2" })).resolves.toEqual({
      messageId: "media-2",
      outcome: "retracted",
      runId: steerRun.id,
    });
    await expect(steerControls.cancelSteer({ messageId: "none-3" })).resolves.toEqual({
      messageId: "none-3",
      outcome: "none_pending",
      runId: steerRun.id,
    });
    await steerControls.cancel();
    for await (const _event of steerRun) {
      // Drain the owned stream so the next scripted run starts after cancellation.
    }
    await steerSession.delete();

    const staleSession = await client.sessions.create({});
    const ended = await staleSession.run("finish the addressed run");
    const endedId = ended.id;
    for await (const _event of ended) {
      // Drain to the terminal state before issuing exact-run controls.
    }
    const endedControls = staleSession.controls(endedId);
    for (const operation of [
      () => endedControls.resolveAsk("ended-ask", "deny"),
      () => endedControls.cancel(),
      () => endedControls.steer("late"),
      () => endedControls.cancelSteer(),
    ]) {
      await expectServerCode(operation(), "stale_run_control", transport);
    }

    const replacement = await staleSession.run("keep a replacement active");
    for (const operation of [
      () => endedControls.resolveAsk("replacement-ask", "deny"),
      () => endedControls.cancel(),
      () => endedControls.steer("late replacement steer"),
      () => endedControls.cancelSteer(),
    ]) {
      const stale = await expectServerCode(operation(), "stale_run_control", transport);
      expect(stale.message).not.toContain(replacement.id);
    }
    await staleSession.controls(replacement.id).cancel();
    for await (const _event of replacement) {
      // The replacement must remain independently controllable.
    }
    await staleSession.delete();

    const planSession = await client.sessions.create({ mode: 2 });
    const planRun = await planSession.run("present the scripted plan");
    const planAsk = await nextAsk(planRun);
    expect(planAsk.tool).toBe("PresentPlan");
    await expectServerCode(
      planSession.controls(planRun.id).resolveAsk(planAsk.askId, "allow_once"),
      "plan_resolution_required",
      transport,
    );
    await planSession.controls(planRun.id).cancel();
    await finishRun(planAsk.iterator, planAsk.events);
    await planSession.delete();
  } finally {
    await client.close();
  }
}

function rejectWhenAborted(signal: AbortSignal | null): Promise<never> {
  if (signal === null) throw new Error("HTTP control request omitted its cancellation signal");
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise((_, reject) => {
    const guard = setTimeout(
      () => reject(new Error("HTTP control deadline did not reach fetch")),
      1_000,
    );
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(guard);
        reject(signal.reason);
      },
      { once: true },
    );
  });
}

async function exerciseRequestCancellationAndDeadline(daemon: Daemon): Promise<void> {
  if (daemon.ready.http_address === undefined) throw new Error("mecated omitted its HTTP address");
  const cancellation = new AbortController();
  let boundedControlRequests = 0;
  const client = connectHttp({
    baseUrl: `http://${daemon.ready.http_address}`,
    fetch: async (input, init) => {
      const response = await fetch(input, init);
      if (new URL(String(input)).pathname.endsWith("/controls/cancel-steer")) {
        boundedControlRequests += 1;
        if (boundedControlRequests === 1) {
          cancellation.abort(new Error("cancel after real-wire acknowledgement"));
          init?.signal?.throwIfAborted();
        }
        if (boundedControlRequests === 2) {
          await rejectWhenAborted(init?.signal ?? null);
        }
      }
      return response;
    },
  });
  try {
    const session = await client.sessions.create({});
    const run = await session.run("hold while request lifetime expires");
    const controls = session.controls(run.id);

    await expect(
      controls.cancelSteer(undefined, { signal: cancellation.signal }),
    ).rejects.toBeInstanceOf(MecatlError);
    expect(boundedControlRequests).toBe(1);
    await expect(controls.cancelSteer(undefined, { timeoutMs: 20 })).rejects.toBeInstanceOf(
      MecatlError,
    );
    expect(boundedControlRequests).toBe(2);

    await expect(controls.cancelSteer()).resolves.toMatchObject({
      outcome: "none_pending",
      runId: run.id,
    });
    expect(boundedControlRequests).toBe(3);
    await controls.cancel();
    const events: Event[] = [];
    for await (const event of run) events.push(event);
    expect(events.at(-1)).toMatchObject({
      kind: "result",
      payload: { stop: "cancelled" },
      runId: run.id,
    });
    await session.delete();
  } finally {
    await client.close();
  }
}

async function exerciseRestartedResolve(testCase: WireCase): Promise<void> {
  await withDaemon(
    {
      ...testCase.daemon,
      durable: true,
      script: fixture("run-controls-awaiting.json"),
    },
    async (daemon) => {
      const originalClient = testCase.connect(daemon.ready);
      const originalSession = await originalClient.sessions.create({});
      const run = await originalSession.run("park before daemon restart");
      const pending = await nextAsk(run);

      await daemon.restart({ script: fixture("run-controls-awaiting-resume.json") });
      await originalClient.close();

      const client = testCase.connect(daemon.ready);
      try {
        const session = await client.sessions.get(originalSession.id);
        const controls = session.controls(run.id);
        const transport = testCase.name === "http" ? "http" : "grpc";
        await expectServerCode(controls.cancel(), "no_active_run", transport);
        await expectServerCode(
          controls.steer("cannot steer a parked run"),
          "stale_run_control",
          transport,
        );
        await expectServerCode(controls.cancelSteer(), "stale_run_control", transport);
        await expectServerCode(
          controls.resolveAsk("wrong-ask", "allow_once"),
          "ask_not_pending",
          transport,
        );

        await expect(controls.resolveAsk(pending.askId, "allow_once")).resolves.toBeUndefined();
        await waitForCompleted(session);
        await expect(
          readFile(join(daemon.workspace, "restart-control-approved.txt"), "utf8"),
        ).resolves.toBe("approved by detached controls\n");
        await session.delete();
      } finally {
        await client.close();
      }
    },
  );
}

describe("offline run-control wire", () => {
  it("run controls work over grpc tcp uds and http", async () => {
    for (const testCase of wireCases) {
      await withDaemon(
        {
          ...testCase.daemon,
          script: fixture("run-controls.json"),
        },
        (daemon) => exerciseLiveControls(testCase, daemon),
      );
      await exerciseRestartedResolve(testCase);
    }
    await withDaemon(
      { http: true, script: fixture("cancel.json") },
      exerciseRequestCancellationAndDeadline,
    );
  }, 120_000);
});
