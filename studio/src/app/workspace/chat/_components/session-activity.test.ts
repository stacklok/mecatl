import { describe, expect, it } from "vitest";
import { sessionActivity } from "./session-activity";

describe("sessionActivity", () => {
  it("is null for an idle session", () => {
    expect(sessionActivity({ isStreaming: false, state: "idle" })).toBeNull();
    expect(sessionActivity({ isStreaming: false })).toBeNull();
    expect(sessionActivity({ state: "completed" })).toBeNull();
  });

  it("reads running from the local streaming flag or the daemon state", () => {
    expect(sessionActivity({ isStreaming: true })).toEqual({
      kind: "running",
      label: "Running",
    });
    expect(sessionActivity({ isStreaming: false, state: "running" })).toEqual({
      kind: "running",
      label: "Running",
    });
  });

  it("reads a parked (awaiting) session as awaiting approval, even while it also streams", () => {
    expect(sessionActivity({ isStreaming: false, state: "awaiting" })).toEqual({
      kind: "awaiting",
      label: "Awaiting approval",
    });
    expect(sessionActivity({ isStreaming: true, state: "awaiting" })).toEqual({
      kind: "awaiting",
      label: "Awaiting approval",
    });
  });
});
