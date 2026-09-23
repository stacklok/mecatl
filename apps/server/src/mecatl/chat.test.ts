// SPDX-License-Identifier: Apache-2.0

import { type Client, type Event, MecatlError, SessionMode } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";
import { createMecatlChatService, runError, serializeEvent } from "./chat";

describe("Mecatl chat event serialization", () => {
  it("converts SDK bigint fields into JSON-safe strings", () => {
    const event = {
      kind: "turn.end",
      payload: { durationMs: 250n, usage: undefined },
      runId: "run-1",
      seq: 7n,
      text: "",
      turn: 2,
      usage: {
        cacheReadTokens: 1n,
        cacheWriteTokens: 2n,
        inputTokens: 3n,
        outputTokens: 4n,
        reasoningTokens: 5n,
      },
    } as Event;

    expect(serializeEvent(event)).toMatchObject({
      payload: { durationMs: "250" },
      seq: "7",
      usage: { inputTokens: "3", outputTokens: "4" },
    });
  });
});

describe("Mecatl chat sessions", () => {
  it("maps product session controls to published SDK options", async () => {
    const create = vi.fn().mockResolvedValue({ id: "session-1" });
    const service = createMecatlChatService({ sessions: { create } } as unknown as Client);

    await service.createSession({
      mode: "plan",
      model: { id: "claude-sonnet", providerId: "anthropic" },
      reasoningEffort: "high",
      toolAccess: "noFilesystem",
    });

    expect(create).toHaveBeenCalledWith({
      mode: 2,
      modelId: "claude-sonnet",
      profile: "no-fs",
      providerId: "anthropic",
      reasoningEffort: "high",
    });
  });

  it("forces the no-fs profile and forwards debugTargetSessionId for an AI-debug session", async () => {
    const create = vi.fn().mockResolvedValue({ id: "session-debug" });
    const service = createMecatlChatService({ sessions: { create } } as unknown as Client);

    await service.createSession({
      debugTargetSessionId: "session-1",
      mode: "default",
      reasoningEffort: "default",
      toolAccess: "all",
    });

    expect(create).toHaveBeenCalledWith({
      debugTargetSessionId: "session-1",
      mode: 1,
      profile: "no-fs",
    });
  });

  it("exposes a debug session's bound target on the list row, empty for an ordinary chat", async () => {
    const list = vi.fn().mockResolvedValue({
      nextCursor: "",
      sessions: [
        {
          capabilities: { delete: true, rename: true },
          createdAtUnix: 0n,
          modifiedAtUnix: 0n,
          relationship: { debugTargetSessionId: "session-1" },
          sessionId: "session-debug",
          title: "Debug chat",
          turns: 0,
        },
        {
          capabilities: { delete: true, rename: true },
          createdAtUnix: 0n,
          modifiedAtUnix: 0n,
          sessionId: "session-1",
          title: "Ordinary chat",
          turns: 0,
        },
      ],
    });
    const service = createMecatlChatService({ sessions: { list } } as unknown as Client);

    const response = await service.listSessions();

    expect(response.items.map((item) => item.debugTargetSessionId)).toEqual(["session-1", ""]);
  });

  it("maps snapshot configuration, capabilities, and cumulative token usage", async () => {
    const snapshot = vi.fn().mockResolvedValue({
      capabilities: { manualCompaction: true, modelSelection: true },
      resolvedModel: {
        contextWindow: 200_000n,
        modelId: "claude-sonnet",
        providerId: "anthropic",
        reasoningEffort: "high",
      },
      sessionCapabilities: { image: true },
      sessionId: "session-1",
      state: "idle",
      mode: SessionMode.Plan,
      tokenUsage: {
        main: {
          models: {},
          total: {
            cacheReadTokens: 3n,
            cacheWriteTokens: 4n,
            inputTokens: 100n,
            outputTokens: 20n,
            reasoningTokens: 5n,
          },
        },
        session_title: {
          models: {
            fallback: {
              cacheReadTokens: 7n,
              cacheWriteTokens: 8n,
              inputTokens: 50n,
              outputTokens: 10n,
              reasoningTokens: 9n,
            },
          },
        },
      },
    });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ snapshot }) },
    } as unknown as Client);

    await expect(service.detail("session-1")).resolves.toEqual({
      capabilities: { image: true, manualCompaction: true, modelSelection: true },
      id: "session-1",
      mode: "plan",
      model: {
        contextWindow: "200000",
        id: "claude-sonnet",
        providerId: "anthropic",
        reasoningEffort: "high",
      },
      state: "idle",
      usage: {
        cacheReadTokens: "3",
        cacheWriteTokens: "4",
        inputTokens: "100",
        outputTokens: "20",
        reasoningTokens: "5",
      },
    });
  });

  it("sends text and inline images as a structured SDK prompt", async () => {
    const run = vi.fn().mockResolvedValue({
      id: "run-1",
      async *[Symbol.asyncIterator]() {},
    });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ run }) },
    } as unknown as Client);

    const deliveries = [];
    for await (const delivery of service.run(
      "session-1",
      {
        images: [{ data: "aGVsbG8=", mimeType: "image/png", name: "diagram.png" }],
        prompt: "Describe this image",
      },
      new AbortController().signal,
    )) {
      deliveries.push(delivery);
    }

    expect(run).toHaveBeenCalledWith(
      [
        { kind: "text", text: "Describe this image" },
        {
          bytes: new Uint8Array([104, 101, 108, 108, 111]),
          kind: "image",
          mimeType: "image/png",
        },
      ],
      {},
      { signal: expect.any(AbortSignal) },
    );
    expect(deliveries).toEqual([{ runId: "run-1", sessionId: "session-1", type: "run.started" }]);
  });

  it("supports an image-only structured prompt", async () => {
    const run = vi.fn().mockResolvedValue({
      id: "run-1",
      async *[Symbol.asyncIterator]() {},
    });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ run }) },
    } as unknown as Client);

    for await (const _delivery of service.run(
      "session-1",
      {
        images: [{ data: "aGVsbG8=", mimeType: "image/jpeg", name: "photo.jpg" }],
        prompt: "",
      },
      new AbortController().signal,
    )) {
      // Drain the run stream.
    }

    expect(run).toHaveBeenCalledWith(
      [expect.objectContaining({ kind: "image", mimeType: "image/jpeg" })],
      {},
      { signal: expect.any(AbortSignal) },
    );
  });

  it("projects persisted transcript image data and URLs", async () => {
    const transcript = vi.fn().mockResolvedValue({
      complete: true,
      messages: [
        {
          parts: [
            {
              data: new Uint8Array([104, 101, 108, 108, 111]),
              kind: 1,
              mimeType: "image/png",
              url: "",
            },
            {
              data: new Uint8Array(),
              kind: 1,
              mimeType: "image/webp",
              url: "https://example.com/image.webp",
            },
            { data: new Uint8Array(), kind: 0, mimeType: "text/plain", url: "" },
          ],
          role: "user",
          text: "Compare these",
          toolCalls: [],
        },
      ],
      sessionId: "session-1",
    });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ transcript }) },
    } as unknown as Client);

    await expect(service.transcript("session-1")).resolves.toEqual({
      complete: true,
      messages: [
        {
          images: [
            { data: "aGVsbG8=", mimeType: "image/png", name: "Image 1" },
            {
              mimeType: "image/webp",
              name: "Image 2",
              url: "https://example.com/image.webp",
            },
          ],
          role: "user",
          text: "Compare these",
          toolCalls: [],
        },
      ],
      sessionId: "session-1",
    });
  });

  it("round-trips the extra-high and max reasoning-effort tiers", async () => {
    for (const reasoningEffort of ["xhigh", "max"] as const) {
      const snapshot = vi.fn().mockResolvedValue({
        capabilities: {},
        resolvedModel: {
          contextWindow: 200_000n,
          modelId: "claude-sonnet",
          providerId: "anthropic",
          reasoningEffort,
        },
        sessionId: "session-1",
        state: "idle",
        mode: SessionMode.Default,
        tokenUsage: {},
      });
      const service = createMecatlChatService({
        sessions: { get: vi.fn().mockResolvedValue({ snapshot }) },
      } as unknown as Client);

      await expect(service.detail("session-1")).resolves.toMatchObject({
        model: { reasoningEffort },
      });
    }
  });

  it("uses published SDK operations for mode, compaction, clearing, and model forks", async () => {
    const compact = vi.fn().mockResolvedValue(true);
    const setMode = vi.fn().mockResolvedValue({ mode: SessionMode.AcceptEdits });
    const clear = vi.fn().mockResolvedValue({ id: "session-clear" });
    const fork = vi.fn().mockResolvedValue({ id: "session-fork" });
    const service = createMecatlChatService({
      sessions: {
        fork,
        get: vi.fn().mockResolvedValue({ clear, compact, setMode }),
      },
    } as unknown as Client);

    await expect(service.setMode("session-1", "acceptEdits")).resolves.toEqual({
      mode: "acceptEdits",
    });
    await expect(service.compactSession("session-1")).resolves.toEqual({ compacted: true });
    await expect(service.clearSession("session-1")).resolves.toEqual({ id: "session-clear" });
    await expect(
      service.forkSession("session-1", {
        model: { id: "claude-sonnet", providerId: "anthropic" },
        reasoningEffort: "high",
      }),
    ).resolves.toEqual({ id: "session-fork" });

    expect(setMode).toHaveBeenCalledWith(SessionMode.AcceptEdits);
    expect(compact).toHaveBeenCalledOnce();
    expect(clear).toHaveBeenCalledOnce();
    expect(fork).toHaveBeenCalledWith("session-1", {
      modelId: "claude-sonnet",
      providerId: "anthropic",
      reasoningEffort: "high",
    });
  });

  it("cancels a reattached run through its exact durable run handle", async () => {
    const cancel = vi.fn().mockResolvedValue(undefined);
    const controls = vi.fn().mockReturnValue({ cancel });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ controls }) },
    } as unknown as Client);

    await expect(service.cancelRun("session-1", "run-1")).resolves.toBe(true);
    expect(controls).toHaveBeenCalledWith("run-1");
    expect(cancel).toHaveBeenCalledOnce();
  });

  it("steers a reattached run through its exact durable run handle", async () => {
    const steer = vi.fn().mockResolvedValue({ outcome: "accepted" });
    const controls = vi.fn().mockReturnValue({ steer });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ controls }) },
    } as unknown as Client);

    await expect(service.steer("session-1", "run-1", "focus on the auth module")).resolves.toBe(
      true,
    );
    expect(controls).toHaveBeenCalledWith("run-1");
    expect(steer).toHaveBeenCalledWith("focus on the auth module");
  });

  it("resolves a permission on a reattached run through its exact durable run handle", async () => {
    const resolveAsk = vi.fn().mockResolvedValue(undefined);
    const controls = vi.fn().mockReturnValue({ resolveAsk });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ controls }) },
    } as unknown as Client);

    await expect(
      service.resolvePermission("session-1", "run-1", "ask-1", "allow_once"),
    ).resolves.toBe(true);
    expect(controls).toHaveBeenCalledWith("run-1");
    expect(resolveAsk).toHaveBeenCalledWith("ask-1", "allow_once");
  });

  it("reports no active run to steer once the run has ended", async () => {
    const steer = vi.fn().mockRejectedValue(
      new MecatlError("run ended", {
        code: "stale_run_control",
        status: 409,
        transport: "http",
      }),
    );
    const service = createMecatlChatService({
      sessions: {
        get: vi.fn().mockResolvedValue({ controls: vi.fn().mockReturnValue({ steer }) }),
      },
    } as unknown as Client);

    await expect(service.steer("session-1", "run-1", "keep going")).resolves.toBe(false);
  });

  it("replays durable activity and stops when the current run is terminal", async () => {
    const close = vi.fn().mockResolvedValue(undefined);
    const event = {
      kind: "result",
      payload: {
        error: "",
        permanent: false,
        stop: "end_turn",
        text: "done",
      },
      runId: "run-1",
      seq: 1n,
      text: "done",
      turn: 1,
      usage: undefined,
    } as Event;
    const activity = {
      close,
      async *[Symbol.asyncIterator]() {
        yield {
          cursor: "zero",
          event: {
            kind: "message.delta",
            payload: undefined,
            runId: "run-1",
            seq: 0n,
            text: "done",
            turn: 1,
            usage: undefined,
          } as Event,
          kind: "unknown" as const,
          phase: "future",
        };
        yield { cursor: "one", event, kind: "event" as const, phase: "replay" as const };
        yield { cursor: "two", kind: "boundary" as const, phase: "live" as const };
      },
    };
    const service = createMecatlChatService({
      sessions: {
        get: vi.fn().mockResolvedValue({ activity: vi.fn().mockResolvedValue(activity) }),
      },
    } as unknown as Client);

    const deliveries = [];
    for await (const { delivery } of service.activity("session-1", {
      replayMax: 100,
      signal: new AbortController().signal,
    })) {
      deliveries.push(delivery);
    }

    expect(deliveries).toMatchObject([
      { runId: "run-1", sessionId: "session-1", type: "run.started" },
      { event: { kind: "message.delta", runId: "run-1" }, type: "run.event" },
      { event: { kind: "result", runId: "run-1" }, type: "run.event" },
    ]);
    expect(close).toHaveBeenCalledOnce();
  });
  it("filters inspect-only sessions and reports an incomplete inventory on a cursor loop", async () => {
    const row = (sessionId: string, publicChat = "") => ({
      capabilities: { delete: true, rename: true, reasons: { delete: "", publicChat, rename: "" } },
      createdAtUnix: 1_700_000_000n,
      modelId: "m",
      modifiedAtUnix: 1_700_000_100n,
      sessionId,
      state: "idle",
      title: "",
      turns: 1,
    });
    const list = vi
      .fn()
      .mockResolvedValueOnce({
        nextCursor: "page-2",
        sessions: [row("visible"), row("inspect", "inspect_only_kind"), row("")],
      })
      .mockResolvedValueOnce({ nextCursor: "page-2", sessions: [row("second")] });
    const service = createMecatlChatService({ sessions: { list } } as unknown as Client);

    const response = await service.listSessions();

    expect(response.items.map((item) => item.id)).toEqual(["visible", "second"]);
    expect(response.items[0]?.title).toBe("Untitled chat");
    expect(response.items[0]?.createdAt).toBe("2023-11-14T22:13:20.000Z");
    expect(response.complete).toBe(false);
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("preserves unknown SDK event kinds as unknown run events with their raw payload", () => {
    const event = {
      kind: "unknown",
      rawData: { bytes: new Uint8Array([1, 2]), nested: { count: 9n }, note: "hello" },
      runId: "run-1",
      seq: 3n,
      text: "",
      turn: 1,
      wireKind: "future.kind",
    } as unknown as Event;

    expect(serializeEvent(event)).toEqual({
      kind: "future.kind",
      raw: { bytes: { data: "AQI=", encoding: "base64" }, nested: { count: "9" }, note: "hello" },
      runId: "run-1",
      seq: "3",
      text: "",
      turn: 1,
      unknown: true,
    });
  });
});

describe("Mecatl chat bounded activity replay", () => {
  /** A durable stream of `count` replay events, then the live boundary. */
  function replayStream(count: number, close = vi.fn().mockResolvedValue(undefined)) {
    let pulled = 0;
    return {
      close,
      /** Envelopes the consumer has asked the daemon for so far. */
      pulls: () => pulled,
      async *[Symbol.asyncIterator]() {
        for (let index = 0; index < count; index += 1) {
          pulled += 1;
          yield {
            cursor: `c${index}`,
            event: {
              kind: "message.delta",
              payload: undefined,
              runId: "run-1",
              seq: BigInt(index),
              text: "x",
              turn: 1,
              usage: undefined,
            } as Event,
            kind: "event" as const,
            phase: "replay" as const,
          };
        }
        yield { cursor: "boundary", kind: "boundary" as const, phase: "live" as const };
      },
    };
  }

  function serviceOver(stream: unknown, attach = vi.fn().mockResolvedValue(stream)) {
    return {
      attach,
      service: createMecatlChatService({
        sessions: { get: vi.fn().mockResolvedValue({ activity: attach }) },
      } as unknown as Client),
    };
  }

  it("bounds a no-cursor replay and reports truncation with the last cursor", async () => {
    const close = vi.fn().mockResolvedValue(undefined);
    const stream = replayStream(50, close);
    const { attach, service } = serviceOver(stream);

    const deliveries = [];
    for await (const item of service.activity("session-1", {
      replayMax: 3,
      signal: new AbortController().signal,
    })) {
      deliveries.push(item);
    }

    // `run.started`, exactly three replay events, then the truncation frame.
    expect(deliveries.map((item) => item.delivery?.type)).toEqual([
      "run.started",
      "run.event",
      "run.event",
      "run.event",
      "run.truncated",
    ]);
    expect(
      deliveries.filter((item) => item.cursor !== undefined).map((item) => item.cursor),
    ).toEqual(["c0", "c1", "c2"]);
    expect(deliveries.at(-1)?.delivery).toEqual({
      cursor: "c2",
      reason: "bound",
      type: "run.truncated",
    });
    // The truncation frame itself carries no SSE id: it is not a durable position.
    expect(deliveries.at(-1)?.cursor).toBeUndefined();
    expect(attach).toHaveBeenCalledWith(expect.objectContaining({ from: "start" }));
    expect(close).toHaveBeenCalledOnce();
    // No history is read past the bound: exactly three envelopes were pulled.
    expect(stream.pulls()).toBe(3);
  });

  it("resumes exactly after a cursor without duplicating events", async () => {
    // The daemon resumes after `c2`, so the stream starts at `c3`.
    const resumed = {
      close: vi.fn().mockResolvedValue(undefined),
      async *[Symbol.asyncIterator]() {
        for (const index of [3, 4]) {
          yield {
            cursor: `c${index}`,
            event: {
              kind: "message.delta",
              payload: undefined,
              runId: "run-1",
              seq: BigInt(index),
              text: "x",
              turn: 1,
              usage: undefined,
            } as Event,
            kind: "event" as const,
            phase: "replay" as const,
          };
        }
        yield { cursor: "boundary", kind: "boundary" as const, phase: "live" as const };
      },
    };
    const { attach, service } = serviceOver(resumed);

    const deliveries = [];
    for await (const item of service.activity("session-1", {
      cursor: "c2",
      replayMax: 100,
      signal: new AbortController().signal,
    })) {
      deliveries.push(item);
    }

    expect(attach).toHaveBeenCalledWith(expect.objectContaining({ from: "c2" }));
    // The first frame is the next durable event itself; no run.started
    // precedes it, because the client already follows that run.
    const frames = deliveries.filter((item) => item.delivery !== undefined);
    expect(frames[0]?.delivery?.type).toBe("run.event");
    expect(frames.map((item) => item.cursor)).toEqual(["c3", "c4"]);
    expect(deliveries.filter((item) => item.delivery?.type === "run.event")).toHaveLength(2);
    expect(deliveries.map((item) => item.cursor)).not.toContain("c2");
    expect(deliveries.some((item) => item.delivery?.type === "run.truncated")).toBe(false);
  });

  it("a resumed stream still announces a later run with run.started", async () => {
    const event = (index: number, runId: string) => ({
      cursor: `c${index}`,
      event: {
        kind: "message.delta",
        payload: undefined,
        runId,
        seq: BigInt(index),
        text: "x",
        turn: 1,
        usage: undefined,
      } as Event,
      kind: "event" as const,
      phase: "replay" as const,
    });
    const resumed = {
      close: vi.fn().mockResolvedValue(undefined),
      async *[Symbol.asyncIterator]() {
        yield event(3, "run-1");
        yield event(4, "run-2");
        yield { cursor: "boundary", kind: "boundary" as const, phase: "live" as const };
      },
    };
    const { service } = serviceOver(resumed);
    const types = [];
    for await (const item of service.activity("session-1", {
      cursor: "c2",
      replayMax: 100,
      signal: new AbortController().signal,
    })) {
      if (item.delivery !== undefined) types.push(item.delivery.type);
    }
    expect(types).toEqual(["run.event", "run.started", "run.event"]);
  });

  it("stops at a durable gap instead of resuming from a hole", async () => {
    const gapped = {
      close: vi.fn().mockResolvedValue(undefined),
      async *[Symbol.asyncIterator]() {
        yield {
          cursor: "c0",
          event: {
            kind: "message.delta",
            payload: undefined,
            runId: "run-1",
            seq: 0n,
            text: "x",
            turn: 1,
            usage: undefined,
          } as Event,
          kind: "event" as const,
          phase: "replay" as const,
        };
        yield { kind: "gap" as const, phase: "gap" as const };
        yield {
          cursor: "c9",
          event: {
            kind: "message.delta",
            payload: undefined,
            runId: "run-1",
            seq: 9n,
            text: "later",
            turn: 1,
            usage: undefined,
          } as Event,
          kind: "event" as const,
          phase: "replay" as const,
        };
      },
    };
    const { service } = serviceOver(gapped);

    const deliveries = [];
    for await (const item of service.activity("session-1", {
      replayMax: 100,
      signal: new AbortController().signal,
    })) {
      deliveries.push(item);
    }

    expect(deliveries.at(-1)?.delivery).toEqual({
      cursor: "",
      reason: "gap",
      type: "run.truncated",
    });
    // Nothing past the hole is delivered.
    expect(deliveries.map((item) => item.cursor)).not.toContain("c9");
    expect(deliveries).toHaveLength(3);
  });

  it("live events are never bounded by the replay cap", async () => {
    const live = {
      close: vi.fn().mockResolvedValue(undefined),
      async *[Symbol.asyncIterator]() {
        yield { cursor: "boundary", kind: "boundary" as const, phase: "live" as const };
        for (let index = 0; index < 20; index += 1) {
          yield {
            cursor: `live${index}`,
            event: {
              kind: "message.delta",
              payload: undefined,
              runId: "run-1",
              seq: BigInt(index),
              text: "x",
              turn: 1,
              usage: undefined,
            } as Event,
            kind: "event" as const,
            phase: "live" as const,
          };
        }
      },
    };
    const { service } = serviceOver(live);

    const deliveries = [];
    for await (const item of service.activity("session-1", {
      replayMax: 2,
      signal: new AbortController().signal,
    })) {
      deliveries.push(item);
    }

    expect(deliveries.filter((item) => item.delivery?.type === "run.event")).toHaveLength(20);
    expect(deliveries.some((item) => item.delivery?.type === "run.truncated")).toBe(false);
  });

  it("marks the replay-to-live boundary so a stream can attach before any live event", async () => {
    const idle = {
      close: vi.fn().mockResolvedValue(undefined),
      async *[Symbol.asyncIterator]() {
        yield { cursor: "boundary", kind: "boundary" as const, phase: "live" as const };
      },
    };
    const { service } = serviceOver(idle);
    const iterator = service
      .activity("session-1", { replayMax: 10, signal: new AbortController().signal })
      [Symbol.asyncIterator]();
    // An idle session has nothing to replay: the first item is the marker,
    // with no frame and no cursor, not a wait for a live event.
    await expect(iterator.next()).resolves.toEqual({ done: false, value: {} });
    await expect(iterator.next()).resolves.toMatchObject({ done: true });
  });

  it("closes at the live boundary when no run was seen and the session has none active", async () => {
    const boundaryOnly = () => ({
      close: vi.fn().mockResolvedValue(undefined),
      async *[Symbol.asyncIterator]() {
        yield { cursor: "boundary", kind: "boundary" as const, phase: "live" as const };
        // A live event that only an active session would ever produce.
        yield {
          cursor: "live-1",
          event: { kind: "message.delta", runId: "run-2", seq: 1n, text: "x", turn: 1 } as Event,
          kind: "event" as const,
          phase: "live" as const,
        };
      },
    });
    const over = (state: string) =>
      createMecatlChatService({
        sessions: {
          get: vi.fn().mockResolvedValue({
            activity: vi.fn().mockResolvedValue(boundaryOnly()),
            snapshot: vi.fn().mockResolvedValue({ state }),
          }),
        },
      } as unknown as Client);
    const collect = async (state: string) => {
      const items = [];
      for await (const item of over(state).activity("session-1", {
        cursor: "c9",
        replayMax: 10,
        signal: new AbortController().signal,
      })) {
        items.push(item);
      }
      return items;
    };
    // Resumed from the last event of a finished run: nothing to follow.
    await expect(collect("completed")).resolves.toEqual([]);
    await expect(collect("idle")).resolves.toEqual([]);
    // A live session keeps the stream open for its events.
    const live = await collect("running");
    expect(live[0]).toEqual({});
    expect(live.some((item) => item.cursor === "live-1")).toBe(true);
  });

  it("reports an activity_gap error from the daemon as a gap truncation", async () => {
    const failing = {
      close: vi.fn().mockResolvedValue(undefined),
      // biome-ignore lint/correctness/useYield: the daemon fails before yielding
      async *[Symbol.asyncIterator]() {
        throw new MecatlError("history is not contiguous", {
          code: "activity_gap",
          status: 9,
          transport: "grpc",
        });
      },
    };
    const { service } = serviceOver(failing);
    const deliveries = [];
    for await (const item of service.activity("session-1", {
      cursor: "c2",
      replayMax: 10,
      signal: new AbortController().signal,
    })) {
      deliveries.push(item);
    }
    expect(deliveries).toEqual([
      { delivery: { cursor: "", reason: "gap", type: "run.truncated" } },
    ]);
    expect(failing.close).toHaveBeenCalled();
  });
});

describe("run.error frames", () => {
  it("bound the message and redact upstream addresses and bearer material", () => {
    const frame = runError(
      new MecatlError(
        `dial https://mecated.internal:50051 failed with Bearer eyJhbGciOi.secret.sig ${"x".repeat(600)}`,
        { code: "transport", transport: "grpc" },
      ),
    );
    expect(frame.code).toBe("transport");
    expect(frame.message.length).toBeLessThanOrEqual(400);
    expect(frame.message).not.toContain("mecated.internal");
    expect(frame.message).not.toContain("eyJhbGciOi");
    expect(runError(new Error("")).message).toBe("Mecatl could not complete the run.");
  });
});
