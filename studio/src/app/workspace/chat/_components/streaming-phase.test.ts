import { describe, expect, it } from "vitest";
import type { AgentMessage, ToolCallInfo } from "@/features/agent/types";
import { streamingPhaseLabel } from "./streaming-phase";

const call = (partial: Partial<ToolCallInfo>): ToolCallInfo => ({
  callId: "c1",
  name: "Read",
  input: null,
  status: "running",
  ...partial,
});

const message = (partial: Partial<AgentMessage> = {}): AgentMessage => ({
  id: "m1",
  role: "assistant",
  content: "",
  timestamp: 0,
  ...partial,
});

/**
 * The live activity line names the running tool (the TUI footer's "running
 * Read"), not a generic "Running tools" — the name is what tells the user
 * whether the agent is reading, editing, or shelling out.
 */
describe("streamingPhaseLabel", () => {
  it("is Thinking before any text and Writing once text has streamed", () => {
    expect(streamingPhaseLabel(undefined)).toBe("Thinking");
    expect(streamingPhaseLabel(message())).toBe("Thinking");
    expect(streamingPhaseLabel(message({ content: "hello" }))).toBe("Writing");
  });

  it("is Reasoning once reasoning deltas arrive with no text yet, and Writing once text streams", () => {
    expect(streamingPhaseLabel(message({ reasoning: "weighing it" }))).toBe(
      "Reasoning",
    );
    expect(streamingPhaseLabel(message({ reasoning: "  \n" }))).toBe(
      "Thinking",
    );
    expect(
      streamingPhaseLabel(message({ reasoning: "weighing", content: "so" })),
    ).toBe("Writing");
    // A running tool still wins: its name says what is actually happening.
    expect(
      streamingPhaseLabel(
        message({ reasoning: "weighing", toolCalls: [call({ name: "Grep" })] }),
      ),
    ).toBe("Running Grep");
  });

  it("names the running tool", () => {
    expect(
      streamingPhaseLabel(message({ toolCalls: [call({ name: "Read" })] })),
    ).toBe("Running Read");
  });

  it("ignores finished calls and prefers the running ones even mid-text", () => {
    expect(
      streamingPhaseLabel(
        message({
          content: "partial",
          toolCalls: [
            call({ callId: "c0", name: "Glob", status: "completed" }),
            call({ callId: "c1", name: "Grep" }),
          ],
        }),
      ),
    ).toBe("Running Grep");
  });

  it("lists distinct concurrent tools, capping the spelled-out names", () => {
    expect(
      streamingPhaseLabel(
        message({
          toolCalls: [
            call({ callId: "c1", name: "Read" }),
            call({ callId: "c2", name: "Read" }),
            call({ callId: "c3", name: "Grep" }),
          ],
        }),
      ),
    ).toBe("Running Read, Grep");
    expect(
      streamingPhaseLabel(
        message({
          toolCalls: ["Read", "Grep", "Glob", "ListDir", "Shell"].map(
            (name, index) => call({ callId: `c${index}`, name }),
          ),
        }),
      ),
    ).toBe("Running Read, Grep, Glob +2");
  });

  it("falls back to the generic label for a nameless running call", () => {
    expect(
      streamingPhaseLabel(message({ toolCalls: [call({ name: "" })] })),
    ).toBe("Running tools");
  });

  it("clamps a daemon-influenced tool name to one bounded line", () => {
    expect(
      streamingPhaseLabel(
        message({
          toolCalls: [call({ name: `mcp__evil__${"x".repeat(60)}\nforged` })],
        }),
      ),
    ).toBe(`Running mcp__evil__${"x".repeat(28)}…`);
  });
});
