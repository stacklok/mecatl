import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ApprovalChoice, ApprovalRequest } from "@/features/agent";
import { ShortcutsProvider } from "@/lib/shortcuts/use-shortcuts";
import { useApprovalShortcuts } from "./use-approval-shortcuts";

/**
 * The app-wide verdict keys for the main chat's head ask, dispatched through
 * the REAL ShortcutsProvider so the registry's `approval.*` bindings are
 * exercised: a/y allow once, w always allow (main-agent asks only), d/n
 * deny — registered only while an ask is waiting, suppressed while typing.
 */

const mainAsk: ApprovalRequest = {
  approvalId: "s1:1:c1:r1",
  sessionId: "s1",
  toolName: "Bash",
  description: "Bash needs your approval.",
  details: "ls -la",
};

function Surface({
  approval,
  onRespond,
  debugSession,
}: {
  approval: ApprovalRequest | null;
  onRespond: (choice: ApprovalChoice) => void;
  debugSession?: boolean;
}) {
  useApprovalShortcuts({ approval, onRespond, debugSession });
  return <textarea aria-label="composer" />;
}

function mount(
  approval: ApprovalRequest | null,
  onRespond = vi.fn(),
  debugSession = false,
) {
  const utils = render(
    <ShortcutsProvider>
      <Surface
        approval={approval}
        onRespond={onRespond}
        debugSession={debugSession}
      />
    </ShortcutsProvider>,
  );
  (document.activeElement as HTMLElement | null)?.blur();
  return { ...utils, onRespond };
}

const press = (key: string) => fireEvent.keyDown(document.body, { key });

describe("useApprovalShortcuts", () => {
  it("gives the TUI's verdicts while a main-agent ask waits", () => {
    const { onRespond } = mount(mainAsk);
    press("y");
    press("a");
    press("w");
    press("n");
    press("d");
    expect(onRespond.mock.calls.map(([c]) => c)).toEqual([
      "once",
      "once",
      "always",
      "deny",
      "deny",
    ]);
  });

  it("claims the press (prevents its default) while an ask waits", () => {
    mount(mainAsk);
    expect(press("y")).toBe(false);
  });

  it("registers nothing with no ask pending — the letters keep their native meaning", () => {
    const { onRespond } = mount(null);
    expect(press("y")).toBe(true);
    expect(press("w")).toBe(true);
    expect(press("n")).toBe(true);
    expect(onRespond).not.toHaveBeenCalled();
  });

  it("withholds Always allow for a child ask (the key does nothing) but still allows and denies", () => {
    const { onRespond } = mount({
      ...mainAsk,
      approvalId: "subagent-x:1:c1:r1",
      child: true,
    });
    press("w");
    expect(onRespond).not.toHaveBeenCalled();
    press("y");
    press("n");
    expect(onRespond.mock.calls.map(([c]) => c)).toEqual(["once", "deny"]);
  });

  it("withholds Always allow for a debugger MCP ask in a debug session", () => {
    const debugAsk: ApprovalRequest = {
      ...mainAsk,
      toolName: "mcp__github__create_issue",
      reason: "debug MCP call requires fresh current operator approval",
    };
    const { onRespond } = mount(debugAsk, vi.fn(), true);
    press("w");
    expect(onRespond).not.toHaveBeenCalled();
    press("a");
    expect(onRespond).toHaveBeenCalledWith("once");
  });

  it("keeps the letters as text while the caret is in a text field", () => {
    const { onRespond } = mount(mainAsk);
    const composer = screen.getByLabelText("composer");
    composer.focus();
    fireEvent.keyDown(composer, { key: "y" });
    fireEvent.keyDown(composer, { key: "n" });
    expect(onRespond).not.toHaveBeenCalled();
  });

  it("stops firing once the ask is answered (re-render with no ask)", () => {
    const onRespond = vi.fn();
    const { rerender } = mount(mainAsk, onRespond);
    press("y");
    expect(onRespond).toHaveBeenCalledTimes(1);
    rerender(
      <ShortcutsProvider>
        <Surface approval={null} onRespond={onRespond} />
      </ShortcutsProvider>,
    );
    expect(press("y")).toBe(true);
    expect(onRespond).toHaveBeenCalledTimes(1);
  });
});
