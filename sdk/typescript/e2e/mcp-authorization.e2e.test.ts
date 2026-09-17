import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

import { describe, expect, it } from "vitest";

import {
  type Client,
  connect as connectHttp,
  type Event,
  RunAuthorizationRequiredError,
  ServerError,
  type Session,
} from "../src/index.js";
import { connect as connectGrpc } from "../src/node.js";
import {
  type AuthorizationDaemon,
  type DaemonOptions,
  fixture,
  type ReadyDocument,
  withAuthorizationDaemon,
} from "./harness.js";

interface WireCase {
  readonly daemon: Omit<DaemonOptions, "authorization" | "script">;
  readonly name: "grpc tcp" | "grpc uds" | "http";
  connect(ready: ReadyDocument): Client;
}

const wireCases: readonly WireCase[] = [
  {
    connect: (ready) => connectGrpc({ baseUrl: `http://${ready.grpc_address}` }),
    daemon: {},
    name: "grpc tcp",
  },
  {
    connect: (ready) => {
      if (ready.socket_path === undefined) throw new Error("fixture omitted its UDS path");
      return connectGrpc({ socketPath: ready.socket_path });
    },
    daemon: { uds: true },
    name: "grpc uds",
  },
  {
    connect: (ready) => {
      if (ready.http_address === undefined) throw new Error("fixture omitted its HTTP address");
      return connectHttp({ baseUrl: `http://${ready.http_address}` });
    },
    daemon: { http: true },
    name: "http",
  },
];

async function parkedAuthorization(
  client: Client,
  prompt: string,
): Promise<{ readonly authorizationId: string; readonly session: Session }> {
  const session = await client.sessions.create({});
  const run = await session.run(prompt);
  const failure = await run.result().catch((error: unknown) => error);
  if (!(failure instanceof RunAuthorizationRequiredError)) {
    throw new Error(`run did not park for authorization: ${String(failure)}`, { cause: failure });
  }
  expect(failure).toBeInstanceOf(RunAuthorizationRequiredError);
  const required = failure.outcome;
  expect(required).toMatchObject({ outcome: "authorization_required", sessionId: session.id });
  expect(required.authorization).toMatchObject({
    kind: "authorization.required",
    payload: { status: "pending" },
    runId: run.id,
  });
  return { authorizationId: required.authorization.payload.authorizationId, session };
}

async function presentAndComplete(
  daemon: AuthorizationDaemon,
  session: Session,
  authorizationId: string,
  decision: "grant" | "deny",
): Promise<void> {
  const responseHeaders: Headers[] = [];
  const presentation = await session.mcpAuthorization(authorizationId).presentation({
    headers: { "x-e2e-caller": "presentation", "x-e2e-session-id": session.id },
    onHeader: (headers) => responseHeaders.push(headers),
    timeoutMs: 10_000,
  });
  expect(responseHeaders.at(-1)?.get("x-e2e-response")).toBe("fixture");
  expect(responseHeaders.at(-1)?.get("x-e2e-caller-seen")).toBe("presentation");
  expect(responseHeaders.at(-1)?.get("x-e2e-affinity-seen")).toBe("true");
  await daemon.completeAuthorization(presentation, decision);
}

async function exerciseLifecycle(testCase: WireCase, daemon: AuthorizationDaemon): Promise<void> {
  const client = testCase.connect(daemon.ready);
  const transport = testCase.name === "http" ? "http" : "grpc";
  try {
    const granted = await parkedAuthorization(client, "grant protected access");
    const grantedHandle = granted.session.mcpAuthorization(granted.authorizationId);
    await expect(grantedHandle.recheck().result()).resolves.toMatchObject({
      outcome: "pending",
      status: "pending",
    });
    await presentAndComplete(daemon, granted.session, granted.authorizationId, "grant");
    const streamHeaders: Headers[] = [];
    await expect(
      grantedHandle
        .recheck(undefined, {
          headers: { "x-e2e-caller": "recheck", "x-e2e-session-id": granted.session.id },
          onHeader: (headers) => streamHeaders.push(headers),
          timeoutMs: 10_000,
        })
        .result(),
    ).resolves.toMatchObject({
      continuation: { content: "authorization grant completed" },
      outcome: "completed",
      status: "granted",
    });
    expect(streamHeaders.at(-1)?.get("x-e2e-caller-seen")).toBe("recheck");
    expect(streamHeaders.at(-1)?.get("x-e2e-affinity-seen")).toBe("true");
    await granted.session.delete();

    const denied = await parkedAuthorization(client, "deny protected access");
    await presentAndComplete(daemon, denied.session, denied.authorizationId, "deny");
    await expect(
      denied.session.mcpAuthorization(denied.authorizationId).recheck().result(),
    ).resolves.toMatchObject({
      continuation: { content: "authorization denial completed" },
      outcome: "completed",
      status: "denied",
    });
    await denied.session.delete();

    const cancelled = await parkedAuthorization(client, "cancel protected access");
    await expect(
      cancelled.session.mcpAuthorization(cancelled.authorizationId).cancel().result(),
    ).resolves.toMatchObject({
      continuation: { content: "authorization cancellation completed" },
      outcome: "completed",
      status: "cancelled",
    });
    await cancelled.session.delete();

    const permissionAllowed = await parkedAuthorization(client, "allow continuation write");
    await presentAndComplete(
      daemon,
      permissionAllowed.session,
      permissionAllowed.authorizationId,
      "grant",
    );
    const allowedAsks: string[] = [];
    await expect(
      permissionAllowed.session
        .mcpAuthorization(permissionAllowed.authorizationId)
        .recheck({
          onPermissionAsk: (ask) => {
            allowedAsks.push(ask.tool);
            return "allow_once";
          },
          permissionRequestOptions: {
            headers: { "x-e2e-caller": "permission-allow" },
            timeoutMs: 10_000,
          },
        })
        .result(),
    ).resolves.toMatchObject({
      continuation: { content: "permission allow completed" },
      outcome: "completed",
      status: "granted",
    });
    expect(allowedAsks).toEqual(["Write"]);
    await expect(readFile(join(daemon.workspace, "permission-allowed.txt"), "utf8")).resolves.toBe(
      "allowed\n",
    );
    await permissionAllowed.session.delete();

    const permissionDenied = await parkedAuthorization(client, "deny continuation write");
    await presentAndComplete(
      daemon,
      permissionDenied.session,
      permissionDenied.authorizationId,
      "grant",
    );
    const deniedAsks: string[] = [];
    await expect(
      permissionDenied.session
        .mcpAuthorization(permissionDenied.authorizationId)
        .recheck({
          onPermissionAsk: (ask) => {
            deniedAsks.push(ask.tool);
            return "deny";
          },
        })
        .result(),
    ).resolves.toMatchObject({
      continuation: { content: "permission deny completed" },
      outcome: "completed",
      status: "granted",
    });
    expect(deniedAsks).toEqual(["Write"]);
    await expect(
      readFile(join(daemon.workspace, "permission-denied.txt"), "utf8"),
    ).rejects.toMatchObject({ code: "ENOENT" });
    await permissionDenied.session.delete();

    const chained = await parkedAuthorization(client, "chain protected access");
    await presentAndComplete(daemon, chained.session, chained.authorizationId, "grant");
    const first = await chained.session
      .mcpAuthorization(chained.authorizationId)
      .recheck()
      .result();
    expect(first).toMatchObject({ outcome: "authorization_required", status: "granted" });
    if (first.outcome !== "authorization_required") {
      throw new Error(`expected chained authorization, got ${first.outcome}`);
    }
    expect(first.nextAuthorization.payload.authorizationId).not.toBe(chained.authorizationId);
    const nextID = first.nextAuthorization.payload.authorizationId;
    await presentAndComplete(daemon, chained.session, nextID, "grant");
    await expect(
      chained.session.mcpAuthorization(nextID).recheck().result(),
    ).resolves.toMatchObject({
      continuation: { content: "chained authorization completed" },
      outcome: "completed",
      status: "granted",
    });
    await chained.session.delete();

    const continuationCancelled = await parkedAuthorization(client, "cancel continuation");
    await presentAndComplete(
      daemon,
      continuationCancelled.session,
      continuationCancelled.authorizationId,
      "grant",
    );
    const cancellationFlow = continuationCancelled.session
      .mcpAuthorization(continuationCancelled.authorizationId)
      .recheck();
    const cancellationIterator = cancellationFlow[Symbol.asyncIterator]();
    const cancellationEvents: Event[] = [];
    while (cancellationFlow.continuationRunId === undefined) {
      const next = await cancellationIterator.next();
      if (next.done) throw new Error("continuation ended before its run id was observed");
      cancellationEvents.push(next.value);
    }
    await cancellationFlow.cancelContinuation({
      headers: { "x-e2e-caller": "continuation-cancel" },
      timeoutMs: 10_000,
    });
    for (;;) {
      const next = await cancellationIterator.next();
      if (next.done) break;
      cancellationEvents.push(next.value);
    }
    expect(cancellationEvents.at(-1)).toMatchObject({
      kind: "result",
      payload: { stop: "cancelled" },
      runId: cancellationFlow.continuationRunId,
    });

    const unknown = await continuationCancelled.session
      .mcpAuthorization("well-formed-unknown")
      .presentation()
      .catch((error: unknown) => error);
    expect(unknown).toBeInstanceOf(ServerError);
    expect(unknown).toMatchObject({ transport });
    expect((unknown as ServerError).code).not.toBe("");
    await continuationCancelled.session.delete();
  } finally {
    await client.close();
  }
}

async function waitForState(session: Session, state: string): Promise<void> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    if ((await session.snapshot()).state === state) return;
    await delay(20);
  }
  throw new Error(`session ${session.id} did not reach ${state}`);
}

async function disconnectActiveContinuation(testCase: WireCase): Promise<void> {
  await withAuthorizationDaemon(
    { ...testCase.daemon, script: fixture("mcp-authorization-disconnect.json") },
    async (daemon) => {
      const client = testCase.connect(daemon.ready);
      try {
        const { authorizationId, session } = await parkedAuthorization(
          client,
          `disconnect active ${testCase.name} continuation`,
        );
        await presentAndComplete(daemon, session, authorizationId, "grant");
        const iterator = session
          .mcpAuthorization(authorizationId)
          .recheck()
          [Symbol.asyncIterator]();
        await expect(iterator.next()).resolves.toMatchObject({
          done: false,
          value: { kind: "authorization.resolved", payload: { status: "granted" } },
        });
        await expect(iterator.return?.()).resolves.toMatchObject({ done: true });
        await waitForState(session, testCase.name === "http" ? "cancelled" : "completed");
        await session.delete();
      } finally {
        await client.close();
      }
    },
  );
}

async function disconnectParkedAsk(testCase: WireCase): Promise<void> {
  await withAuthorizationDaemon(
    { ...testCase.daemon, script: fixture("mcp-authorization-disconnect-ask.json") },
    async (daemon) => {
      const client = testCase.connect(daemon.ready);
      try {
        const { authorizationId, session } = await parkedAuthorization(
          client,
          `disconnect ${testCase.name} permission park`,
        );
        await presentAndComplete(daemon, session, authorizationId, "grant");
        const iterator = session
          .mcpAuthorization(authorizationId)
          .recheck()
          [Symbol.asyncIterator]();
        for (;;) {
          const next = await iterator.next();
          if (next.done) throw new Error("continuation ended before the permission park");
          if (next.value.kind === "permission.ask") break;
        }
        await expect(iterator.return?.()).resolves.toMatchObject({ done: true });
        await waitForState(session, "cancelled");
        await expect(
          readFile(join(daemon.workspace, "stranded.txt"), "utf8"),
        ).rejects.toMatchObject({ code: "ENOENT" });
        await session.delete();
      } finally {
        await client.close();
      }
    },
  );
}

async function disconnectAfterChainedPark(testCase: WireCase): Promise<void> {
  await withAuthorizationDaemon(
    { ...testCase.daemon, script: fixture("mcp-authorization-disconnect-chain.json") },
    async (daemon) => {
      const client = testCase.connect(daemon.ready);
      try {
        const { authorizationId, session } = await parkedAuthorization(
          client,
          `disconnect ${testCase.name} after chained park`,
        );
        await presentAndComplete(daemon, session, authorizationId, "grant");
        const iterator = session
          .mcpAuthorization(authorizationId)
          .recheck()
          [Symbol.asyncIterator]();
        let nextAuthorizationID = "";
        for (;;) {
          const next = await iterator.next();
          if (next.done) throw new Error("continuation ended before the chained authorization");
          if (next.value.kind === "authorization.required") {
            nextAuthorizationID = next.value.payload.authorizationId;
            break;
          }
        }
        expect(nextAuthorizationID).not.toBe(authorizationId);
        await expect(iterator.return?.()).resolves.toMatchObject({ done: true });

        const nextHandle = session.mcpAuthorization(nextAuthorizationID);
        await expect(nextHandle.presentation()).resolves.toMatch(/^https:\/\//u);
        await expect(nextHandle.recheck().result()).resolves.toMatchObject({
          outcome: "pending",
          status: "pending",
        });
        await expect(nextHandle.cancel().result()).resolves.toMatchObject({
          continuation: { content: "chained cancellation completed" },
          outcome: "completed",
          status: "cancelled",
        });
        await session.delete();
      } finally {
        await client.close();
      }
    },
  );
}

describe("real-wire MCP authorization", () => {
  it("MCP authorization works over gRPC TCP UDS and HTTP SSE", async () => {
    for (const testCase of wireCases) {
      await withAuthorizationDaemon(
        { ...testCase.daemon, script: fixture("mcp-authorization-lifecycle.json") },
        async (daemon) => {
          try {
            await exerciseLifecycle(testCase, daemon);
          } catch (cause) {
            throw new Error(`${testCase.name} authorization lifecycle failed`, { cause });
          }
        },
      );
    }
  }, 120_000);

  it("MCP authorization disconnect follows transport and park phase", async () => {
    for (const testCase of wireCases) {
      await disconnectActiveContinuation(testCase);
      await disconnectParkedAsk(testCase);
      await disconnectAfterChainedPark(testCase);
    }
  }, 120_000);
});
