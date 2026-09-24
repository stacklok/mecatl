// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  type AuthorizationHandoff,
  AuthorizationReview,
  recordAuthorizationEvent,
} from "./authorization-review";
import { ChatTranscript } from "./chat-transcript";

afterEach(cleanup);

const pending: AuthorizationHandoff = {
  authorizationId: "auth-1",
  callId: "call-7",
  displayName: "Calendar connector",
  runId: "run-3",
  sessionId: "session-2",
  status: "pending",
};

describe("authorization review", () => {
  it("shows authorization handoff without treating it as an ask verdict", async () => {
    const review = vi.fn();
    const message = {
      content: "",
      id: "assistant-1",
      role: "assistant",
      tools: [{ args: "{}", id: "call-7", name: "Calendar" }],
    };
    const required = recordAuthorizationEvent(
      [message],
      {
        kind: "authorization.required",
        payload: {
          authorizationId: "auth-1",
          callId: "call-7",
          displayName: "Calendar connector",
          status: "pending",
        },
        runId: "run-3",
      },
      "session-2",
      "assistant-1",
    );
    expect(required[0]?.authorizations).toEqual([pending]);
    const transcript = render(
      <ChatTranscript messages={required} onReviewAuthorization={review} showToolCalls />,
    );
    const tool = screen.getByText("Tool: Calendar").closest("li");
    if (!tool) throw new Error("The tool call row is missing");
    expect(within(tool).getByText("Calendar connector")).toBeTruthy();
    expect(within(tool).getByText("Pending authorization")).toBeTruthy();
    fireEvent.click(within(tool).getByRole("button", { name: "Review authorization" }));
    expect(review).toHaveBeenCalledExactlyOnceWith(pending);
    transcript.unmount();

    let finish: (() => void) | undefined;
    const operate = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    const panel = render(<AuthorizationReview authorization={pending} onOperate={operate} />);
    expect(screen.getByText("Calendar connector")).toBeTruthy();
    expect(screen.getByText("Pending authorization")).toBeTruthy();
    expect(screen.getByText(/run-3/)).toBeTruthy();
    expect(screen.getByText(/call-7/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /allow|deny|approve/i })).toBeNull();
    const link = screen.getByRole("link", { name: "Open authorization" });
    expect(link.getAttribute("href")).toBe(
      "/api/v1/sessions/session-2/authorizations/auth-1/presentation",
    );
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(screen.getByText(/opening.*does not grant access/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Recheck" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel authorization" }));
    expect(operate).toHaveBeenCalledExactlyOnceWith("recheck", pending);
    finish?.();

    panel.rerender(
      <AuthorizationReview authorization={{ ...pending, status: "granted" }} onOperate={operate} />,
    );
    const resolved = recordAuthorizationEvent(
      required,
      {
        kind: "authorization.resolved",
        payload: {
          authorizationId: "auth-1",
          callId: "call-7",
          displayName: "Calendar connector",
          status: "granted",
        },
        runId: "",
      },
      "session-2",
      "assistant-1",
    );
    expect(resolved[0]?.authorizations?.[0]?.status).toBe("granted");
    expect(screen.getByText("Access granted")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Open authorization" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Recheck" })).toBeNull();

    for (const [status, label] of [
      ["expired", "Authorization expired"],
      ["cancelled", "Authorization cancelled"],
      ["failed", "Authorization failed"],
    ] as const) {
      panel.rerender(
        <AuthorizationReview authorization={{ ...pending, status }} onOperate={operate} />,
      );
      expect(screen.getByText(label)).toBeTruthy();
    }
  });
});
