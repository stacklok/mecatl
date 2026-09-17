import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KEYMAP_STORAGE_KEY } from "@/lib/shortcuts/keymap";
import { ShortcutsProvider, useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { memoryStorage } from "@/test/memory-storage";
import { ApprovalVerdictBar } from "./approval-verdict-bar";

/**
 * The keyboard half of the verdict buttons (the TUI's approval modal foot):
 * focus lands on Allow once when a new ask arrives, ← / → cycle the visible
 * buttons with wrap, the registry's verdict keys and Esc answer the ask from
 * inside the bar (marked consumed, so the global dispatcher never doubles
 * it), and each button shows its key as an aria-hidden keycap.
 */

const ask = { approvalId: "s1:1:c1:r1" };

const button = (name: string) => screen.getByRole("button", { name });
const keyDown = (key: string, init: KeyboardEventInit = {}) =>
  fireEvent.keyDown(document.activeElement ?? document.body, { key, ...init });

afterEach(() => {
  (document.activeElement as HTMLElement | null)?.blur();
});

describe("ApprovalVerdictBar", () => {
  it("focuses Allow once when the ask lands, and moves to the next ask's button too", () => {
    const { rerender } = render(
      <ApprovalVerdictBar approval={ask} onRespond={() => {}} offerAlways />,
    );
    expect(button("Allow once")).toHaveFocus();
    button("Deny").focus();
    rerender(
      <ApprovalVerdictBar
        approval={{ approvalId: "s1:2:c2:r1" }}
        onRespond={() => {}}
        offerAlways
      />,
    );
    expect(button("Allow once")).toHaveFocus();
  });

  it("never steals focus from a text field the operator is typing in", () => {
    render(<textarea aria-label="search" />);
    const field = screen.getByLabelText("search");
    field.focus();
    render(
      <ApprovalVerdictBar approval={ask} onRespond={() => {}} offerAlways />,
    );
    expect(field).toHaveFocus();
  });

  it("does not move focus with autoFocus off", () => {
    render(
      <ApprovalVerdictBar
        approval={ask}
        onRespond={() => {}}
        offerAlways
        autoFocus={false}
      />,
    );
    expect(button("Allow once")).not.toHaveFocus();
  });

  it("cycles → and ← over the visible buttons with wrap, Home/End jump", () => {
    render(
      <ApprovalVerdictBar approval={ask} onRespond={() => {}} offerAlways />,
    );
    expect(button("Allow once")).toHaveFocus();
    keyDown("ArrowRight");
    expect(button("Always allow")).toHaveFocus();
    keyDown("ArrowRight");
    expect(button("Deny")).toHaveFocus();
    keyDown("ArrowRight");
    expect(button("Allow once")).toHaveFocus();
    keyDown("ArrowLeft");
    expect(button("Deny")).toHaveFocus();
    keyDown("Home");
    expect(button("Allow once")).toHaveFocus();
    keyDown("End");
    expect(button("Deny")).toHaveFocus();
  });

  it("skips the withheld Always allow when cycling a two-button bar", () => {
    render(
      <ApprovalVerdictBar
        approval={ask}
        onRespond={() => {}}
        offerAlways={false}
      />,
    );
    expect(screen.queryByRole("button", { name: "Always allow" })).toBeNull();
    keyDown("ArrowRight");
    expect(button("Deny")).toHaveFocus();
    keyDown("ArrowRight");
    expect(button("Allow once")).toHaveFocus();
  });

  it("answers y/a → once, w → always, n/d → deny, Esc → deny from inside the bar", () => {
    const onRespond = vi.fn();
    render(
      <ApprovalVerdictBar approval={ask} onRespond={onRespond} offerAlways />,
    );
    keyDown("y");
    keyDown("a");
    keyDown("w");
    keyDown("n");
    keyDown("d");
    keyDown("Escape");
    expect(onRespond.mock.calls.map(([c]) => c)).toEqual([
      "once",
      "once",
      "always",
      "deny",
      "deny",
      "deny",
    ]);
  });

  it("marks a verdict press consumed so the global dispatcher does not fire it again", () => {
    const onRespond = vi.fn();
    const globalAllow = vi.fn();
    // The same registry id ChatView registers for the main chat's ask: with
    // focus inside the bar the press must answer ONCE, through the bar.
    function Global() {
      useShortcut("approval.allow", globalAllow);
      return null;
    }
    render(
      <ShortcutsProvider>
        <Global />
        <ApprovalVerdictBar approval={ask} onRespond={onRespond} offerAlways />
      </ShortcutsProvider>,
    );
    const notPrevented = keyDown("y");
    expect(notPrevented).toBe(false);
    expect(onRespond).toHaveBeenCalledTimes(1);
    expect(onRespond).toHaveBeenCalledWith("once");
    expect(globalAllow).not.toHaveBeenCalled();
    // With focus outside the bar the same press reaches the dispatcher.
    (document.activeElement as HTMLElement | null)?.blur();
    fireEvent.keyDown(document.body, { key: "y" });
    expect(globalAllow).toHaveBeenCalledTimes(1);
    expect(onRespond).toHaveBeenCalledTimes(1);
  });

  it("ignores w when Always allow is withheld, and leaves modifier chords alone", () => {
    const onRespond = vi.fn();
    render(
      <ApprovalVerdictBar
        approval={ask}
        onRespond={onRespond}
        offerAlways={false}
      />,
    );
    keyDown("w");
    keyDown("y", { metaKey: true });
    keyDown("n", { ctrlKey: true });
    keyDown("d", { altKey: true });
    expect(onRespond).not.toHaveBeenCalled();
    keyDown("n");
    expect(onRespond).toHaveBeenCalledWith("deny");
  });

  it("shows each verdict's key as an aria-hidden keycap and lists every key in aria-keyshortcuts", () => {
    render(
      <ApprovalVerdictBar approval={ask} onRespond={() => {}} offerAlways />,
    );
    const caps = screen.getAllByTestId("verdict-key");
    expect(caps.map((k) => k.textContent)).toEqual(["Y", "W", "N"]);
    for (const cap of caps) expect(cap).toHaveAttribute("aria-hidden", "true");
    // The keycap is out of the accessible name: the buttons keep their labels.
    expect(button("Allow once")).toHaveAttribute("aria-keyshortcuts", "y a");
    expect(button("Always allow")).toHaveAttribute("aria-keyshortcuts", "w");
    expect(button("Deny")).toHaveAttribute("aria-keyshortcuts", "n d");
  });

  it("renders no W hint on a bar without Always allow", () => {
    render(
      <ApprovalVerdictBar
        approval={ask}
        onRespond={() => {}}
        offerAlways={false}
      />,
    );
    expect(
      screen.getAllByTestId("verdict-key").map((k) => k.textContent),
    ).toEqual(["Y", "N"]);
  });

  it("honours a remapped verdict key (Settings → Keyboard) in both the hint and the press", () => {
    vi.stubGlobal("localStorage", memoryStorage());
    window.localStorage.setItem(
      KEYMAP_STORAGE_KEY,
      JSON.stringify({ "approval.deny": "x" }),
    );
    const onRespond = vi.fn();
    render(
      <ApprovalVerdictBar approval={ask} onRespond={onRespond} offerAlways />,
    );
    expect(
      screen.getAllByTestId("verdict-key").map((k) => k.textContent),
    ).toEqual(["Y", "W", "X"]);
    keyDown("n");
    expect(onRespond).not.toHaveBeenCalled();
    keyDown("x");
    expect(onRespond).toHaveBeenCalledWith("deny");
  });

  it("answers a click on each button", () => {
    const onRespond = vi.fn();
    render(
      <ApprovalVerdictBar approval={ask} onRespond={onRespond} offerAlways />,
    );
    fireEvent.click(button("Allow once"));
    fireEvent.click(button("Always allow"));
    fireEvent.click(button("Deny"));
    expect(onRespond.mock.calls.map(([c]) => c)).toEqual([
      "once",
      "always",
      "deny",
    ]);
  });
});
