import { type ChatSeedParams, resolveChatSeed } from "@/lib/chat-seed";
import { ChatWorkspace } from "../_components/chat-workspace";

/**
 * `/workspace/chat` (a draft with no daemon session yet) and
 * `/workspace/chat/<sessionId>` are served by one optional-catch-all route,
 * so moving between them keeps `ChatWorkspace` mounted — a draft's in-flight
 * stream survives the router.replace to its freshly minted session id.
 *
 * Deep link (the web analogue of `mecatui -p/--prompt-file`):
 * `?prompt=<text>` arrives with the composer pre-filled; `&send=1` adds a
 * one-click confirmation showing the exact text, after which it is sent as
 * the next turn and the chat stays interactive. The PWA share target
 * (`app/manifest.ts`) lands here as `prompt`/`title`/`url`, prefill-only.
 * The workspace strips the query once it has consumed it, so a reload or
 * Back never re-seeds. See `lib/chat-seed.ts` for the rules.
 */
export default async function ChatPage({
  params,
  searchParams,
}: {
  params: Promise<{ sessionId?: string[] }>;
  searchParams: Promise<ChatSeedParams>;
}) {
  const [{ sessionId }, query] = await Promise.all([params, searchParams]);
  return (
    <ChatWorkspace sessionId={sessionId?.[0]} seed={resolveChatSeed(query)} />
  );
}
