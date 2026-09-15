import { ChatWorkspace } from "../_components/chat-workspace";

/**
 * `/workspace/chat` (a draft with no daemon session yet) and
 * `/workspace/chat/<sessionId>` are served by one optional-catch-all route,
 * so moving between them keeps `ChatWorkspace` mounted — a draft's in-flight
 * stream survives the router.replace to its freshly minted session id.
 */
export default async function ChatPage({
  params,
}: {
  params: Promise<{ sessionId?: string[] }>;
}) {
  const { sessionId } = await params;
  return <ChatWorkspace sessionId={sessionId?.[0]} />;
}
