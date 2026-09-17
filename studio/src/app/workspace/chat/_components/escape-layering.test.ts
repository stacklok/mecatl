import { describe, expect, it } from "vitest";
import {
  DISARMED_ESCAPE,
  ESCAPE_ARM_MS,
  type EscapeState,
  pressEscapeToClear,
  resolveEscapeAction,
} from "./escape-layering";

/**
 * Esc resolves to exactly one action, in the TUI's layering: a transcript
 * selection drops first, then an open side panel closes, then a streaming
 * run is cancelled, then an unsent draft is cleared; with nothing to do it
 * is a no-op. An Esc pressed to drop a selection can therefore never stop a
 * run.
 */

const none: EscapeState = {
  hasSelection: false,
  panelOpen: false,
  isStreaming: false,
  hasDraft: false,
};

describe("resolveEscapeAction", () => {
  it("does nothing when there is nothing to do", () => {
    expect(resolveEscapeAction(none)).toBe("none");
  });

  it("resolves each layer alone", () => {
    expect(resolveEscapeAction({ ...none, hasSelection: true })).toBe(
      "clear-selection",
    );
    expect(resolveEscapeAction({ ...none, panelOpen: true })).toBe(
      "close-panel",
    );
    expect(resolveEscapeAction({ ...none, isStreaming: true })).toBe(
      "cancel-run",
    );
    expect(resolveEscapeAction({ ...none, hasDraft: true })).toBe(
      "clear-draft",
    );
  });

  it("selection beats panel beats run beats draft", () => {
    const all: EscapeState = {
      hasSelection: true,
      panelOpen: true,
      isStreaming: true,
      hasDraft: true,
    };
    expect(resolveEscapeAction(all)).toBe("clear-selection");
    expect(resolveEscapeAction({ ...all, hasSelection: false })).toBe(
      "close-panel",
    );
    expect(
      resolveEscapeAction({ ...all, hasSelection: false, panelOpen: false }),
    ).toBe("cancel-run");
    expect(
      resolveEscapeAction({
        ...all,
        hasSelection: false,
        panelOpen: false,
        isStreaming: false,
      }),
    ).toBe("clear-draft");
  });

  it("never cancels a run while a selection is up, even mid-stream", () => {
    expect(
      resolveEscapeAction({ ...none, hasSelection: true, isStreaming: true }),
    ).toBe("clear-selection");
  });

  // The TUI's Esc in the approval modal: a run parked on an ask is denied,
  // never cancelled — even with a side panel open (the expanded ask view is
  // one) and the run counted as streaming (`waiting_approval` folds into
  // isStreaming). The gap this closes: Esc used to stop the whole run.
  it("denies a pending ask ahead of the panel, the run and the draft", () => {
    expect(
      resolveEscapeAction({
        ...none,
        pendingAsk: true,
        panelOpen: true,
        isStreaming: true,
        hasDraft: true,
      }),
    ).toBe("deny-ask");
    expect(resolveEscapeAction({ ...none, pendingAsk: true })).toBe("deny-ask");
  });

  it("still drops a selection before denying — a verdict is never given by accident", () => {
    expect(
      resolveEscapeAction({ ...none, hasSelection: true, pendingAsk: true }),
    ).toBe("clear-selection");
  });

  it("treats an absent pendingAsk as no ask", () => {
    expect(resolveEscapeAction({ ...none, isStreaming: true })).toBe(
      "cancel-run",
    );
    expect(
      resolveEscapeAction({ ...none, pendingAsk: false, panelOpen: true }),
    ).toBe("close-panel");
  });
});

/**
 * The double-Esc clear machine (the TUI's "esc esc"): first press arms for
 * ESCAPE_ARM_MS, a second inside the window clears and disarms, a press
 * after the window only re-arms. Pure over an injected clock.
 */
describe("pressEscapeToClear", () => {
  const t0 = 10_000;

  it("arms on a first press without clearing", () => {
    const { state, clear } = pressEscapeToClear(DISARMED_ESCAPE, t0);
    expect(clear).toBe(false);
    expect(state).toEqual({ armedUntil: t0 + ESCAPE_ARM_MS });
  });

  it("clears and disarms on a second press inside the window", () => {
    const armed = pressEscapeToClear(DISARMED_ESCAPE, t0).state;
    const { state, clear } = pressEscapeToClear(armed, t0 + ESCAPE_ARM_MS - 1);
    expect(clear).toBe(true);
    expect(state).toEqual(DISARMED_ESCAPE);
    // The clear consumed the arm: a third press starts over.
    expect(pressEscapeToClear(state, t0 + ESCAPE_ARM_MS).clear).toBe(false);
  });

  it("re-arms instead of clearing once the window has lapsed", () => {
    const armed = pressEscapeToClear(DISARMED_ESCAPE, t0).state;
    const late = t0 + ESCAPE_ARM_MS;
    const { state, clear } = pressEscapeToClear(armed, late);
    expect(clear).toBe(false);
    expect(state).toEqual({ armedUntil: late + ESCAPE_ARM_MS });
  });

  it("pins the window to the TUI's 1.5 s", () => {
    expect(ESCAPE_ARM_MS).toBe(1500);
  });
});
