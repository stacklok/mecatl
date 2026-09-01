import { describe, expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { createRawClient } from "../src/index.js";
import { routerFor, scriptedState } from "./scripted-state.js";

describe("injected router transport", () => {
  it("router transport drives unary and streaming operations offline", async () => {
    const client = createRawClient({ transport: routerFor() });
    const created = await client.unary(HarnessService.method.createSession, {
      workspace: scriptedState.workspace,
    });
    expect(created.sessionId).toBe(scriptedState.sessionId);

    const events = [];
    async function* eventRequests() {
      yield { sessionId: created.sessionId };
    }
    for await (const event of client.stream(
      HarnessService.method.streamSessionEvents,
      eventRequests(),
    )) {
      events.push(event.type);
    }
    expect(events).toEqual(["message.delta", "result"]);

    async function* requests() {
      yield {
        kind: {
          case: "prompt" as const,
          value: { sessionId: created.sessionId, text: "hello" },
        },
      };
    }
    const responses = [];
    for await (const response of client.stream(HarnessService.method.converse, requests())) {
      responses.push(response.event?.text);
    }
    expect(responses).toEqual(["prompt"]);
  });
});
