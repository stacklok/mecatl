import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  createHttpTransport,
  createRawClient,
  ServerError,
  UnsupportedFeatureError,
} from "../src/index.js";
import { fetchFor, routerFor, scriptedState } from "./scripted-state.js";

async function* steerFrames() {
  yield {
    kind: {
      case: "prompt" as const,
      value: { sessionId: scriptedState.sessionId, text: "start" },
    },
  };
  yield {
    kind: {
      case: "steer" as const,
      value: { messageId: "steer-1", text: "turn left" },
    },
  };
}

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function staleControl(): ConnectError {
  const error = new ConnectError("run is no longer current", Code.Aborted);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([
      ...field(1, bytes("stale_run_control")),
      ...field(2, bytes("mecatl.stacklok.com")),
    ]),
  });
  return error;
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

describe("raw steer", () => {
  it("HTTP steer is a typed unsupported-feature error", async () => {
    const http = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: fetchFor({ ...scriptedState, features: ["server_info"] }),
      }),
    });
    const readHTTP = async () => {
      for await (const _response of http.stream(HarnessService.method.converse, steerFrames())) {
        // Drain so the input-side feature gate is observed.
      }
    };
    await expect(readHTTP()).rejects.toMatchObject({
      code: "unsupported_feature",
      feature: "http_steer",
      transport: "http",
    });

    const grpc = createRawClient({ transport: routerFor() });
    const grpcFrames = [];
    for await (const response of grpc.stream(HarnessService.method.converse, steerFrames())) {
      grpcFrames.push(response.event?.text);
    }
    expect(grpcFrames).toEqual(["prompt", "steer"]);
    expect(new UnsupportedFeatureError("http_steer", { transport: "http" })).toBeInstanceOf(
      UnsupportedFeatureError,
    );

    const encoder = new TextEncoder();
    const ergonomicFetch: typeof globalThis.fetch = async (input, init) => {
      const path = new URL(String(input)).pathname;
      if (path === `/v1/sessions/${scriptedState.sessionId}/prompt`) {
        return new Response(
          new ReadableStream({
            start(controller) {
              controller.enqueue(
                encoder.encode(
                  `data: ${JSON.stringify({
                    run_id: "run-http",
                    text: "started",
                    type: "message.delta",
                  })}\n\n`,
                ),
              );
            },
          }),
          { headers: { "content-type": "text/event-stream" } },
        );
      }
      return fetchFor({ ...scriptedState, features: ["server_info"] })(input, init);
    };
    const client = connect({
      baseUrl: "http://mecatl.test",
      fetch: ergonomicFetch,
    });
    const session = await client.sessions.create({});
    const run = await session.run("start");
    await run.steer("turn left");
    await expect(run.result()).rejects.toMatchObject({
      code: "unsupported_feature",
      feature: "http_steer",
      transport: "http",
    });
    await client.close();
  });

  it("HTTP steer uses the advertised unary control routes", async () => {
    const controls: Array<{ body: Record<string, unknown>; path: string }> = [];
    const encoder = new TextEncoder();
    let closePrompt!: () => void;
    const prompt = new ReadableStream({
      start(controller) {
        controller.enqueue(
          encoder.encode('data: {"run_id":"run-http","text":"started","type":"message.delta"}\n\n'),
        );
        closePrompt = () => controller.close();
      },
    });
    const fallback = fetchFor({ ...scriptedState, features: ["http_steer", "server_info"] });
    const fetch: typeof globalThis.fetch = async (input, init) => {
      const path = new URL(String(input)).pathname;
      if (path === `/v1/sessions/${scriptedState.sessionId}/prompt`) {
        return new Response(prompt, { headers: { "content-type": "text/event-stream" } });
      }
      if (
        path === `/v1/sessions/${scriptedState.sessionId}/controls/steer` ||
        path === `/v1/sessions/${scriptedState.sessionId}/controls/cancel-steer`
      ) {
        controls.push({
          body: JSON.parse(String(init?.body)) as Record<string, unknown>,
          path,
        });
        if (path.endsWith("/cancel-steer")) closePrompt();
        return Response.json({ outcome: path.endsWith("/steer") ? "accepted" : "retracted" });
      }
      return fallback(input, init);
    };
    const http = createRawClient({
      transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch }),
    });
    async function* frames() {
      yield {
        kind: {
          case: "prompt" as const,
          value: { sessionId: scriptedState.sessionId, text: "start" },
        },
      };
      yield {
        kind: {
          case: "steer" as const,
          value: {
            expectedRunId: "run-http",
            messageId: "steer-http",
            text: "turn left",
          },
        },
      };
      yield {
        kind: {
          case: "steerCancel" as const,
          value: { expectedRunId: "run-http", messageId: "cancel-http" },
        },
      };
    }
    for await (const _response of http.stream(HarnessService.method.converse, frames())) {
      // Drain until cancel-steer closes the scripted prompt stream.
    }
    expect(controls).toEqual([
      {
        body: {
          expected_run_id: "run-http",
          message_id: "steer-http",
          parts: [],
          text: "turn left",
        },
        path: `/v1/sessions/${scriptedState.sessionId}/controls/steer`,
      },
      {
        body: { expected_run_id: "run-http", message_id: "cancel-http" },
        path: `/v1/sessions/${scriptedState.sessionId}/controls/cancel-steer`,
      },
    ]);
  });

  it("run.steer never promotes; the raw seam may", async () => {
    const finishNewer = deferred();
    let runNumber = 0;
    let newerEvents = 0;
    const strictTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-strict" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: () => ({ session: { sessionId: "session-strict" } }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          runNumber += 1;
          if (runNumber === 1) {
            yield { event: { runId: "run-old", text: "old", type: "message.delta" } };
            const steer = await input.next();
            expect(steer.value?.kind.case).toBe("steer");
            if (steer.value?.kind.case === "steer") {
              expect(steer.value.kind.value.expectedRunId).toBe("run-old");
            }
            throw staleControl();
          }
          newerEvents += 1;
          yield { event: { runId: "run-new", text: "new", type: "message.delta" } };
          await finishNewer.promise;
          newerEvents += 1;
          yield { event: { runId: "run-new", text: "flowing", type: "message.delta" } };
          yield {
            event: {
              result: { stop: "end_turn", text: "new done" },
              runId: "run-new",
              type: "result",
            },
          };
        },
      });
    });
    const client = connect({ transport: strictTransport });
    const oldSession = await client.sessions.create({});
    const newerSession = await client.sessions.get("session-strict");
    const oldRun = await oldSession.run("old");
    const newerRun = await newerSession.run("new");
    await oldRun.steer("late instruction");

    const refused = await oldRun.result().catch((error) => error);
    expect(refused).toBeInstanceOf(ServerError);
    expect(refused).toMatchObject({ code: "stale_run_control" });
    expect(newerEvents).toBe(1);
    finishNewer.resolve();
    await expect(newerRun.result()).resolves.toMatchObject({
      runId: "run-new",
      text: "new done",
    });
    expect(newerEvents).toBe(2);
    await client.close();

    let omittedExpectedRunId: string | undefined;
    const promotionTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield { event: { runId: "run-old", text: "old", type: "message.delta" } };
          const steer = await input.next();
          if (steer.value?.kind.case === "steer") {
            omittedExpectedRunId = steer.value.kind.value.expectedRunId;
          }
          yield { event: { runId: "run-promoted", text: "promoted", type: "message.delta" } };
          yield {
            event: {
              result: { stop: "end_turn", text: "promoted done" },
              runId: "run-promoted",
              type: "result",
            },
          };
        },
      });
    });
    const raw = createRawClient({ transport: promotionTransport });
    async function* rawFrames() {
      yield {
        kind: {
          case: "prompt" as const,
          value: { sessionId: "session-raw", text: "old" },
        },
      };
      yield {
        kind: {
          case: "steer" as const,
          value: { messageId: "raw-steer", text: "promote me" },
        },
      };
    }
    const rawRunIds: string[] = [];
    for await (const response of raw.stream(HarnessService.method.converse, rawFrames())) {
      if (response.event !== undefined) rawRunIds.push(response.event.runId);
    }
    expect(omittedExpectedRunId).toBe("");
    expect(rawRunIds).toEqual(["run-old", "run-promoted", "run-promoted"]);
  });
});
