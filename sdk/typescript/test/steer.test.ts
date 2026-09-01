import { describe, expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { createHttpTransport, createRawClient, UnsupportedFeatureError } from "../src/index.js";
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
  });
});
