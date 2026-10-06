// SPDX-License-Identifier: Apache-2.0

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ChatStatus, deriveChatStatus } from "./chat-status";

describe("chat run status", () => {
  it("derives status only from known run facts", () => {
    expect(deriveChatStatus({ phase: "sending" })?.label).toBe("Sending");
    expect(deriveChatStatus({ phase: "following", runId: "run-1" })?.label).toBe("Working");
    expect(
      deriveChatStatus({ approvals: [{ askId: "ask-1" }], phase: "following", runId: "run-1" })
        ?.label,
    ).toBe("Waiting for approval");
    expect(
      deriveChatStatus({ phase: "following", runId: "run-1", sessionState: "awaiting" })?.label,
    ).toBe("Waiting for approval");
    expect(
      deriveChatStatus({ phase: "following", runId: "run-1", sessionState: "running" })?.label,
    ).toBe("Working");
    expect(
      deriveChatStatus({ authorizationPending: true, phase: "following", runId: "run-1" })?.label,
    ).toBe("Waiting for authorization");
    expect(deriveChatStatus({ authorizationPending: true, phase: "idle" })?.label).toBe(
      "Waiting for authorization",
    );
    expect(
      deriveChatStatus({ phase: "closed", runId: "run-1", sawResult: true, stopReason: "end_turn" })
        ?.label,
    ).toBe("Completed");
    expect(
      deriveChatStatus({
        phase: "closed",
        runId: "run-1",
        sawResult: true,
        stopReason: "cancelled",
      })?.label,
    ).toBe("Cancelled");
    expect(
      deriveChatStatus({ phase: "closed", runId: "run-1", sawResult: true, stopReason: "budget" })
        ?.label,
    ).toBe("Stopped: token budget");
    expect(
      deriveChatStatus({
        failure: { message: "provider down", permanent: false, prompt: "hi" },
        phase: "closed",
        runId: "run-1",
      })?.label,
    ).toBe("Failed");
    expect(deriveChatStatus({ phase: "closed", runId: "run-1" })?.label).toMatch(/Unknown/);
    expect(deriveChatStatus({ phase: "idle" })).toBeUndefined();

    const html = renderToStaticMarkup(
      <ChatStatus status={deriveChatStatus({ phase: "closed", runId: "run-1" })} />,
    );
    expect(html).toContain('role="status"');
    expect(html).toContain('aria-live="polite"');
    expect(html).toContain("Unknown");
  });
});
