import fs from "node:fs";

import { fromJson, type JsonValue } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import {
  HarnessService,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  CursorMalformedError,
  CursorScopeError,
  connect,
  InvalidStateError,
  type SdkCursor,
  type SessionActivity,
  type WatchEnvelope,
} from "../src/index.js";

const sessionId = "session-cursor";
const runId = "run-cursor";
const watchFeature = "watch_session_events";

interface DurableRecord {
  readonly event: Record<string, JsonValue>;
  readonly token: string;
}

interface WatchRequest {
  readonly cursor: string;
  readonly runId: string;
}

function message(run: string, token: string, text: string): DurableRecord {
  return { event: { run_id: run, text, type: "message.delta" }, token };
}

function result(run: string, token: string): DurableRecord {
  return {
    event: { result: { stop: "end_turn", text: "done" }, run_id: run, type: "result" },
    token,
  };
}

function ask(run: string, token: string, askId: string): DurableRecord {
  return {
    event: {
      ask: { args: "{}", ask_id: askId, reason: "test", tool: "Bash" },
      run_id: run,
      type: "permission.ask",
    },
    token,
  };
}

function approval(run: string, token: string, askId: string): DurableRecord {
  return {
    event: {
      approval: {
        ask_id: askId,
        call_id: "call-approval",
        tool: "Bash",
        verdict: "APPROVAL_VERDICT_ALLOW_ONCE",
      },
      run_id: run,
      type: "approval",
    },
    token,
  };
}

function harness(records: DurableRecord[]) {
  const requests: WatchRequest[] = [];
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
      getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      watchSessionEvents: async function* (request) {
        requests.push({ cursor: request.cursor, runId: request.runId });
        const matched = records.findIndex((record) => record.token === request.cursor);
        const start = request.cursor === "" ? 0 : matched + 1;
        for (const record of records.slice(start)) {
          if (request.runId !== "" && record.event.run_id !== request.runId) continue;
          yield fromJson(WatchSessionEventsResponseSchema, {
            cursor: record.token,
            event: record.event,
            phase: "replay",
          });
        }
        yield fromJson(WatchSessionEventsResponseSchema, {
          cursor: records.at(-1)?.token ?? request.cursor,
          phase: "live",
        });
      },
    });
  });
  return { records, requests, transport };
}

async function sessionFor(state: ReturnType<typeof harness>) {
  const client = connect({ transport: state.transport });
  const session = await client.sessions.get(sessionId);
  return { client, session };
}

async function collect(activity: SessionActivity): Promise<WatchEnvelope[]> {
  const envelopes: WatchEnvelope[] = [];
  for await (const envelope of activity) envelopes.push(envelope);
  return envelopes;
}

function text(envelope: WatchEnvelope | undefined): string | undefined {
  return envelope?.kind === "event" ? envelope.event.text : undefined;
}

function authoredCursor(value: Record<string, unknown>): SdkCursor {
  return Buffer.from(JSON.stringify(value), "utf8").toString("base64url");
}

describe("attachment cursor", () => {
  it("the checkpoint advances when the consumer requests the next envelope", async () => {
    const state = harness([
      message(runId, "token-1", "one"),
      message(runId, "token-2", "two"),
      result(runId, "token-3"),
    ]);
    const { client, session } = await sessionFor(state);
    const attached = await session.attach(runId);
    const initial = attached.cursor;
    const iterator = attached[Symbol.asyncIterator]();

    const first = (await iterator.next()).value;
    expect(text(first)).toBe("one");
    expect(attached.cursor).toBe(initial);

    const second = (await iterator.next()).value;
    expect(text(second)).toBe("two");
    expect(attached.cursor).toBe(first?.cursor);

    await iterator.return?.();
    await client.close();
  });

  it("the checkpoint advances over filtered records", async () => {
    const state = harness([
      message(runId, "token-1", "one"),
      approval(runId, "token-2", "ask-filtered"),
      message(runId, "token-3", "three"),
      result(runId, "token-4"),
    ]);
    const { client, session } = await sessionFor(state);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();

    expect(text((await iterator.next()).value)).toBe("one");
    expect(text((await iterator.next()).value)).toBe("three");
    const checkpoint = attached.cursor;
    await iterator.return?.();

    const resumed = await session.attach(runId, { from: checkpoint });
    const resumedFirst = await resumed[Symbol.asyncIterator]().next();
    expect(state.requests.at(-1)).toEqual({ cursor: "token-2", runId });
    expect(resumedFirst.value).toMatchObject({ event: { text: "three" } });
    await resumed.close();
    await client.close();
  });

  it("a filtered approval still retires its ask", async () => {
    const state = harness([
      ask(runId, "token-1", "ask-retired"),
      approval(runId, "token-2", "ask-retired"),
      message(runId, "token-3", "after approval"),
      result(runId, "token-4"),
    ]);
    const { client, session } = await sessionFor(state);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();

    expect((await iterator.next()).value).toMatchObject({ event: { kind: "permission.ask" } });
    expect(text((await iterator.next()).value)).toBe("after approval");
    const checkpoint = attached.cursor;
    await iterator.return?.();

    const replay = await collect(await session.attach(runId, { from: checkpoint }));
    expect(state.requests.at(-1)).toEqual({ cursor: "token-2", runId });
    expect(
      replay.map((envelope) => (envelope.kind === "event" ? envelope.event.kind : null)),
    ).toEqual(["message.delta", "result"]);
    await client.close();
  });

  it("resuming from an exposed cursor re-delivers at least once", async () => {
    const state = harness([
      message(runId, "token-1", "processed"),
      message(runId, "token-2", "later"),
      result(runId, "token-3"),
    ]);
    const { client, session } = await sessionFor(state);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();

    expect(text((await iterator.next()).value)).toBe("processed");
    const checkpoint = attached.cursor;
    await attached.close();

    const replay = await collect(await session.attach(runId, { from: checkpoint }));
    expect(state.requests.at(-1)).toEqual({ cursor: "", runId });
    expect(replay.filter((envelope) => text(envelope) === "processed")).toHaveLength(1);
    await client.close();
  });

  it("a serialized cursor resumes through a fresh client", async () => {
    const state = harness([
      message(runId, "token-1", "one"),
      message(runId, "token-2", "two"),
      result(runId, "token-3"),
    ]);
    const first = await sessionFor(state);
    const attached = await first.session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();
    await iterator.next();
    expect(text((await iterator.next()).value)).toBe("two");
    const serialized = JSON.parse(JSON.stringify(attached.cursor)) as string;
    await iterator.return?.();
    await first.client.close();

    const fresh = await sessionFor(state);
    const resumed = await fresh.session.attach(runId, { from: serialized });
    const resumedFirst = await resumed[Symbol.asyncIterator]().next();
    expect(state.requests.at(-1)).toEqual({ cursor: "token-1", runId });
    expect(text(resumedFirst.value)).toBe("two");
    await resumed.close();
    await fresh.client.close();
  });

  it("cursor scope is delivered-set containment over run and filter", async () => {
    const state = harness([
      message(runId, "token-1", "one"),
      message(runId, "token-2", "two"),
      result(runId, "token-3"),
    ]);
    const { client, session } = await sessionFor(state);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();
    const bound = attached.cursor;
    await iterator.return?.();
    const beforeRefusals = state.requests.length;

    await expect(session.activity({ from: bound })).rejects.toBeInstanceOf(CursorScopeError);
    await expect(session.attach("run-other", { from: bound })).rejects.toBeInstanceOf(
      CursorScopeError,
    );
    expect(state.requests).toHaveLength(beforeRefusals);

    const sameRun = await session.attach(runId, { from: bound });
    await sameRun[Symbol.asyncIterator]().next();
    expect(state.requests.at(-1)).toEqual({ cursor: "token-1", runId });
    await sameRun.close();
    const restored = await session.attach(undefined, { from: bound });
    expect(restored.runId).toBe(runId);
    await restored[Symbol.asyncIterator]().next();
    expect(state.requests.at(-1)).toEqual({ cursor: "token-1", runId });
    await restored.close();

    const activity = await session.activity();
    const activityIterator = activity[Symbol.asyncIterator]();
    await activityIterator.next();
    await activityIterator.next();
    const unbound = activity.cursor;
    await activityIterator.return?.();
    const narrowed = await session.attach("run-any", { from: unbound });
    await narrowed[Symbol.asyncIterator]().next();
    expect(state.requests.at(-1)).toEqual({ cursor: "token-1", runId: "run-any" });
    await narrowed.close();

    const filteredWithoutRun = authoredCursor({
      filter: runId,
      run: "",
      token: "token-1",
      v: "sdkcur/1",
    });
    const beforeFilterRefusal = state.requests.length;
    await expect(session.activity({ from: filteredWithoutRun })).rejects.toBeInstanceOf(
      CursorScopeError,
    );
    expect(state.requests).toHaveLength(beforeFilterRefusal);
    await client.close();
  });

  it("cursor acceptance is structural rather than provenance-based", async () => {
    const state = harness([message(runId, "token-1", "one"), result(runId, "token-2")]);
    const { client, session } = await sessionFor(state);
    const malformed = [
      authoredCursor({ filter: runId, run: runId, token: "", v: "sdkcur/2" }),
      "not%base64url",
      authoredCursor({ run: runId, token: "", v: "sdkcur/1" }),
      "server-token-1",
    ];

    for (const cursor of malformed) {
      await expect(session.attach(runId, { from: cursor })).rejects.toBeInstanceOf(
        CursorMalformedError,
      );
    }
    expect(state.requests).toHaveLength(0);

    const handBuilt = authoredCursor({ filter: runId, run: runId, token: "", v: "sdkcur/1" });
    const accepted = await session.attach(runId, { from: handBuilt });
    await accepted[Symbol.asyncIterator]().next();
    expect(state.requests).toEqual([{ cursor: "", runId }]);
    type ExposesTokenMember = "token" extends keyof SdkCursor ? true : false;
    const exposesTokenMember: ExposesTokenMember = false;
    expect(exposesTokenMember).toBe(false);
    await accepted.close();
    await client.close();
  });

  it("an attachment writes no local storage and no files", async () => {
    const state = harness([
      message(runId, "token-1", "one"),
      message(runId, "token-2", "two"),
      result(runId, "token-3"),
    ]);
    const storageWrites: string[] = [];
    const storage = {
      clear: vi.fn(),
      getItem: vi.fn(() => null),
      key: vi.fn(() => null),
      length: 0,
      removeItem: vi.fn(),
      setItem: vi.fn((key: string) => storageWrites.push(key)),
    } satisfies Storage;
    const localDescriptor = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
    const sessionDescriptor = Object.getOwnPropertyDescriptor(globalThis, "sessionStorage");
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
    Object.defineProperty(globalThis, "sessionStorage", { configurable: true, value: storage });
    const fileSpies = [
      vi.spyOn(fs, "appendFileSync"),
      vi.spyOn(fs, "createWriteStream"),
      vi.spyOn(fs, "writeFileSync"),
      vi.spyOn(fs.promises, "appendFile"),
      vi.spyOn(fs.promises, "writeFile"),
    ];

    try {
      const { client, session } = await sessionFor(state);
      const first = await session.attach(runId);
      const iterator = first[Symbol.asyncIterator]();
      await iterator.next();
      await iterator.next();
      const checkpoint = first.cursor;
      await iterator.return?.();
      const reconnected = await session.attach(runId, { from: checkpoint });
      await collect(reconnected);
      await reconnected[Symbol.asyncDispose]();
      await client.close();

      expect(storageWrites).toEqual([]);
      expect(storage.setItem).not.toHaveBeenCalled();
      for (const spy of fileSpies) expect(spy).not.toHaveBeenCalled();
    } finally {
      for (const spy of fileSpies) spy.mockRestore();
      if (localDescriptor === undefined)
        delete (globalThis as { localStorage?: Storage }).localStorage;
      else Object.defineProperty(globalThis, "localStorage", localDescriptor);
      if (sessionDescriptor === undefined) {
        delete (globalThis as { sessionStorage?: Storage }).sessionStorage;
      } else {
        Object.defineProperty(globalThis, "sessionStorage", sessionDescriptor);
      }
    }
  });

  it("an attachment has exactly one consumer", async () => {
    const state = harness([message(runId, "token-1", "one"), result(runId, "token-2")]);
    const { client, session } = await sessionFor(state);
    const attached = await session.attach(runId);
    const first = attached[Symbol.asyncIterator]();

    expect(() => attached[Symbol.asyncIterator]()).toThrow(InvalidStateError);
    expect(() => attached[Symbol.asyncIterator]()).toThrow(
      expect.objectContaining({ code: "invalid_state" }),
    );
    await first.return?.();
    await client.close();
  });

  it("an implicit attach cursor restores its run rather than re-deriving one", async () => {
    const state = harness([message("run-original", "token-1", "original")]);
    const first = await sessionFor(state);
    const attached = await first.session.attach();
    expect(attached.runId).toBe("run-original");
    const iterator = attached[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();
    const serialized = JSON.parse(JSON.stringify(attached.cursor)) as string;
    await iterator.return?.();
    await first.client.close();

    state.records.push(
      result("run-original", "token-2"),
      message("run-newest", "token-3", "newest"),
      result("run-newest", "token-4"),
    );
    const fresh = await sessionFor(state);
    const restored = await fresh.session.attach(undefined, { from: serialized });

    expect(restored.runId).toBe("run-original");
    const replay = await collect(restored);
    expect(state.requests.at(-1)).toEqual({ cursor: "token-1", runId: "" });
    expect(replay).toHaveLength(1);
    expect(replay[0]).toMatchObject({ event: { kind: "result", runId: "run-original" } });
    await fresh.client.close();
  });
});
