// SPDX-License-Identifier: Apache-2.0

import type { SessionTranscriptResponse } from "@mecatl-studio/contracts";
import { useEffect, useRef, useState } from "react";
import { type ChatMessage, messagesFromTranscript } from "./chat-state";
import { reconcileRecordedMessages } from "./use-delivery-follow";

/** Owns the visible rows across session switches and saved transcript refreshes. */
export function useChatMessages(
  sessionId: string | undefined,
  isRunning: boolean,
  transcript: SessionTranscriptResponse | undefined,
  activeOwner: { current: { sessionId?: string } | undefined },
) {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const loadedTranscriptSession = useRef<string | undefined>(undefined);

  // biome-ignore lint/correctness/useExhaustiveDependencies: only a session switch resets rows; the active owner ref is read at that boundary
  useEffect(() => {
    if (activeOwner.current && activeOwner.current.sessionId === sessionId) return;
    loadedTranscriptSession.current = undefined;
    setMessages([]);
  }, [sessionId]);

  useEffect(() => {
    if (!sessionId || isRunning || !transcript) return;
    const saved = messagesFromTranscript(transcript.messages);
    loadedTranscriptSession.current = sessionId;
    setMessages((current) => {
      // A live authorization park can finish before the first transcript fetch.
      // Its rows still need reconciliation with that snapshot, even when empty.
      return current.length === 0 ? saved : reconcileRecordedMessages(current, saved);
    });
  }, [isRunning, sessionId, transcript]);

  return { loadedTranscriptSession, messages, setMessages };
}
