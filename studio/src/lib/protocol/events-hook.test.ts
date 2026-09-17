import type { Event as SdkEvent } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import { translateEvent } from "./events";

/**
 * Builds one SDK-decoded `hook` event: the structured payload (phase, tool,
 * numeric decision, callId) plus the human-readable message on `text`, the
 * way the daemon's EvHook crosses the wire.
 */
function hookEvent(
  payload: { phase: string; tool: string; decision: number; callId: string },
  common: Partial<{ runId: string; text: string }> = {},
): SdkEvent {
  return {
    kind: "hook",
    payload,
    runId: common.runId ?? "",
    seq: BigInt(0),
    text: common.text ?? "",
    turn: 0,
    usage: undefined,
  } as unknown as SdkEvent;
}

const preToolUse = (decision: number, callId = "c1") => ({
  phase: "PreToolUse",
  tool: "Shell",
  decision,
  callId,
});

/**
 * Hook fires used to sit in the translator's silent set and vanished. They
 * now translate to a typed `hook` StreamEvent carrying the labelled decision
 * so a PreToolUse block, an argument rewrite or a guardrail advisory always
 * has a visual surface.
 */
describe("translateEvent — hook", () => {
  it("translates a blocked PreToolUse fire into a typed hook event, never silent", () => {
    expect(
      translateEvent(
        hookEvent(preToolUse(2), { text: "rm -rf is not allowed" }),
        "s1",
      ),
    ).toEqual([
      {
        type: "hook",
        phase: "PreToolUse",
        tool: "Shell",
        decision: "blocked",
        callId: "c1",
        text: "rm -rf is not allowed",
      },
    ]);
  });

  it.each([
    [1, "info"],
    [2, "blocked"],
    [3, "modified"],
    [4, "advisory"],
  ])("labels wire decision %i as %s", (wire, label) => {
    const [event] = translateEvent(hookEvent(preToolUse(wire)), "s1");
    expect(event).toMatchObject({ type: "hook", decision: label });
  });

  it("degrades an unspecified or unknown decision to info — the daemon's own default", () => {
    expect(translateEvent(hookEvent(preToolUse(0)), "s1")[0]).toMatchObject({
      decision: "info",
    });
    expect(translateEvent(hookEvent(preToolUse(9)), "s1")[0]).toMatchObject({
      decision: "info",
    });
  });

  it("keeps a call-less lifecycle hook renderable (empty callId and tool)", () => {
    expect(
      translateEvent(
        hookEvent(
          { phase: "SessionStart", tool: "", decision: 2, callId: "" },
          { text: "session blocked by SessionStart hook" },
        ),
        "s1",
      ),
    ).toEqual([
      {
        type: "hook",
        phase: "SessionStart",
        tool: "",
        decision: "blocked",
        callId: "",
        text: "session blocked by SessionStart hook",
      },
    ]);
  });

  it("still surfaces a fire whose message is empty", () => {
    expect(translateEvent(hookEvent(preToolUse(3)), "s1")).toHaveLength(1);
  });

  it("stamps the run id like every other run-scoped event", () => {
    expect(
      translateEvent(hookEvent(preToolUse(4), { runId: "run-7" }), "s1")[0],
    ).toMatchObject({ runId: "run-7" });
  });
});
