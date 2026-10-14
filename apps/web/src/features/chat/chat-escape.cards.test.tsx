// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * `useChatEscape` on a surface that shows approval cards, as the chat
 * workspace and the side thread do: every pending ask has its card, and the
 * surface's active ask, the one Escape answers, is the first. The cards are
 * the real ones, so what they mark in the DOM reaches the hook as it does in
 * the app.
 */

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { ApprovalPanel, type ApprovalRequest } from "./approval-panel";
import { useChatEscape } from "./chat-escape";
import { EscapeHintContext } from "./escape-hint-context";
import { PlanReviewCard } from "./plan-review-card";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function ask(askId: string, reason: string, tool = "Read"): ApprovalRequest {
  return {
    args: tool === "PresentPlan" ? '{"plan":"Ship it"}' : '{"path":"notes.txt"}',
    askId,
    controlTarget: { askId, runId: "run-a", sessionId: "chat-a" },
    reason,
    tool,
  };
}

const first = ask("ask-1", "First ask");
const second = ask("ask-2", "Second ask");
const firstPlan = ask("plan-1", "First plan", "PresentPlan");

/**
 * A chat surface in miniature: the pending asks' cards, a side panel that
 * isn't an approval (a canvas, say), and the composer. It records each Escape
 * action and which ask it answered.
 */
function Surface({
  asks = [first, second],
  panelOpen = true,
  runActive = true,
}: {
  asks?: ApprovalRequest[];
  panelOpen?: boolean;
  runActive?: boolean;
}) {
  const [pending, setPending] = useState(asks);
  const [panel, setPanel] = useState(panelOpen);
  const [running, setRunning] = useState(runActive);
  const [draft, setDraft] = useState("Unsent draft");
  const [menu, setMenu] = useState(false);
  const [hint, setHint] = useState(false);
  const [actions, setActions] = useState<string[]>([]);
  const record = (action: string) => setActions((current) => [...current, action]);
  const active = pending[0];
  const settle = () => setPending((current) => current.slice(1));
  useChatEscape({
    active: true,
    askAvailable: true,
    draft: Boolean(draft),
    onClearDraft: () => {
      record("clear");
      setDraft("");
    },
    onClosePanel: () => {
      record("panel");
      setPanel(false);
    },
    onDenyAsk: () => {
      record(`deny ${active?.askId}`);
      settle();
    },
    onHintChange: setHint,
    onIteratePlan: () => {
      record(`iterate ${active?.askId}`);
      settle();
    },
    onStopRun: () => {
      record("stop");
      setRunning(false);
    },
    onUnavailablePlan: () => record(`unavailable ${active?.askId}`),
    overlayOpen: menu,
    panelOpen: panel,
    pendingAsk: !active ? "none" : active.tool === "PresentPlan" ? "plan" : "ordinary",
    planAvailable: true,
    runActive: running,
  });
  return (
    <main>
      <EscapeHintContext.Provider value={active}>
        {pending.map((approval) =>
          approval.tool === "PresentPlan" ? (
            <PlanReviewCard
              approval={approval}
              disabled={false}
              key={approval.askId}
              onRespond={() => record(`click ${approval.askId}`)}
            />
          ) : (
            <ApprovalPanel
              approval={approval}
              disabled={false}
              key={approval.askId}
              onRespond={() => record(`click ${approval.askId}`)}
            />
          ),
        )}
      </EscapeHintContext.Provider>
      {panel && (
        <aside aria-label="Canvas">
          <button type="button">Canvas control</button>
        </aside>
      )}
      <button onClick={() => setMenu((open) => !open)} type="button">
        Toggle menu
      </button>
      {menu && (
        <div data-state="open" role="menu">
          <button type="button">Menu item</button>
        </div>
      )}
      <p>Selectable text</p>
      <textarea
        aria-label="Composer"
        onChange={(event) => setDraft(event.target.value)}
        value={draft}
      />
      <output aria-label="Escape actions">{actions.join(",")}</output>
      <p>{hint ? "Press Escape again to clear draft" : "No hint"}</p>
    </main>
  );
}

function keyDown(init: KeyboardEventInit = {}) {
  fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape", ...init });
}

function keyUp() {
  fireEvent.keyUp(document.activeElement ?? document.body, { key: "Escape" });
}

/** One full Escape press from the focused element: down, then released. */
function pressEscape(init: KeyboardEventInit = {}) {
  keyDown(init);
  keyUp();
}

function actions() {
  return screen.getByRole("status", { name: "Escape actions" }).textContent;
}

function card(reason: string): HTMLElement {
  return screen.getByText(reason).closest("section") as HTMLElement;
}

function composer(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "Composer" });
}

afterEach(() => {
  cleanup();
  window.getSelection()?.removeAllRanges();
});

describe("chat Escape with approval cards on the surface", () => {
  it("denies the active ask from its own card, the composer, a side panel, or the page", () => {
    const targets: Array<() => HTMLElement | undefined> = [
      () => within(card("First ask")).getByRole("button", { name: "Deny" }),
      () => within(card("First ask")).getByRole("button", { name: "Raw arguments" }),
      composer,
      () => screen.getByRole("button", { name: "Canvas control" }),
      // Nothing focused: the press comes from the page body.
      () => undefined,
    ];
    for (const target of targets) {
      render(<Surface />);
      target()?.focus();
      pressEscape();
      expect(actions()).toBe("deny ask-1");
      expect(screen.queryByText("First ask")).toBeNull();
      expect(screen.getByText("Second ask")).toBeTruthy();
      cleanup();
    }
  });

  it("iterates the active plan from its own card, the composer, or the page", () => {
    const targets: Array<() => HTMLElement | undefined> = [
      () => within(card("First plan")).getByRole("button", { name: "Iterate" }),
      composer,
      () => undefined,
    ];
    for (const target of targets) {
      render(<Surface asks={[firstPlan, second]} />);
      expect(within(card("First plan")).getByText("Esc to Iterate")).toBeTruthy();
      target()?.focus();
      pressEscape();
      expect(actions()).toBe("iterate plan-1");
      cleanup();
    }
  });

  it("takes one layer per press: menu, selection, active ask, panel, run, then the draft", () => {
    render(<Surface asks={[first]} />);
    within(card("First ask")).getByRole("button", { name: "Deny" }).focus();

    // An open menu owns the press; the surface leaves it alone.
    fireEvent.click(screen.getByRole("button", { name: "Toggle menu" }));
    pressEscape();
    expect(actions()).toBe("");
    fireEvent.click(screen.getByRole("button", { name: "Toggle menu" }));
    expect(screen.queryByRole("menu")).toBeNull();

    // A text selection is cleared before any ask is answered.
    const range = document.createRange();
    range.selectNodeContents(screen.getByText("Selectable text"));
    window.getSelection()?.addRange(range);
    pressEscape();
    expect(window.getSelection()?.isCollapsed).toBe(true);
    expect(actions()).toBe("");

    // Then the active ask, the side panel, and the run, one per press.
    pressEscape();
    expect(actions()).toBe("deny ask-1");
    pressEscape();
    expect(actions()).toBe("deny ask-1,panel");
    pressEscape();
    expect(actions()).toBe("deny ask-1,panel,stop");

    // Last, the unsent draft: one press arms, a second released press clears.
    pressEscape();
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    expect(actions()).toBe("deny ask-1,panel,stop");
    pressEscape();
    expect(actions()).toBe("deny ask-1,panel,stop,clear");
    expect(composer().value).toBe("");
  });

  it("acts on no held Escape: not the plan, the active ask, the panel, or the run", () => {
    render(<Surface asks={[firstPlan, first]} />);
    within(card("First plan")).getByRole("button", { name: "Iterate" }).focus();
    pressEscape({ repeat: true });
    expect(actions()).toBe("");
    // The press itself acts once; held down, it repeats and acts on nothing more.
    keyDown();
    expect(actions()).toBe("iterate plan-1");
    within(card("First ask")).getByRole("button", { name: "Deny" }).focus();
    keyDown({ repeat: true });
    keyDown();
    expect(actions()).toBe("iterate plan-1");
    keyUp();

    // From the active ask's own card, a repeat denies nothing.
    pressEscape({ repeat: true });
    expect(actions()).toBe("iterate plan-1");
    pressEscape();
    expect(actions()).toBe("iterate plan-1,deny ask-1");

    pressEscape({ repeat: true });
    expect(actions()).toBe("iterate plan-1,deny ask-1");
    pressEscape();
    expect(actions()).toBe("iterate plan-1,deny ask-1,panel");

    pressEscape({ repeat: true });
    expect(actions()).toBe("iterate plan-1,deny ask-1,panel");
    pressEscape();
    expect(actions()).toBe("iterate plan-1,deny ask-1,panel,stop");

    pressEscape({ repeat: true });
    expect(screen.getByText("No hint")).toBeTruthy();
    expect(actions()).toBe("iterate plan-1,deny ask-1,panel,stop");
  });

  it("leaves an Escape that ends an IME composition to the input method", () => {
    render(<Surface asks={[first]} />);
    composer().focus();
    pressEscape({ isComposing: true });
    expect(actions()).toBe("");
    pressEscape();
    expect(actions()).toBe("deny ask-1");
  });

  it("clears an unsent draft from the composer only on a released second press", () => {
    render(<Surface asks={[]} panelOpen={false} runActive={false} />);
    composer().focus();
    keyDown();
    expect(screen.getByText("Press Escape again to clear draft")).toBeTruthy();
    // Still held: the second keydown is the same press.
    keyDown();
    expect(composer().value).toBe("Unsent draft");
    keyUp();
    pressEscape();
    expect(composer().value).toBe("");
    expect(actions()).toBe("clear");
    expect(screen.getByText("No hint")).toBeTruthy();
  });
});
