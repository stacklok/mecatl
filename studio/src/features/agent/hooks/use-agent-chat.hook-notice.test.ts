import { describe, expect, it } from "vitest";
import type { AgentMessage, StreamEvent } from "../types";
import { reduceWatchEvent } from "./use-agent-chat";

/**
 * Hook fires in the durable-watch reducer (ADR 0250): a fire attributed to a
 * tool call becomes that call's chip, a call-less lifecycle fire becomes a
 * marked notice on the trailing assistant bubble, and neither ever changes a
 * call's status — the daemon's own tool result does that, exactly once.
 */
describe("reduceWatchEvent hook notices", () => {
  let serial = 0;
  const nextId = () => `id-${++serial}`;
  const run = (events: StreamEvent[]): AgentMessage[] =>
    events.reduce<AgentMessage[]>(
      (messages, event) => reduceWatchEvent(messages, event, nextId),
      [],
    );

  const toolCall: StreamEvent = {
    type: "tool_call",
    name: "Shell",
    callId: "c1",
    input: "cmd: rm -rf /",
  };
  const hook = (
    partial: Partial<Extract<StreamEvent, { type: "hook" }>> = {},
  ): StreamEvent => ({
    type: "hook",
    phase: "PreToolUse",
    tool: "Shell",
    decision: "blocked",
    callId: "c1",
    text: "rm -rf is not allowed",
    ...partial,
  });

  it("attaches a blocked fire to its call and lets the error result fail the call once", () => {
    const messages = run([
      { type: "user_prompt", text: "clean up" },
      toolCall,
      hook(),
      {
        type: "tool_result",
        callId: "c1",
        output: "rm -rf is not allowed",
        isError: true,
      },
    ]);
    expect(messages).toHaveLength(2);
    expect(messages[1].toolCalls?.[0]).toMatchObject({
      status: "failed",
      isError: true,
      hooks: [{ phase: "PreToolUse", decision: "blocked" }],
    });
    expect(messages[1].notices).toBeUndefined();
  });

  it("keeps a running call running when a modified fire arrives", () => {
    const messages = run([
      toolCall,
      hook({
        decision: "modified",
        text: "PreToolUse hook rewrote tool arguments for Shell",
      }),
    ]);
    expect(messages[0].toolCalls?.[0]).toMatchObject({
      status: "running",
      hooks: [{ decision: "modified" }],
    });
  });

  it("renders a call-less advisory fire as a marked notice on the trailing assistant", () => {
    const messages = run([
      { type: "user_prompt", text: "hello" },
      { type: "token", text: "Hi." },
      hook({
        phase: "UserPromptSubmit",
        tool: "",
        callId: "",
        decision: "advisory",
        text: "prompt mentions a credential",
      }),
    ]);
    expect(messages).toHaveLength(2);
    expect(messages[1].notices).toEqual([
      "[hook:advisory] UserPromptSubmit hook: prompt mentions a credential",
    ]);
  });

  it("opens an assistant bubble for a fire that arrives before any assistant activity", () => {
    const messages = run([
      { type: "user_prompt", text: "hello" },
      hook({ phase: "SessionStart", tool: "", callId: "" }),
    ]);
    expect(messages).toHaveLength(2);
    expect(messages[1].role).toBe("assistant");
    expect(messages[1].notices?.[0]).toMatch(/^\[hook:blocked\] SessionStart/);
  });

  it("lands a PostToolUse fire on the turn that owns the call after a later prompt opened a new bubble", () => {
    const messages = run([
      toolCall,
      { type: "tool_result", callId: "c1", output: "ok" },
      { type: "user_prompt", text: "next" },
      { type: "token", text: "On it." },
      hook({
        phase: "PostToolUse",
        decision: "advisory",
        text: "output mentions a token",
      }),
    ]);
    expect(messages).toHaveLength(3);
    expect(messages[0].toolCalls?.[0].hooks).toEqual([
      {
        phase: "PostToolUse",
        tool: "Shell",
        decision: "advisory",
        text: "output mentions a token",
      },
    ]);
    expect(messages[2].notices).toBeUndefined();
  });
});
