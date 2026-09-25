// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useChatEscape } from "./chat-escape";

function pressEscape(target: HTMLElement = document.body, repeat = false) {
  target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape", repeat }));
}
function releaseEscape(target: HTMLElement = document.body) {
  target.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "Escape" }));
}

function Harness({
  ask = "ordinary",
  available = true,
  actionable = true,
  navigationKey = "chat-a",
}: {
  ask?: "ordinary" | "plan" | "none";
  available?: boolean;
  actionable?: boolean;
  navigationKey?: string;
}) {
  const [pending, setPending] = useState(ask);
  const [panel, setPanel] = useState(true);
  const [running, setRunning] = useState(true);
  const [draft, setDraft] = useState("unsent");
  const [overlay, setOverlay] = useState(false);
  const [actions, setActions] = useState<string[]>([]);
  const [hint, setHint] = useState(false);
  const record = (action: string) => setActions((current) => [...current, action]);
  useChatEscape({
    active: true,
    askAvailable: actionable,
    draft: Boolean(draft),
    onClearDraft: () => {
      record("clear");
      setDraft("");
    },
    onDenyAsk: () => {
      record("deny");
      setPending("none");
    },
    onIteratePlan: () => {
      record("iterate");
      setPending("none");
    },
    onClosePanel: () => {
      record("panel");
      setPanel(false);
    },
    onStopRun: () => {
      record("stop");
      setRunning(false);
    },
    onUnavailablePlan: () => record("unavailable"),
    navigationKey,
    onHintChange: setHint,
    overlayOpen: overlay,
    pendingAsk: pending,
    panelOpen: panel,
    planAvailable: available,
    runActive: running,
  });
  return (
    <main>
      <button onClick={() => setOverlay(true)} type="button">
        Open overlay
      </button>
      <button onClick={() => setOverlay(false)} type="button">
        Close overlay
      </button>
      <output>{actions.join(",")}</output>
      <p>{hint ? "Press Escape again to clear draft" : "No hint"}</p>
      <p>Selectable text</p>
      {overlay && (
        <div
          data-state="open"
          role="menu"
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.stopPropagation();
              setOverlay(false);
              document.querySelector<HTMLButtonElement>("button")?.focus();
            }
          }}
        >
          <button type="button">Menu item</button>
        </div>
      )}
      <textarea
        aria-label="Draft"
        onChange={(event) => setDraft(event.target.value)}
        value={draft}
      />
    </main>
  );
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("chat Escape", () => {
  it("closes the focused overlay before touching a pending ask", () => {
    render(<Harness />);
    act(() => screen.getByRole("button", { name: "Open overlay" }).click());
    act(() => pressEscape());
    expect(screen.getByRole("status").textContent).toBe("");
    act(() => releaseEscape());
    const item = screen.getByRole("button", { name: "Menu item" });
    item.focus();
    act(() => pressEscape(item));
    expect(screen.getByRole("status").textContent).toBe("");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Open overlay" }));
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByRole("status").textContent).toBe("deny");
    cleanup();
    render(<Harness />);
    const draft = screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Draft" });
    draft.focus();
    draft.setSelectionRange(0, draft.value.length);
    act(() => pressEscape(draft));
    expect(draft.selectionStart).toBe(draft.selectionEnd);
    expect(screen.getByRole("status").textContent).toBe("");
  });

  it("takes exactly one Escape action per key press", () => {
    render(<Harness ask="plan" />);
    act(() => pressEscape());
    expect(screen.getByRole("status").textContent).toBe("iterate");
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByRole("status").textContent).toBe("iterate,panel");
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByRole("status").textContent).toBe("iterate,panel,stop");
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("iterate,panel,stop");

    cleanup();
    render(<Harness ask="plan" available={false} />);
    act(() => pressEscape());
    expect(screen.getByRole("status").textContent).toBe("unavailable");
    cleanup();
    render(<Harness actionable={false} />);
    act(() => pressEscape());
    expect(screen.getByRole("status").textContent).toBe("");
    cleanup();
    render(<Harness />);
    const range = document.createRange();
    range.selectNodeContents(screen.getByText("Selectable text"));
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);
    act(() => pressEscape());
    expect(window.getSelection()?.isCollapsed).toBe(true);
    expect(screen.getByRole("status").textContent).toBe("");
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByRole("status").textContent).toBe("deny");
  });

  it("requires a released second Escape to clear an unsent draft", () => {
    vi.useFakeTimers();
    render(<Harness ask="none" />);
    act(() => pressEscape());
    act(() => {
      releaseEscape();
      pressEscape();
    });
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    act(() => pressEscape(document.body, true));
    expect(screen.getByRole("status").textContent).toBe("panel,stop");
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByRole("status").textContent).toBe("panel,stop,clear");
  });

  it("expires the visible draft hint without another press", () => {
    vi.useFakeTimers();
    render(<Harness ask="none" />);
    act(() => pressEscape());
    act(() => {
      releaseEscape();
      pressEscape();
    });
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    act(() => vi.advanceTimersByTime(501));
    expect(screen.getByText("No hint")).toBeTruthy();
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByRole("status").textContent).toBe("panel,stop");
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
  });

  it("disarms the clear hint on edit, focus change, composition, and a new overlay", () => {
    render(<Harness ask="none" />);
    act(() => pressEscape());
    act(() => {
      releaseEscape();
      pressEscape();
    });
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox", { name: "Draft" }), {
      target: { value: "edited" },
    });
    expect(screen.getByText("No hint")).toBeTruthy();
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    fireEvent.focusIn(screen.getByRole("button", { name: "Open overlay" }));
    expect(screen.getByText("No hint")).toBeTruthy();
    act(() => {
      releaseEscape();
      pressEscape();
    });
    fireEvent.compositionStart(screen.getByRole("textbox", { name: "Draft" }));
    expect(screen.getByText("No hint")).toBeTruthy();
    act(() => screen.getByRole("button", { name: "Open overlay" }).click());
    expect(screen.getByText("No hint")).toBeTruthy();
  });

  it("disarms an armed draft when the viewed chat changes", () => {
    const view = render(<Harness ask="none" />);
    act(() => pressEscape());
    act(() => {
      releaseEscape();
      pressEscape();
    });
    act(() => {
      releaseEscape();
      pressEscape();
    });
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    view.rerender(<Harness ask="none" navigationKey="chat-b" />);
    expect(screen.getByText("No hint")).toBeTruthy();
  });
});
