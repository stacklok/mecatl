import { describe, expect, it } from "vitest";
import type { AgentMessage } from "../types";
import { stampTrailingAssistantStop } from "./stop-stamp";

/**
 * A terminal the tab never received from the daemon (a client-side cancel,
 * a rehydrated `cancelled` session) still lands on the transcript: the
 * trailing assistant turn is stamped, a turn with nothing recorded gets an
 * empty bubble so the stop is visible, and a turn that already names its
 * terminal is left alone.
 */
describe("stampTrailingAssistantStop", () => {
  const nextId = () => "stamped";
  const user = (id: string): AgentMessage => ({
    id,
    role: "user",
    content: "go",
    timestamp: 0,
  });
  const assistant = (partial: Partial<AgentMessage> = {}): AgentMessage => ({
    id: "a1",
    role: "assistant",
    content: "partial answer",
    timestamp: 0,
    ...partial,
  });

  it("stamps the trailing assistant turn", () => {
    const messages = [user("u1"), assistant()];
    const stamped = stampTrailingAssistantStop(messages, "cancelled", nextId);
    expect(stamped).toHaveLength(2);
    expect(stamped[1]).toMatchObject({
      id: "a1",
      content: "partial answer",
      stopReason: "cancelled",
    });
    // The other turns are untouched (same references).
    expect(stamped[0]).toBe(messages[0]);
  });

  it("opens an empty assistant bubble when the run was cut before any assistant record", () => {
    const stamped = stampTrailingAssistantStop(
      [user("u1")],
      "cancelled",
      nextId,
      42,
    );
    expect(stamped).toHaveLength(2);
    expect(stamped[1]).toEqual({
      id: "stamped",
      role: "assistant",
      content: "",
      timestamp: 42,
      stopReason: "cancelled",
    });
  });

  it("is a no-op (same array) when the trailing turn already names its terminal", () => {
    const stopped = [user("u1"), assistant({ stopReason: "max_turns" })];
    expect(stampTrailingAssistantStop(stopped, "cancelled", nextId)).toBe(
      stopped,
    );
    const failed = [user("u1"), assistant({ failed: true })];
    expect(stampTrailingAssistantStop(failed, "cancelled", nextId)).toBe(
      failed,
    );
  });

  it("leaves an empty transcript empty — there is no turn to describe", () => {
    const empty: AgentMessage[] = [];
    expect(stampTrailingAssistantStop(empty, "cancelled", nextId)).toBe(empty);
  });
});
