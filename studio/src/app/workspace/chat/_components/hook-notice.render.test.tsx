import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentMessage, HookNotice, ToolCallInfo } from "@/features/agent";
import { memoryStorage } from "@/test/memory-storage";
import { HookChips, HookNoticeLine, HookSection } from "./hook-notice";
import { MessageBubble } from "./message-bubble";
import { ToolCallList } from "./tool-call-list";
import { ToolCallPanel } from "./tool-call-panel";

// The activity list and the panel frame persist preferences in localStorage.
beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

const blocked: HookNotice = {
  phase: "PreToolUse",
  tool: "Shell",
  decision: "blocked",
  text: "rm -rf is not allowed",
};
const modified: HookNotice = {
  phase: "PreToolUse",
  tool: "Shell",
  decision: "modified",
  text: "PreToolUse hook rewrote tool arguments for Shell",
};

const call: ToolCallInfo = {
  callId: "c1",
  name: "Shell",
  input: "cmd: rm -rf /",
  rawArgs: JSON.stringify({ cmd: "rm -rf /" }),
  output: "rm -rf is not allowed",
  isError: true,
  status: "failed",
  hooks: [blocked],
};

/**
 * Hook fires get a decision-coloured surface everywhere a call shows: a chip
 * on the activity row, a Hooks section in the drill-down panel, and a glyph
 * line in the bubble for a call-less lifecycle fire.
 */
describe("HookChips", () => {
  it("renders one chip per fire with the decision label and the full text on hover", () => {
    render(<HookChips hooks={[blocked, modified]} />);
    expect(screen.getByText("Blocked")).toBeInTheDocument();
    expect(screen.getByText("Modified")).toBeInTheDocument();
    expect(
      screen.getByTitle("PreToolUse hook (Shell): rm -rf is not allowed"),
    ).toBeInTheDocument();
    expect(
      screen.getByTitle("PreToolUse hook rewrote tool arguments for Shell"),
    ).toBeInTheDocument();
  });

  it("tints a blocked fire destructive and an advisory one warning", () => {
    render(
      <HookChips
        hooks={[blocked, { ...blocked, decision: "advisory", text: "risky" }]}
      />,
    );
    expect(screen.getByText("Blocked").parentElement).toHaveClass(
      "text-destructive",
    );
    expect(screen.getByText("Advisory").parentElement).toHaveClass(
      "text-warning",
    );
  });

  it("renders nothing for a call no hook touched", () => {
    const { container } = render(<HookChips hooks={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("HookSection", () => {
  it("lists phase, decision and message per fire", () => {
    render(<HookSection hooks={[blocked, { ...modified, text: "" }]} />);
    const section = screen.getByRole("region", { name: "Hooks" });
    expect(section).toBeInTheDocument();
    expect(screen.getByText("rm -rf is not allowed")).toBeInTheDocument();
    // A fire with no message still says what the decision did.
    expect(
      screen.getByText("The hook rewrote the action."),
    ).toBeInTheDocument();
    expect(screen.getAllByText("PreToolUse")).toHaveLength(2);
  });

  it("renders nothing for a call no hook touched", () => {
    const { container } = render(<HookSection hooks={[]} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("HookNoticeLine", () => {
  it("renders the parsed notice with its glyph, tone and a spoken label", () => {
    render(
      <HookNoticeLine text="[hook:blocked] SessionStart hook: no network today" />,
    );
    const text = screen.getByText("SessionStart hook: no network today");
    expect(text.closest("p")).toHaveClass("text-destructive");
    expect(text.closest("p")).toHaveTextContent("✗");
    expect(screen.getByText("Blocked hook:")).toHaveClass("sr-only");
  });

  it("renders nothing for an unmarked notice", () => {
    const { container } = render(<HookNoticeLine text="read 3 of 9 files" />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("hook chips on the activity row", () => {
  it("shows the fire's chip on the expanded row", () => {
    render(<ToolCallList toolCalls={[call]} />);
    fireEvent.click(screen.getByRole("button", { name: /Activity/ }));
    expect(screen.getByTestId("tool-hook-chips")).toBeInTheDocument();
    expect(screen.getByText("Blocked")).toBeInTheDocument();
  });

  it("shows no chip strip on a call no hook touched", () => {
    render(<ToolCallList toolCalls={[{ ...call, hooks: undefined }]} />);
    fireEvent.click(screen.getByRole("button", { name: /Activity/ }));
    expect(screen.queryByTestId("tool-hook-chips")).not.toBeInTheDocument();
  });
});

describe("Hooks section in the drill-down panel", () => {
  const shared = {
    onClose: () => {},
    maximized: false,
    onToggleMaximize: () => {},
  };

  it("lists the call's fires under a Hooks heading", () => {
    render(<ToolCallPanel call={call} {...shared} />);
    expect(screen.getByRole("region", { name: "Hooks" })).toBeInTheDocument();
    expect(screen.getByText("Blocked")).toBeInTheDocument();
  });

  it("omits the section for a call no hook touched", () => {
    render(<ToolCallPanel call={{ ...call, hooks: undefined }} {...shared} />);
    expect(
      screen.queryByRole("region", { name: "Hooks" }),
    ).not.toBeInTheDocument();
  });
});

describe("hook notice in the message bubble", () => {
  const assistant = (partial: Partial<AgentMessage> = {}): AgentMessage => ({
    id: "m1",
    role: "assistant",
    content: "",
    timestamp: 1_755_000_000_000,
    ...partial,
  });

  it("renders a marked hook notice with its glyph and tone instead of a plain line", () => {
    render(
      <MessageBubble
        message={assistant({
          notices: [
            "[hook:advisory] UserPromptSubmit hook: prompt mentions a credential",
            "read 3 of 9 files",
          ],
        })}
      />,
    );
    const hookText = screen.getByText(
      "UserPromptSubmit hook: prompt mentions a credential",
    );
    expect(hookText.closest("p")).toHaveClass("text-warning");
    expect(hookText.closest("p")).toHaveTextContent("⚠");
    // The ordinary notice keeps its plain muted rendering.
    expect(screen.getByText("read 3 of 9 files")).toHaveClass(
      "text-muted-foreground/70",
    );
  });
});
