// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { ChatWorkspace } from "../features/chat/chat-workspace";

export const Route = createFileRoute("/workspace/chat")({
  component: ChatPage,
  validateSearch: (search: Record<string, unknown>): { sessionId?: string } => ({
    sessionId: typeof search.sessionId === "string" ? search.sessionId : undefined,
  }),
});

function ChatPage() {
  const { sessionId } = Route.useSearch();
  return <ChatWorkspace sessionId={sessionId} />;
}
