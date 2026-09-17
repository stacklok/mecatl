import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSession } from "@/features/agent/types";
import { postureSummary } from "@/lib/posture";
import {
  ChatStatusStrip,
  DEBUG_PRIVACY_NOTICE,
  debugServersNotice,
  debugToolsNotice,
} from "./chat-status-strip";

/**
 * The persistent chat status strip (mecatui's header bar). Pins that (1) the
 * handle is the documented bare 12-column literal and a click copies the
 * FULL id, (2) the model segment shows the daemon-RESOLVED model's display
 * name with the provider route and effort, (3) "resolving model…" appears
 * only while the detail read is pending on a live chat (or while
 * connecting) and settles honestly on a failed/absent echo, (4) the mode
 * segment shows a deferred switch as "(pending)", (5) the server segment
 * names managed/external + deployment and never a URL, (6) the posture
 * badge follows the daemon-reported tier (muted / warning / danger, hidden
 * when unreported) with the TUI sentence as its tooltip, and (7) a debug
 * session gets the amber DEBUG target row, the durable privacy line and
 * the reporting-servers sentence.
 */

const runtime = {
  state: "connected" as "connecting" | "connected" | "offline",
  mode: "managed" as "managed" | "external",
  deployment: "",
  serverCapabilities: {} as Record<string, unknown>,
};

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const { copyToClipboard } = vi.hoisted(() => ({
  copyToClipboard: vi.fn(async () => true),
}));
vi.mock("@/lib/clipboard", () => ({ copyToClipboard }));

const session = (over: Partial<AgentSession> = {}): AgentSession => ({
  id: "session-fixture-1",
  title: "Fix the flaky scheduler test",
  projectId: null,
  model: "",
  createdAt: 0,
  updatedAt: 0,
  pinned: false,
  archived: false,
  messageCount: 0,
  isStreaming: false,
  inputTokens: 0,
  outputTokens: 0,
  unread: false,
  estimatedCost: null,
  contextLength: null,
  lastPromptTokens: null,
  thresholdTokens: null,
  ...over,
});

const models = [
  { id: "gpt-5", label: "GPT-5", providerId: "openai" },
  { id: "fixture-model", label: "Fixture model", providerId: "fixture" },
];

beforeEach(() => {
  runtime.state = "connected";
  runtime.mode = "managed";
  runtime.deployment = "";
  runtime.serverCapabilities = {};
  copyToClipboard.mockClear();
});

describe("ChatStatusStrip", () => {
  it("renders the bare 12-column handle and copies the FULL id on click", () => {
    render(
      <ChatStatusStrip
        session={session()}
        live
        resolvedModelId="fixture-model"
        modelResolution="ok"
        models={models}
        mode="default"
      />,
    );
    const handle = screen.getByRole("button", {
      name: "Session session-fixt: copy the full session id",
    });
    expect(handle).toHaveTextContent(/^session-fixt$/);
    expect(handle).toHaveAttribute(
      "title",
      expect.stringContaining("session-fixture-1"),
    );
    fireEvent.click(handle);
    expect(copyToClipboard).toHaveBeenCalledWith(
      "session-fixture-1",
      "Session id",
    );
  });

  it("shows the resolved model's display name with the route and effort, and the mode", () => {
    render(
      <ChatStatusStrip
        session={session()}
        live
        resolvedModelId="gpt-5"
        reasoningEffort="medium"
        modelResolution="ok"
        models={models}
        providerRoute="azure"
        mode="plan"
      />,
    );
    expect(screen.getByTestId("chat-status-model")).toHaveTextContent(
      "GPT-5/azure",
    );
    expect(screen.getByText("medium")).toBeInTheDocument();
    expect(screen.getByTestId("chat-status-mode")).toHaveTextContent(
      "Mode: Plan",
    );
  });

  it("falls back to the raw id when the model is not in the picker list", () => {
    render(
      <ChatStatusStrip
        session={session()}
        live
        resolvedModelId="unlisted-model"
        modelResolution="ok"
        models={models}
      />,
    );
    expect(screen.getByTestId("chat-status-model")).toHaveTextContent(
      "unlisted-model",
    );
    // No mode passed (a read-only chat): the segment is absent, not "Mode:".
    expect(screen.queryByTestId("chat-status-mode")).toBeNull();
  });

  it("says resolving only while the read is pending on a live chat, then settles honestly", () => {
    const { rerender } = render(
      <ChatStatusStrip
        session={session({ model: "configured-id" })}
        live
        resolvedModelId={null}
        modelResolution="loading"
      />,
    );
    const model = () => screen.getByTestId("chat-status-model");
    expect(model()).toHaveTextContent("resolving model…");
    expect(model()).toHaveAttribute("aria-busy", "true");

    // A failed detail read (or an older daemon that echoes nothing) falls
    // back to the inventory row's configured id — never a forever-spinner.
    rerender(
      <ChatStatusStrip
        session={session({ model: "configured-id" })}
        live
        resolvedModelId={null}
        modelResolution="failed"
      />,
    );
    expect(model()).toHaveTextContent("configured-id");
    expect(model()).not.toHaveAttribute("aria-busy");

    // Settled with nothing echoed AND no configured id: say so plainly.
    rerender(
      <ChatStatusStrip
        session={session()}
        live
        resolvedModelId={null}
        modelResolution="ok"
      />,
    );
    expect(model()).toHaveTextContent("model unavailable");

    // Not live: a pending read cannot resolve, so it is not "resolving".
    rerender(
      <ChatStatusStrip
        session={session({ model: "configured-id" })}
        live={false}
        resolvedModelId={null}
        modelResolution="loading"
      />,
    );
    expect(model()).toHaveTextContent("configured-id");
  });

  it("reads resolving and connecting while the daemon connection is still connecting", () => {
    runtime.state = "connecting";
    render(
      <ChatStatusStrip
        session={session()}
        live={false}
        resolvedModelId="gpt-5"
        modelResolution="ok"
        models={models}
      />,
    );
    // Never a stale model while connecting — the TUI's absent-while-connecting.
    expect(screen.getByTestId("chat-status-model")).toHaveTextContent(
      "resolving model…",
    );
    expect(screen.getByTestId("chat-status-server")).toHaveTextContent(
      "connecting…",
    );
  });

  it("marks a deferred mode switch as pending", () => {
    render(
      <ChatStatusStrip
        session={session()}
        live
        resolvedModelId="gpt-5"
        modelResolution="ok"
        mode="default"
        pendingMode="acceptEdits"
      />,
    );
    expect(screen.getByTestId("chat-status-mode")).toHaveTextContent(
      "Mode: Accept edits (pending)",
    );
  });

  it("names managed/external mode and the deployment label, never an address", () => {
    runtime.mode = "external";
    runtime.deployment = "staging-eu";
    render(
      <ChatStatusStrip
        session={session()}
        live
        resolvedModelId="gpt-5"
        modelResolution="ok"
      />,
    );
    const server = screen.getByTestId("chat-status-server");
    expect(server).toHaveTextContent("external daemon · staging-eu");
    expect(server.textContent).not.toMatch(/https?:|localhost|:\d{2,5}/);
  });

  it("renders the posture badge by tone and hides it when the daemon reports none", () => {
    const strip = (posture?: string) => {
      runtime.serverCapabilities = posture === undefined ? {} : { posture };
      return render(
        <ChatStatusStrip
          session={session()}
          live
          resolvedModelId="gpt-5"
          modelResolution="ok"
        />,
      );
    };

    let view = strip("trusted");
    let badge = screen.getByTestId("chat-posture-badge");
    expect(badge).toHaveTextContent("posture trusted");
    expect(badge).toHaveAttribute("data-tone", "muted");
    expect(badge).toHaveAttribute("title", postureSummary("trusted"));
    view.unmount();

    view = strip("auto");
    badge = screen.getByTestId("chat-posture-badge");
    expect(badge).toHaveTextContent("⚠ auto");
    expect(badge).toHaveAttribute("data-tone", "warning");
    view.unmount();

    view = strip("yolo");
    badge = screen.getByTestId("chat-posture-badge");
    expect(badge).toHaveTextContent("⚠ yolo");
    expect(badge).toHaveAttribute("data-tone", "danger");
    expect(badge).toHaveAttribute("title", postureSummary("yolo"));
    view.unmount();

    view = strip("");
    expect(screen.queryByTestId("chat-posture-badge")).toBeNull();
    view.unmount();

    strip(undefined);
    expect(screen.queryByTestId("chat-posture-badge")).toBeNull();
  });

  it("turns amber on a debug session with the target handle, privacy line and reporting servers", () => {
    const onOpenSession = vi.fn();
    render(
      <ChatStatusStrip
        session={session({
          id: "debug-9f3a",
          debugTargetSessionId: "session-fixture-1",
        })}
        live
        resolvedModelId="gpt-5"
        modelResolution="ok"
        debugMcpServers={["github", "slack"]}
        onOpenSession={onOpenSession}
      />,
    );
    const strip = screen.getByTestId("chat-status-strip");
    expect(strip).toHaveAttribute("data-debug", "true");
    expect(strip.className).toContain("amber");
    expect(screen.getByText("DEBUG")).toBeInTheDocument();

    const target = screen.getByRole("button", {
      name: "Open debug target session session-fixture-1",
    });
    expect(target).toHaveTextContent("session-fixt");
    expect(target).toHaveAttribute("title", "session-fixture-1");
    fireEvent.click(target);
    expect(onOpenSession).toHaveBeenCalledWith("session-fixture-1");

    const privacy = screen.getByTestId("chat-debug-privacy");
    expect(privacy).toHaveTextContent(DEBUG_PRIVACY_NOTICE);
    expect(privacy).toHaveTextContent(debugServersNotice(["github", "slack"]));
    expect(privacy).toHaveTextContent(
      "does not authorize publication or sending",
    );
  });

  it("omits the reporting-servers sentence when none are bound, and the whole row on an ordinary chat", () => {
    const { unmount } = render(
      <ChatStatusStrip
        session={session({ id: "debug-1", debugTargetSessionId: "target-1" })}
        live
        resolvedModelId="gpt-5"
        modelResolution="ok"
      />,
    );
    const privacy = screen.getByTestId("chat-debug-privacy");
    expect(privacy).toHaveTextContent(DEBUG_PRIVACY_NOTICE);
    expect(privacy).not.toHaveTextContent("reporting servers");
    unmount();

    render(
      <ChatStatusStrip
        session={session()}
        live
        resolvedModelId="gpt-5"
        modelResolution="ok"
      />,
    );
    expect(screen.queryByTestId("chat-debug-privacy")).toBeNull();
    expect(screen.queryByText("DEBUG")).toBeNull();
    expect(screen.getByTestId("chat-status-strip")).not.toHaveAttribute(
      "data-debug",
    );
  });

  it("names the mounted debugger MCP tools and that each call asks anew", () => {
    const { unmount } = render(
      <ChatStatusStrip
        session={session({ id: "debug-1", debugTargetSessionId: "target-1" })}
        live
        resolvedModelId="gpt-5"
        modelResolution="ok"
        debugMcpServers={["github"]}
        debugMcpTools={["mcp__github__create_issue"]}
      />,
    );
    const privacy = screen.getByTestId("chat-debug-privacy");
    expect(privacy).toHaveTextContent(
      debugToolsNotice(["mcp__github__create_issue"]),
    );
    expect(privacy).toHaveTextContent("Always allow is not learned");
    unmount();

    render(
      <ChatStatusStrip
        session={session({ id: "debug-1", debugTargetSessionId: "target-1" })}
        live
        resolvedModelId="gpt-5"
        modelResolution="ok"
        debugMcpServers={["github"]}
      />,
    );
    expect(screen.getByTestId("chat-debug-privacy")).not.toHaveTextContent(
      "Debugger MCP tools",
    );
  });
});
