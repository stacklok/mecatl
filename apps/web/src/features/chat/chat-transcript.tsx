// SPDX-License-Identifier: Apache-2.0

import { ExternalLink, MessageSquareText } from "lucide-react";
import { memo, useCallback, useRef } from "react";
import type { ChatMessage } from "./chat-state";
import { FailedTurnCard } from "./failed-turn-card";
import { type ChatImage, chatImageDisplay } from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import { ReasoningDisclosure } from "./reasoning-disclosure";
import { hasVisibleStopReason, StopReasonChip } from "./stop-reason-chip";
import { StreamingIndicator } from "./streaming-indicator";
import type { ToolActivity } from "./tool-activity";

export interface TranscriptScrollMetrics {
  clientHeight: number;
  scrollHeight: number;
  scrollTop: number;
}

/** The reader controls following by proximity, not by whether a token arrived. */
export function isNearTranscriptBottom(metrics: TranscriptScrollMetrics): boolean {
  return metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight <= 80;
}

export interface TranscriptRowState {
  message: ChatMessage;
  showToolCalls: boolean;
  streaming: boolean;
}

/** Object identity is stable for historical rows while the active row receives deltas. */
export function shouldUpdateTranscriptRow(
  previous: TranscriptRowState,
  next: TranscriptRowState,
): boolean {
  return (
    previous.message !== next.message ||
    previous.showToolCalls !== next.showToolCalls ||
    previous.streaming !== next.streaming
  );
}

interface TranscriptRowProps extends TranscriptRowState {
  agentName: string;
  onOpenThread?: (message: ChatMessage) => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  threadDisabled: boolean;
  threadSessionId?: string;
  userName: string;
}

function TranscriptRow({
  agentName,
  message,
  onOpenThread,
  onPreviewImage,
  onPreviewTool,
  showToolCalls,
  streaming,
  threadDisabled,
  threadSessionId,
  userName,
}: TranscriptRowProps) {
  const user = message.role === "user";
  const label = user ? userName : agentName;
  const hasContent =
    message.content ||
    message.images?.length ||
    message.reasoning ||
    (showToolCalls && message.tools?.length) ||
    message.failure ||
    hasVisibleStopReason(message.stopReason ?? "") ||
    message.turnStat;
  if (!user && !streaming && !hasContent) return null;

  return (
    <article
      aria-label={`${label} message`}
      className="min-w-0 max-w-full break-words border-b border-border/60 pb-5 last:border-b-0"
      id={`chat-message-${message.id}`}
    >
      <h3 className="mb-2 text-xs font-semibold text-muted-foreground">{label}</h3>
      {message.reasoning && <ReasoningDisclosure streaming={streaming} text={message.reasoning} />}
      {message.images && message.images.length > 0 && (
        <div className="mb-3 grid min-w-0 grid-cols-2 gap-2">
          {message.images.map((image, index) => {
            const display = chatImageDisplay(image);
            return (
              <div className="min-w-0 overflow-hidden rounded-lg border" key={image.id ?? index}>
                {display.kind === "inline" ? (
                  onPreviewImage ? (
                    <button
                      aria-label={`Preview ${image.name}`}
                      className="block w-full"
                      onClick={() => onPreviewImage(image)}
                      type="button"
                    >
                      <img
                        alt={image.name}
                        className="max-h-48 w-full object-contain"
                        src={display.src}
                      />
                    </button>
                  ) : (
                    <img
                      alt={image.name}
                      className="max-h-48 w-full object-contain"
                      src={display.src}
                    />
                  )
                ) : display.kind === "link" ? (
                  <a
                    className="block break-all px-3 py-2 text-sm underline"
                    href={display.href}
                    rel="noreferrer"
                    target="_blank"
                  >
                    {image.name} <ExternalLink aria-hidden="true" className="inline size-3" />
                  </a>
                ) : (
                  <span className="block truncate px-3 py-2 text-sm">{image.name}</span>
                )}
              </div>
            );
          })}
        </div>
      )}
      {message.content ? (
        <MarkdownMessage>{message.content}</MarkdownMessage>
      ) : streaming ? (
        <StreamingIndicator />
      ) : null}
      {showToolCalls && message.tools && message.tools.length > 0 && (
        <ol className="mt-3 space-y-2">
          {message.tools.map((tool) => (
            <li className="min-w-0 rounded-lg border bg-muted/20 p-3 text-xs" key={tool.id}>
              <p className="font-mono font-semibold">Tool: {tool.name}</p>
              <p className="mt-2 text-muted-foreground">Input</p>
              <pre className="mt-1 max-w-full overflow-x-auto whitespace-pre-wrap break-words rounded bg-background p-2 font-mono">
                {tool.args || "{}"}
              </pre>
              {tool.output !== undefined && (
                <>
                  <p className="mt-2 text-muted-foreground">
                    {tool.isError ? "Failed result" : "Result"}
                  </p>
                  <pre className="mt-1 max-w-full overflow-x-auto whitespace-pre-wrap break-words rounded bg-background p-2 font-mono">
                    {tool.output || "No output"}
                  </pre>
                  {onPreviewTool && (
                    <button
                      className="mt-2 underline"
                      onClick={() => onPreviewTool(tool)}
                      type="button"
                    >
                      Open result
                    </button>
                  )}
                </>
              )}
            </li>
          ))}
        </ol>
      )}
      {!streaming && !message.failure && <StopReasonChip stopReason={message.stopReason ?? ""} />}
      {message.failure && (
        <FailedTurnCard
          detail={message.failure.detail}
          permanent={message.failure.permanent}
          summary={message.failure.message}
        />
      )}
      {!streaming && message.turnStat && (
        <p className="mt-2 text-xs tabular-nums text-muted-foreground">{message.turnStat}</p>
      )}
      {onOpenThread && message.content && (
        <button
          aria-label={threadSessionId ? "Open side thread" : "Reply in side thread"}
          className="mt-2 flex items-center gap-1 text-xs text-muted-foreground underline disabled:opacity-50"
          disabled={threadDisabled}
          onClick={() => onOpenThread(message)}
          type="button"
        >
          <MessageSquareText aria-hidden="true" className="size-3" />
          {threadSessionId ? "Open thread" : "Reply in thread"}
        </button>
      )}
    </article>
  );
}

const MemoTranscriptRow = memo(
  TranscriptRow,
  (previous, next) =>
    !shouldUpdateTranscriptRow(previous, next) &&
    previous.agentName === next.agentName &&
    previous.userName === next.userName &&
    previous.threadDisabled === next.threadDisabled &&
    previous.threadSessionId === next.threadSessionId &&
    previous.onOpenThread === next.onOpenThread &&
    previous.onPreviewImage === next.onPreviewImage &&
    previous.onPreviewTool === next.onPreviewTool,
);

export interface ChatTranscriptProps {
  agentName?: string;
  messages: ChatMessage[];
  onOpenThread?: (message: ChatMessage) => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  showToolCalls: boolean;
  streamingMessageId?: string;
  threadDisabled?: boolean;
  threadSessionIdForMessage?: (message: ChatMessage) => string | undefined;
  userName?: string;
}

/** Flat, left-aligned conversation rows; settled rows keep their React identity. */
export function ChatTranscript({
  agentName = "Mecatl",
  messages,
  onOpenThread,
  onPreviewImage,
  onPreviewTool,
  showToolCalls,
  streamingMessageId,
  threadDisabled = false,
  threadSessionIdForMessage,
  userName = "You",
}: ChatTranscriptProps) {
  const actions = useRef({ onOpenThread, onPreviewImage, onPreviewTool });
  actions.current = { onOpenThread, onPreviewImage, onPreviewTool };
  const previewImage = useCallback(
    (image: ChatImage) => actions.current.onPreviewImage?.(image),
    [],
  );
  const previewTool = useCallback(
    (tool: ToolActivity) => actions.current.onPreviewTool?.(tool),
    [],
  );
  const openThread = useCallback(
    (message: ChatMessage) => actions.current.onOpenThread?.(message),
    [],
  );

  return (
    <div className="min-w-0 max-w-full space-y-5">
      {messages.map((message) => (
        <MemoTranscriptRow
          agentName={agentName}
          key={message.id}
          message={message}
          onOpenThread={onOpenThread ? openThread : undefined}
          onPreviewImage={onPreviewImage ? previewImage : undefined}
          onPreviewTool={onPreviewTool ? previewTool : undefined}
          showToolCalls={showToolCalls}
          streaming={message.id === streamingMessageId}
          threadDisabled={threadDisabled}
          threadSessionId={threadSessionIdForMessage?.(message)}
          userName={userName}
        />
      ))}
    </div>
  );
}
