// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import type { SessionTranscriptResponse } from "@mecatl-studio/contracts";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { useChatMessages } from "./use-chat-messages";

afterEach(cleanup);

function savedTranscript(sessionId: string, text: string): SessionTranscriptResponse {
  return {
    complete: true,
    messages: [{ images: [], role: "user", text, toolCalls: [] }],
    sessionId,
  };
}

it("shows a fresh cached transcript when returning to a previously opened chat", () => {
  const owner = { current: undefined };
  const first = savedTranscript("chat-a", "First conversation");
  const second = savedTranscript("chat-b", "Second conversation");
  const { result, rerender } = renderHook(
    ({ sessionId, transcript }) => useChatMessages(sessionId, false, transcript, owner),
    { initialProps: { sessionId: "chat-a", transcript: first } },
  );

  expect(result.current.messages.map((message) => message.content)).toEqual(["First conversation"]);
  rerender({ sessionId: "chat-b", transcript: second });
  expect(result.current.messages.map((message) => message.content)).toEqual([
    "Second conversation",
  ]);
  rerender({ sessionId: "chat-a", transcript: first });
  expect(result.current.messages.map((message) => message.content)).toEqual(["First conversation"]);
});

it("keeps a draft run's live rows when its new session opens", () => {
  const owner: { current: { sessionId?: string } | undefined } = { current: undefined };
  const { result, rerender } = renderHook(
    ({ sessionId }) => useChatMessages(sessionId, true, undefined, owner),
    { initialProps: { sessionId: undefined as string | undefined } },
  );
  act(() => result.current.setMessages([{ content: "Draft prompt", id: "live", role: "user" }]));

  owner.current = { sessionId: "new-chat" };
  rerender({ sessionId: "new-chat" });
  expect(result.current.messages.map((message) => message.content)).toEqual(["Draft prompt"]);
});
