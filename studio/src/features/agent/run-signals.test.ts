import { describe, expect, it, vi } from "vitest";
import {
  emitRunFinished,
  onRunFinished,
  RUN_FINISHED_EVENT,
} from "./run-signals";

/** The typed run-finished signal: emit reaches every subscriber with the
 *  session and stop, and an unsubscribed listener hears nothing more. */
describe("run-signals", () => {
  it("delivers the session and stop to subscribers until they unsubscribe", () => {
    const first = vi.fn();
    const second = vi.fn();
    const stopFirst = onRunFinished(first);
    const stopSecond = onRunFinished(second);

    emitRunFinished({ sessionId: "s1", stop: "end_turn" });
    expect(first).toHaveBeenCalledWith({ sessionId: "s1", stop: "end_turn" });
    expect(second).toHaveBeenCalledTimes(1);

    stopFirst();
    emitRunFinished({ sessionId: "s2", stop: "error" });
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(2);
    expect(second).toHaveBeenLastCalledWith({ sessionId: "s2", stop: "error" });
    stopSecond();
  });

  it("rides one named window event and tolerates a detail-less dispatch", () => {
    const raw = vi.fn();
    const typed = vi.fn();
    window.addEventListener(RUN_FINISHED_EVENT, raw);
    const stop = onRunFinished(typed);

    emitRunFinished({ sessionId: "s1", stop: "" });
    expect(raw).toHaveBeenCalledTimes(1);

    // A bare Event under the same name (no detail) still reaches the typed
    // listener with empty fields rather than throwing.
    window.dispatchEvent(new Event(RUN_FINISHED_EVENT));
    expect(typed).toHaveBeenLastCalledWith({ sessionId: "", stop: "" });

    window.removeEventListener(RUN_FINISHED_EVENT, raw);
    stop();
  });
});
