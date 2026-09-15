import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  InvalidStateError,
  PermissionAskAlreadyResolvedError,
  type PermissionVerdict,
} from "../src/index.js";

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

function ask(
  runId: string,
  askId: string,
  guardrail?: {
    kind: number;
    repeatAvailable: boolean;
    reviewId: string;
    sessionOnly: boolean;
  },
) {
  return {
    event: {
      ask: { args: `{"ask":"${askId}"}`, askId, guardrail, reason: "test", tool: "Bash" },
      runId,
      type: "permission.ask",
    },
  };
}

function retract(runId: string, askId: string) {
  return { event: { ask: { askId }, runId, type: "permission.retract" } };
}

function terminal(runId: string, stop = "end_turn") {
  return { event: { result: { stop, text: "done" }, runId, type: "result" } };
}

describe("permission asks", () => {
  it("raw asks are always emitted", async () => {
    const callbacks: string[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-raw" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* () {
          yield ask("run-raw", "ask-raw");
          yield terminal("run-raw");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start", {
      onPermissionAsk: (value) => {
        callbacks.push(value.askId);
        return undefined;
      },
    });

    const events = [];
    for await (const event of run) events.push(event);

    expect(callbacks).toEqual(["ask-raw"]);
    expect(events.map((event) => event.kind)).toEqual(["permission.ask", "result"]);
    expect(events[0]).toMatchObject({ payload: { askId: "ask-raw" } });
    await client.close();
  });

  it("first accepted verdict wins per ask", async () => {
    const releases = new Map<string, ReturnType<typeof deferred<PermissionVerdict | undefined>>>();
    const started: string[] = [];
    const controls: Array<{ askId: string; expectedRunId: string; verdict: number }> = [];
    const controlReceived = deferred<void>();
    const releaseTerminal = deferred<void>();
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-first" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield ask("run-first", "ask-winner");
          yield ask("run-first", "ask-other");
          const next = await input.next();
          if (next.value?.kind.case === "resumeApproval") {
            controls.push({
              askId: next.value.kind.value.askId,
              expectedRunId: next.value.kind.value.expectedRunId,
              verdict: next.value.kind.value.verdict,
            });
          }
          controlReceived.resolve();
          await releaseTerminal.promise;
          yield terminal("run-first");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start", {
      onPermissionAsk: (value) => {
        started.push(value.askId);
        const release = deferred<PermissionVerdict | undefined>();
        releases.set(value.askId, release);
        return release.promise;
      },
    });
    const iterator = run[Symbol.asyncIterator]();

    await iterator.next();
    await iterator.next();
    expect(started).toEqual(["ask-winner", "ask-other"]);

    const terminalEvent = iterator.next();
    releases.get("ask-winner")?.resolve("allow_once");
    releases.get("ask-other")?.resolve(undefined);
    await controlReceived.promise;
    await expect(run.resolveAsk("ask-winner", "deny")).rejects.toBeInstanceOf(
      PermissionAskAlreadyResolvedError,
    );
    expect(controls).toEqual([{ askId: "ask-winner", expectedRunId: "run-first", verdict: 2 }]);

    releaseTerminal.resolve();
    await terminalEvent;
    await client.close();
  });

  it("thrown or abstaining callbacks leave the ask pending", async () => {
    const controls: Array<{ askId: string; expectedRunId: string; verdict: number }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-pending" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield ask("run-pending", "ask-thrown");
          yield ask("run-pending", "ask-abstained");
          for (let index = 0; index < 2; index += 1) {
            const next = await input.next();
            if (next.value?.kind.case === "resumeApproval") {
              controls.push({
                askId: next.value.kind.value.askId,
                expectedRunId: next.value.kind.value.expectedRunId,
                verdict: next.value.kind.value.verdict,
              });
            }
          }
          yield terminal("run-pending");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start", {
      onPermissionAsk: (value) => {
        if (value.askId === "ask-thrown") throw new Error("responder failed");
        return undefined;
      },
    });
    const iterator = run[Symbol.asyncIterator]();

    await iterator.next();
    await iterator.next();
    await run.resolveAsk("ask-thrown", "allow_always");
    await run.resolveAsk("ask-abstained", "deny");
    await iterator.next();

    expect(controls).toEqual([
      { askId: "ask-thrown", expectedRunId: "run-pending", verdict: 3 },
      { askId: "ask-abstained", expectedRunId: "run-pending", verdict: 1 },
    ]);
    await client.close();
  });

  it("abort fires on manual resolution and run end", async () => {
    vi.useFakeTimers();
    try {
      const aborts = new Map<string, number>();
      const signals = new Map<string, AbortSignal>();
      const transport = createRouterTransport((router) => {
        router.service(HarnessService, {
          createSession: () => ({ sessionId: "session-abort" }),
          getCompatibilityInfo: () => ({ apiMajor: 1 }),
          converse: async function* (requests) {
            const input = requests[Symbol.asyncIterator]();
            await input.next();
            yield ask("run-abort", "ask-manual");
            yield ask("run-abort", "ask-terminal");
            await input.next();
            yield terminal("run-abort", "cancelled");
          },
        });
      });
      const client = connect({ transport });
      const session = await client.sessions.create({});
      const run = await session.run("start", {
        onPermissionAsk: (value, signal) => {
          signals.set(value.askId, signal);
          signal.addEventListener("abort", () => {
            aborts.set(value.askId, (aborts.get(value.askId) ?? 0) + 1);
          });
          return new Promise<PermissionVerdict>(() => {});
        },
      });
      const iterator = run[Symbol.asyncIterator]();

      await iterator.next();
      await iterator.next();
      await vi.advanceTimersByTimeAsync(24 * 60 * 60 * 1000);
      expect(signals.get("ask-manual")?.aborted).toBe(false);
      expect(signals.get("ask-terminal")?.aborted).toBe(false);
      expect(aborts.size).toBe(0);

      await run.resolveAsk("ask-manual", "deny");
      expect(signals.get("ask-manual")?.aborted).toBe(true);
      expect(signals.get("ask-terminal")?.aborted).toBe(false);
      expect(aborts.get("ask-manual")).toBe(1);

      const ended = await iterator.next();
      expect(ended.value).toMatchObject({ kind: "result", payload: { stop: "cancelled" } });
      expect(signals.get("ask-terminal")?.aborted).toBe(true);
      expect(aborts.get("ask-terminal")).toBe(1);
      await client.close();
    } finally {
      vi.useRealTimers();
    }
  });

  it("retains guardrail scope across a correlated refusal and one corrected retry", async () => {
    const controls: Array<{
      askId: string;
      expectedRunId: string;
      guardrailKind: number;
      reviewId: string;
      verdict: number;
    }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-guardrail" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield ask("run-guardrail", "ask-guardrail", {
            kind: 2,
            repeatAvailable: false,
            reviewId: "review-7",
            sessionOnly: true,
          });
          const first = await input.next();
          if (first.value?.kind.case === "resumeApproval") controls.push(first.value.kind.value);
          yield {
            event: {
              controlRefused: { askId: "ask-guardrail", category: "approval_intent_mismatch" },
              runId: "run-guardrail",
              text: "approval intent does not match pending ask",
              type: "control.refused",
            },
          };
          const second = await input.next();
          if (second.value?.kind.case === "resumeApproval") controls.push(second.value.kind.value);
          yield terminal("run-guardrail");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start");
    const iterator = run[Symbol.asyncIterator]();

    const permission = await iterator.next();
    expect(permission.value).toMatchObject({
      kind: "permission.ask",
      payload: { guardrail: { kind: "result_release", reviewId: "review-7" } },
    });
    await expect(run.resolveAsk("ask-guardrail", "allow_always")).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    await run.resolveAsk("ask-guardrail", "allow_once");
    const refusal = await iterator.next();
    expect(refusal.value).toMatchObject({
      kind: "control.refused",
      payload: { askId: "ask-guardrail", category: "approval_intent_mismatch" },
    });
    await run.resolveAsk("ask-guardrail", "deny");
    await iterator.next();

    expect(controls).toHaveLength(2);
    expect(controls).toMatchObject([
      {
        allow: true,
        askId: "ask-guardrail",
        expectedRunId: "run-guardrail",
        guardrailKind: 2,
        reviewId: "review-7",
        verdict: 2,
      },
      {
        allow: false,
        askId: "ask-guardrail",
        expectedRunId: "run-guardrail",
        guardrailKind: 2,
        reviewId: "review-7",
        verdict: 1,
      },
    ]);
    await client.close();
  });

  it("late verdicts fail typed", async () => {
    const controls: Array<{ askId: string; runId: string }> = [];
    let runNumber = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-late" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          runNumber += 1;
          const runId = `run-late-${runNumber}`;
          if (runNumber === 1) {
            yield ask(runId, "ask-resolved");
            const next = await input.next();
            if (next.value?.kind.case === "resumeApproval") {
              controls.push({ askId: next.value.kind.value.askId, runId });
            }
          } else if (runNumber === 2) {
            yield ask(runId, "ask-retracted");
            yield retract(runId, "ask-retracted");
          } else if (runNumber === 3) {
            yield ask(runId, "ask-prior");
          } else {
            yield ask(runId, "ask-current");
            const next = await input.next();
            if (next.value?.kind.case === "resumeApproval") {
              controls.push({ askId: next.value.kind.value.askId, runId });
            }
          }
          yield terminal(runId);
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    const resolvedRun = await session.run("resolved");
    const resolvedEvents = resolvedRun[Symbol.asyncIterator]();
    await resolvedEvents.next();
    await resolvedRun.resolveAsk("ask-resolved", "allow_once");
    await resolvedEvents.next();
    await expect(resolvedRun.resolveAsk("ask-resolved", "deny")).rejects.toBeInstanceOf(
      PermissionAskAlreadyResolvedError,
    );
    expect(controls).toEqual([{ askId: "ask-resolved", runId: "run-late-1" }]);

    const retractedRun = await session.run("retracted");
    const retractedEvents = retractedRun[Symbol.asyncIterator]();
    await retractedEvents.next();
    await retractedEvents.next();
    await expect(retractedRun.resolveAsk("ask-retracted", "deny")).rejects.toBeInstanceOf(
      PermissionAskAlreadyResolvedError,
    );
    expect(controls).toHaveLength(1);
    await retractedEvents.next();

    const priorRun = await session.run("prior");
    const priorEvents = priorRun[Symbol.asyncIterator]();
    await priorEvents.next();
    await priorEvents.next();

    const currentRun = await session.run("current");
    const currentEvents = currentRun[Symbol.asyncIterator]();
    await currentEvents.next();
    await expect(priorRun.resolveAsk("ask-prior", "allow_always")).rejects.toBeInstanceOf(
      PermissionAskAlreadyResolvedError,
    );
    expect(controls).toHaveLength(1);

    await currentRun.resolveAsk("ask-current", "deny");
    await currentEvents.next();
    expect(controls).toEqual([
      { askId: "ask-resolved", runId: "run-late-1" },
      { askId: "ask-current", runId: "run-late-4" },
    ]);
    await client.close();
  });

  it("rejects unknown approve ids without sending a scope-less fallback", async () => {
    const controls: string[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-unknown" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield { event: { runId: "run-unknown", text: "started", type: "message.delta" } };
          const next = await Promise.race([
            input.next(),
            new Promise<undefined>((resolve) => setTimeout(() => resolve(undefined), 10)),
          ]);
          if (next?.value?.kind.case === "resumeApproval")
            controls.push(next.value.kind.value.askId);
          yield terminal("run-unknown");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start");

    await expect(run.approve("ask-never-seen", true)).rejects.toBeInstanceOf(
      PermissionAskAlreadyResolvedError,
    );
    await run.result();
    expect(controls).toEqual([]);
    await client.close();
  });

  it("restores only the correlated stale approval and retries with the ask event run id", async () => {
    const controls: Array<{ askId: string; expectedRunId: string }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-stale" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield { event: { runId: "run-original", text: "started", type: "message.delta" } };
          yield ask("run-current", "ask-current");
          const first = await input.next();
          if (first.value?.kind.case === "resumeApproval") {
            controls.push({
              askId: first.value.kind.value.askId,
              expectedRunId: first.value.kind.value.expectedRunId,
            });
          }
          yield {
            event: {
              controlRefused: { askId: "ask-other", category: "stale_run_control" },
              runId: "run-current",
              text: "unrelated refusal",
              type: "control.refused",
            },
          };
          yield {
            event: {
              controlRefused: { askId: "ask-current", category: "stale_run_control" },
              runId: "run-current",
              text: "stale approval refused",
              type: "control.refused",
            },
          };
          const retry = await input.next();
          if (retry.value?.kind.case === "resumeApproval") {
            controls.push({
              askId: retry.value.kind.value.askId,
              expectedRunId: retry.value.kind.value.expectedRunId,
            });
          }
          yield terminal("run-original");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start");
    const events = run[Symbol.asyncIterator]();

    await events.next();
    await events.next();
    await run.resolveAsk("ask-current", "allow_once");
    await events.next();
    await expect(run.resolveAsk("ask-current", "deny")).rejects.toBeInstanceOf(
      PermissionAskAlreadyResolvedError,
    );
    await events.next();
    await run.resolveAsk("ask-current", "deny");
    await events.next();
    expect(controls).toEqual([
      { askId: "ask-current", expectedRunId: "run-current" },
      { askId: "ask-current", expectedRunId: "run-current" },
    ]);
    await client.close();
  });
});
