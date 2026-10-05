// SPDX-License-Identifier: Apache-2.0

import { AsyncLocalStorage } from "node:async_hooks";
import { getEventListeners } from "node:events";
import type { ObserveWriterRequest } from "@mecatl-studio/contracts";
import type {
  Client,
  CreateSessionOptions,
  Event,
  RunOptions,
  Session,
} from "@stacklok-oss/mecatl-sdk";
import { SessionMode } from "@stacklok-oss/mecatl-sdk";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMecatlWriterService, WriterResponseError } from "./writer.js";

const input: ObserveWriterRequest = {
  document: { content: "A hypothesis might fail.", revision: 2 },
  checkpoint: { content: "A hypothesis will succeed.", revision: 1 },
  observations: [{ revision: 1, status: "dismissed", text: "Earlier thought" }],
  discussion: [{ role: "user", text: "Why?" }],
};

function harness(output: string, stop = "end_turn") {
  const deleted = vi.fn(async () => undefined);
  const cancel = vi.fn(async () => undefined);
  const run = vi.fn(async (_prompt: string, _options: RunOptions) => ({
    cancel,
    async *[Symbol.asyncIterator]() {
      yield { kind: "result", payload: { stop, error: "", text: output } } as Event;
    },
  }));
  const create = vi.fn(
    async (_options: CreateSessionOptions) =>
      ({ id: "writer-session", delete: deleted, run }) as unknown as Session,
  );
  const client = { sessions: { create } } as unknown as Client;
  return { service: createMecatlWriterService(client), create, deleted, cancel, run };
}

describe("Writer SDK query boundary", () => {
  afterEach(() => vi.useRealTimers());
  it("returns silent without a session when the document matches its checkpoint", async () => {
    const fake = harness("{}");
    await expect(
      fake.service.observe(
        { ...input, document: { ...input.document, content: "A hypothesis will succeed." } },
        new AbortController().signal,
      ),
    ).resolves.toEqual({ status: "silent" });
    expect(fake.create).not.toHaveBeenCalled();
  });

  it("forwards bounded data through the real SDK query into a no-fs plan session and cleans up", async () => {
    const fake = harness('{"status":"observe","text":"What changed?"}');
    const signal = new AbortController().signal;
    await expect(
      fake.service.observe(
        { ...input, model: { id: "example-model", providerId: "example-provider" } },
        signal,
      ),
    ).resolves.toEqual({
      status: "observe",
      text: "What changed?",
    });
    expect(fake.create).toHaveBeenCalledWith({
      mode: SessionMode.Plan,
      profile: "no-fs",
      limits: { maxTurns: 4, maxToolCalls: 3 },
      modelId: "example-model",
      providerId: "example-provider",
    } satisfies CreateSessionOptions);
    const prompt = fake.run.mock.calls[0]?.[0];
    expect(prompt).toContain("Never modify the document");
    expect(prompt).toContain("During unsolicited observation, do not rewrite sentences");
    expect(prompt).toContain("may suggest wording when the user's message explicitly asks for it");
    expect(prompt).toContain(
      JSON.stringify({
        mode: "observe",
        ...input,
        model: { id: "example-model", providerId: "example-provider" },
      }),
    );
    expect(prompt).not.toContain("Never use tools");
    expect(prompt).toContain("WebSearch");
    expect(prompt).toContain("cite sources");
    const options = fake.run.mock.calls[0]?.[1];
    const ask = { askId: "synthetic-ask", args: "{}", reason: "test", tool: "Read" };
    expect(options?.onPermissionAsk).toBeTypeOf("function");
    expect(await options?.onPermissionAsk?.(ask, signal)).toBe("deny");
    expect(options?.onPlanApproval).toBeTypeOf("function");
    expect(
      await options?.onPlanApproval?.({ ...ask, tool: "PresentPlan" }, signal),
    ).toBeUndefined();
    expect(fake.deleted).toHaveBeenCalledOnce();
    expect(getEventListeners(signal, "abort")).toHaveLength(0);
  });

  it("abandons a PresentPlan ask without starting an approved continuation", async () => {
    const fake = harness("SILENT");
    fake.run.mockResolvedValueOnce({
      cancel: fake.cancel,
      async *[Symbol.asyncIterator]() {
        yield {
          kind: "permission.ask",
          payload: { askId: "synthetic-plan", tool: "PresentPlan", args: "{}", reason: "test" },
        } as Event;
        yield { kind: "result", payload: { stop: "cancelled", error: "", text: "" } } as Event;
      },
    });
    await expect(fake.service.observe(input, new AbortController().signal)).rejects.toBeInstanceOf(
      WriterResponseError,
    );
    expect(fake.run).toHaveBeenCalledOnce();
    expect(fake.cancel).toHaveBeenCalledOnce();
    expect(fake.deleted).toHaveBeenCalledOnce();
  });

  it("accepts SILENT and answers explicit discussion with the current document", async () => {
    const quiet = harness("SILENT");
    await expect(quiet.service.observe(input, new AbortController().signal)).resolves.toEqual({
      status: "silent",
    });
    expect(quiet.create).toHaveBeenCalledWith({
      mode: SessionMode.Plan,
      profile: "no-fs",
      limits: { maxTurns: 4, maxToolCalls: 3 },
    } satisfies CreateSessionOptions);
    const discussion = harness('{"text":"What evidence would change your mind?"}');
    const request = {
      ...input,
      model: { id: "discussion-model", providerId: "discussion-provider" },
      message: "Help me think about this",
    };
    await expect(
      discussion.service.discuss(request, new AbortController().signal),
    ).resolves.toEqual({ text: "What evidence would change your mind?" });
    expect(discussion.run.mock.calls[0]?.[0]).toContain(
      JSON.stringify({ mode: "discuss", ...request }),
    );
    expect(discussion.create).toHaveBeenCalledWith({
      mode: SessionMode.Plan,
      profile: "no-fs",
      limits: { maxTurns: 4, maxToolCalls: 3 },
      modelId: "discussion-model",
      providerId: "discussion-provider",
    } satisfies CreateSessionOptions);
    expect(discussion.deleted).toHaveBeenCalledOnce();
  });

  it("rejects malformed, oversized, and non-success terminal results, still deleting sessions", async () => {
    for (const [output, stop] of [
      ["not JSON", "end_turn"],
      ["x".repeat(4_001), "end_turn"],
      ["SILENT", "timeout"],
    ]) {
      const fake = harness(output ?? "", stop);
      await expect(
        fake.service.observe(input, new AbortController().signal),
      ).rejects.toBeInstanceOf(WriterResponseError);
      expect(fake.deleted).toHaveBeenCalledOnce();
    }
  });

  it("surfaces cleanup errors rather than returning model text", async () => {
    const fake = harness("SILENT");
    fake.deleted.mockRejectedValueOnce(new Error("deletion failed"));
    await expect(fake.service.observe(input, new AbortController().signal)).rejects.toThrow(
      "deletion failed",
    );
  });

  it.each(["disconnect", "timeout"])("keeps caller identity during %s cleanup", async (cause) => {
    vi.useFakeTimers();
    const credentials = new AsyncLocalStorage<string>();
    let release: (() => void) | undefined;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    let started: (() => void) | undefined;
    const running = new Promise<void>((resolve) => {
      started = resolve;
    });
    const identities: { cancel?: string | undefined; delete?: string | undefined } = {};
    const cancel = vi.fn(async () => {
      identities.cancel = credentials.getStore();
      release?.();
    });
    const deleted = vi.fn(async () => {
      identities.delete = credentials.getStore();
    });
    const client = {
      sessions: {
        create: async () => ({
          id: "temporary",
          delete: deleted,
          run: async () => ({
            cancel,
            async *[Symbol.asyncIterator]() {
              started?.();
              await pending;
              yield {
                kind: "result",
                payload: { stop: "cancelled", error: "", text: "" },
              } as Event;
            },
          }),
        }),
      },
    } as unknown as Client;
    const controller = new AbortController();
    const result = credentials.run("synthetic-writer-caller", () =>
      createMecatlWriterService(client).observe(input, controller.signal),
    );
    const rejected = expect(result).rejects.toThrow();
    await running;
    expect(credentials.getStore()).toBeUndefined();
    if (cause === "disconnect") controller.abort();
    else await vi.advanceTimersByTimeAsync(30_000);
    await rejected;
    expect(cancel).toHaveBeenCalledOnce();
    expect(deleted).toHaveBeenCalledOnce();
    expect(identities).toEqual({
      cancel: "synthetic-writer-caller",
      delete: "synthetic-writer-caller",
    });
    expect(getEventListeners(controller.signal, "abort")).toHaveLength(0);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not create a session on an already cancelled request", async () => {
    const fake = harness("SILENT");
    const controller = new AbortController();
    controller.abort();
    await expect(fake.service.observe(input, controller.signal)).rejects.toThrow();
    expect(fake.create).not.toHaveBeenCalled();
  });
});
