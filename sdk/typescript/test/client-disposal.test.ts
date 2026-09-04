import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { create } from "@bufbuild/protobuf";
import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { connectTransport } from "../src/client.js";
import {
  HarnessService,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, type DiagnosticRecord, InvalidStateError, type Session } from "../src/index.js";
import { spawnInternal } from "../src/spawn.js";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const scratchRoot = join(repositoryRoot, ".scratch");
const watchFeature = "watch_session_events";

interface ProcessExit {
  code: number | null;
  signal: NodeJS.Signals | null;
}

interface SpawnHarnessOptions {
  pid?: number;
  sigkillStops?: boolean;
  sigtermStops?: boolean;
}

function deferred<T>() {
  let resolvePromise!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve;
  });
  return { promise, resolve: resolvePromise };
}

function event(runId: string, type: string, text = "") {
  return { event: { runId, text, type } };
}

function lifecycleTransport(events: string[] = []): Transport {
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      createSession: () => ({ sessionId: "session-1" }),
      getCompatibilityInfo: () => ({ apiMajor: 1, features: [watchFeature] }),
      getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      converse: async function* (requests) {
        const input = requests[Symbol.asyncIterator]();
        await input.next();
        yield event("run-1", "message.delta", "accepted");
        const control = await input.next();
        if (control.value?.kind.case === "cancel") events.push("run");
      },
      watchSessionEvents: async function* (_request, context) {
        yield create(WatchSessionEventsResponseSchema, {
          cursor: "cursor-1",
          phase: "boundary",
        });
        await new Promise<void>((resolveAbort) => {
          if (context.signal.aborted) resolveAbort();
          else context.signal.addEventListener("abort", () => resolveAbort(), { once: true });
        });
        events.push("attachment");
      },
    });
  }) as Transport & AsyncDisposable;
  transport[Symbol.asyncDispose] = async () => {
    events.push("transport");
  };
  return transport;
}

function daemonLifecycle(events: string[] = []) {
  return {
    exit: new Promise<never>(() => undefined),
    removeRuntime: async () => {
      events.push("runtime");
    },
    stop: async () => {
      events.push("daemon");
    },
  };
}

function spawnHarness(testRoot: string, options: SpawnHarnessOptions = {}) {
  const events: string[] = [];
  const killed: ("SIGKILL" | "SIGTERM")[] = [];
  const exit = deferred<ProcessExit>();
  const transport = lifecycleTransport(events);
  let running = false;

  const finish = (status: ProcessExit) => {
    if (!running) return;
    running = false;
    exit.resolve(status);
  };

  return {
    events,
    finish,
    internal: {
      createTransport: () => transport,
      launcher: (request: { args: readonly string[] }) => {
        running = true;
        const readyIndex = request.args.indexOf("--ready-file");
        const socketIndex = request.args.indexOf("--grpc-unix-socket");
        const readyFile = request.args[readyIndex + 1];
        const socketPath = request.args[socketIndex + 1];
        if (readyFile === undefined || socketPath === undefined) throw new Error("missing argv");
        setImmediate(() => {
          void writeFile(
            readyFile,
            JSON.stringify({
              api_major: 1,
              features: ["mcp_servers_on_create"],
              pid: options.pid ?? 4242,
              schema: "mecated-ready/1",
              socket_path: socketPath,
              transport: "unix",
            }),
          );
        });
        return {
          closeLifetime: () => events.push("close-lifetime"),
          exit: exit.promise,
          isRunning: () => running,
          kill: (signal: "SIGKILL" | "SIGTERM") => {
            killed.push(signal);
            events.push(signal);
            if (signal === "SIGTERM" && (options.sigtermStops ?? true)) {
              finish({ code: null, signal });
            }
            if (signal === "SIGKILL" && (options.sigkillStops ?? true)) {
              finish({ code: null, signal });
            }
          },
        };
      },
      scheduler: {
        sleep: () => new Promise<void>((resolveSleep) => setImmediate(resolveSleep)),
      },
      tempDirectory: testRoot,
    },
    killed,
  };
}

let testRoot: string;

beforeEach(async () => {
  await mkdir(scratchRoot, { recursive: true });
  testRoot = await mkdtemp(join(scratchRoot, "sdk-client-disposal-test-"));
});

afterEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  await rm(testRoot, { force: true, recursive: true });
});

describe("client disposal", () => {
  it("close tears down owned resources in a fixed order", async () => {
    const events: string[] = [];
    const steps: string[] = [];
    class VisibilityTarget extends EventTarget {
      readonly visibilityState = "visible";

      override removeEventListener(
        type: string,
        callback: EventListenerOrEventListenerObject | null,
        options?: EventListenerOptions | boolean,
      ): void {
        events.push("status");
        super.removeEventListener(type, callback, options);
      }
    }
    vi.stubGlobal("document", new VisibilityTarget());

    const client = connectTransport({
      diagnostics: () => undefined,
      internal: {
        daemon: daemonLifecycle(events),
        onTeardownStep: (step) => steps.push(step),
        toolHost: {
          abort: () => events.push("host-abort"),
          start: () => {
            events.push("host-start");
          },
          stop: async () => {
            events.push("host-stop");
          },
        },
      },
      owned: true,
      transport: lifecycleTransport(events),
      transportKind: "grpc",
      visibility: true,
    });
    const session = await client.sessions.create({});
    const run = await session.run("keep running");
    const runIterator = run[Symbol.asyncIterator]();
    await runIterator.next();
    const pendingRun = runIterator.next().catch(() => undefined);
    const activity = await session.activity();
    const activityIterator = activity[Symbol.asyncIterator]();
    await activityIterator.next();
    const pendingActivity = activityIterator.next().catch(() => undefined);
    client.status.subscribe(() => undefined);
    events.length = 0;

    await client.close();
    await Promise.all([pendingRun, pendingActivity]);

    expect(steps).toEqual([
      "cancel_owned_run",
      "release_attachment",
      "status_monitor",
      "tool_host_abort",
      "tool_host_stop",
      "transport",
      "daemon_stop",
      "runtime_directory",
    ]);
    expect(new Set(events.slice(0, 2))).toEqual(new Set(["run", "attachment"]));
    expect(events.slice(2)).toEqual([
      "status",
      "host-abort",
      "host-stop",
      "transport",
      "daemon",
      "runtime",
    ]);
  });

  it("closing a connected client signals no process", async () => {
    const signal = vi.spyOn(process, "kill");
    const transport = lifecycleTransport();
    const dispose = vi.spyOn(transport as Transport & AsyncDisposable, Symbol.asyncDispose);
    const client = connect({ transport });

    await client.close();

    expect(signal).not.toHaveBeenCalled();
    expect(dispose).not.toHaveBeenCalled();
  });

  it("a daemon that stops on SIGTERM is never escalated", async () => {
    const harness = spawnHarness(testRoot);
    const client = await spawnInternal({ binaryPath: process.execPath }, harness.internal);

    await client.close();

    expect(harness.killed).toEqual(["SIGTERM"]);
  });

  it("an unresponsive daemon is escalated to SIGKILL and close resolves", async () => {
    const harness = spawnHarness(testRoot, { sigtermStops: false });
    const client = await spawnInternal({ binaryPath: process.execPath }, harness.internal);

    await expect(client.close()).resolves.toBeUndefined();
    expect(harness.killed).toEqual(["SIGTERM", "SIGKILL"]);
  });

  it("asyncDispose and close are the same idempotent teardown", async () => {
    const harness = spawnHarness(testRoot);
    const client = await spawnInternal({ binaryPath: process.execPath }, harness.internal);

    await Promise.all([client.close(), client[Symbol.asyncDispose](), client.close()]);

    expect(harness.killed).toEqual(["SIGTERM"]);
    expect(harness.events.filter((eventName) => eventName === "transport")).toHaveLength(1);
  });

  it("a teardown failure is reported and never aborts disposal", async () => {
    const diagnostics: DiagnosticRecord[] = [];
    const events: string[] = [];
    const transport = lifecycleTransport(events) as Transport & AsyncDisposable;
    transport[Symbol.asyncDispose] = async () => {
      events.push("transport");
      throw new Error("transport failed");
    };
    const client = connectTransport({
      diagnostics: (record) => diagnostics.push(record),
      internal: {
        daemon: {
          exit: new Promise<never>(() => undefined),
          removeRuntime: async () => {
            events.push("runtime");
          },
          stop: async () => {
            events.push("daemon");
            throw new Error("daemon failed");
          },
        },
        toolHost: {
          abort: () => {
            events.push("host-abort");
            throw new Error("abort failed");
          },
          start: () => undefined,
          stop: async () => {
            events.push("host-stop");
            throw new Error("stop failed");
          },
        },
      },
      owned: true,
      transport,
      transportKind: "grpc",
      visibility: false,
    });

    await expect(client.close()).resolves.toBeUndefined();

    expect(events).toEqual(["host-abort", "host-stop", "transport", "daemon", "runtime"]);
    expect(diagnostics.map((record) => record.code)).toEqual([
      "client_disposal_failed",
      "client_disposal_failed",
      "client_disposal_failed",
      "client_disposal_failed",
    ]);
    expect(diagnostics.map((record) => record.fields.step)).toEqual([
      "tool_host_abort",
      "tool_host_stop",
      "transport",
      "daemon_stop",
    ]);
  });

  it("a closed client refuses operations with invalid_state", async () => {
    const client = connect({ transport: lifecycleTransport() });
    const session = await client.sessions.create({});
    const run = await session.run("keep running");
    await client.close();

    const operations: Array<() => Promise<unknown>> = [
      () => client.sessions.create({}),
      () => client.sessions.get("session-1"),
      () => client.sessions.fork("session-1"),
      () => session.attach("run-1"),
      () => session.activity(),
      () => session.run("again"),
      () => session.close(),
      () => session.delete(),
      () => run.approve("ask-1", true),
      () => run.cancel(),
      () => run.resolveAsk("ask-1", "deny"),
      () => run.steer("again"),
    ];
    for (const operation of operations) {
      const error = await operation().catch((reason: unknown) => reason);
      expect(error).toBeInstanceOf(InvalidStateError);
      expect(error).toMatchObject({ code: "invalid_state" });
    }
    expect(() => client.status.subscribe(() => undefined)).toThrow(InvalidStateError);
  });

  it("disposal aborts in-flight handlers before closing the transport", async () => {
    const events: string[] = [];
    const handler = new AbortController();
    const client = connectTransport({
      internal: {
        toolHost: {
          abort: (reason) => handler.abort(reason),
          start: () => undefined,
          stop: async () => {
            expect(handler.signal.aborted).toBe(true);
            events.push("host-stopped");
          },
        },
      },
      owned: true,
      transport: lifecycleTransport(events),
      transportKind: "grpc",
      visibility: false,
    });

    await client.close();

    expect(events).toEqual(["host-stopped", "transport"]);
  });

  it("a daemon that dies mid-session fails the client typed", async () => {
    const diagnostics: DiagnosticRecord[] = [];
    const harness = spawnHarness(testRoot);
    const client = await spawnInternal(
      { binaryPath: process.execPath, diagnostics: (record) => diagnostics.push(record) },
      harness.internal,
    );
    const session: Session = await client.sessions.create({});
    let operationAtOfflineNotification: Promise<unknown> | undefined;
    client.status.subscribe((status) => {
      if (status === "offline") {
        operationAtOfflineNotification = client.sessions
          .create({})
          .catch((reason: unknown) => reason);
      }
    });

    harness.finish({ code: 17, signal: null });
    await new Promise<void>((resolveWait) => setImmediate(resolveWait));

    expect(operationAtOfflineNotification).toBeDefined();
    await expect(operationAtOfflineNotification).resolves.toMatchObject({ code: "invalid_state" });
    for (const operation of [() => client.sessions.create({}), () => session.run("too late")]) {
      const error = await operation().catch((reason: unknown) => reason);
      expect(error).toBeInstanceOf(InvalidStateError);
      expect(error).toMatchObject({ code: "invalid_state", transport: "local" });
    }
    expect(diagnostics).toEqual([
      {
        code: "daemon_exited",
        fields: { exitCode: 17, signal: null },
        level: "error",
        message: "The spawned mecated daemon exited unexpectedly",
      },
    ]);
    await client.close();
  });

  it("disposal signals the child handle and never a pid from the ready file", async () => {
    const signal = vi.spyOn(process, "kill");
    const live = spawnHarness(testRoot, { pid: 999_999 });
    const liveClient = await spawnInternal({ binaryPath: process.execPath }, live.internal);

    await liveClient.close();

    expect(live.killed).toEqual(["SIGTERM"]);
    expect(signal).not.toHaveBeenCalled();

    const exitedRoot = await mkdtemp(join(scratchRoot, "sdk-client-disposal-exited-test-"));
    try {
      const exited = spawnHarness(exitedRoot, { pid: 888_888 });
      const exitedClient = await spawnInternal({ binaryPath: process.execPath }, exited.internal);
      exited.finish({ code: 0, signal: null });
      await new Promise<void>((resolveWait) => setImmediate(resolveWait));

      await exitedClient.close();

      expect(exited.killed).toEqual([]);
      expect(signal).not.toHaveBeenCalled();
    } finally {
      await rm(exitedRoot, { force: true, recursive: true });
    }
  });
});
