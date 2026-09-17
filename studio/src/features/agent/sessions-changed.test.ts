import { describe, expect, it, vi } from "vitest";
import {
  notifySessionsChanged,
  onSessionsChanged,
  SESSIONS_CHANGED_EVENT,
} from "./sessions-changed";

/** The typed sessions-changed signal: notify reaches every subscriber,
 *  and an unsubscribed listener hears nothing more. */
describe("sessions-changed", () => {
  it("delivers notify to subscribers until they unsubscribe", () => {
    const first = vi.fn();
    const second = vi.fn();
    const stopFirst = onSessionsChanged(first);
    const stopSecond = onSessionsChanged(second);

    notifySessionsChanged();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);

    stopFirst();
    notifySessionsChanged();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(2);
    stopSecond();
  });

  it("rides one named window event", () => {
    const listener = vi.fn();
    window.addEventListener(SESSIONS_CHANGED_EVENT, listener);
    notifySessionsChanged();
    expect(listener).toHaveBeenCalledTimes(1);
    window.removeEventListener(SESSIONS_CHANGED_EVENT, listener);
  });
});
